package controlapi_test

// Cancel the actual caller while Core's cancellation-shielded final journal
// write is paused AFTER the real business commit. This is not a process crash.

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	driversqlite "modernc.org/sqlite"
)

const cancelLateFunction = "devbridge_test_only_cancel_late_barrier"
const cancelLateTimeout = "TEST_ONLY_CANCEL_LATE_BARRIER_TIMEOUT"

var cancelLateRegistration sync.Once
var cancelLateRegistrationError error
var cancelLateRegistry = struct {
	sync.Mutex
	state *cancelLateState
}{}

type cancelLateEvent struct {
	Sequence int
	Kind     string
	At       time.Time
}
type cancelLateCapture struct {
	Events                                        []cancelLateEvent
	Entries, Mismatches, Returns, ObserversJoined int
	Witness                                       [][]string
	TimedOut                                      bool
	ServerIntent                                  c.Intent
	ServerError                                   string
	ServerFailure                                 *c.Failure
}
type cancelLateState struct {
	canceledOnce, returnedOnce          sync.Once
	mu                                  sync.Mutex
	expected                            c.CommitInput
	capture                             cancelLateCapture
	paused, release, canceled, returned chan struct{}
	releaseOnce                         sync.Once
}

func (s *cancelLateState) eventLocked(kind string) {
	s.capture.Events = append(s.capture.Events, cancelLateEvent{len(s.capture.Events) + 1, kind, time.Now().UTC()})
}
func (s *cancelLateState) event(kind string) { s.mu.Lock(); defer s.mu.Unlock(); s.eventLocked(kind) }
func (s *cancelLateState) unblock() {
	s.releaseOnce.Do(func() { s.event("writer_release"); close(s.release) })
}
func (s *cancelLateState) snapshot() cancelLateCapture {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.capture
	out.Events = append([]cancelLateEvent(nil), out.Events...)
	out.Witness = nil
	for _, w := range s.capture.Witness {
		out.Witness = append(out.Witness, append([]string(nil), w...))
	}
	return out
}

func TestSimulatedContinuationCancelLateCompletion(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent gate; run scripts/test-simulated-continuation.sh --cancel-late-completion")
	}
	// Isolated, nonparallel script selection only: modernc registration is global
	// and must happen before opening fixture connections.
	cancelLateRegistration.Do(func() {
		cancelLateRegistrationError = driversqlite.RegisterScalarFunction(cancelLateFunction, 7, func(_ *driversqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
			args := make([]string, len(values))
			for i, v := range values {
				var ok bool
				args[i], ok = v.(string)
				if !ok {
					return nil, errors.New("cancel-late witness must be text")
				}
			}
			cancelLateRegistry.Lock()
			state := cancelLateRegistry.state
			cancelLateRegistry.Unlock()
			if state == nil {
				return nil, errors.New("cancel-late state unavailable")
			}
			state.mu.Lock()
			state.capture.Witness = append(state.capture.Witness, args)
			state.eventLocked("writer_paused")
			count := len(state.capture.Witness)
			if count == 1 {
				close(state.paused)
			}
			state.mu.Unlock()
			if count != 1 {
				return nil, errors.New("cancel-late writer entered more than once")
			}
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			// No DB re-entry, authority writes or testing calls from this callback.
			select {
			case <-state.release:
				state.event("writer_return")
				return nil, nil
			case <-timer.C:
				state.mu.Lock()
				state.capture.TimedOut = true
				state.eventLocked("writer_timeout")
				state.mu.Unlock()
				return nil, errors.New(cancelLateTimeout)
			}
		})
	})
	if cancelLateRegistrationError != nil {
		t.Fatal(cancelLateRegistrationError)
	}
	runSimulatedContinuationGate(t, (*continuationGate).exerciseCancelLateCompletion)
}

// Observe only. The unchanged public input and actual request context are
// forwarded to the real service; server results are oracle-only, not recovery inputs.
type cancelLateService struct {
	*mcpserver.StaticService
	state *cancelLateState
}

func (s *cancelLateService) CommitConfirmation(ctx context.Context, in c.CommitInput) (c.Intent, error) {
	state := s.state
	state.mu.Lock()
	state.capture.Entries++
	if c.ValueHash(in) != c.ValueHash(state.expected) {
		state.capture.Mismatches++
	}
	state.eventLocked("service_entry")
	state.mu.Unlock()
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			state.event("server_context_canceled")
			state.canceledOnce.Do(func() { close(state.canceled) })
		case <-stop:
		}
	}()
	out, err := s.StaticService.CommitConfirmation(ctx, in)
	close(stop)
	<-joined
	state.mu.Lock()
	state.capture.Returns++
	state.capture.ObserversJoined++
	// Keep oracle-only output detached from subsequent adapter normalization.
	raw, marshalErr := json.Marshal(out)
	if marshalErr == nil {
		marshalErr = json.Unmarshal(raw, &state.capture.ServerIntent)
	}
	if marshalErr != nil {
		state.capture.Mismatches++
	}
	if err != nil {
		state.capture.ServerError = err.Error()
		var failure *c.Failure
		if errors.As(err, &failure) {
			copy := *failure
			state.capture.ServerFailure = &copy
		}
	}
	state.eventLocked("service_return")
	state.mu.Unlock()
	state.returnedOnce.Do(func() { close(state.returned) })
	return out, err
}

func (s *continuationGate) exerciseCancelLateCompletion() {
	view := s.checkCheckpoint(false, false)
	pending := s.prepare(s.choice("DEFER_TASK", "cancel-late-prepare-defer", "Retain exact verified fixture evidence; not acceptance or permission to merge.", int(view.ActionNode.Generation)))
	in := s.commitInput(pending, "cancel-late-commit-once")
	state := &cancelLateState{expected: in, paused: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{}), returned: make(chan struct{})}
	cancelLateRegistry.Lock()
	cancelLateRegistry.state = state
	cancelLateRegistry.Unlock()
	defer func() { cancelLateRegistry.Lock(); cancelLateRegistry.state = nil; cancelLateRegistry.Unlock() }()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test-only-cancel-late", Ready: true}, s.f.plane.AIContext, s.f.facade)
	if err != nil {
		s.t.Fatal(err)
	}
	server, err := mcpserver.New(mcpserver.Config{RequestTimeout: continuationRequestTimeout}, &cancelLateService{service, state})
	if err != nil {
		s.t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	client, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fixture-user-cancel-late", Version: "test-only"}, nil).Connect(s.ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", HTTPClient: host.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	defer client.Close()
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	ddl := fmt.Sprintf(`CREATE TRIGGER devbridge_test_only_cancel_late BEFORE UPDATE OF intent_json ON confirmation_intents
 WHEN NEW.intent_id=%s AND NEW.ai_session_id=%s
 AND json_extract(OLD.intent_json,'$.state')='CONFIRMING' AND json_extract(OLD.intent_json,'$.steps[0].state')='UNKNOWN'
 AND json_extract(NEW.intent_json,'$.state')='COMPLETED' AND json_array_length(json_extract(NEW.intent_json,'$.steps'))=1
 AND json_extract(NEW.intent_json,'$.steps[0].state')='COMPLETED' AND json_extract(NEW.intent_json,'$.steps[0].effect')='COMMAND_COMPLETED'
 AND json_extract(NEW.intent_json,'$.steps[0].kind')='RECORD_TASK_CHOICE' AND json_extract(NEW.intent_json,'$.action')='DEFER_TASK'
 AND json_extract(OLD.intent_json,'$.commit.requestId')=%s AND json_extract(NEW.intent_json,'$.commit.requestId')=%s
 AND json_extract(OLD.intent_json,'$.steps[0].requestId')=json_extract(NEW.intent_json,'$.steps[0].requestId')
 AND EXISTS(SELECT 1 FROM task_hold_receipts WHERE actor=%s AND request_id=json_extract(NEW.intent_json,'$.steps[0].requestId') AND task_id=%s AND execution_id=%s AND revision_id=%s AND action='DEFER_TASK')
 BEGIN SELECT %s(NEW.intent_id,NEW.ai_session_id,json_extract(NEW.intent_json,'$.commit.requestId'),json_extract(NEW.intent_json,'$.steps[0].requestId'),json_extract(NEW.intent_json,'$.action'),OLD.intent_json,NEW.intent_json); END`, quote(in.IntentID), quote(in.AISessionID), quote(in.RequestID), quote(in.RequestID), quote("session:"+in.AISessionID), quote(s.f.task), quote(s.executionID), quote(s.revision), cancelLateFunction)
	if _, err = s.f.plane.DB.SQL().ExecContext(s.ctx, ddl); err != nil {
		s.t.Fatal(err)
	}
	installed := true
	drop := func() {
		if !installed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := s.f.plane.DB.SQL().ExecContext(ctx, `DROP TRIGGER devbridge_test_only_cancel_late`); err != nil {
			s.t.Errorf("drop cancel-late barrier: %v", err)
			return
		}
		installed = false
	}
	defer drop()
	s.save("cancel-late-plan.json", map[string]any{"triggerSQL": ddl, "request": in, "scope": "actual request cancellation after business commit, before cancellation-shielded final journal save; not crash, rollback or early cancellation", "callbackTimeoutSeconds": 5, "rendezvousTimeoutSeconds": 2, "phaseTimeoutSeconds": 10})
	phaseCtx, phaseCancel := context.WithTimeout(s.ctx, 10*time.Second)
	callCtx, callCancel := context.WithCancel(phaseCtx)
	results := make(chan arbitrationCall, 1)
	var call *arbitrationCall
	// Always release first. Join both caller and real service (which joins its
	// observer) before dropping the trigger or closing clients/Core. Retain partial
	// trace and joined response on every failure, never convert timeout to success.
	defer func() {
		defer func() {
			s.save("cancel-late-trace.json", state.snapshot())
			s.save("cancel-late-joins.json", map[string]bool{"clientJoined": call != nil, "serviceJoined": cancelLateChannelClosed(state.returned)})
			if call != nil {
				entry := map[string]any{"actor": "fixture_user", "tool": "confirmation_commit", "input": in, "startedAt": call.StartedAt, "finishedAt": call.FinishedAt, "result": call.Result}
				if call.Err != nil {
					entry["transportError"] = call.Err.Error()
				}
				if err := json.NewEncoder(s.transcript).Encode(entry); err != nil {
					s.t.Errorf("retain canceled response: %v", err)
				}
			}
		}()
		state.unblock()
		callCancel()
		phaseCancel()
		timer := time.NewTimer(12 * time.Second)
		defer timer.Stop()
		if call == nil {
			select {
			case r := <-results:
				call = &r
			case <-timer.C:
				s.t.Error("cancel-late client cleanup timeout")
				return
			}
		}
		select {
		case <-state.returned:
		case <-timer.C:
			s.t.Error("cancel-late service cleanup timeout")
		}
	}()
	go func() {
		at := time.Now().UTC()
		result, err := client.CallTool(callCtx, &sdkmcp.CallToolParams{Name: "confirmation_commit", Arguments: in})
		state.event("client_return")
		results <- arbitrationCall{"confirmation_commit", at, time.Now().UTC(), result, err}
	}()
	await := func(ready <-chan struct{}, what string) {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-ready:
		case <-timer.C:
			s.t.Fatalf("cancel-late rendezvous timeout: %s", what)
		}
	}
	await(state.paused, "real final journal writer pause")
	// Independent WAL reads observe the actual prior journal and already-committed
	// receipt/hold/event, while the final writer is still paused, not after release.
	canonical := s.journalRecoverySnapshot(in, 1)
	before := s.submissionWithdrawalSnapshot(in)
	s.save("cancel-late-before.json", before)
	s.save("cancel-late-canonical.json", canonical)
	s.check(!cancelLateChannelClosed(state.returned), "real service has not returned before caller cancellation", nil)
	state.event("client_cancel_issued")
	callCancel()
	select {
	case r := <-results:
		call = &r
	case <-phaseCtx.Done():
		s.t.Fatalf("canceled client did not join: %v", phaseCtx.Err())
	}
	s.check(call.Result == nil && errors.Is(call.Err, context.Canceled), "client sees cancellation with no usable result, not business failure or retry authority", call)
	await(state.canceled, "propagated real server-context cancellation")
	s.check(!cancelLateChannelClosed(state.returned), "server remains in real final persistence after caller returned canceled", nil)
	state.unblock()
	select {
	case <-state.returned:
	case <-phaseCtx.Done():
		s.t.Fatalf("late real service did not join: %v", phaseCtx.Err())
	}
	capture := state.snapshot()
	order := map[string]int{}
	for _, event := range capture.Events {
		order[event.Kind] = event.Sequence
	}
	s.check(capture.Entries == 1 && capture.Returns == 1 && capture.ObserversJoined == 1 && capture.Mismatches == 0 && !capture.TimedOut && len(capture.Witness) == 1 && order["service_entry"] > 0 && order["service_entry"] < order["writer_paused"] && order["writer_paused"] < order["client_cancel_issued"] && order["client_cancel_issued"] < order["server_context_canceled"] && order["server_context_canceled"] < order["writer_release"] && order["client_cancel_issued"] < order["client_return"] && order["client_return"] < order["writer_release"] && order["writer_release"] < order["writer_return"] && order["writer_return"] < order["service_return"], "actual cancellation and late persistence have a recorded causal order, not timing guesses", capture)
	witness := capture.Witness[0]
	s.check(len(witness) == 7 && reflect.DeepEqual(witness[:5], []string{in.IntentID, in.AISessionID, in.RequestID, canonical.ReceiptRequest, in.Action}) && witness[5] == before.IntentJSON, "exact one final-save witness equals independently observed pre-release journal", witness)
	after := s.submissionWithdrawalSnapshot(in)
	s.save("cancel-late-after.json", after)
	s.check(after.IntentJSON == witness[6], "raw durable journal equals actual attempted COMPLETED save after cancellation", after)
	expected := before
	expected.IntentJSON = after.IntentJSON
	// Production task_action_confirmation_settled releases this terminal intent and
	// advances its generation in the same real journal transaction.
	expected.Owner = ""
	expected.Generation++
	s.check(before.Owner == in.IntentID && reflect.DeepEqual(after, expected), "late save completes journal and releases owner once; immutable submission/hold/receipt/event unchanged", after)
	s.check(capture.ServerIntent.State == "COMPLETED" && capture.ServerIntent.Commit != nil && reflect.DeepEqual(*capture.ServerIntent.Commit, in) && len(capture.ServerIntent.Steps) == 1 && capture.ServerIntent.Steps[0].State == "COMPLETED" && capture.ServerFailure != nil && capture.ServerFailure.Code == "CONFIRMATION_REQUEST_INTERRUPTED" && capture.ServerFailure.Stage == "RECEIPT_READBACK", "server separately records late COMPLETED plus canceled receipt-readback failure, not a delivered success", capture)
	// No dedicated old-Core endpoint remains alive during subsequent cold reopen.
	_ = client.Close()
	host.Close()
	drop()
	var triggers int
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='devbridge_test_only_cancel_late'`).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(!installed && triggers == 0, "cancel-late barrier removed only after both real calls joined", triggers)
	// Recovery uses only the identity frozen BEFORE dispatch, never the oracle-only
	// server result or a resent commit. A fresh unaffected public client reads it.
	got := s.confirmationRead(pending, in.RequestID)
	s.checkCancelLateCompleted(got, pending, in, canonical)
	s.checkCheckpoint(true, false)
	s.check(reflect.DeepEqual(s.submissionWithdrawalSnapshot(in), after), "warm pure readback preserves final persisted journal and genuine effect", nil)
	s.cold()
	cold := s.confirmationRead(pending, in.RequestID)
	s.checkCancelLateCompleted(cold, pending, in, canonical)
	s.check(reflect.DeepEqual(cold.Commit, got.Commit) && reflect.DeepEqual(cold.Steps, got.Steps) && reflect.DeepEqual(cold.Submission, got.Submission), "cold original-identity read preserves the same completed consumed receipt", cold)
	s.checkCheckpoint(true, false)
	final := s.submissionWithdrawalSnapshot(in)
	s.check(reflect.DeepEqual(final, after), "cold reopen does not redispatch, repair or replace late completion", final)
	s.save("cancel-late-cold.json", final)
	s.save("final-workspace.json", s.workspace())
}

func cancelLateChannelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
func (s *continuationGate) checkCancelLateCompleted(got, pending c.Intent, in c.CommitInput, canonical journalRecoverySnapshot) {
	s.check(got.State == "COMPLETED" && !got.CanResume && got.IntentID == pending.IntentID && got.AISessionID == in.AISessionID && got.Version == pending.Version && got.Purpose == pending.Purpose && got.Action == pending.Action && got.ContentHash == pending.ContentHash && reflect.DeepEqual(got.Content, pending.Content) && got.Commit != nil && reflect.DeepEqual(*got.Commit, in), "readback identifies exact original late-completed choice", got)
	sub := got.Submission
	s.check(sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.BusinessOutcome == "COMMITTED" && sub.Consumed != nil && *sub.Consumed && sub.ConsumptionDisposition == "CONSUMED" && sub.RetryDisposition == c.RetryNone && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.NextAction == c.ReadOriginalReceipt && sub.ReceiptRequestID == in.RequestID && sub.PayloadSHA256 == c.ValueHash(in) && reflect.DeepEqual(sub.Request, in), "canceled call is reconciled by exact consumed receipt, never retry authority", sub)
	s.check(len(got.Steps) == 1 && got.Steps[0].Kind == "RECORD_TASK_CHOICE" && got.Steps[0].State == "COMPLETED" && got.Steps[0].Effect == "COMMAND_COMPLETED" && got.Steps[0].Failure == nil && got.Steps[0].RequestID == canonical.ReceiptRequest && got.Steps[0].TaskID == s.f.task && got.Steps[0].ExecutionID == s.executionID && got.Steps[0].RevisionID == s.revision && got.Steps[0].RunID == "", "late completed step matches pre-cancellation canonical receipt without Run dispatch", got.Steps)
}
