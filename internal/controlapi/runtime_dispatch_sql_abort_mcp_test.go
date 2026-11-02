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
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// Observation only: every Save returns the real SQLite result unchanged.
// The injected SQL trigger, not this wrapper, causes the negative-case failure.
type dispatchSQLObserver struct {
	*sqlite.AgentRuntimeStore
	target   atomic.Pointer[agentruntime.Request]
	observed atomic.Bool
	saved    chan error
}

func (s *dispatchSQLObserver) Save(ctx context.Context, expected uint64, v agentruntime.Snapshot, resolution bool) error {
	target := s.target.Load()
	exact := false
	if target != nil && resolution && v.Identity == target.Identity {
		for _, q := range v.Requests {
			if q.ID == target.ID && q.State == "RESPONDING" && q.Delivery == "DISPATCHING" {
				exact = true
			}
		}
	}
	err := s.AgentRuntimeStore.Save(ctx, expected, v, resolution)
	if exact && s.observed.CompareAndSwap(false, true) {
		s.saved <- err
	}
	return err
}

func TestRuntimeDispatchSQLAbortLocalMCP(t *testing.T) {
	for _, negative := range []bool{false, true} {
		name := "normal"
		if negative {
			name = "abort"
		}
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_RESPONSE_LOSS", "DEVBRIDGE_CODING_V1_MULTI_QUESTION", "DEVBRIDGE_CODING_V1_WITH_APPROVAL", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS"} {
				t.Setenv(key, "")
			}
			t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
			t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
			var observer *dispatchSQLObserver
			runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, executionID string) bool {
				f.plane.Retirement.Close()
				pending, _ := waitUserRequest(t, f, f.task, "turn-implementation")
				observer.target.Store(&pending)
				live, ok := f.plane.Supervisor.GetSession(pending.RunID)
				if !ok {
					t.Fatal("no real managed session")
				}
				ctx, cancel := context.WithTimeout(f.ctx, 40*time.Second)
				defer cancel()
				ro, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(f.dataRoot, "state", "devbridge.db"), RawQuery: "mode=ro&_pragma=query_only(1)"}).String())
				if err != nil {
					t.Fatal(err)
				}
				defer ro.Close()
				ids := func(table string) []string {
					t.Helper()
					rows, e := ro.QueryContext(ctx, "SELECT id FROM "+table+" WHERE task_id=? ORDER BY id", f.task)
					if e != nil {
						t.Fatal(e)
					}
					defer rows.Close()
					out := []string{}
					for rows.Next() {
						var id string
						if e = rows.Scan(&id); e != nil {
							t.Fatal(e)
						}
						out = append(out, id)
					}
					if e = rows.Err(); e != nil {
						t.Fatal(e)
					}
					return out
				}
				runIDs, proposalIDs := ids("runs"), ids("execution_proposals")
				client := newApprovedStartLocalClient(t, f)
				draft := agentruntime.UserDecisionInput{Identity: pending.Identity, RuntimeRequestID: pending.ID, SubmissionRequestID: "dispatch-sql-" + name, ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
				args := func(d agentruntime.UserDecisionInput) map[string]any {
					return map[string]any{"aiSessionId": f.session, "decision": d}
				}
				var prepared struct {
					Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
				}
				if err = json.Unmarshal(client.call("runtime_request_user_decision_prepare_hash", args(draft), false), &prepared); err != nil {
					t.Fatal(err)
				}
				choice := prepared.Result.Decision
				if choice.PayloadSHA256 == "" || prepared.Result.Recorded {
					t.Fatal("bad formal preparation")
				}
				const trigger = "test_exact_runtime_dispatch_abort"
				if negative {
					// Isolated disposable DB; only this exact runtime request's attempt INSERT.
					sqlText := fmt.Sprintf("CREATE TRIGGER %s BEFORE INSERT ON runtime_response_dispatch_attempts WHEN NEW.run_id='%s' AND NEW.runtime_request_id='%s' BEGIN SELECT RAISE(ABORT,'TEST_EXACT_DISPATCH_INTENT_ABORT'); END", trigger, strings.ReplaceAll(pending.RunID, "'", "''"), strings.ReplaceAll(pending.ID, "'", "''"))
					if _, err = f.plane.DB.SQL().ExecContext(ctx, sqlText); err != nil {
						t.Fatal(err)
					}
					defer func() { _, _ = f.plane.DB.SQL().Exec("DROP TRIGGER IF EXISTS " + trigger) }()
				}
				var accepted struct {
					Result controlapi.AgentUserDecisionResult `json:"result"`
				}
				if err = json.Unmarshal(client.call("runtime_request_user_decision_submit", args(choice), false), &accepted); err != nil {
					t.Fatal(err)
				}
				var saveErr error
				select {
				case saveErr = <-observer.saved:
				case <-ctx.Done():
					t.Fatal("real exact dispatch Save was not observed")
				}
				if negative {
					if saveErr == nil || !strings.Contains(saveErr.Error(), "TEST_EXACT_DISPATCH_INTENT_ABORT") || errors.Is(saveErr, agentruntime.ErrStale) {
						t.Fatalf("not genuine SQL abort: %v", saveErr)
					}
				} else if saveErr != nil {
					t.Fatalf("positive Save failed: %v", saveErr)
				}
				var finalRun run.Run
				var joinedErr error
				if negative {
					finalRun, joinedErr = live.Wait(ctx)
					var infrastructure *execution.InfrastructureError
					if !errors.As(joinedErr, &infrastructure) || finalRun.Status != run.StatusInfrastructureFailed || finalRun.Failure == nil || finalRun.Failure.Stage != "Codex turn" || finalRun.Failure.Kind != run.FailureInfrastructure {
						t.Fatalf("wrong joined failure: run=%+v err=%v", finalRun, joinedErr)
					}
				} else {
					deadline := time.Now().Add(8 * time.Second)
					for {
						v, e := f.facade.AgentRuntimeStateForSession(ctx, f.session, pending.RunID)
						if e != nil {
							t.Fatal(e)
						}
						if len(v.Requests) == 1 && agentruntime.ResponseDisposition(v.Requests[0]) == "CONSUMED" {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("positive not consumed")
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
				state, err := f.facade.AgentRuntimeStateForSession(ctx, f.session, pending.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Requests) != 1 {
					t.Fatal("wrong request count")
				}
				q := state.Requests[0]
				wantAttempts := 1
				wantDisposition := "CONSUMED"
				if negative {
					wantAttempts = 0
					wantDisposition = "NOT_DISPATCHED"
					if q.State != "USER_DECISION_RECORDED" || len(q.Resolution) == 0 {
						t.Fatalf("lost recorded/resolved choice: %+v", q)
					}
				}
				if q.ID != pending.ID || q.Identity != pending.Identity || agentruntime.ResponseDisposition(q) != wantDisposition {
					t.Fatalf("wrong exact disposition: %+v", q)
				}
				var count int
				var raw string
				if err = ro.QueryRowContext(ctx, "SELECT count(*),COALESCE(max(decision_json),'') FROM runtime_user_decisions WHERE runtime_request_id=?", pending.ID).Scan(&count, &raw); err != nil || count != 1 {
					t.Fatalf("decision count=%d err=%v", count, err)
				}
				var immutable agentruntime.UserDecision
				if err = json.Unmarshal([]byte(raw), &immutable); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(immutable, accepted.Result.Decision) || immutable.SubmittedBy != "session:"+f.session || immutable.RawText != "yes" {
					t.Fatal("immutable decision lost/changed")
				}
				if err = ro.QueryRowContext(ctx, "SELECT count(*) FROM runtime_response_dispatch_attempts WHERE runtime_request_id=?", pending.ID).Scan(&count); err != nil || count != wantAttempts {
					t.Fatalf("attempts=%d want=%d err=%v", count, wantAttempts, err)
				}
				wire := func() []byte {
					t.Helper()
					b, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
					if e != nil {
						t.Fatal(e)
					}
					return b
				}
				if negative {
					if len(q.RecoveryEvidence) > 1 {
						t.Fatal("duplicate pre-effect evidence")
					}
					for _, proof := range q.RecoveryEvidence {
						if proof.Kind != "PRE_EFFECT_REJECTED" || proof.Identity != pending.Identity || proof.RuntimeRequestID != pending.ID || proof.BackendRequestID != string(pending.RequestID) || proof.AttemptID != "" {
							t.Fatalf("false response evidence: %+v", proof)
						}
					}
				} else {
					if len(q.DispatchAttempts) != 1 {
						t.Fatal("positive exact attempt missing")
					}
					at := q.DispatchAttempts[0]
					expected, e := agentruntime.SerializeUserDecision(pending, immutable)
					if e != nil {
						t.Fatal(e)
					}
					if at.Identity != pending.Identity || at.RuntimeRequestID != pending.ID || at.UserDecisionID != immutable.ID || at.CommandRequestID != immutable.SubmissionRequestID || !bytes.Equal(at.Payload, expected) {
						t.Fatal("positive attempt changed exact answer")
					}
				}
				beforeWire := wire()
				responses := bytes.Count(beforeWire, []byte(`"method":"user-response/turn-implementation"`))
				if !negative {
					expected, e := agentruntime.SerializeUserDecision(pending, immutable)
					if e != nil {
						t.Fatal(e)
					}
					var want any
					_ = json.Unmarshal(expected, &want)
					for _, line := range bytes.Split(bytes.TrimSpace(beforeWire), []byte{'\n'}) {
						var entry struct {
							Method string
							Params json.RawMessage
						}
						if e = json.Unmarshal(line, &entry); e != nil {
							t.Fatal(e)
						}
						if entry.Method == "user-response/turn-implementation" {
							var got any
							_ = json.Unmarshal(entry.Params, &got)
							if !reflect.DeepEqual(got, want) {
								t.Fatal("positive wire changed answer")
							}
						}
					}
				}
				if responses != wantAttempts {
					t.Fatalf("wire answers=%d want=%d", responses, wantAttempts)
				}
				if !reflect.DeepEqual(runIDs, ids("runs")) || !reflect.DeepEqual(proposalIDs, ids("execution_proposals")) {
					t.Fatal("implicit successor created")
				}
				var taskPhase, taskStatus, currentExecution, outcome, failureCode, implementationRunID, verificationRunID string
				var advance, classification string
				var quiesced, applied, claimCount, continuationSafe int
				if negative {
					if err = ro.QueryRowContext(ctx, "SELECT advance_state,quiesced,applied_at IS NOT NULL FROM agent_finalization_intents WHERE run_id=?", pending.RunID).Scan(&advance, &quiesced, &applied); err != nil {
						t.Fatal(err)
					}
					if err = ro.QueryRowContext(ctx, "SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?", pending.RunID).Scan(&claimCount); err != nil {
						t.Fatal(err)
					}
					if err = ro.QueryRowContext(ctx, "SELECT classification FROM agent_operation_recovery_states WHERE run_id=?", pending.RunID).Scan(&classification); err != nil {
						t.Fatal(err)
					}
					if err = ro.QueryRowContext(ctx, "SELECT a.continuation_safe FROM agent_conversations a JOIN agent_operation_boundaries b ON b.agent_conversation_id=a.id WHERE b.run_id=?", pending.RunID).Scan(&continuationSafe); err != nil {
						t.Fatal(err)
					}
					// RecordFinalization explicitly normalizes UNKNOWN advance to quiesced=false.
					if advance != "UNKNOWN" || quiesced != 0 || applied != 1 || claimCount != 1 || classification != "UNKNOWN" || continuationSafe != 0 {
						t.Fatalf("unsafe finalization: advance=%s quiesced=%d applied=%d claim=%d classification=%s safe=%d", advance, quiesced, applied, claimCount, classification, continuationSafe)
					}
					if err = ro.QueryRowContext(ctx, "SELECT phase,status,COALESCE(current_execution_id,'') FROM tasks WHERE id=?", f.task).Scan(&taskPhase, &taskStatus, &currentExecution); err != nil {
						t.Fatal(err)
					}
					if err = ro.QueryRowContext(ctx, "SELECT outcome,COALESCE(failure_code,''),COALESCE(run_id,''),COALESCE(verification_run_id,'') FROM execution_proposals WHERE id=?", executionID).Scan(&outcome, &failureCode, &implementationRunID, &verificationRunID); err != nil {
						t.Fatal(err)
					}
					if taskPhase != "IMPLEMENTING" || taskStatus != "ACTIVE" || currentExecution != executionID || outcome != "FAILED" || failureCode != "EXECUTION_FAILED" || implementationRunID != pending.RunID || verificationRunID != "" {
						t.Fatalf("incorrect task/execution outcome: %s/%s owner=%s outcome=%s code=%s", taskPhase, taskStatus, currentExecution, outcome, failureCode)
					}
					if _, err = f.plane.DB.SQL().ExecContext(ctx, "DROP TRIGGER "+trigger); err != nil {
						t.Fatal(err)
					}
				}
				tables := []string{"runtime_user_decisions", "runtime_response_dispatch_attempts", "runtime_response_evidence", "codex_server_requests", "agent_runtime_states", "runs", "tasks", "execution_proposals", "execution_cancellations", "feature_area_agent_claims", "agent_finalization_intents", "agent_operation_recovery_states"}
				before := definitionFormalSnapshot(t, f, tables)
				files := (&continuationGate{t: t, f: f}).snapshot()
				var replay struct {
					Result controlapi.AgentUserDecisionResult `json:"result"`
				}
				if err = json.Unmarshal(client.call("runtime_request_user_decision_submit", args(choice), false), &replay); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(replay.Result.Decision, immutable) {
					t.Fatal("settled replay changed decision")
				}
				assertDefinitionFormalSnapshot(t, f, tables, before, "SQL fault removed: immutable replay is receipt only, not restart")
				if !bytes.Equal(beforeWire, wire()) || !reflect.DeepEqual(files, (&continuationGate{t: t, f: f}).snapshot()) {
					t.Fatal("replay had file/wire effect")
				}
				t.Logf("case=%s actualSave=%v joined=%v disposition=%s attempts=%d wire=%d claim=%d recovery=%s", name, saveErr, joinedErr, agentruntime.ResponseDisposition(q), count, responses, claimCount, classification)
				if root := os.Getenv("DEVBRIDGE_DISPATCH_ABORT_EVIDENCE"); root != "" {
					root = filepath.Join(root, name)
					if err = os.MkdirAll(root, 0700); err != nil {
						t.Fatal(err)
					}
					raw, err := json.MarshalIndent(map[string]any{"request": q, "decision": immutable, "run": finalRun, "saveError": fmt.Sprint(saveErr), "joinedError": fmt.Sprint(joinedErr), "advance": advance, "quiesced": quiesced, "applied": applied, "claimCount": claimCount, "classification": classification, "continuationSafe": continuationSafe, "replay": replay.Result, "taskPhase": taskPhase, "taskStatus": taskStatus, "currentExecution": currentExecution, "outcome": outcome, "failureCode": failureCode, "implementationRunID": implementationRunID, "verificationRunID": verificationRunID, "protocol": client.session.InitializeResult()}, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(root, "result.json"), raw, 0600); err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(root, "agent-wire.jsonl"), beforeWire, 0600); err != nil {
						t.Fatal(err)
					}
				}
				return false
			}, nil, func(f *upperLifecycleFixture) {
				observer = &dispatchSQLObserver{AgentRuntimeStore: sqlite.NewAgentRuntimeStore(f.plane.DB), saved: make(chan error, 1)}
				f.plane.Runs.AgentRuntime = agentruntime.NewService(observer)
				factory, ok := f.plane.Coordinator.Codex.(execution.DefaultCodexFactory)
				if !ok {
					t.Fatal("unexpected factory")
				}
				factory.Config.AgentRuntime = f.plane.Runs.AgentRuntime
				f.plane.Coordinator.Codex = factory
			})
		})
	}
}
