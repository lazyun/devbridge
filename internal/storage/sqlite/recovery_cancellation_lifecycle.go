package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/task"
)

type settledRecoveryCancellation struct {
	RecoveryID   string `json:"recoveryId"`
	ProjectID    string `json:"projectId"`
	RequestID    string `json:"requestId"`
	OwnerType    string `json:"ownerType"`
	OwnerID      string `json:"ownerId"`
	CurrentOwner bool   `json:"currentOwner"`
}

// A FAILED record alone does not distinguish a failed diagnostic from an
// explicitly canceled owner. Reuse the full durable cancellation proof used
// by runtime authority, including the separate no-backend preparation branch.
func settledRecoveryCancellationsTx(ctx context.Context, tx definitionQuerier, taskID string) ([]settledRecoveryCancellation, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_group_array(json_object('id',s.id,'sourceRunId',s.source_run_id,'record',s.record_json,
 'currentOwner',CASE WHEN (r.operation_kind='EXECUTION' AND r.operation_id=t.current_execution_id)
 OR (r.operation_kind='REVIEW' AND r.operation_id=t.current_review_id) THEN 1 ELSE 0 END)),'[]')
 FROM recovery_status_records s JOIN runs r ON r.id=s.source_run_id JOIN tasks t ON t.id=r.task_id
 WHERE t.id=? AND s.state='FAILED' AND json_extract(s.record_json,'$.failureReason')='RECOVERY_CANCELED'
 AND EXISTS(SELECT 1 FROM recovery_child_admissions a WHERE a.recovery_id=s.id)`, taskID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var records []struct {
		ID, SourceRunID, Record string
		CurrentOwner            int
	}
	if err = json.Unmarshal([]byte(raw), &records); err != nil {
		return nil, err
	}
	out := []settledRecoveryCancellation{}
	for _, row := range records {
		var v rs.Record
		if err = json.Unmarshal([]byte(row.Record), &v); err != nil {
			return nil, err
		}
		if v.ID != row.ID || v.Spec.RunID != row.SourceRunID || v.Spec.TaskID != taskID || v.State != "FAILED" || v.FailureReason != "RECOVERY_CANCELED" {
			continue
		}
		if err = validateIndependentRecoveryCancellationTx(ctx, tx, v); err != nil {
			if errors.Is(err, rs.ErrIneligible) || errors.Is(err, rs.ErrConflict) || errors.Is(err, sql.ErrNoRows) {
				continue // Incomplete or mismatched evidence remains a live fence.
			}
			return nil, err
		}
		var receipt executioncancel.Receipt
		if err = json.Unmarshal(v.Resolution, &receipt); err != nil {
			return nil, err
		}
		out = append(out, settledRecoveryCancellation{v.ID, receipt.ProjectID, receipt.RequestID, receipt.OwnerType, receipt.OwnerID, row.CurrentOwner == 1})
	}
	return out, nil
}

func reconcileCanceledRecoveryTaskTx(ctx context.Context, tx *sql.Tx, taskID string, now time.Time) error {
	settled, err := settledRecoveryCancellationsTx(ctx, tx, taskID)
	if err != nil || len(settled) == 0 {
		return err
	}
	raw, err := json.Marshal(settled)
	if err != nil {
		return err
	}
	var uncertain bool
	if err = tx.QueryRowContext(ctx, `SELECT `+taskRecoveryUncertainSQL+` FROM tasks t WHERE t.id=?`, string(raw), string(raw), taskID).Scan(&uncertain); err != nil || uncertain {
		return err
	}
	var phase task.Phase
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status,phase FROM tasks WHERE id=?`, taskID).Scan(&status, &phase); err != nil || status != "ACTIVE" || phase != task.PhaseRecoveryRequired {
		return err
	}
	// Do not choose between two independently canceled current owners. Such a
	// mixed history needs an explicit continuation, not an inferred latest Run.
	var owner *settledRecoveryCancellation
	for _, candidate := range settled {
		if !candidate.CurrentOwner {
			continue
		}
		if owner != nil && (candidate.OwnerType != owner.OwnerType || candidate.OwnerID != owner.OwnerID) {
			return nil
		}
		value := candidate
		owner = &value
	}
	if owner == nil {
		return nil
	}
	next, column := task.PhaseAcceptancePending, "current_execution_id"
	if owner.OwnerType == "REVIEW" {
		next, column = task.PhaseReviewRequired, "current_review_id"
	}
	return advanceTaskPhaseTx(ctx, tx, taskID, phase, next, column, owner.OwnerID, now)
}
