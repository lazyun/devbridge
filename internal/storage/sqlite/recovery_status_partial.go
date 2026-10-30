package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"time"
)

// resolvePartialTx closes only the interrupted engineering attempt for RECOVERED_PARTIAL.
// It creates neither a successor nor a revision. The existing retry/acceptance protocol
// remains responsible for explicit continuation and revised authorization.
// REVISION_REQUIRED is handled separately by resolveRevisionRequiredTx.
func (s *RecoveryStatusStore) resolvePartialTx(ctx context.Context, tx *sql.Tx, v *rs.Record, source run.Run) error {
	var assessment rs.Assessment
	if json.Unmarshal(v.Assessment, &assessment) != nil || assessment.Consistency != "CONSISTENT" {
		return rs.ErrIneligible
	}
	if source.OperationKind != run.OperationExecution || source.Purpose != run.PurposeImplementation {
		return rs.ErrIneligible
	}
	if assessment.EngineeringDisposition != "RECOVERED_PARTIAL" {
		return rs.ErrIneligible
	}
	code := "RECOVERY_PARTIAL"
	observedAt := time.Now().UTC()
	now := formatTime(observedAt)
	changed, err := tx.ExecContext(ctx, `UPDATE runs SET status='FAILED',failure_class=COALESCE(failure_class,'TASK_FAILED'),failure_code=COALESCE(failure_code,?),failure_stage=COALESCE(failure_stage,'engineering recovery'),failure_message=COALESCE(failure_message,?),finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND turn_id=? AND status IN ('RUNNING','COLLECTING','FAILED','INFRASTRUCTURE_FAILED')`, code, code, now, now, source.ID, source.TurnID)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return rs.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome='FAILED',failure_code=?,failure_message=?,updated_at=? WHERE id=? AND task_id=? AND outcome IN ('RUNNING','FAILED')`, code, code, now, source.ExecutionProposalID, source.TaskID); err != nil {
		return err
	}
	if err = finishRecoveryDiagnosticTx(ctx, tx, v, observedAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=? AND operation_id=? AND operation_kind=?`, source.ID, source.OperationID, source.OperationKind); err != nil {
		return err
	}
	// The frozen Recovery Matrix targets TASK_IMPLEMENTING for IMPLEMENTATION|PARTIAL only.
	// Advance the Task Phase in the same transaction as the RecoveryStatus RESOLVED write.
	if source.Purpose == run.PurposeImplementation {
		if err = advanceTaskPhaseTx(ctx, tx, source.TaskID, task.PhaseRecoveryRequired, task.PhaseImplementing, "current_execution_id", source.ExecutionProposalID, observedAt); err != nil {
			return err
		}
	}
	return nil
}
