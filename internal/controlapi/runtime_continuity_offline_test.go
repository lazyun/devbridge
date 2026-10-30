package controlapi_test

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/run"
	"os"
	"testing"
	"time"
)

// Keep the original opt-in entry point, using the same measured qualification
// and immutable start/rejoin checks as the current native continuity gate.
func TestRuntimeContinuityOfflineCoreReopen(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	if native == "" {
		t.Skip("offline installed App Server binary not selected")
	}
	runQualifiedNativeContinuity(t, native, []string{"waiting", "recorded", "lost"})
}

func waitContinuityQuestion(t *testing.T, f *upperLifecycleFixture, previousConnection string) (agentruntime.Request, run.Run) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		values, err := f.plane.Runs.ListByTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range values {
			if v.Failure != nil {
				t.Fatalf("Run failed before request: %+v", *v.Failure)
			}
			if v.AgentRuntime != nil {
				for _, q := range v.AgentRuntime.Requests {
					if q.State == "WAITING_FOR_USER" && q.ConnectionID != previousConnection && len(q.AvailableActions) > 0 {
						return q, v
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	values, _ := f.plane.Runs.ListByTask(f.ctx, f.task)
	for _, v := range values {
		if v.AgentRuntime != nil && v.AgentRuntime.LastError != nil {
			t.Logf("runtime error=%+v", *v.AgentRuntime.LastError)
		}
		t.Logf("run=%+v runtime=%+v", v, v.AgentRuntime)
	}
	t.Fatal("no exact pending request after Core startup")
	return agentruntime.Request{}, run.Run{}
}

// Stop admissions before enumerating owned receipts, so failure cleanup cannot
// race a canonical Implementation-to-Verification continuation starting S2.
func stopContinuityFixtureServers(f *upperLifecycleFixture) {
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
			var b appserver.Binding
			if json.Unmarshal(raw, &b) == nil {
				_ = appserver.Stop(context.Background(), b)
			}
		}
	}
}
