package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

// RepairResolvedPurpose creates only the missing canonical intent/link for a
// legacy resolved revision-required Recovery. It preserves the original
// assessment, report, resolution and Task ownership.
func (s *RecoveryStatusStore) RepairResolvedPurpose(ctx context.Context, in rs.RepairPurposeInput) (rs.PurposeRepairReceipt, error) {
	var out rs.PurposeRepairReceipt
	if in.RecoveryID == "" || in.RequestID == "" || in.PayloadHash == "" || in.PayloadHash != in.ComputePayloadHash() || in.Proposal.RecoveryID != in.RecoveryID {
		return out, rs.ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	record, err := recoveryReadTx(ctx, tx, in.RecoveryID)
	if err != nil {
		return out, err
	}
	if record.State != "RESOLVED" || record.Spec.OwnerKind == "PROJECT_INSPECTION" || len(record.Resolution) == 0 {
		return out, rs.ErrIneligible
	}
	var assessment rs.Assessment
	if json.Unmarshal(record.Assessment, &assessment) != nil || assessment.Consistency != "CONSISTENT" || assessment.EngineeringDisposition != "REVISION_REQUIRED" || in.Proposal.Validate(record.Spec.Source, record.ID, record.Spec.Generation, record.AssessmentHash) != nil {
		return out, rs.ErrIneligible
	}
	var requestID, payloadHash, intentID string
	err = tx.QueryRowContext(ctx, `SELECT repair_request_id,repair_payload_hash,intent_id FROM recovery_purpose_repair_receipts WHERE recovery_id=? AND recovery_generation=?`, record.ID, record.Spec.Generation).Scan(&requestID, &payloadHash, &intentID)
	if err == nil {
		if requestID != in.RequestID || payloadHash != in.PayloadHash {
			return out, rs.ErrConflict
		}
		return rs.PurposeRepairReceipt{RecoveryID: record.ID, RecoveryGeneration: record.Spec.Generation, RequestID: requestID, PayloadHash: payloadHash, IntentID: intentID, Replay: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var linked int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_links WHERE recovery_id=? AND recovery_generation=?)`, record.ID, record.Spec.Generation).Scan(&linked); err != nil {
		return out, err
	}
	if linked != 0 {
		return out, rs.ErrConflict
	}
	source, _, err := s.validateSourceTx(ctx, tx, record.Spec.Source)
	if err != nil {
		return out, err
	}
	// The same transaction-local canonical Prepare helper and source validation
	// are used by the ordinary Resolve transition. Only its state guard sees a
	// local ASSESSED copy; the stored Recovery remains RESOLVED throughout.
	prepared := record
	prepared.State = "ASSESSED"
	if err = s.resolveRevisionRequiredTx(ctx, tx, &prepared, source, rs.ResolveInput{Purpose: &in.Proposal}); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT intent_id FROM recovery_purpose_confirmation_links WHERE recovery_id=? AND recovery_generation=?`, record.ID, record.Spec.Generation).Scan(&intentID); err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_purpose_repair_receipts(recovery_id,recovery_generation,repair_request_id,repair_payload_hash,intent_id,repaired_at) VALUES(?,?,?,?,?,?)`, record.ID, record.Spec.Generation, in.RequestID, in.PayloadHash, intentID, formatTime(time.Now().UTC()))
	if err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	return rs.PurposeRepairReceipt{RecoveryID: record.ID, RecoveryGeneration: record.Spec.Generation, RequestID: in.RequestID, PayloadHash: in.PayloadHash, IntentID: intentID}, nil
}
