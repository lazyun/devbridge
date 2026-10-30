package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

const purposeDispatchBatchSize = 32

type pendingPurposeConsumption struct {
	rowID int64
	owner string
	input rs.PurposeConsumeInput
}

// ContinueConfirmedPurposes is a Core write-side drain, never a client command
// or read projection. The immutable receipt is the durable queue. A cursor
// visits every receipt even when an earlier one is stale or temporarily fails.
// No caller identity is borrowed from the original Session.
func (s *RecoveryPurposeStore) ContinueConfirmedPurposes(ctx context.Context, after int64) (int64, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT r.rowid,r.intent_ai_session_id,r.intent_id,r.intent_version,r.intent_content_hash,r.source_owner_kind,r.purpose_link_id,r.purpose_link_ordinal,r.commit_request_id,r.submitted_hash
 FROM recovery_purpose_confirmation_receipts r
 WHERE r.outcome='COMMITTED' AND r.rowid>?
 AND NOT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions c WHERE c.intent_ai_session_id=r.intent_ai_session_id AND c.intent_id=r.intent_id AND c.intent_version=r.intent_version AND c.intent_content_hash=r.intent_content_hash)
 ORDER BY r.rowid LIMIT ?`, after, purposeDispatchBatchSize)
	if err != nil {
		return after, err
	}
	var pending []pendingPurposeConsumption
	for rows.Next() {
		var v pendingPurposeConsumption
		var link, request, submitted string
		var ordinal int
		if err = rows.Scan(&v.rowID, &v.input.AISessionID, &v.input.IntentID, &v.input.Version, &v.input.ContentHash, &v.owner, &link, &ordinal, &request, &submitted); err != nil {
			rows.Close()
			return after, err
		}
		switch v.owner {
		case "IMPLEMENTATION":
			v.input.Action = rs.ApplyConfirmedImplementationPurpose
		case "VERIFICATION":
			v.input.Action = rs.ApplyConfirmedVerificationPurpose
		case "REVIEW":
			v.input.Action = rs.ApplyConfirmedReviewPurpose
		}
		v.input.RequestID = "core_recovery_purpose_" + confirmation.ValueHash([]any{"v1", v.input.AISessionID, v.input.IntentID, v.input.Version, v.input.ContentHash, v.input.Action, link, ordinal, request, submitted})
		v.input.PayloadHash = v.input.ComputePayloadHash()
		pending = append(pending, v)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return after, err
	}
	var failures []error
	for _, v := range pending {
		if ctx.Err() != nil {
			return after, errors.Join(append(failures, ctx.Err())...)
		}
		// A crash after writing the receipt but before the final step must not
		// strand the durable queue. This transaction only seals that exact step;
		// business consumption remains a separate transaction below.
		err = s.completeCommittedPurpose(ctx, v.input)
		if err == nil {
			_, err = s.consumeConfirmed(ctx, v.input, v.owner, v.input.Action, true)
		}
		if err != nil && !errors.Is(err, rs.ErrPurposeAlreadyConsumed) {
			failures = append(failures, fmt.Errorf("purpose %s: %w", v.input.IntentID, err))
		}
		after = v.rowID
	}
	if len(pending) < purposeDispatchBatchSize {
		after = 0
	}
	return after, errors.Join(failures...)
}

func (s *RecoveryPurposeStore) completeCommittedPurpose(ctx context.Context, in rs.PurposeConsumeInput) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	intent, err := formalConfirmationTx(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return err
	}
	if intent.Version != in.Version || intent.ContentHash != in.ContentHash || intent.Purpose != confirmation.RecoveryPurpose || intent.Action != confirmation.ConfirmRecoveryPurpose || intent.Commit == nil || intent.Content.RecoveryPurpose == nil || len(intent.Steps) != 1 || intent.Steps[0].Kind != "WRITE_PURPOSE_RECEIPT" {
		return rs.ErrPurposeReceiptMismatch
	}
	if err = validateCommittedPurposeReceiptTx(ctx, tx, intent); err != nil {
		return err
	}
	if intent.State == "COMPLETED" {
		if !completedConfirmation(intent) {
			return rs.ErrPurposeReceiptMismatch
		}
		return nil
	}
	if (intent.State != "CONFIRMING" && intent.State != "UNKNOWN") || (intent.Steps[0].State != "UNKNOWN" && intent.Steps[0].State != "COMPLETED") {
		return rs.ErrPurposeReceiptMismatch
	}
	step := &intent.Steps[0]
	step.State, step.Effect, step.ProjectID = "COMPLETED", "COMMAND_COMPLETED", intent.ProjectID
	step.Failure, step.Code, step.Message = nil, "", ""
	intent.State = "COMPLETED"
	if !completedConfirmation(intent) {
		return rs.ErrPurposeReceiptMismatch
	}
	if err = confirmationSave(ctx, tx, intent); err != nil {
		return err
	}
	return tx.Commit()
}

// Receipt readback is historical: current generation, source and cancellation
// are checked separately by the consume writer and cannot rewrite this outcome.
func validateCommittedPurposeReceiptTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent) error {
	if intent.Commit == nil || intent.Content.RecoveryPurpose == nil {
		return rs.ErrPurposeReceiptMismatch
	}
	p := intent.Content.RecoveryPurpose.Proposal
	if p.SubmittedHash != p.ComputeSubmittedHash() || p.SourceAISessionID != intent.AISessionID {
		return rs.ErrPurposeReceiptMismatch
	}
	sub, err := confirmationSubmissionTx(ctx, q, intent, intent.Commit.RequestID)
	if err != nil {
		return err
	}
	if !sub.Recorded || sub.Outcome != confirmation.Consumed || sub.Request.VariantID != intent.Commit.VariantID {
		return rs.ErrPurposeReceiptMismatch
	}
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_receipts r JOIN recovery_purpose_link_history l ON l.link_id=r.purpose_link_id AND l.link_ordinal=r.purpose_link_ordinal
 WHERE r.intent_ai_session_id=? AND r.intent_id=? AND r.intent_version=? AND r.intent_content_hash=? AND r.commit_request_id=? AND r.selected_variant_id=? AND r.submitted_hash=? AND r.outcome='COMMITTED'
 AND r.recovery_id=? AND r.recovery_generation=? AND r.source_owner_kind=? AND r.source_owner_id=? AND r.proposal_hash=?
 AND l.intent_ai_session_id=r.intent_ai_session_id AND l.intent_id=r.intent_id AND l.intent_version=r.intent_version AND l.intent_content_hash=r.intent_content_hash AND l.recovery_id=r.recovery_id AND l.recovery_generation=r.recovery_generation AND l.source_owner_kind=r.source_owner_kind AND l.source_owner_id=r.source_owner_id AND l.proposal_hash=r.proposal_hash
 AND l.task_id=? AND l.feature_area_id=? AND l.source_execution_id=? AND l.source_review_id=? AND l.task_definition_revision_id=? AND l.source_operation_id=? AND l.source_run_id=? AND l.source_turn_id=? AND l.runtime_epoch=? AND l.runtime_workset_hash=? AND l.assessment_hash=?)`, intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash, intent.Commit.RequestID, intent.Commit.VariantID, sub.PayloadSHA256,
		p.RecoveryID, p.RecoveryGeneration, p.OwnerKind, p.OwnerID, p.SubmittedHash, p.TaskID, p.FeatureAreaID, p.SourceExecutionID, p.SourceReviewID, p.TaskDefinitionRevisionID, p.SourceOperationID, p.SourceRunID, p.SourceTurnID, p.RuntimeEpoch, p.RuntimeWorksetHash, p.AssessmentHash).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return rs.ErrPurposeReceiptMismatch
	}
	return nil
}
