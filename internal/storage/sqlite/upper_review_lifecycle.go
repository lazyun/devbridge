package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/review"
)

// requireUpperReviewCurrentTargetTx is a pure authorization predicate. Callers
// use it both before file IO in a read snapshot and after IO inside the short
// writer transaction that publishes evidence, activates access, or finalizes
// a report. Retirement and cancellation therefore linearize before any later
// authorization/content commit; historical report reads do not use this gate.
func requireUpperReviewCurrentTargetTx(ctx context.Context, tx *sql.Tx, t review.UpperTarget) error {
	return requireUpperReviewTargetTx(ctx, tx, t, false)
}

func requireUpperReviewTargetTx(ctx context.Context, tx *sql.Tx, t review.UpperTarget, preparing bool) error {
	canonicalWorkspace, err := canonicalUpperReviewWorkspaceTx(ctx, tx, t.ReviewID, t.RunID, preparing)
	if err != nil {
		return err
	}

	var allowed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM reviews v JOIN tasks t ON t.id=v.task_id JOIN feature_areas a ON a.id=v.feature_area_id
 JOIN review_grants g ON g.review_id=v.id JOIN runs r ON r.id=g.run_id
 JOIN feature_area_agent_claims c ON c.feature_area_id=a.id JOIN runtime_authority ra ON ra.singleton=1
 WHERE v.id=? AND v.created_by_ai_session_id=? AND v.feature_area_id=? AND v.task_id=? AND v.executor_kind='UPPER_LLM'
 AND a.project_id=v.project_id AND a.legacy_read_only=0 AND a.status IN ('DISCOVERING','ACTIVE')
 AND t.project_id=v.project_id AND t.feature_area_id=a.id AND t.legacy_read_only=0 AND t.status='ACTIVE' AND t.phase='REVIEWING' AND t.current_review_id=v.id
 AND g.project_id=v.project_id AND g.feature_area_id=a.id AND (g.protocol_version=2 OR (g.repository_id=v.repository_id AND g.baseline=v.base_commit))
 AND ((g.protocol_version=2 AND g.worktree_id IS NULL AND COALESCE(g.baseline,'')='' AND (r.workspace_id IS NULL OR (? AND r.workspace_id=v.workspace_id)) AND r.worktree_id IS NULL AND (g.workspace_id IS NULL OR g.workspace_id=v.workspace_id)) OR (g.protocol_version=1 AND (g.workspace_id IS NULL OR g.workspace_id=r.workspace_id) AND (g.worktree_id IS NULL OR g.worktree_id=r.worktree_id)))
 AND g.run_id=? AND g.runtime_epoch=? AND g.generation=? AND g.state IN ('PREPARING','ACTIVE','AWAITING_REPORT')
 AND v.linked_run_id=r.id AND r.project_id=v.project_id AND r.feature_area_id=a.id AND r.task_id=t.id
 AND r.review_id=v.id AND r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.purpose='SCOPE_REVIEW'
 AND r.runtime_epoch=g.runtime_epoch AND ra.epoch_id=r.runtime_epoch AND (r.status IN ('RUNNING','COLLECTING') OR (? AND r.status IN ('CREATED','PREPARING')))
 AND r.thread_id IS NULL AND r.turn_id IS NULL AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND c.operation_kind='REVIEW' AND c.operation_id=v.id AND c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch
 AND EXISTS(SELECT 1 FROM agent_executions e WHERE e.id=r.agent_execution_id AND e.provenance_kind<>'LEGACY_UNKNOWN' AND e.task_id=t.id AND e.project_id=v.project_id AND e.feature_area_id=a.id AND e.semantic_owner_kind='REVIEW' AND e.semantic_owner_id=v.id)
 AND NOT EXISTS(SELECT 1 FROM agent_orphan_dispositions o WHERE o.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations k WHERE k.run_id=r.id AND k.owner_type='REVIEW' AND k.owner_id=v.id AND k.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
 AND NOT EXISTS(SELECT 1 FROM review_reports p WHERE p.review_id=v.id OR p.run_id=r.id))`, t.ReviewID, t.AISessionID, t.FeatureAreaID, t.TaskID, canonicalWorkspace, t.RunID, t.RuntimeEpoch, t.GrantGeneration, preparing).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return review.ErrTargetStale
	}
	return nil
}

// CheckPreparationTarget admits physical preparation, not file reads. The same
// lifecycle predicate is used, with pre-running states explicitly permitted.
func (s *UpperReviewAccessStore) CheckPreparationTarget(ctx context.Context, t review.UpperTarget) error {
	if _, err := s.UpperReviewAccessForSession(ctx, t.AISessionID, t.ReviewID); err != nil {
		return err
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return requireUpperReviewTargetTx(ctx, tx, t, true)
}
