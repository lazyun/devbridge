package mcpserver

import (
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/workflowstate"
)

// Translate the Core state only for the legacy workflow engine vocabulary.
func workflowExecutionState(p *agentexecution.Projection) workflowstate.ExecutionState {
	switch currentExecutionStatus(p) {
	case "PENDING":
		if p.AuthorizationActionable() {
			return workflowstate.ExecutionPendingAuthorization
		}
		return workflowstate.ExecutionRunning
	case "COMPLETED":
		return workflowstate.ExecutionSucceeded
	case "FAILED":
		return workflowstate.ExecutionFailed
	case "REJECTED":
		return workflowstate.ExecutionRejected
	case "CANCELED":
		return workflowstate.ExecutionCanceled
	case "UNAVAILABLE":
		return workflowstate.ExecutionNone
	default:
		return workflowstate.ExecutionRunning
	}
}
