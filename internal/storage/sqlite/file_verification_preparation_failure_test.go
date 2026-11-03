package sqlite

import (
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
)

// Pure canonical Run/receipt predicate tests only. These do not claim to test
// the cross-table FILE source/claim transaction; the HTTP SQL-cut test does.
func TestFileVerificationNeverEnteredGuard(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"safe", "missing_receipt", "unknown_receipt", "preparing_receipt", "accepted_receipt", "ever_accepted", "stale_epoch", "mismatched_record_epoch", "wrong_execution", "wrong_task", "wrong_run", "implementation_run", "running_run", "unfinished_run", "started_run", "prompt_sent", "prompt_hash", "workspace_attached", "thread_entered", "wrong_failure_code", "wrong_failure_stage", "wrong_failure_kind"} {
		t.Run(name, func(t *testing.T) {
			value := run.Run{ID: "verification", TaskID: "task", ExecutionProposalID: "file-execution", AgentExecutionID: "file-execution", RuntimeEpoch: "epoch", Purpose: run.PurposeVerification, Status: run.StatusInfrastructureFailed, FinishedAt: &now, Failure: &run.Failure{Kind: run.FailureInfrastructure, Code: "RUNTIME_WORKSET_PREPARATION_FAILED", Stage: "exact repository workset", Message: "controlled failure"}}
			handoff := executionproposal.Handoff{State: executionproposal.HandoffTerminal, RecordExists: true, RecordedState: executionproposal.HandoffRejected, RecordedCode: "RUNTIME_WORKSET_PREPARATION_FAILED", RecordEpochMatchesRun: true, RunEpochCurrent: true, RunID: value.ID, ExecutionID: value.ExecutionProposalID, TaskID: value.TaskID, RuntimeEpoch: value.RuntimeEpoch, RecordedRuntimeEpoch: value.RuntimeEpoch}
			switch name {
			case "missing_receipt":
				handoff.RecordExists = false
			case "unknown_receipt":
				handoff.RecordedState = executionproposal.HandoffUnknown
			case "preparing_receipt":
				handoff.RecordedState = executionproposal.HandoffPreparing
			case "accepted_receipt":
				handoff.RecordedState = executionproposal.HandoffAccepted
			case "ever_accepted":
				handoff.AcceptedAt = &now
			case "stale_epoch":
				handoff.RunEpochCurrent = false
			case "mismatched_record_epoch":
				handoff.RecordedRuntimeEpoch = "other-epoch"
			case "wrong_execution":
				handoff.ExecutionID = "other-execution"
			case "wrong_task":
				handoff.TaskID = "other-task"
			case "wrong_run":
				handoff.RunID = "other-run"
			case "implementation_run":
				value.Purpose = run.PurposeImplementation
			case "running_run":
				value.Status = run.StatusRunning
			case "unfinished_run":
				value.FinishedAt = nil
			case "started_run":
				value.StartedAt = &now
			case "prompt_sent":
				value.PromptSentAt = &now
			case "prompt_hash":
				value.PromptSHA256 = "existing-prompt"
			case "workspace_attached":
				value.WorkspaceID = "workspace"
			case "thread_entered":
				value.ThreadID = "thread"
			case "wrong_failure_code":
				value.Failure.Code = "OTHER_FAILURE"
			case "wrong_failure_stage":
				value.Failure.Stage = "after entering runtime"
			case "wrong_failure_kind":
				value.Failure.Kind = run.FailureCanceled
			}
			if got := fileVerificationNeverEntered(value, handoff); got != (name == "safe") {
				t.Fatalf("%s eligibility=%t", name, got)
			}
		})
	}
}
