package sqlite

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
)

// SettleRejectedImplementationPreparation does not infer safety from a terminal
// Run projection. Only an exact current admission rejected before attaching any
// runtime workspace is covered. Unknown handoffs and existing runtime evidence
// retain their existing recovery authority. Qualification and completion share
// one write transaction, including the canonical event and claim release.
func (s *ExecutionProposalStore) SettleRejectedImplementationPreparation(ctx context.Context, executionID, runID string, now time.Time) (executionproposal.Proposal, bool, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, false, err
	}
	defer tx.Rollback()
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM execution_proposals e JOIN runs r ON r.id=e.run_id
 JOIN tasks t ON t.id=e.task_id
 JOIN feature_areas a ON a.id=t.feature_area_id AND a.project_id=e.project_id AND a.status='ACTIVE'
 JOIN feature_area_agent_claims c ON c.run_id=r.id
 JOIN runtime_handoffs h ON h.run_id=r.id
 WHERE e.id=? AND r.id=? AND e.status='CONSUMED' AND e.outcome='RUNNING'
 AND e.verification_run_id IS NULL AND t.current_execution_id=e.id
 AND t.current_definition_revision_id=e.task_definition_revision_id AND t.status='ACTIVE'
 AND t.phase='IMPLEMENTING' AND t.project_id=e.project_id
 AND r.execution_proposal_id=e.id AND r.task_id=t.id AND r.project_id=e.project_id
 AND r.feature_area_id=t.feature_area_id AND r.operation_kind='EXECUTION' AND r.operation_id=e.id
 AND r.purpose='IMPLEMENTATION' AND r.status='INFRASTRUCTURE_FAILED'
 AND r.failure_class='INFRASTRUCTURE_FAILED'
 AND r.failure_code='RUNTIME_WORKSET_PREPARATION_FAILED' AND r.failure_stage='exact repository workset'
 AND r.finished_at IS NOT NULL AND r.started_at IS NULL
 AND r.workspace_id IS NULL AND r.worktree_id IS NULL
 AND r.thread_id IS NULL AND r.turn_id IS NULL AND r.start_snapshot IS NULL AND r.end_snapshot IS NULL
 AND r.prompt_sent_at IS NULL AND COALESCE(r.final_codex_prompt,'')='' AND COALESCE(r.prompt_sha256,'')=''
 AND COALESCE(r.prompt_bytes,0)=0 AND COALESCE(r.runtime_workset_json,'')='' AND COALESCE(r.runtime_workset_hash,'')=''
 AND h.state='REJECTED' AND h.code='RUNTIME_WORKSET_PREPARATION_FAILED' AND h.accepted_at IS NULL
 AND h.runtime_epoch=r.runtime_epoch AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND c.runtime_epoch=r.runtime_epoch AND c.feature_area_id=r.feature_area_id
 AND c.operation_kind='EXECUTION' AND c.operation_id=e.id
 AND NOT EXISTS(SELECT 1 FROM runs later WHERE later.execution_proposal_id=e.id AND later.id<>r.id)
 AND NOT EXISTS(SELECT 1 FROM run_repository_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM run_execution_records x WHERE x.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations x WHERE x.execution_id=e.id OR x.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_recovery_states x WHERE x.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM recovery_status_records x WHERE x.source_run_id=r.id OR x.recovery_run_id=r.id)
 )`, executionID, runID).Scan(&eligible)
	if err != nil || !eligible {
		return executionproposal.Proposal{}, false, err
	}
	if err = s.db.checkRunTx(ctx, tx, runID); err != nil {
		return executionproposal.Proposal{}, false, err
	}
	var code, message string
	if err = tx.QueryRowContext(ctx, `SELECT failure_code,failure_message FROM runs WHERE id=?`, runID).Scan(&code, &message); err != nil {
		return executionproposal.Proposal{}, false, err
	}
	value, err := s.completeTx(ctx, tx, executionID, executionproposal.OutcomeFailed, code, message, now)
	if err != nil {
		return value, false, err
	}
	if err = tx.Commit(); err != nil {
		return value, false, err
	}
	return value, true, nil
}
