package mcpserver

import (
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
	"github.com/lazyun/devbridge/internal/workflowstate"
)

// Keep compatibility workflow JSON aligned with the main presentation while
// leaving exact historical Review/Run detail serializers untouched.
func currentReviewProgress(projection workflowProjection, current controlapi.CurrentWorkflow) (string, workflowstate.ReviewState) {
	status := "PENDING"
	if value := projection.productReview; value != nil {
		active := current.ActiveReview != nil && current.ActiveReview.ID == value.ID && current.ActiveReview.RunID == value.RunID
		status = workflowpresentation.ReviewProgressStatus(*value, projection.review, active)
	} else if value := projection.review; value != nil {
		// Historical Task scope reviews have no product-level Review identity.
		switch value.Status {
		case run.StatusSucceeded:
			status = "COMPLETE"
		case run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
			status = "FAILED"
		default:
			status = "RUNNING"
		}
	}
	switch status {
	case "PENDING":
		return status, workflowstate.ReviewNone
	case "COMPLETE":
		return status, workflowstate.ReviewSucceeded
	case "RUNNING":
		return status, workflowstate.ReviewRunning
	default:
		return status, workflowstate.ReviewFailed
	}
}

func currentReviewCompatibilityStatus(projection workflowProjection, current controlapi.CurrentWorkflow) string {
	status, _ := currentReviewProgress(projection, current)
	switch status {
	case "COMPLETE":
		return "SUCCEEDED"
	case "PENDING":
		return "NOT_STARTED"
	case "UNKNOWN":
		return "STARTING"
	default:
		return status
	}
}
