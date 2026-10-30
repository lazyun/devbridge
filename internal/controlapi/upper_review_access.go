package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
)

// UpperReviewAccessService is separate from generic Direct File authority.
// Its methods only admit a durable Upper LLM review target and never create a
// coding-agent conversation or a Codex interaction.
type UpperReviewAccessService interface {
	UpperReviewAccessForSession(context.Context, string, string) (review.UpperAccess, error)
	Prepare(context.Context, review.UpperTarget) (review.UpperAccess, error)
	Read(context.Context, review.UpperTarget, directfile.Input) (directfile.Result, error)
	List(context.Context, review.UpperTarget, directfile.Input) (directfile.Result, error)
	ListEvidenceForSession(context.Context, string, string) ([]review.FileEvidence, error)
}

func (f *Facade) UpperReviewAccessForSession(ctx context.Context, sessionID, reviewID string) (review.UpperAccess, error) {
	if f.deps.UpperReviewAccess == nil {
		return review.UpperAccess{}, &Error{Code: CodeInvalidState, Message: "Upper Review access is unavailable"}
	}
	v, err := f.deps.UpperReviewAccess.UpperReviewAccessForSession(ctx, sessionID, reviewID)
	if err != nil {
		return v, MapError(err)
	}
	return v, nil
}
func (f *Facade) PrepareUpperReview(ctx context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	if f.deps.UpperReviewAccess == nil {
		return review.UpperAccess{}, &Error{Code: CodeInvalidState, Message: "Upper Review access is unavailable"}
	}
	var v review.UpperAccess
	var err error
	if f.deps.UpperReviewPreparer != nil {
		v, err = f.deps.UpperReviewPreparer.PrepareUpperReview(ctx, target)
	} else {
		v, err = f.deps.UpperReviewAccess.Prepare(ctx, target)
	}
	if err != nil {
		return v, MapError(err)
	}
	return v, nil
}
func (f *Facade) UpperReviewRead(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if in.AISessionID != "" || in.FeatureAreaID != "" || in.WorkflowID != "" {
		return directfile.Result{}, &Error{Code: CodeInvalidArgument, Message: "reviewTarget cannot be combined with Direct File scope"}
	}
	if in.ReviewTarget == nil {
		return directfile.Result{}, &Error{Code: CodeInvalidArgument, Message: "reviewTarget is required"}
	}
	if f.deps.UpperReviewAccess == nil {
		return directfile.Result{}, &Error{Code: CodeInvalidState, Message: "Upper Review access is unavailable"}
	}
	v, err := f.deps.UpperReviewAccess.Read(ctx, *in.ReviewTarget, in)
	if err != nil {
		return v, MapError(err)
	}
	return v, nil
}
func (f *Facade) UpperReviewList(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if in.AISessionID != "" || in.FeatureAreaID != "" || in.WorkflowID != "" {
		return directfile.Result{}, &Error{Code: CodeInvalidArgument, Message: "reviewTarget cannot be combined with Direct File scope"}
	}
	if in.ReviewTarget == nil {
		return directfile.Result{}, &Error{Code: CodeInvalidArgument, Message: "reviewTarget is required"}
	}
	if f.deps.UpperReviewAccess == nil {
		return directfile.Result{}, &Error{Code: CodeInvalidState, Message: "Upper Review access is unavailable"}
	}
	v, err := f.deps.UpperReviewAccess.List(ctx, *in.ReviewTarget, in)
	if err != nil {
		return v, MapError(err)
	}
	return v, nil
}
func (f *Facade) ListUpperReviewEvidenceForSession(ctx context.Context, sessionID, reviewID string) ([]review.FileEvidence, error) {
	if f.deps.UpperReviewAccess == nil {
		return nil, &Error{Code: CodeInvalidState, Message: "Upper Review access is unavailable"}
	}
	v, err := f.deps.UpperReviewAccess.ListEvidenceForSession(ctx, sessionID, reviewID)
	if err != nil {
		return nil, MapError(err)
	}
	return v, nil
}
