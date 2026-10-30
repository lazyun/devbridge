package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/localoperator"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// PrepareSuccessorExecutionForSession is the sole client-neutral successor
// command. Every adapter forwards this envelope rather than deriving its own
// eligibility or writing its own receipt.
func (f *Facade) PrepareSuccessorExecutionForSession(ctx context.Context, sessionID string, in wp.PrepareSuccessorExecution) (wp.SuccessorCommandReceipt, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return wp.SuccessorCommandReceipt{}, err
	}
	if in.ProjectID != "" && in.ProjectID != projectID {
		return wp.SuccessorCommandReceipt{}, MapError(wp.Reject("PROJECT_MISMATCH", wp.NoRunSuccessorGuidance(projectID, in.TaskID, in.PredecessorExecutionID)))
	}
	in.ProjectID = projectID
	if f.operatorProject != "" {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.Check(projectID, in.TaskID, "PREPARE_SUCCESSOR_EXECUTION") != nil {
			return wp.SuccessorCommandReceipt{}, MapError(localoperator.ErrAccessDenied)
		}
		in.ActorAuthority = "operator:" + actor.PrincipalID()
	} else {
		in.ActorAuthority = sessionID
	}
	command, ok := f.deps.WorkflowProgression.(interface {
		PrepareSuccessorExecution(context.Context, wp.PrepareSuccessorExecution) (wp.SuccessorCommandReceipt, error)
	})
	if !ok {
		return wp.SuccessorCommandReceipt{}, invalid("Successor preparation unavailable")
	}
	receipt, err := command.PrepareSuccessorExecution(ctx, in)
	if err != nil {
		return receipt, MapError(err)
	}
	return receipt, nil
}

func (f *Facade) PrepareSuccessorExecutionForOperator(ctx context.Context, projectID string, in wp.PrepareSuccessorExecution) (wp.SuccessorCommandReceipt, error) {
	ctx, err := localoperator.BindTask(ctx, projectID, in.TaskID, "PREPARE_SUCCESSOR_EXECUTION")
	if err != nil {
		return wp.SuccessorCommandReceipt{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return wp.SuccessorCommandReceipt{}, err
	}
	if in.ProjectID != "" && in.ProjectID != projectID || in.ActorAuthority != "" {
		return wp.SuccessorCommandReceipt{}, invalid("Operator successor command cannot supply a different Project or actor")
	}
	return reader.PrepareSuccessorExecutionForSession(ctx, "", in)
}
