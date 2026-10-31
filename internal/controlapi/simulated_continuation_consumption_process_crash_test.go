//go:build !windows

package controlapi_test

// This crash is BEFORE durable consumption, unlike the business-transaction
// crash. Staged consumption is rolled back; only an explicit Core withdrawal,
// never a missing receipt or this test's SQL oracle, authorizes a fresh choice.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

func TestSimulatedContinuationConsumptionProcessCrash(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseConsumptionProcessCrash)
}

type consumptionProcessCrashSnapshot struct {
	Ledger                                               submissionWithdrawalSnapshot
	WorkerEpoch, IntentsJSON, BindingsJSON, ReleasesJSON string
	ReplacementReceipts                                  int
}

// Each read transaction is internally consistent. These sequential snapshots
// are not a global atomic workspace snapshot; no command runs between them.
func (s *continuationGate) consumptionCrashSnapshot(db *sql.DB, in c.CommitInput) consumptionProcessCrashSnapshot {
	s.t.Helper()
	out := consumptionProcessCrashSnapshot{Ledger: s.submissionWithdrawalSnapshotFromDB(db, in)}
	tx, err := db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	queries := []struct {
		sql  string
		args []any
		dest []any
	}{
		{`SELECT worker_epoch FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, []any{in.IntentID, in.AISessionID}, []any{&out.WorkerEpoch}},
		{`SELECT json_group_array(json_object('id',intent_id,'prepareRequest',prepare_request_id,'prepareHash',prepare_hash,'worker',worker_epoch,'intent',intent_json)) FROM (SELECT * FROM confirmation_intents WHERE ai_session_id=? ORDER BY intent_id)`, []any{in.AISessionID}, []any{&out.IntentsJSON}},
		{`SELECT json_group_array(json_object('intent',intent_id,'task',task_id,'generation',generation)) FROM (SELECT * FROM task_action_confirmation_bindings WHERE task_id=? ORDER BY intent_id)`, []any{s.f.task}, []any{&out.BindingsJSON}},
		{`SELECT json_group_array(json_object('intent',intent_id,'task',task_id,'generation',generation,'reason',reason)) FROM (SELECT * FROM task_action_releases WHERE task_id=? ORDER BY intent_id)`, []any{s.f.task}, []any{&out.ReleasesJSON}},
		{`SELECT COUNT(*) FROM confirmation_replacement_receipts WHERE ai_session_id=?`, []any{in.AISessionID}, []any{&out.ReplacementReceipts}},
	}
	for _, q := range queries {
		if err = tx.QueryRowContext(s.ctx, q.sql, q.args...).Scan(q.dest...); err != nil {
			s.t.Fatal(err)
		}
	}
	return out
}

func (s *continuationGate) checkCrashedUnobserved(got, pending c.Intent, in c.CommitInput) {
	s.t.Helper()
	s.check(got.IntentID == in.IntentID && got.AISessionID == in.AISessionID && got.Version == in.Version && got.ContentHash == in.ContentHash && got.State == "PENDING" && got.Commit == nil && len(got.Steps) == 0 && !got.CanResume && got.Purpose == pending.Purpose && got.Action == pending.Action && reflect.DeepEqual(got.Content, pending.Content), "pre-consumption crash preserves original pending proposal, not a consumed unfinished choice", got)
	sub := got.Submission
	s.check(sub != nil && !sub.Recorded && sub.Outcome == c.Unknown && sub.Consumed == nil && sub.BusinessOutcome == "OUTCOME_UNKNOWN" && sub.ConsumptionDisposition == "UNKNOWN" && sub.NextAction == c.ReadOriginalReceipt && sub.RetryDisposition == c.RetryReadOriginalReceipt && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.PayloadSHA256 == "" && sub.ReceiptRequestID == "" && sub.Request.RequestID == in.RequestID, "missing original receipt after actual dispatch/crash is not non-consumption or retry authority", sub)
}

// Compare every prior raw Session receipt, not just the count of this intent's
// requests. Then independently validate the single new immutable request row.
func (s *continuationGate) checkOneAddedCrashReceipt(before, after string, in c.CommitInput, outcome, failureCode, failureStage string) {
	s.t.Helper()
	var prior, current []json.RawMessage
	gateDecode(s.t, json.RawMessage(before), &prior)
	gateDecode(s.t, json.RawMessage(after), &current)
	remaining := []json.RawMessage{}
	added := 0
	for _, raw := range current {
		var row struct {
			Session, RequestID, IntentID, Submitted, Request, Outcome string
			Failure                                                   *string
		}
		gateDecode(s.t, raw, &row)
		if row.RequestID != in.RequestID {
			remaining = append(remaining, raw)
			continue
		}
		added++
		var request c.CommitInput
		gateDecode(s.t, json.RawMessage(row.Request), &request)
		s.check(row.Session == in.AISessionID && row.IntentID == in.IntentID && row.Submitted == c.ValueHash(in) && reflect.DeepEqual(request, in) && row.Outcome == outcome, "added durable receipt retains exact originally submitted identity and outcome", row)
		if outcome == "REJECTED_NOT_CONSUMED" {
			s.check(row.Failure != nil, "rejected request has durable negative authority rather than absence", row)
			var failure c.Failure
			gateDecode(s.t, json.RawMessage(*row.Failure), &failure)
			s.check(failure.Code == failureCode && failure.Stage == failureStage, "rejection receipt retains exact unnormalized durable failure origin", failure)
		} else {
			s.check(row.Failure == nil, "fresh consumed receipt has no withdrawal failure", row)
		}
	}
	s.check(added == 1 && reflect.DeepEqual(remaining, prior), "one new receipt is added without rewriting or deleting any prior Session receipt", nil)
}

func (s *continuationGate) exerciseConsumptionProcessCrash() {
	s.exerciseConsumptionCrashLifecycle(nil)
}

// A later scenario can interrupt the real explicit withdrawal at this SAME
// original checkpoint. These values come only from actual prior commands and
// read-only observations, never from a seeded or replacement authority context.
type unconsumedCrashCheckpoint struct {
	Pending, Deferred c.Intent
	Input, DeferInput c.CommitInput
	Baseline          consumptionProcessCrashSnapshot
	Business          continueUnknownBusinessSnapshot
}

func (s *continuationGate) exerciseConsumptionCrashLifecycle(beforeFence func(unconsumedCrashCheckpoint)) {
	view := s.checkCheckpoint(false, false)
	original := s.prepare(s.choice("DEFER_TASK", "consumption-crash-prepare-defer", "Retain exact verified fixture evidence; not acceptance or permission to merge.", int(view.ActionNode.Generation)))
	deferInput := s.commitInput(original, "consumption-crash-commit-defer")
	deferred := s.commit(deferInput)
	s.check(deferred.State == "COMPLETED" && deferred.Submission != nil && deferred.Submission.Outcome == c.Consumed, "genuine DEFER completes before consumption crash barrier exists", deferred)
	view = s.checkCheckpoint(true, false)
	business := s.continueUnknownBusinessSnapshot(deferred, deferInput)
	pending := s.prepare(s.choice("CONTINUE_TASK", "consumption-crash-prepare-continue", "", int(view.ActionNode.Generation)))
	in := s.commitInput(pending, "consumption-crash-original-once")
	baseline := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in)
	s.check(baseline.Ledger.IntentRequests == 0 && baseline.Ledger.Owner == in.IntentID && baseline.ReplacementReceipts == 0, "original pending choice has ownership but no attempted submission", baseline)
	s.save("consumption-process-crash-before.json", baseline)
	s.save("consumption-process-crash-business-before.json", business)
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	// BeginCommit has already inserted the exact consumed submission and updated
	// worker_epoch in this SAME transaction. It has not saved the intent or
	// committed, returned fresh=true, marked the step UNKNOWN or dispatched it.
	ddl := fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF intent_json ON confirmation_intents
 WHEN NEW.intent_id=%s AND NEW.ai_session_id=%s
 AND json_extract(OLD.intent_json,'$.state')='PENDING' AND json_extract(OLD.intent_json,'$.commit') IS NULL
 AND json_array_length(json_extract(OLD.intent_json,'$.steps'))=0
 AND json_extract(NEW.intent_json,'$.state')='CONFIRMING' AND json_extract(NEW.intent_json,'$.action')='CONTINUE_TASK'
 AND json_extract(NEW.intent_json,'$.commit.requestId')=%s AND json_array_length(json_extract(NEW.intent_json,'$.steps'))=1
 AND json_extract(NEW.intent_json,'$.steps[0].kind')='RECORD_TASK_CHOICE' AND json_extract(NEW.intent_json,'$.steps[0].state')='PENDING'
 AND EXISTS(SELECT 1 FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND intent_id=NEW.intent_id AND request_id=%s AND outcome='CONSUMED' AND submitted_sha256=%s)
 BEGIN SELECT %s(NEW.intent_id,NEW.ai_session_id,json_extract(NEW.intent_json,'$.commit.requestId'),json_extract(NEW.intent_json,'$.steps[0].requestId'),'CONTINUE_TASK',OLD.intent_json,
 json_object('nextIntent',NEW.intent_json,'worker',NEW.worker_epoch,
 'requests',(SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=NEW.intent_id),
 'request',(SELECT request_json FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=%s),
 'submitted',(SELECT submitted_sha256 FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=%s),
 'outcome',(SELECT outcome FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=%s),
 'holds',(SELECT COUNT(*) FROM task_holds WHERE task_id=%s),
 'receipts',(SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=%s),
 'events',(SELECT COUNT(*) FROM events WHERE task_id=%s AND type='task.verification_choice'))); END`, crashBarrierTrigger, quote(in.IntentID), quote(in.AISessionID), quote(in.RequestID), quote(in.RequestID), quote(c.ValueHash(in)), crashBarrierFunction, quote(in.RequestID), quote(in.RequestID), quote(in.RequestID), quote(s.f.task), quote(s.f.task), quote(s.f.task))
	fault := s.killChoiceCore(in, "consumption-process-crash", ddl, "owned Core SIGKILL after staging CONSUMED submission/worker epoch before BeginCommit intent save and transaction commit")
	var staged struct {
		NextIntent, Worker, Request, Submitted, Outcome string
		Requests, Holds, Receipts, Events               int
	}
	gateDecode(s.t, json.RawMessage(fault.Paused.Witness[6]), &staged)
	var attempted c.Intent
	var submitted c.CommitInput
	gateDecode(s.t, json.RawMessage(staged.NextIntent), &attempted)
	gateDecode(s.t, json.RawMessage(staged.Request), &submitted)
	s.check(fault.Paused.Witness[5] == baseline.Ledger.IntentJSON && staged.Worker == fault.ChildEpoch && staged.Worker != baseline.WorkerEpoch && staged.Requests == 1 && staged.Outcome == "CONSUMED" && staged.Submitted == c.ValueHash(in) && reflect.DeepEqual(submitted, in), "transaction witness proves genuine exact staged consumption and child worker, not a before-write crash", staged)
	s.check(attempted.State == "CONFIRMING" && attempted.Commit != nil && reflect.DeepEqual(*attempted.Commit, in) && len(attempted.Steps) == 1 && attempted.Steps[0].Kind == "RECORD_TASK_CHOICE" && attempted.Steps[0].State == "PENDING" && attempted.Steps[0].Effect == "" && attempted.Steps[0].Failure == nil && attempted.Steps[0].RequestID == fault.Paused.Witness[3] && staged.Holds == 1 && staged.Receipts == 1 && staged.Events == 1, "attempted consumption has only an undispatched PENDING step and no CONTINUE business effect", attempted)
	s.save("consumption-process-crash-transaction.json", staged)
	// First post-death oracle is read-only and precedes Core Open/recovery.
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(s.f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	before := s.consumptionCrashSnapshot(db, in)
	s.check(reflect.DeepEqual(before, baseline) && reflect.DeepEqual(s.continueUnknownBusinessSnapshotFromDB(db, deferred, deferInput), business), "pre-Open rollback restores exact original PENDING/worker, zero attempted receipts, genuine hold and all prior history", before)
	var triggers int
	if err = db.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggers == 1, "pre-consumption crash retains committed test barrier but no staged business authority", nil)
	s.save("consumption-process-crash-before-open.json", before)
	if err = db.Close(); err != nil {
		s.t.Fatal(err)
	}
	epoch := s.reopenKilledCore(fault)
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in), baseline), "new Core epoch does not reconstruct staged consumption", nil)
	if _, err = s.f.plane.DB.SQL().ExecContext(s.ctx, "DROP TRIGGER "+crashBarrierTrigger); err != nil {
		s.t.Fatal(err)
	}
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggers == 0, "only scoped consumption barrier removed without authority repair", nil)
	s.install()
	s.connect()
	assertPending := func(label string) {
		s.t.Helper()
		got := s.confirmationRead(pending, in.RequestID)
		s.checkCrashedUnobserved(got, pending, in)
		view := s.checkCheckpoint(true, false)
		s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID && view.ActionNode.Generation == baseline.Ledger.Generation, label+": original pending action remains owner of unchanged generation", view.ActionNode)
		s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in), baseline) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), business), label+": read creates no rejection receipt, release or business effect", nil)
	}
	assertPending("warm original read")
	s.cold()
	assertPending("cold original read")
	s.save("consumption-process-crash-after-cold-read.json", s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in))
	s.save("consumption-process-crash-epochs.json", []string{fault.PriorEpoch, fault.ChildEpoch, epoch, s.f.plane.DB.RuntimeEpoch()})
	if beforeFence != nil {
		beforeFence(unconsumedCrashCheckpoint{Pending: pending, Deferred: deferred, Input: in, DeferInput: deferInput, Baseline: baseline, Business: business})
	}
	// Do not replay or replace the uncertain request on absence evidence. A real
	// explicit withdrawal first establishes durable non-consumption authority.
	withdrawn := s.withdrawSubmission(in)
	s.checkWithdrawnPending(withdrawn, pending, in)
	sealed := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in)
	s.checkOneAddedCrashReceipt(baseline.Ledger.RequestsJSON, sealed.Ledger.RequestsJSON, in, "REJECTED_NOT_CONSUMED", "CONFIRMATION_SUBMISSION_WITHDRAWN", "SUBMISSION_WITHDRAWAL")
	expected := baseline
	expected.Ledger.RequestsJSON = sealed.Ledger.RequestsJSON
	expected.Ledger.IntentRequests = 1
	s.check(sealed.Ledger.IntentRequests == 1 && reflect.DeepEqual(sealed, expected) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), business), "withdrawal adds only original durable rejection; pending ownership and genuine hold remain exact", sealed)
	s.save("consumption-process-crash-sealed.json", sealed)
	s.cold()
	var late c.Result
	gateDecode(s.t, s.call("fixture_user", "confirmation_commit", in, "CONFIRMATION_SUBMISSION_WITHDRAWN"), &late)
	s.check(late.Intent != nil, "intentional late original delivery retains Core rejection", late)
	s.checkWithdrawnPending(*late.Intent, pending, in)
	s.check(reflect.DeepEqual(late.Intent.Submission, withdrawn.Submission) && reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in), sealed), "late original commit after durable withdrawal cannot consume, redispatch or rewrite history", late)
	s.checkCheckpoint(true, false)
	// New explicit fixture-user choice is authorized by the durable fence, not
	// by the test oracle or earlier UNKNOWN read. It keeps the SAME pending card.
	fresh := s.commitInput(pending, "consumption-crash-new-explicit-continue")
	continued := s.commit(fresh)
	s.check(continued.State == "COMPLETED" && continued.Commit != nil && continued.Commit.RequestID == fresh.RequestID && continued.Submission != nil && continued.Submission.Outcome == c.Consumed, "new explicitly confirmed request, not crashed request, genuinely completes CONTINUE", continued)
	s.checkCheckpoint(false, true)
	completed := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in)
	s.checkOneAddedCrashReceipt(sealed.Ledger.RequestsJSON, completed.Ledger.RequestsJSON, fresh, "CONSUMED", "", "")
	s.check(completed.Ledger.IntentRequests == 2, "exactly one withdrawal rejection and one new consumed request survive", completed)
	s.cold()
	history := s.confirmationRead(pending, in.RequestID)
	expectedSub := *withdrawn.Submission
	expectedSub.NextAction = c.ReadCurrentTarget
	expectedSub.RetryDisposition = c.RetryReprepareRequired
	expectedSub.ReprepareAllowed = false
	s.check(history.State == "COMPLETED" && reflect.DeepEqual(history.Commit, continued.Commit) && reflect.DeepEqual(history.Steps, continued.Steps) && reflect.DeepEqual(history.Submission, &expectedSub), "original rejection remains immutable but its retry projection closes after explicit new consumption", history)
	current := s.confirmationRead(pending, fresh.RequestID)
	s.check(reflect.DeepEqual(current.Commit, continued.Commit) && reflect.DeepEqual(current.Steps, continued.Steps) && reflect.DeepEqual(current.Submission, continued.Submission), "cold new request retains exact completed CONTINUE receipt", current)
	old := s.confirmationRead(original, deferInput.RequestID)
	s.check(reflect.DeepEqual(old.Commit, deferred.Commit) && reflect.DeepEqual(old.Steps, deferred.Steps) && reflect.DeepEqual(old.Submission, deferred.Submission), "old DEFER history cannot recreate the cleared hold", old)
	finalView := s.checkCheckpoint(false, true)
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in), completed), "cold historical reads change no receipts, ownership or completed business facts", nil)
	for name, want := range fault.AgentLogs {
		got, err := os.ReadFile(filepath.Join(s.evidence, name))
		s.check(err == nil && bytes.Equal(got, want), "crash, fencing and explicit continuation add no Agent work: "+name, err)
	}
	s.save("consumption-process-crash-completed.json", completed)
	// Preserve the view whose full checkpoint just passed. Only read-only ledger
	// and log comparisons follow it; an extra MCP read adds no business oracle.
	s.save("final-workspace.json", finalView)
}
