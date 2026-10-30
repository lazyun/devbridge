package controlapi

import (
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func presentationFixture() (CurrentWorkflow, presentation.DevelopmentContext, projectcontext.ProjectContext, agentconversation.State) {
	return CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area", ProjectID: "project", Title: "Live capability", Status: featurearea.StatusActive, UpdatedAt: time.Unix(1, 0)}}, presentation.DevelopmentContext{Project: presentation.Identity{ID: "project", Name: "Bridge"}, Repositories: []presentation.RepositoryContext{}}, projectcontext.ProjectContext{Status: projectcontext.StatusNoGuidance}, agentconversation.State{Status: "READY"}
}

func TestFeatureAreaRetireCapabilityAcceptsBusyAndDiscovering(t *testing.T) {
	tests := []struct {
		name       string
		area       *featurearea.FeatureArea
		operation  agentconversation.State
		projection presentation.Projection
		available  bool
	}{
		{name: "active and idle", area: &featurearea.FeatureArea{Status: featurearea.StatusActive}, operation: agentconversation.State{Status: "READY"}, available: true},
		{name: "discovering", area: &featurearea.FeatureArea{Status: featurearea.StatusDiscovering}, operation: agentconversation.State{Status: "READY"}, available: true},
		{name: "retired", area: &featurearea.FeatureArea{Status: featurearea.StatusRetired}, operation: agentconversation.State{Status: "READY"}},
		{name: "legacy read only", area: &featurearea.FeatureArea{Status: featurearea.StatusActive, LegacyReadOnly: true}, operation: agentconversation.State{Status: "READY"}},
		{name: "active operation", area: &featurearea.FeatureArea{Status: featurearea.StatusActive}, operation: agentconversation.State{Status: "RUNNING"}, available: true},
		{name: "claim blocker", area: &featurearea.FeatureArea{Status: featurearea.StatusActive}, operation: agentconversation.State{Status: "READY", Claim: &agentconversation.Claim{FeatureAreaID: "area"}}, available: true},
		{name: "pending decision", area: &featurearea.FeatureArea{Status: featurearea.StatusActive}, operation: agentconversation.State{Status: "READY"}, projection: presentation.Projection{PendingDecision: &presentation.Decision{}}, available: true},
		{name: "recovery required", area: &featurearea.FeatureArea{Status: featurearea.StatusActive}, operation: agentconversation.State{Status: "READY"}, projection: presentation.Projection{Recovery: &presentation.Recovery{Required: true}}, available: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectFeatureAreaRetireCapability(test.area, test.operation, &test.projection)
			var got *presentation.Capability
			for index := range test.projection.Capabilities {
				if test.projection.Capabilities[index].Action == "RETIRE" {
					got = &test.projection.Capabilities[index]
				}
			}
			if got == nil || got.Available != test.available {
				t.Fatalf("RETIRE capability = %+v, want available=%v", got, test.available)
			}
			if !test.available && got.Reason == "" {
				t.Fatal("unavailable RETIRE capability must explain the blocker")
			}
		})
	}
}

func definitionPresentation(c *CurrentWorkflow) {
	c.Task = &task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Title: "Intent", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CurrentDefinitionRevisionID: "revision", BaseCommit: "baseline"}
	c.TaskPlan = &taskplan.Plan{ID: "plan", Status: taskplan.StatusConfirmed}
}

func executionPresentation(c *CurrentWorkflow, outcome executionproposal.Outcome) {
	definitionPresentation(c)
	c.Task.Status = task.StatusActive
	c.Task.Phase = task.PhaseImplementing
	c.Task.CurrentExecutionID = "execution"
	c.Execution = &executionproposal.Proposal{ID: "execution", Status: executionproposal.StatusConsumed, Outcome: outcome}
	c.Runs = []run.Run{{ID: "implementation", ExecutionProposalID: "execution", Purpose: run.PurposeImplementation, Status: run.StatusRunning}}
}

func TestFeatureAreaPresentationUsesOnlyTaskPhase(t *testing.T) {
	for _, phase := range []task.Phase{task.PhaseReviewRequired, task.PhaseReviewing, task.PhaseReviewApprovalPending, task.PhaseImplementationReady, task.PhaseImplementing, task.PhaseVerificationReady, task.PhaseVerifying, task.PhaseAcceptancePending} {
		c, d, contextValue, state := presentationFixture()
		c.Task = &task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Title: "Intent", Status: task.StatusActive, Phase: phase, CurrentReviewID: "exact-review", CurrentExecutionID: "exact-execution", CurrentDefinitionRevisionID: "revision", BaseCommit: "base"}
		c.CurrentReview = &review.Review{ReviewDepth: "DEEP", ID: "foreign-review", TaskID: "other"}
		c.Execution = &executionproposal.Proposal{ID: "foreign-execution", Outcome: executionproposal.OutcomeFailed}
		c.Runs = []run.Run{{ID: "historical", Status: run.StatusSucceeded}}
		got := projectFeatureAreaPresentation(c, d, contextValue, state)
		if got.CurrentPhase != string(phase) || got.WorkflowStatus != string(phase) || !reflect.DeepEqual(got.Progress, presentation.TaskPhaseProgress(phase, task.StatusActive)) {
			t.Fatalf("child facts changed Task phase: %+v", got)
		}
		if got.Details.TaskID != "task" || got.Details.ReviewID != "exact-review" || got.Details.ExecutionID != "exact-execution" || got.Details.Baseline != "base" {
			t.Fatal("lost exact Task lineage")
		}
		state.Status = "RECOVERY_REQUIRED"
		got = projectFeatureAreaPresentation(c, d, contextValue, state)
		if got.CurrentPhase != string(phase) || !reflect.DeepEqual(got.Progress, presentation.TaskPhaseProgress(phase, task.StatusActive)) {
			t.Fatal("recovery replaced semantic phase")
		}
	}
}
func TestPresentationRuntimeDecisionCapabilitiesAndLineage(t *testing.T) {
	c, d, contextValue, state := presentationFixture()
	executionPresentation(&c, executionproposal.OutcomeRunning)
	state = agentconversation.State{Status: "BUSY", Claim: &agentconversation.Claim{Kind: agentconversation.Execution, OperationID: "execution", RunID: "run"}}
	c.PendingApproval = &approval.Approval{ID: "approval", RunID: "run", Status: approval.StatusPending, AllowedDecisions: []approval.DecisionType{approval.DecisionCancel}, WorkflowOwnedRun: true}
	got := projectFeatureAreaPresentation(c, d, contextValue, state)
	if got.PendingDecision == nil || got.PendingDecision.ID != "RUNTIME_APPROVAL:approval" || got.PendingDecision.CancelEffect != "DECLINE_ACTION" || got.CurrentPhase != string(task.PhaseImplementing) {
		t.Fatalf("runtime overlay changed Task: %+v", got)
	}
	if !reflect.DeepEqual(presentation.AvailableActions(got), []string{"REFRESH", "VIEW_DETAILS", "CANCEL"}) {
		t.Fatal(got.Capabilities)
	}
}
func TestDevelopmentPresentationPreservesBranchFacts(t *testing.T) {
	no := false
	context := developmentcycle.Context{DevelopmentBranchSelectionRequired: true, Repositories: []developmentcycle.RepositoryContext{{RepositoryID: "repo", RepositoryName: "Source", DevelopmentRef: "refs/heads/development", BranchShortName: "development", CurrentHeadSHA: "current", SelectedHeadSHA: "original", StatusCode: "DEVELOPMENT_REF_MISSING", RepositoryCheckoutMatchesDevelopmentRef: &no}}}
	got := developmentPresentation("project", "Dev Bridge", context)
	if !got.SelectionRequired || got.Repositories[0].Revision != "current" || got.Repositories[0].SelectedRevision != "original" || got.Repositories[0].Status != "DEVELOPMENT_REF_MISSING" || *got.Repositories[0].CheckoutMatches {
		t.Fatalf("context guessed or discarded: %+v", got)
	}
}

func TestDevelopmentPresentationReadyRequiresExistingSelectedRefs(t *testing.T) {
	for _, state := range []string{"NOT_SELECTED", "MISSING", "EXISTS"} {
		context := developmentcycle.Context{DevelopmentBranchSelectionRequired: state == "NOT_SELECTED", Repositories: []developmentcycle.RepositoryContext{{DevelopmentRefStatus: state}}}
		got := developmentPresentation("project", "Project", context)
		if got.Ready != (state == "EXISTS") {
			t.Fatalf("state=%s ready=%t", state, got.Ready)
		}
	}
	if developmentPresentation("project", "Project", developmentcycle.Context{}).Ready {
		t.Fatal("empty repository inventory is not ready")
	}
}

func TestFeatureAreaPresentationRetainsLifecycleUpdateTime(t *testing.T) {
	c, d, contextValue, state := presentationFixture()
	c.Topic.CreatedAt, c.Topic.UpdatedAt = time.Unix(1, 0), time.Unix(9, 0)
	got := projectFeatureAreaPresentation(c, d, contextValue, state)
	if !got.UpdatedAt.Equal(c.Topic.UpdatedAt) {
		t.Fatalf("lifecycle update lost: %s", got.UpdatedAt)
	}
}

func TestTaskReviewSelectionUsesDurablePointerAcrossRemounts(t *testing.T) {
	c, d, contextValue, state := presentationFixture()
	definitionPresentation(&c)
	c.Task.Phase = task.PhaseReviewing
	c.Task.CurrentReviewID = "current-review"
	c.Reviews = []review.Review{{ID: "current-review", TaskID: c.Task.ID, ProjectID: c.Task.ProjectID, FeatureAreaID: c.Task.FeatureAreaID, RunID: "exact-run"}, {ID: "newer-foreign", TaskID: "other", CreatedAt: time.Unix(200, 0)}}
	selectTaskPresentationReview(&c)
	got := projectFeatureAreaPresentation(c, d, contextValue, state)
	if c.CurrentReview == nil || c.CurrentReview.ID != "current-review" || got.Details.TaskID != c.Task.ID || got.Details.ReviewID != "current-review" || got.CurrentPhase != string(task.PhaseReviewing) {
		t.Fatal("remount guessed latest history")
	}
}
