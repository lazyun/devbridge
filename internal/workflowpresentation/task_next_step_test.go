package workflowpresentation

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
	"time"
)

func nextStepFixture() (task.Task, []run.Run, []executionproposal.Proposal) {
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseRecoveryRequired, CurrentExecutionID: "execution", CurrentDefinitionRevisionID: "revision", RecoveryOrigin: &task.RecoveryOrigin{Phase: task.PhaseVerifying, SourceEventID: "origin"}}
	values := []run.Run{{ID: "impl", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutionProposalID: "execution", Purpose: run.PurposeImplementation, Status: run.StatusSucceeded, AnalysisState: "OPTIONAL"}, {ID: "verify", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutionProposalID: "execution", Purpose: run.PurposeVerification, Status: run.StatusInfrastructureFailed, AnalysisState: "NOT_REQUIRED"}}
	executions := []executionproposal.Proposal{{ID: "execution", TaskID: "task", ProjectID: "project", TaskDefinitionRevisionID: "revision", ImplementationRunID: "impl", VerificationRunID: "verify"}}
	return owner, values, executions
}
func TestNextStepOrdersAnalysisWithoutChangingRecoveryAuthority(t *testing.T) {
	owner, runs, executions := nextStepFixture()
	before, _ := json.Marshal(owner)
	v := BuildTaskWithHistory(owner, runs, nil, executions, nil, time.Time{})
	if v.NextStep == nil || v.NextStep.Code != "ANALYZE_RUN_OUTPUT" || v.NextStep.RunID != "impl" || !v.NextStep.Recommended || v.NextStep.Purpose != "IMPLEMENTATION" {
		t.Fatalf("next step: %+v", v.NextStep)
	}
	if v.Phase != task.PhaseRecoveryRequired || v.BusinessPhase != task.PhaseVerifying || v.Status != "ACTIVE" || v.ActionsStatus != "UNAVAILABLE" || len(v.Actions) != 0 {
		t.Fatalf("analysis invented admission: %+v", v)
	}
	after, _ := json.Marshal(owner)
	if string(before) != string(after) {
		t.Fatal("presentation mutated Task")
	}
	// Recording analysis removes the recommendation and reveals the unchanged
	// recovery requirement; it never advances the Task or starts verification.
	runs[0].AnalysisState = "RECORDED"
	v = BuildTaskWithHistory(owner, runs, nil, executions, nil, time.Time{})
	if v.NextStep != nil || v.Phase != task.PhaseRecoveryRequired {
		t.Fatalf("recorded analysis changed phase: %+v", v)
	}
	runs[1].AnalysisState = "REQUIRED"
	v = BuildTaskWithHistory(owner, runs, nil, executions, nil, time.Time{})
	if v.NextStep == nil || v.NextStep.RunID != "verify" || v.NextStep.Recommended {
		t.Fatalf("required verification priority: %+v", v.NextStep)
	}
}
func TestNextStepRejectsMissingStaleHistoricalAndActiveFacts(t *testing.T) {
	for _, name := range []string{"wrong execution", "wrong purpose", "wrong revision", "missing origin", "other stage", "active verification", "terminal task", "historical task", "analysis recorded", "unknown analysis"} {
		t.Run(name, func(t *testing.T) {
			o, r, e := nextStepFixture()
			switch name {
			case "wrong execution":
				r[0].ExecutionProposalID = "previous"
			case "wrong purpose":
				r[0].Purpose = run.PurposeScopeReview
			case "wrong revision":
				e[0].TaskDefinitionRevisionID = "previous"
			case "missing origin":
				o.RecoveryOrigin = nil
			case "other stage":
				o.Phase = task.PhaseReviewing
			case "active verification":
				r[1].Status = run.StatusRunning
			case "terminal task":
				o.Status = task.StatusCanceled
			case "historical task":
				o.LegacyReadOnly = true
			case "analysis recorded":
				r[0].AnalysisState = "RECORDED"
			case "unknown analysis":
				r[0].AnalysisState = ""
			}
			v := BuildTaskWithHistory(o, r, nil, e, nil, time.Time{})
			if v.NextStep != nil {
				t.Fatalf("borrowed next step: %+v", v.NextStep)
			}
		})
	}
}

func TestNativeApprovalNextStepPreservesImplementationPhase(t *testing.T) {
	owner, runs, executions := nextStepFixture()
	owner.Phase = task.PhaseImplementing
	owner.RecoveryOrigin = nil
	runs = runs[:1]
	executions[0].VerificationRunID = ""
	r := &runs[0]
	r.Status = run.StatusRunning
	r.ThreadID = "thread"
	r.TurnID = "turn"
	identity := agentruntime.Identity{RunID: r.ID, TaskID: owner.ID, ProjectID: r.ProjectID, FeatureAreaID: r.FeatureAreaID, ThreadID: r.ThreadID, TurnID: r.TurnID, OperationKind: "EXECUTION", OperationID: executions[0].ID, TaskDefinitionRevisionID: executions[0].TaskDefinitionRevisionID}
	q := agentruntime.Request{Identity: identity, ID: "approval", Kind: "RUNTIME_APPROVAL", State: "PENDING", Method: "item/commandExecution/requestApproval"}
	r.AgentRuntime = &agentruntime.Snapshot{Identity: identity, Availability: "AVAILABLE", ConnectionState: "CONNECTED", WaitingState: "WAITING_FOR_COMMAND_APPROVAL", Requests: []agentruntime.Request{q}}
	before, _ := json.Marshal(owner)
	v := BuildTaskWithHistory(owner, runs, nil, executions, nil, time.Time{})
	if v.NextStep == nil || v.NextStep.Code != "RESOLVE_RUNTIME_APPROVAL" || v.NextStep.Label != "等待命令批准" || v.NextStep.RunID != r.ID || v.Phase != task.PhaseImplementing {
		t.Fatalf("%+v activity=%+v", v.NextStep, v.Activity)
	}
	after, _ := json.Marshal(owner)
	if string(before) != string(after) {
		t.Fatal("presentation changed workflow")
	}
	r.AgentRuntime.Requests[0].Kind = "CODEX_REQUEST"
	v = BuildTaskWithHistory(owner, runs, nil, executions, nil, time.Time{})
	if v.NextStep != nil {
		t.Fatal("semantic question shown as permission")
	}
}
