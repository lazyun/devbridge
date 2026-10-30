package mcpserver

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/executionproposal"
)

// Correlation belongs to the adapter; actionability belongs to the Core result.
func authorizationActionable(proposal *executionproposal.Proposal, p *agentexecution.Projection) bool {
	return proposal != nil && p != nil && p.AuthorizationActionable() &&
		p.ExecutionID == proposal.ID && p.ExecutionProposalID == proposal.ID &&
		p.SemanticOwnerKind == "EXECUTION" && p.SemanticOwnerID == proposal.ID &&
		p.TaskID == proposal.TaskID && p.ProjectID == proposal.ProjectID
}

// Map only the Core vocabulary to the Card's presentation vocabulary. Task
// plans and execution results remain separate domain facts/decisions.
func applyCoreExecutionCard(result *UIWorkflowStateOutput) {
	p := result.AgentExecution
	if p == nil {
		return
	}
	// Operation terminality is activity metadata; Task.phase remains authoritative.
	if p.Terminal() || p.ReadOnly || p.RecoveryRequired || p.FinalizationPending {
		result.Approval = nil
	}
	if !p.AuthorizationActionable() {
		result.ExecutionProposal = nil
	}
}

func (s *StaticService) GetAgentExecutionForSession(ctx context.Context, sessionID, executionID string) (agentexecution.Projection, error) {
	if s.control == nil {
		return agentexecution.Projection{}, errors.New("durable execution reader unavailable")
	}
	return s.control.GetAgentExecutionForSession(ctx, sessionID, executionID)
}

func enrichUIAgentExecution(ctx context.Context, service BridgeService, sessionID string, result *UIWorkflowStateOutput) error {
	if result.ExecutionID == "" {
		return nil
	}
	reader, ok := service.(interface {
		GetAgentExecutionForSession(context.Context, string, string) (agentexecution.Projection, error)
	})
	if !ok {
		return nil
	}
	p, err := reader.GetAgentExecutionForSession(ctx, sessionID, result.ExecutionID)
	if err != nil {
		return err
	}
	if p.ExecutionID != result.ExecutionID || result.Task != nil && p.TaskID != result.Task.ID {
		return errors.New("Card Execution subject mismatch")
	}
	result.AgentExecution = &p
	return nil
}

// Both adapters read the same session-scoped durable inventory.
func (s *StaticService) ListAgentExecutionsForSession(ctx context.Context, sessionID string) ([]agentexecution.Projection, error) {
	if s.control == nil {
		return nil, errors.New("durable execution reader unavailable")
	}
	return s.control.ListAgentExecutionsForSession(ctx, sessionID)
}

type agentExecutionReader interface {
	GetAgentExecutionForSession(context.Context, string, string) (agentexecution.Projection, error)
}

func readAgentExecution(ctx context.Context, service BridgeService, sessionID, id string) (agentexecution.Projection, error) {
	reader, ok := service.(agentExecutionReader)
	if !ok {
		return agentexecution.Projection{}, errors.New("durable execution reader unavailable")
	}
	return reader.GetAgentExecutionForSession(ctx, sessionID, id)
}
func (s projectOpenProjectionService) GetAgentExecutionForSession(ctx context.Context, sessionID, id string) (agentexecution.Projection, error) {
	return readAgentExecution(ctx, s.BridgeService, sessionID, id)
}
