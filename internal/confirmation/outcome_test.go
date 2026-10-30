package confirmation

import "testing"

func TestNormalizePublicConfirmationFailureRepairsHistoricalEmptyMessage(t *testing.T) {
	original := &Failure{Code: "PROJECT_TOPOLOGY_CHANGED", Source: "CORE", Stage: "CONFIRMATION_PREFLIGHT", Message: "", NextAction: PrepareNewVersion}
	intent := &Intent{Steps: []Step{{Failure: original}}, Submission: &Submission{Outcome: RejectedNotConsumed, Recorded: true, Failure: &Failure{Code: "UNKNOWN_OLD_CODE", Message: ""}, NextAction: PrepareNewVersion}}
	NormalizePublicIntent(intent)
	if intent.Steps[0].Failure.Message == "" || intent.Steps[0].Failure.ReasonCode != "PROJECT_TOPOLOGY_CHANGED" {
		t.Fatalf("step failure not normalized: %+v", intent.Steps[0].Failure)
	}
	if intent.Submission.Failure.Message != publicFailureFallback || intent.Submission.Failure.Source != "CONFIRMATION" || intent.Submission.Failure.Stage != "RECEIPT_READBACK" {
		t.Fatalf("historical failure not safely normalized: %+v", intent.Submission.Failure)
	}
	if intent.Submission.Consumed == nil || *intent.Submission.Consumed || intent.Submission.BusinessOutcome != "REJECTED_NOT_CONSUMED" || intent.Submission.ConsumptionDisposition != "NOT_CONSUMED" || intent.Submission.RedispatchSameIntent || !intent.Submission.ReprepareAllowed {
		t.Fatalf("typed rejection facts incorrect: %+v", intent.Submission)
	}
}

func TestNormalizePublicUnknownNeverClaimsNonConsumptionOrRedispatch(t *testing.T) {
	intent := &Intent{Submission: &Submission{Outcome: Unknown, Recorded: false, NextAction: ReadOriginalReceipt}}
	NormalizePublicIntent(intent)
	if intent.Submission.Consumed != nil || intent.Submission.ConsumptionDisposition != "UNKNOWN" || intent.Submission.BusinessOutcome != "OUTCOME_UNKNOWN" || intent.Submission.RedispatchSameIntent || intent.Submission.ReprepareAllowed {
		t.Fatalf("UNKNOWN was weakened: %+v", intent.Submission)
	}
}
