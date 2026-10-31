package controlapi_test

// Both real public calls overlap. The first real SQLite writer is paused before
// its ledger INSERT; this is not proof that the second writer reached its lock.

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
	"sync/atomic"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	driversqlite "modernc.org/sqlite"
)

const arbitrationFunction = "devbridge_test_only_submission_writer_barrier"
const arbitrationTimeout = "TEST_ONLY_SUBMISSION_WRITER_BARRIER_TIMEOUT"
const arbitrationCommit = "confirmation_commit"
const arbitrationWithdraw = "confirmation_submission_withdraw"

var arbitrationRegistration sync.Once
var arbitrationRegistrationError error
var arbitrationSequence atomic.Uint64
var arbitrationRegistry = struct {
	sync.Mutex
	states map[string]*continuationArbitration
}{states: make(map[string]*continuationArbitration)}

type arbitrationEvent struct {
	Sequence        int
	Kind, Operation string
}
type arbitrationCapture struct {
	Events     []arbitrationEvent
	Entries    map[string]int
	Witness    [][]string
	Mismatches int
	TimedOut   bool
}
type continuationArbitration struct {
	mu              sync.Mutex
	expected        c.CommitInput
	capture         arbitrationCapture
	paused, release chan struct{}
	entered         map[string]chan struct{}
	releaseOnce     sync.Once
}

func (a *continuationArbitration) eventLocked(kind, operation string) {
	a.capture.Events = append(a.capture.Events, arbitrationEvent{len(a.capture.Events) + 1, kind, operation})
}
func (a *continuationArbitration) observe(operation string, in c.CommitInput) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.capture.Entries[operation]++
	if c.ValueHash(in) != c.ValueHash(a.expected) {
		a.capture.Mismatches++
		a.eventLocked("mismatched_port_entry", operation)
		return
	}
	a.eventLocked("port_entry", operation)
	select {
	case <-a.entered[operation]:
	default:
		close(a.entered[operation])
	}
}
func (a *continuationArbitration) unblock() {
	a.releaseOnce.Do(func() { a.mu.Lock(); a.eventLocked("writer_release", ""); a.mu.Unlock(); close(a.release) })
}
func (a *continuationArbitration) snapshot() arbitrationCapture {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.capture
	out.Events = append([]arbitrationEvent(nil), a.capture.Events...)
	out.Entries = make(map[string]int)
	for k, v := range a.capture.Entries {
		out.Entries[k] = v
	}
	out.Witness = nil
	for _, v := range a.capture.Witness {
		out.Witness = append(out.Witness, append([]string(nil), v...))
	}
	return out
}

func TestSimulatedContinuationWithdrawArbitration(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent gate; run scripts/test-simulated-continuation.sh --withdraw-arbitration")
	}
	// Isolated nonparallel scenario only: modernc registration is process-global
	// and must precede fixture connections. No production hook is installed.
	arbitrationRegistration.Do(func() {
		arbitrationRegistrationError = driversqlite.RegisterScalarFunction(arbitrationFunction, 7, func(_ *driversqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
			args := make([]string, len(values))
			for i, v := range values {
				text, ok := v.(string)
				if !ok {
					return nil, errors.New("arbitration identity must be text")
				}
				args[i] = text
			}
			arbitrationRegistry.Lock()
			state := arbitrationRegistry.states[args[0]]
			arbitrationRegistry.Unlock()
			if state == nil {
				return nil, errors.New("arbitration token unavailable")
			}
			state.mu.Lock()
			state.capture.Witness = append(state.capture.Witness, append([]string(nil), args[1:]...))
			state.eventLocked("writer_paused", args[4])
			count := len(state.capture.Witness)
			if count == 1 {
				close(state.paused)
			}
			state.mu.Unlock()
			if count != 1 {
				return nil, errors.New("arbitration writer reached barrier more than once")
			}
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			// No DB re-entry, authority writes or testing calls. Success only follows
			// explicit release; timeout is an injected failure, NEVER a legal winner.
			select {
			case <-state.release:
				state.mu.Lock()
				state.eventLocked("writer_return", "")
				state.mu.Unlock()
				return nil, nil
			case <-timer.C:
				state.mu.Lock()
				state.capture.TimedOut = true
				state.eventLocked("writer_timeout", "")
				state.mu.Unlock()
				return nil, errors.New(arbitrationTimeout)
			}
		})
	})
	if arbitrationRegistrationError != nil {
		t.Fatal(arbitrationRegistrationError)
	}
	runSimulatedContinuationGate(t, (*continuationGate).exerciseWithdrawArbitration)
}

// Observation only: each call is forwarded synchronously and unchanged to the
// same real production service/Facade. This wrapper supplies no outcomes.
type arbitrationService struct {
	*mcpserver.StaticService
	state *continuationArbitration
}

func (s *arbitrationService) CommitConfirmation(ctx context.Context, in c.CommitInput) (c.Intent, error) {
	s.state.observe(arbitrationCommit, in)
	return s.StaticService.CommitConfirmation(ctx, in)
}
func (s *arbitrationService) WithdrawConfirmationSubmission(ctx context.Context, in c.CommitInput) (c.Intent, error) {
	s.state.observe(arbitrationWithdraw, in)
	return s.StaticService.WithdrawConfirmationSubmission(ctx, in)
}

type arbitrationCall struct {
	Tool                  string
	StartedAt, FinishedAt time.Time
	Result                *sdkmcp.CallToolResult
	Err                   error
}

func (s *continuationGate) arbitrateWithdrawal(in c.CommitInput, first, label string) (c.Result, c.Result) {
	s.t.Helper()
	second := arbitrationCommit
	outcome := "REJECTED_NOT_CONSUMED"
	if first == arbitrationCommit {
		second = arbitrationWithdraw
		outcome = "CONSUMED"
	}
	state := &continuationArbitration{expected: in, capture: arbitrationCapture{Entries: make(map[string]int)}, paused: make(chan struct{}), release: make(chan struct{}), entered: map[string]chan struct{}{arbitrationCommit: make(chan struct{}), arbitrationWithdraw: make(chan struct{})}}
	token := fmt.Sprintf("arbitration-%d", arbitrationSequence.Add(1))
	arbitrationRegistry.Lock()
	arbitrationRegistry.states[token] = state
	arbitrationRegistry.Unlock()
	defer func() {
		arbitrationRegistry.Lock()
		delete(arbitrationRegistry.states, token)
		arbitrationRegistry.Unlock()
	}()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test-only-arbitration", Ready: true}, s.f.plane.AIContext, s.f.facade)
	if err != nil {
		s.t.Fatal(err)
	}
	server, err := mcpserver.New(mcpserver.Config{RequestTimeout: continuationRequestTimeout}, &arbitrationService{service, state})
	if err != nil {
		s.t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	clients := make(map[string]*sdkmcp.ClientSession)
	for _, tool := range []string{arbitrationCommit, arbitrationWithdraw} {
		client, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fixture-user-" + tool, Version: "test-only"}, nil).Connect(s.ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", HTTPClient: host.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if err != nil {
			s.t.Fatal(err)
		}
		clients[tool] = client
		defer client.Close()
	}
	// Both clients are initialized before the exact barrier is armed.
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	ddl := fmt.Sprintf(`CREATE TRIGGER devbridge_test_only_arbitration BEFORE INSERT ON confirmation_commit_requests
 WHEN NEW.ai_session_id=%s AND NEW.intent_id=%s AND NEW.request_id=%s AND NEW.outcome=%s AND NEW.submitted_sha256=%s
 AND json_extract(NEW.request_json,'$.action')='DEFER_TASK'
 AND EXISTS(SELECT 1 FROM confirmation_intents WHERE intent_id=NEW.intent_id AND ai_session_id=NEW.ai_session_id AND json_extract(intent_json,'$.state')='PENDING' AND json_type(intent_json,'$.commit') IS NULL)
 BEGIN SELECT %s(%s,NEW.ai_session_id,NEW.intent_id,NEW.request_id,NEW.outcome,NEW.submitted_sha256,NEW.payload_sha256); END`, quote(in.AISessionID), quote(in.IntentID), quote(in.RequestID), quote(outcome), quote(c.ValueHash(in)), arbitrationFunction, quote(token))
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
		if _, err := s.f.plane.DB.SQL().ExecContext(ctx, `DROP TRIGGER devbridge_test_only_arbitration`); err != nil {
			s.t.Errorf("drop arbitration barrier: %v", err)
			return
		}
		installed = false
	}
	defer drop()
	s.save("arbitration-plan-"+label+".json", map[string]any{"triggerSQL": ddl, "first": first, "second": second, "expectedOutcome": outcome, "request": in, "scope": "first real writer paused; competing production service invocation overlaps; not second-writer lock-arrival proof", "callbackTimeoutSeconds": 3, "rendezvousTimeoutSeconds": 2, "phaseTimeoutSeconds": 10})
	phaseCtx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	results := make(chan arbitrationCall, 2)
	started, joined := 0, 0
	calls := make(map[string]arbitrationCall)
	// Cleanup always unblocks before cancel/join, then drops the trigger and
	// closes clients/server. Buffered results never strand a finishing goroutine.
	defer func() {
		// This finalizer also runs on rendezvous/join failure. Only the main
		// test goroutine writes the transcript; each joined result appears once.
		defer func() {
			for _, tool := range []string{first, second} {
				call, ok := calls[tool]
				if !ok {
					continue
				}
				entry := map[string]any{"startedAt": call.StartedAt, "finishedAt": call.FinishedAt, "durationMillis": call.FinishedAt.Sub(call.StartedAt).Milliseconds(), "actor": "fixture_user", "arbitrationPhase": label, "tool": tool, "input": in, "result": call.Result}
				if call.Err != nil {
					entry["transportError"] = call.Err.Error()
				}
				if err := json.NewEncoder(s.transcript).Encode(entry); err != nil {
					s.t.Errorf("retain arbitration response: %v", err)
				}
			}
			s.save("arbitration-trace-"+label+".json", state.snapshot())
			s.save("arbitration-joins-"+label+".json", map[string]int{"started": started, "joined": joined})
		}()
		state.unblock()
		cancel()
		timer := time.NewTimer(12 * time.Second)
		defer timer.Stop()
		for joined < started {
			select {
			case r := <-results:
				calls[r.Tool] = r
				joined++
			case <-timer.C:
				s.t.Error("arbitration client cleanup timeout")
				return
			}
		}
	}()
	start := func(tool string) {
		started++
		go func() {
			at := time.Now().UTC()
			result, err := clients[tool].CallTool(phaseCtx, &sdkmcp.CallToolParams{Name: tool, Arguments: in})
			results <- arbitrationCall{tool, at, time.Now().UTC(), result, err}
		}()
	}
	await := func(ready <-chan struct{}, what string) {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-ready:
			return
		case r := <-results:
			calls[r.Tool] = r
			joined++
			s.t.Fatalf("arbitration completed before %s: %s %v", what, r.Tool, r.Err)
		case <-timer.C:
			s.t.Fatalf("arbitration rendezvous timeout: %s", what)
		}
	}
	start(first)
	await(state.paused, "first writer pause")
	start(second)
	await(state.entered[second], "competing exact service entry")
	state.unblock()
	for joined < started {
		select {
		case r := <-results:
			calls[r.Tool] = r
			joined++
		case <-phaseCtx.Done():
			s.t.Fatalf("arbitration calls did not join: %v", phaseCtx.Err())
		}
	}
	capture := state.snapshot()
	index := func(kind, operation string) int {
		for _, event := range capture.Events {
			if event.Kind == kind && event.Operation == operation {
				return event.Sequence
			}
		}
		return 0
	}
	firstEntry, paused, secondEntry, released, returned := index("port_entry", first), index("writer_paused", outcome), index("port_entry", second), index("writer_release", ""), index("writer_return", "")
	s.check(capture.Entries[arbitrationCommit] == 1 && capture.Entries[arbitrationWithdraw] == 1 && capture.Mismatches == 0 && !capture.TimedOut && len(capture.Witness) == 1 && firstEntry > 0 && firstEntry < paused && paused < secondEntry && secondEntry < released && released < returned, "recorded barrier order proves two overlapping exact public calls and known first writer", capture)
	a, b := calls[first], calls[second]
	s.check(a.StartedAt.Before(b.FinishedAt) && b.StartedAt.Before(a.FinishedAt), "real client invocation intervals overlap, not merely race-detector instrumentation", calls)
	envelopes := make(map[string]c.Result)
	for _, tool := range []string{first, second} {
		call := calls[tool]
		s.check(call.Err == nil && call.Result != nil, "arbitration request has real production transport result: "+tool, call)
		var out c.Result
		gateDecode(s.t, call.Result.StructuredContent, &out)
		expectedError := first == arbitrationWithdraw && tool == arbitrationCommit
		s.check(call.Result.IsError == expectedError && out.Intent != nil && (!expectedError || out.Failure != nil && out.Failure.Code == "CONFIRMATION_SUBMISSION_WITHDRAWN"), "only the withdrawal-fenced commit is refused: "+tool, out)
		envelopes[tool] = out
	}
	var savedOutcome, submitted, typed, raw string
	var count int
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT outcome,submitted_sha256,payload_sha256,request_json FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=? AND request_id=?`, in.AISessionID, in.IntentID, in.RequestID).Scan(&savedOutcome, &submitted, &typed, &raw); err != nil {
		s.t.Fatal(err)
	}
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=?`, in.AISessionID, in.RequestID).Scan(&count); err != nil {
		s.t.Fatal(err)
	}
	var request c.CommitInput
	if err = json.Unmarshal([]byte(raw), &request); err != nil {
		s.t.Fatal(err)
	}
	s.check(count == 1 && savedOutcome == outcome && submitted == c.ValueHash(in) && reflect.DeepEqual(request, in) && reflect.DeepEqual(capture.Witness[0], []string{in.AISessionID, in.IntentID, in.RequestID, outcome, submitted, typed}), "one actual immutable ledger winner equals exact in-transaction witness", capture.Witness)
	drop()
	var triggers int
	if err = s.f.plane.DB.SQL().QueryRowContext(s.ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='devbridge_test_only_arbitration'`).Scan(&triggers); err != nil {
		s.t.Fatal(err)
	}
	s.check(!installed && triggers == 0, "barrier removed after both calls join, before cold readback", triggers)
	return envelopes[arbitrationCommit], envelopes[arbitrationWithdraw]
}

func (s *continuationGate) checkArbitrationCompleted(got c.Intent, in c.CommitInput) {
	s.t.Helper()
	s.check(got.State == "COMPLETED" && !got.CanResume && got.IntentID == in.IntentID && got.AISessionID == in.AISessionID && got.Version == in.Version && got.Purpose == in.Purpose && got.Action == in.Action && got.ContentHash == in.ContentHash && got.Commit != nil && reflect.DeepEqual(*got.Commit, in), "arbitration winner has exact completed original identity", got)
	sub := got.Submission
	s.check(sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.BusinessOutcome == "COMMITTED" && sub.ConsumptionDisposition == "CONSUMED" && sub.Consumed != nil && *sub.Consumed && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.RetryDisposition == c.RetryNone && sub.ReceiptRequestID == in.RequestID && sub.PayloadSHA256 == c.ValueHash(in) && reflect.DeepEqual(sub.Request, in), "winning request retains exact consumed receipt without retry authority", sub)
	s.check(len(got.Steps) == 1 && got.Steps[0].Kind == "RECORD_TASK_CHOICE" && got.Steps[0].State == "COMPLETED" && got.Steps[0].Effect == "COMMAND_COMPLETED" && got.Steps[0].Failure == nil && got.Steps[0].RunID == "" && got.Steps[0].TaskID == s.f.task && got.Steps[0].ExecutionID == s.executionID && got.Steps[0].RevisionID == s.revision, "winning business effect is one exact Task choice, never Run dispatch", got.Steps)
}

func (s *continuationGate) exerciseWithdrawArbitration() {
	view := s.checkCheckpoint(false, false)
	pending := s.prepare(s.choice("DEFER_TASK", "arbitration-prepare-defer", "Retain exact verified fixture evidence; not acceptance or permission to merge.", int(view.ActionNode.Generation)))
	rejectedInput := s.commitInput(pending, "arbitration-withdrawal-wins")
	commit, withdraw := s.arbitrateWithdrawal(rejectedInput, arbitrationWithdraw, "withdrawal-first")
	s.checkWithdrawnPending(*commit.Intent, pending, rejectedInput)
	s.checkWithdrawnPending(*withdraw.Intent, pending, rejectedInput)
	s.check(reflect.DeepEqual(commit.Intent.Submission, withdraw.Intent.Submission), "both overlapping callers see the same authoritative withdrawal winner", nil)
	sealed := s.submissionWithdrawalSnapshot(rejectedInput)
	s.check(sealed.IntentRequests == 1 && sealed.HoldsJSON == "[]" && sealed.ReceiptsJSON == "[]" && sealed.EventsJSON == "[]", "withdrawal winner produced no choice receipt, event or hold", sealed)
	s.save("arbitration-withdrawal-before.json", sealed)
	s.checkCheckpoint(false, false)
	s.cold()
	rejected := s.confirmationRead(pending, rejectedInput.RequestID)
	s.checkWithdrawnPending(rejected, pending, rejectedInput)
	after := s.submissionWithdrawalSnapshot(rejectedInput)
	s.check(reflect.DeepEqual(after, sealed), "cold original rejection preserves complete ledger and zero effects", after)
	s.save("arbitration-withdrawal-after.json", after)
	// Only the proven non-consumption fence permits this NEW explicit request.
	// Same prepared intent, different request: no consumed/unknown commit retry.
	consumedInput := s.commitInput(pending, "arbitration-commit-wins")
	commit, withdraw = s.arbitrateWithdrawal(consumedInput, arbitrationCommit, "commit-first")
	deferred := *commit.Intent
	s.checkArbitrationCompleted(deferred, consumedInput)
	observed := withdraw.Intent
	sub := observed.Submission
	s.check((observed.State == "CONFIRMING" || observed.State == "COMPLETED") && observed.Commit != nil && reflect.DeepEqual(observed.Commit, deferred.Commit) && observed.IntentID == pending.IntentID && sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.Consumed != nil && *sub.Consumed && sub.ReceiptRequestID == consumedInput.RequestID && sub.PayloadSHA256 == c.ValueHash(consumedInput) && reflect.DeepEqual(sub.Request, consumedInput) && !sub.RedispatchSameIntent && sub.RetryDisposition == c.RetryNone, "racing withdrawal reads original consumed result, possibly before business completion", observed)
	s.checkCheckpoint(true, false)
	business := s.continueUnknownBusinessSnapshot(deferred, consumedInput)
	completed := s.submissionWithdrawalSnapshot(consumedInput)
	s.check(completed.IntentRequests == 2, "exact old withdrawn request and new consumed request coexist without duplicate consumption", completed)
	s.save("arbitration-consumed-before.json", completed)
	final := s.confirmationRead(pending, consumedInput.RequestID)
	s.checkArbitrationCompleted(final, consumedInput)
	s.cold()
	final = s.confirmationRead(pending, consumedInput.RequestID)
	s.checkArbitrationCompleted(final, consumedInput)
	old := s.confirmationRead(pending, rejectedInput.RequestID)
	expected := *rejected.Submission
	expected.NextAction = c.ReadCurrentTarget
	expected.RetryDisposition = c.RetryReprepareRequired
	expected.ReprepareAllowed = false
	s.check(reflect.DeepEqual(old.Submission, &expected), "old withdrawal receipt keeps its own non-consumption facts after new winner", old)
	s.checkCheckpoint(true, false)
	after = s.submissionWithdrawalSnapshot(consumedInput)
	s.check(reflect.DeepEqual(after, completed) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, consumedInput), business), "warm/cold readback preserves exact winning ledger, one original hold/receipt/event and no extra Runs", after)
	s.save("arbitration-consumed-after.json", after)
	s.save("final-workspace.json", s.workspace())
}
