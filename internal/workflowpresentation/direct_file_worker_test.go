package workflowpresentation

import (
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func fileWorkerProjection() Projection {
	state := &directfile.State{State: "RECOVERY_REQUIRED", ProtocolVersion: 2, AISessionID: "session", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", DecisionID: "approval", WorkflowID: "workflow", RuntimeEpoch: "old-epoch", GrantGeneration: 1, FailureCode: directfile.WorkerContinuationUnsupported, FailureReason: "Same-workflow worker continuation is unsupported; cancellation does not prove worker stop."}
	return Projection{DirectFile: state, TaskView: &TaskView{ID: "task", DirectFile: state}, Details: Details{TaskID: "task", TaskDefinitionRevisionID: "revision"}, Guidance: &wp.Guidance{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", NextAction: wp.Action{Code: "CANCEL_DIRECT_FILE"}}, Recovery: &Recovery{Required: true, Summary: "Generic recovery"}, NextAction: wp.Action{Code: "RECOVER_DIRECT_FILE"}}
}

func TestDirectFileWorkerCurrentActionUsesOnlyExactObservedCancellation(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*Projection)
	}{
		{"exact", "CANCEL_DIRECT_FILE", func(*Projection) {}},
		{"no-guidance", "REFRESH", func(p *Projection) { p.Guidance = nil }},
		{"unresolved-effects", "REFRESH", func(p *Projection) { p.Guidance.NextAction.Code = "REFRESH" }},
		{"legacy-recovery-copy", "REFRESH", func(p *Projection) { p.Guidance.NextAction.Code = "RECOVER_DIRECT_FILE" }},
		{"foreign-task", "REFRESH", func(p *Projection) { p.TaskView.ID = "another" }},
		{"foreign-revision", "REFRESH", func(p *Projection) { p.Details.TaskDefinitionRevisionID = "another" }},
		{"foreign-guidance", "REFRESH", func(p *Projection) { p.Guidance.TaskID = "another" }},
		{"foreign-project", "REFRESH", func(p *Projection) { p.Guidance.ProjectID = "another" }},
		{"foreign-area", "REFRESH", func(p *Projection) { p.Guidance.FeatureAreaID = "another" }},
		{"missing-epoch", "REFRESH", func(p *Projection) { p.DirectFile.RuntimeEpoch = "" }},
		{"missing-workflow", "REFRESH", func(p *Projection) { p.DirectFile.WorkflowID = "" }},
		{"missing-approval", "REFRESH", func(p *Projection) { p.DirectFile.DecisionID = "" }},
		{"missing-selected-grant", "REFRESH", func(p *Projection) { p.TaskView.DirectFile = nil }},
		{"foreign-selected-grant", "REFRESH", func(p *Projection) { c := *p.DirectFile; c.WorkflowID = "another"; p.TaskView.DirectFile = &c }},
		{"missing-generation", "REFRESH", func(p *Projection) { p.DirectFile.GrantGeneration = 0 }},
		{"still-enabled", "REFRESH", func(p *Projection) { p.DirectFile.Enabled = true }},
		{"terminal-task", "REFRESH", func(p *Projection) { p.TaskView.Terminal = true }},
		{"wait", "WAIT_FOR_DIRECT_FILE", func(p *Projection) { p.Guidance.NextAction.Code = "WAIT_FOR_DIRECT_FILE" }},
		{"blocking-current-confirmation", "RESOLVE_CURRENT_TASK_ACTION", func(p *Projection) {
			p.NextAction = wp.Action{Code: "RESOLVE_CURRENT_TASK_ACTION", Required: true, Message: "Read the original unknown confirmation; no new cancellation."}
			p.TaskView.ActionNode = &taskaction.Node{TaskID: "task", IntentID: "original", State: "UNKNOWN", Blocking: true}
		}},
		{"missing-blocking-node", "REFRESH", func(p *Projection) { p.NextAction.Code = "RESOLVE_CURRENT_TASK_ACTION" }},
		{"foreign-blocking-node", "REFRESH", func(p *Projection) {
			p.NextAction.Code = "RESOLVE_CURRENT_TASK_ACTION"
			p.TaskView.ActionNode = &taskaction.Node{TaskID: "other", Blocking: true}
		}},
		{"no-longer-blocking-node", "REFRESH", func(p *Projection) {
			p.NextAction.Code = "RESOLVE_CURRENT_TASK_ACTION"
			p.TaskView.ActionNode = &taskaction.Node{TaskID: "task"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fileWorkerProjection()
			tc.change(&p)
			before := *p.DirectFile
			got := CurrentAction(p)
			if got.Code != tc.want {
				t.Fatalf("action=%+v want=%s", got, tc.want)
			}
			if tc.want == "RESOLVE_CURRENT_TASK_ACTION" && got != p.NextAction {
				t.Fatal("blocking action lost its original requirement/message")
			}
			if p.DirectFile.State != before.State || p.DirectFile.RuntimeEpoch != before.RuntimeEpoch || p.DirectFile.GrantGeneration != before.GrantGeneration {
				t.Fatal("projection changed authority")
			}
		})
	}
}

func TestDirectFileWorkerActionPreservesOtherCapabilitiesAndLegacyRecovery(t *testing.T) {
	p := fileWorkerProjection()
	p.TaskView.ID = "other-task"
	p.Capabilities = []Capability{{Action: "CANCEL_EXECUTION", Available: true, Target: &ActionTarget{OperationKind: "TASK", OperationID: "other-task", TaskID: "other-task"}}}
	if CurrentAction(p).Code != "REFRESH" || !p.Capabilities[0].Available || p.Capabilities[0].Target.TaskID != "other-task" {
		t.Fatal("other Task capability replaced")
	}
	p = fileWorkerProjection()
	p.DirectFile.FailureCode = "CORE_RESTART"
	if CurrentAction(p).Code != "RECOVER" {
		t.Fatal("legacy recovery changed")
	}
	p = fileWorkerProjection()
	p.PendingDecision = &Decision{ID: "decision", Summary: "Existing independent decision"}
	if CurrentAction(p).Code != "RESOLVE_DECISION" {
		t.Fatal("decision precedence changed")
	}
}
