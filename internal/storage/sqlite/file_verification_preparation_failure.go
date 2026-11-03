package sqlite

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
)

// fileVerificationNeverEntered checks canonical persisted facts, not the
// convenient handoff State projection, which is TERMINAL even for UNKNOWN.
func fileVerificationNeverEntered(value run.Run, handoff executionproposal.Handoff) bool {
	return value.ID != "" && value.ExecutionProposalID != "" && value.AgentExecutionID == value.ExecutionProposalID &&
		value.Purpose == run.PurposeVerification && value.Status == run.StatusInfrastructureFailed &&
		value.Failure != nil && value.Failure.Kind == run.FailureInfrastructure &&
		value.Failure.Code == "RUNTIME_WORKSET_PREPARATION_FAILED" && value.Failure.Stage == "exact repository workset" &&
		value.FinishedAt != nil && value.StartedAt == nil && value.PromptSentAt == nil &&
		value.FinalCodexPrompt == "" && value.PromptSHA256 == "" && value.PromptBytes == 0 &&
		value.WorkspaceID == "" && value.WorktreeID == "" && value.RuntimeWorkset == nil &&
		value.ThreadID == "" && value.TurnID == "" && value.StartSnapshot == "" && value.EndSnapshot == "" &&
		handoff.RecordExists && handoff.RecordedState == executionproposal.HandoffRejected &&
		handoff.RecordedCode == value.Failure.Code && handoff.AcceptedAt == nil &&
		handoff.RecordEpochMatchesRun && handoff.RunEpochCurrent &&
		handoff.RunID == value.ID && handoff.ExecutionID == value.ExecutionProposalID &&
		handoff.TaskID == value.TaskID && handoff.RuntimeEpoch == value.RuntimeEpoch &&
		handoff.RecordedRuntimeEpoch == value.RuntimeEpoch
}

// SettleRejectedFileVerification is intentionally FILE-only. It neither retries
// verification nor rewrites its immutable FILE source. Its exact source proof and
// canonical Execution completion share one writer transaction.
func (s *ExecutionProposalStore) SettleRejectedFileVerification(ctx context.Context, executionID, runID string, now time.Time) (executionproposal.Proposal, bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, false, err
	}
	defer tx.Rollback()
	var sourceExact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM direct_file_verifications v
 JOIN direct_file_implementation_results f ON f.id=v.result_id AND f.task_id=v.task_id AND f.revision_id=v.revision_id
 JOIN direct_file_task_authorizations g ON g.workflow_id=f.workflow_id AND g.task_id=f.task_id AND g.project_id=f.project_id AND g.ai_session_id=f.ai_session_id
 JOIN execution_proposals e ON e.id=v.execution_id AND e.task_id=v.task_id AND e.task_definition_revision_id=v.revision_id AND e.project_id=f.project_id
 JOIN tasks t ON t.id=e.task_id AND t.project_id=e.project_id
 JOIN feature_areas a ON a.id=t.feature_area_id AND a.project_id=t.project_id
 JOIN runs r ON r.id=e.verification_run_id AND r.id=e.run_id AND r.execution_proposal_id=e.id
 JOIN feature_area_agent_claims c ON c.run_id=r.id
 WHERE e.id=? AND r.id=? AND e.status='CONSUMED' AND e.outcome='RUNNING'
 AND t.status='ACTIVE' AND t.phase='VERIFYING' AND a.status='ACTIVE'
 AND t.current_execution_id=e.id AND t.current_definition_revision_id=v.revision_id
 AND json_extract(t.work_spec_json,'$.implementationMode')='DIRECT_FILE'
 AND json_extract(e.work_spec_json,'$.implementationMode')='DIRECT_FILE'
 AND g.state='CLOSED' AND g.task_definition_revision_id=v.revision_id AND g.decision_id=t.review_approval_id
 AND r.project_id=e.project_id AND r.task_id=t.id AND r.feature_area_id=t.feature_area_id
 AND r.operation_kind='EXECUTION' AND r.operation_id=e.id
 AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND c.runtime_epoch=r.runtime_epoch AND c.feature_area_id=r.feature_area_id AND c.operation_kind='EXECUTION' AND c.operation_id=e.id
 AND COALESCE(r.runtime_workset_json,'')='' AND COALESCE(r.runtime_workset_hash,'')=''
 AND NOT EXISTS(SELECT 1 FROM runs other WHERE other.execution_proposal_id=e.id AND other.id<>r.id)
 AND NOT EXISTS(SELECT 1 FROM run_repository_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM run_execution_records x WHERE x.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations x WHERE x.execution_id=e.id OR x.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_recovery_states x WHERE x.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM recovery_status_records x WHERE x.source_run_id=r.id OR x.recovery_run_id=r.id)
 )`, executionID, runID).Scan(&sourceExact)
	if err != nil || !sourceExact {
		return executionproposal.Proposal{}, false, err
	}
	actual, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil {
		return executionproposal.Proposal{}, false, err
	}
	handoff, err := readRuntimeHandoff(ctx, tx, actual.ProjectID, actual.ID)
	if err != nil {
		return executionproposal.Proposal{}, false, err
	}
	if !fileVerificationNeverEntered(actual, handoff) {
		return executionproposal.Proposal{}, false, nil
	}
	if err = s.db.checkRunTx(ctx, tx, runID); err != nil {
		return executionproposal.Proposal{}, false, err
	}
	completed, err := s.completeTx(ctx, tx, executionID, executionproposal.OutcomeFailed, actual.Failure.Code, actual.Failure.Message, now)
	if err != nil {
		return completed, false, err
	}
	if err = tx.Commit(); err != nil {
		return completed, false, err
	}
	return completed, true, nil
}
