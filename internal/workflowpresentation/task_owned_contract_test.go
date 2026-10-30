package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
	"time"
)

func TestTaskOperationHistoryKeepsExactOwnerAndDurableCurrentPointers(t *testing.T) {
	now := time.Unix(100, 0)
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Title: "Semantic title", Status: task.StatusActive, Phase: task.PhaseReviewing, CurrentReviewID: "review-2", CurrentExecutionID: "execution-1"}
	runs := []run.Run{
		{ID: "review-run", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ReviewID: "review-2", Status: run.StatusRunning},
		{ID: "implementation", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutionProposalID: "execution-1", Status: run.StatusSucceeded},
		{ID: "verification", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutionProposalID: "execution-1", Status: run.StatusFailed},
	}
	executions := []executionproposal.Proposal{
		{ID: "foreign-project", TaskID: "task", ProjectID: "foreign"},
		{ID: "foreign-task", TaskID: "foreign", ProjectID: "project"},
		{ID: "execution-1", TaskID: "task", ProjectID: "project", ImplementationRunID: "implementation", VerificationRunID: "verification"},
	}
	v := BuildTaskWithHistory(owner, runs, nil, executions, nil, now)
	reviews := []review.Review{
		{ID: "review-2", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", Ordinal: 2, RunID: "review-run", Instruction: "Instruction is not the semantic title"},
		{ID: "review-1", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", Ordinal: 1, TerminalStatus: "SUCCEEDED"},
		{ID: "later-foreign", TaskID: "foreign", ProjectID: "project", FeatureAreaID: "area", Ordinal: 99},
		{ID: "wrong-area", TaskID: "task", ProjectID: "project", FeatureAreaID: "foreign"},
	}
	AttachTaskReviews(&v, owner, reviews, runs)
	if v.Title != owner.Title || v.Phase != task.PhaseReviewing || v.Status != "ACTIVE" || v.PhaseDescription != "审查中" {
		t.Fatalf("history rewrote authority: %+v", v)
	}
	if len(v.ReviewHistory) != 2 || v.ReviewHistory[0].ID != "review-1" || v.CurrentReview == nil || v.CurrentReview.ID != "review-2" || v.CurrentReview.Run == nil || v.CurrentReview.Run.ID != "review-run" {
		t.Fatalf("review ownership: %+v", v)
	}
	if len(v.ExecutionHistory) != 1 || !v.ExecutionHistory[0].Current || v.ExecutionHistory[0].ImplementationRun.ID != "implementation" || v.ExecutionHistory[0].VerificationRun.ID != "verification" {
		t.Fatalf("execution ownership: %+v", v.ExecutionHistory)
	}
	owner.CurrentReviewID = "missing"
	AttachTaskReviews(&v, owner, reviews, runs)
	if v.CurrentReview != nil {
		t.Fatal("history substituted for missing durable current pointer")
	}
}

func TestTaskTerminalLabelsKeepDurablePhase(t *testing.T) {
	for _, tc := range []struct {
		status task.Status
		label  string
	}{{task.StatusSucceeded, "Done"}, {task.StatusCanceled, "Canceled"}, {task.StatusFailed, "Failed"}} {
		v := BuildTask(task.Task{Type: task.TypeDevelopment, Status: tc.status, Phase: task.PhaseVerifying}, nil, nil, time.Time{})
		if v.Phase != task.PhaseVerifying || v.PhaseLabel != tc.label || !v.Terminal || len(v.Actions) != 0 {
			t.Fatalf("terminal display: %+v", v)
		}
	}
}
