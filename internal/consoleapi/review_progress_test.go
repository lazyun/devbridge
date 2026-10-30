package consoleapi

import (
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestWebReviewCurrentAndHistoricalCompletionRemainDistinct(t *testing.T) {
	value := review.Review{ID: "review", RunID: "run"}
	for _, status := range []run.Status{run.StatusRunning, run.StatusSucceeded} {
		runs := []run.Run{{ID: value.RunID, ReviewID: value.ID, Status: status}}
		state, _ := productReviewPresentationWithOwnership(value, runs, true)
		if state != "REVIEWING" || workflowpresentation.ReviewProgressStatus(value, &runs[0], true) != "RUNNING" {
			t.Fatalf("Web and shared active semantics disagree: %s", state)
		}
		if status == run.StatusSucceeded {
			history, _ := productReviewPresentation(value, runs)
			if history != "COMPLETE" {
				t.Fatalf("history overwritten by active state: %s", history)
			}
		}
	}
}
