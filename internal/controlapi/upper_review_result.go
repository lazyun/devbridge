package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
)

// UpperReviewResultService is deliberately session-scoped at every public
// read. The storage implementation independently repeats the ownership check.
type UpperReviewResultService interface {
	SubmitReviewResult(context.Context, review.SubmitInput) (review.ReportReceipt, error)
	GetReviewReportForSession(context.Context, string, string) (review.StoredReport, error)
	GetReviewResultReceiptForSession(context.Context, string, string, string) (review.ReportReceipt, error)
}

func (f *Facade) SubmitReviewResult(ctx context.Context, in review.SubmitInput) (review.ReportReceipt, error) {
	if f == nil || f.deps.UpperReviewResults == nil {
		return review.ReportReceipt{}, MapError(errors.New("upper review result service is unavailable"))
	}
	if _, err := f.GetReviewForSession(ctx, in.AISessionID, in.ReviewID); err != nil {
		return review.ReportReceipt{}, err
	}
	value, err := f.deps.UpperReviewResults.SubmitReviewResult(ctx, in)
	if err != nil {
		return review.ReportReceipt{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetReviewReportForSession(ctx context.Context, aiSessionID, reviewID string) (review.StoredReport, error) {
	if f == nil || f.deps.UpperReviewResults == nil {
		return review.StoredReport{}, MapError(errors.New("upper review result service is unavailable"))
	}
	if _, err := f.GetReviewForSession(ctx, aiSessionID, reviewID); err != nil {
		return review.StoredReport{}, err
	}
	value, err := f.deps.UpperReviewResults.GetReviewReportForSession(ctx, aiSessionID, reviewID)
	if err != nil {
		return review.StoredReport{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetReviewResultReceiptForSession(ctx context.Context, aiSessionID, reviewID, requestID string) (review.ReportReceipt, error) {
	if f == nil || f.deps.UpperReviewResults == nil {
		return review.ReportReceipt{}, MapError(errors.New("upper review result service is unavailable"))
	}
	if _, err := f.GetReviewForSession(ctx, aiSessionID, reviewID); err != nil {
		return review.ReportReceipt{}, err
	}
	value, err := f.deps.UpperReviewResults.GetReviewResultReceiptForSession(ctx, aiSessionID, reviewID, requestID)
	if err != nil {
		return review.ReportReceipt{}, MapError(err)
	}
	return value, nil
}
