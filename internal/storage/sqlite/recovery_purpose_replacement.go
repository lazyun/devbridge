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

// Replay reconstructs the request's predecessor binding from immutable history,
// never from the current pointer, which may have advanced since this request.
func recoveryReplacementReplayBindingTx(ctx context.Context, tx *sql.Tx, sourceID string) (*confirmationReplacementBinding, error) {
	b := &confirmationReplacementBinding{}
	err := tx.QueryRowContext(ctx, `SELECT recovery_id,recovery_generation,source_owner_kind,source_owner_id,link_id,link_ordinal FROM recovery_purpose_link_history WHERE intent_id=?`, sourceID).Scan(&b.RecoveryID, &b.RecoveryGeneration, &b.SourceOwnerKind, &b.SourceOwnerID, &b.PredecessorLinkID, &b.PredecessorLinkOrdinal)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, confirmation.ErrRequestConflict
	}
	if err != nil {
		return nil, err
	}
	b.ExpectedSelectionVersion = b.PredecessorLinkOrdinal
	return b, nil
}

func purposeConfirmationVariants(p rs.PurposeProposal) []confirmation.Variant {
	variants := make([]confirmation.Variant, 0, len(p.Variants))
	for _, variant := range p.Variants {
		v := confirmation.Variant{ID: variant.ID, Label: variant.Label, Summary: variant.Summary}
		switch p.OwnerKind {
		case "IMPLEMENTATION":
			b := variant.Implementation
			v.WorkSpec, v.VerificationSpec = &b.WorkSpec, &b.VerificationSpec
			v.RepositoryTargets, v.PrimaryMemberID, v.ImplementationMode = b.RepositoryTargets, b.PrimaryMemberID, b.ImplementationMode
		case "VERIFICATION":
			b := variant.Verification
			v.WorkSpec, v.VerificationSpec = b.WorkSpec, &b.VerificationSpec
			v.RepositoryTargets, v.PrimaryMemberID, v.ImplementationMode = b.RepositoryTargets, b.PrimaryMemberID, b.ImplementationMode
		case "REVIEW":
			b := variant.Review
			v.ReviewInstruction, v.RepositoryTargets = b.ReviewInstruction, b.RepositoryTargets
		}
		variants = append(variants, v)
	}
	return variants
}

// ReprepareRecoveryPurpose is one Core writer transaction: new intent, appended
// immutable link, current-link CAS, old supersession and exact request receipt.
func (s *ConfirmationStore) ReprepareRecoveryPurpose(ctx context.Context, in confirmation.ReprepareInput) (confirmation.Intent, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	source, err := confirmationRead(ctx, tx, in.AISessionID, in.SourceIntentID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if source.Purpose != confirmation.RecoveryPurpose || source.Content.RecoveryPurpose == nil {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	binding, err := recoveryReplacementReplayBindingTx(ctx, tx, source.IntentID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if out, found, e := replayConfirmationReplacementTx(ctx, tx, in, "RECOVERY_PURPOSE", binding); e != nil || found {
		return out, e
	}
	hash, err := replacementPayloadHash(ctx, in, source, "RECOVERY_PURPOSE", binding)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if source.Commit != nil || (source.State != "PENDING" && source.State != "STALE") {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	var attempts, rejected int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN outcome='REJECTED_NOT_CONSUMED' THEN 1 ELSE 0 END),0) FROM confirmation_commit_requests WHERE ai_session_id=? AND intent_id=?`, in.AISessionID, source.IntentID).Scan(&attempts, &rejected); err != nil {
		return confirmation.Intent{}, err
	}
	if attempts != rejected {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	// Validate immutable Recovery evidence and current source before inspecting
	// stale candidate context: source drift requires a new generation instead.
	if failure, e := recoveryPurposeFenceTx(ctx, tx, source, false); e != nil {
		return confirmation.Intent{}, e
	} else if failure != nil {
		return confirmation.Intent{}, failure
	}
	if attempts == 0 && source.State != "STALE" && time.Now().Before(source.ExpiresAt) {
		failure, e := recoveryPurposeFenceTx(ctx, tx, source, true)
		if e != nil {
			return confirmation.Intent{}, e
		}
		if failure == nil {
			return confirmation.Intent{}, confirmation.NewFailure("SOURCE_INTENT_NOT_STALE", "CORE", "REPLACEMENT", "A current purpose with no rejection does not need replacement.", confirmation.ReadCurrentTarget, nil)
		}
	}
	selected, err := currentPurposeLinkTx(ctx, tx, source)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if selected.ID != binding.PredecessorLinkID || selected.Ordinal != binding.PredecessorLinkOrdinal || selected.SelectionVersion != binding.ExpectedSelectionVersion {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	var candidate confirmation.PrepareInput
	raw, err := json.Marshal(source.Content)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if err = json.Unmarshal(raw, &candidate); err != nil {
		return confirmation.Intent{}, err
	}
	p := candidate.RecoveryPurpose.Proposal
	if in.RecoveryPurpose != nil {
		p = *in.RecoveryPurpose
	}
	record, err := recoveryReadTx(ctx, tx, binding.RecoveryID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if p.OwnerKind != binding.SourceOwnerKind || p.OwnerID != binding.SourceOwnerID || p.SourceAISessionID != source.AISessionID || p.Validate(record.Spec.Source, record.ID, record.Spec.Generation, record.AssessmentHash) != nil {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	// Fresh target validation is independent of stale proposal topology/context.
	runtimeSource, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, p.SourceRunID))
	if err != nil {
		return confirmation.Intent{}, err
	}
	if err = validatePurposeTargetsTx(ctx, tx, p, runtimeSource); err != nil {
		return confirmation.Intent{}, err
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, source.ProjectID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	candidate.RecoveryPurpose = &confirmation.RecoveryPurposeBody{Proposal: p, ExpectedTaskStatus: "ACTIVE", ExpectedTaskPhase: "RECOVERY_REQUIRED", PreparedTopologyRevision: topology, PreparedDevelopmentContextHash: development}
	candidate.Variants = purposeConfirmationVariants(p)
	candidate.RecommendedRuntimeConfig = p.RecommendedRuntimeConfig
	candidate.AISessionID, candidate.RequestID, candidate.SourceIntentID, candidate.SubmittedHash = in.AISessionID, in.RequestID, source.IntentID, hash
	// STALE is a read projection in legacy rows. If a durable STALE row exists,
	// normalize it only inside this writer transaction; rollback restores it and
	// no observer can see this intermediate state.
	if source.State == "STALE" {
		source.State = "PENDING"
		if err = confirmationSave(ctx, tx, source); err != nil {
			return confirmation.Intent{}, err
		}
	}
	out, err := prepareConfirmationIntentTx(ctx, tx, source.ProjectID, candidate, time.Now().UTC(), s.db.epoch)
	if err != nil {
		return confirmation.Intent{}, err
	}
	binding.ReplacementLinkID = "purpose-link:" + out.IntentID
	binding.LinkOrdinal = binding.PredecessorLinkOrdinal + 1
	if err = appendReplacementPurposeLinkTx(ctx, tx, source, out, *binding); err != nil {
		return confirmation.Intent{}, err
	}
	if err = insertConfirmationReplacementReceiptTx(ctx, tx, in, "RECOVERY_PURPOSE", out, binding); err != nil {
		return confirmation.Intent{}, err
	}
	receipt, _, err := readConfirmationReplacementReceiptTx(ctx, tx, in.AISessionID, in.RequestID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	out.ReplacementReceipt = receipt
	if err = tx.Commit(); err != nil {
		return confirmation.Intent{}, err
	}
	return out, nil
}
