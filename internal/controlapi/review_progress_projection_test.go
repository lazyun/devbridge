package controlapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestReviewProgressUsesCurrentOwnerNotCompletedHistory(t *testing.T) {
	for _, test := range []struct {
		name                      string
		status                    run.Status
		active, blocked, recovery bool
		want                      string
	}{
		{"active", run.StatusRunning, true, false, false, "RUNNING"},
		{"collecting", run.StatusCollecting, true, false, false, "RUNNING"},
		{"approval", run.StatusWaitingApproval, true, false, false, "RUNNING"},
		{"success awaiting owner release", run.StatusSucceeded, true, false, false, "RUNNING"},
		{"blocked beats successful Run", run.StatusSucceeded, true, true, false, "BLOCKED"},
		{"failed", run.StatusFailed, true, false, false, "FAILED"},
		{"infrastructure failure with recovery", run.StatusInfrastructureFailed, true, false, true, "FAILED"},
		{"completed without active owner", run.StatusSucceeded, false, false, false, "COMPLETE"},
	} {
		t.Run(test.name, func(t *testing.T) {
			current, development, context, operation := presentationFixture()
			old := &review.Review{ReviewDepth: "DEEP", ID: "historical-completed", RunID: "old-run"}
			selected := &review.Review{ReviewDepth: "DEEP", ID: "current-review", RunID: "current-run"}
			current.CurrentReview, current.LatestReview = old, old
			if test.active {
				current.ActiveReview = selected
			} else {
				current.LatestReview = selected
			}
			if test.blocked {
				selected.StartFailure = &review.StartFailure{Code: "FORBIDDEN"}
			}
			current.Runs = []run.Run{{ID: old.RunID, ReviewID: old.ID, Status: run.StatusSucceeded}, {ID: selected.RunID, ReviewID: selected.ID, Status: test.status}}
			if test.active {
				operation.Status = "BUSY"
				operation.Claim = &agentconversation.Claim{Kind: agentconversation.Review, OperationID: selected.ID, RunID: selected.RunID}
			}
			if test.recovery {
				operation.Status = "RECOVERY_REQUIRED"
			}
			current.Task = &task.Task{ID: "intent", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Title: "Intent", Status: task.StatusActive, Phase: task.PhaseReviewing, CurrentReviewID: selected.ID}
			got := projectFeatureAreaPresentation(current, development, context, operation)
			if got.Progress[0].Status != "CURRENT" || got.Progress[0].SourceID != "" || got.Details.ReviewID != selected.ID || got.CurrentPhase != "REVIEWING" {
				t.Fatalf("current Review projection=%+v", got)
			}
			if !strings.Contains(presentation.RenderText(got), "Name: \"REVIEW\"\n    Status: \""+"CURRENT"+"\"") {
				t.Fatalf("text disagrees: %s", presentation.RenderText(got))
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var decoded presentation.Projection
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Progress[0] != got.Progress[0] {
				t.Fatalf("JSON disagrees: %s", data)
			}
			if current.Runs[0].Status != run.StatusSucceeded || old.ID != "historical-completed" {
				t.Fatal("historical facts changed")
			}
		})
	}
}
