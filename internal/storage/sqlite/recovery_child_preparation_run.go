package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

// createRecoveryChildPreparationRunTx is called only by CreatePreparationRun.
// The source Run contributes owner provenance, never actual workspace facts.
func createRecoveryChildPreparationRunTx(ctx context.Context, db *DB, tx *sql.Tx, recoveryID, planID, runID, requestID, payloadSHA256 string, now time.Time) (run.Run, error) {
	store := NewRecoveryStatusStore(db)
	v, err := recoveryReadTx(ctx, tx, recoveryID)
	if err != nil {
		return run.Run{}, err
	}
	// Freshness was checked when admission was committed. An explicit retry
	// re-observes the unchanged source before reusing these frozen facts.
	if v.State != "PREPARED" || v.RecoveryRunID != "" || v.RuntimeEpoch != db.epoch || !v.Admission.SourceTurnTerminal || !v.Admission.ThreadIdle || !v.Admission.NoPendingRequests || !v.Admission.Quiesced || !json.Valid(v.Admission.Before) || v.Admission.ObservedAt.IsZero() {
		return run.Run{}, rs.ErrIneligible
	}
	var admittedPlan, admittedRequest, admittedPayload string
	err = tx.QueryRowContext(ctx, `SELECT child_plan_id,admission_request_id,admission_payload_sha256 FROM recovery_child_admissions WHERE recovery_id=?`, recoveryID).Scan(&admittedPlan, &admittedRequest, &admittedPayload)
	if err != nil || admittedPlan != planID || admittedRequest != requestID || admittedPayload != payloadSHA256 {
		return run.Run{}, rs.ErrConflict
	}
	source, _, err := store.validateSourceTx(ctx, tx, v.Spec.Source)
	if err != nil {
		return run.Run{}, err
	}
	if source.RuntimeWorkset == nil || source.RuntimeWorkset.ContentHash != v.Spec.RuntimeWorksetHash {
		return run.Run{}, rs.ErrConflict
	}
	var attempt int
	err = tx.QueryRowContext(ctx, `UPDATE tasks SET next_attempt=MAX(next_attempt,(SELECT COALESCE(MAX(attempt),0) FROM runs WHERE task_id=?))+1 WHERE id=? RETURNING next_attempt`, source.TaskID, source.TaskID).Scan(&attempt)
	if err != nil {
		return run.Run{}, err
	}
	v.RecoveryRunID = runID
	v.UpdatedAt = now.UTC()
	if err = saveRecoveryTx(ctx, tx, v); err != nil {
		return run.Run{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,task_id,attempt,status,created_at,updated_at,purpose,execution_proposal_id,codex_instruction,review_scope_kind,review_scope_paths_json,review_mode,selected_modules_json,review_id,context_probe_id,runtime_epoch,agent_execution_id)
 SELECT ?,project_id,feature_area_id,operation_kind,operation_id,task_id,?,'CREATED',?,?,'RECOVERY_STATUS',execution_proposal_id,?,review_scope_kind,review_scope_paths_json,review_mode,selected_modules_json,review_id,CASE WHEN ?='PROJECT_INSPECTION' THEN NULL ELSE context_probe_id END,?,agent_execution_id FROM runs WHERE id=?`, runID, attempt, formatTime(now), formatTime(now), v.Spec.Instruction, v.Spec.OwnerKind, v.RuntimeEpoch, source.ID)
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_token_usage(run_id,status,updated_at) VALUES(?,'PENDING',?)`, runID, formatTime(now)); err != nil {
		return run.Run{}, conflict(err)
	}
	v.State = "RUN_CREATED"
	if err = saveRecoveryTx(ctx, tx, v); err != nil {
		return run.Run{}, err
	}
	created, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, runID))
	if err != nil {
		return run.Run{}, err
	}
	return created, nil
}
