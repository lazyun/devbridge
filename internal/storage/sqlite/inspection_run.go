package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/run"
)

func (s *ContextProbeStore) syncInspectionRunTx(ctx context.Context, tx *sql.Tx, p *contextprobe.Probe) error {
	if p.Status == contextprobe.StatusRunning && p.RunID == "" {
		p.RunID = "inspection_" + p.ID
		p.RuntimeEpoch = s.db.epoch
		v := run.Run{ID: p.RunID, TaskID: p.TaskID, ProjectID: p.ProjectID, OperationKind: run.OperationInspection, OperationID: p.ID, CreatedAt: p.CreatedAt}
		if err := insertRunAgentExecutionTx(ctx, tx, &v); err != nil {
			return err
		}
		p.AgentExecutionID = v.AgentExecutionID
		_, err := tx.ExecContext(ctx, `INSERT INTO runs(id,task_id,project_id,attempt,status,purpose,operation_kind,operation_id,context_probe_id,runtime_epoch,created_at,updated_at,started_at,agent_execution_id)
 VALUES(?,?,?,1,'RUNNING','INSPECTION','INSPECTION',?,?,?,?,?,?,?)`, p.RunID, p.TaskID, p.ProjectID, p.ID, p.ID, p.RuntimeEpoch, formatTime(p.CreatedAt), formatTime(p.UpdatedAt), nullableTime(p.StartedAt), v.AgentExecutionID)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO run_token_usage(run_id,status,updated_at) VALUES(?,'PENDING',?)`, p.RunID, formatTime(p.UpdatedAt)); err != nil {
			return err
		}
		linked, err := tx.ExecContext(ctx, `UPDATE context_probes SET run_id=?,runtime_epoch=? WHERE id=? AND run_id IS NULL`, p.RunID, p.RuntimeEpoch, p.ID)
		if err != nil {
			return err
		}
		if affected, err := linked.RowsAffected(); err != nil || affected != 1 {
			return contextprobe.ErrConflict
		}
	}
	if p.RunID == "" {
		return nil
	}
	status := string(p.Status)
	if status == "NO_GUIDANCE" {
		status = "SUCCEEDED"
	}
	updated, err := tx.ExecContext(ctx, `UPDATE runs SET status=?,thread_id=?,turn_id=?,result_text=?,finished_at=?,updated_at=? WHERE id=? AND task_id=? AND project_id=? AND context_probe_id=? AND operation_kind='INSPECTION' AND operation_id=? AND runtime_epoch=?`, status, nullString(p.ThreadID), nullString(p.TurnID), nullString(p.Result), nullableTime(p.FinishedAt), formatTime(p.UpdatedAt), p.RunID, p.TaskID, p.ProjectID, p.ID, p.ID, p.RuntimeEpoch)
	if err != nil {
		return err
	}
	if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
		return contextprobe.ErrConflict
	}
	// Token accounting remains canonical in the Probe while its Run exposes
	// availability. No fabricated counts or accounting receipts.
	updated, err = tx.ExecContext(ctx, `UPDATE run_token_usage SET status=?,updated_at=? WHERE run_id=?`, inspectionAccountingStatus(p.TokenUsage.Status), formatTime(p.UpdatedAt), p.RunID)
	if err != nil {
		return err
	}
	if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
		return contextprobe.ErrConflict
	}
	return nil
}

// Probe accounting remains the sole source for inspection token counts. Its Run
// does not add a second counted usage row; Task.Work projects Probe availability.
func inspectionAccountingStatus(status run.TokenUsageStatus) run.TokenUsageStatus {
	if status == run.TokenUsageAvailable {
		return run.TokenUsageUnavailable
	}
	return status
}
