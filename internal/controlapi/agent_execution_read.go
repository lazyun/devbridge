package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentexecution"
)

// GetAgentExecutionForSession keeps execution reads inside the session's
// immutable project and uses the same reducer as the inventory.
func (f *Facade) GetAgentExecutionForSession(ctx context.Context, sessionID, executionID string) (agentexecution.Projection, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return agentexecution.Projection{}, err
	}
	if f.deps.AgentExecutions == nil {
		return agentexecution.Projection{}, notFound()
	}
	e, err := f.deps.AgentExecutions.Get(ctx, executionID)
	if err != nil {
		return agentexecution.Projection{}, MapError(err)
	}
	if e.ProjectID != projectID {
		return agentexecution.Projection{}, notFound()
	}
	return f.projectAgentExecution(ctx, e)
}
