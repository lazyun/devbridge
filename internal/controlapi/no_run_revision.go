package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// A retained canceled predecessor is not the runtime source for a newly
// approved definition. This exact read never authorizes a start by itself.
func (f *Facade) noRunRevisionStartEligible(ctx context.Context, owner task.Task) (bool, error) {
	reader, ok := f.deps.WorkflowProgression.(interface {
		NoRunRevisionStartEligible(context.Context, string, string, string, string) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return reader.NoRunRevisionStartEligible(ctx, owner.ProjectID, owner.ID, owner.CurrentExecutionID, owner.CurrentDefinitionRevisionID)
}

func (f *Facade) StopAndReviseNoRunTaskForSession(ctx context.Context, sessionID string, in wp.StopAndReviseNoRunTask) (task.Task, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return task.Task{}, err
	}
	if in.ProjectID != "" && in.ProjectID != projectID {
		return task.Task{}, invalid("Task revision Project mismatch")
	}
	in.ProjectID, in.ActorAuthority = projectID, sessionID
	if f.operatorProject != "" {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || actor.Check(projectID, in.TaskID, wp.NoRunStopAndReviseAction) != nil {
			return task.Task{}, MapError(localoperator.ErrAccessDenied)
		}
		in.ActorAuthority = "operator:" + actor.PrincipalID()
	}
	store, ok := f.deps.WorkflowProgression.(interface {
		StopAndReviseNoRunTask(context.Context, wp.StopAndReviseNoRunTask) (task.Task, error)
	})
	if !ok {
		return task.Task{}, invalid("No-Run Task revision unavailable")
	}
	value, err := store.StopAndReviseNoRunTask(ctx, in)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) StopAndReviseNoRunTaskForOperator(ctx context.Context, projectID string, in wp.StopAndReviseNoRunTask) (task.Task, error) {
	ctx, err := localoperator.BindTask(ctx, projectID, in.TaskID, wp.NoRunStopAndReviseAction)
	if err != nil {
		return task.Task{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return task.Task{}, err
	}
	if in.ActorAuthority != "" {
		return task.Task{}, invalid("Operator revision cannot supply an actor")
	}
	return reader.StopAndReviseNoRunTaskForSession(ctx, "", in)
}
