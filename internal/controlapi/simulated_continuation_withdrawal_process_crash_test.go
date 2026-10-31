//go:build !windows

package controlapi_test

// A transaction-local withdrawal rejection is not durable negative authority.
// Kill the real Core after its INSERT and prove the same original uncertainty
// survives until a separately explicit withdrawal actually commits.

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
)

func TestSimulatedContinuationWithdrawalProcessCrash(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseWithdrawalProcessCrash)
}

func (s *continuationGate) exerciseWithdrawalProcessCrash() {
	// Establish the actual first SIGKILL and same Session/card/request checkpoint,
	// then interrupt its first withdrawal. The remaining lifecycle only sends a
	// late original commit AFTER the next explicit withdrawal durably seals it.
	s.exerciseConsumptionCrashLifecycle(s.crashWithdrawalFence)
}

func (s *continuationGate) crashWithdrawalFence(cp unconsumedCrashCheckpoint) {
	in := cp.Input
	before := s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in)
	s.check(reflect.DeepEqual(before, cp.Baseline) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(cp.Deferred, cp.DeferInput), cp.Business), "withdrawal crash starts from same genuinely dispatched original request with no durable consumption or rejection", before)
	s.save("withdrawal-process-crash-before.json", before)
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	// AFTER INSERT witnesses a real staged row, not an intent to reject or a
	// manually fabricated receipt. No callback re-entry or authority write occurs.
	ddl := fmt.Sprintf(`CREATE TRIGGER %s AFTER INSERT ON confirmation_commit_requests
 WHEN NEW.ai_session_id=%s AND NEW.intent_id=%s AND NEW.request_id=%s
 AND NEW.outcome='REJECTED_NOT_CONSUMED' AND NEW.submitted_sha256=%s
 AND json_extract(NEW.failure_json,'$.code')='CONFIRMATION_SUBMISSION_WITHDRAWN'
 AND json_extract(NEW.failure_json,'$.stage')='SUBMISSION_WITHDRAWAL'
 AND EXISTS(SELECT 1 FROM confirmation_intents WHERE ai_session_id=NEW.ai_session_id AND intent_id=NEW.intent_id
 AND json_extract(intent_json,'$.state')='PENDING' AND json_extract(intent_json,'$.commit') IS NULL
 AND json_array_length(json_extract(intent_json,'$.steps'))=0 AND json_extract(intent_json,'$.action')='CONTINUE_TASK')
 BEGIN SELECT %s(NEW.intent_id,NEW.ai_session_id,NEW.request_id,NEW.request_id,'CONTINUE_TASK',
 (SELECT intent_json FROM confirmation_intents WHERE ai_session_id=NEW.ai_session_id AND intent_id=NEW.intent_id),
 json_object('request',NEW.request_json,'submitted',NEW.submitted_sha256,'typedHash',NEW.payload_sha256,'outcome',NEW.outcome,'failure',NEW.failure_json,
 'selectedRequest',(SELECT request_json FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=NEW.request_id),
 'selectedOutcome',(SELECT outcome FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=NEW.request_id),
 'selectedSubmitted',(SELECT submitted_sha256 FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=NEW.request_id),
 'selectedTypedHash',(SELECT payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=NEW.request_id),
 'selectedFailure',(SELECT failure_json FROM confirmation_commit_requests WHERE ai_session_id=NEW.ai_session_id AND request_id=NEW.request_id),
 'requests',(SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=NEW.intent_id),
 'owner',(SELECT COALESCE(owner_intent_id,'') FROM task_action_heads WHERE task_id=%s),
 'generation',(SELECT generation FROM task_action_heads WHERE task_id=%s),
 'worker',(SELECT worker_epoch FROM confirmation_intents WHERE intent_id=NEW.intent_id),
 'releases',(SELECT COUNT(*) FROM task_action_releases WHERE intent_id=NEW.intent_id),
 'holds',(SELECT COUNT(*) FROM task_holds WHERE task_id=%s),
 'receipts',(SELECT COUNT(*) FROM task_hold_receipts WHERE task_id=%s),
 'events',(SELECT COUNT(*) FROM events WHERE task_id=%s AND type='task.verification_choice'))); END`, crashBarrierTrigger, quote(in.AISessionID), quote(in.IntentID), quote(in.RequestID), quote(c.ValueHash(in)), crashBarrierFunction, quote(s.f.task), quote(s.f.task), quote(s.f.task), quote(s.f.task), quote(s.f.task))
	fault := s.killCoreCommand(processCrashPlan{DataRoot: s.f.dataRoot, Evidence: s.evidence, Prefix: "withdrawal-process-crash", TriggerSQL: ddl, Scope: "owned Core SIGKILL after exact withdrawal rejection INSERT before RejectCommit transaction commit", Withdrawal: &in})
	var staged struct {
		Request, Submitted, TypedHash, Outcome, Failure, SelectedRequest, SelectedOutcome, SelectedSubmitted, SelectedTypedHash, SelectedFailure, Owner, Worker string
		Requests, Releases, Holds, Receipts, Events                                                                                                             int
		Generation                                                                                                                                              int64
	}
	gateDecode(s.t, json.RawMessage(fault.Paused.Witness[6]), &staged)
	var request c.CommitInput
	var failure c.Failure
	gateDecode(s.t, json.RawMessage(staged.Request), &request)
	gateDecode(s.t, json.RawMessage(staged.Failure), &failure)
	// payload_sha256 hashes typed struct JSON bytes, whereas submitted_sha256
	// canonicalizes JSON object keys. Internal hash/original-pointer fields have
	// json:"-" and must not be fabricated as part of either submitted payload.
	typed, err := json.Marshal(in)
	if err != nil {
		s.t.Fatal(err)
	}
	s.check(fault.Paused.Witness[3] == in.RequestID && fault.Paused.Witness[5] == before.Ledger.IntentJSON && reflect.DeepEqual(request, in) && staged.Submitted == c.ValueHash(in) && staged.TypedHash == fmt.Sprintf("%x", sha256.Sum256(typed)) && staged.Outcome == "REJECTED_NOT_CONSUMED" && staged.SelectedRequest == staged.Request && staged.SelectedOutcome == staged.Outcome && staged.SelectedSubmitted == staged.Submitted && staged.SelectedTypedHash == staged.TypedHash && staged.SelectedFailure == staged.Failure && staged.Requests == 1, "transaction witness proves exact original withdrawal rejection really inserted, not yet durable", staged)
	s.check(failure.Code == "CONFIRMATION_SUBMISSION_WITHDRAWN" && failure.Source == "CORE" && failure.Stage == "SUBMISSION_WITHDRAWAL" && failure.NextAction == c.EditSelection && staged.Owner == in.IntentID && staged.Generation == before.Ledger.Generation && staged.Worker == before.WorkerEpoch && staged.Releases == 0 && staged.Holds == 1 && staged.Receipts == 1 && staged.Events == 1, "staged negative authority retains original pending ownership/worker and genuine held business facts", staged)
	s.save("withdrawal-process-crash-transaction.json", staged)
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(s.f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	after := s.consumptionCrashSnapshot(db, in)
	s.check(reflect.DeepEqual(after, before) && reflect.DeepEqual(s.continueUnknownBusinessSnapshotFromDB(db, cp.Deferred, cp.DeferInput), cp.Business), "pre-Open second-crash rollback removes staged negative receipt and restores exact original pending ledger/hold", after)
	var triggers int
	if err = db.QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggers == 1, "withdrawal test barrier survives crash but uncommitted rejection does not", nil)
	s.save("withdrawal-process-crash-before-open.json", after)
	if err = db.Close(); err != nil {
		s.t.Fatal(err)
	}
	epoch := s.reopenKilledCore(fault)
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in), before), "new Core startup cannot turn staged withdrawal witness into durable authority", nil)
	if _, err = s.f.plane.DB.SQL().ExecContext(s.ctx, "DROP TRIGGER "+crashBarrierTrigger); err != nil {
		s.t.Fatal(err)
	}
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, crashBarrierTrigger).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(triggers == 0, "only scoped withdrawal barrier removed; no receipt or ownership repair", nil)
	s.install()
	s.connect()
	assertUnknown := func(label string) {
		s.t.Helper()
		s.checkCrashedUnobserved(s.confirmationRead(cp.Pending, in.RequestID), cp.Pending, in)
		view := s.checkCheckpoint(true, false)
		s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID && view.ActionNode.Generation == before.Ledger.Generation, label+": lost withdrawal does not release original pending ownership", view.ActionNode)
		s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in), before) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(cp.Deferred, cp.DeferInput), cp.Business), label+": original read supplies no rejection receipt or permission to resend", nil)
	}
	assertUnknown("warm second-crash read")
	s.cold()
	assertUnknown("cold second-crash read")
	s.call("gpt", "execution_prepare", s.choice("CONTINUE_TASK", "withdrawal-crash-blocked-proposal", "", int(before.Ledger.Generation)), "TASK_ACTION_PENDING")
	s.check(reflect.DeepEqual(s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in), before), "staged rejection cannot authorize a competing proposal", nil)
	for name, want := range fault.AgentLogs {
		got, err := os.ReadFile(filepath.Join(s.evidence, name))
		s.check(err == nil && bytes.Equal(got, want), "second actual crash and unknown readback add no Agent work: "+name, err)
	}
	s.save("withdrawal-process-crash-after-cold-read.json", s.consumptionCrashSnapshot(s.f.plane.DB.SQL(), in))
	s.save("withdrawal-process-crash-epochs.json", []string{fault.PriorEpoch, fault.ChildEpoch, epoch, s.f.plane.DB.RuntimeEpoch()})
	// Return SAME original identity to the established explicit-withdrawal path.
	// No business commit has been retried, and no negative proof was inferred.
}
