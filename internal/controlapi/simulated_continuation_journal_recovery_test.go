package controlapi_test

// This fault aborts only the final journal save AFTER the real business commit.
// Recovery observes Core-created canonical receipts; it never retries a choice.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	driversqlite "modernc.org/sqlite"
)

const journalSaveFaultFunction = "devbridge_test_only_final_journal_abort"
const journalSaveFaultMessage = "TEST_ONLY_EXACT_FINAL_CHOICE_JOURNAL_SAVE_ABORT"

var journalSaveFaultRegistration sync.Once
var journalSaveFaultRegistrationError error
var journalSaveFaultSequence atomic.Uint64
var journalSaveFaultWitnesses = struct {
	sync.Mutex
	calls map[string][][]string
}{calls: make(map[string][][]string)}

func TestSimulatedContinuationJournalRecovery(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent gate; run scripts/test-simulated-continuation.sh --journal-recovery")
	}
	// Supported selection is this isolated nonparallel scenario plus the pure
	// receipt oracle. Registration is process-global, before fixture DB opens.
	journalSaveFaultRegistration.Do(func() {
		journalSaveFaultRegistrationError = driversqlite.RegisterScalarFunction(journalSaveFaultFunction, 8, func(_ *driversqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
			args := make([]string, len(values))
			for index, value := range values {
				text, ok := value.(string)
				if !ok {
					return nil, errors.New("test-only final journal witness requires text identity")
				}
				args[index] = text
			}
			journalSaveFaultWitnesses.Lock()
			_, exists := journalSaveFaultWitnesses.calls[args[0]]
			if exists {
				journalSaveFaultWitnesses.calls[args[0]] = append(journalSaveFaultWitnesses.calls[args[0]], append([]string(nil), args[1:]...))
			}
			journalSaveFaultWitnesses.Unlock()
			if !exists {
				return nil, errors.New("test-only final journal witness token unavailable")
			}
			// No DB access, record writes, testing calls or callback re-entry.
			return nil, errors.New(journalSaveFaultMessage)
		})
	})
	if journalSaveFaultRegistrationError != nil {
		t.Fatal(journalSaveFaultRegistrationError)
	}
	token := fmt.Sprintf("journal-save-%d", journalSaveFaultSequence.Add(1))
	journalSaveFaultWitnesses.Lock()
	journalSaveFaultWitnesses.calls[token] = nil
	journalSaveFaultWitnesses.Unlock()
	t.Cleanup(func() {
		journalSaveFaultWitnesses.Lock()
		delete(journalSaveFaultWitnesses.calls, token)
		journalSaveFaultWitnesses.Unlock()
	})
	runSimulatedContinuationGate(t, func(s *continuationGate) { s.exerciseJournalRecovery(token) })
}

func journalSaveFaultCalls(token string) [][]string {
	journalSaveFaultWitnesses.Lock()
	defer journalSaveFaultWitnesses.Unlock()
	var result [][]string
	for _, call := range journalSaveFaultWitnesses.calls[token] {
		result = append(result, append([]string(nil), call...))
	}
	return result
}

type journalRecoverySnapshot struct {
	IntentJSON, RequestJSON, SubmittedHash, RequestOutcome, Owner                                                              string
	ReceiptActor, ReceiptRequest, ReceiptTask, ReceiptExecution, ReceiptRevision, ReceiptHash, ReceiptAction, ReceiptCreatedAt string
	HoldsJSON, ChoiceEventsJSON, ChoiceReceiptsJSON                                                                            string
	CommitRequests, ChoiceReceipts                                                                                             int
	Generation                                                                                                                 int64
}

func (s *continuationGate) journalRecoverySnapshot(in c.CommitInput, receipts int) journalRecoverySnapshot {
	return s.journalRecoverySnapshotFromDB(s.f.plane.DB.SQL(), in, receipts)
}

// A process-crash driver can inspect the durable WAL through a mode=ro handle
// before opening another Core epoch. This helper performs only SELECTs.
func (s *continuationGate) journalRecoverySnapshotFromDB(db *sql.DB, in c.CommitInput, receipts int) journalRecoverySnapshot {
	s.t.Helper()
	tx, err := db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	var out journalRecoverySnapshot
	if err = tx.QueryRowContext(s.ctx, `SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, in.IntentID, in.AISessionID).Scan(&out.IntentJSON); err != nil {
		s.t.Fatal(err)
	}
	var durable c.Intent
	if err = json.Unmarshal([]byte(out.IntentJSON), &durable); err != nil {
		s.t.Fatal(err)
	}
	s.check(durable.State == "CONFIRMING" && durable.Commit != nil && reflect.DeepEqual(*durable.Commit, in) && len(durable.Steps) == 1 && durable.Steps[0].State == "UNKNOWN" && durable.Steps[0].Effect == "" && durable.Steps[0].Failure == nil, "raw journal is still original CONFIRMING/UNKNOWN, not projected or repaired completion", durable)
	queries := []struct {
		sql  string
		args []any
		dest []any
	}{
		{`SELECT request_json,submitted_sha256,outcome FROM confirmation_commit_requests WHERE intent_id=? AND ai_session_id=? AND request_id=?`, []any{in.IntentID, in.AISessionID, in.RequestID}, []any{&out.RequestJSON, &out.SubmittedHash, &out.RequestOutcome}},
		{`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, []any{in.IntentID}, []any{&out.CommitRequests}},
		{`SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=?`, []any{s.f.task}, []any{&out.ChoiceReceipts}},
		{`SELECT actor,request_id,task_id,execution_id,revision_id,payload_sha256,action,created_at FROM task_hold_receipts WHERE actor=? AND request_id=?`, []any{"session:" + in.AISessionID, durable.Steps[0].RequestID}, []any{&out.ReceiptActor, &out.ReceiptRequest, &out.ReceiptTask, &out.ReceiptExecution, &out.ReceiptRevision, &out.ReceiptHash, &out.ReceiptAction, &out.ReceiptCreatedAt}},
		{`SELECT json_group_array(json_object('task',task_id,'execution',execution_id,'revision',revision_id,'note',note,'heldAt',held_at)) FROM task_holds WHERE task_id=?`, []any{s.f.task}, []any{&out.HoldsJSON}},
		{`SELECT json_group_array(json_object('sequence',sequence,'id',id,'payload',payload_json,'createdAt',created_at)) FROM (SELECT sequence,id,payload_json,created_at FROM events WHERE task_id=? AND type='task.verification_choice' ORDER BY sequence)`, []any{s.f.task}, []any{&out.ChoiceEventsJSON}},
		{`SELECT json_group_array(json_object('actor',actor,'request',request_id,'task',task_id,'execution',execution_id,'revision',revision_id,'hash',payload_sha256,'action',action,'createdAt',created_at)) FROM (SELECT * FROM task_hold_receipts WHERE task_id=? ORDER BY actor,request_id)`, []any{s.f.task}, []any{&out.ChoiceReceiptsJSON}},
		{`SELECT generation,COALESCE(owner_intent_id,'') FROM task_action_heads WHERE task_id=?`, []any{s.f.task}, []any{&out.Generation, &out.Owner}},
	}
	for _, q := range queries {
		if err = tx.QueryRowContext(s.ctx, q.sql, q.args...).Scan(q.dest...); err != nil {
			s.t.Fatal(err)
		}
	}
	body := durable.Content.Execution
	s.check(body != nil, "durable original choice retains frozen Execution body", durable)
	raw, err := json.Marshal(task.HoldInput{ProjectID: durable.ProjectID, TaskID: body.TaskID, FeatureAreaID: body.FeatureAreaID, ExecutionID: body.ExecutionID, RevisionID: body.TaskDefinitionRevisionID, Action: in.Action, RequestID: durable.Steps[0].RequestID, Note: body.Note})
	if err != nil {
		s.t.Fatal(err)
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	s.check(out.ReceiptActor == "session:"+in.AISessionID && out.ReceiptRequest == durable.Steps[0].RequestID && out.ReceiptRequest != in.RequestID && out.ReceiptTask == s.f.task && out.ReceiptExecution == s.executionID && out.ReceiptRevision == s.revision && out.ReceiptAction == in.Action && out.ReceiptHash == expectedHash && out.ReceiptCreatedAt != "", "genuine canonical choice receipt matches exact actor, business subrequest, targets and independent frozen payload digest", out)
	s.check(out.CommitRequests == 1 && out.RequestOutcome == "CONSUMED" && out.SubmittedHash == c.ValueHash(in) && out.ChoiceReceipts == receipts && out.Owner == in.IntentID, "one original consumed submission and exact canonical receipt count; raw action owner not repaired", out)
	var events []json.RawMessage
	var holds []json.RawMessage
	if err = json.Unmarshal([]byte(out.ChoiceEventsJSON), &events); err != nil {
		s.t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(out.HoldsJSON), &holds); err != nil {
		s.t.Fatal(err)
	}
	expectedHolds := 0
	if in.Action == "DEFER_TASK" {
		expectedHolds = 1
	}
	s.check(len(events) == receipts && len(holds) == expectedHolds, "business effect and one event per genuine choice exist independently of unfinished final journal persistence", out)
	return out
}

func (s *continuationGate) checkJournalRecoveredChoice(got, pending c.Intent, in c.CommitInput, snapshot journalRecoverySnapshot) {
	s.t.Helper()
	s.check(got.IntentID == pending.IntentID && got.AISessionID == in.AISessionID && got.Version == pending.Version && got.Purpose == pending.Purpose && got.Action == pending.Action && got.ContentHash == pending.ContentHash && reflect.DeepEqual(got.Content, pending.Content) && got.State == "COMPLETED" && !got.CanResume && got.Commit != nil && reflect.DeepEqual(*got.Commit, in), "read-only reconciliation completes only the exact original consumed choice", got)
	sub := got.Submission
	s.check(sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.BusinessOutcome == "COMMITTED" && sub.Consumed != nil && *sub.Consumed && sub.ConsumptionDisposition == "CONSUMED" && sub.RetryDisposition == c.RetryNone && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.NextAction == c.ReadOriginalReceipt && sub.ReceiptRequestID == in.RequestID && sub.PayloadSHA256 == c.ValueHash(in) && reflect.DeepEqual(sub.Request, in), "recovered submission freezes original identity without retry authority", sub)
	s.check(len(got.Steps) == 1 && got.Steps[0].Kind == "RECORD_TASK_CHOICE" && got.Steps[0].State == "COMPLETED" && got.Steps[0].Effect == "COMMAND_COMPLETED" && got.Steps[0].Failure == nil && got.Steps[0].Code == "" && got.Steps[0].Message == "" && got.Steps[0].RequestID == snapshot.ReceiptRequest && got.Steps[0].TaskID == snapshot.ReceiptTask && got.Steps[0].ExecutionID == snapshot.ReceiptExecution && got.Steps[0].RevisionID == snapshot.ReceiptRevision && got.Steps[0].RunID == "", "reconstructed completed step comes from original canonical receipt, not dispatch", got.Steps)
	var options c.Result
	gateDecode(s.t, s.call("gpt", "confirmation_options_get", c.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash, RequestID: in.RequestID}, ""), &options)
	// Completed options expose a fresh-current-prepare hint; this is distinct
	// from the immutable consumed submission's no-retry/no-reprepare receipt.
	s.check(options.Options != nil && options.Options.IntentID == in.IntentID && !options.Options.Eligibility.Available && options.Options.Eligibility.ReprepareAllowed && options.Options.Eligibility.RetryDisposition == string(c.RetryNone) && options.Options.Eligibility.ReasonCode == "INTENT_ALREADY_CONSUMED" && options.Options.Eligibility.ReceiptState == "CONSUMED" && options.Options.Eligibility.NextAction == c.ReadOriginalReceipt, "completed options report consumed original and no retry, with only fresh-prepare hint", options.Options)
	for _, variant := range options.Options.Variants {
		s.check(!variant.Available, "consumed original variant cannot be reconfirmed", variant)
	}
}

func (s *continuationGate) failFinalChoiceJournal(token string, pending c.Intent, in c.CommitInput, ordinal int) journalRecoverySnapshot {
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	fault := fmt.Sprintf(`CREATE TRIGGER devbridge_test_only_final_journal_fault BEFORE UPDATE OF intent_json ON confirmation_intents
 WHEN NEW.intent_id=%s AND NEW.ai_session_id=%s
 AND json_extract(OLD.intent_json,'$.state')='CONFIRMING' AND json_extract(OLD.intent_json,'$.steps[0].state')='UNKNOWN'
 AND json_extract(NEW.intent_json,'$.state')='COMPLETED' AND json_array_length(json_extract(NEW.intent_json,'$.steps'))=1
 AND json_extract(NEW.intent_json,'$.steps[0].state')='COMPLETED' AND json_extract(NEW.intent_json,'$.steps[0].effect')='COMMAND_COMPLETED'
 AND json_extract(NEW.intent_json,'$.steps[0].kind')='RECORD_TASK_CHOICE' AND json_extract(NEW.intent_json,'$.action')=%s
 AND json_extract(OLD.intent_json,'$.commit.requestId')=%s AND json_extract(NEW.intent_json,'$.commit.requestId')=%s
 AND json_extract(OLD.intent_json,'$.steps[0].requestId')=json_extract(NEW.intent_json,'$.steps[0].requestId')
 AND EXISTS(SELECT 1 FROM task_hold_receipts WHERE actor=%s AND request_id=json_extract(NEW.intent_json,'$.steps[0].requestId') AND task_id=%s AND execution_id=%s AND revision_id=%s AND action=%s)
 BEGIN SELECT %s(%s,NEW.intent_id,NEW.ai_session_id,json_extract(NEW.intent_json,'$.commit.requestId'),json_extract(NEW.intent_json,'$.steps[0].requestId'),json_extract(NEW.intent_json,'$.action'),OLD.intent_json,NEW.intent_json); END`, quote(in.IntentID), quote(in.AISessionID), quote(in.Action), quote(in.RequestID), quote(in.RequestID), quote("session:"+in.AISessionID), quote(s.f.task), quote(s.executionID), quote(s.revision), quote(in.Action), journalSaveFaultFunction, quote(token))
	if _, err := s.f.plane.DB.SQL().ExecContext(s.ctx, fault); err != nil {
		s.t.Fatal(err)
	}
	installed := true
	drop := func() {
		if !installed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := s.f.plane.DB.SQL().ExecContext(ctx, `DROP TRIGGER devbridge_test_only_final_journal_fault`); err != nil {
			s.t.Errorf("remove final journal fault: %v", err)
			return
		}
		installed = false
	}
	defer drop()
	s.save("journal-recovery-fault-"+in.Action+".json", map[string]any{"faultSQL": fault, "faultError": journalSaveFaultMessage, "scope": "final confirmation journal save abort after genuine business commit; not business rollback, crash or response-stream loss", "noAuthorityRowsFabricated": true})
	var failed c.Result
	gateDecode(s.t, s.call("fixture_user", "confirmation_commit", in, "CONFIRMATION_FACTS_UNAVAILABLE"), &failed)
	s.check(failed.Failure != nil && failed.Failure.Stage == "RECEIPT_READBACK" && failed.Failure.NextAction == c.ReadOriginalReceipt && failed.Intent != nil && failed.Intent.IntentID == pending.IntentID && failed.Intent.State == "CONFIRMING" && !failed.Intent.CanResume && len(failed.Intent.Steps) == 1 && failed.Intent.Steps[0].State == "UNKNOWN", "initial real commit reports final receipt-readback failure, not observed business completion", failed)
	// Capture actual durable facts before ANY recovery-capable public read.
	before := s.journalRecoverySnapshot(in, ordinal)
	witness := journalSaveFaultCalls(token)
	s.check(len(witness) == ordinal && len(witness[ordinal-1]) == 7 && reflect.DeepEqual(witness[ordinal-1][:5], []string{in.IntentID, in.AISessionID, in.RequestID, before.ReceiptRequest, in.Action}) && witness[ordinal-1][5] == before.IntentJSON, "exactly one final-save fault per original choice, after canonical receipt exists", witness)
	var attempted c.Intent
	if err := json.Unmarshal([]byte(witness[ordinal-1][6]), &attempted); err != nil {
		s.t.Fatal(err)
	}
	s.check(attempted.State == "COMPLETED" && len(attempted.Steps) == 1 && attempted.Steps[0].State == "COMPLETED" && attempted.Steps[0].Effect == "COMMAND_COMPLETED" && attempted.Steps[0].RequestID == before.ReceiptRequest, "witness distinguishes attempted final journal from its persisted UNKNOWN predecessor", attempted)
	s.save("journal-recovery-before-"+in.Action+".json", before)
	s.save("journal-recovery-witness.json", witness)
	drop()
	var triggers int
	if err := s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='devbridge_test_only_final_journal_fault'`).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(!installed && triggers == 0, "final journal fault removed before readback or cold reopen", triggers)
	return before
}

func (s *continuationGate) exerciseJournalRecovery(token string) {
	view := s.checkCheckpoint(false, false)
	pending := s.prepare(s.choice("DEFER_TASK", "journal-recovery-prepare-defer", "Retain exact verified fixture evidence; not acceptance or permission to merge.", int(view.ActionNode.Generation)))
	deferInput := s.commitInput(pending, "journal-recovery-commit-defer-once")
	before := s.failFinalChoiceJournal(token, pending, deferInput, 1)
	deferred := s.confirmationRead(pending, deferInput.RequestID)
	s.checkJournalRecoveredChoice(deferred, pending, deferInput, before)
	view = s.checkCheckpoint(true, false)
	s.check(!view.ActionNode.Blocking && view.ActionNode.Generation == before.Generation+1, "read-only action node projects settled choice without mutating raw ownership", view.ActionNode)
	s.check(reflect.DeepEqual(s.journalRecoverySnapshot(deferInput, 1), before), "warm receipt/options/workspace reads do not repair journal or repeat business effect", nil)
	s.cold()
	coldDefer := s.confirmationRead(pending, deferInput.RequestID)
	s.checkJournalRecoveredChoice(coldDefer, pending, deferInput, before)
	s.check(reflect.DeepEqual(coldDefer.Steps, deferred.Steps) && reflect.DeepEqual(coldDefer.Submission, deferred.Submission), "cold recovery preserves exact original DEFER receipt", coldDefer)
	view = s.checkCheckpoint(true, false)
	after := s.journalRecoverySnapshot(deferInput, 1)
	s.check(reflect.DeepEqual(after, before) && len(journalSaveFaultCalls(token)) == 1, "cold recovery is read-only and never re-enters final journal write", after)
	s.save("journal-recovery-after-DEFER_TASK.json", after)
	// This is a NEW explicit user choice only after original DEFER is proven.
	// Normal prepare may now persist prior reconciliation/advance action ownership;
	// the pure-read snapshot window above is deliberately closed before this write.
	next := s.prepare(s.choice("CONTINUE_TASK", "journal-recovery-prepare-continue", "", int(view.ActionNode.Generation)))
	continueInput := s.commitInput(next, "journal-recovery-commit-continue-once")
	before = s.failFinalChoiceJournal(token, next, continueInput, 2)
	// Cold reopen BEFORE the first receipt/options/workspace recovery read.
	s.cold()
	continued := s.confirmationRead(next, continueInput.RequestID)
	s.checkJournalRecoveredChoice(continued, next, continueInput, before)
	s.checkCheckpoint(false, true)
	historical := s.confirmationRead(pending, deferInput.RequestID)
	s.check(reflect.DeepEqual(historical.Steps, deferred.Steps) && reflect.DeepEqual(historical.Commit, deferred.Commit) && reflect.DeepEqual(historical.Submission, deferred.Submission), "old DEFER history stays original after CONTINUE without recreating a hold", historical)
	repeated := s.confirmationRead(next, continueInput.RequestID)
	s.checkJournalRecoveredChoice(repeated, next, continueInput, before)
	s.checkCheckpoint(false, true)
	after = s.journalRecoverySnapshot(continueInput, 2)
	s.check(reflect.DeepEqual(after, before) && len(journalSaveFaultCalls(token)) == 2, "cold-first CONTINUE recovery and historical reads neither persist journal nor repeat choices", after)
	s.save("journal-recovery-after-CONTINUE_TASK.json", after)
	s.save("final-workspace.json", s.workspace())
	// Neither original confirmation_commit is ever resent, with any request ID.
}
