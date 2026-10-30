package workflowpresentation

import (
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func interruptedContinuationFixture() TaskView {
	o, runs, executions := nextStepFixture()
	o.WorkSpec.ImplementationMode = task.ImplementationCodingAgent
	runs[1].Failure = &run.Failure{Code: run.CodexTurnInterruptedCode}
	runs[1].NativeResultState = "RECEIVED"
	v := BuildTaskWithHistory(o, runs, nil, executions, nil, time.Time{})
	v.ImplementationMode = "CODING_AGENT"
	v.ActionsStatus = "AVAILABLE"
	v.CurrentExecutionAcceptance = &ExecutionAcceptanceView{DecisionID: "TASK_ACCEPTANCE:execution"}
	v.Actions = []Capability{{Action: "REQUEST_REVERIFICATION", Available: true, Target: &ActionTarget{TaskID: "task", FeatureAreaID: "area", ExecutionID: "execution", SubjectID: "execution", TaskDefinitionRevisionID: "revision", DecisionID: "TASK_ACCEPTANCE:execution"}}}
	return v
}

func TestCodexContinuationUsesCurrentExactCoreEligibility(t *testing.T) {
	for _, name := range []string{"safe", "unknown", "unavailable-result", "not-started", "busy", "wrong-task", "wrong-execution", "wrong-revision", "canceled", "ordinary-failure", "historical-run", "readonly", "terminal", "qualification-unavailable"} {
		t.Run(name, func(t *testing.T) {
			v := interruptedContinuationFixture()
			switch name {
			case "unknown":
				v.VerificationRun.NativeResultState = "UNKNOWN"
			case "unavailable-result":
				v.VerificationRun.NativeResultState = "UNAVAILABLE"
			case "not-started":
				v.VerificationRun.NativeResultState = "NOT_STARTED"
			case "busy":
				v.Actions[0].Available = false
			case "wrong-task":
				v.Actions[0].Target.TaskID = "other"
			case "wrong-execution":
				v.Actions[0].Target.ExecutionID = "other"
			case "wrong-revision":
				v.Actions[0].Target.TaskDefinitionRevisionID = "other"
			case "canceled":
				v.VerificationRun.TransportStatus = "CANCELED"
			case "ordinary-failure":
				v.VerificationRun.FailureCode = "VERIFICATION_CHECK_FAILED"
			case "historical-run":
				v.currentVerificationExact = false
			case "readonly":
				v.ReadOnly = true
			case "terminal":
				v.Terminal = true
			case "qualification-unavailable":
				v.ActionsStatus = "UNAVAILABLE"
			}
			got := CodexContinuationFor(v)
			available := name == "safe" || name == "not-started"
			if (got != nil && got.State == "AVAILABLE" && got.Action != nil) != available {
				t.Fatalf("continuation %+v", got)
			}
			if available && (got.RunID != "verify" || got.Purpose != "VERIFICATION" || got.Action.Action != "REQUEST_REVERIFICATION") {
				t.Fatalf("wrong continuation owner %+v", got)
			}
		})
	}
}

func TestInterruptedImplementationNeverBorrowsLaterVerification(t *testing.T) {
	v := interruptedContinuationFixture()
	v.CurrentExecution.VerificationRunID, v.VerificationRun = "", nil
	v.ImplementationRun.TransportStatus, v.ImplementationRun.FailureCode, v.ImplementationRun.NativeResultState = "INFRASTRUCTURE_FAILED", run.CodexTurnInterruptedCode, "RECEIVED"
	v.Actions[0].Action = "RETRY_IMPLEMENTATION"
	if got := CodexContinuationFor(v); got == nil || got.State != "AVAILABLE" || got.RunID != "impl" || got.Purpose != "IMPLEMENTATION" {
		t.Fatalf("implementation %+v", got)
	}
	v.CurrentExecution.VerificationRunID = "later-verification"
	if got := CodexContinuationFor(v); got != nil {
		t.Fatalf("borrowed old implementation %+v", got)
	}
}

func TestInterruptedCodexReviewUsesItsOwnSupplementalReviewEligibility(t *testing.T) {
	v := interruptedContinuationFixture()
	v.Phase, v.BusinessPhase = task.PhaseRecoveryRequired, task.PhaseReviewing
	v.CurrentReview = &ReviewView{ID: "review", TaskID: v.ID, ExecutorKind: review.ExecutorCodingAgent, Run: &VerificationRunView{ID: "review-run", TransportStatus: "INFRASTRUCTURE_FAILED", NativeResultState: "RECEIVED", FailureCode: run.CodexTurnInterruptedCode}}
	v.Actions = []Capability{{Action: "REQUEST_TASK_REVIEW", Available: true, Target: &ActionTarget{TaskID: v.ID, FeatureAreaID: "area", ExpectedReviewRequest: &wp.TaskReviewRequestTarget{CurrentReviewID: "review"}}}}
	if got := CodexContinuationFor(v); got == nil || got.State != "AVAILABLE" || got.RunID != "review-run" || got.Purpose != "SCOPE_REVIEW" {
		t.Fatalf("review continuation %+v", got)
	}
	v.Actions[0].Target.ExpectedReviewRequest.CurrentReviewID = "other-review"
	if got := CodexContinuationFor(v); got == nil || got.State == "AVAILABLE" || got.Action != nil {
		t.Fatalf("borrowed review eligibility %+v", got)
	}
	v.CurrentReview.ExecutorKind = review.ExecutorUpperLLM
	if got := CodexContinuationFor(v); got != nil {
		t.Fatalf("GPT review mistaken for Codex %+v", got)
	}
}
