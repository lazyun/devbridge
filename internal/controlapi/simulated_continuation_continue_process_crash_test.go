//go:build !windows

package controlapi_test

// Unlike the SQL-abort scenario, kill Core before the business transaction can
// return or write a finalized failure. Its already-consumed confirmation remains
// CONFIRMING/UNKNOWN; absence of a canonical receipt is not retry authority.

import (
	"bytes"
	"crypto/sha256"
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
	"github.com/lazyun/devbridge/internal/task"
)

func TestSimulatedContinuationContinueProcessCrash(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseContinueProcessCrash)
}

type continueProcessCrashSnapshot struct {
	IntentJSON, WorkerEpoch, RequestJSON, SubmittedHash, RequestOutcome, Owner string
	CommitRequests, Holds, ChoiceReceipts, ContinueReceipts, ChoiceEvents      int
	Generation                                                                 int64
}

func (s *continuationGate) continueProcessCrashSnapshot(db *sql.DB, in c.CommitInput) continueProcessCrashSnapshot {
	s.t.Helper()
	tx, err := db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	var out continueProcessCrashSnapshot
	queries := []struct {
		sql  string
		args []any
		dest []any
	}{
		{`SELECT intent_json,worker_epoch FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, []any{in.IntentID, in.AISessionID}, []any{&out.IntentJSON, &out.WorkerEpoch}},
		{`SELECT request_json,submitted_sha256,outcome FROM confirmation_commit_requests WHERE intent_id=? AND ai_session_id=? AND request_id=?`, []any{in.IntentID, in.AISessionID, in.RequestID}, []any{&out.RequestJSON, &out.SubmittedHash, &out.RequestOutcome}},
		{`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, []any{in.IntentID}, []any{&out.CommitRequests}},
		{`SELECT COUNT(*) FROM task_holds WHERE task_id=?`, []any{s.f.task}, []any{&out.Holds}},
		{`SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=?`, []any{s.f.task}, []any{&out.ChoiceReceipts}},
		{`SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=? AND action='CONTINUE_TASK'`, []any{s.f.task}, []any{&out.ContinueReceipts}},
		{`SELECT COUNT(*) FROM events WHERE task_id=? AND type='task.verification_choice'`, []any{s.f.task}, []any{&out.ChoiceEvents}},
		{`SELECT generation,COALESCE(owner_intent_id,'') FROM task_action_heads WHERE task_id=?`, []any{s.f.task}, []any{&out.Generation, &out.Owner}},
	}
	for _, q := range queries {
		if err = tx.QueryRowContext(s.ctx, q.sql, q.args...).Scan(q.dest...); err != nil {
			s.t.Fatal(err)
		}
	}
	var durable c.Intent
	var request c.CommitInput
	gateDecode(s.t, json.RawMessage(out.IntentJSON), &durable)
	gateDecode(s.t, json.RawMessage(out.RequestJSON), &request)
	s.check(durable.State == "CONFIRMING" && !durable.CanResume && durable.Commit != nil && reflect.DeepEqual(*durable.Commit, in) && len(durable.Steps) == 1 && durable.Steps[0].Kind == "RECORD_TASK_CHOICE" && durable.Steps[0].State == "UNKNOWN" && durable.Steps[0].Effect == "" && durable.Steps[0].Failure == nil && durable.Steps[0].RequestID != "" && durable.Steps[0].RequestID != in.RequestID, "raw crash journal remains original CONFIRMING/UNKNOWN without invented final failure", durable)
	s.check(out.WorkerEpoch != "" && reflect.DeepEqual(request, in) && out.SubmittedHash == c.ValueHash(in) && out.RequestOutcome == "CONSUMED" && out.CommitRequests == 1 && out.Owner == in.IntentID, "one original consumed submission retains exact immutable request and action ownership", out)
	s.check(out.Holds == 1 && out.ChoiceReceipts == 1 && out.ContinueReceipts == 0 && out.ChoiceEvents == 1, "durable rollback retains only original DEFER hold/receipt/event and no CONTINUE effect", out)
	return out
}

func (s *continuationGate) checkUncommittedCrashChoice(got, pending c.Intent, in c.CommitInput, before continueProcessCrashSnapshot) {
	s.t.Helper()
	var durable c.Intent
	gateDecode(s.t, json.RawMessage(before.IntentJSON), &durable)
	s.check(got.IntentID == pending.IntentID && got.AISessionID == in.AISessionID && got.Version == pending.Version && got.Purpose == pending.Purpose && got.Action == pending.Action && got.ContentHash == pending.ContentHash && reflect.DeepEqual(got.Content, pending.Content) && got.State == "CONFIRMING" && !got.CanResume && got.Commit != nil && reflect.DeepEqual(*got.Commit, in), "public original identity remains CONFIRMING with unresolved business step, never crash-implied completion", got)
	s.check(reflect.DeepEqual(got.Steps, durable.Steps) && len(got.Steps) == 1 && got.Steps[0].State == "UNKNOWN" && got.Steps[0].Effect == "" && got.Steps[0].Failure == nil && got.Steps[0].RunID == "", "read projection preserves unfinished step instead of fabricating SQL-abort failure or successor Run", got.Steps)
	sub := got.Submission
	s.check(sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.BusinessOutcome == "COMMITTED" && sub.Consumed != nil && *sub.Consumed && sub.ConsumptionDisposition == "CONSUMED" && sub.RetryDisposition == c.RetryNone && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.NextAction == c.ReadOriginalReceipt && sub.ReceiptRequestID == in.RequestID && sub.PayloadSHA256 == c.ValueHash(in) && reflect.DeepEqual(sub.Request, in), "submission consumption is not completed CONTINUE, negative proof or unsafe retry authority", sub)
	var options c.Result
	gateDecode(s.t, s.call("gpt", "confirmation_options_get", c.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash, RequestID: in.RequestID}, ""), &options)
	s.check(options.Options != nil && options.Options.IntentID == in.IntentID && !options.Options.Eligibility.Available && !options.Options.Eligibility.ReprepareAllowed && options.Options.Eligibility.RetryDisposition == string(c.RetryNone) && options.Options.Eligibility.ReasonCode == "COMMIT_OUTCOME_UNKNOWN", "crashed unfinished choice offers no confirmation or reprepare path", options.Options)
	for _, v := range options.Options.Variants {
		s.check(!v.Available, "unresolved crashed choice variant is unavailable", v)
	}
}

// The returned checkpoint is composed only from the real lifecycle above and
// exact original receipts. A later scenario may exercise commands against that
// same surviving Core/Session, never a seeded or replaced authority context.
type crashedContinueCheckpoint struct {
	Original, Deferred, Pending, Observed c.Intent
	Input, DeferInput                     c.CommitInput
	Journal                               continueProcessCrashSnapshot
	Business                              continueUnknownBusinessSnapshot
	Fault                                 killedChoiceCore
}

func (s *continuationGate) exerciseContinueProcessCrash() {
	s.establishCrashedContinue()
	s.save("final-workspace.json", s.workspace())
}

func (s *continuationGate) establishCrashedContinue() crashedContinueCheckpoint {
	view := s.checkCheckpoint(false, false)
	note := "Retain exact verified fixture evidence; not acceptance or permission to merge."
	original := s.prepare(s.choice("DEFER_TASK", "continue-crash-prepare-defer", note, int(view.ActionNode.Generation)))
	deferInput := s.commitInput(original, "continue-crash-commit-defer")
	deferred := s.commit(deferInput)
	s.check(deferred.State == "COMPLETED" && deferred.Submission != nil && deferred.Submission.Outcome == c.Consumed, "genuine DEFER completes before any process-crash barrier exists", deferred)
	view = s.checkCheckpoint(true, false)
	baseline := s.continueUnknownBusinessSnapshot(deferred, deferInput)
	s.save("continue-process-crash-business-before.json", baseline)
	pending := s.prepare(s.choice("CONTINUE_TASK", "continue-crash-prepare-continue", "", int(view.ActionNode.Generation)))
	in := s.commitInput(pending, "continue-crash-commit-once")
	view = s.checkCheckpoint(true, false)
	generation := view.ActionNode.Generation
	s.check(reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), baseline), "preparing CONTINUE leaves exact genuine held business facts untouched", nil)
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	// This BEFORE INSERT executes after production's exact DELETE, but before its
	// receipt, event and Commit. SQLite changes() additionally witnesses one actual
	// deleted row, not merely an already-absent hold. JSON is text to the scalar.
	ddl := fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON task_hold_receipts
 WHEN NEW.actor=%s AND NEW.task_id=%s AND NEW.execution_id=%s AND NEW.revision_id=%s AND NEW.action='CONTINUE_TASK'
 AND NOT EXISTS(SELECT 1 FROM task_holds WHERE task_id=NEW.task_id)
 AND NEW.request_id=(SELECT json_extract(intent_json,'$.steps[0].requestId') FROM confirmation_intents
 WHERE intent_id=%s AND ai_session_id=%s AND json_extract(intent_json,'$.state')='CONFIRMING'
 AND json_array_length(json_extract(intent_json,'$.steps'))=1 AND json_extract(intent_json,'$.steps[0].state')='UNKNOWN'
 AND json_extract(intent_json,'$.steps[0].kind')='RECORD_TASK_CHOICE' AND json_extract(intent_json,'$.commit.requestId')=%s)
 BEGIN SELECT %s(%s,%s,%s,NEW.request_id,NEW.action,
 (SELECT intent_json FROM confirmation_intents WHERE intent_id=%s AND ai_session_id=%s),
 json_object('deletedRows',changes(),'holds',(SELECT COUNT(*) FROM task_holds WHERE task_id=NEW.task_id),
 'receipts',(SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=NEW.task_id),
 'events',(SELECT COUNT(*) FROM events WHERE task_id=NEW.task_id AND type='task.verification_choice'),
 'actor',NEW.actor,'task',NEW.task_id,'execution',NEW.execution_id,'revision',NEW.revision_id,'payloadSHA256',NEW.payload_sha256)); END`, crashBarrierTrigger, quote("session:"+in.AISessionID), quote(s.f.task), quote(s.executionID), quote(s.revision), quote(in.IntentID), quote(in.AISessionID), quote(in.RequestID), crashBarrierFunction, quote(in.IntentID), quote(in.AISessionID), quote(in.RequestID), quote(in.IntentID), quote(in.AISessionID))
	fault := s.killChoiceCore(in, "continue-process-crash", ddl, "owned Core SIGKILL inside CONTINUE transaction after genuine hold DELETE before canonical receipt INSERT")
	var transaction struct {
		DeletedRows, Holds, Receipts, Events            int
		Actor, Task, Execution, Revision, PayloadSHA256 string
	}
	gateDecode(s.t, json.RawMessage(fault.Paused.Witness[6]), &transaction)
	body := pending.Content.Execution
	s.check(body != nil, "original pending CONTINUE retains exact frozen execution body", pending)
	raw, err := json.Marshal(task.HoldInput{ProjectID: pending.ProjectID, TaskID: body.TaskID, FeatureAreaID: body.FeatureAreaID, ExecutionID: body.ExecutionID, RevisionID: body.TaskDefinitionRevisionID, Action: in.Action, RequestID: fault.Paused.Witness[3], Note: body.Note})
	if err != nil {
		s.t.Fatal(err)
	}
	s.check(transaction.DeletedRows == 1 && transaction.Holds == 0 && transaction.Receipts == 1 && transaction.Events == 1 && transaction.Actor == "session:"+in.AISessionID && transaction.Task == s.f.task && transaction.Execution == s.executionID && transaction.Revision == s.revision && transaction.PayloadSHA256 == fmt.Sprintf("%x", sha256.Sum256(raw)), "witness proves exact original hold was deleted inside uncommitted CONTINUE before canonical receipt/event", transaction)
	s.save("continue-process-crash-transaction.json", transaction)
	// Do not let startup or any public read precede the independent rollback oracle.
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(s.f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	before := s.continueProcessCrashSnapshot(db, in)
	business := s.continueUnknownBusinessSnapshotFromDB(db, deferred, deferInput)
	var count int
	if err = db.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&count); err != nil {
		s.t.Fatal(err)
	}
	s.check(count == 1 && before.IntentJSON == fault.Paused.Witness[5] && before.WorkerEpoch == fault.ChildEpoch && before.Generation == generation, "pre-Open durable journal matches child witness and frozen generation; crash retains test barrier", before)
	s.check(reflect.DeepEqual(business, baseline), "pre-Open SQLite rollback restores exact original hold/HeldAt and unchanged DEFER receipt/event/journal", business)
	s.save("continue-process-crash-before-open.json", before)
	s.save("continue-process-crash-business-after-death.json", business)
	if err = db.Close(); err != nil {
		s.t.Fatal(err)
	}
	epoch := s.reopenKilledCore(fault)
	s.check(reflect.DeepEqual(s.continueProcessCrashSnapshot(s.f.plane.DB.SQL(), in), before) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), baseline), "new Core epoch does not finish or repair the crashed transaction", nil)
	if _, err = s.f.plane.DB.SQL().ExecContext(s.ctx, "DROP TRIGGER "+crashBarrierTrigger); err != nil {
		s.t.Fatal(err)
	}
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&count); err != nil {
		s.t.Fatal(err)
	}
	s.check(count == 0, "exact crash barrier removed without business authority repair", nil)
	s.install()
	s.connect()
	got := s.confirmationRead(pending, in.RequestID)
	s.checkUncommittedCrashChoice(got, pending, in, before)
	view = s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID && view.ActionNode.Generation == generation, "surviving hold does not release the original consumed unresolved action", view.ActionNode)
	old := s.confirmationRead(original, deferInput.RequestID)
	s.check(reflect.DeepEqual(old.Commit, deferred.Commit) && reflect.DeepEqual(old.Steps, deferred.Steps) && reflect.DeepEqual(old.Submission, deferred.Submission), "historical completed DEFER cannot settle crashed CONTINUE", old)
	s.call("gpt", "execution_prepare", s.choice("CONTINUE_TASK", "continue-crash-blocked-fresh-choice", "", int(generation)), "TASK_ACTION_PENDING")
	s.check(reflect.DeepEqual(s.continueProcessCrashSnapshot(s.f.plane.DB.SQL(), in), before) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), baseline), "original reads and refused competing prepare preserve exact crash journal and restored business facts", nil)
	s.save("continue-process-crash-after-read.json", s.continueProcessCrashSnapshot(s.f.plane.DB.SQL(), in))
	s.save("continue-process-crash-epochs.json", []string{fault.PriorEpoch, fault.ChildEpoch, epoch})
	s.cold()
	cold := s.confirmationRead(pending, in.RequestID)
	s.checkUncommittedCrashChoice(cold, pending, in, before)
	s.check(reflect.DeepEqual(cold.Steps, got.Steps) && reflect.DeepEqual(cold.Commit, got.Commit) && reflect.DeepEqual(cold.Submission, got.Submission), "another cold epoch preserves same original unresolved receipt without retry", cold)
	view = s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID && view.ActionNode.Generation == generation, "cold unresolved action retains exact original ownership and generation", view.ActionNode)
	after := s.continueProcessCrashSnapshot(s.f.plane.DB.SQL(), in)
	s.check(reflect.DeepEqual(after, before) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), baseline), "cold recovery neither deletes the restored hold nor invents CONTINUE completion or failure", after)
	for name, want := range fault.AgentLogs {
		got, err := os.ReadFile(filepath.Join(s.evidence, name))
		s.check(err == nil && bytes.Equal(got, want), "in-transaction crash/recovery emits no additional Agent activity: "+name, err)
	}
	s.save("continue-process-crash-after-cold.json", after)
	return crashedContinueCheckpoint{Original: original, Deferred: deferred, Pending: pending, Observed: cold, Input: in, DeferInput: deferInput, Journal: before, Business: baseline, Fault: fault}
}
