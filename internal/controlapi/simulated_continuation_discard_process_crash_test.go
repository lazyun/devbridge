//go:build !windows

package controlapi_test

// Test atomic retirement of an unconsumed continuation proposal. Discard is a
// typed ReadInput command: it has no invented commit/withdrawal request identity.

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

func TestSimulatedContinuationDiscardProcessCrash(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseDiscardProcessCrash)
}

func (s *continuationGate) checkDiscardCrashIntent(got, pending c.Intent, state string, submission *c.Submission) {
	s.t.Helper()
	s.check(got.IntentID == pending.IntentID && got.AISessionID == pending.AISessionID && got.Version == pending.Version && got.ContentHash == pending.ContentHash && got.Purpose == pending.Purpose && got.Action == pending.Action && reflect.DeepEqual(got.Content, pending.Content) && got.State == state && got.Commit == nil && len(got.Steps) == 0 && reflect.DeepEqual(got.Submission, submission) && !got.CanResume, "discard read retains exact original unconsumed identity and actual "+state+" state with only observed submission facts", got)
}

func (s *continuationGate) checkRetirementSnapshot(before, after consumptionProcessCrashSnapshot, pending c.Intent) {
	s.t.Helper()
	var retired c.Intent
	gateDecode(s.t, json.RawMessage(before.Ledger.IntentJSON), &retired)
	retired.State = "SUPERSEDED"
	raw, err := json.Marshal(retired)
	if err != nil {
		s.t.Fatal(err)
	}
	var expectedRows, actualRows []map[string]any
	gateDecode(s.t, json.RawMessage(before.IntentsJSON), &expectedRows)
	gateDecode(s.t, json.RawMessage(after.IntentsJSON), &actualRows)
	changed := 0
	for _, row := range expectedRows {
		if row["id"] == pending.IntentID {
			row["intent"] = string(raw)
			changed++
		}
	}
	s.check(changed == 1 && reflect.DeepEqual(expectedRows, actualRows), "retirement changes only original intent state; all Session prepare identities, workers and other intent bytes remain exact", nil)
	expected := before
	expected.IntentsJSON = after.IntentsJSON // Independently compared field-by-field above.
	expected.Ledger.IntentJSON = string(raw)
	expected.Ledger.Owner = ""
	expected.Ledger.Generation++
	s.check(reflect.DeepEqual(after, expected), "successful retirement only supersedes original intent and clears owner/advances generation exactly once, with no request/release/business changes", after)
}

func (s *continuationGate) exerciseDiscardProcessCrash() {
	view := s.checkCheckpoint(false, false)
	original := s.prepare(s.choice("DEFER_TASK", "discard-crash-prepare-defer", "Retain exact verified fixture evidence; not acceptance or permission to merge.", int(view.ActionNode.Generation)))
	deferInput := s.commitInput(original, "discard-crash-commit-defer")
	deferred := s.commit(deferInput)
	view = s.checkCheckpoint(true, false)
	business := s.continueUnknownBusinessSnapshot(deferred, deferInput)
	pending := s.prepare(s.choice("CONTINUE_TASK", "discard-crash-prepare-continue", "", int(view.ActionNode.Generation)))
	read := c.ReadInput{AISessionID: pending.AISessionID, IntentID: pending.IntentID, Version: pending.Version, Purpose: pending.Purpose, ContentHash: pending.ContentHash}
	// Reuse the read-only snapshot's Session/intent selectors ONLY. This is not a
	// commit input sent to Core, has no request/variant, and supplies no authority.
	key := c.CommitInput{AISessionID: read.AISessionID, IntentID: read.IntentID}
	baseline := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key)
	s.check(baseline.Ledger.IntentRequests == 0 && baseline.Ledger.Owner == pending.IntentID && baseline.ReplacementReceipts == 0, "pending retirement target has no commit submission and owns the exact action head", baseline)
	s.save("discard-process-crash-before.json", baseline)
	s.save("discard-process-crash-business-before.json", business)
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	// Production's settled-intent AFTER trigger performs this head UPDATE. This
	// AFTER-head callback observes BOTH real changes already staged in the same
	// outer DiscardProposal transaction. It adds no release receipt or row writes.
	ddl := fmt.Sprintf(`CREATE TRIGGER %s AFTER UPDATE OF owner_intent_id,generation ON task_action_heads
 WHEN OLD.task_id=%s AND NEW.task_id=OLD.task_id AND OLD.owner_intent_id=%s
 AND OLD.generation=%d AND NEW.owner_intent_id IS NULL AND NEW.generation=OLD.generation+1
 AND EXISTS(SELECT 1 FROM confirmation_intents WHERE intent_id=%s AND ai_session_id=%s
 AND json_extract(intent_json,'$.state')='SUPERSEDED' AND json_extract(intent_json,'$.action')='CONTINUE_TASK'
 AND json_extract(intent_json,'$.commit') IS NULL AND json_array_length(json_extract(intent_json,'$.steps'))=0)
 BEGIN SELECT %s(%s,%s,'',NEW.task_id,'CONTINUE_TASK',
 (SELECT intent_json FROM confirmation_intents WHERE intent_id=%s AND ai_session_id=%s),
 json_object('oldOwner',OLD.owner_intent_id,'newOwner',COALESCE(NEW.owner_intent_id,''),'oldGeneration',OLD.generation,'newGeneration',NEW.generation,
 'selectedOwner',(SELECT COALESCE(owner_intent_id,'') FROM task_action_heads WHERE task_id=NEW.task_id),
 'selectedGeneration',(SELECT generation FROM task_action_heads WHERE task_id=NEW.task_id),
 'requests',(SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=%s),
 'releases',(SELECT COUNT(*) FROM task_action_releases WHERE intent_id=%s AND task_id=NEW.task_id),
 'holds',(SELECT COUNT(*) FROM task_holds WHERE task_id=NEW.task_id),
 'receipts',(SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=NEW.task_id),
 'events',(SELECT COUNT(*) FROM events WHERE task_id=NEW.task_id AND type='task.verification_choice'))); END`, crashBarrierTrigger, quote(s.f.task), quote(pending.IntentID), baseline.Ledger.Generation, quote(pending.IntentID), quote(pending.AISessionID), crashBarrierFunction, quote(pending.IntentID), quote(pending.AISessionID), quote(pending.IntentID), quote(pending.AISessionID), quote(pending.IntentID), quote(pending.IntentID))
	fault := s.killCoreCommand(processCrashPlan{DataRoot: s.f.dataRoot, Evidence: s.evidence, Prefix: "discard-process-crash", TriggerSQL: ddl, Scope: "owned Core SIGKILL after pending retirement and owner release/generation advance are staged before DiscardProposal transaction commit", Discard: &read, DiscardAction: pending.Action})
	var staged struct {
		OldOwner, NewOwner, SelectedOwner                string
		OldGeneration, NewGeneration, SelectedGeneration int64
		Requests, Releases, Holds, Receipts, Events      int
	}
	gateDecode(s.t, json.RawMessage(fault.Paused.Witness[6]), &staged)
	var attempted c.Intent
	gateDecode(s.t, json.RawMessage(fault.Paused.Witness[5]), &attempted)
	s.checkDiscardCrashIntent(attempted, pending, "SUPERSEDED", nil)
	s.check(fault.Paused.Witness[3] == s.f.task && staged.OldOwner == pending.IntentID && staged.NewOwner == "" && staged.SelectedOwner == "" && staged.OldGeneration == baseline.Ledger.Generation && staged.NewGeneration == baseline.Ledger.Generation+1 && staged.SelectedGeneration == staged.NewGeneration && staged.Requests == 0 && staged.Releases == 0 && staged.Holds == 1 && staged.Receipts == 1 && staged.Events == 1, "nested trigger witness proves actual staged retirement and cleared owner/advanced head, not a pre-write crash or business continuation", staged)
	s.save("discard-process-crash-transaction.json", staged)
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(s.f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	before := s.consumptionCrashSnapshot(db, key)
	s.check(reflect.DeepEqual(before, baseline) && reflect.DeepEqual(s.continueUnknownBusinessSnapshotFromDB(db, deferred, deferInput), business), "pre-Open crash rollback restores exact pending intent, original owner/generation, all ledger/bindings and genuine hold", before)
	var triggers int
	if err = db.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggers == 1, "committed test barrier survives while staged retirement rolls back", nil)
	s.save("discard-process-crash-before-open.json", before)
	if err = db.Close(); err != nil {
		s.t.Fatal(err)
	}
	epoch := s.reopenKilledCore(fault)
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key), baseline), "new Core startup cannot project staged retirement as durable", nil)
	if _, err = s.f.plane.DB.SQL().ExecContext(s.ctx, "DROP TRIGGER "+crashBarrierTrigger); err != nil {
		s.t.Fatal(err)
	}
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggers == 0, "only scoped discard barrier removed, no ownership repair", nil)
	s.install()
	s.connect()
	assertPending := func(label string) {
		s.t.Helper()
		s.checkDiscardCrashIntent(s.confirmationRead(pending, ""), pending, "PENDING", nil)
		view := s.checkCheckpoint(true, false)
		s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == pending.IntentID && view.ActionNode.Generation == baseline.Ledger.Generation, label+": original pending proposal still blocks current action at unchanged generation", view.ActionNode)
		s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key), baseline) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), business), label+": pure reads do not retire original card or change any durable business facts", nil)
	}
	assertPending("warm")
	s.cold()
	assertPending("cold")
	s.call("gpt", "execution_prepare", s.choice("CONTINUE_TASK", "discard-crash-blocked-prepare", "", int(baseline.Ledger.Generation)), "TASK_ACTION_PENDING")
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key), baseline), "competing prepare cannot use a rolled-back generation advance", nil)
	s.save("discard-process-crash-after-cold-read.json", s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key))
	s.save("discard-process-crash-epochs.json", []string{fault.PriorEpoch, fault.ChildEpoch, epoch, s.f.plane.DB.RuntimeEpoch()})
	// A separate explicit fixture-user retirement, not automatic retry inferred
	// from the lost response. No business commit was ever submitted on this card.
	retired := s.discardChoice(pending)
	s.checkDiscardCrashIntent(retired, pending, "SUPERSEDED", nil)
	settled := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key)
	s.checkRetirementSnapshot(baseline, settled, pending)
	s.check(reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), business), "successful discard still does not clear genuine hold or alter DEFER business history", nil)
	view = s.checkCheckpoint(true, false)
	s.check(!view.ActionNode.Blocking && view.ActionNode.IntentID == "" && view.ActionNode.Generation == baseline.Ledger.Generation+1, "explicit retirement alone releases action and advances generation exactly once", view.ActionNode)
	s.save("discard-process-crash-retired.json", settled)
	s.cold()
	s.checkDiscardCrashIntent(s.discardChoice(pending), pending, "SUPERSEDED", nil)
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key), settled), "cold repeated retirement is idempotent and does not advance generation twice", nil)
	s.call("gpt", "execution_prepare", s.choice("CONTINUE_TASK", "discard-crash-stale-generation", "", int(baseline.Ledger.Generation)), "TASK_ACTION_STALE")
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key), settled), "old generation cannot change retirement history", nil)
	probe := s.commitInput(pending, "discard-crash-retired-card-negative-probe")
	var rejected c.Result
	gateDecode(s.t, s.call("fixture_user", "confirmation_commit", probe, "CONFIRMATION_INTENT_NOT_CURRENT"), &rejected)
	s.check(rejected.Intent != nil && rejected.Intent.Submission != nil, "retired-card probe exposes its durable non-consumption receipt", rejected)
	sub := rejected.Intent.Submission
	s.check(sub.Recorded && sub.Outcome == c.RejectedNotConsumed && sub.BusinessOutcome == "REJECTED_NOT_CONSUMED" && sub.Consumed != nil && !*sub.Consumed && sub.ConsumptionDisposition == "NOT_CONSUMED" && sub.RetryDisposition == c.RetryReprepareRequired && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.NextAction == c.ReadCurrentTarget && sub.ReceiptRequestID == "" && sub.PayloadSHA256 == c.ValueHash(probe) && reflect.DeepEqual(sub.Request, probe) && sub.Failure != nil && sub.Failure.Code == "CONFIRMATION_INTENT_NOT_CURRENT", "retired-card refusal proves no consumption but cannot re-arm old card", sub)
	s.checkDiscardCrashIntent(*rejected.Intent, pending, "SUPERSEDED", sub)
	refused := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key)
	s.checkOneAddedCrashReceipt(settled.Ledger.RequestsJSON, refused.Ledger.RequestsJSON, probe, "REJECTED_NOT_CONSUMED", "CONFIRMATION_INTENT_NOT_CURRENT", "CONFIRMATION_PREFLIGHT")
	expected := settled
	expected.Ledger.RequestsJSON = refused.Ledger.RequestsJSON // Exact added row and all older rows checked above.
	expected.Ledger.IntentRequests = 1
	s.check(reflect.DeepEqual(refused, expected), "retired-card rejection adds only one exact negative receipt without consumption or action-head changes", refused)
	s.save("discard-process-crash-refused.json", refused)
	view = s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Generation == baseline.Ledger.Generation+1, "refused old-card probes preserve exactly once generation advance", view.ActionNode)
	fresh := s.prepare(s.choice("CONTINUE_TASK", "discard-crash-fresh-current-prepare", "", int(view.ActionNode.Generation)))
	s.check(fresh.IntentID != pending.IntentID, "fresh current-generation proposal cannot revive retired card", fresh)
	freshInput := s.commitInput(fresh, "discard-crash-new-explicit-continue")
	continued := s.commit(freshInput)
	s.checkCheckpoint(false, true)
	completed := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key)
	s.check(completed.Ledger.IntentRequests == 1 && completed.Ledger.IntentJSON == settled.Ledger.IntentJSON, "old retired proposal retains only its rejection after distinct new CONTINUE completes", completed)
	s.checkOneAddedCrashReceipt(refused.Ledger.RequestsJSON, completed.Ledger.RequestsJSON, freshInput, "CONSUMED", "", "")
	s.cold()
	s.checkDiscardCrashIntent(s.confirmationRead(pending, ""), pending, "SUPERSEDED", sub)
	now := s.confirmationRead(fresh, freshInput.RequestID)
	s.check(reflect.DeepEqual(now.Commit, continued.Commit) && reflect.DeepEqual(now.Steps, continued.Steps) && reflect.DeepEqual(now.Submission, continued.Submission), "new proposal retains its own exact completed CONTINUE receipt across cold reopen", now)
	old := s.confirmationRead(original, deferInput.RequestID)
	s.check(reflect.DeepEqual(old.Commit, deferred.Commit) && reflect.DeepEqual(old.Steps, deferred.Steps) && reflect.DeepEqual(old.Submission, deferred.Submission), "DEFER history stays immutable and cannot recreate cleared hold", old)
	s.checkCheckpoint(false, true)
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), key), completed), "cold historical reads preserve retired original and completed new proposal without release or receipt changes", nil)
	for name, want := range fault.AgentLogs {
		got, err := os.ReadFile(filepath.Join(s.evidence, name))
		s.check(err == nil && bytes.Equal(got, want), "discard crash/retirement/new choice add no Agent work: "+name, err)
	}
	s.save("discard-process-crash-completed.json", completed)
	s.save("final-workspace.json", s.workspace())
}
