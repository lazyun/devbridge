package recoverystatus

import "fmt"

// PurposeError preserves the frozen consume failure branch across Core and
// transport boundaries. Unwrap retains the existing broad error family for
// callers that have not migrated to the typed reason yet.
type PurposeError struct {
	ReasonCode string
	cause      error
}

func (e *PurposeError) Error() string { return fmt.Sprintf("%s: %v", e.ReasonCode, e.cause) }
func (e *PurposeError) Unwrap() error { return e.cause }

var (
	ErrPurposeReceiptMismatch          = &PurposeError{"PURPOSE_CONFIRMATION_RECEIPT_MISMATCH", ErrConflict}
	ErrPurposeOwnerMismatch            = &PurposeError{"PURPOSE_CONFIRMATION_OWNER_MISMATCH", ErrConflict}
	ErrPurposeAlreadyConsumed          = &PurposeError{"PURPOSE_CONFIRMATION_ALREADY_CONSUMED", ErrConflict}
	ErrPurposeSourceRevisionChanged    = &PurposeError{"SOURCE_REVISION_CHANGED", ErrIneligible}
	ErrPurposeSourceEpochChanged       = &PurposeError{"SOURCE_EPOCH_CHANGED", ErrIneligible}
	ErrPurposeSourceWorksetChanged     = &PurposeError{"SOURCE_WORKSET_CHANGED", ErrIneligible}
	ErrPurposeEffectiveCancellation    = &PurposeError{"EFFECTIVE_CANCELLATION", ErrIneligible}
	ErrPurposeSuccessorExists          = &PurposeError{"SUCCESSOR_ALREADY_EXISTS", ErrConflict}
	ErrPurposeRequestConflict          = &PurposeError{"REQUEST_CONFLICT", ErrConflict}
	ErrPurposeSourceSessionUnavailable = &PurposeError{"RECOVERY_SOURCE_AI_SESSION_UNAVAILABLE", ErrIneligible}
	ErrPurposeSourceSessionMismatch    = &PurposeError{"RECOVERY_SOURCE_AI_SESSION_MISMATCH", ErrIneligible}
)
