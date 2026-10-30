package controlapi

import (
	"context"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// StartTaskExecutionInstructions commits authored instructions and admission
// before Core creates the matching preparation Run.
func (f *Facade) StartTaskExecutionInstructions(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation, requestID string, instructions task.ExecutionInstructions) (executionproposal.StartResult, error) {
	if !validTaskExecutionExpectation(expected) || strings.TrimSpace(requestID) == "" {
		return executionproposal.StartResult{}, invalid("Exact instruction submission target and requestId are required")
	}
	if _, err := f.sessionProject(ctx, sessionID); err != nil {
		return executionproposal.StartResult{}, err
	}
	store, ok := f.deps.WorkflowProgression.(interface {
		ReserveTaskExecutionInstructions(context.Context, string, string, wp.TaskExecutionExpectation, string, task.ExecutionInstructions) (executionproposal.Proposal, run.Run, error)
	})
	if !ok {
		return executionproposal.StartResult{}, errors.New("execution instruction materialization unavailable")
	}
	value, reserved, err := store.ReserveTaskExecutionInstructions(ctx, sessionID, taskID, expected, requestID, instructions)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	starter, ok := f.deps.ExecutionProposals.(interface {
		StartAuthorizedTurn(context.Context, string, executionproposal.Proposal, run.Run) (executionproposal.StartResult, error)
	})
	if !ok {
		return executionproposal.StartResult{Proposal: value, Run: reserved}, errors.New("reserved Execution handoff unavailable")
	}
	return starter.StartAuthorizedTurn(ctx, sessionID, value, reserved)
}

// ReadTaskExecutionInstructionsReservation compares the entire original
// submission before returning its existing reservation; it never dispatches.
func (f *Facade) ReadTaskExecutionInstructionsReservation(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation, requestID string, instructions *task.ExecutionInstructions) (executionproposal.StartResult, bool, error) {
	if !validTaskExecutionExpectation(expected) || strings.TrimSpace(requestID) == "" {
		return executionproposal.StartResult{}, false, invalid("Exact instruction submission target and requestId are required")
	}
	if _, err := f.sessionProject(ctx, sessionID); err != nil {
		return executionproposal.StartResult{}, false, err
	}
	reader, ok := f.deps.WorkflowProgression.(interface {
		ReadTaskExecutionInstructionsReservation(context.Context, string, string, wp.TaskExecutionExpectation, string, *task.ExecutionInstructions) (executionproposal.Proposal, run.Run, bool, error)
	})
	if !ok {
		return executionproposal.StartResult{}, false, errors.New("execution instruction reservation read unavailable")
	}
	value, reserved, found, err := reader.ReadTaskExecutionInstructionsReservation(ctx, sessionID, taskID, expected, requestID, instructions)
	if err != nil {
		return executionproposal.StartResult{}, false, MapError(err)
	}
	result := executionproposal.StartResult{Proposal: value, Run: reserved}
	if found && reserved.ID != "" {
		if reader, ok := f.deps.ExecutionProposals.(interface {
			ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error)
		}); ok {
			result.Handoff, err = reader.ReadHandoff(ctx, reserved.ProjectID, reserved.ID)
			if err != nil {
				return result, true, err
			}
		}
	}
	return result, found, nil
}
