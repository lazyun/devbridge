package mcpserver

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

type recoveryAdapterFixture struct {
	*surfaceReadFixture
	reads, starts, resolves, continuations int
}

func (f *recoveryAdapterFixture) ContinueInspectionRecoveryForSession(context.Context, string, probeexecution.InspectionContinuationInput) (contextprobe.Probe, error) {
	f.continuations++
	return contextprobe.Probe{}, errors.New("not used")
}

func (f *recoveryAdapterFixture) PrepareRecoveryStatusForSession(context.Context, string, recoverystatus.Spec) (controlapi.RecoveryStatusResult, error) {
	return controlapi.RecoveryStatusResult{}, errors.New("not used")
}
func (f *recoveryAdapterFixture) StartRecoveryStatusForSession(context.Context, string, controlapi.RecoveryStatusCommand) (controlapi.RecoveryStatusResult, error) {
	f.starts++
	return controlapi.RecoveryStatusResult{}, errors.New("not used")
}
func (f *recoveryAdapterFixture) ResolveRecoveryStatusForSession(context.Context, string, controlapi.RecoveryStatusResolveCommand) (controlapi.RecoveryStatusResult, error) {
	f.resolves++
	return controlapi.RecoveryStatusResult{}, errors.New("not used")
}
func (f *recoveryAdapterFixture) ReadRecoveryStatusForSession(_ context.Context, session, id string) (controlapi.RecoveryStatusResult, error) {
	if session != "session" || id != "recovery" {
		return controlapi.RecoveryStatusResult{}, errors.New("scope mismatch")
	}
	f.reads++
	return controlapi.RecoveryStatusResult{Record: recoverystatus.Record{ID: id, State: "RESOLVED"}, Projection: recoverystatus.ProjectProjection(recoverystatus.Projection{State: "RESOLVED", Consistency: "CONSISTENT", EngineeringDisposition: "RECOVERED_COMPLETE", SourceResponseDisposition: "UNKNOWN"})}, nil
}
func TestRecoveryStatusActualSDKReadAndOutcomeInjectionRejection(t *testing.T) {
	f := &recoveryAdapterFixture{surfaceReadFixture: newSurfaceReadFixture()}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "recovery-contract", Version: "1"}, nil)
	RegisterRecoveryStatusTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	for i := 0; i < 2; i++ {
		out, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "recovery_status_read", Arguments: map[string]any{"aiSessionId": "session", "id": "recovery"}})
		if err != nil || out.IsError {
			t.Fatalf("read: %+v %v", out, err)
		}
	}
	for _, field := range []string{"engineeringDisposition", "admission", "force"} {
		t.Run(field, func(t *testing.T) {
			out, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "recovery_status_resolve", Arguments: map[string]any{"aiSessionId": "session", "command": map[string]any{field: "RECOVERED_COMPLETE"}}})
			if err == nil && !out.IsError {
				t.Fatal("client outcome accepted")
			}
		})
	}
	for _, field := range []string{"engineeringDisposition", "inspectionResult", "findings", "targetSet", "force"} {
		t.Run("continuation-"+field, func(t *testing.T) {
			out, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "project_inspection_recovery_continue", Arguments: map[string]any{"aiSessionId": "session", "command": map[string]any{"recoveryStatusId": "recovery", "requestId": "request", "instruction": "exact caller bytes", "payloadHash": "hash", "expectedRuntimeEpoch": "epoch", field: true}}})
			if err == nil && !out.IsError {
				t.Fatal("client continuation outcome accepted")
			}
		})
	}
	if f.reads != 2 || f.starts != 0 || f.resolves != 0 || f.continuations != 0 {
		t.Fatalf("unexpected effects: %+v", f)
	}
}
