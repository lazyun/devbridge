package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// StartTaskExecutionExpected commits the exact Task start decision before
// preparing a modern Implementation Run and handing it to the runtime.
func (f *Facade) StartTaskExecutionExpected(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation) (executionproposal.StartResult, error) {
	action := "START_EXECUTION"
	if expected.Phase == task.PhaseVerificationReady {
		action = "START_VERIFICATION"
	}
	return f.startTaskExecutionExpectedCommand(ctx, sessionID, taskID, action, "", expected)
}

// StartTaskExecutionExpectedCommand preserves a persistent orchestration
// step's stable request identity through reservation and handoff.
func (f *Facade) StartTaskExecutionExpectedCommand(ctx context.Context, sessionID, taskID, action, requestID string, expected wp.TaskExecutionExpectation) (executionproposal.StartResult, error) {
	if requestID == "" {
		return executionproposal.StartResult{}, invalid("Execution start request is required")
	}
	return f.startTaskExecutionExpectedCommand(ctx, sessionID, taskID, action, requestID, expected)
}

func (f *Facade) startTaskExecutionExpectedCommand(ctx context.Context, sessionID, taskID, action, requestID string, expected wp.TaskExecutionExpectation) (executionproposal.StartResult, error) {
	if !validTaskExecutionExpectation(expected) {
		return executionproposal.StartResult{}, invalid("Exact Task execution target is required")
	}
	if _, err := f.sessionProject(ctx, sessionID); err != nil {
		return executionproposal.StartResult{}, err
	}
	var value executionproposal.Proposal
	var reserved run.Run
	var err error
	if requestID != "" {
		store, ok := f.deps.WorkflowProgression.(interface {
			ReserveTaskExecutionExpectedCommand(context.Context, string, string, string, string, *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error)
		})
		if !ok {
			return executionproposal.StartResult{}, errors.New("Persistent Task progression unavailable")
		}
		value, reserved, err = store.ReserveTaskExecutionExpectedCommand(ctx, sessionID, taskID, action, requestID, &expected)
	} else {
		store, ok := f.deps.WorkflowProgression.(interface {
			ReserveTaskExecutionExpected(context.Context, string, string, *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error)
		})
		if !ok {
			return executionproposal.StartResult{}, errors.New("Exact Task progression unavailable")
		}
		value, reserved, err = store.ReserveTaskExecutionExpected(ctx, sessionID, taskID, &expected)
	}
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	value, reserved, err = f.prepareAdmittedTaskExecution(ctx, sessionID, value, reserved)
	if err != nil {
		return executionproposal.StartResult{Proposal: value, Run: reserved}, MapError(err)
	}
	starter, ok := f.deps.ExecutionProposals.(interface {
		StartAuthorizedTurn(context.Context, string, executionproposal.Proposal, run.Run) (executionproposal.StartResult, error)
	})
	if !ok {
		return executionproposal.StartResult{Proposal: value, Run: reserved}, errors.New("reserved Execution handoff unavailable")
	}
	return starter.StartAuthorizedTurn(ctx, sessionID, value, reserved)
}

func (f *Facade) prepareAdmittedTaskExecution(ctx context.Context, sessionID string, value executionproposal.Proposal, reserved run.Run) (executionproposal.Proposal, run.Run, error) {
	if value.Status != executionproposal.StatusAdmitted {
		return value, reserved, nil
	}
	if reserved.ID != "" {
		return value, reserved, executionproposal.ErrConflict
	}
	preparer, ok := f.deps.ExecutionProposals.(interface {
		PrepareAdmittedExecution(context.Context, string, executionproposal.Proposal) (executionproposal.Proposal, run.Run, error)
	})
	if !ok {
		return value, reserved, errors.New("Core Execution preparation unavailable")
	}
	return preparer.PrepareAdmittedExecution(ctx, sessionID, value)
}

// ReadTaskExecutionReservation returns only an already-reserved result. Unlike
// StartTaskExecutionExpected it never resumes or dispatches a backend operation.
func (f *Facade) ReadTaskExecutionReservation(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation) (executionproposal.StartResult, bool, error) {
	if !validTaskExecutionExpectation(expected) {
		return executionproposal.StartResult{}, false, invalid("Exact Task execution target is required")
	}
	if _, err := f.sessionProject(ctx, sessionID); err != nil {
		return executionproposal.StartResult{}, false, err
	}
	reader, ok := f.deps.WorkflowProgression.(interface {
		ReadTaskExecutionReservation(context.Context, string, string, wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, bool, error)
	})
	if !ok {
		return executionproposal.StartResult{}, false, errors.New("Task reservation read unavailable")
	}
	value, reserved, found, err := reader.ReadTaskExecutionReservation(ctx, sessionID, taskID, expected)
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

func validTaskExecutionExpectation(expected wp.TaskExecutionExpectation) bool {
	return expected.FeatureAreaID != "" && expected.TaskDefinitionRevisionID != "" &&
		(expected.Phase == task.PhaseImplementationReady || expected.Phase == task.PhaseVerificationReady && expected.ExecutionID != "")
}

// TaskExecutionTargetForSession exposes the exact read-side target used by the
// Task presentation. This observation grants no authority; start revalidates it.
func (f *Facade) TaskExecutionTargetForSession(ctx context.Context, sessionID, taskID string) (wp.TaskExecutionExpectation, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return wp.TaskExecutionExpectation{}, err
	}
	current, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || current.ProjectID != projectID {
		return wp.TaskExecutionExpectation{}, notFound()
	}
	presented, err := f.TaskPresentationForSession(ctx, sessionID, current.FeatureAreaID, taskID)
	if err != nil {
		return wp.TaskExecutionExpectation{}, err
	}
	if presented.TaskView == nil || presented.TaskView.ExecutionStartTarget == nil {
		return wp.TaskExecutionExpectation{}, invalid("Task has no current execution start target")
	}
	return *presented.TaskView.ExecutionStartTarget, nil
}

func taskExecutionStartTarget(t task.Task, executions []executionproposal.Proposal) *wp.TaskExecutionExpectation {
	if t.Status != task.StatusActive || t.LegacyReadOnly || t.CurrentDefinitionRevisionID == "" || t.WorkSpec.ImplementationMode != task.ImplementationCodingAgent ||
		(t.Phase != task.PhaseImplementationReady && t.Phase != task.PhaseVerificationReady) {
		return nil
	}
	expected := wp.TaskExecutionExpectation{FeatureAreaID: t.FeatureAreaID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, ExecutionID: t.CurrentExecutionID, Phase: t.Phase}
	for _, candidate := range executions {
		if candidate.ID == t.CurrentExecutionID && candidate.TaskID == t.ID && candidate.ProjectID == t.ProjectID && candidate.TaskDefinitionRevisionID == t.CurrentDefinitionRevisionID {
			expected.PrimaryMemberID, expected.ApprovedWorksetHash = candidate.PrimaryMemberID, candidate.ApprovedWorksetHash
			return &expected
		}
	}
	if t.Phase == task.PhaseVerificationReady {
		return nil // Verification requires the exact already-approved Execution.
	}
	// An approve-only revision can retain a canceled predecessor pointer. The
	// new definition supplies the workset; the original pointer remains exact.
	if len(t.RepositoryTargets) > 0 {
		expected.PrimaryMemberID, expected.ApprovedWorksetHash = t.PrimaryMemberID, confirmationHash(t.RepositoryTargets)
	}
	return &expected
}
