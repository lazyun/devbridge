package workflowpresentation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestImplementationScopeConflictExplainsHandlingWithoutGrantingAuthority(t *testing.T) {
	for _, state := range []string{"RECEIVED", "UNKNOWN", "UNAVAILABLE", ""} {
		t.Run(state, func(t *testing.T) {
			owner, runs, executions := nextStepFixture()
			owner.RecoveryOrigin.Phase = task.PhaseImplementing
			executions[0].VerificationRunID = ""
			runs = runs[:1]
			runs[0].Status = run.StatusFailed
			runs[0].Failure = &run.Failure{Kind: run.FailureTask, Code: "SCOPE_CONFLICT", Message: "Scope cannot satisfy the approved goal"}
			runs[0].NativeResultState = state
			before, _ := json.Marshal(owner)
			v := BuildTaskWithHistory(owner, runs, nil, executions, nil, time.Time{})
			if v.RunRecovery == nil || v.NextStep == nil || !strings.Contains(v.NextStep.Summary, "范围变化必须正式确认新方案") || !strings.Contains(v.NextStep.Summary, "是否补审") || !strings.Contains(v.NextStep.Summary, "保留已改文件") {
				t.Fatalf("missing handling: recovery=%+v next=%+v", v.RunRecovery, v.NextStep)
			}
			want := "PROCESS_RUN_RESULT"
			if state == "UNKNOWN" {
				want = "RECOVER_RUN_RESULT"
			}
			if state == "UNAVAILABLE" || state == "" {
				want = "CHECK_RUN_RESULT"
			}
			if v.NextStep.Code != want || v.NextStep.RunID != "impl" || v.NextStep.ExecutionID != "execution" || v.RunRecovery.Required != (state == "UNKNOWN") {
				t.Fatalf("changed native result handling: %+v %+v", v.RunRecovery, v.NextStep)
			}
			if v.Phase != task.PhaseRecoveryRequired || len(v.Actions) != 0 || v.ActionsStatus != "UNAVAILABLE" {
				t.Fatalf("read guidance created authority: %+v", v)
			}
			after, _ := json.Marshal(owner)
			if string(before) != string(after) {
				t.Fatal("presentation changed Task")
			}
		})
	}
}

func TestScopeConflictGuidanceDoesNotBorrowHistoryOrOtherOperation(t *testing.T) {
	for _, mutation := range []string{"historical-run", "other-execution", "revision", "verification", "review", "not-terminal", "message-only"} {
		t.Run(mutation, func(t *testing.T) {
			owner, runs, executions := nextStepFixture()
			owner.RecoveryOrigin.Phase = task.PhaseImplementing
			executions[0].VerificationRunID = ""
			runs = runs[:1]
			runs[0].Status = run.StatusFailed
			runs[0].NativeResultState = "RECEIVED"
			runs[0].Failure = &run.Failure{Kind: run.FailureTask, Code: "SCOPE_CONFLICT"}
			switch mutation {
			case "historical-run":
				executions[0].ImplementationRunID = "current-impl"
			case "other-execution":
				runs[0].ExecutionProposalID = "other"
			case "revision":
				executions[0].TaskDefinitionRevisionID = "old"
			case "verification":
				owner.RecoveryOrigin.Phase = task.PhaseVerifying
			case "review":
				runs[0].Purpose = run.PurposeScopeReview
			case "not-terminal":
				runs[0].Status = run.StatusRunning
			case "message-only":
				runs[0].Failure.Code = "TASK_FAILED"
				runs[0].Failure.Message = "SCOPE_CONFLICT: ordinary text"
			}
			v := BuildTaskWithHistory(owner, runs, nil, executions, nil, time.Time{})
			if v.RunRecovery != nil && strings.Contains(v.RunRecovery.Summary, "范围变化必须正式确认新方案") {
				t.Fatalf("borrowed typed failure: %+v", v.RunRecovery)
			}
		})
	}
}
