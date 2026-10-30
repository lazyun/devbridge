package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/contextprobe"
)

// ConfirmCanonicalInspection is the sole creation point for a new canonical
// Project Inspection.  It creates its existing PROJECT Task and probe receipt
// together, before any workspace, Run, claim, conversation, or transport work.
func (s *ContextProbeStore) ConfirmCanonicalInspection(ctx context.Context, in contextprobe.ConfirmedInspection) (contextprobe.Probe, error) {
	v := in.Probe
	if v.ID == "" || v.ProjectID == "" || v.AISessionID == "" || v.RepositoryID == "" || v.CodexConfigID == "" || v.CanonicalRequestID == "" || v.CanonicalPayloadSHA256 == "" || in.ReceiptJSON == "" {
		return contextprobe.Probe{}, contextprobe.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	defer tx.Rollback()
	var existingID, payload string
	err = tx.QueryRowContext(ctx, `SELECT id,canonical_payload_hash FROM context_probes WHERE project_id=? AND canonical_request_id=?`, v.ProjectID, v.CanonicalRequestID).Scan(&existingID, &payload)
	if err == nil {
		if payload != v.CanonicalPayloadSHA256 {
			return contextprobe.Probe{}, contextprobe.ErrConflict
		}
		return scanContextProbe(tx.QueryRowContext(ctx, selectContextProbe+` WHERE id=?`, existingID))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return contextprobe.Probe{}, err
	}
	paths, err := encodeProbePaths(v.Scope.Paths)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	taskID, err := insertWorkEnvelopeTx(ctx, tx, "INSPECTION", v.ID, v.ProjectID, "", v.RepositoryID, v.CodexConfigID, v.CodexInstruction, "", "", "", v.CreatedAt)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if taskID != v.TaskID {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO context_probes(id,ai_session_id,project_id,repository_id,codex_config_id,scope_kind,scope_paths_json,codex_instruction,status,token_usage_status,created_at,updated_at,task_id,canonical_target_receipt_json,canonical_request_id,canonical_payload_hash,primary_member_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.AISessionID, v.ProjectID, v.RepositoryID, v.CodexConfigID, v.Scope.Kind, paths, v.CodexInstruction, v.Status, v.TokenUsage.Status, formatTime(v.CreatedAt), formatTime(v.UpdatedAt), taskID, in.ReceiptJSON, v.CanonicalRequestID, v.CanonicalPayloadSHA256, v.PrimaryMemberID)
	if err != nil {
		return contextprobe.Probe{}, conflict(err)
	}
	if err = tx.Commit(); err != nil {
		return contextprobe.Probe{}, err
	}
	return s.Get(ctx, v.ID)
}
