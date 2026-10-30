package workflowpresentation

import (
	"fmt"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"strings"
	"testing"
	"time"
)

func TestTaskRunRowsBoundedStableAndFrozenIdentity(t *testing.T) {
	at := time.Unix(100, 0)
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Model: "current-default"}
	values := []run.Run{}
	for i := 49; i >= 0; i-- {
		values = append(values, run.Run{ID: fmt.Sprintf("run-%02d", i), AgentExecutionID: "agent", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, Purpose: run.PurposeVerification, ExecutionProposalID: "batch", CreatedAt: at, Status: run.StatusRunning, StartedAt: &at, Result: &run.Result{AgentClaim: evidence.AgentClaim{Message: strings.Repeat("x", 100000)}}})
	}
	view := BuildTask(owner, values, nil, at.Add(time.Second))
	if len(view.Details.Runs) != 20 || view.Details.RunsTotal != 50 || !view.Details.RunsTruncated || view.Details.Runs[0].ID != "run-00" || view.Details.Runs[19].ID != "run-19" {
		t.Fatalf("unbounded/unstable rows: %+v", view.Details)
	}
	d := view.Details.Runs[0]
	if d.ExecutionID != "agent" || d.AgentExecutionID != "agent" || d.TaskExecutionID != "batch" || d.RunPurpose != "VERIFICATION" || d.Model != "" || d.ConfigSource != "UNKNOWN" || d.ElapsedMS == nil || *d.ElapsedMS != 1000 || d.DurationComplete || len(d.Result) > 605 {
		t.Fatalf("row identity/config/duration: %+v", d)
	}
	AttachTaskRunConfig(&d, []executionproposal.Proposal{{ID: "batch", TaskDefinitionRevisionID: "revision", RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "frozen", ReasoningEffort: "high"}}}, nil)
	if d.Model != "frozen" || d.ReasoningEffort != "high" || d.ConfigSource != "EXECUTION_RUNTIME_CONFIG" || d.TaskDefinitionRevisionID != "revision" {
		t.Fatal(d)
	}
}
func TestRunElapsedDistinguishesMissingInvalidActiveAndZero(t *testing.T) {
	at := time.Unix(100, 0)
	before := at.Add(-time.Second)
	for _, r := range []run.Run{{}, {StartedAt: &at, FinishedAt: &before}, {StartedAt: &at, Status: run.StatusSucceeded}} {
		if ms, complete := RunElapsed(r, at); ms != nil || complete {
			t.Fatalf("invalid time fabricated: %v %v", ms, complete)
		}
	}
	ms, complete := RunElapsed(run.Run{StartedAt: &at, FinishedAt: &at, Status: run.StatusSucceeded}, at)
	if ms == nil || *ms != 0 || !complete {
		t.Fatal("legitimate zero duration missing")
	}
}
func TestTaskRunOwnerChainRejectsWrongLinksAndKeepsVerificationOnly(t *testing.T) {
	owner := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area"}
	r := run.Run{ID: "verify", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, Purpose: run.PurposeVerification, ExecutionProposalID: "batch", OperationKind: run.OperationExecution, OperationID: "batch"}
	executions := []executionproposal.Proposal{{ID: "batch", TaskID: owner.ID, ProjectID: owner.ProjectID, VerificationRunID: r.ID}}
	if !TaskRunOwned(owner, r, executions, nil) {
		t.Fatal("verification-only owner rejected")
	}
	for _, alter := range []func(*run.Run){func(v *run.Run) { v.OperationID = "other" }, func(v *run.Run) { v.TaskID = "other" }, func(v *run.Run) { v.ProjectID = "other" }, func(v *run.Run) { v.ID = "other" }, func(v *run.Run) { v.ReviewID = "review" }, func(v *run.Run) { v.Purpose = run.PurposeImplementation }} {
		copy := r
		alter(&copy)
		if TaskRunOwned(owner, copy, executions, nil) {
			t.Fatalf("foreign owner accepted: %+v", copy)
		}
	}
	owner.Phase = task.PhaseReviewing
	owner.CurrentReviewID = "review"
	owner.Status = task.StatusActive
	rr := run.Run{ID: "review-run", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, ReviewID: "review", Purpose: run.PurposeScopeReview, Status: run.StatusInfrastructureFailed}
	reviews := []review.Review{{ID: "review", TaskID: owner.ID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, RunID: rr.ID}}
	if !TaskRunOwned(owner, rr, nil, reviews) {
		t.Fatal("review owner rejected")
	}
	v := BuildTask(owner, []run.Run{rr}, nil, time.Now())
	AttachTaskReviews(&v, owner, reviews, []run.Run{rr})
	if v.Phase != task.PhaseReviewing || v.Status != "ACTIVE" || v.Activity.Status != "INFRASTRUCTURE_FAILED" || v.Activity.Summary != "审查未完成／本次运行已中断" {
		t.Fatalf("transport failure changed Task or spinner: %+v", v)
	}
}
