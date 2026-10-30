package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

type RecoveryChildThreadClaim = rs.ChildThreadClaim

// ClaimRecoveryChildThread is the durable pre-RPC fence. An existing claim is
// a readback obligation, even when no response receipt exists yet.
func (s *RecoveryStatusStore) ClaimRecoveryChildThread(ctx context.Context, in RecoveryChildThreadClaim, now time.Time) (bool, error) {
	if in.RecoveryID == "" || in.ChildRunID == "" || in.WorksetHash == "" || in.WorkspaceID == "" || in.RequestID == "" || len(in.PayloadSHA256) != 64 || in.RuntimeEpoch != s.db.epoch {
		return false, rs.ErrStale
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, in.ChildRunID); err != nil {
		return false, err
	}
	var saved RecoveryChildThreadClaim
	err = tx.QueryRowContext(ctx, `SELECT recovery_id,child_run_id,child_runtime_workset_hash,child_workspace_id,runtime_epoch,request_id,payload_sha256 FROM recovery_child_thread_attempts WHERE recovery_id=? OR request_id=?`, in.RecoveryID, in.RequestID).Scan(&saved.RecoveryID, &saved.ChildRunID, &saved.WorksetHash, &saved.WorkspaceID, &saved.RuntimeEpoch, &saved.RequestID, &saved.PayloadSHA256)
	if err == nil {
		originalEpoch := saved.RuntimeEpoch
		saved.RuntimeEpoch = in.RuntimeEpoch
		exactEpoch, epochErr := recoveryDiagnosticEpochChainTx(ctx, tx, in.RecoveryID, in.ChildRunID, originalEpoch, in.RuntimeEpoch)
		if epochErr != nil {
			return false, epochErr
		}
		if saved != in || !exactEpoch {
			return false, rs.ErrConflict
		}
		return true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_thread_attempts(recovery_id,child_run_id,child_runtime_workset_hash,child_workspace_id,runtime_epoch,request_id,payload_sha256,attempted_at) VALUES(?,?,?,?,?,?,?,?)`, in.RecoveryID, in.ChildRunID, in.WorksetHash, in.WorkspaceID, in.RuntimeEpoch, in.RequestID, in.PayloadSHA256, formatTime(now))
	if err != nil {
		return false, rs.ErrConflict
	}
	return false, tx.Commit()
}

type RecoveryChildThreadReceipt = rs.ChildThreadReceipt

func (s *RecoveryStatusStore) ReadRecoveryChildThread(ctx context.Context, requestID string) (RecoveryChildThreadReceipt, bool, error) {
	var v RecoveryChildThreadReceipt
	err := s.db.sql.QueryRowContext(ctx, `SELECT request_id,payload_sha256,outcome,COALESCE(child_thread_id,''),app_server_instance_id,observed_binding_hash,failure_code FROM recovery_child_thread_receipts WHERE request_id=?`, requestID).Scan(&v.RequestID, &v.PayloadSHA256, &v.Outcome, &v.ChildThreadID, &v.AppServerInstanceID, &v.BindingHash, &v.FailureCode)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (s *RecoveryStatusStore) RecordRecoveryChildThread(ctx context.Context, in RecoveryChildThreadReceipt, now time.Time) error {
	if in.RequestID == "" || len(in.PayloadSHA256) != 64 || in.AppServerInstanceID == "" {
		return rs.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var saved RecoveryChildThreadReceipt
	err = tx.QueryRowContext(ctx, `SELECT request_id,payload_sha256,outcome,COALESCE(child_thread_id,''),app_server_instance_id,observed_binding_hash,failure_code FROM recovery_child_thread_receipts WHERE request_id=?`, in.RequestID).Scan(&saved.RequestID, &saved.PayloadSHA256, &saved.Outcome, &saved.ChildThreadID, &saved.AppServerInstanceID, &saved.BindingHash, &saved.FailureCode)
	if err == nil {
		if saved != in {
			return rs.ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_thread_receipts(request_id,payload_sha256,outcome,child_thread_id,app_server_instance_id,observed_binding_hash,failure_code,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, in.RequestID, in.PayloadSHA256, in.Outcome, nullString(in.ChildThreadID), in.AppServerInstanceID, in.BindingHash, in.FailureCode, formatTime(now))
	if err != nil {
		return rs.ErrConflict
	}
	return tx.Commit()
}

type RecoveryChildTurnClaim = rs.ChildTurnClaim

func (s *RecoveryStatusStore) ClaimRecoveryChildTurn(ctx context.Context, in RecoveryChildTurnClaim, now time.Time) (bool, error) {
	if in.RecoveryID == "" || in.ThreadRequestID == "" || in.ChildThreadID == "" || in.RequestID == "" || len(in.PayloadSHA256) != 64 {
		return false, rs.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var childRunID string
	if err = tx.QueryRowContext(ctx, `SELECT child_run_id FROM recovery_child_thread_attempts WHERE recovery_id=? AND request_id=?`, in.RecoveryID, in.ThreadRequestID).Scan(&childRunID); err != nil {
		return false, rs.ErrConflict
	}
	if err = s.db.checkRunTx(ctx, tx, childRunID); err != nil {
		return false, err
	}
	var saved RecoveryChildTurnClaim
	err = tx.QueryRowContext(ctx, `SELECT recovery_id,thread_request_id,child_thread_id,request_id,payload_sha256 FROM recovery_child_turn_attempts WHERE recovery_id=? OR request_id=?`, in.RecoveryID, in.RequestID).Scan(&saved.RecoveryID, &saved.ThreadRequestID, &saved.ChildThreadID, &saved.RequestID, &saved.PayloadSHA256)
	if err == nil {
		if saved != in {
			return false, rs.ErrConflict
		}
		return true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_turn_attempts(recovery_id,thread_request_id,child_thread_id,request_id,payload_sha256,attempted_at) VALUES(?,?,?,?,?,?)`, in.RecoveryID, in.ThreadRequestID, in.ChildThreadID, in.RequestID, in.PayloadSHA256, formatTime(now))
	if err != nil {
		return false, rs.ErrConflict
	}
	return false, tx.Commit()
}

type RecoveryChildTurnReceipt = rs.ChildTurnReceipt

func (s *RecoveryStatusStore) ReadRecoveryChildTurn(ctx context.Context, requestID string) (RecoveryChildTurnReceipt, bool, error) {
	var v RecoveryChildTurnReceipt
	err := s.db.sql.QueryRowContext(ctx, `SELECT request_id,payload_sha256,outcome,child_thread_id,COALESCE(child_turn_id,''),dispatch_attempt_id,failure_code FROM recovery_child_turn_receipts WHERE request_id=?`, requestID).Scan(&v.RequestID, &v.PayloadSHA256, &v.Outcome, &v.ChildThreadID, &v.ChildTurnID, &v.DispatchAttemptID, &v.FailureCode)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (s *RecoveryStatusStore) RecordRecoveryChildTurn(ctx context.Context, in RecoveryChildTurnReceipt, now time.Time) error {
	if in.RequestID == "" || len(in.PayloadSHA256) != 64 || in.ChildThreadID == "" || in.DispatchAttemptID == "" {
		return rs.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var saved RecoveryChildTurnReceipt
	err = tx.QueryRowContext(ctx, `SELECT request_id,payload_sha256,outcome,child_thread_id,COALESCE(child_turn_id,''),dispatch_attempt_id,failure_code FROM recovery_child_turn_receipts WHERE request_id=?`, in.RequestID).Scan(&saved.RequestID, &saved.PayloadSHA256, &saved.Outcome, &saved.ChildThreadID, &saved.ChildTurnID, &saved.DispatchAttemptID, &saved.FailureCode)
	if err == nil {
		if saved != in {
			return rs.ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_child_turn_receipts(request_id,payload_sha256,outcome,child_thread_id,child_turn_id,dispatch_attempt_id,failure_code,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, in.RequestID, in.PayloadSHA256, in.Outcome, in.ChildThreadID, nullString(in.ChildTurnID), in.DispatchAttemptID, in.FailureCode, formatTime(now))
	if err != nil {
		return rs.ErrConflict
	}
	return tx.Commit()
}
