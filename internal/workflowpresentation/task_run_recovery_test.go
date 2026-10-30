package workflowpresentation

import (
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestRunResultRecoveryDistinguishesNativeResultFromWorkflowBlock(t *testing.T) {
	for _, tc := range []struct {
		state, code string
		required    bool
	}{
		{"UNKNOWN", "RECOVER_RUN_RESULT", true},
		{"RECEIVED", "PROCESS_RUN_RESULT", false},
		{"NOT_STARTED", "PROCESS_RUN_RESULT", false},
		{"UNAVAILABLE", "CHECK_RUN_RESULT", false},
	} {
		t.Run(tc.state, func(t *testing.T) {
			o, runs, executions := nextStepFixture()
			runs[0].NativeResultState, runs[0].AnalysisState = "RECEIVED", "RECORDED"
			runs[1].NativeResultState = tc.state
			v := BuildTaskWithHistory(o, runs, nil, executions, nil, time.Time{})
			if v.RunRecovery == nil || v.RunRecovery.Required != tc.required || v.RunRecovery.RunID != "verify" || v.RunRecovery.Purpose != "VERIFICATION" {
				t.Fatalf("recovery %+v", v.RunRecovery)
			}
			if v.NextStep == nil || v.NextStep.Code != tc.code || v.NextStep.RunID != "verify" {
				t.Fatalf("step %+v", v.NextStep)
			}
			if strings.Contains(v.PhaseDescription, "等待恢复") || strings.Contains(v.Activity.Summary, "等待恢复") {
				t.Fatal("generic recovery leaked into current result")
			}
			if v.Phase != task.PhaseRecoveryRequired || v.ActionsStatus != "UNAVAILABLE" || len(v.Actions) != 0 || v.ImplementationRun.AnalysisState != "RECORDED" {
				t.Fatal("read projection changed admission")
			}
		})
	}
}

func TestReviewRunRecoveryUsesItsOwnNativeResult(t *testing.T) {
	for _, state := range []string{"RECEIVED", "UNKNOWN"} {
		v := TaskView{ID: "task", Status: "ACTIVE", Phase: task.PhaseRecoveryRequired, BusinessPhase: task.PhaseReviewing, BusinessPhaseSourceEventID: "event", CurrentReview: &ReviewView{ID: "review", TaskID: "task", ExecutorKind: review.ExecutorCodingAgent, Run: &VerificationRunView{ID: "review-run", TransportStatus: "INFRASTRUCTURE_FAILED", NativeResultState: state}}}
		ApplyTaskRunRecovery(&v)
		n := TaskNextStepFor(v)
		if v.RunRecovery.Required != (state == "UNKNOWN") || n == nil || n.ReviewID != "review" || n.RunID != "review-run" || n.ExecutionID != "" || n.Purpose != "SCOPE_REVIEW" {
			t.Fatalf("review recovery %+v %+v", v.RunRecovery, n)
		}
		v.CurrentReview.TaskID = "other-task"
		ApplyTaskRunRecovery(&v)
		if v.RunRecovery.Required || TaskNextStepFor(v) != nil {
			t.Fatal("borrowed another Task's review")
		}
	}
}

func TestKnownNativeResultPrioritizesAnalysisWithoutCallingItRecovery(t *testing.T) {
	o, runs, e := nextStepFixture()
	runs[0].AnalysisState = "RECORDED"
	runs[1].NativeResultState, runs[1].Status, runs[1].AnalysisState = "RECEIVED", run.StatusSucceeded, "REQUIRED"
	v := BuildTaskWithHistory(o, runs, nil, e, nil, time.Time{})
	if v.RunRecovery.Required || v.NextStep == nil || v.NextStep.Code != "ANALYZE_RUN_OUTPUT" || v.NextStep.RunID != "verify" {
		t.Fatalf("known analysis %+v %+v", v.RunRecovery, v.NextStep)
	}
}

func TestRunResultRecoveryNeverBorrowsHistoricalOrUnboundEvidence(t *testing.T) {
	for _, mutation := range []string{"run", "execution", "revision", "origin", "stage", "terminal", "readonly"} {
		t.Run(mutation, func(t *testing.T) {
			o, runs, e := nextStepFixture()
			runs[0].AnalysisState, runs[1].NativeResultState = "RECORDED", "UNKNOWN"
			switch mutation {
			case "run":
				e[0].VerificationRunID = "another-run"
			case "execution":
				runs[1].ExecutionProposalID = "previous"
			case "revision":
				e[0].TaskDefinitionRevisionID = "previous"
			case "origin":
				o.RecoveryOrigin = nil
			case "stage":
				o.RecoveryOrigin.Phase = task.PhaseDelivering
			case "terminal":
				o.Status = task.StatusCanceled
			case "readonly":
				o.LegacyReadOnly = true
			}
			v := BuildTaskWithHistory(o, runs, nil, e, nil, time.Time{})
			if v.NextStep != nil || v.RunRecovery != nil && v.RunRecovery.Required {
				t.Fatalf("borrowed recovery %+v %+v", v.NextStep, v.RunRecovery)
			}
		})
	}
}
