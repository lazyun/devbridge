package controlapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This test-only decorator pauses before the real SQLite Save opens its
// transaction. It never supplies a cancellation, business row or fence error.
// The caller holds Session.mu and the transport write lock here: observe the
// cancellation commit through a separate read-only DB, NOT a terminal wait.
type cancelBeforeDispatchStore struct {
	*sqlite.AgentRuntimeStore
	target  atomic.Pointer[agentruntime.Request]
	hit     atomic.Bool
	entered chan []byte
	release chan struct{}
	once    sync.Once
	saved   chan error
}

func (s *cancelBeforeDispatchStore) unblock() { s.once.Do(func() { close(s.release) }) }
func (s *cancelBeforeDispatchStore) Save(ctx context.Context, expected uint64, v agentruntime.Snapshot, resolution bool) error {
	target := s.target.Load()
	if target != nil && resolution && v.Identity == target.Identity {
		for _, q := range v.Requests {
			if q.ID == target.ID && q.Identity == target.Identity && q.State == "RESPONDING" && q.Delivery == "DISPATCHING" && q.UserDecision != nil && q.UserDecision.Action == "RESPOND" && s.hit.CompareAndSwap(false, true) {
				raw, err := json.Marshal(q)
				if err != nil {
					return err
				}
				s.entered <- raw
				timer := time.NewTimer(15 * time.Second)
				defer timer.Stop()
				select {
				case <-s.release:
				case <-timer.C:
					return errors.New("TEST_ONLY_DISPATCH_GATE_TIMEOUT")
				}
				err = s.AgentRuntimeStore.Save(ctx, expected, v, resolution)
				s.saved <- err
				return err
			}
		}
	}
	return s.AgentRuntimeStore.Save(ctx, expected, v, resolution)
}

func TestRuntimeCancellationCommittedBeforeAnswerDispatchLocalMCP(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "release_without_cancel", true: "cancel_commits_before_dispatch"}[cancelFirst], func(t *testing.T) {
			for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_RESPONSE_LOSS", "DEVBRIDGE_CODING_V1_MULTI_QUESTION", "DEVBRIDGE_CODING_V1_WITH_APPROVAL", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS", "DEVBRIDGE_CODING_V1_LATE_RESOLVED_FILE"} {
				t.Setenv(key, "")
			}
			t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
			t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
			var gate *cancelBeforeDispatchStore
			runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, executionID string) bool {
				f.plane.Retirement.Close()
				pending, live := waitUserRequest(t, f, f.task, "turn-implementation")
				if pending.ExecutionID != executionID || live.ExecutionProposalID != executionID {
					t.Fatal("wrong live question")
				}
				gate.target.Store(&pending)
				var initialRuns int
				if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&initialRuns); e != nil {
					t.Fatal(e)
				}
				first, second := newApprovedStartLocalClient(t, f), newApprovedStartLocalClient(t, f)
				ctx, cancel := context.WithTimeout(f.ctx, 40*time.Second)
				var wg sync.WaitGroup
				defer func() { gate.unblock(); cancel(); wg.Wait() }()
				dsn := (&url.URL{Scheme: "file", Path: filepath.Join(f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String()
				ro, err := sql.Open("sqlite", dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer ro.Close()
				proposalIDs := func() []string {
					t.Helper()
					rows, e := ro.QueryContext(ctx, `SELECT id FROM execution_proposals WHERE task_id=? ORDER BY id`, f.task)
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
				initialProposals := proposalIDs()

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
				decode := func(result *sdkmcp.CallToolResult, out any) {
					t.Helper()
					if result == nil || result.IsError {
						t.Fatalf("unexpected MCP error: %+v", result)
					}
					raw, e := json.Marshal(result.StructuredContent)
					if e != nil {
						t.Fatal(e)
					}
					if e = json.Unmarshal(raw, out); e != nil {
						t.Fatal(e)
					}
				}
				draft := agentruntime.UserDecisionInput{Identity: pending.Identity, RuntimeRequestID: pending.ID, SubmissionRequestID: "before-dispatch-answer", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", SelectedOptionID: "yes", RawText: "yes"}
				args := func(d agentruntime.UserDecisionInput) map[string]any {
					return map[string]any{"aiSessionId": f.session, "decision": d}
				}
				var prepared struct {
					Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
				}
				raw, e := first.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "runtime_request_user_decision_prepare_hash", Arguments: args(draft)})
				if e != nil {
					t.Fatal(e)
				}
				decode(raw, &prepared)
				choice := prepared.Result.Decision
				if prepared.Result.Recorded || choice.PayloadSHA256 == "" || choice.Identity != pending.Identity {
					t.Fatal("prepare was not exact/read-only")
				}
				type outcome struct {
					Result *sdkmcp.CallToolResult
					Err    error
				}
				submitDone, cancelDone := make(chan outcome, 1), make(chan outcome, 1)
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, e := first.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "runtime_request_user_decision_submit", Arguments: args(choice)})
					submitDone <- outcome{r, e}
				}()
				var staged agentruntime.Request
				select {
				case b := <-gate.entered:
					if e := json.Unmarshal(b, &staged); e != nil {
						t.Fatal(e)
					}
				case <-ctx.Done():
					t.Fatal("exact dispatch gate never reached")
				}
				if staged.ID != pending.ID || staged.Identity != pending.Identity || len(staged.DispatchAttempts) != 1 {
					t.Fatal("not the exact first proposed dispatch")
				}
				var decisionJSON string
				if e := ro.QueryRowContext(ctx, `SELECT decision_json FROM runtime_user_decisions WHERE runtime_request_id=?`, pending.ID).Scan(&decisionJSON); e != nil {
					t.Fatal(e)
				}
				var decision agentruntime.UserDecision
				if e := json.Unmarshal([]byte(decisionJSON), &decision); e != nil {
					t.Fatal(e)
				}
				if decision.ID != staged.UserDecision.ID || decision.PayloadSHA256 != choice.PayloadSHA256 || decision.SubmittedBy != "session:"+f.session || count("runtime_user_decisions") != 1 || count("runtime_response_dispatch_attempts") != 0 || bytes.Contains(wire(), []byte("user-response/")) {
					t.Fatal("gate did not precede actual dispatch after immutable decision")
				}
				cancelID := "cancel-before-dispatch"
				var committedState, committedOwner, committedRun string
				if cancelFirst {
					command := map[string]any{"aiSessionId": f.session, "ownerType": "EXECUTION", "ownerId": executionID, "runId": pending.RunID, "requestId": cancelID, "reason": "Explicit fixture cancellation before answer dispatch"}
					wg.Add(1)
					go func() {
						defer wg.Done()
						r, e := second.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "operation_cancel", Arguments: command})
						cancelDone <- outcome{r, e}
					}()
					deadline := time.Now().Add(8 * time.Second)
					for {
						e := ro.QueryRowContext(ctx, `SELECT state,owner_id,run_id FROM execution_cancellations WHERE request_id=? AND project_id=? AND owner_type='EXECUTION'`, cancelID, pending.ProjectID).Scan(&committedState, &committedOwner, &committedRun)
						if e == nil {
							break
						}
						if !errors.Is(e, sql.ErrNoRows) {
							t.Fatal(e)
						}
						if time.Now().After(deadline) {
							t.Fatal("cancellation did not commit while dispatch gate held")
						}
						time.Sleep(10 * time.Millisecond)
					}
					if committedState != "CANCEL_REQUESTED" || committedOwner != executionID || committedRun != pending.RunID || count("runtime_response_dispatch_attempts") != 0 || bytes.Contains(wire(), []byte("user-response/")) {
						t.Fatalf("not exact cancel-before-dispatch commit: %s %s %s", committedState, committedOwner, committedRun)
					}
				} else if count("execution_cancellations") != 0 {
					t.Fatal("positive control already canceled")
				}
				gate.unblock()
				var saveErr error
				select {
				case saveErr = <-gate.saved:
				case <-ctx.Done():
					t.Fatal("real Save did not finish")
				}
				if cancelFirst {
					if !errors.Is(saveErr, agentruntime.ErrStale) {
						t.Fatalf("real SQLite cancel fence did not reject: %v", saveErr)
					}
				} else if saveErr != nil {
					t.Fatalf("positive-control real dispatch failed: %v", saveErr)
				}
				var submitted outcome
				select {
				case submitted = <-submitDone:
				case <-ctx.Done():
					t.Fatal("submit did not finish")
				}
				if submitted.Err != nil {
					t.Fatalf("submit transport error: %v", submitted.Err)
				}
				if submitted.Result == nil {
					t.Fatal("missing submit result")
				}
				if submitted.Result.IsError {
					exactFence := false
					for _, c := range submitted.Result.Content {
						if text, ok := c.(*sdkmcp.TextContent); ok && strings.HasPrefix(text.Text, "CONFLICT:") && strings.Contains(text.Text, agentruntime.ErrStale.Error()) {
							exactFence = true
						}
					}
					if !cancelFirst || !exactFence {
						t.Fatalf("unexpected submit error: %+v", submitted.Result)
					}
				} else {
					var out struct {
						Result controlapi.AgentUserDecisionResult `json:"result"`
					}
					decode(submitted.Result, &out)
					if !reflect.DeepEqual(out.Result.Decision, decision) {
						t.Fatal("submit changed committed choice")
					}
				}
				var receipt executioncancel.Receipt
				if cancelFirst {
					select {
					case out := <-cancelDone:
						if out.Err != nil {
							t.Fatal(out.Err)
						}
						var envelope struct {
							Result executioncancel.Receipt `json:"result"`
						}
						decode(out.Result, &envelope)
						receipt = envelope.Result
					case <-ctx.Done():
						t.Fatal("cancel call did not finish")
					}
					if receipt.RequestID != cancelID || receipt.OwnerID != executionID || receipt.RunID != pending.RunID {
						t.Fatal("cancel returned another receipt")
					}
				}
				deadline := time.Now().Add(8 * time.Second)
				var final agentruntime.Snapshot
				for {
					final, err = f.facade.AgentRuntimeStateForSession(ctx, f.session, pending.RunID)
					if err != nil {
						t.Fatal(err)
					}
					ready := false
					if len(final.Requests) == 1 {
						if cancelFirst {
							var state string
							if e := ro.QueryRowContext(ctx, `SELECT state FROM execution_cancellations WHERE project_id=? AND request_id=?`, pending.ProjectID, cancelID).Scan(&state); e != nil {
								t.Fatal(e)
							}
							ready = state == "CANCELED" && final.Requests[0].State == "CANCELED"
						} else {
							ready = agentruntime.ResponseDisposition(final.Requests[0]) == "CONSUMED" && bytes.Count(wire(), []byte("user-response/turn-implementation")) == 1
						}
					}
					if ready {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("did not settle: %+v", final)
					}
					time.Sleep(10 * time.Millisecond)
				}
				q := final.Requests[0]
				if q.ID != pending.ID || q.UserDecision == nil || !reflect.DeepEqual(*q.UserDecision, decision) || count("runtime_user_decisions") != 1 {
					t.Fatal("settlement lost immutable answer")
				}
				wireFinal := wire()
				responses, interrupts := 0, 0
				expected, e := agentruntime.SerializeUserDecision(pending, decision)
				if e != nil {
					t.Fatal(e)
				}
				for _, line := range bytes.Split(bytes.TrimSpace(wireFinal), []byte{'\n'}) {
					var entry struct {
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					if e := json.Unmarshal(line, &entry); e != nil {
						t.Fatal(e)
					}
					switch entry.Method {
					case "turn/interrupt":
						interrupts++
					case "user-response/turn-implementation":
						responses++
						var got, want any
						if json.Unmarshal(entry.Params, &got) != nil || json.Unmarshal(expected, &want) != nil || !reflect.DeepEqual(got, want) {
							t.Fatal("wire payload differs from immutable answer")
						}
					}
				}
				want := 1
				if cancelFirst {
					want = 0
				}
				if count("runtime_response_dispatch_attempts") != want || responses != want || interrupts != map[bool]int{false: 0, true: 1}[cancelFirst] {
					t.Fatalf("wrong effects: attempts=%d answers=%d interrupts=%d", count("runtime_response_dispatch_attempts"), responses, interrupts)
				}
				if !cancelFirst {
					var b string
					if e := ro.QueryRowContext(ctx, `SELECT attempt_json FROM runtime_response_dispatch_attempts WHERE runtime_request_id=?`, pending.ID).Scan(&b); e != nil {
						t.Fatal(e)
					}
					var a agentruntime.ResponseDispatchAttempt
					if json.Unmarshal([]byte(b), &a) != nil || a.UserDecisionID != decision.ID || a.Identity != pending.Identity || !bytes.Equal(a.Payload, expected) {
						t.Fatal("positive dispatch payload/owner mismatch")
					}
				}
				var implementations int
				if e := ro.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task).Scan(&implementations); e != nil || implementations != 1 {
					t.Fatalf("unexpected successor: %d %v", implementations, e)
				}
				var finalRuns int
				if e := ro.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&finalRuns); e != nil || finalRuns != initialRuns {
					t.Fatalf("new Run appeared: before=%d after=%d %v", initialRuns, finalRuns, e)
				}
				if count("execution_cancellations") != map[bool]int{false: 0, true: 1}[cancelFirst] {
					t.Fatal("duplicate or unexpected cancellation receipt")
				}
				finalProposals := proposalIDs()
				if !reflect.DeepEqual(initialProposals, finalProposals) {
					t.Fatalf("new NO_START/successor proposal: before=%v after=%v", initialProposals, finalProposals)
				}
				var finalCancellation struct{ ProjectID, RequestID, OwnerType, OwnerID, RunID, State, InterruptState, RuntimeEpoch string }
				if cancelFirst {
					if e := ro.QueryRowContext(ctx, `SELECT project_id,request_id,owner_type,owner_id,run_id,state,interrupt_state,runtime_epoch FROM execution_cancellations WHERE project_id=? AND request_id=?`, pending.ProjectID, cancelID).Scan(&finalCancellation.ProjectID, &finalCancellation.RequestID, &finalCancellation.OwnerType, &finalCancellation.OwnerID, &finalCancellation.RunID, &finalCancellation.State, &finalCancellation.InterruptState, &finalCancellation.RuntimeEpoch); e != nil {
						t.Fatal(e)
					}
					if finalCancellation.OwnerType != "EXECUTION" || finalCancellation.OwnerID != executionID || finalCancellation.RunID != pending.RunID || finalCancellation.State != "CANCELED" || finalCancellation.RuntimeEpoch != pending.RuntimeEpoch {
						t.Fatalf("final cancellation is not the original exact settled receipt: %+v", finalCancellation)
					}
				}
				wg.Wait()
				t.Logf("cancelFirst=%v realSave=%v immutable=1 attempts=%d wireAnswers=%d interrupts=%d finalState=%s disposition=%s", cancelFirst, saveErr, want, responses, interrupts, q.State, agentruntime.ResponseDisposition(q))
				if root := os.Getenv("DEVBRIDGE_CANCEL_DISPATCH_EVIDENCE"); root != "" {
					root = filepath.Join(root, fmt.Sprint(cancelFirst))
					if e := os.MkdirAll(root, 0700); e != nil {
						t.Fatal(e)
					}
					b, e := json.MarshalIndent(map[string]any{"staged": staged, "decision": decision, "cancelCommitState": committedState, "cancelCommitOwner": committedOwner, "cancelCommitRun": committedRun, "realSaveError": fmt.Sprint(saveErr), "submit": submitted.Result, "cancelReceipt": receipt, "final": final, "initialProposalIDs": initialProposals, "finalProposalIDs": finalProposals, "finalCancellationRow": finalCancellation}, "", "  ")
					if e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(root, "result.json"), b, 0600); e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(filepath.Join(root, "agent-wire.jsonl"), wireFinal, 0600); e != nil {
						t.Fatal(e)
					}
				}
				return false
			}, nil, func(f *upperLifecycleFixture) {
				gate = &cancelBeforeDispatchStore{AgentRuntimeStore: sqlite.NewAgentRuntimeStore(f.plane.DB), entered: make(chan []byte, 1), release: make(chan struct{}), saved: make(chan error, 1)}
				t.Cleanup(gate.unblock)
				f.plane.Runs.AgentRuntime = agentruntime.NewService(gate)
				factory, ok := f.plane.Coordinator.Codex.(execution.DefaultCodexFactory)
				if !ok {
					t.Fatal("unexpected fixture factory")
				}
				factory.Config.AgentRuntime = f.plane.Runs.AgentRuntime
				f.plane.Coordinator.Codex = factory
			})
		})
	}
}
