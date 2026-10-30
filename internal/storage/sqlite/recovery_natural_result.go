package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

// SaveNaturalExecution records the diagnostic Turn's completion only. Its
// source claim and UNKNOWN response disposition remain owned by recovery.
func (s *RecoveryStatusStore) SaveNaturalExecution(ctx context.Context, id, epoch string, raw, after, captured []byte) (rs.Record, error) {
	return s.mutate(ctx, id, epoch, func(tx *sql.Tx, v *rs.Record) error {
		if v.State != "RUNNING" || len(v.Report) > 0 || !json.Valid(after) || len(raw) > 1<<20 {
			return rs.ErrConflict
		}
		child, _, err := readRecoveryDiagnosticEvidenceTx(ctx, tx, *v, false)
		if err != nil {
			return err
		}
		if child.ResultProtocol != run.NaturalResultProtocol {
			return rs.ErrConflict
		}
		if _, err = recoveryRequestDispositionTx(ctx, tx, child.ID, false); err != nil {
			return err
		}
		now := time.Now().UTC()
		var ev run.ExecutionEvidence
		if len(captured) > 16<<20 || json.Unmarshal(captured, &ev) != nil || !bytes.Equal(ev.RecoveryAfter, after) {
			return rs.ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `UPDATE run_execution_records SET commands_json=? WHERE run_id=? AND commands_json IS NULL`, string(captured), child.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE runs SET result_text=? WHERE id=? AND status='RUNNING'`, string(raw), child.ID); err != nil {
			return err
		}
		if err = finishRecoveryDiagnosticTx(ctx, tx, v, now); err != nil {
			return err
		}
		v.CompletedAt = &now
		v.AnalysisRequired = true
		payload, _ := event.Payload(map[string]any{"status": "SUCCEEDED", "analysisState": "REQUIRED"})
		return appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: child.ProjectID, TaskID: child.TaskID, RunID: child.ID, EntityType: "run", EntityID: child.ID, Payload: payload, CreatedAt: now})
	})
}
