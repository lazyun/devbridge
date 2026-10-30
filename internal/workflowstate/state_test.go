package workflowstate

import (
	"testing"

	"github.com/lazyun/devbridge/internal/projectcontext"
)

func TestWorkflowPhaseDerivation(t *testing.T) {
	tests := []struct {
		name      string
		input     Input
		phase     Phase
		objective Objective
	}{
		{"no bound session", Input{}, PhaseProjectSelectionRequired, ObjectiveSelectProject},
		{"context required", Input{SessionBound: true, ContextStatus: projectcontext.StatusRequired}, PhaseProjectContextRequired, ObjectiveAcquireProjectContext},
		{"context running", Input{SessionBound: true, ContextStatus: projectcontext.StatusRunning}, PhaseProjectContextRunning, ObjectiveAcquireProjectContext},
		{"ready no featureArea", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady}, PhaseReadyForFeatureArea, ObjectiveWaitForEngineeringGoal},
		{"no guidance no featureArea", Input{SessionBound: true, ContextStatus: projectcontext.StatusNoGuidance}, PhaseReadyForFeatureArea, ObjectiveWaitForEngineeringGoal},
		{"featureArea no review", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true}, PhaseFeatureAreaAnalysis, ObjectiveUnderstandFeatureArea},
		{"review active", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Review: ReviewRunning}, PhaseFeatureAreaReviewRunning, ObjectiveUnderstandFeatureArea},
		{"review retry", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Review: ReviewFailed}, PhaseFeatureAreaAnalysis, ObjectiveUnderstandFeatureArea},
		{"review succeeded", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Review: ReviewSucceeded}, PhaseTaskRequired, ObjectiveFormExecutableTask},
		{"task pending", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Review: ReviewSucceeded, Task: TaskPending}, PhaseTaskAwaitingConfirmation, ObjectiveWaitForTaskDecision},
		{"execution pending", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Task: TaskConfirmed, Execution: ExecutionPendingAuthorization}, PhaseExecutionAwaitingAuthorization, ObjectiveWaitForExecutionDecision},
		{"execution running", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Execution: ExecutionRunning}, PhaseExecutionRunning, ObjectiveObserveExecution},
		{"approval", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Execution: ExecutionRunning, ApprovalPending: true}, PhaseWaitingApproval, ObjectiveResolveUserDecision},
		{"terminal", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Execution: ExecutionSucceeded}, PhaseResultAvailable, ObjectivePresentResult},
		{"blocker", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, HardBlocker: true}, PhaseBlocked, ObjectiveResolveBlocker},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Derive(test.input)
			if got.Phase != test.phase || got.Objective != test.objective {
				t.Fatalf("derive=%+v want phase=%s objective=%s", got, test.phase, test.objective)
			}
		})
	}
}

func TestNoGuidanceIsUsableAndNotABlocker(t *testing.T) {
	got := ProjectReadiness(projectcontext.StatusNoGuidance)
	if got.State != ReadinessReadyNoProjectGuidance || !got.Usable || !got.CanCreateFeatureArea || len(got.Blockers) != 0 {
		t.Fatalf("readiness=%+v", got)
	}
}

func TestTopicCapabilityDoesNotDependOnProjectContext(t *testing.T) {
	for _, status := range []projectcontext.Status{projectcontext.StatusRequired, projectcontext.StatusRunning, projectcontext.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			input := Input{SessionBound: true, ContextStatus: status, FeatureAreaExists: true}
			got := WorkflowReadiness(input, Derive(input))
			if !got.CanCreateFeatureArea || got.CanStartReview || got.CanCreateTask || got.CanExecute || len(got.Blockers) == 0 {
				t.Fatalf("readiness=%+v", got)
			}
		})
	}
}

func TestWorkflowCapabilitiesFollowProductDecisions(t *testing.T) {
	tests := []struct {
		name                        string
		input                       Input
		review, createTask, execute bool
	}{
		{"review", Input{SessionBound: true, ContextStatus: projectcontext.StatusNoGuidance, FeatureAreaExists: true}, true, false, false},
		{"review retry", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Review: ReviewFailed}, true, false, false},
		{"create task", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Review: ReviewSucceeded}, false, true, false},
		{"execute", Input{SessionBound: true, ContextStatus: projectcontext.StatusReady, FeatureAreaExists: true, Task: TaskConfirmed, Execution: ExecutionPendingAuthorization}, false, false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := WorkflowReadiness(test.input, Derive(test.input))
			if got.CanStartReview != test.review || got.CanCreateTask != test.createTask || got.CanExecute != test.execute || got.Summary == "" {
				t.Fatalf("readiness=%+v", got)
			}
		})
	}
}

func TestUsableProjectContextRemainsReviewableDuringAndAfterRefresh(t *testing.T) {
	for _, status := range []projectcontext.Status{projectcontext.StatusRunning, projectcontext.StatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			input := Input{SessionBound: true, ContextStatus: status, ContextUsable: true, FeatureAreaExists: true}
			workflow := Derive(input)
			readiness := WorkflowReadiness(input, workflow)
			if workflow.Phase != PhaseFeatureAreaAnalysis || !readiness.Usable || !readiness.CanStartReview || len(readiness.Blockers) != 0 {
				t.Fatalf("workflow=%+v readiness=%+v", workflow, readiness)
			}
		})
	}

	input := Input{SessionBound: true, ContextStatus: projectcontext.StatusRunning, FeatureAreaExists: true}
	workflow := Derive(input)
	if workflow.Phase != PhaseProjectContextRunning || WorkflowReadiness(input, workflow).CanStartReview {
		t.Fatalf("first bootstrap must block Review: workflow=%+v readiness=%+v", workflow, WorkflowReadiness(input, workflow))
	}
}
