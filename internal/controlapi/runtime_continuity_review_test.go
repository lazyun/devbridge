package controlapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// The installed App Server talks exclusively to this loopback Responses
// fixture. This does not opt into the real-model smoke.
func TestRuntimeContinuityOfflineCodingReviewCoreReopen(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	if native == "" {
		t.Skip("offline installed App Server binary not selected")
	}
	var planeConfig controlplane.Config
	var calls *atomic.Int64
	runCodingReviewFixture(t, func(f *upperLifecycleFixture, taskID, reviewID, runID string) bool {
		f.task = taskID
		request, original := waitContinuityQuestion(t, f, "")
		if original.ID != runID || original.ReviewID != reviewID || request.ReviewID != reviewID {
			t.Fatalf("wrong review request: Run=%+v request=%+v", original, request)
		}
		assertNoAutomaticAnswer(t, f, request, task.PhaseReviewing)
		binding, err := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = appserver.Stop(context.Background(), binding) })
		if err = f.plane.Close(); err != nil {
			t.Fatal(err)
		}
		f.plane = nil
		if err = appserver.Verify(f.ctx, binding); err != nil {
			t.Fatalf("Core close killed original review server: %v", err)
		}
		f.plane, err = controlplane.Open(f.ctx, planeConfig)
		if err != nil {
			t.Fatal(err)
		}
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
		recovered, current := waitContinuityQuestion(t, f, request.ConnectionID)
		if recovered.ID != request.ID || current.ID != runID || current.ReviewID != reviewID || current.ThreadID != original.ThreadID || current.TurnID != original.TurnID || recovered.RuntimeEpoch == request.RuntimeEpoch {
			t.Fatalf("recovered review identity changed: old=%+v new=%+v", request, recovered)
		}
		assertNoAutomaticAnswer(t, f, recovered, task.PhaseReviewing)
		answer, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, recovered, "RESPOND", "yes"))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			current, err = f.plane.Runs.Get(f.ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if current.AgentRuntime != nil && current.AgentRuntime.TurnStatus == "completed" {
				for _, q := range current.AgentRuntime.Requests {
					if q.ID == request.ID && q.State == "RESPONDED" {
						if q.UserDecision == nil || q.UserDecision.ID != answer.Decision.ID || calls.Load() != 2 || current.ThreadID != original.ThreadID || current.TurnID != original.TurnID {
							t.Fatalf("review answer duplicated/replaced: request=%+v calls=%d", q, calls.Load())
						}
						t.Logf("same review AppServer=%s PID=%d Run=%s Thread=%s Turn=%s request=%s decision=%s", binding.ID, binding.PID, runID, current.ThreadID, current.TurnID, q.ID, answer.Decision.ID)
						return false
					}
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("review original Turn did not complete: Run=%+v failure=%+v runtime=%+v calls=%d", current, current.Failure, current.AgentRuntime, calls.Load())
		return false
	}, func(f *upperLifecycleFixture) {
		calls = &atomic.Int64{}
		requestCount := calls
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
			n := requestCount.Add(1)
			var item map[string]any
			if n == 1 {
				args := `{"questions":[{"id":"adaptation","header":"Review","question":"May the read-only review continue?","options":[{"label":"yes","description":"Continue review"},{"label":"no","description":"Stop review"}]}]}`
				item = map[string]any{"type": "function_call", "call_id": "fixture-review-question", "name": "request_user_input", "arguments": args}
			} else {
				result := os.Getenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT")
				if !json.Valid([]byte(result)) {
					http.Error(w, "missing review fixture result", 500)
					return
				}
				item = map[string]any{"id": "fixture-review-result", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": result}}}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": fmt.Sprint("review-response-", n)}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": fmt.Sprint("review-response-", n), "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
				data, _ := json.Marshal(event)
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
			}
		}))
		t.Cleanup(provider.Close)
		home := t.TempDir()
		config := fmt.Sprintf("openai_base_url=%q\nmodel=\"mock-model\"\nmodel_provider=\"openai\"\n[features]\ndefault_mode_request_user_input=true\nresponses_websockets=false\nresponses_websockets_v2=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1")
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"offline-fixture"}`), 0600); err != nil {
			t.Fatal(err)
		}
		spec := func(codexconfig.Config) appserver.Spec {
			return appserver.Spec{ExecutablePath: native, CodexHome: home, Env: []string{"OPENAI_BASE_URL=" + provider.URL + "/v1", "OPENAI_API_KEY=offline-fixture"}}
		}
		store := sqlite.NewAgentRuntimeStore(f.plane.DB)
		f.plane.Coordinator.AppServers = store
		f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: native, AgentRuntime: f.plane.Runs.AgentRuntime}, ContinuityDirectory: f.dataRoot, AppServers: store, AppServerSpec: spec, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}
		planeConfig = controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: spec}
		t.Cleanup(func() { stopContinuityFixtureServers(f) })
	})
}
