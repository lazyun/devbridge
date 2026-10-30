package recoverystatus

import "testing"

func TestRejectedPurposeOffersOnlyReasonSpecificAction(t *testing.T) {
	for _, tc := range []struct {
		name, code, action string
	}{
		{"selection", "CONFIRMATION_VARIANT_UNAVAILABLE", "EDIT_SELECTION_AND_RESUBMIT"},
		{"revision", "SOURCE_REVISION_CHANGED", "PREPARE_RECOVERY_STATUS"},
		{"epoch", "SOURCE_EPOCH_CHANGED", "PREPARE_RECOVERY_STATUS"},
		{"workset", "SOURCE_WORKSET_CHANGED", "PREPARE_RECOVERY_STATUS"},
		{"topology", "PROJECT_TOPOLOGY_CHANGED", "PREPARE_NEW_VERSION"},
		{"canceled", "EFFECTIVE_CANCELLATION", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ProjectProjection(Projection{State: "RESOLVED", EngineeringDisposition: "REVISION_REQUIRED", OwnerKind: "IMPLEMENTATION", PurposeConfirmation: &PurposeConfirmationProjection{IntentID: "intent", State: "REJECTED", SubmissionOutcome: "REJECTED_NOT_CONSUMED", FailureCode: tc.code}})
			mutations := []string{}
			for _, action := range p.AvailableActions {
				if action == "EDIT_SELECTION_AND_RESUBMIT" || action == "PREPARE_NEW_VERSION" || action == "PREPARE_RECOVERY_STATUS" || action == "COMMIT_CONFIRMATION" {
					mutations = append(mutations, action)
				}
			}
			if len(mutations) > 1 || tc.action == "" && len(mutations) != 0 || tc.action != "" && (len(mutations) != 1 || mutations[0] != tc.action) {
				t.Fatalf("failure=%s actions=%v", tc.code, p.AvailableActions)
			}
		})
	}
}

func TestPurposeStaleProjectionUsesCurrentSourceAndCancellation(t *testing.T) {
	for _, code := range []string{"SOURCE_REVISION_CHANGED", "SOURCE_EPOCH_CHANGED", "SOURCE_WORKSET_CHANGED", "EFFECTIVE_CANCELLATION"} {
		for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION", "REVIEW"} {
			p := ProjectProjection(Projection{State: "RESOLVED", EngineeringDisposition: "REVISION_REQUIRED", OwnerKind: owner, PurposeConfirmation: &PurposeConfirmationProjection{IntentID: "old", State: "STALE", CurrentFailureCode: code}})
			want := []string{"READ_RECOVERY_STATUS"}
			if code != "EFFECTIVE_CANCELLATION" {
				want = append(want, "PREPARE_RECOVERY_STATUS")
			}
			if len(p.AvailableActions) != len(want) {
				t.Fatalf("%s %s actions=%v", owner, code, p.AvailableActions)
			}
			for i, action := range want {
				if p.AvailableActions[i] != action {
					t.Fatalf("%s %s actions=%v", owner, code, p.AvailableActions)
				}
			}
		}
	}
	p := ProjectProjection(Projection{State: "RESOLVED", EngineeringDisposition: "REVISION_REQUIRED", OwnerKind: "IMPLEMENTATION", PurposeConfirmation: &PurposeConfirmationProjection{IntentID: "old", State: "PENDING", Historical: true}})
	if len(p.AvailableActions) != 1 || p.AvailableActions[0] != "READ_RECOVERY_STATUS" {
		t.Fatalf("historical generation offers authority: %v", p.AvailableActions)
	}
}

func TestPurposeDriftProjectionMatrix(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION", "REVIEW"} {
		for _, state := range []string{"STALE", "REJECTED"} {
			for _, code := range []string{"SOURCE_REVISION_CHANGED", "SOURCE_EPOCH_CHANGED", "SOURCE_WORKSET_CHANGED", "EFFECTIVE_CANCELLATION"} {
				c := &PurposeConfirmationProjection{IntentID: "old", State: state, FailureCode: code}
				if state == "REJECTED" {
					c.SubmissionOutcome = "REJECTED_NOT_CONSUMED"
				}
				p := ProjectProjection(Projection{State: "RESOLVED", EngineeringDisposition: "REVISION_REQUIRED", OwnerKind: owner, PurposeConfirmation: c})
				want := 1
				if code != "EFFECTIVE_CANCELLATION" {
					want = 2
				}
				if len(p.AvailableActions) != want || want == 2 && p.AvailableActions[1] != "PREPARE_RECOVERY_STATUS" {
					t.Fatalf("%s %s %s actions=%v", owner, state, code, p.AvailableActions)
				}
			}
		}
		for _, code := range []string{"SOURCE_REVISION_CHANGED", "EFFECTIVE_CANCELLATION"} {
			p := ProjectProjection(Projection{State: "RESOLVED", EngineeringDisposition: "REVISION_REQUIRED", OwnerKind: owner, PurposeConfirmation: &PurposeConfirmationProjection{IntentID: "unknown", State: "PENDING", SubmissionOutcome: "UNKNOWN", CurrentFailureCode: code}})
			if len(p.AvailableActions) != 2 || p.AvailableActions[1] != "READ_CONFIRMATION_RECEIPT" {
				t.Fatalf("unknown %s %s actions=%v", owner, code, p.AvailableActions)
			}
		}
	}
}

func TestCommittedPurposeDoesNotExposeCoreConsumeAsUserAction(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION", "REVIEW"} {
		p := ProjectProjection(Projection{State: "RESOLVED", EngineeringDisposition: "REVISION_REQUIRED", OwnerKind: owner, PurposeConfirmation: &PurposeConfirmationProjection{IntentID: "intent", State: "COMPLETED", OwnerKind: owner, ReceiptCommitted: true}})
		if p.PurposeConfirmation.State != "CONFIRMATION_COMMITTED" {
			t.Fatalf("%s state=%s", owner, p.PurposeConfirmation.State)
		}
		for _, action := range p.AvailableActions {
			if action == ApplyConfirmedImplementationPurpose || action == ApplyConfirmedVerificationPurpose || action == ApplyConfirmedReviewPurpose {
				t.Fatalf("%s exposed Core-only action %s", owner, action)
			}
		}
	}
}
