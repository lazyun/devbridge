package controlapi

import (
	"errors"
	"github.com/lazyun/devbridge/internal/confirmation"
	"reflect"
	"testing"
)

func TestConfirmationRecoveredSubmissionGuidanceRequiresExactProof(t *testing.T) {
	for _, name := range []string{"exact", "same_epoch_not_resumable", "missing_commit", "unrecorded", "unknown", "rejected", "other_receipt", "recovery_error"} {
		t.Run(name, func(t *testing.T) {
			owner := confirmation.Intent{CanResume: true, Commit: &confirmation.CommitInput{RequestID: "original"}}
			receipt := confirmation.Submission{Recorded: true, Outcome: confirmation.Consumed, ReceiptRequestID: "original", NextAction: confirmation.ReadOriginalReceipt}
			var recoveryErr error
			switch name {
			case "same_epoch_not_resumable":
				owner.CanResume = false
			case "missing_commit":
				owner.Commit = nil
			case "unrecorded":
				receipt.Recorded = false
			case "unknown":
				receipt.Outcome = confirmation.Unknown
			case "rejected":
				receipt.Outcome = confirmation.RejectedNotConsumed
			case "other_receipt":
				receipt.ReceiptRequestID = "another"
			case "recovery_error":
				recoveryErr = errors.New("canonical receipt unavailable")
			}
			want := receipt
			if name == "exact" {
				want.NextAction = confirmation.ContinueOriginalSteps
			}
			got := confirmationRecoveredSubmissionGuidance(owner, receipt, recoveryErr)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("guidance changed non-proven facts: got=%+v want=%+v", got, want)
			}
		})
	}
}
