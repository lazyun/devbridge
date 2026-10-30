package execution

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

func (c *Coordinator) recoveryReviewPreparation(ctx context.Context, runID string) (run.ReviewPreparation, bool, error) {
	reader, ok := c.Runs.(interface {
		ReadReviewPreparation(context.Context, string) (run.ReviewPreparation, error)
	})
	if !ok {
		return run.ReviewPreparation{}, false, nil
	}
	p, err := reader.ReadReviewPreparation(ctx, runID)
	if errors.Is(err, run.ErrNotFound) {
		return run.ReviewPreparation{}, false, nil
	}
	return p, err == nil, err
}

func reviewPreparationInput(value review.Review, reserved run.Run, p run.ReviewPreparation) MultiRuntimePrepareInput {
	r := p.SourceRuntime
	return MultiRuntimePrepareInput{
		Owner:       MultiRuntimeOwner{ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, ReviewID: value.ID, RunID: reserved.ID},
		WorkspaceID: value.WorkspaceID, PrimaryMemberID: p.Plan.PrimaryMemberID, Targets: p.Plan.RepositoryTargets,
		Runtime: MultiRuntimeIdentity{BackendConfigID: p.Plan.BackendIdentity, BackendConfigFingerprint: r.BackendConfigFingerprint, Model: p.Plan.Model, ReasoningEffort: p.Plan.ReasoningEffort, ServiceTier: r.ServiceTier, RuntimeAdapterIdentity: p.Plan.RuntimeAdapterIdentity, RuntimeProtocolIdentity: r.RuntimeProtocolIdentity, CreatedAt: reserved.CreatedAt}, ReadOnly: true,
	}
}

// Upper Review uses the same frozen-plan workspace transaction without starting
// a backend. Its grant becomes usable only after the complete binding exists.
func (c *Coordinator) startPreparedPurposeUpperReview(ctx context.Context, value review.Review, reserved run.Run, p run.ReviewPreparation, resolveTarget func() (review.UpperTarget, error)) (run.Run, error) {
	if reserved.WorkspaceID == "" {
		var err error
		reserved, err = c.Runs.AttachWorkspace(ctx, reserved.ID, value.WorkspaceID)
		if err != nil {
			return run.Run{}, err
		}
	}
	input := reviewPreparationInput(value, reserved, p)
	input.UpperReview = true
	if _, err := c.PrepareMultiRuntime(ctx, input); err != nil {
		return run.Run{}, err
	}
	var err error
	if reserved.Status != run.StatusRunning {
		reserved, err = c.Runs.Transition(ctx, reserved.ID, run.StatusRunning)
		if err != nil {
			return run.Run{}, err
		}
	}
	target, err := resolveTarget()
	if err != nil {
		return run.Run{}, err
	}
	if _, err = c.UpperReviews.Prepare(ctx, target); err != nil {
		return run.Run{}, err
	}
	return c.Runs.Get(ctx, reserved.ID)
}
