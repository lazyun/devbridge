package mcpserver

import (
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowstate"
)

func TestCompatibilityCurrentReviewWaitsForOwnershipRelease(t *testing.T) {
	value := &review.Review{ReviewDepth: "DEEP", ID: "review", RunID: "run"}
	completedRun := &run.Run{ID: "run", ReviewID: "review", Status: run.StatusSucceeded}
	for _, active := range []bool{true, false} {
		current := controlapi.CurrentWorkflow{CurrentReview: value, Guidance: &wp.Guidance{TaskID: "intent", TaskPhase: task.PhaseReviewing, CurrentState: "REVIEWING"}}
		if active {
			current.ActiveReview = value
		}
		projection := workflowProjection{productReview: value, review: completedRun, featureArea: &featurearea.FeatureArea{ID: "area", Status: featurearea.StatusActive}}
		wantState, wantStatus := "REVIEWING", "SUCCEEDED"
		if active {
			wantState, wantStatus = "REVIEWING", "RUNNING"
		}
		applyProductWorkflow(&projection, current, workflowstate.Input{}, projectcontext.ProjectContext{Status: projectcontext.StatusNoGuidance})
		if projection.output.State != wantState || projection.output.Current.Review.State != "SUCCEEDED" || currentReviewCompatibilityStatus(projection, current) != wantStatus {
			t.Fatalf("compatibility current Review disagrees: %+v", projection.output)
		}
		if historical := reviewSummaryFor(*value, completedRun); historical.State != "SUCCEEDED" {
			t.Fatalf("historical Run truth changed: %+v", historical)
		}
	}
}
