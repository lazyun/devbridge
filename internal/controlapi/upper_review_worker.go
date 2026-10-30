package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
)

type UpperReviewWorkerService interface {
	ClaimUpperReviewWorker(context.Context, review.WorkerClaimInput) (review.WorkerClaimResult, error)
	GetUpperReviewWorkerClaim(context.Context, review.WorkerClaimInput) (review.WorkerClaimResult, error)
}

func (f *Facade) ClaimUpperReviewWorker(ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
	if f == nil || f.deps.UpperReviewWorkers == nil {
		return review.WorkerClaimResult{}, MapError(errors.New("upper Review pull worker unavailable"))
	}
	if _, err := f.GetReviewForSession(ctx, in.AISessionID, in.ReviewID); err != nil {
		return review.WorkerClaimResult{}, err
	}
	result, err := f.deps.UpperReviewWorkers.ClaimUpperReviewWorker(ctx, in)
	if err != nil {
		return review.WorkerClaimResult{}, MapError(err)
	}
	return result, nil
}

func (f *Facade) GetUpperReviewWorkerClaim(ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
	if f == nil || f.deps.UpperReviewWorkers == nil {
		return review.WorkerClaimResult{}, MapError(errors.New("upper Review pull worker unavailable"))
	}
	if _, err := f.GetReviewForSession(ctx, in.AISessionID, in.ReviewID); err != nil {
		return review.WorkerClaimResult{}, err
	}
	result, err := f.deps.UpperReviewWorkers.GetUpperReviewWorkerClaim(ctx, in)
	if err != nil {
		return review.WorkerClaimResult{}, MapError(err)
	}
	return result, nil
}
