package controlapi

import (
	"context"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Mirror REQUEST_REVIEW's COALESCE(required depth, current Review depth, FAST)
// before that reducer runs, since APPROVE clears required_review_depth.
func (f *Facade) preparedReviewDepthFloor(ctx context.Context, owner task.Task, supplemental bool) (*task.ReviewDepth, error) {
	if owner.RequiredReviewDepth != nil || !supplemental {
		return owner.RequiredReviewDepth, nil
	}
	if owner.CurrentReviewID == "" {
		return task.ReviewDepthPointer(task.ReviewDepthFast), nil
	}
	current, err := f.deps.Reviews.Get(ctx, owner.CurrentReviewID)
	if err != nil {
		return nil, err
	}
	if current.ProjectID != owner.ProjectID || current.TaskID != owner.ID || current.FeatureAreaID != owner.FeatureAreaID || !current.ReviewDepth.Valid() {
		return nil, invalid("Current Review depth is unavailable")
	}
	return task.ReviewDepthPointer(current.ReviewDepth), nil
}

// A historical partially consumed supplemental confirmation must never be
// replayed with a changed depth. Advertise only a separately qualified NEW
// start preparation on the same Task; preserve every original step/receipt.
func (f *Facade) attachReviewStartPreparation(ctx context.Context, intent *c.Intent) {
	intent.ReviewStartPreparation = nil
	if intent.Purpose != c.Review || intent.Action != "REQUEST_SUPPLEMENTAL_REVIEW" || intent.State != "PARTIAL" || intent.CanResume || intent.Commit == nil || intent.Content.Review == nil || len(intent.Steps) != 2 {
		return
	}
	request, start := intent.Steps[0], intent.Steps[1]
	b := intent.Content.Review
	if request.Kind != "REQUEST_REVIEW" || request.State != "COMPLETED" || request.TaskID != b.TaskID || start.Kind != "START_REVIEW" || start.Failure == nil || start.Failure.Code != "REVIEW_DEPTH_TOO_SHALLOW" || start.ReviewID != "" || start.RunID != "" {
		return
	}
	owner, err := f.deps.Tasks.Get(ctx, b.TaskID)
	if err != nil || owner.ProjectID != intent.ProjectID || owner.FeatureAreaID != b.FeatureAreaID || owner.Status != task.StatusActive || owner.Phase != task.PhaseReviewRequired || b.ExpectedTarget == nil || owner.CurrentDefinitionRevisionID != b.ExpectedTarget.TaskDefinitionRevisionID || owner.CurrentReviewID != b.ExpectedTarget.CurrentReviewID || owner.CurrentExecutionID != "" || owner.RequiredReviewDepth == nil || b.ReviewDepth.Satisfies(*owner.RequiredReviewDepth) {
		return
	}
	store, ok := f.deps.WorkflowProgression.(wp.TaskReviewRequestStore)
	if !ok {
		return
	}
	facts, err := store.TaskReviewRequestEligibility(ctx, intent.AISessionID, b.TaskID)
	if err != nil || facts.CurrentRequest == nil || facts.CurrentRequest.RequestID != request.RequestID {
		return
	}
	current, err := f.TaskPresentationForSession(ctx, intent.AISessionID, b.FeatureAreaID, b.TaskID)
	if err != nil || current.TaskView == nil || current.TaskView.ActionsStatus != "AVAILABLE" {
		return
	}
	for _, action := range current.TaskView.Actions {
		if action.Action != "START_TASK_REVIEW" || !action.Available || action.Target == nil || action.Target.TaskID != owner.ID || action.Target.FeatureAreaID != owner.FeatureAreaID {
			continue
		}
		intent.ReviewStartPreparation = &c.ReviewStartPreparation{
			Action: "START_REVIEW", TaskID: owner.ID, FeatureAreaID: owner.FeatureAreaID,
			ExpectedTarget:      wp.TaskReviewRequestTarget{Phase: owner.Phase, TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID, CurrentReviewID: owner.CurrentReviewID, CurrentExecutionID: owner.CurrentExecutionID},
			RequiredReviewDepth: owner.RequiredReviewDepth, AllowedReviewDepths: task.AllowedReviewDepths(owner.RequiredReviewDepth), RequiresNewConfirmation: true, ReasonCode: start.Failure.Code,
			Summary: "The supplemental request is recorded, but its prepared Review depth was below this Task's requirement. Keep the original consumed intent and receipts. Read this same Task and prepare a NEW review_prepare(action=START_REVIEW) with an allowed reviewDepth and the original complete instructions; do not request supplemental review again or replay/change the consumed confirmation. The user must confirm the corrected start; this read starts nothing.",
		}
		return
	}
}
