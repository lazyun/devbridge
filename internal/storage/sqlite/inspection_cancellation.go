package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
)

// Inspection owns a context_probe rather than a FeatureArea writer claim. It
// uses the same durable cancellation ledger, with its own exact owner fence.
func (s *ExecutionCancellationStore) beginInspectionCancellation(ctx context.Context, in executioncancel.Input, live bool) (executioncancel.Receipt, bool, error) {
	empty := executioncancel.Receipt{}
	if in.ExpectedUpperVersion != nil {
		return empty, false, executioncancel.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	if err = cancellationActorTx(ctx, tx, in); err != nil {
		return empty, false, err
	}
	prior, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, in.ProjectID, in.RequestID))
	if err == nil {
		if prior.OwnerType != in.OwnerType || prior.OwnerID != in.OwnerID || prior.RunID != in.RunID || prior.Reason != in.Reason || prior.RequestedByAISessionID != in.AISessionID || prior.RequestedByOperatorID != in.OperatorPrincipalID {
			return empty, false, executioncancel.ErrRequestConflict
		}
		return prior, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, false, err
	}
	var project, taskID, runID, epoch, status string
	err = tx.QueryRowContext(ctx, `SELECT p.project_id,p.task_id,p.run_id,p.runtime_epoch,p.status FROM context_probes p JOIN runs r ON r.id=p.run_id AND r.context_probe_id=p.id AND r.operation_id=p.id AND r.operation_kind='INSPECTION' AND r.runtime_epoch=p.runtime_epoch WHERE p.id=?`, in.OwnerID).Scan(&project, &taskID, &runID, &epoch, &status)
	if err != nil {
		return empty, false, executioncancel.ErrRunNotActive
	}
	if project != in.ProjectID || runID != in.RunID {
		return empty, false, executioncancel.ErrRunExecutionMismatch
	}
	state, disposition := "CANCEL_REQUESTED", "ACTIVE"
	var resolved any
	now := time.Now().UTC()
	if status != "RUNNING" {
		state, disposition, resolved = "ALREADY_TERMINAL", "ACTIVE", formatTime(now)
	} else if !live || epoch != s.db.epoch {
		return empty, false, executioncancel.ErrRecoveryRequired
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_cancellations WHERE run_id=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))`, runID).Scan(&pending); err != nil {
		return empty, false, err
	}
	if pending {
		return empty, false, executioncancel.ErrRequestConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_cancellations(project_id,request_id,run_id,requested_by_ai_session_id,requested_by_operator_id,reason,requested_at,runtime_epoch,state,interrupt_state,resolved_at,owner_type,owner_id,task_id,backend_disposition) VALUES(?,?,?,?,?,?,?,?,?,'NOT_SENT',?,'INSPECTION',?,?,?)`, project, in.RequestID, runID, nullString(in.AISessionID), nullString(in.OperatorPrincipalID), in.Reason, formatTime(now), epoch, state, resolved, in.OwnerID, taskID, disposition)
	if err != nil {
		return empty, false, err
	}
	value, err := scanCancellation(tx.QueryRowContext(ctx, selectCancellation+` WHERE c.project_id=? AND c.request_id=?`, project, in.RequestID))
	if err != nil {
		return empty, false, err
	}
	if err = tx.Commit(); err != nil {
		return empty, false, err
	}
	return value, state == "CANCEL_REQUESTED", nil
}

// FinishInspectionCancellation records only an observed exact terminal Turn.
// Process closure or an interrupt ACK alone never establishes quiescence.
func (s *ExecutionCancellationStore) FinishInspectionCancellation(ctx context.Context, runID string, terminal bool) error {
	var canonical bool
	if err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM inspection_runtime_records WHERE run_id=?)`, runID).Scan(&canonical); err != nil {
		return err
	}
	if canonical {
		return executioncancel.ErrRecoveryRequired
	}
	state, disposition := "RECOVERY_REQUIRED", "ACTIVE"
	var resolved any
	if terminal {
		state, disposition, resolved = "CANCELED", "TERMINATED", formatTime(time.Now().UTC())
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE execution_cancellations SET state=?,backend_disposition=?,terminal_observed=?,resolved_at=? WHERE run_id=? AND owner_type='INSPECTION' AND runtime_epoch=? AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')`, state, disposition, terminal, resolved, runID, s.db.epoch)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); terminal && changed > 0 {
		if err = cancelRuntimeRequestsTx(ctx, tx, runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
