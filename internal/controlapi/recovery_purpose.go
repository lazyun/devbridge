package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/recoverystatus"
)

// These commands map the three frozen Core consumption transitions. The
// adapters supply only exact receipt and request identity; SQLite owns all
// authority checks, durable effects, replay, and response-loss readback.
func (f *Facade) ApplyConfirmedImplementationPurpose(ctx context.Context, in recoverystatus.PurposeConsumeInput) (recoverystatus.PurposeConsumption, error) {
	return f.applyRecoveryPurpose(ctx, in, recoverystatus.ApplyConfirmedImplementationPurpose)
}
func (f *Facade) ApplyConfirmedVerificationPurpose(ctx context.Context, in recoverystatus.PurposeConsumeInput) (recoverystatus.PurposeConsumption, error) {
	return f.applyRecoveryPurpose(ctx, in, recoverystatus.ApplyConfirmedVerificationPurpose)
}
func (f *Facade) ApplyConfirmedReviewPurpose(ctx context.Context, in recoverystatus.PurposeConsumeInput) (recoverystatus.PurposeConsumption, error) {
	return f.applyRecoveryPurpose(ctx, in, recoverystatus.ApplyConfirmedReviewPurpose)
}

func (f *Facade) applyRecoveryPurpose(ctx context.Context, in recoverystatus.PurposeConsumeInput, action string) (recoverystatus.PurposeConsumption, error) {
	if in.Action != action || f.deps.RecoveryPurposeConsumers == nil {
		return recoverystatus.PurposeConsumption{}, MapError(recoverystatus.ErrIneligible)
	}
	if _, err := f.confirmationSession(ctx, in.AISessionID); err != nil {
		return recoverystatus.PurposeConsumption{}, err
	}
	switch action {
	case recoverystatus.ApplyConfirmedImplementationPurpose:
		return f.deps.RecoveryPurposeConsumers.ApplyConfirmedImplementationPurpose(ctx, in)
	case recoverystatus.ApplyConfirmedVerificationPurpose:
		return f.deps.RecoveryPurposeConsumers.ApplyConfirmedVerificationPurpose(ctx, in)
	case recoverystatus.ApplyConfirmedReviewPurpose:
		return f.deps.RecoveryPurposeConsumers.ApplyConfirmedReviewPurpose(ctx, in)
	}
	return recoverystatus.PurposeConsumption{}, MapError(recoverystatus.ErrIneligible)
}

func (f *Facade) RepairResolvedRecoveryPurposeForSession(ctx context.Context, sessionID string, in recoverystatus.RepairPurposeInput) (recoverystatus.PurposeRepairReceipt, error) {
	if in.Proposal.SourceAISessionID != sessionID || in.RecoveryID == "" {
		return recoverystatus.PurposeRepairReceipt{}, MapError(recoverystatus.ErrIneligible)
	}
	record, err := f.deps.RecoveryStatus.Read(ctx, in.RecoveryID)
	if err != nil {
		return recoverystatus.PurposeRepairReceipt{}, MapError(err)
	}
	if _, _, err = f.scopedRun(ctx, sessionID, record.Spec.RunID); err != nil {
		return recoverystatus.PurposeRepairReceipt{}, err
	}
	repairer, ok := f.deps.RecoveryStatus.(interface {
		RepairResolvedPurpose(context.Context, recoverystatus.RepairPurposeInput) (recoverystatus.PurposeRepairReceipt, error)
	})
	if !ok {
		return recoverystatus.PurposeRepairReceipt{}, MapError(recoverystatus.ErrIneligible)
	}
	receipt, err := repairer.RepairResolvedPurpose(ctx, in)
	return receipt, MapError(err)
}
