package controlapi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/probeexecution"
)

func TestRuntimeUnknownProductionInspectionTerminalHold(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	t.Setenv("DEVBRIDGE_INSPECTION_DECISION_HELPER", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	launcher := filepath.Join(t.TempDir(), "fake-inspection")
	executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestInspectionUserDecisionAppServerHelper$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := probeexecution.NewRunner(f.plane.ContextProbes, f.plane.Repositories, f.plane.CodexConfigs, inspectionDecisionFactory{config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}})
	if err != nil {
		t.Fatal(err)
	}
	runner.Cancellations = f.plane.ExecutionCancellations
	runner.AgentRuntime = f.plane.Runs.AgentRuntime
	f.plane.ContextProbeRunner.Shutdown()
	f.plane.ContextProbeRunner = runner
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := f.facade.CreateContextProbe(f.ctx, contextprobe.CreateInput{AISessionID: f.session, RepositoryID: f.repos["app"], CodexConfigID: codexconfig.DefaultConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: "Inspect only; ask the user before adaptation"})
	if err != nil {
		t.Fatal(err)
	}
	probe, err = f.facade.StartContextProbe(f.ctx, f.session, probe.ID)
	if err != nil {
		t.Fatal(err)
	}
	var request agentruntime.Request
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, probe.RunID)
		if e != nil {
			t.Fatal(e)
		}
		for _, q := range state.Requests {
			if q.State == "WAITING_FOR_USER" {
				request = q
			}
		}
		if request.ID != "" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if request.ID == "" {
		t.Fatal("missing inspection request")
	}
	if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, request, "RESPOND", "no")); err != nil {
		t.Fatal(err)
	}
	q := waitUnknownRequest(t, f, request, true)
	assertUnknownContract(t, f, q, "")
	probe, err = f.facade.GetContextProbeForSession(f.ctx, f.session, probe.ID)
	if err != nil || probe.Status != contextprobe.StatusRunning {
		t.Fatalf("inspection advanced: %+v %v", probe, err)
	}
	out, err := f.facade.RecoverAgentRequestForSession(f.ctx, f.session, controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "inspection-unknown-cancel", Action: "CANCEL_OPERATION"})
	if err != nil || out.Cancellation == nil {
		t.Fatalf("inspection cancel %+v %v", out, err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		probe, err = f.facade.GetContextProbeForSession(f.ctx, f.session, probe.ID)
		if err != nil {
			t.Fatal(err)
		}
		if probe.Status != contextprobe.StatusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if probe.Status != contextprobe.StatusCanceled {
		t.Fatalf("inspection not canceled: %+v", probe)
	}
}
