package controlapi_test

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// Real owner/store/serializer admission with an independent process holding an
// actual unfinished Turn. Faults affect protocol delivery, never database state.
func TestRecoveryStatusProductionAdmissionWindows(t *testing.T) {
	for _, mode := range []string{"rejected-runtime-approval", "unknown-nonterminal", "active-cancellation", "active-thread-consumed", "nonterminal-idle-thread"} {
		t.Run(mode, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("DEVBRIDGE_RECOVERY_ADMISSION_SERVER", mode)
			wire := filepath.Join(t.TempDir(), "wire.jsonl")
			t.Setenv("DEVBRIDGE_RECOVERY_ADMISSION_WIRE", wire)
			var reopen controlplane.Config
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
				q, source := waitContinuityQuestion(t, f, "")
				if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, q, "RESPOND", "yes")); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					snapshot, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, source.ID)
					if e != nil {
						t.Fatal(e)
					}
					ready := false
					for _, request := range snapshot.Requests {
						if mode == "rejected-runtime-approval" {
							ready = ready || (request.Kind == "RUNTIME_APPROVAL" && request.State == "REJECTED" && request.Delivery == "JSONRPC_ERROR" && request.UserDecision == nil)
						} else {
							ready = ready || (request.ID == q.ID && request.State == "RESPONDED")
						}
					}
					if ready {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if mode == "unknown-nonterminal" {
					// The backend consumed the answer, while the durable resolved receipt was
					// deliberately unavailable. Core reopen must preserve UNKNOWN while active.
					if err := f.plane.Close(); err != nil {
						t.Fatal(err)
					}
					f.plane = nil
					f.plane, err = controlplane.Open(f.ctx, reopen)
					if err != nil {
						t.Fatal(err)
					}
					f.facade, err = controlapi.NewFromControlPlane(f.plane)
					if err != nil {
						t.Fatal(err)
					}
					q = waitUnknownRequest(t, f, q, false)
				}
				source, err = f.plane.Runs.Get(f.ctx, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				w, err := f.plane.Runs.RuntimeWorkset(f.ctx, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				binding, err := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				if err = appserver.Verify(f.ctx, binding); err != nil {
					t.Fatal(err)
				}
				cfg := codex.Config{CodexPath: executable, AppServerBinding: &binding}
				if w.TaskPermissions != nil {
					_, roots, writes := w.TaskPermissionBinding()
					cfg.TaskRuntimeBinding = &codex.RuntimeBinding{RuntimeWorkspaceRoots: roots, WritableRoots: writes, TaskPermissions: w.TaskPermissions.Clone()}
					cfg.DiagnosticTaskReadOnly = true
				}
				client, err := codex.Start(f.ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				observed, err := client.ObserveLoadedTurn(f.ctx, source.ThreadID, source.TurnID)
				client.Close()
				if err != nil || observed.Turn.Status != "inProgress" || observed.ThreadStatus != map[bool]string{true: "idle", false: "active"}[mode == "nonterminal-idle-thread"] {
					t.Fatalf("source not genuinely active: %+v %v", observed, err)
				}
				snapshot, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "active-thread-consumed" || mode == "nonterminal-idle-thread" || mode == "active-cancellation" {
					consumed := false
					for _, request := range snapshot.Requests {
						if agentruntime.OpenRequest(request) {
							t.Fatal("pending request masks independent admission condition")
						}
						consumed = consumed || (request.ID == q.ID && agentruntime.ResponseDisposition(request) == "CONSUMED")
					}
					if !consumed {
						t.Fatal("original answer not durably consumed")
					}
				}
				t.Logf("before admission: thread=%s turn=%s request=%s", observed.ThreadStatus, observed.Turn.Status, mode)
				if mode == "rejected-runtime-approval" {
					pending := false
					for _, request := range snapshot.Requests {
						if request.Kind == "CODEX_REQUEST" && agentruntime.OpenRequest(request) {
							t.Fatal("question masks source activity gate")
						}
						pending = pending || (request.Kind == "RUNTIME_APPROVAL" && request.State == "REJECTED" && request.Delivery == "JSONRPC_ERROR" && request.UserDecision == nil)
					}
					if !pending {
						t.Fatal("no durable system refusal")
					}
				}
				if mode == "active-cancellation" {
					ctx, cancel := context.WithTimeout(f.ctx, 600*time.Millisecond)
					receipt, e := f.facade.CancelExecutionForSession(ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: executionID, ExecutionID: executionID, RunID: source.ID, RequestID: "held-cancel", Reason: "Cancel active source explicitly"})
					cancel()
					if e == nil && receipt.State == "CANCELED" {
						t.Fatal("fixture cancellation became terminal")
					}
					var state string
					if err = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT state FROM execution_cancellations WHERE request_id='held-cancel'`).Scan(&state); err != nil {
						t.Fatal(err)
					}
					if state != "CANCEL_REQUESTED" && state != "RECOVERY_REQUIRED" {
						t.Fatalf("no active cancellation intent: %s", state)
					}
				}
				src := recoverystatus.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: w.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: q.AgentConversationID, ThreadID: source.ThreadID, AppServerInstanceID: binding.ID}
				spec := recoverystatus.Spec{Source: src, RequestID: "blocked-" + mode, RecoveryReason: mode, Instruction: "Observe original execution without editing.", OutputSchemaVersion: "v1", Generation: 1}
				prepared, e := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, spec)
				if mode == "active-cancellation" {
					if e == nil {
						t.Fatal("canonical pending request/cancellation was not rejected at Prepare")
					}
				} else if e != nil {
					t.Fatalf("source failed earlier owner preparation rather than runtime admission: %v", e)
				}
				if e == nil {
					if prepared.StartCommand == nil {
						t.Fatal("prepared source missing explicit start command")
					}
					_, e = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, *prepared.StartCommand)
				}
				if e == nil {
					t.Fatal("unsafe source admitted recovery")
				}
				var children int
				if err = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT count(*) FROM runs WHERE purpose='RECOVERY_STATUS'`).Scan(&children); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(wire)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("after admission: recoveryChildren=%d originalTurnStarts=%d", children, strings.Count(string(raw), "turn/start\n"))
				if children != 0 || strings.Count(string(raw), "turn/start\n") != 1 {
					t.Fatalf("admission created effects children=%d wire=%s", children, raw)
				}
				if mode == "active-cancellation" && !strings.Contains(string(raw), "turn/interrupt\n") {
					t.Fatal("cancellation never reached real serializer")
				}
				return false
			}, func(f *upperLifecycleFixture) {
				store := sqlite.NewAgentRuntimeStore(f.plane.DB)
				if mode == "unknown-nonterminal" {
					f.plane.Runs.AgentRuntime = agentruntime.NewService(&loseResolvedReceiptStore{AgentRuntimeStore: store})
				}
				spec := func(codexconfig.Config) appserver.Spec {
					return appserver.Spec{ExecutablePath: executable, CodexHome: t.TempDir()}
				}
				f.plane.Coordinator.AppServers = store
				f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: executable, AgentRuntime: f.plane.Runs.AgentRuntime}, ContinuityDirectory: f.dataRoot, AppServers: store, AppServerSpec: spec, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
					return codex.DynamicToolCallResponse{Success: false}, nil
				}}

				reopen = controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: executable}, AppServerContinuity: true, AppServerSpec: spec}
				t.Cleanup(func() { stopRecoveryFixtureServers(f) })
			})
		})
	}
}

type admissionWindowServer struct {
	mu        sync.Mutex
	cwd, turn string
	profile   string
}

func init() {
	mode := os.Getenv("DEVBRIDGE_RECOVERY_ADMISSION_SERVER")
	if mode == "" {
		return
	}
	socket := ""
	for i, a := range os.Args[1:] {
		if a == "--version" {
			fmt.Println("codex-cli 0.149.0")
			os.Exit(0)
		}
		if a == "--out" && i+2 < len(os.Args) {
			_ = os.WriteFile(filepath.Join(os.Args[i+2], "schema.json"), []byte(`{"type":"object"}`), 0600)
			os.Exit(0)
		}
		if a == "--listen" && i+2 < len(os.Args) {
			socket = strings.TrimPrefix(os.Args[i+2], "unix://")
		}
	}
	if socket == "" {
		os.Exit(71)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(72)
	}
	server := &admissionWindowServer{}
	_ = http.Serve(listener, http.HandlerFunc(server.accept))
	os.Exit(0)
}
func (s *admissionWindowServer) thread() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	turns := []any{}
	status := "idle"
	if s.turn != "" {
		status = "active"
		if os.Getenv("DEVBRIDGE_RECOVERY_ADMISSION_SERVER") == "nonterminal-idle-thread" {
			status = "idle"
		}
		turns = append(turns, map[string]any{"id": s.turn, "status": "inProgress", "items": []any{}})
	}
	return map[string]any{"id": "admission-thread", "cwd": s.cwd, "status": map[string]string{"type": status}, "turns": turns}
}
func (s *admissionWindowServer) accept(rw http.ResponseWriter, r *http.Request) {
	conn, buf, err := rw.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	if buf.Flush() != nil {
		return
	}
	ws := &recoveryFakeWS{Conn: conn, reader: buf.Reader}
	for {
		raw, e := ws.read()
		if e != nil {
			return
		}
		var q struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &q) != nil {
			return
		}
		if q.Method != "" {
			f, e := os.OpenFile(os.Getenv("DEVBRIDGE_RECOVERY_ADMISSION_WIRE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if e == nil {
				fmt.Fprintln(f, q.Method)
				f.Close()
			}
		}
		respond := func(v any) { _ = ws.send(map[string]any{"id": q.ID, "result": v}) }
		switch q.Method {
		case "config/read":
			respond(map[string]any{"config": map[string]any{}})
		case "mcpServerStatus/list":
			respond(map[string]any{"data": []any{}, "nextCursor": nil})
		case "initialize":
			respond(map[string]any{"userAgent": "admission-window"})
		case "initialized":
		case "thread/loaded/list":
			respond(map[string]any{"data": []string{"admission-thread"}, "nextCursor": nil})
		case "thread/start":
			var p struct {
				CWD         string `json:"cwd"`
				Permissions string `json:"permissions"`
			}
			_ = json.Unmarshal(q.Params, &p)
			s.mu.Lock()
			s.cwd = p.CWD
			s.profile = p.Permissions
			s.mu.Unlock()
			result := map[string]any{"thread": s.thread(), "cwd": p.CWD}
			if p.Permissions != "" {
				result["activePermissionProfile"] = map[string]any{"id": p.Permissions}
			}
			respond(result)
		case "thread/turns/list":
			respond(map[string]any{"data": s.thread()["turns"]})
		case "thread/read", "thread/resume":
			var p struct {
				Permissions string `json:"permissions"`
			}
			json.Unmarshal(q.Params, &p)
			s.mu.Lock()
			if p.Permissions != "" {
				s.profile = p.Permissions
			}
			profile := s.profile
			s.mu.Unlock()
			result := map[string]any{"thread": s.thread()}
			if profile != "" {
				result["activePermissionProfile"] = map[string]any{"id": profile}
			}
			respond(result)
		case "turn/start":
			s.mu.Lock()
			s.turn = "admission-turn"
			s.mu.Unlock()
			respond(map[string]any{"turn": map[string]any{"id": "admission-turn", "status": "inProgress", "items": []any{}}})
			_ = ws.send(map[string]any{"id": "admission-question", "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": "admission-thread", "turnId": "admission-turn", "itemId": "question-item", "isBlocking": true, "questions": []any{map[string]any{"id": "adaptation", "header": "Fixture", "question": "Proceed?", "options": []any{map[string]string{"label": "yes", "description": "Continue"}, map[string]string{"label": "no", "description": "Stop"}}}}}})
		case "":
			if len(q.Result) > 0 {
				_ = ws.send(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "admission-thread", "requestId": "admission-question"}})
				if os.Getenv("DEVBRIDGE_RECOVERY_ADMISSION_SERVER") == "rejected-runtime-approval" {
					s.mu.Lock()
					cwd := s.cwd
					s.mu.Unlock()
					_ = ws.send(map[string]any{"id": "admission-approval", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "admission-thread", "turnId": "admission-turn", "itemId": "approval-item", "cwd": cwd, "command": "printf fixture", "startedAtMs": 1}})
				}
			}
		case "turn/interrupt": // Intentionally lose acknowledgement and keep the exact Turn active.
		default:
			respond(map[string]any{})
		}
	}
}
