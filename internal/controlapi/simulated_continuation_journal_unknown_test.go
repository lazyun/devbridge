package controlapi_test

// The fault only aborts one real task-choice transaction. It never inserts or
// edits authority/outcome rows: Core itself persists the conservative UNKNOWN.

import (
	"context"
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
	driversqlite "modernc.org/sqlite"
)

const journalFaultFunction = "devbridge_test_only_choice_receipt_abort"
const journalFaultMessage = "TEST_ONLY_EXACT_TASK_CHOICE_RECEIPT_ABORT"

var journalFaultRegistration sync.Once
var journalFaultRegistrationError error
var journalFaultSequence atomic.Uint64
var journalFaultWitnesses = struct {
	sync.Mutex
	calls map[string][][]string
}{calls: make(map[string][][]string)}

func TestSimulatedContinuationJournalUnknown(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent gate; run scripts/test-simulated-continuation.sh --journal-unknown")
	}
	token := newJournalFaultWitness(t)
	runSimulatedContinuationGate(t, func(s *continuationGate) { s.exerciseJournalUnknown(token) })
}

func newJournalFaultWitness(t *testing.T) string {
	t.Helper()
	// The dedicated script selects this nonparallel scenario plus the pure
	// receipt oracle. Register once BEFORE opening any fixture DB connections;
	// modernc's registration is global and applies only to future connections.
	journalFaultRegistration.Do(func() {
		journalFaultRegistrationError = driversqlite.RegisterScalarFunction(journalFaultFunction, 7, func(_ *driversqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
			args := make([]string, len(values))
			for index, value := range values {
				text, ok := value.(string)
				if !ok {
					return nil, errors.New("test-only journal witness requires text identity")
				}
				args[index] = text
			}
			journalFaultWitnesses.Lock()
			_, exists := journalFaultWitnesses.calls[args[0]]
			if exists {
				journalFaultWitnesses.calls[args[0]] = append(journalFaultWitnesses.calls[args[0]], append([]string(nil), args[1:]...))
			}
			journalFaultWitnesses.Unlock()
			if !exists {
				return nil, errors.New("test-only journal witness token unavailable")
			}
			// No DB access, business-record writes, test assertions or re-entry.
			return nil, errors.New(journalFaultMessage)
		})
	})
	if journalFaultRegistrationError != nil {
		t.Fatal(journalFaultRegistrationError)
	}
	token := fmt.Sprintf("journal-%d", journalFaultSequence.Add(1))
	journalFaultWitnesses.Lock()
	journalFaultWitnesses.calls[token] = nil
	journalFaultWitnesses.Unlock()
	t.Cleanup(func() {
		journalFaultWitnesses.Lock()
		delete(journalFaultWitnesses.calls, token)
		journalFaultWitnesses.Unlock()
	})
	return token
}

func journalFaultCalls(token string) [][]string {
	journalFaultWitnesses.Lock()
	defer journalFaultWitnesses.Unlock()
	var result [][]string
	for _, call := range journalFaultWitnesses.calls[token] {
		result = append(result, append([]string(nil), call...))
	}
	return result
}

type journalUnknownSnapshot struct {
	IntentJSON, RequestJSON, SubmittedHash, RequestOutcome, Owner string
	CommitRequests, Holds, ChoiceReceipts                         int
	Generation                                                    int64
}

// These exact read-only queries observe persisted state independently of the
// public recovery projection. They do not manufacture a missing result.
func (s *continuationGate) journalSnapshot(in c.CommitInput) journalUnknownSnapshot {
	return s.journalSnapshotWithCounts(in, 0, 0)
}

func (s *continuationGate) journalSnapshotWithCounts(in c.CommitInput, holds, receipts int) journalUnknownSnapshot {
	s.t.Helper()
	db := s.f.plane.DB.SQL()
	var out journalUnknownSnapshot
	queries := []struct {
		sql  string
		args []any
		dest []any
	}{
		{`SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, []any{in.IntentID, in.AISessionID}, []any{&out.IntentJSON}},
		{`SELECT request_json,submitted_sha256,outcome FROM confirmation_commit_requests WHERE intent_id=? AND ai_session_id=? AND request_id=?`, []any{in.IntentID, in.AISessionID, in.RequestID}, []any{&out.RequestJSON, &out.SubmittedHash, &out.RequestOutcome}},
		{`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, []any{in.IntentID}, []any{&out.CommitRequests}},
		{`SELECT COUNT(*) FROM task_holds WHERE task_id=?`, []any{s.f.task}, []any{&out.Holds}},
		{`SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=?`, []any{s.f.task}, []any{&out.ChoiceReceipts}},
		{`SELECT generation,COALESCE(owner_intent_id,'') FROM task_action_heads WHERE task_id=?`, []any{s.f.task}, []any{&out.Generation, &out.Owner}},
	}
	for _, query := range queries {
		if err := db.QueryRowContext(s.ctx, query.sql, query.args...).Scan(query.dest...); err != nil {
			s.t.Fatal(err)
		}
	}
	var durable c.Intent
	if err := json.Unmarshal([]byte(out.IntentJSON), &durable); err != nil {
		s.t.Fatal(err)
	}
	s.check(durable.State == "UNKNOWN" && durable.Commit != nil && reflect.DeepEqual(*durable.Commit, in) && len(durable.Steps) == 1 && durable.Steps[0].State == "UNKNOWN" && durable.Steps[0].Effect == "UNKNOWN", "actual durable journal remains UNKNOWN, not only a public projection", durable)
	s.check(out.CommitRequests == 1 && out.RequestOutcome == "CONSUMED" && out.SubmittedHash == c.ValueHash(in) && out.Holds == holds && out.ChoiceReceipts == receipts && out.Owner == in.IntentID, "one consumed request, exact expected hold/choice counts, original action owner retained", out)
	return out
}

func (s *continuationGate) checkUnknownChoice(observed c.Intent, pending c.Intent, in c.CommitInput) {
	s.t.Helper()
	s.check(observed.IntentID == pending.IntentID && observed.AISessionID == in.AISessionID && observed.Version == pending.Version && observed.Purpose == pending.Purpose && observed.Action == pending.Action && observed.ContentHash == pending.ContentHash && reflect.DeepEqual(observed.Content, pending.Content) && observed.State == "UNKNOWN" && !observed.CanResume && observed.Commit != nil && observed.Commit.RequestID == in.RequestID, "exact original consumed choice remains UNKNOWN without resume", observed)
	sub := observed.Submission
	// COMMITTED here is submission consumption, NOT completed business effect.
	// The intent/step must still say UNKNOWN; hold presence/absence is not retry authority.
	s.check(sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.BusinessOutcome == "COMMITTED" && sub.Consumed != nil && *sub.Consumed && sub.ConsumptionDisposition == "CONSUMED" && sub.RetryDisposition == c.RetryNone && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.NextAction == c.ReadOriginalReceipt && sub.ReceiptRequestID == in.RequestID && sub.PayloadSHA256 == c.ValueHash(in) && reflect.DeepEqual(sub.Request, in), "consumed submission is not unknown-step success or permission to retry", sub)
	s.check(len(observed.Steps) == 1 && observed.Steps[0].Kind == "RECORD_TASK_CHOICE" && observed.Steps[0].State == "UNKNOWN" && observed.Steps[0].Effect == "UNKNOWN" && observed.Steps[0].RunID == "" && observed.Steps[0].Failure != nil && observed.Steps[0].Failure.Code == "CONFIRMATION_FACTS_UNAVAILABLE" && observed.Steps[0].Failure.Stage == "BUSINESS_STEP", "exact business step remains unresolved despite consumed confirmation", observed.Steps)
}

func (s *continuationGate) checkUnknownOptions(pending c.Intent, in c.CommitInput) {
	var out c.Result
	read := c.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash, RequestID: in.RequestID}
	gateDecode(s.t, s.call("gpt", "confirmation_options_get", read, ""), &out)
	s.check(out.Options != nil && out.Options.IntentID == pending.IntentID && !out.Options.Eligibility.Available && !out.Options.Eligibility.ReprepareAllowed && out.Options.Eligibility.RetryDisposition == string(c.RetryNone), "UNKNOWN options provide no confirm or reprepare authority", out)
	for _, variant := range out.Options.Variants {
		s.check(!variant.Available, "UNKNOWN prepared variant is unavailable", variant)
	}
}

func (s *continuationGate) exerciseJournalUnknown(token string) {
	view := s.checkCheckpoint(false, false)
	note := "Retain exact verified fixture evidence; not acceptance or permission to merge."
	pending := s.prepare(s.choice("DEFER_TASK", "journal-prepare-defer", note, int(view.ActionNode.Generation)))
	in := s.commitInput(pending, "journal-commit-once")
	s.checkCheckpoint(false, false)
	witness := s.injectChoiceReceiptRollback(token, pending, in)
	before := s.journalSnapshot(in)
	s.save("journal-unknown-before.json", before)
	observed := s.confirmationRead(pending, in.RequestID)
	s.checkUnknownChoice(observed, pending, in)
	s.checkUnknownOptions(pending, in)
	discarded := s.discardChoice(observed)
	s.checkUnknownChoice(discarded, pending, in)
	view = s.checkCheckpoint(false, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID, "UNKNOWN owns and blocks the original Task action", view.ActionNode)
	s.call("gpt", "execution_prepare", s.choice("CONTINUE_TASK", "journal-blocked-continue", "", int(view.ActionNode.Generation)), "TASK_ACTION_PENDING")
	s.check(reflect.DeepEqual(s.journalSnapshot(in), before), "warm receipt/options/discard/refused prepare do not repair or replace UNKNOWN", nil)
	// No confirmation_commit is retried, with either the old or a new request.
	s.cold()
	observed = s.confirmationRead(pending, in.RequestID)
	s.checkUnknownChoice(observed, pending, in)
	s.checkUnknownOptions(pending, in)
	view = s.checkCheckpoint(false, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID && view.ActionNode.Generation == before.Generation, "cold UNKNOWN retains original action owner/generation", view.ActionNode)
	after := s.journalSnapshot(in)
	s.check(reflect.DeepEqual(after, before) && reflect.DeepEqual(journalFaultCalls(token), witness), "cold reads preserve exact durable UNKNOWN and never re-enter the failed business step", after)
	s.save("journal-unknown-after.json", after)
	s.save("final-workspace.json", s.workspace())
}

// The ordinary scoped trigger reaches the real pooled writer only after Core
// durably journals this exact choice subrequest. It never fabricates a receipt.
func (s *continuationGate) injectChoiceReceiptRollback(token string, pending c.Intent, in c.CommitInput) [][]string {
	s.t.Helper()
	s.check(len(journalFaultCalls(token)) == 0, "fault witness untouched by real pipeline/bootstrap", nil)
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	// Ordinary trigger (not connection-local TEMP): the pooled real writer must
	// see it. Its step request comes from Core's ALREADY durable UNKNOWN step,
	// not a seeded receipt or caller-created authority. For CONTINUE, the
	// trigger also observes that this transaction has actually deleted the hold.
	fault := fmt.Sprintf(`CREATE TRIGGER devbridge_test_only_journal_fault BEFORE INSERT ON task_hold_receipts
 WHEN NEW.actor=%s AND NEW.task_id=%s AND NEW.execution_id=%s AND NEW.revision_id=%s AND NEW.action=%s
 AND (NEW.action!='CONTINUE_TASK' OR NOT EXISTS(SELECT 1 FROM task_holds WHERE task_id=NEW.task_id))
 AND NEW.request_id=(SELECT json_extract(intent_json,'$.steps[0].requestId') FROM confirmation_intents
 WHERE intent_id=%s AND ai_session_id=%s AND json_extract(intent_json,'$.state')='CONFIRMING'
 AND json_extract(intent_json,'$.steps[0].state')='UNKNOWN' AND json_extract(intent_json,'$.commit.requestId')=%s)
 BEGIN SELECT %s(%s,NEW.actor,NEW.request_id,NEW.task_id,NEW.execution_id,NEW.revision_id,NEW.action); END`, quote("session:"+s.f.session), quote(s.f.task), quote(s.executionID), quote(s.revision), quote(in.Action), quote(in.IntentID), quote(in.AISessionID), quote(in.RequestID), journalFaultFunction, quote(token))
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
		if _, err := s.f.plane.DB.SQL().ExecContext(ctx, `DROP TRIGGER devbridge_test_only_journal_fault`); err != nil {
			s.t.Errorf("remove test-only journal fault: %v", err)
			return
		}
		installed = false
	}
	defer drop()
	s.save("journal-fault.json", map[string]any{"faultSQL": fault, "faultError": journalFaultMessage, "scope": "known injected canonical-choice transaction rollback; not arbitrary crash/commit ambiguity", "noAuthorityRowsFabricated": true})
	var failed c.Result
	gateDecode(s.t, s.call("fixture_user", "confirmation_commit", in, "CONFIRMATION_FACTS_UNAVAILABLE"), &failed)
	s.check(failed.Intent != nil, "failed real business command retains original consumed intent", failed)
	s.checkUnknownChoice(*failed.Intent, pending, in)
	witness := journalFaultCalls(token)
	expected := []string{"session:" + s.f.session, failed.Intent.Steps[0].RequestID, s.f.task, s.executionID, s.revision, in.Action}
	s.check(len(witness) == 1 && reflect.DeepEqual(witness[0], expected) && expected[1] != in.RequestID, "fault fired exactly once at the original durable business subrequest", witness)
	s.save("journal-fault-witness.json", witness)
	drop()
	var triggers int
	if err := s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='devbridge_test_only_journal_fault'`).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(!installed && triggers == 0, "test fault removed before recovery observations and cold reopen", triggers)
	return witness
}
