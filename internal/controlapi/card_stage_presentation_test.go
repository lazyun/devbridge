package controlapi

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/review"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func cardStageGraph() (presentation.Projection, CurrentWorkflow) {
	t := &task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, ReviewApprovalID: "approval", ProblemStatement: "Exact selected Task", Title: "Semantic intent", Status: task.StatusActive, Phase: task.PhaseImplementing, CurrentExecutionID: "exec", Work: task.Work{Phase: "IMPLEMENTATION", RunID: "impl"}}
	p := presentation.Projection{Details: presentation.Details{TaskID: t.ID}, Progress: []presentation.PhaseProgress{{Phase: "REVIEW", Status: "PENDING"}, {Phase: "TASK", Status: "COMPLETE"}}, Capabilities: []presentation.Capability{{Action: "RECOVER", Available: true}}, Recovery: &presentation.Recovery{Required: true, Status: "RECOVERY_REQUIRED"}}
	c := CurrentWorkflow{Task: t, Execution: &executionproposal.Proposal{ID: "exec", TaskID: t.ID, ImplementationRunID: "impl"}, AgentOperation: &agentconversation.State{Claim: &agentconversation.Claim{RunID: "impl"}}, Runs: []run.Run{{ID: "impl", ProjectID: t.ProjectID, FeatureAreaID: t.FeatureAreaID, TaskID: t.ID, ExecutionProposalID: "exec", Purpose: run.PurposeImplementation, Status: run.StatusInfrastructureFailed, AgentRuntime: &agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: "impl"}, LatestPlan: &agentruntime.Plan{Total: 5, Completed: 5}}}}}
	return p, c
}
func TestCardStagesFailedRecoveryUsesExactFactsAndDoesNotChangeAuthority(t *testing.T) {
	p, c := cardStageGraph()
	before, _ := json.Marshal(c)
	progress := append([]presentation.PhaseProgress(nil), p.Progress...)
	caps := append([]presentation.Capability(nil), p.Capabilities...)
	projectCardStages(&p, c)
	statuses := func(items []presentation.PhaseProgress) []string {
		out := []string{}
		for _, v := range items {
			out = append(out, v.Status)
		}
		return out
	}
	if got := statuses(p.TaskStages.Stages); !reflect.DeepEqual(got, []string{"INFRASTRUCTURE_FAILED", "PENDING"}) {
		t.Fatal(got)
	}
	if got := statuses(p.ProductProgress); !reflect.DeepEqual(got, []string{"COMPLETE", "COMPLETE", "CURRENT", "PENDING", "PENDING"}) {
		t.Fatal(got)
	}
	if p.AgentRuntime == nil || p.AgentRuntime.PlanCompleted != 5 {
		t.Fatal("missing exact run summary")
	}
	after, _ := json.Marshal(c)
	if string(before) != string(after) || !reflect.DeepEqual(progress, p.Progress) || !reflect.DeepEqual(caps, p.Capabilities) {
		t.Fatal("display projection mutated authority")
	}
}
func TestCardStagesRejectOtherTaskAttemptAndTelemetry(t *testing.T) {
	p, c := cardStageGraph()
	other := c.Runs[0]
	other.ID = "other-attempt"
	other.Status = run.StatusSucceeded
	other.AgentRuntime = &agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: other.ID}}
	c.Runs = append(c.Runs, other)
	c.AgentOperation.Claim.RunID = other.ID
	c.Runs[1].TaskID = "other-task"
	p.AgentRuntime = &agentruntime.Summary{RunID: other.ID}
	projectCardStages(&p, c)
	if p.TaskStages.Recovery != nil || p.ProductProgress[2].Status != "CURRENT" || p.TaskStages.Stages[0].SourceID != "impl" || p.AgentRuntime.RunID != "impl" {
		t.Fatalf("mixed Task graph: %+v", p)
	}
	c.Runs[0].AgentRuntime.Identity.RunID = "foreign-telemetry"
	p.ProductProgress = nil
	projectCardStages(&p, c)
	if p.AgentRuntime != nil {
		t.Fatal("foreign summary accepted")
	}
}
func TestCardStagesVerificationAndWaiverAreExact(t *testing.T) {
	for _, status := range []run.Status{run.StatusRunning, run.StatusSucceeded, run.StatusFailed, run.StatusCanceled} {
		p, c := cardStageGraph()
		c.AgentOperation = nil
		c.Execution.VerificationRunID = "verify"
		c.Task.Work.RunID = "verify"
		c.Task.Work.Phase = "VERIFICATION"
		c.Task.Phase = task.PhaseVerifying
		c.Runs[0].Status = run.StatusSucceeded
		c.Runs = append(c.Runs, run.Run{ID: "verify", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutionProposalID: "exec", Purpose: run.PurposeVerification, Status: status})
		projectCardStages(&p, c)
		if p.TaskStages.Stages[1].Status != string(status) || p.TaskStages.Stages[0].Status != "SUCCEEDED" {
			t.Fatal(p.TaskStages)
		}
	}
	p, c := cardStageGraph()
	c.AgentOperation = nil
	c.Execution.Outcome = executionproposal.OutcomeSucceeded
	c.Task.Phase = task.PhaseAcceptancePending
	c.Execution.VerificationSpec.Mode = task.VerificationModeNone
	c.Runs[0].Status = run.StatusSucceeded
	projectCardStages(&p, c)
	if p.TaskStages.Stages[1].Status != "WAIVED" || p.ProductProgress[3].Status != "COMPLETE" {
		t.Fatal(p)
	}
}
func TestCardStagesRequiresSelectedDevelopmentTaskAndApprovalProof(t *testing.T) {
	p, c := cardStageGraph()
	c.Task.ReviewApprovalID = ""
	c.Task.Phase = task.PhaseReviewRequired
	projectCardStages(&p, c)
	if p.ProductProgress[0].Status != "CURRENT" || p.ProductProgress[1].Status != "PENDING" {
		t.Fatal("invented approval")
	}
	for _, kind := range []task.Type{task.Type("REVIEW"), task.TypeInspection} {
		p, c := cardStageGraph()
		c.Task.Type = kind
		projectCardStages(&p, c)
		if p.TaskStages != nil {
			t.Fatal("non-development stages")
		}
	}
	p, c = cardStageGraph()
	p.Details.TaskID = "other"
	projectCardStages(&p, c)
	if p.TaskStages != nil {
		t.Fatal("wrong selected Task")
	}
}

func TestExplicitTaskPresentationDoesNotSelectNewFeatureAreaReview(t *testing.T) {
	c, d, contextValue, state := presentationFixture()
	executionPresentation(&c, executionproposal.OutcomeSucceeded)
	c.Task.Type = task.TypeDevelopment
	c.Task.ReviewApprovalID = "approval"
	c.Task.Work.Phase = "IMPLEMENTATION"
	c.Task.CreatedAt = time.Unix(1, 0)
	newer := review.Review{ReviewDepth: "DEEP", ID: "new-review", ProjectID: c.Task.ProjectID, FeatureAreaID: c.Task.FeatureAreaID, RunID: "new-review-run", CreatedAt: time.Unix(200, 0)}
	c.ActiveReview = &newer
	c.LatestReview = &newer
	c.CurrentReview = &newer
	c.Reviews = append(c.Reviews, newer)
	selectTaskPresentationReview(&c)
	p := projectFeatureAreaPresentation(c, d, contextValue, state)
	projectCardStages(&p, c)
	if p.TaskStages == nil || p.TaskStages.TaskID != c.Task.ID || p.Details.ReviewID == newer.ID {
		t.Fatalf("new Review replaced exact Task: %+v", p)
	}
}

func TestCardStagesKeepsRunEnteredBetweenCompletedImplementationAndVerification(t *testing.T) {
	p, c := cardStageGraph()
	c.AgentOperation = nil
	c.Runs[0].Status = run.StatusSucceeded
	c.Execution.Outcome = executionproposal.OutcomeRunning
	c.Task.Phase = task.PhaseVerificationReady
	projectCardStages(&p, c)
	if p.ProductProgress[3].Status != "PENDING" || p.TaskStages.Stages[1].Status != "PENDING" {
		t.Fatal("pending Verification erased entered Run", p.ProductProgress)
	}
}
