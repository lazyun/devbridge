package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/directfile"
)

func (f *Facade) DirectFileEnable(ctx context.Context, scope directfile.Scope) (directfile.State, error) {
	if f.deps.DirectFiles == nil {
		return directfile.State{}, MapError(directfile.Failure(directfile.WorktreeUnavailable, "Direct File authority is unavailable"))
	}
	value, err := f.deps.DirectFiles.Enable(ctx, scope)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) DirectFileDisable(ctx context.Context, scope directfile.Scope) (directfile.State, error) {
	if f.deps.DirectFiles == nil {
		return directfile.State{}, MapError(directfile.Failure(directfile.NotEnabled, "Direct File is disabled"))
	}
	value, err := f.deps.DirectFiles.Disable(ctx, scope)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) DirectFileStatus(ctx context.Context, sessionID, areaID string) (directfile.State, error) {
	if f.deps.DirectFiles == nil {
		return directfile.State{}, nil
	}
	value, err := f.deps.DirectFiles.Status(ctx, sessionID, areaID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) fileOperation(ctx context.Context, in directfile.Input, operation string) (directfile.Result, error) {
	if in.ReviewTarget != nil {
		return directfile.Result{}, MapError(directfile.Failure(directfile.OperationFailed, "review-scoped file access must use UpperReviewRead"))
	}
	if f.deps.DirectFiles == nil {
		return directfile.Result{}, MapError(directfile.Failure(directfile.NotEnabled, "Direct File is disabled"))
	}
	var value directfile.Result
	var err error
	switch operation {
	case "list":
		value, err = f.deps.DirectFiles.List(ctx, in)
	case "read":
		value, err = f.deps.DirectFiles.Read(ctx, in)
	case "create":
		value, err = f.deps.DirectFiles.Create(ctx, in)
	case "replace":
		value, err = f.deps.DirectFiles.Replace(ctx, in)
	case "delete":
		value, err = f.deps.DirectFiles.Delete(ctx, in)
	case "move":
		value, err = f.deps.DirectFiles.Move(ctx, in)
	}
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) FileList(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if in.ReviewTarget != nil {
		return f.UpperReviewList(ctx, in)
	}
	return f.fileOperation(ctx, in, "list")
}
func (f *Facade) FileRead(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if in.ReviewTarget != nil {
		return f.UpperReviewRead(ctx, in)
	}
	return f.fileOperation(ctx, in, "read")
}
func (f *Facade) FileCreate(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	return f.fileOperation(ctx, in, "create")
}
func (f *Facade) FileReplace(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	return f.fileOperation(ctx, in, "replace")
}
func (f *Facade) FileDelete(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	return f.fileOperation(ctx, in, "delete")
}
func (f *Facade) FileMove(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	return f.fileOperation(ctx, in, "move")
}

// DirectFileTaskStatus reads only this exact Task's existing approval authority.
func (f *Facade) DirectFileTaskStatus(ctx context.Context, sessionID, taskID string) (directfile.State, error) {
	if f.deps.DirectFiles == nil {
		return directfile.State{}, MapError(directfile.Failure(directfile.WorktreeUnavailable, "Direct File authority is unavailable"))
	}
	value, err := f.deps.DirectFiles.TaskStatus(ctx, sessionID, taskID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

// DirectFileReconcileTaskAuthorization is an explicit maintenance write. Reads
// and actual file calls cannot create or reopen this approval-derived authority.
func (f *Facade) DirectFileReconcileTaskAuthorization(ctx context.Context, in directfile.Registration) (directfile.State, error) {
	if f.deps.DirectFiles == nil {
		return directfile.State{}, MapError(directfile.Failure(directfile.WorktreeUnavailable, "Direct File authority is unavailable"))
	}
	value, err := f.deps.DirectFiles.ReconcileTaskAuthorization(ctx, in)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
