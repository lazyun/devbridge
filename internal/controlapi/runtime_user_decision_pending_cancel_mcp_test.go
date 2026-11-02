package controlapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type pendingCancelTarget struct{ Project, Owner, Run string }
type pendingCancelClaimResult struct {
	Target   executioncancel.Target
	Dispatch bool
	Err      error
}

// Embed the concrete store, not its narrow interface: optional finalization and
// eligibility methods remain promoted. Every durable operation uses real SQL.
type pendingCancelStore struct {
	*sqlite.ExecutionCancellationStore
	target     atomic.Pointer[pendingCancelTarget]
	receipt    atomic.Pointer[executioncancel.Receipt]
	begins     atomic.Int32
	dispatches atomic.Int32
	claims     atomic.Int32
	hit        atomic.Bool
	entered    chan struct{}
	release    chan struct{}
	once       sync.Once
	claimed    chan pendingCancelClaimResult
	acked      chan error
}

func (s *pendingCancelStore) unblock() { s.once.Do(func() { close(s.release) }) }
func (s *pendingCancelStore) Begin(ctx context.Context, in executioncancel.Input, live bool) (executioncancel.Receipt, bool, error) {
	r, dispatch, err := s.ExecutionCancellationStore.Begin(ctx, in, live)
	target := s.target.Load()
	normalized := in.Normalized()
	if target != nil && normalized.ProjectID == target.Project && normalized.OwnerType == "EXECUTION" && normalized.OwnerID == target.Owner && normalized.RunID == target.Run {
		s.begins.Add(1)
		if err == nil && dispatch {
			s.dispatches.Add(1)
			copy := r
			s.receipt.CompareAndSwap(nil, &copy)
		}
	}
	return r, dispatch, err
}
func (s *pendingCancelStore) ClaimInterrupt(ctx context.Context, project, request string) (executioncancel.Target, bool, error) {
	expected := s.receipt.Load()
	if expected != nil && expected.ProjectID == project && expected.RequestID == request {
		s.claims.Add(1)
		if s.hit.CompareAndSwap(false, true) {
			close(s.entered)
			timer := time.NewTimer(15 * time.Second)
			defer timer.Stop()
			select {
			case <-s.release:
			case <-timer.C:
				return executioncancel.Target{}, false, errors.New("TEST_ONLY_PENDING_CANCEL_GATE_TIMEOUT")
			}
			target, dispatch, err := s.ExecutionCancellationStore.ClaimInterrupt(ctx, project, request)
			s.claimed <- pendingCancelClaimResult{target, dispatch, err}
			return target, dispatch, err
		}
	}
	return s.ExecutionCancellationStore.ClaimInterrupt(ctx, project, request)
}
func (s *pendingCancelStore) RecordInterrupt(ctx context.Context, project, request, state string) error {
	err := s.ExecutionCancellationStore.RecordInterrupt(ctx, project, request, state)
	expected := s.receipt.Load()
	if expected != nil && expected.ProjectID == project && expected.RequestID == request && state == "ACKNOWLEDGED" {
		s.acked <- err
	}
	return err
}

// Real Local MCP + Core/SQLite + explicitly simulated protocol Agent. The gate
// pauses the asynchronous Claim before opening its transaction; pending replays
// can reach real Begin without holding DB, runtime Session or supervisor locks.
func TestRuntimeUserDecisionPendingCancellationExactReplayLocalMCP(t *testing.T) {
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_RESPONSE_LOSS", "DEVBRIDGE_CODING_V1_MULTI_QUESTION", "DEVBRIDGE_CODING_V1_WITH_APPROVAL", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS", "DEVBRIDGE_CODING_V1_LATE_RESOLVED_FILE"} {
		t.Setenv(key, "")
	}
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
	var gate *pendingCancelStore
	runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, executionID string) bool {
		f.plane.Retirement.Close()
		pending, runBefore := waitUserRequest(t, f, f.task, "turn-implementation")
		if pending.ExecutionID != executionID || runBefore.ExecutionProposalID != executionID {
			t.Fatal("wrong ordinary Execution question")
		}
		live, ok := f.plane.Supervisor.GetSession(pending.RunID)
		if !ok {
			t.Fatal("missing live runtime pipeline")
		}
		gate.target.Store(&pendingCancelTarget{pending.ProjectID, executionID, pending.RunID})
		ctx, cancel := context.WithTimeout(f.ctx, 40*time.Second)
		defer func() { gate.unblock(); cancel() }()
		client := newApprovedStartLocalClient(t, f)
		dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
		ro, e := sql.Open("sqlite", dsn)
		if e != nil {
			t.Fatal(e)
		}
		defer ro.Close()
		IDs := func(table string) []string {
			t.Helper()
			rows, e := ro.QueryContext(ctx, "SELECT id FROM "+table+" WHERE task_id=? ORDER BY id", f.task)
			if e != nil {
				t.Fatal(e)
			}
			defer rows.Close()
			ids := []string{}
			for rows.Next() {
				var id string
				if e := rows.Scan(&id); e != nil {
					t.Fatal(e)
				}
				ids = append(ids, id)
			}
			if e := rows.Err(); e != nil {
				t.Fatal(e)
			}
			return ids
		}
		initialRuns, initialProposals := IDs("runs"), IDs("execution_proposals")
		count := func(table string) int {
			t.Helper()
			var n int
			if e := ro.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE run_id=?", pending.RunID).Scan(&n); e != nil {
				t.Fatal(e)
			}
			return n
		}
		wire := func() []byte {
			t.Helper()
			b, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
			if e != nil {
				t.Fatal(e)
			}
			return b
		}
		var calls []map[string]any
		call := func(name string, args map[string]any, out any) {
			t.Helper()
			result, e := client.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
			if e != nil {
				t.Fatalf("%s transport: %v", name, e)
			}
			if result == nil || result.IsError {
				t.Fatalf("%s unexpected tool error: %+v", name, result)
			}
			b, e := json.Marshal(result.StructuredContent)
			if e != nil {
				t.Fatal(e)
			}
			calls = append(calls, map[string]any{"tool": name, "arguments": args, "result": json.RawMessage(b)})
			if e = json.Unmarshal(b, out); e != nil {
				t.Fatal(e)
			}
		}
		draft := agentruntime.UserDecisionInput{Identity: pending.Identity, RuntimeRequestID: pending.ID, SubmissionRequestID: "explicit-pending-cancel", ExpectedState: "WAITING_FOR_USER", Action: "CANCEL_OPERATION", SourceSurface: "CHATGPT"}
		args := func(d agentruntime.UserDecisionInput) map[string]any {
			return map[string]any{"aiSessionId": f.session, "decision": d}
		}
		var prepared struct {
			Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
		}
		call("runtime_request_user_decision_prepare_hash", args(draft), &prepared)
		choice := prepared.Result.Decision
		if prepared.Result.Recorded || choice.Identity != pending.Identity || choice.Action != "CANCEL_OPERATION" || choice.PayloadSHA256 == "" || choice.SubmittedBy != "" || count("runtime_user_decisions") != 0 {
			t.Fatal("prepare changed or recorded choice")
		}
		var first struct {
			Result controlapi.AgentUserDecisionResult `json:"result"`
		}
		call("runtime_request_user_decision_submit", args(choice), &first)
		select {
		case <-gate.entered:
		case <-ctx.Done():
			t.Fatal("real exact Claim did not reach gate")
		}
		captured := gate.receipt.Load()
		if captured == nil || first.Result.Cancellation == nil || !reflect.DeepEqual(*first.Result.Cancellation, *captured) || captured.State != "CANCEL_REQUESTED" || captured.BackendInterruptState != "NOT_SENT" || captured.OwnerID != executionID || captured.RunID != pending.RunID {
			t.Fatalf("first call not exact pending receipt: %+v", first)
		}
		type cancellationRow struct{ ProjectID, RequestID, OwnerType, OwnerID, RunID, State, InterruptState, Epoch string }
		row := func() cancellationRow {
			t.Helper()
			var v cancellationRow
			if e := ro.QueryRowContext(ctx, `SELECT project_id,request_id,owner_type,owner_id,run_id,state,interrupt_state,runtime_epoch FROM execution_cancellations WHERE project_id=? AND request_id=?`, pending.ProjectID, captured.RequestID).Scan(&v.ProjectID, &v.RequestID, &v.OwnerType, &v.OwnerID, &v.RunID, &v.State, &v.InterruptState, &v.Epoch); e != nil {
				t.Fatal(e)
			}
			return v
		}
		pendingRow := row()
		if pendingRow.OwnerType != "EXECUTION" || pendingRow.OwnerID != executionID || pendingRow.RunID != pending.RunID || pendingRow.Epoch != pending.RuntimeEpoch || pendingRow.State != "CANCEL_REQUESTED" || pendingRow.InterruptState != "NOT_SENT" || count("execution_cancellations") != 1 {
			t.Fatal("SQL did not prove original committed pending receipt")
		}
		var decisionJSON string
		if e := ro.QueryRowContext(ctx, `SELECT decision_json FROM runtime_user_decisions WHERE runtime_request_id=?`, pending.ID).Scan(&decisionJSON); e != nil {
			t.Fatal(e)
		}
		var decision agentruntime.UserDecision
		if e := json.Unmarshal([]byte(decisionJSON), &decision); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(decision, first.Result.Decision) || decision.Action != "CANCEL_OPERATION" || decision.PayloadSHA256 != choice.PayloadSHA256 || decision.SubmittedBy != "session:"+f.session || count("runtime_user_decisions") != 1 || count("runtime_response_dispatch_attempts") != 0 || bytes.Contains(wire(), []byte("user-response/")) || bytes.Contains(wire(), []byte("turn/interrupt")) {
			t.Fatal("pending cancellation manufactured response/interrupt or changed decision")
		}
		for i := 0; i < 2; i++ {
			var replay struct {
				Result controlapi.AgentUserDecisionResult `json:"result"`
			}
			call("runtime_request_user_decision_submit", args(choice), &replay)
			if !reflect.DeepEqual(replay.Result.Decision, decision) || replay.Result.Cancellation == nil || !reflect.DeepEqual(*replay.Result.Cancellation, *captured) {
				t.Fatalf("pending replay changed choice/receipt: %+v", replay)
			}
			if gate.begins.Load() != int32(i+2) || gate.dispatches.Load() != 1 || gate.claims.Load() != 1 || row() != pendingRow || count("runtime_response_dispatch_attempts") != 0 || bytes.Contains(wire(), []byte("turn/interrupt")) || bytes.Contains(wire(), []byte("user-response/")) {
				t.Fatal("pending replay did not only reobserve original cancellation")
			}
		}
		gate.unblock()
		var claimed pendingCancelClaimResult
		select {
		case claimed = <-gate.claimed:
		case <-ctx.Done():
			t.Fatal("real Claim did not complete")
		}
		if claimed.Err != nil || !claimed.Dispatch || claimed.Target.RunID != pending.RunID || claimed.Target.RuntimeEpoch != pending.RuntimeEpoch || claimed.Target.ThreadID != pending.ThreadID || claimed.Target.TurnID != pending.TurnID {
			t.Fatalf("not the real exact interrupt claim: %+v", claimed)
		}
		select {
		case e := <-gate.acked:
			if e != nil {
				t.Fatal(e)
			}
		case <-ctx.Done():
			t.Fatal("actual interrupt ACK was not persisted")
		}
		// RecordInterrupt has returned from genuine SQL and the original managed
		// pipeline has joined: neither ACK nor a transient CANCELED row is a join.
		select {
		case <-live.Done():
		case <-ctx.Done():
			t.Fatal("original cancellation pipeline did not join")
		}
		finalRow := row()
		if finalRow.ProjectID != pendingRow.ProjectID || finalRow.RequestID != pendingRow.RequestID || finalRow.OwnerType != pendingRow.OwnerType || finalRow.OwnerID != pendingRow.OwnerID || finalRow.RunID != pendingRow.RunID || finalRow.Epoch != pendingRow.Epoch || finalRow.State != "CANCELED" {
			t.Fatalf("original canonical receipt did not settle: %+v", finalRow)
		}
		final, err := f.facade.AgentRuntimeStateForSession(ctx, f.session, pending.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if len(final.Requests) != 1 || final.Requests[0].ID != pending.ID || final.Requests[0].State != "CANCELED" || final.Requests[0].UserDecision == nil || !reflect.DeepEqual(*final.Requests[0].UserDecision, decision) || count("runtime_user_decisions") != 1 || count("runtime_response_dispatch_attempts") != 0 || count("execution_cancellations") != 1 {
			t.Fatal("cancellation lost immutable choice or produced an answer")
		}
		wireBefore := wire()
		interrupts := 0
		for _, line := range bytes.Split(bytes.TrimSpace(wireBefore), []byte{'\n'}) {
			var v struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if e := json.Unmarshal(line, &v); e != nil {
				t.Fatal(e)
			}
			if v.Method == "turn/interrupt" {
				interrupts++
				var target struct {
					ThreadID string `json:"threadId"`
					TurnID   string `json:"turnId"`
				}
				if json.Unmarshal(v.Params, &target) != nil || target.ThreadID != pending.ThreadID || target.TurnID != pending.TurnID {
					t.Fatal("interrupted different runtime identity")
				}
			}
			if v.Method == "user-response/turn-implementation" {
				t.Fatal("CANCEL_OPERATION sent semantic answer")
			}
		}
		if interrupts != 1 || !reflect.DeepEqual(initialRuns, IDs("runs")) || !reflect.DeepEqual(initialProposals, IDs("execution_proposals")) {
			t.Fatal("duplicate interrupt or implicit successor")
		}
		tables := []string{"runtime_user_decisions", "runtime_response_dispatch_attempts", "runtime_response_evidence", "codex_server_requests", "agent_runtime_states", "execution_cancellations", "runs", "tasks", "execution_proposals"}
		before := definitionFormalSnapshot(t, f, tables)
		files := (&continuationGate{t: t, f: f}).snapshot()
		var terminal struct {
			Result controlapi.AgentUserDecisionResult `json:"result"`
		}
		call("runtime_request_user_decision_submit", args(choice), &terminal)
		if !reflect.DeepEqual(terminal.Result.Decision, decision) || terminal.Result.Cancellation == nil || terminal.Result.Cancellation.RequestID != captured.RequestID || terminal.Result.Cancellation.OwnerID != executionID || terminal.Result.Cancellation.RunID != pending.RunID || terminal.Result.Cancellation.State != "CANCELED" || gate.begins.Load() != 3 || gate.dispatches.Load() != 1 || gate.claims.Load() != 1 {
			t.Fatal("terminal replay did not remain exact pure read")
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "terminal exact user cancellation replay")
		if !bytes.Equal(wireBefore, wire()) || !reflect.DeepEqual(files, (&continuationGate{t: t, f: f}).snapshot()) || !reflect.DeepEqual(initialRuns, IDs("runs")) || !reflect.DeepEqual(initialProposals, IDs("execution_proposals")) {
			t.Fatal("terminal replay changed files/wire or created successor")
		}
		t.Logf("Begin=3 dispatchTrue=1 Claim=1 interrupt=1 answer=0 immutable=CANCEL_OPERATION pending=CANCEL_REQUESTED/NOT_SENT final=CANCELED terminalReplay=pure-read")
		if root := os.Getenv("DEVBRIDGE_PENDING_CANCEL_EVIDENCE"); root != "" {
			if e := os.MkdirAll(root, 0700); e != nil {
				t.Fatal(e)
			}
			b, e := json.MarshalIndent(map[string]any{"decision": decision, "firstReceipt": captured, "pendingSQL": pendingRow, "claim": claimed, "finalSQL": finalRow, "finalRuntime": final, "terminalResult": terminal, "initialRuns": initialRuns, "finalRuns": IDs("runs"), "initialProposals": initialProposals, "finalProposals": IDs("execution_proposals"), "beginCalls": gate.begins.Load(), "beginDispatches": gate.dispatches.Load(), "claimCalls": gate.claims.Load(), "calls": calls}, "", "  ")
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(root, "result.json"), b, 0600); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(filepath.Join(root, "agent-wire.jsonl"), wireBefore, 0600); e != nil {
				t.Fatal(e)
			}
		}
		return false
	}, nil, func(f *upperLifecycleFixture) {
		gate = &pendingCancelStore{ExecutionCancellationStore: f.plane.ExecutionCancellations, entered: make(chan struct{}), release: make(chan struct{}), claimed: make(chan pendingCancelClaimResult, 1), acked: make(chan error, 1)}
		t.Cleanup(gate.unblock)
		f.plane.Coordinator.Cancellations = gate
	})
}
