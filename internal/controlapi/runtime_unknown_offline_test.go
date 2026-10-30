package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// Fail the durable receipt boundary, never insert a final state. The real server
// consumes the response and continues; Core B has no receipt to read back. This
// is explicitly a lost-durable-receipt fixture, not a fabricated protocol query.
type loseResolvedReceiptStore struct {
	*sqlite.AgentRuntimeStore
	rejected        atomic.Int64
	beforeEffect    bool
	uncertainIntent bool
}

func (s *loseResolvedReceiptStore) Save(ctx context.Context, expected uint64, v agentruntime.Snapshot, resolution bool) error {
	for _, q := range v.Requests {
		if s.beforeEffect && q.Kind == "CODEX_REQUEST" && q.Delivery == "DISPATCHING" {
			s.rejected.Add(1)
			if s.uncertainIntent {
				if err := s.AgentRuntimeStore.Save(ctx, expected, v, resolution); err != nil {
					return err
				}
			}
			return errors.New("fixture: dispatch intent acknowledgement lost before backend write")
		}
		if !s.beforeEffect && q.Kind == "CODEX_REQUEST" && q.State == "RESPONDED" {
			s.rejected.Add(1)
			return errors.New("fixture: resolved receipt persistence unavailable")
		}
	}
	return s.AgentRuntimeStore.Save(ctx, expected, v, resolution)
}

func TestRuntimeUnknownOfflineConsumedReceiptLostCoreReopen(t *testing.T) {
	runUnknownOfflineCoreReopen(t, false, false)
}
func TestRuntimeUnknownOfflinePreEffectRejectedCoreReopen(t *testing.T) {
	runUnknownOfflineCoreReopen(t, true, false)
}
func TestRuntimeUnknownOfflineExactPendingProofCoreReopen(t *testing.T) {
	runUnknownOfflineCoreReopen(t, true, true)
}
func runUnknownOfflineCoreReopen(t *testing.T, beforeEffect, uncertainIntent bool) {
	native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	if native == "" {
		t.Skip("offline installed App Server binary not selected")
	}
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		defer r.Body.Close()
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		n := calls.Add(1)
		var item map[string]any
		if n == 1 {
			item = map[string]any{"type": "function_call", "call_id": "unknown-fixture-question", "name": "request_user_input", "arguments": `{"questions":[{"id":"adaptation","header":"Fixture","question":"Choose the fixture path?","options":[{"label":"yes","description":"Continue"},{"label":"no","description":"Alternative"}]}]}`}
		} else {
			item = map[string]any{"id": "unknown-result", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": `{"resultDisposition":"COMPLETED","summary":"Original turn completed after consumption; no repository edits"}`}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("unknown-response-", n)}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("unknown-response-", n), "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		}
	}))
	defer provider.Close()
	home := t.TempDir()
	configText := fmt.Sprintf("openai_base_url=%q\nmodel=\"mock-model\"\nmodel_provider=\"openai\"\n[features]\ndefault_mode_request_user_input=true\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configText), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"offline-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec := func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: home, Env: []string{"OPENAI_BASE_URL=" + provider.URL + "/v1", "OPENAI_API_KEY=offline-fixture"}}
	}
	var planeConfig controlplane.Config
	var fault *loseResolvedReceiptStore
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		request, original := waitContinuityQuestion(t, f, "")
		binding, err := fault.AppServerBinding(f.ctx, original.ID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "RESPOND", "no"))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if ((!beforeEffect && calls.Load() == 2) || (beforeEffect && calls.Load() == 1)) && fault.rejected.Load() > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if (!beforeEffect && calls.Load() != 2) || (beforeEffect && calls.Load() != 1) || fault.rejected.Load() == 0 {
			t.Fatalf("fault not hit consumed=%d rejected=%d", calls.Load(), fault.rejected.Load())
		}
		if err = f.plane.Close(); err != nil {
			t.Fatal(err)
		}
		f.plane = nil
		if err = appserver.Verify(f.ctx, binding); err != nil {
			t.Fatalf("original instance lost: %v", err)
		}
		f.plane, err = controlplane.Open(f.ctx, planeConfig)
		if err != nil {
			t.Fatal(err)
		}
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
		if beforeEffect {
			deadline = time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				state, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, request.RunID)
				if e != nil {
					t.Fatal(e)
				}
				for _, q := range state.Requests {
					if q.ID == request.ID && agentruntime.ResponseDisposition(q) == "CONSUMED" && calls.Load() >= 2 {
						if q.UserDecision == nil || q.UserDecision.ID != result.Decision.ID || q.AppServerInstanceID != binding.ID || calls.Load() != 2 || len(q.DispatchAttempts) != map[bool]int{false: 1, true: 2}[uncertainIntent] {
							t.Fatalf("pre-effect recovery mismatch: %+v calls=%d", q, calls.Load())
						}
						return false
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatal("pre-effect rejection did not recover same immutable decision")
			return false
		}
		q := waitUnknownRequest(t, f, request, true)
		if q.UserDecision == nil || q.UserDecision.ID != result.Decision.ID || q.RunID != original.ID || q.ThreadID != request.ThreadID || q.TurnID != request.TurnID || q.AppServerInstanceID != binding.ID || q.RuntimeEpoch == request.RuntimeEpoch {
			t.Fatalf("reopen identity/decision: %+v", q)
		}
		assertUnknownContract(t, f, q, task.PhaseImplementing)
		if calls.Load() != 2 {
			t.Fatalf("blind redispatch providercalls=%d", calls.Load())
		}
		var bindings, decisions, intents int
		for query, dst := range map[string]*int{`SELECT count(*) FROM app_server_run_bindings`: &bindings, `SELECT count(*) FROM runtime_user_decisions`: &decisions, `SELECT count(*) FROM agent_finalization_intents WHERE run_id='` + original.ID + `'`: &intents} {
			if err = f.plane.DB.SQL().QueryRow(query).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		if bindings != 1 || decisions != 1 || intents != 0 {
			t.Fatalf("unexpected replacement/finalization: bindings=%d decisions=%d intents=%d", bindings, decisions, intents)
		}
		t.Logf("real App Server consumed; durable receipt loss verified; same instance=%s run=%s unknown after Core reopen", binding.ID, original.ID)
		return false
	}, func(f *upperLifecycleFixture) {
		store := sqlite.NewAgentRuntimeStore(f.plane.DB)
		fault = &loseResolvedReceiptStore{AgentRuntimeStore: store, beforeEffect: beforeEffect, uncertainIntent: uncertainIntent}
		f.plane.Runs.AgentRuntime = agentruntime.NewService(fault)
		f.plane.Coordinator.AppServers = store
		f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: native, AgentRuntime: f.plane.Runs.AgentRuntime}, ContinuityDirectory: f.dataRoot, AppServers: store, AppServerSpec: spec, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}
		planeConfig = controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: spec}
		t.Cleanup(func() { stopContinuityFixtureServers(f) })
	})
}
