package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/run"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type inspectionContinuationReadFixture struct {
	*surfaceReadFixture
	probe contextprobe.Probe
	runs  map[string]run.Run
}

func (f *inspectionContinuationReadFixture) GetContextProbeForSession(_ context.Context, session, id string) (contextprobe.Probe, error) {
	if session != "session" || id != f.probe.ID {
		return contextprobe.Probe{}, errors.New("Inspection scope mismatch")
	}
	return f.probe, nil
}
func (f *inspectionContinuationReadFixture) GetRunForSession(_ context.Context, session, id string) (run.Run, error) {
	f.runReads++
	if session != "session" {
		return run.Run{}, errors.New("Run scope mismatch")
	}
	r, ok := f.runs[id]
	if !ok {
		return run.Run{}, errors.New("Run unavailable")
	}
	return r, nil
}
func (f *inspectionContinuationReadFixture) GetAgentExecutionForSession(_ context.Context, session, id string) (agentexecution.Projection, error) {
	if session != "session" || id != "agent-execution" {
		return agentexecution.Projection{}, errors.New("Execution scope mismatch")
	}
	// The universal envelope remains bound to the immutable original Run.
	return agentexecution.Projection{ExecutionID: id, ProjectID: "project", TaskID: "task", Kind: agentexecution.KindInspection, SemanticOwnerKind: "INSPECTION", SemanticOwnerID: "inspection", RunID: "source-run", RunIDs: []string{"source-run"}, Status: "FAILED"}, nil
}

func newInspectionContinuationReadFixture() *inspectionContinuationReadFixture {
	base := newSurfaceReadFixture()
	base.task.FeatureAreaID = ""
	base.workflow = p.Projection{}
	request := runtimePayloadRequest()
	request.ID, request.RunID, request.OperationKind, request.OperationID = "continuation-request", "continuation-run", "INSPECTION", "inspection"
	return &inspectionContinuationReadFixture{surfaceReadFixture: base,
		probe: contextprobe.Probe{ID: "inspection", AISessionID: "session", ProjectID: "project", TaskID: "task", RunID: "source-run", RecoverySuccessorRunID: "continuation-run", Status: contextprobe.StatusRunning},
		runs: map[string]run.Run{
			"source-run": {ID: "source-run", AgentExecutionID: "agent-execution", ProjectID: "project", TaskID: "task", ContextProbeID: "inspection", OperationKind: run.OperationInspection, OperationID: "inspection", Purpose: run.PurposeInspection, Status: run.StatusFailed},
			// Core persists the exact continuation receipt and successor pointer;
			// continuation Runs intentionally keep ContextProbeID empty.
			"continuation-run": {ID: "continuation-run", AgentExecutionID: "agent-execution", ProjectID: "project", TaskID: "task", OperationKind: run.OperationInspection, OperationID: "inspection", Purpose: run.PurposeInspection, Status: run.StatusRunning, AgentRuntime: &agentruntime.Snapshot{Requests: []agentruntime.Request{request}}},
		},
	}
}
func inspectionReadInput(runID string) OperationGetInput {
	return OperationGetInput{AISessionID: "session", Target: p.OperationTarget{Kind: "INSPECTION", OperationID: "inspection", OwnerTaskID: "task", RunID: runID}, Include: []string{"RESULT", "RUNTIME_REQUESTS"}}
}

func TestOperationGetInspectionContinuationActualSDK(t *testing.T) {
	f := newInspectionContinuationReadFixture()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "inspection-continuation-read", Version: "1"}, nil)
	RegisterSurfaceBusinessTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	for _, runID := range []string{"source-run", "continuation-run", ""} {
		t.Run("run="+runID, func(t *testing.T) {
			out, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "operation_get", Arguments: inspectionReadInput(runID)})
			if err != nil {
				t.Fatal(err)
			}
			if out.IsError {
				t.Fatalf("valid Inspection Run read failed: %+v", out)
			}
			encoded, _ := json.Marshal(out.StructuredContent)
			var got OperationGetOutput
			if err = json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			want := runID
			if want == "" {
				want = "continuation-run"
			}
			if got.Result.Operation.RunID != want || got.Context.Target == nil || got.Context.Target.RunID != want {
				t.Fatalf("selected wrong Run: %+v", got)
			}
			if want == "continuation-run" && (got.Result.CoreOperation != nil || got.Result.Cancellation != nil || got.Result.Operation.Status != "RUNNING") {
				t.Fatalf("historical envelope replaced current continuation: %+v", got.Result)
			}
			// The original source identity stays historical rather than being rewritten.
			if got.Result.Inspection.RunID != "source-run" || len(got.Result.RunResults) != 2 || got.Result.RunResults[1].RunID != "continuation-run" || got.Result.RunResults[1].Status != "RUNNING" {
				t.Fatalf("lost source/continuation results: %+v", got.Result)
			}
			if len(got.Result.RuntimeRequests) != 1 || got.Result.RuntimeRequests[0].ID != "continuation-request" {
				t.Fatalf("continuation request unavailable: %+v", got.Result.RuntimeRequests)
			}
		})
	}
}

func TestOperationGetInspectionContinuationRejectsUnboundRuns(t *testing.T) {
	for _, field := range []string{"session", "target-owner", "unlinked", "recovery-child", "id", "project", "task", "kind", "missing-kind", "operation", "missing-operation", "context-probe", "purpose", "feature-area"} {
		t.Run(field, func(t *testing.T) {
			f := newInspectionContinuationReadFixture()
			in := inspectionReadInput("continuation-run")
			r := f.runs["continuation-run"]
			switch field {
			case "session":
				in.AISessionID = "other-session"
			case "target-owner":
				in.Target.OwnerTaskID = "other-task"
			case "unlinked":
				f.probe.RecoverySuccessorRunID = ""
			case "recovery-child":
				in.Target.RunID = "diagnostic-run"
				r.ID = in.Target.RunID
				r.Purpose = run.PurposeRecoveryStatus
				f.runs[r.ID] = r
			case "id":
				r.ID = "other-run"
			case "project":
				r.ProjectID = "other-project"
			case "task":
				r.TaskID = "other-task"
			case "kind":
				r.OperationKind = run.OperationReview
			case "missing-kind":
				r.OperationKind = ""
			case "operation":
				r.OperationID = "other-inspection"
			case "missing-operation":
				r.OperationID = ""
			case "context-probe":
				r.ContextProbeID = "other-inspection"
			case "purpose":
				r.Purpose = run.PurposeRecoveryStatus
			case "feature-area":
				r.FeatureAreaID = "other-area"
			}
			f.runs["continuation-run"] = r
			if out, err := OperationGetCore(context.Background(), f, in); err == nil {
				t.Fatalf("unbound %s accepted: %+v", field, out)
			}
		})
	}
}

func TestOperationGetInspectionWithoutContinuationKeepsOriginal(t *testing.T) {
	f := newInspectionContinuationReadFixture()
	f.probe.RecoverySuccessorRunID = ""
	out, err := OperationGetCore(context.Background(), f, inspectionReadInput(""))
	if err != nil || out.Result.Operation.RunID != "source-run" || len(out.Result.RunResults) != 1 {
		t.Fatalf("ordinary Inspection changed: %+v %v", out, err)
	}
}
