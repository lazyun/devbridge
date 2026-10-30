package controlapi

import (
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/recoverystatus"
)

func TestRecoveryPurposeReasonCodesSurviveCoreMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code ErrorCode
	}{
		{recoverystatus.ErrPurposeReceiptMismatch, "PURPOSE_CONFIRMATION_RECEIPT_MISMATCH"},
		{recoverystatus.ErrPurposeOwnerMismatch, "PURPOSE_CONFIRMATION_OWNER_MISMATCH"},
		{recoverystatus.ErrPurposeAlreadyConsumed, "PURPOSE_CONFIRMATION_ALREADY_CONSUMED"},
		{recoverystatus.ErrPurposeSourceRevisionChanged, "SOURCE_REVISION_CHANGED"},
		{recoverystatus.ErrPurposeSourceEpochChanged, "SOURCE_EPOCH_CHANGED"},
		{recoverystatus.ErrPurposeSourceWorksetChanged, "SOURCE_WORKSET_CHANGED"},
		{recoverystatus.ErrPurposeEffectiveCancellation, "EFFECTIVE_CANCELLATION"},
		{recoverystatus.ErrPurposeSuccessorExists, "SUCCESSOR_ALREADY_EXISTS"},
		{recoverystatus.ErrPurposeRequestConflict, "REQUEST_CONFLICT"},
		{recoverystatus.ErrPurposeSourceSessionUnavailable, "RECOVERY_SOURCE_AI_SESSION_UNAVAILABLE"},
		{recoverystatus.ErrPurposeSourceSessionMismatch, "RECOVERY_SOURCE_AI_SESSION_MISMATCH"},
	} {
		mapped := MapError(tc.err)
		var public *Error
		if !errors.As(mapped, &public) || public.Code != tc.code || !errors.Is(mapped, tc.err) {
			t.Fatalf("%v mapped to %v, want %s", tc.err, mapped, tc.code)
		}
	}
}
