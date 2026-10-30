package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// advanceTaskPhaseTx is shared by the canonical admission/completion and
// decision transactions. It fences the exact operation, never history counts.
func advanceTaskPhaseTx(ctx context.Context, tx *sql.Tx, id string, from, to task.Phase, ownerColumn, owner string, now time.Time) error {
	if !from.Valid() || !to.Valid() || (ownerColumn != "current_review_id" && ownerColumn != "current_execution_id") {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase=?,updated_at=? WHERE id=? AND status='ACTIVE' AND phase=? AND `+ownerColumn+`=?`, string(to), formatTime(now), id, string(from), owner)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("Task phase or current operation changed: %w", ErrConflict)
	}
	return nil
}

func startTaskRunPhaseTx(ctx context.Context, tx *sql.Tx, r run.Run, now time.Time) error {
	if r.OperationKind != run.OperationExecution {
		return nil
	}
	from, to := task.PhaseImplementationReady, task.PhaseImplementing
	if r.Purpose == run.PurposeImplementation {
		var persisted task.Phase
		if err := tx.QueryRowContext(ctx, `SELECT phase FROM tasks WHERE id=?`, r.TaskID).Scan(&persisted); err != nil {
			return err
		}
		if persisted == task.PhaseImplementationPreparing {
			var successor bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM no_run_successor_effect_authorities a JOIN tasks t ON t.id=a.task_id WHERE a.successor_execution_id=? AND a.task_id=? AND t.current_execution_id=a.successor_execution_id AND t.phase='IMPLEMENTATION_PREPARING')`, r.ExecutionProposalID, r.TaskID).Scan(&successor); err != nil {
				return err
			}
			if !successor {
				return ErrConflict
			}
			from = task.PhaseImplementationPreparing
		}
	}
	if r.Purpose == run.PurposeVerification {
		from, to = task.PhaseVerificationReady, task.PhaseVerifying
	}
	return advanceTaskPhaseTx(ctx, tx, r.TaskID, from, to, "current_execution_id", r.ExecutionProposalID, now)
}

func completeTaskRunPhaseTx(ctx context.Context, tx *sql.Tx, r run.Run) error {
	if r.OperationKind != run.OperationExecution || r.Status != run.StatusSucceeded {
		return nil
	}
	var phase task.Phase
	var current, status string
	if err := tx.QueryRowContext(ctx, `SELECT phase,COALESCE(current_execution_id,''),status FROM tasks WHERE id=?`, r.TaskID).Scan(&phase, &current, &status); err != nil {
		return err
	}
	if current != r.ExecutionProposalID || status != "ACTIVE" {
		return nil
	}
	switch r.Purpose {
	case run.PurposeImplementation:
		if phase != task.PhaseImplementing {
			return nil
		}
		next := task.PhaseVerificationReady
		var encoded string
		if err := tx.QueryRowContext(ctx, `SELECT verification_spec_json FROM execution_proposals WHERE id=? AND task_id=?`, current, r.TaskID).Scan(&encoded); err != nil {
			return err
		}
		spec, err := decodeVerificationSpec(encoded)
		if err != nil {
			return err
		}
		if spec.Mode == task.VerificationModeNone && spec.Waiver != nil && spec.Waiver.Source == task.VerificationWaiverUser && spec.Waiver.Reason != "" {
			next = task.PhaseAcceptancePending
		}
		return advanceTaskPhaseTx(ctx, tx, r.TaskID, phase, next, "current_execution_id", current, r.UpdatedAt)
	case run.PurposeVerification:
		if phase != task.PhaseVerifying || r.VerificationReport == nil {
			return nil
		}
		return advanceTaskPhaseTx(ctx, tx, r.TaskID, phase, task.PhaseAcceptancePending, "current_execution_id", current, r.UpdatedAt)
	}
	return nil
}
