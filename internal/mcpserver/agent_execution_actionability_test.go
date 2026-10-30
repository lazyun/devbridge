package mcpserver

import (
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowstate"
	"testing"
)

func TestExecutionDecisionAndHandleRequireExactCoreActionability(t *testing.T) {
	e := executionproposal.Proposal{ID: "execution", TaskID: "task", ProjectID: "project", Status: executionproposal.StatusPendingConfirmation}
	p := agentexecution.Projection{ExecutionID: e.ID, ExecutionProposalID: e.ID, TaskID: e.TaskID, ProjectID: e.ProjectID, Kind: agentexecution.KindDevelopment, SemanticOwnerKind: "EXECUTION", SemanticOwnerID: e.ID, Status: "PENDING", UserAuthorizationRequired: true}
	for _, tc := range []struct {
		name   string
		change func(*agentexecution.Projection)
		want   bool
	}{
		{"pending", func(*agentexecution.Projection) {}, true},
		{"rejected", func(p *agentexecution.Projection) { p.Status = "REJECTED" }, false},
		{"canceled", func(p *agentexecution.Projection) { p.Status = "CANCELED" }, false},
		{"completed", func(p *agentexecution.Projection) { p.Status = "COMPLETED" }, false},
		{"failed", func(p *agentexecution.Projection) { p.Status = "FAILED" }, false},
		{"unknown", func(p *agentexecution.Projection) { p.Status = "UNKNOWN" }, false},
		{"orphan", func(p *agentexecution.Projection) { p.Status = "UNKNOWN_ORPHANED" }, false},
		{"other execution", func(p *agentexecution.Projection) { p.ExecutionID = "other" }, false},
		{"other proposal", func(p *agentexecution.Projection) { p.ExecutionProposalID = "other" }, false},
		{"other task", func(p *agentexecution.Projection) { p.TaskID = "other" }, false},
		{"other project", func(p *agentexecution.Projection) { p.ProjectID = "other" }, false},
		{"other owner", func(p *agentexecution.Projection) { p.SemanticOwnerID = "other" }, false},
		{"verification pending", func(p *agentexecution.Projection) {
			p.RunIDs = []string{"implementation"}
			p.UserAuthorizationRequired = false
		}, false},
		{"read only", func(p *agentexecution.Projection) { p.ReadOnly = true }, false},
		{"no user authorization", func(p *agentexecution.Projection) { p.UserAuthorizationRequired = false }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := p
			tc.change(&copy)
			current := controlapi.CurrentWorkflow{AgentExecution: &copy}
			projection := workflowProjection{proposal: &e, current: current}
			if (pendingDecision(projection, current) != nil) != false || (pendingDecisionHandle(projection) != nil) != false {
				t.Fatal("decision/handle ignored exact Core", copy)
			}
		})
	}
}

func TestCardPresentationConsumesCoreInsteadOfTerminalDomainOutcome(t *testing.T) {
	for _, status := range []string{"PENDING", "FINALIZING", "UNKNOWN", "UNKNOWN_ORPHANED", "COMPLETED", "FAILED", "REJECTED", "CANCELED"} {
		t.Run(status, func(t *testing.T) {
			p := agentexecution.Projection{Status: status, Kind: agentexecution.KindDevelopment, UserAuthorizationRequired: status == "PENDING"}
			card := UIWorkflowStateOutput{AgentExecution: &p, Phase: "CANCELED", Terminal: true, ExecutionProposal: &ExecutionProposalOutput{Status: "PENDING_CONFIRMATION"}}
			applyCoreExecutionCard(&card)
			if !card.Terminal || (card.ExecutionProposal != nil) != p.AuthorizationActionable() {
				t.Fatal(card)
			}
			if (status == "UNKNOWN" || status == "UNKNOWN_ORPHANED" || status == "FINALIZING") && card.Phase != "CANCELED" {
				t.Fatal("Core disposition flattened", card)
			}
		})
	}
}

func TestVerificationPendingCannotAdvertiseAuthorizationGuidance(t *testing.T) {
	e := executionproposal.Proposal{ID: "execution", ProjectID: "project", TaskID: "task", Status: executionproposal.StatusConsumed}
	p := agentexecution.Projection{ExecutionID: e.ID, ExecutionProposalID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, Kind: agentexecution.KindDevelopment, Status: "PENDING", Stage: "VERIFICATION", RunIDs: []string{"implementation"}}
	current := controlapi.CurrentWorkflow{AgentExecution: &p, Guidance: &wp.Guidance{TaskID: "task", TaskPhase: task.PhaseVerifying, CurrentState: "VERIFYING", NextAction: wp.Action{Code: "WAIT"}}}
	projection := workflowProjection{proposal: &e, current: current, featureArea: &featurearea.FeatureArea{Status: featurearea.StatusActive}}
	applyProductWorkflow(&projection, current, workflowstate.Input{}, usableContext())
	if projection.output.State != "VERIFYING" || projection.output.PendingDecision != nil || projection.output.Capabilities.CanExecute || workflowExecutionState(&p) == workflowstate.ExecutionPendingAuthorization {
		t.Fatal(projection.output)
	}
}
