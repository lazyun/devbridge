package confirmation

import "testing"

func TestRetiredContinuationNoEffectDoesNotRewriteConsumption(t *testing.T) {
	fixture := func() Intent {
		return Intent{State: "REJECTED", Purpose: Execution, Action: "REQUEST_CHANGES", Commit: &CommitInput{RequestID: "request"}, Submission: &Submission{Outcome: Consumed, Recorded: true, ReceiptRequestID: "request"}, Steps: []Step{{Kind: "RESOLVE_CONTINUATION", State: "REJECTED", Effect: "NO_EFFECT_PROVEN", Failure: NewFailure("REQUEST_CHANGES_REQUIRES_FORMAL_REVIEW", "CORE", "RECEIPT_READBACK", "No effect proved", ReadCurrentTarget, nil)}, {Kind: "START_IMPLEMENTATION", State: "PENDING"}}}
	}
	i := fixture()
	NormalizePublicIntent(&i)
	if i.Submission.BusinessOutcome != "REJECTED_NO_EFFECT" || i.Submission.Outcome != Consumed || i.Submission.Consumed == nil || !*i.Submission.Consumed || i.Submission.ConsumptionDisposition != "CONSUMED" || i.Submission.RedispatchSameIntent {
		t.Fatalf("no-effect projection changed original consumption: %+v", i.Submission)
	}
	for name, alter := range map[string]func(*Intent){
		"unknown":         func(v *Intent) { v.State = "UNKNOWN" },
		"differentAction": func(v *Intent) { v.Action = "RETRY_IMPLEMENTATION" },
		"missingProof":    func(v *Intent) { v.Steps[0].Effect = "UNKNOWN" },
		"ordinaryError":   func(v *Intent) { v.Steps[0].Failure.Stage = "BUSINESS_STEP" },
		"differentCode":   func(v *Intent) { v.Steps[0].Failure.Code = "TASK_RUNTIME_SELECTION_BINDING_MISSING" },
		"alreadyStarted":  func(v *Intent) { v.Steps[1].State = "UNKNOWN" },
		"effectIdentity":  func(v *Intent) { v.Steps[0].ExecutionID = "actual-execution" },
		"otherReceipt":    func(v *Intent) { v.Submission.ReceiptRequestID = "other" },
		"unobserved":      func(v *Intent) { v.Submission.Recorded = false },
	} {
		t.Run(name, func(t *testing.T) {
			v := fixture()
			alter(&v)
			NormalizePublicIntent(&v)
			if v.Submission.BusinessOutcome == "REJECTED_NO_EFFECT" {
				t.Fatalf("incomplete negative proof accepted: %+v", v)
			}
		})
	}
}
