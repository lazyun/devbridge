package controlapi_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// This separately authorized real-model smoke never creates a semantic answer.
// Only the explicitly selected config/auth files are copied into a fresh home.
func TestRealCodexRequestReconnect(t *testing.T) {
	if os.Getenv("DEVBRIDGE_REAL_CODEX_REQUEST_RECONNECT") != "1" {
		t.Skip("REAL_CODEX_REQUEST_RECONNECT_E2E_NOT_RUN")
	}
	native := os.Getenv("DEVBRIDGE_REAL_CODEX")
	configFile := os.Getenv("DEVBRIDGE_REAL_CODEX_RECONNECT_CONFIG_FILE")
	authFile := os.Getenv("DEVBRIDGE_REAL_CODEX_RECONNECT_AUTH_FILE")
	// Validate every required opt-in before creating a Core or invoking Codex.
	for name, path := range map[string]string{"DEVBRIDGE_REAL_CODEX": native, "DEVBRIDGE_REAL_CODEX_RECONNECT_CONFIG_FILE": configFile, "DEVBRIDGE_REAL_CODEX_RECONNECT_AUTH_FILE": authFile} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must explicitly name an absolute isolated fixture file; no user home is discovered", name)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s must name an existing regular file", name)
		}
	}
	configBytes, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal("read explicitly selected config fixture:", err)
	}
	authBytes, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatal("read explicitly selected auth fixture:", err)
	}
	if len(configBytes) == 0 || !json.Valid(authBytes) {
		t.Fatal("explicit config must be nonempty and auth fixture must be valid JSON")
	}
	home := t.TempDir()
	if err = os.WriteFile(filepath.Join(home, "config.toml"), configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "auth.json"), authBytes, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "real-operator")
	spec := func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: home, Env: []string{"HOME=" + home, "CODEX_HOME=" + home}}
	}
	var planeConfig controlplane.Config
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		waitQuestion := func(previousConnection string) (agentruntime.Request, run.Run) {
			t.Helper()
			deadline := time.Now().Add(5 * time.Minute)
			for time.Now().Before(deadline) {
				values, e := f.plane.Runs.ListByTask(f.ctx, f.task)
				if e != nil {
					t.Fatal(e)
				}
				for _, value := range values {
					if value.AgentRuntime == nil {
						continue
					}
					for _, q := range value.AgentRuntime.Requests {
						if q.Kind == "CODEX_REQUEST" && q.State == "WAITING_FOR_USER" && q.ConnectionID != previousConnection {
							return q, value
						}
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			t.Fatal("real pending request not observed; no answer was generated")
			return agentruntime.Request{}, run.Run{}
		}
		originalRequest, originalRun := waitQuestion("")
		if originalRun.ExecutionProposalID != executionID {
			t.Fatal("request is not owned by original fixture execution")
		}
		binding, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, originalRun.ID)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = appserver.Stop(context.Background(), binding) })
		if e = f.plane.Close(); e != nil {
			t.Fatal(e)
		}
		f.plane = nil
		if e = appserver.Verify(f.ctx, binding); e != nil {
			t.Fatalf("Core close lost original App Server: %v", e)
		}
		f.plane, e = controlplane.Open(f.ctx, planeConfig)
		if e != nil {
			t.Fatal(e)
		}
		f.facade, e = controlapi.NewFromControlPlane(f.plane)
		if e != nil {
			t.Fatal(e)
		}
		recovered, current := waitQuestion(originalRequest.ConnectionID)
		if recovered.ID != originalRequest.ID || current.ID != originalRun.ID || current.ThreadID != originalRun.ThreadID || current.TurnID != originalRun.TurnID || recovered.RuntimeEpoch == originalRequest.RuntimeEpoch {
			t.Fatal("Core reopen replaced original request, Run, Thread, or Turn")
		}
		currentBinding, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, current.ID)
		if e != nil || currentBinding.ID != binding.ID || currentBinding.PID != binding.PID || currentBinding.ProcessStartIdentity != binding.ProcessStartIdentity {
			t.Fatal("Core reopen did not preserve exact instance", e)
		}
		requestPath := filepath.Join(f.dataRoot, "operator-reconnected-runtime-request.json")
		answerPath := filepath.Join(f.dataRoot, "operator-reconnected-user-decision.json")
		raw, _ := json.MarshalIndent(recovered, "", "  ")
		if e = os.WriteFile(requestPath, raw, 0600); e != nil {
			t.Fatal(e)
		}
		t.Logf("Original request reattached. Read %s and explicitly author UserDecisionInput at %s. Require SourceSurface REAL_SMOKE_OPERATOR and nonempty sourceUserEventId. No answer file or default answer is generated.", requestPath, answerPath)
		var decision agentruntime.UserDecision
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			data, readErr := os.ReadFile(answerPath)
			if os.IsNotExist(readErr) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			var input agentruntime.UserDecisionInput
			if e = json.Unmarshal(data, &input); e != nil {
				t.Fatal("operator answer file is invalid:", e)
			}
			if input.Identity != recovered.Identity || input.RuntimeRequestID != recovered.ID || input.Action != "RESPOND" || input.SourceSurface != "REAL_SMOKE_OPERATOR" || input.SourceUserEventID == "" {
				t.Fatal("operator-authored input must explicitly answer exact recovered request with user event provenance")
			}
			input.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(input)
			result, submitErr := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, input)
			if submitErr != nil {
				t.Fatal(submitErr)
			}
			decision = result.Decision
			break
		}
		if decision.ID == "" {
			t.Fatal("operator supplied no explicit answer within smoke deadline")
		}
		deadline = time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			value, getErr := f.plane.Runs.Get(f.ctx, originalRun.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if value.AgentRuntime != nil {
				for _, q := range value.AgentRuntime.Requests {
					if q.ID == originalRequest.ID && q.State == "RESPONDED" && value.AgentRuntime.TurnStatus == "completed" {
						if q.UserDecision == nil || q.UserDecision.ID != decision.ID || value.ThreadID != originalRun.ThreadID || value.TurnID != originalRun.TurnID {
							t.Fatal("original answered Turn identity changed")
						}
						t.Logf("same App Server %s, Run %s, Thread %s, Turn %s completed with exact operator decision %s", binding.ID, value.ID, value.ThreadID, value.TurnID, decision.ID)
						return false
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("original request acknowledgement and Turn completion not proven; no further semantic answer was generated")
		return false
	}, func(f *upperLifecycleFixture) {
		t.Cleanup(func() {
			if f.plane == nil {
				return
			}
			_ = f.plane.Supervisor.Shutdown()
			rows, err := f.plane.DB.SQL().Query(`SELECT binding_json FROM app_server_run_bindings`)
			if err != nil {
				return
			}
			defer rows.Close()
			for rows.Next() {
				var raw []byte
				if rows.Scan(&raw) == nil {
					var binding appserver.Binding
					if json.Unmarshal(raw, &binding) == nil {
						_ = appserver.Stop(context.Background(), binding)
					}
				}
			}
		})
		store := sqlite.NewAgentRuntimeStore(f.plane.DB)
		f.plane.Coordinator.AppServers = store
		f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: native, AgentRuntime: f.plane.Runs.AgentRuntime}, ContinuityDirectory: f.dataRoot, AppServers: store, AppServerSpec: spec, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}
		planeConfig = controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: spec}
	})
}
