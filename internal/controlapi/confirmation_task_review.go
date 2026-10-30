package controlapi

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

// A Task review choice can only remove the prepared Review step, never add work
// to CREATE_TASK or alter the frozen action/content identity. Omission keeps the
// original behavior so historical requests and their hashes remain replayable.
func confirmationTaskReviewChoice(intent c.Intent, selected *bool) (bool, error) {
	if intent.Purpose != c.Task {
		if selected != nil {
			return false, confirmationSelectionFailure("TASK_REVIEW_SELECTION_NOT_APPLICABLE", "The review choice is only accepted when creating a Task.")
		}
		return false, nil
	}
	body := intent.Content.Task
	if body == nil || (intent.Action != "CREATE_TASK" && intent.Action != "CREATE_TASK_AND_REVIEW") || body.StartReview != (intent.Action == "CREATE_TASK_AND_REVIEW") {
		return false, invalid("Task startReview must match the explicit confirmation action")
	}
	if selected == nil {
		return body.StartReview, nil
	}
	if *selected && !body.StartReview {
		return false, c.NewFailure("TASK_REVIEW_NOT_PREPARED", "CORE", "CONFIRMATION_PREFLIGHT", "Prepare complete Review instructions, depth and scope before enabling review.", c.PrepareNewVersion, nil)
	}
	return *selected, nil
}

func confirmationTaskReviewDeferred(intent c.Intent, selected *bool) bool {
	return intent.Purpose == c.Task && intent.Action == "CREATE_TASK_AND_REVIEW" && selected != nil && !*selected
}

// This detached view is only for current options/prerequisite validation. It
// qualifies the task-only subset without requiring a Review executor, plan or
// runtime that will not be used. The stored intent, commit action, contentHash,
// variant IDs and canonical CREATE_TASK command are never replaced by this view.
func confirmationTaskOnlyOptionsIntent(intent c.Intent) c.Intent {
	content := intent.Content
	body := *content.Task
	body.StartReview, body.ReviewDepth, body.ReviewPlan = false, "", run.ReviewPlan{}
	content.Action, content.Task, content.RecommendedRuntimeConfig = "CREATE_TASK", &body, nil
	content.Variants = make([]c.Variant, len(intent.Content.Variants))
	for i, variant := range intent.Content.Variants {
		content.Variants[i] = c.Variant{ID: variant.ID, Label: variant.Label, Summary: variant.Summary, DisplaySummary: variant.DisplaySummary}
	}
	intent.Action, intent.Content = content.Action, content
	return intent
}

// The explicit mode is bound to the original request and prepared variant. A
// legacy false startReview still means defer, never silently waive the gate.
func validateTaskReviewMode(intent c.Intent, input c.CommitInput, variant c.Variant) error {
	mode := input.Selection.ReviewMode
	if mode == "" {
		return nil
	}
	if intent.Purpose != c.Task {
		return confirmationSelectionFailure("REVIEW_MODE_NOT_APPLICABLE", "Review mode is only selected when creating a Task.")
	}
	switch mode {
	case "NONE":
		if input.Selection.StartReview == nil || *input.Selection.StartReview {
			return invalid("NONE must explicitly disable Review startup")
		}
	case string(review.ExecutorUpperLLM), string(review.ExecutorCodingAgent):
		if input.Selection.StartReview != nil && !*input.Selection.StartReview || variant.ExecutorKind != mode {
			return invalid("Review mode must match the exact prepared Review variant")
		}
	default:
		return invalid("unknown Review mode")
	}
	return nil
}
