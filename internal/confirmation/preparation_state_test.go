package confirmation

import "testing"

func TestClassifyPreparationEvidence(t *testing.T) {
	cases := []struct {
		name string
		ev   PreparationEvidence
		want PreparationClassification
	}{
		{"no row", PreparationEvidence{}, PreparationClassification{State: PreparationIncompleteNotCommitted, Consumed: boolPtr(false), ReprepareAllowed: true}},
		{"complete pending, no attempt", PreparationEvidence{IntentRowExists: true, IntentFactsComplete: true, State: "PENDING"}, PreparationClassification{State: PreparedPresentationUnavailable, Consumed: boolPtr(false)}},
		{"half-written, nothing committed", PreparationEvidence{IntentRowExists: true, IntentFactsComplete: false, State: "PENDING"}, PreparationClassification{State: PreparationIncompleteNotCommitted, Consumed: boolPtr(false), ReprepareAllowed: true}},
		{"half-written but business objects survive", PreparationEvidence{IntentRowExists: true, IntentFactsComplete: false, State: "PENDING", BusinessObjectsExist: true}, PreparationClassification{State: PreparationCommittedFactsCorrupt, Consumed: nil}},
		{"attempt exists, non-terminal", PreparationEvidence{IntentRowExists: true, IntentFactsComplete: true, State: "PENDING", CommitAttemptExists: true}, PreparationClassification{State: AttemptExistsOutcomeUnknown, Consumed: nil}},
		{"receipt rejected not consumed", PreparationEvidence{IntentRowExists: true, IntentFactsComplete: true, State: "PENDING", CommitAttemptExists: true, ReceiptRecorded: true, ReceiptOutcome: RejectedNotConsumed}, PreparationClassification{State: PreparationIncompleteNotCommitted, Consumed: boolPtr(false), ReprepareAllowed: true}},
		{"completed", PreparationEvidence{IntentRowExists: true, IntentFactsComplete: true, State: "COMPLETED"}, PreparationClassification{State: PreparationCommitted, Consumed: boolPtr(true)}},
		{"confirming unknown effect", PreparationEvidence{IntentRowExists: true, IntentFactsComplete: true, State: "CONFIRMING"}, PreparationClassification{State: AttemptExistsOutcomeUnknown, Consumed: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPreparationEvidence(tc.ev)
			if got.State != tc.want.State {
				t.Fatalf("state=%s want %s", got.State, tc.want.State)
			}
			if (got.Consumed == nil) != (tc.want.Consumed == nil) || (got.Consumed != nil && *got.Consumed != *tc.want.Consumed) {
				t.Fatalf("consumed=%v want %v", got.Consumed, tc.want.Consumed)
			}
			if got.ReprepareAllowed != tc.want.ReprepareAllowed {
				t.Fatalf("reprepare=%v want %v", got.ReprepareAllowed, tc.want.ReprepareAllowed)
			}
			if got.Redispatch {
				t.Fatal("classifier must never authorize redispatch")
			}
		})
	}
}
