package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

type upperCancellationFacts struct {
	run          run.Run
	version      agentconversation.UpperVersion
	grantState   string
	reason       string
	currentOwner bool
}

// upperCancellationFactsTx is shared by observation, fresh admission and exact
// pending convergence. All statements are SELECT; it never acquires authority.
func upperCancellationFactsTx(ctx context.Context, tx *sql.Tx, reviewID, currentEpoch string) (upperCancellationFacts, error) {
	f := upperCancellationFacts{}
	var runID, executor string
	err := tx.QueryRowContext(ctx, `SELECT executor_kind,COALESCE(linked_run_id,'') FROM reviews WHERE id=?`, reviewID).Scan(&executor, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		f.reason = "The Review no longer exists."
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if executor != "UPPER_LLM" {
		f.reason = "The durable Review executor is not Upper LLM."
		return f, nil
	}
	if runID == "" {
		f.reason = "The Upper Review has no assigned Run."
		return f, nil
	}
	f.run, err = scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil {
		return f, err
	}
	var exact, noBackend, noResult, noOrphan bool
	err = tx.QueryRowContext(ctx, `SELECT a.epoch_id,COALESCE(g.runtime_epoch,''),g.generation,g.state,
 COALESCE(v.project_id=r.project_id AND v.feature_area_id=r.feature_area_id AND v.task_id=r.task_id
 AND t.project_id=v.project_id AND t.feature_area_id=v.feature_area_id AND fa.project_id=v.project_id
 AND r.operation_kind='REVIEW' AND r.operation_id=v.id AND r.review_id=v.id AND r.purpose='SCOPE_REVIEW'
 AND g.project_id=v.project_id AND g.feature_area_id=v.feature_area_id AND (g.protocol_version=2 OR g.repository_id=v.repository_id)
 AND g.run_id=r.id AND g.runtime_epoch=r.runtime_epoch
 AND c.operation_kind='REVIEW' AND c.operation_id=v.id AND c.run_id=r.id AND c.runtime_epoch=r.runtime_epoch
 AND EXISTS(SELECT 1 FROM agent_executions e WHERE e.id=r.agent_execution_id AND e.provenance_kind<>'LEGACY_UNKNOWN'
 AND e.task_id=r.task_id AND e.project_id=r.project_id AND e.feature_area_id=r.feature_area_id AND e.semantic_owner_kind='REVIEW' AND e.semantic_owner_id=v.id),0),
 r.thread_id IS NULL AND r.turn_id IS NULL AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id),
 NOT EXISTS(SELECT 1 FROM review_reports p WHERE p.review_id=v.id OR p.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM review_result_receipts p WHERE p.review_id=v.id AND p.state='COMPLETED'),
 NOT EXISTS(SELECT 1 FROM agent_orphan_dispositions o WHERE o.run_id=r.id),
 COALESCE(t.current_review_id=v.id AND ((t.status='ACTIVE' AND t.phase='REVIEWING') OR (fa.status='RETIRING' AND t.status IN ('SUCCEEDED','FAILED','CANCELED'))) AND t.legacy_read_only=0
 AND fa.legacy_read_only=0 AND fa.status IN ('DISCOVERING','ACTIVE','RETIRING'),0)
 FROM reviews v JOIN runs r ON r.id=v.linked_run_id JOIN review_grants g ON g.review_id=v.id
 JOIN tasks t ON t.id=v.task_id JOIN feature_areas fa ON fa.id=v.feature_area_id
 JOIN feature_area_agent_claims c ON c.feature_area_id=v.feature_area_id JOIN runtime_authority a ON a.singleton=1
 WHERE v.id=?`, reviewID).Scan(&f.version.AuthorityEpoch, &f.version.GrantEpoch, &f.version.GrantGeneration, &f.grantState, &exact, &noBackend, &noResult, &noOrphan, &f.currentOwner)
	if errors.Is(err, sql.ErrNoRows) {
		f.reason = "The exact Upper Review grant, claim or owner is absent."
		return f, nil
	}
	if err != nil {
		return f, err
	}
	var unsettled bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_workspace_preparation_claims c JOIN runtime_scope_plans p ON p.plan_id=c.plan_id
 WHERE c.run_id=? AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE'
 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings b WHERE b.run_id=c.run_id)
 AND NOT EXISTS(SELECT 1 FROM runtime_preparation_failures f WHERE f.run_id=c.run_id))`, runID).Scan(&unsettled); err != nil {
		return f, err
	}
	switch {
	case !f.version.Valid() || f.version.AuthorityEpoch != currentEpoch:
		f.reason = "The Core runtime authority is unavailable or fenced."
	case !exact:
		f.reason = "The Upper Review, Run, claim and grant identities or epochs differ."
	case unsettled:
		f.reason = "The exact workspace preparation is active or its result is unknown; it has not supplied a quiescence receipt."
	case !noBackend || !noOrphan:
		f.reason = "The Upper Review has conflicting backend or orphan facts."
	case !noResult || f.run.Status == run.StatusSucceeded:
		f.reason = "The Upper Review already has a completed report or successful Run."
	case f.run.Status != run.StatusCreated && f.run.Status != run.StatusPreparing && f.run.Status != run.StatusRunning && f.run.Status != run.StatusCollecting && f.run.Status != run.StatusFailed && f.run.Status != run.StatusInfrastructureFailed && f.run.Status != run.StatusCanceled:
		f.reason = "The Upper Run is outside the safe no-backend cancellation states."
	case f.grantState != "PREPARING" && f.grantState != "ACTIVE" && f.grantState != "AWAITING_REPORT" && f.grantState != "FINALIZING" && f.grantState != "RECOVERY_REQUIRED":
		f.reason = "The Upper grant is already closed or canceled."
	}
	return f, nil
}

// ReviewCancelEligibility reports the exact current or old grant version in one
// read-only snapshot. The current writer rechecks it when a new command arrives.
func (s *ExecutionCancellationStore) ReviewCancelEligibility(ctx context.Context, reviewID string) (agentconversation.Eligibility, error) {
	result := agentconversation.Eligibility{}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	f, err := upperCancellationFactsTx(ctx, tx, reviewID, s.db.epoch)
	if err != nil {
		return result, err
	}
	if f.reason != "" {
		result.Reason = f.reason
		return result, nil
	}
	if !f.currentOwner {
		result.Reason = "The Upper Review is not the current active Task Review in a cancellable lifecycle."
		return result, nil
	}
	pending, err := pendingCancellationOwnerTx(ctx, tx, executioncancel.Input{OwnerType: "REVIEW", OwnerID: reviewID})
	if err != nil {
		return result, err
	}
	if pending {
		result.Reason = "Cancellation is already admitted for this exact Review; continue its original request."
		return result, nil
	}
	result.Available = true
	result.Target = &agentconversation.ActionTarget{FeatureAreaID: f.run.FeatureAreaID, OperationKind: agentconversation.Review, OperationID: reviewID, RunID: f.run.ID, ExpectedUpperVersion: &f.version}
	return result, nil
}
