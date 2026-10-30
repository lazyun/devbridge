package controlapi

import (
	"context"
	"errors"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// WithdrawConfirmationSubmission fences one user-held, uncertain submission.
// RejectCommit serializes with BeginCommit: either the original consumption wins
// and is only read back, or a durable rejection prevents a late copy from running.
// Absence of a read receipt alone is never non-consumption evidence.
func (f *Facade) WithdrawConfirmationSubmission(ctx context.Context, in c.CommitInput) (out c.Intent, err error) {
	defer c.NormalizePublicIntent(&out)
	if in.RequestID == "" || in.VariantID == "" {
		return out, invalid("the exact original submission is required")
	}
	current, err := f.ReadConfirmation(ctx, c.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash})
	if err != nil {
		return current, err
	}
	if current.Action != in.Action {
		return c.Intent{}, c.ErrRequestConflict
	}
	found := false
	for _, variant := range current.Content.Variants {
		found = found || variant.ID == in.VariantID
	}
	if !found {
		return current, invalid("the original prepared variant is required")
	}
	if in.SubmittedHash == "" {
		in.SubmittedHash = c.ValueHash(in)
	}
	original := in
	original.OriginalRequest = nil
	in.OriginalRequest = &original
	failure := c.NewFailure("CONFIRMATION_SUBMISSION_WITHDRAWN", "CORE", "SUBMISSION_WITHDRAWAL", "The original submission was sealed without consumption. Review the choices before explicitly confirming again.", c.EditSelection, nil)
	out, err = f.deps.Confirmations.RejectCommit(ctx, in, failure)
	if err != nil {
		var withdrawn *c.Failure
		sub := out.Submission
		if !errors.As(err, &withdrawn) || withdrawn.Code != failure.Code || sub == nil || !sub.Recorded || sub.Outcome != c.RejectedNotConsumed || sub.Request.RequestID != in.RequestID || sub.PayloadSHA256 != in.SubmittedHash {
			return out, err
		}
	}
	return f.ReadConfirmation(ctx, c.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash, RequestID: in.RequestID})
}
