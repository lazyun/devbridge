package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
)

// ReplayAdmittedReview is a Project-scoped read of an existing command result.
// No current-state eligibility or dispatch runs on this receipt-only path.
func (f *Facade) ReplayAdmittedReview(ctx context.Context, sessionID, taskID, areaID, requestID, payloadHash string) (review.StartAdmission, bool, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return review.StartAdmission{}, false, err
	}
	owner, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || owner.ProjectID != projectID || owner.FeatureAreaID != areaID {
		return review.StartAdmission{}, false, notFound()
	}
	reader, ok := f.deps.Reviews.(interface {
		ReplayAdmittedStart(context.Context, string, string, string, string, string) (review.StartAdmission, bool, error)
	})
	if !ok {
		return review.StartAdmission{}, false, errors.New("Review admission replay unavailable")
	}
	result, found, err := reader.ReplayAdmittedStart(ctx, projectID, taskID, areaID, requestID, payloadHash)
	if err != nil {
		return result, found, MapError(err)
	}
	return result, found, nil
}
