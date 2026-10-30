package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/directfile"
)

type DirectFileWorkerService interface {
	ClaimFileImplementationWorker(context.Context, directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error)
	GetFileImplementationWorker(context.Context, directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error)
}

func (f *Facade) ClaimFileImplementationWorker(ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
	if f == nil || f.deps.DirectFileWorkers == nil {
		return directfile.WorkerClaimResult{}, MapError(errors.New("Direct File pull worker unavailable"))
	}
	if err := f.fileResultSessionTask(ctx, in.AISessionID, in.TaskID); err != nil {
		return directfile.WorkerClaimResult{}, err
	}
	out, err := f.deps.DirectFileWorkers.ClaimFileImplementationWorker(ctx, in)
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}

func (f *Facade) GetFileImplementationWorker(ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
	if f == nil || f.deps.DirectFileWorkers == nil {
		return directfile.WorkerClaimResult{}, MapError(errors.New("Direct File pull worker unavailable"))
	}
	if err := f.fileResultSessionTask(ctx, in.AISessionID, in.TaskID); err != nil {
		return directfile.WorkerClaimResult{}, err
	}
	out, err := f.deps.DirectFileWorkers.GetFileImplementationWorker(ctx, in)
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}
