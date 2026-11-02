package mcpserver

import (
	"context"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"reflect"
	"testing"
)

func TestHistoricalInspectionRunCannotInheritCurrentWorkflowActions(t *testing.T) {
	for _, tc := range []struct {
		name, selected, currentKind, currentID, currentRun string
		missing, match                                     bool
	}{
		{name: "current continuation", selected: "continuation-run", currentKind: "INSPECTION", currentID: "inspection", currentRun: "continuation-run", match: true},
		{name: "historical source same operation", selected: "source-run", currentKind: "INSPECTION", currentID: "inspection", currentRun: "continuation-run"},
		{name: "different operation", selected: "continuation-run", currentKind: "INSPECTION", currentID: "successor-inspection", currentRun: "continuation-run"},
		{name: "different kind", selected: "continuation-run", currentKind: "REVIEW", currentID: "inspection", currentRun: "continuation-run"},
		{name: "current without live claim", selected: "continuation-run", missing: true, match: true},
		{name: "historical without live claim", selected: "source-run", missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInspectionContinuationReadFixture()
			f.coreRunID = "continuation-run"
			f.workflow = p.Projection{CurrentState: "WAITING_ANALYSIS", NextAction: wp.Action{Code: "ANALYZE_CURRENT_RUN", Required: true}, NextStep: &p.TaskNextStep{Code: "RUN_ANALYSIS_REQUIRED", TaskID: "task", RunID: "continuation-run"}, VerificationDecisions: &p.VerificationDecisions{TaskID: "task", ExecutionID: "current-execution", RevisionID: "current-revision"}, Capabilities: []p.Capability{{Action: "CONTINUE_TASK", Available: true}}, PendingDecision: &p.Decision{Kind: "TASK_ACCEPTANCE", ID: "current-decision"}, Details: p.Details{ProjectID: "project", TaskID: "task"}}
			if !tc.missing {
				f.workflow.CurrentOperation = &p.Operation{Kind: tc.currentKind, ID: tc.currentID, RunID: tc.currentRun}
			}
			original := projectSurfaceWorkflow(f.workflow)
			got, err := OperationGetCore(context.Background(), f, inspectionReadInput(tc.selected))
			if err != nil {
				t.Fatal(err)
			}
			if got.Result.Operation.RunID != tc.selected || got.Context.Target.RunID != tc.selected {
				t.Fatalf("history target changed: %+v", got)
			}
			if tc.match {
				want := original
				if tc.missing {
					want.PendingDecisions = []p.PendingDecision{}
					want.Capabilities = []p.Capability{}
				}
				if !reflect.DeepEqual(got.Workflow, want) {
					t.Fatalf("current workflow altered: got=%+v want=%+v", got.Workflow, original)
				}
			} else {
				w := got.Workflow
				if !w.RefreshRequired || w.NextAction.Code != "" || w.NextAction.Required || w.NextStep != nil || w.VerificationDecisions != nil || len(w.Capabilities) != 0 || len(w.PendingDecisions) != 0 {
					t.Fatalf("history inherited current authority: %+v", w)
				}
			}
			if !reflect.DeepEqual(projectSurfaceWorkflow(f.workflow), original) {
				t.Fatal("shared Core projection mutated by read")
			}
		})
	}
}
