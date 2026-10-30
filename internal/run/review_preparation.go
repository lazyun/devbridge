package run

import (
	"context"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

// ReviewPreparation contains the immutable target plan and source runtime
// identity. Source workspace roots are evidence only and must never be reused.
type ReviewPreparation struct {
	Plan          runtimepreparation.Plan
	SourceRuntime workset.RuntimeWorkset
}

func (s *Service) ReadReviewPreparation(ctx context.Context, runID string) (ReviewPreparation, error) {
	reader, ok := s.store.(interface {
		ReadReviewPreparation(context.Context, string) (ReviewPreparation, error)
	})
	if !ok {
		return ReviewPreparation{}, ErrNotFound
	}
	return reader.ReadReviewPreparation(ctx, runID)
}
