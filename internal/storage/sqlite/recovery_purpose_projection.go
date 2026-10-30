package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

// ReadPurposeConfirmation projects the exact link, canonical intent, submission,
// purpose receipt and consumption from one read transaction. Legacy resolved
// records without a link are reported as repair-required; no intent search is
// performed.
func (s *RecoveryStatusStore) ReadPurposeConfirmation(ctx context.Context, recoveryID string) (rs.PurposeConfirmationProjection, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return rs.PurposeConfirmationProjection{}, err
	}
	defer tx.Rollback()
	var session, intentID, hash, owner string
	var version int
	var linkID string
	var ordinal, generation int
	err = tx.QueryRowContext(ctx, `SELECT h.intent_ai_session_id,h.intent_id,h.intent_version,h.intent_content_hash,h.source_owner_kind,h.link_id,h.link_ordinal,h.recovery_generation FROM recovery_purpose_current_links c JOIN recovery_purpose_link_history h ON h.link_id=c.link_id AND h.recovery_id=c.recovery_id AND h.recovery_generation=c.recovery_generation AND h.link_ordinal=c.link_ordinal WHERE c.recovery_id=?`, recoveryID).Scan(&session, &intentID, &version, &hash, &owner, &linkID, &ordinal, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return rs.PurposeConfirmationProjection{State: "REPAIR_REQUIRED", RepairRequired: true}, nil
	}
	if err != nil {
		return rs.PurposeConfirmationProjection{}, err
	}
	projection := rs.PurposeConfirmationProjection{AISessionID: session, IntentID: intentID, Version: version, ContentHash: hash, OwnerKind: owner, LinkID: linkID, LinkOrdinal: ordinal, RecoveryGeneration: generation}
	current, err := recoveryGenerationCurrentTx(ctx, tx, recoveryID, generation)
	if err != nil {
		return projection, err
	}
	projection.Historical = !current
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, intentID, session).Scan(&raw); err != nil {
		return rs.PurposeConfirmationProjection{}, err
	}
	var intent confirmation.Intent
	if err = json.Unmarshal([]byte(raw), &intent); err != nil {
		return rs.PurposeConfirmationProjection{}, err
	}
	if intent.IntentID != intentID || intent.AISessionID != session || intent.Version != version || intent.ContentHash != hash || intent.Purpose != confirmation.RecoveryPurpose || intent.Content.RecoveryPurpose == nil || intent.Content.RecoveryPurpose.Proposal.OwnerKind != owner {
		return rs.PurposeConfirmationProjection{}, rs.ErrConflict
	}
	projection.State = intent.State
	if intent.State == "PENDING" && intent.Commit == nil && !time.Now().Before(intent.ExpiresAt) {
		projection.State = "STALE"
	}
	var rejectionRequestID string
	if intent.Commit != nil {
		projection.SelectedVariantID = intent.Commit.VariantID
		submission, readErr := confirmationSubmissionTx(ctx, tx, intent, intent.Commit.RequestID)
		if readErr != nil {
			return rs.PurposeConfirmationProjection{}, readErr
		}
		if submission.Recorded {
			projection.SubmissionOutcome = string(submission.Outcome)
			if submission.Outcome == confirmation.RejectedNotConsumed {
				rejectionRequestID = submission.Request.RequestID
			}
		}
	} else {
		submission, found, readErr := latestConfirmationSubmissionTx(ctx, tx, intent)
		if readErr != nil {
			return rs.PurposeConfirmationProjection{}, readErr
		}
		if found {
			projection.SubmissionOutcome = string(submission.Outcome)
			if submission.Outcome == confirmation.RejectedNotConsumed {
				rejectionRequestID = submission.Request.RequestID
			}
		}
	}
	if rejectionRequestID != "" {
		if err = tx.QueryRowContext(ctx, `SELECT failure_code FROM recovery_purpose_rejection_receipts WHERE intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=? AND commit_request_id=?`, session, intentID, version, hash, rejectionRequestID).Scan(&projection.FailureCode); err != nil {
			return rs.PurposeConfirmationProjection{}, err
		}
	}
	var receiptOutcome string
	err = tx.QueryRowContext(ctx, `SELECT outcome FROM recovery_purpose_confirmation_receipts WHERE intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=?`, session, intentID, version, hash).Scan(&receiptOutcome)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return rs.PurposeConfirmationProjection{}, err
	}
	projection.ReceiptCommitted = receiptOutcome == "COMMITTED"
	var consumed int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions WHERE intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=?)`, session, intentID, version, hash).Scan(&consumed); err != nil {
		return rs.PurposeConfirmationProjection{}, err
	}
	projection.Consumed = consumed == 1
	if !projection.Historical && !projection.Consumed {
		failure, err := recoveryPurposeFenceTx(ctx, tx, intent, false)
		if err != nil {
			return projection, err
		}
		if failure != nil {
			projection.CurrentFailureCode = failure.Code
		}
	}
	return projection, nil
}
