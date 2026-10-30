package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
	"time"
)

func TestExecutionSourceRequiresSameTaskPredecessorAndProjectReceipt(t *testing.T) {
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Status: task.StatusActive, Phase: task.PhaseVerificationReady, CurrentExecutionID: "next"}
	executions := []executionproposal.Proposal{{ID: "old", TaskID: "task", ProjectID: "project"}, {ID: "next", TaskID: "task", ProjectID: "project"}}
	receipt := decisionresolution.Receipt{ProjectID: "project", Kind: decisionresolution.KindTaskAcceptance, SubjectID: "old", ResultKind: "EXECUTION_PROPOSAL", ResultID: "next", Option: "REQUEST_REVERIFICATION"}
	for _, valid := range []bool{true, false} {
		if !valid {
			receipt.ProjectID = "foreign"
		}
		view := BuildTaskWithHistory(owner, nil, nil, executions, []decisionresolution.Receipt{receipt}, time.Now())
		for _, history := range view.ExecutionHistory {
			if history.Execution.ID == "next" {
				if valid && history.SourceAction != "REQUEST_REVERIFICATION" {
					t.Fatal("verification-only successor lost authoritative source")
				}
				if !valid && history.SourceAction != "" {
					t.Fatal("foreign receipt provided successor reason")
				}
				if history.ImplementationRun != nil || history.VerificationRun != nil {
					t.Fatal("source projection fabricated a Run")
				}
			}
		}
	}
	receipt.ProjectID = "project"
	receipt.SubjectID = "another-task-execution"
	view := BuildTaskWithHistory(owner, nil, nil, executions, []decisionresolution.Receipt{receipt}, time.Now())
	for _, history := range view.ExecutionHistory {
		if history.SourceAction != "" {
			t.Fatal("foreign predecessor provided source")
		}
	}
}
