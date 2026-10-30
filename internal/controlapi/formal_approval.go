package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalapproval"
)

// finishDefinitionApproval seals only a snapshot produced by the original
// approval transaction. A missing snapshot before APPROVE permits that business
// command; the same absence after APPROVE is an error. Legacy intents are
// explicitly NOT_APPLICABLE and can never be upgraded by this helper.
func (f *Facade) finishDefinitionApproval(ctx context.Context, intent confirmation.Intent) (formalapproval.Receipt, error) {
	store, ok := f.deps.Confirmations.(formalapproval.Store)
	if !ok || intent.Purpose != confirmation.Execution || intent.Action != "APPROVE" {
		return formalapproval.Receipt{}, formalapproval.ErrNotApplicable
	}
	if intent.Commit == nil {
		return formalapproval.Receipt{}, formalapproval.ErrConflict
	}
	c := intent.Commit
	submission, err := f.deps.Confirmations.ReadSubmission(ctx, confirmation.ReadInput{
		AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version,
		Purpose: intent.Purpose, ContentHash: intent.ContentHash, RequestID: c.RequestID,
	})
	if err != nil {
		return formalapproval.Receipt{}, err
	}
	if !submission.Recorded || submission.Outcome != confirmation.Consumed || submission.ReceiptRequestID != c.RequestID {
		return formalapproval.Receipt{}, formalapproval.ErrConflict
	}
	return store.FinalizeDefinitionApproval(ctx, formalapproval.ReadIdentity{
		AISessionID: intent.AISessionID, IntentID: intent.IntentID, IntentVersion: intent.Version,
		RequestID: c.RequestID, ContentHash: intent.ContentHash,
		TypedPayloadHash: formalapproval.HashCommit(*c), SubmittedHash: submission.PayloadSHA256,
	})
}

func definitionApprovalAbsent(err error) bool {
	return errors.Is(err, formalapproval.ErrMissing) || errors.Is(err, formalapproval.ErrNotApplicable)
}
