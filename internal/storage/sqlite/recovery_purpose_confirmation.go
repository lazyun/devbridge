package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
)

// recoveryPurposeCommitFenceTx is the production form of
// confirmation.commit_recovery_purpose. It never evaluates a Review Decision.
func recoveryPurposeCommitFenceTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) (*confirmation.Failure, error) {
	return recoveryPurposeFenceTx(ctx, tx, intent, true)
}

func recoveryPurposeFenceTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, checkPreparedContext bool) (*confirmation.Failure, error) {
	body := intent.Content.RecoveryPurpose
	if body == nil || intent.Action != confirmation.ConfirmRecoveryPurpose || body.Proposal.SourceAISessionID != intent.AISessionID {
		return purposeCommitFailure("PURPOSE_INTENT_INCOMPLETE"), nil
	}
	p := body.Proposal
	if p.SubmittedHash != p.ComputeSubmittedHash() {
		return purposeCommitFailure("PURPOSE_PROPOSAL_CHANGED"), nil
	}
	var linked, current int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_link_history l JOIN recovery_status_records r ON r.id=l.recovery_id
	 JOIN recovery_purpose_current_links c ON c.recovery_id=l.recovery_id AND c.recovery_generation=l.recovery_generation AND c.link_id=l.link_id AND c.link_ordinal=l.link_ordinal
	 JOIN recovery_current_generations g ON g.recovery_id=l.recovery_id AND g.generation=l.recovery_generation AND g.owner_kind=l.source_owner_kind AND g.owner_id=l.source_owner_id
 WHERE l.recovery_id=? AND l.recovery_generation=? AND l.intent_ai_session_id=? AND l.intent_id=? AND l.intent_version=? AND l.intent_content_hash=?
 AND l.proposal_hash=? AND l.source_owner_kind=? AND l.source_owner_id=? AND l.task_id=? AND l.feature_area_id=?
 AND l.source_execution_id=? AND l.source_review_id=? AND l.task_definition_revision_id=? AND l.source_operation_id=? AND l.source_run_id=? AND l.source_turn_id=?
 AND l.runtime_epoch=? AND l.runtime_workset_hash=? AND l.assessment_hash=? AND r.state='RESOLVED' AND r.generation=l.recovery_generation)`,
		p.RecoveryID, p.RecoveryGeneration, intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash,
		p.SubmittedHash, p.OwnerKind, p.OwnerID, p.TaskID, p.FeatureAreaID, p.SourceExecutionID, p.SourceReviewID, p.TaskDefinitionRevisionID,
		p.SourceOperationID, p.SourceRunID, p.SourceTurnID, p.RuntimeEpoch, p.RuntimeWorksetHash, p.AssessmentHash).Scan(&linked)
	if err != nil {
		return nil, err
	}
	if linked != 1 {
		return purposeCommitFailure("RECOVERY_PURPOSE_LINK_NOT_CURRENT"), nil
	}
	canceled, err := recoveryBlockedByCancellation(ctx, tx, p.TaskID, p.SourceRunID)
	if err != nil {
		return nil, err
	}
	if canceled {
		return purposeCommitFailure("EFFECTIVE_CANCELLATION"), nil
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN runs r ON r.id=? WHERE t.id=? AND t.project_id=? AND t.feature_area_id=?
 AND t.status='ACTIVE' AND t.phase='RECOVERY_REQUIRED' AND COALESCE(t.current_definition_revision_id,'')=? AND r.runtime_epoch=? AND r.runtime_workset_hash=?
 AND ((?='REVIEW' AND t.current_review_id=? AND r.operation_kind='REVIEW' AND r.operation_id=?) OR
      (? IN ('IMPLEMENTATION','VERIFICATION') AND t.current_execution_id=? AND r.operation_kind='EXECUTION' AND r.operation_id=?))
 AND NOT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions c WHERE c.intent_ai_session_id=? AND c.intent_id=? AND c.intent_version=? AND c.intent_content_hash=?))`,
		p.SourceRunID, p.TaskID, intent.ProjectID, p.FeatureAreaID, p.TaskDefinitionRevisionID, p.RuntimeEpoch, p.RuntimeWorksetHash,
		p.OwnerKind, p.SourceReviewID, p.SourceReviewID, p.OwnerKind, p.SourceExecutionID, p.SourceExecutionID,
		intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash).Scan(&current)
	if err != nil {
		return nil, err
	}
	if current != 1 {
		var revision, executionID, reviewID, status, phase, epoch, worksetHash string
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(t.current_definition_revision_id,''),COALESCE(t.current_execution_id,''),COALESCE(t.current_review_id,''),t.status,t.phase,COALESCE(r.runtime_epoch,''),COALESCE(r.runtime_workset_hash,'') FROM tasks t JOIN runs r ON r.id=? WHERE t.id=?`, p.SourceRunID, p.TaskID).Scan(&revision, &executionID, &reviewID, &status, &phase, &epoch, &worksetHash)
		if errors.Is(err, sql.ErrNoRows) {
			return purposeCommitFailure("PURPOSE_SOURCE_CHANGED"), nil
		}
		if err != nil {
			return nil, err
		}
		switch {
		case revision != p.TaskDefinitionRevisionID:
			return purposeCommitFailure("SOURCE_REVISION_CHANGED"), nil
		case epoch != p.RuntimeEpoch:
			return purposeCommitFailure("SOURCE_EPOCH_CHANGED"), nil
		case worksetHash != p.RuntimeWorksetHash:
			return purposeCommitFailure("SOURCE_WORKSET_CHANGED"), nil
		case status != "ACTIVE" || phase != "RECOVERY_REQUIRED":
			return purposeCommitFailure("PURPOSE_SOURCE_CHANGED"), nil
		case p.OwnerKind == "REVIEW" && reviewID != p.SourceReviewID || p.OwnerKind != "REVIEW" && executionID != p.SourceExecutionID:
			return purposeCommitFailure("SUCCESSOR_ALREADY_EXISTS"), nil
		default:
			return purposeCommitFailure("PURPOSE_SOURCE_CHANGED"), nil
		}
	}
	if checkPreparedContext {
		topology, development, err := purposeProjectContextTx(ctx, tx, intent.ProjectID)
		if err != nil {
			return nil, err
		}
		if body.PreparedTopologyRevision != topology || body.PreparedDevelopmentContextHash == "" || body.PreparedDevelopmentContextHash != development {
			return purposeCommitFailure("PURPOSE_PROJECT_CONTEXT_CHANGED"), nil
		}
	}
	return nil, nil
}

func purposeCommitFailure(code string) *confirmation.Failure {
	next := confirmation.PrepareNewVersion
	if code == "SOURCE_REVISION_CHANGED" || code == "SOURCE_EPOCH_CHANGED" || code == "SOURCE_WORKSET_CHANGED" || code == "RECOVERY_PURPOSE_LINK_NOT_CURRENT" || code == "PURPOSE_SOURCE_CHANGED" {
		next = confirmation.NextAction("PREPARE_RECOVERY_STATUS")
	}
	if code == "EFFECTIVE_CANCELLATION" || code == "SUCCESSOR_ALREADY_EXISTS" || code == "PURPOSE_OWNER_MISMATCH" {
		next = confirmation.AccessBlocked
	}
	return confirmation.NewFailure(code, "CORE", "UNIQUE_CONSUMPTION", "The exact Recovery purpose authority is no longer current.", next, nil)
}

func recordPurposeRejectionTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, in confirmation.CommitInput, failure *confirmation.Failure) error {
	if intent.Purpose != confirmation.RecoveryPurpose || intent.Content.RecoveryPurpose == nil {
		return nil
	}
	if failure == nil {
		return confirmation.ErrRequestConflict
	}
	p := intent.Content.RecoveryPurpose.Proposal
	_, err := tx.ExecContext(ctx, `INSERT INTO recovery_purpose_rejection_receipts(intent_ai_session_id,intent_id,intent_version,intent_content_hash,commit_request_id,submitted_hash,recovery_id,recovery_generation,source_owner_kind,failure_code,outcome,rejected_at) VALUES(?,?,?,?,?,?,?,?,? ,?,'REJECTED_NOT_CONSUMED',?)`, intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash, in.RequestID, in.SubmittedHash, p.RecoveryID, p.RecoveryGeneration, p.OwnerKind, failure.Code, formatTime(time.Now().UTC()))
	return err
}

// WriteRecoveryPurposeReceipt is the sole business step for the purpose
// Confirmation. Submission/step ledgers stay on the canonical Confirmation
// path; this method atomically validates and records one immutable purpose fact.
func (s *ConfirmationStore) WriteRecoveryPurposeReceipt(ctx context.Context, in confirmation.CommitInput) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	intent, err := confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return err
	}
	if !confirmationCommitIdentity(intent, in) || intent.Purpose != confirmation.RecoveryPurpose || intent.Commit == nil || intent.Commit.RequestID != in.RequestID || intent.Commit.VariantID != in.VariantID {
		return confirmation.ErrRequestConflict
	}
	var priorRequest, priorVariant, priorHash, priorOutcome string
	err = tx.QueryRowContext(ctx, `SELECT commit_request_id,selected_variant_id,submitted_hash,outcome FROM recovery_purpose_confirmation_receipts WHERE intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=?`, in.AISessionID, in.IntentID, in.Version, in.ContentHash).Scan(&priorRequest, &priorVariant, &priorHash, &priorOutcome)
	if err == nil {
		if priorRequest != in.RequestID || priorVariant != in.VariantID || priorOutcome != "COMMITTED" {
			return confirmation.ErrRequestConflict
		}
		sub, readErr := confirmationSubmissionTx(ctx, tx, intent, in.RequestID)
		if readErr != nil {
			return readErr
		}
		if priorHash != sub.PayloadSHA256 {
			return confirmation.ErrRequestConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if failure, fenceErr := recoveryPurposeCommitFenceTx(ctx, tx, intent); fenceErr != nil {
		return fenceErr
	} else if failure != nil {
		return failure
	}
	sub, err := confirmationSubmissionTx(ctx, tx, intent, in.RequestID)
	if err != nil {
		return err
	}
	if !sub.Recorded || sub.PayloadSHA256 == "" || sub.Outcome != confirmation.Consumed {
		return confirmation.ErrRequestConflict
	}
	p := intent.Content.RecoveryPurpose.Proposal
	link, err := currentPurposeLinkTx(ctx, tx, intent)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_purpose_confirmation_receipts(intent_ai_session_id,intent_id,intent_version,intent_content_hash,commit_request_id,selected_variant_id,recovery_id,recovery_generation,source_owner_kind,source_owner_id,proposal_hash,submitted_hash,outcome,consumption_disposition,committed_at,purpose_link_id,purpose_link_ordinal) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash, in.RequestID, in.VariantID, p.RecoveryID, p.RecoveryGeneration, p.OwnerKind, p.OwnerID, p.SubmittedHash, sub.PayloadSHA256, "COMMITTED", "UNCONSUMED", formatTime(time.Now().UTC()), link.ID, link.Ordinal)
	if err != nil {
		return err
	}
	return tx.Commit()
}
