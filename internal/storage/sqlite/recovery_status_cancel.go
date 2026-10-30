package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

func (s *RecoveryStatusStore) GetByRecoveryRun(ctx context.Context, id string) (rs.Record, error) {
	return scanRecovery(s.db.sql.QueryRowContext(ctx, `SELECT record_json FROM recovery_status_records WHERE recovery_run_id=?`, id))
}

// BeginCancel reuses the canonical cancellation ledger. Its interrupt identity
// is the child Turn; the original request and old source Turn are untouched.
func (s *RecoveryStatusStore) BeginCancel(ctx context.Context, id string, in executioncancel.Input) (rs.Record, executioncancel.Receipt, bool, error) {
	in = in.Normalized()
	var receipt executioncancel.Receipt
	if err := in.Validate(); err != nil {
		return rs.Record{}, receipt, false, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return rs.Record{}, receipt, false, err
	}
	defer tx.Rollback()
	v, err := recoveryReadTx(ctx, tx, id)
	if err != nil {
		return v, receipt, false, err
	}
	if in.ProjectID != v.Spec.ProjectID || in.OwnerType != v.Spec.OperationKind || in.OwnerID != v.Spec.OperationID || (in.RunID != v.Spec.RunID && in.RunID != v.RecoveryRunID) {
		return v, receipt, false, rs.ErrConflict
	}
	if err = cancellationActorTx(ctx, tx, in); err != nil {
		return v, receipt, false, err
	}
	payload, _ := json.Marshal(in)
	var previous, previousSession, previousOperator string
	err = tx.QueryRowContext(ctx, `SELECT request_payload_json,COALESCE(requested_by_ai_session_id,''),COALESCE(requested_by_operator_id,'') FROM execution_cancellations WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID).Scan(&previous, &previousSession, &previousOperator)
	if err == nil {
		if previous != string(payload) || previousSession != in.AISessionID || previousOperator != in.OperatorPrincipalID {
			return v, receipt, false, executioncancel.ErrRequestConflict
		}
		receipt, err = scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
		return v, receipt, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v, receipt, false, err
	}
	if v.RecoveryRunID != "" && v.RecoveryTurnID == "" {
		receipt, err = s.abandonFailedPreparationTx(ctx, tx, &v, in)
		if err != nil {
			return v, receipt, false, err
		}
		return v, receipt, false, tx.Commit()
	}
	if v.RecoveryRunID == "" || v.RecoveryTurnID == "" || v.State == "RESOLVED" {
		return v, receipt, false, executioncancel.ErrOutcomeUnknown
	}
	// A failed diagnostic is immutable historical execution, not a resumable
	// worker. Only a new, explicit cancellation may close its exact child under
	// the current writer; it does not adopt or restart the failed diagnostic.
	if v.RuntimeEpoch != s.db.epoch && v.State != "FAILED" {
		return v, receipt, false, rs.ErrStale
	}
	if _, _, err = s.validateSourceTx(ctx, tx, v.Spec.Source); err != nil {
		return v, receipt, false, err
	}
	child, _, err := readRecoveryDiagnosticEvidenceTx(ctx, tx, v, false)
	if err != nil {
		return v, receipt, false, err
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,requested_by_operator_id,reason,requested_at,runtime_epoch,state,interrupt_state,thread_id,turn_id,owner_type,owner_id,task_id,backend_disposition,request_payload_json) VALUES(?,?,?,?,?,?,?,?,?,'CANCEL_REQUESTED','REQUESTED',?,?,?,?,?,'ACTIVE',?)`, in.ProjectID, in.RequestID, nullString(in.ExecutionID), v.RecoveryRunID, nullString(in.AISessionID), nullString(in.OperatorPrincipalID), in.Reason, formatTime(now), s.db.epoch, child.ThreadID, child.TurnID, in.OwnerType, in.OwnerID, v.Spec.TaskID, string(payload))
	if err != nil {
		return v, receipt, false, err
	}
	receipt, err = scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return v, receipt, false, err
	}
	return v, receipt, true, tx.Commit()
}

// RecordCancelInterrupt records only an actual interrupt RPC outcome, bound to
// the original cancellation payload and exact child. Terminal observation is
// recorded independently by FinishCancel and never implies an interrupt ACK.
func (s *RecoveryStatusStore) RecordCancelInterrupt(ctx context.Context, id string, in executioncancel.Input, outcome string) (executioncancel.Receipt, error) {
	in = in.Normalized()
	var receipt executioncancel.Receipt
	if err := in.Validate(); err != nil {
		return receipt, err
	}
	if outcome != "ACKNOWLEDGED" && outcome != "OUTCOME_UNKNOWN" {
		return receipt, executioncancel.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	if err = cancellationActorTx(ctx, tx, in); err != nil {
		return receipt, err
	}
	v, err := recoveryReadTx(ctx, tx, id)
	if err != nil {
		return receipt, err
	}
	payload, _ := json.Marshal(in)
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations c JOIN runs child ON child.id=c.run_id
 WHERE c.project_id=? AND c.request_id=? AND c.request_payload_json=? AND c.run_id=?
 AND c.owner_type=? AND c.owner_id=? AND c.task_id=? AND c.turn_id=? AND c.thread_id=child.thread_id
 AND c.turn_id=child.turn_id AND c.runtime_epoch=?)`, in.ProjectID, in.RequestID, string(payload), v.RecoveryRunID, v.Spec.OperationKind, v.Spec.OperationID, v.Spec.TaskID, v.RecoveryTurnID, s.db.epoch).Scan(&exact)
	if err != nil {
		return receipt, err
	}
	if !exact {
		return receipt, executioncancel.ErrRequestConflict
	}
	if err = recordExecutionInterruptTx(ctx, tx, s.db.epoch, in.ProjectID, in.RequestID, outcome); err != nil {
		return receipt, err
	}
	receipt, err = scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return receipt, err
	}
	return receipt, tx.Commit()
}

func (s *RecoveryStatusStore) FinishCancel(ctx context.Context, id string, in executioncancel.Input, a rs.Admission) (rs.Record, executioncancel.Receipt, error) {
	in = in.Normalized()
	var receipt executioncancel.Receipt
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return rs.Record{}, receipt, err
	}
	defer tx.Rollback()
	v, err := recoveryReadTx(ctx, tx, id)
	if err != nil {
		return v, receipt, err
	}
	if err = cancellationActorTx(ctx, tx, in); err != nil {
		return v, receipt, err
	}
	var payload, state, runID, thread, turn, epoch, callerSession, callerOperator string
	err = tx.QueryRowContext(ctx, `SELECT request_payload_json,state,run_id,thread_id,turn_id,runtime_epoch,COALESCE(requested_by_ai_session_id,''),COALESCE(requested_by_operator_id,'') FROM execution_cancellations WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID).Scan(&payload, &state, &runID, &thread, &turn, &epoch, &callerSession, &callerOperator)
	if err != nil {
		return v, receipt, err
	}
	current, _ := json.Marshal(in)
	if callerSession != in.AISessionID || callerOperator != in.OperatorPrincipalID || payload != string(current) || runID != v.RecoveryRunID || turn != v.RecoveryTurnID {
		return v, receipt, executioncancel.ErrRequestConflict
	}
	if state == "CANCELED" {
		receipt, err = scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
		return v, receipt, err
	}
	child, independent, err := readRecoveryDiagnosticEvidenceTx(ctx, tx, v, false)
	if err != nil {
		return v, receipt, err
	}
	// The current writer may observe an already interrupted independent child
	// across restart. Its immutable cancellation epoch identifies the original
	// interrupt, not this observation. beginWrite still fences stale callbacks;
	// exact child provenance, current owner and fresh quiescence remain required.
	if !independent && (epoch != s.db.epoch || (v.RuntimeEpoch != epoch && v.State != "FAILED")) {
		return v, receipt, rs.ErrStale
	}
	if thread != child.ThreadID || turn != child.TurnID {
		return v, receipt, executioncancel.ErrRequestConflict
	}
	if !validRecoveryAdmission(a) {
		return v, receipt, executioncancel.ErrOutcomeUnknown
	}
	// Admission was freshly gathered after terminal + idle confirmation and
	// worker join. Check current owner again without interpreting old transport.
	var exact bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs r
 WHERE r.id=? AND r.operation_kind=? AND r.operation_id=? AND r.turn_id=? AND (
   EXISTS(SELECT 1 FROM feature_area_agent_claims c JOIN tasks t ON t.id=r.task_id WHERE c.run_id=r.id AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id AND ((r.operation_kind='EXECUTION' AND t.current_execution_id=r.operation_id AND t.current_definition_revision_id=?) OR (r.operation_kind='REVIEW' AND t.current_review_id=r.operation_id)))
   OR EXISTS(SELECT 1 FROM project_inspection_agent_claims c JOIN context_probes p ON p.id=c.inspection_id WHERE r.operation_kind='INSPECTION' AND r.feature_area_id IS NULL AND c.run_id=r.id AND c.inspection_id=r.operation_id AND p.run_id=r.id AND p.task_id=r.task_id AND p.project_id=r.project_id AND p.runtime_epoch=r.runtime_epoch AND p.status='RUNNING')
 ))`, v.Spec.RunID, v.Spec.OperationKind, v.Spec.OperationID, v.Spec.TurnID, v.Spec.TaskDefinitionRevisionID).Scan(&exact)
	if err != nil {
		return v, receipt, err
	}
	if !exact {
		return v, receipt, rs.ErrConflict
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status='CANCELED',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id IN (?,?) AND status IN ('RUNNING','PREPARING','WAITING_APPROVAL','COLLECTING','FAILED','INFRASTRUCTURE_FAILED')`, formatTime(now), formatTime(now), v.Spec.RunID, v.RecoveryRunID); err != nil {
		return v, receipt, err
	}
	if err = terminalizeCancellationOwnerTx(ctx, tx, v.Spec.OperationKind, v.Spec.OperationID, v.Spec.TaskID, now); err != nil {
		return v, receipt, err
	}
	if v.Spec.OwnerKind == "PROJECT_INSPECTION" {
		// The canonical source owns the Inspection claim.  Cancel only after the
		// recovery child is known terminal; its old UNKNOWN request remains an
		// immutable source artifact.
		if _, err = tx.ExecContext(ctx, `UPDATE context_probes SET status='CANCELED',runtime_limitation='CANCELED',token_usage_status='UNAVAILABLE',finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND run_id=? AND status='RUNNING'`, formatTime(now), formatTime(now), v.Spec.OperationID, v.Spec.RunID); err != nil {
			return v, receipt, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE id=? AND task_type='INSPECTION' AND work_owner_id=? AND status='ACTIVE'`, formatTime(now), v.Spec.TaskID, v.Spec.OperationID); err != nil {
			return v, receipt, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_operation_boundaries SET advance_state='KNOWN_ADVANCE',exit_boundary_turn_id=?,quiesced=1 WHERE run_id=?`, v.RecoveryTurnID, v.RecoveryRunID); err != nil {
		return v, receipt, err
	}
	if !independent {
		// Historical shared-Thread diagnostics may advance their own boundary.
		// An independent child supplies no continuation evidence for its source.
		if _, err = tx.ExecContext(ctx, `UPDATE agent_conversations SET current_boundary_turn_id=?,continuation_safe=1 WHERE id=? AND backend_thread_id=? AND ended_at IS NULL`, child.TurnID, v.Spec.AgentConversationID, child.ThreadID); err != nil {
			return v, receipt, err
		}
	}
	if err = cancelRuntimeRequestsTx(ctx, tx, v.RecoveryRunID); err != nil {
		return v, receipt, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET state='CANCELED',terminal_observed=1,backend_disposition='TERMINATED',resolved_at=? WHERE project_id=? AND request_id=?`, formatTime(now), in.ProjectID, in.RequestID); err != nil {
		return v, receipt, err
	}
	receipt, err = scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err != nil {
		return v, receipt, err
	}
	// Independent children have no source Conversation boundary to join when
	// projecting quiescence. Freeze the observed terminal outcome itself; later
	// receipt reads must not infer UNKNOWN from that intentionally absent row.
	receipt.QuiescenceState = "QUIESCED"
	raw, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `UPDATE execution_cancellations SET result_json=? WHERE project_id=? AND request_id=? AND state='CANCELED' AND terminal_observed=1`, string(raw), in.ProjectID, in.RequestID); err != nil {
		return v, receipt, err
	}
	v.Resolution = raw
	v.State = "FAILED"
	v.FailureReason = "RECOVERY_CANCELED"
	v.CompletedAt = &now
	v.UpdatedAt = now
	if err = saveRecoveryTx(ctx, tx, v); err != nil {
		return v, receipt, err
	}
	if v.Spec.OwnerKind == "PROJECT_INSPECTION" {
		// Recovery may be adopted into a newer Core epoch while the detached
		// canonical source claim remains at its immutable source epoch.
		if _, err = tx.ExecContext(ctx, `DELETE FROM project_inspection_agent_claims WHERE inspection_id=? AND run_id=? AND runtime_epoch=?`, v.Spec.OperationID, v.Spec.RunID, v.Spec.RuntimeEpoch); err != nil {
			return v, receipt, err
		}
	} else if _, err = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND operation_id=?`, v.Spec.RunID, v.Spec.OperationID); err != nil {
		return v, receipt, err
	}
	if err = finalizeRecoveryUsageTx(ctx, tx, v.RecoveryRunID, now); err != nil {
		return v, receipt, err
	}
	if err = reconcileCanceledRecoveryTaskTx(ctx, tx, v.Spec.TaskID, now); err != nil {
		return v, receipt, err
	}
	return v, receipt, tx.Commit()
}
