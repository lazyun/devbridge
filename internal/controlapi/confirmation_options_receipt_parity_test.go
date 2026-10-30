package controlapi_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// §8/§9: a rejection receipt is a durable fact the moment it is written, so the
// options read, the exact intent read and the workflow projection must all report
// the same consumption and retry facts. attemptState/receiptState are derived from
// that receipt, never from "did a successor appear", and NOT_ATTEMPTED/NOT_OBSERVED
// belong only to an intent nobody has submitted.

func optionsRead(t *testing.T, f *upperLifecycleFixture, intent c.Intent) c.Options {
	t.Helper()
	value, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, ContentHash: intent.ContentHash}, ProjectID: intent.ProjectID})
	if err != nil {
		t.Fatalf("options read: %v", err)
	}
	return value
}

func TestConfirmationOptionsTrackTheDurableRejectionReceiptAcrossSurfaces(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "approval-for-"+name)
	prepared, commit := prepareLifecycleReviewDecision(t, f, "REVISE", "prepare-"+name)

	// G6: nobody has submitted this intent yet.
	before := optionsRead(t, f, prepared)
	if e := before.Eligibility; !e.Available || e.AttemptState != "NOT_ATTEMPTED" || e.ReceiptState != "NOT_OBSERVED" || e.ReasonCode != "" || e.RetryDisposition != "" || e.ReprepareAllowed {
		t.Fatalf("unattempted intent eligibility=%+v", e)
	}
	if len(before.Variants) == 0 {
		t.Fatal("prepared variants disappeared")
	}
	for _, variant := range before.Variants {
		if !variant.Available {
			t.Fatalf("unsubmitted variant unavailable: %+v", variant)
		}
	}

	// G7: one durable no-effect rejection is now the intent's current fact.
	f.appendTopologyMember(t, name+"-member")
	rejected, err := f.facade.CommitConfirmation(f.ctx, commit)
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "PROJECT_TOPOLOGY_CHANGED" || failure.NextAction != c.PrepareNewVersion {
		t.Fatalf("stale topology commit err=%v failure=%+v", err, failure)
	}
	sub := rejected.Submission
	if sub == nil || !sub.Recorded || sub.Outcome != c.RejectedNotConsumed {
		t.Fatalf("rejection receipt=%+v", sub)
	}
	after := optionsRead(t, f, prepared)
	e := after.Eligibility
	if e.Available || e.AttemptState != "TERMINAL_REJECTED" || e.ReceiptState != string(c.RejectedNotConsumed) ||
		e.RetryDisposition != string(c.RetryReprepareRequired) || e.NextAction != c.PrepareNewVersion ||
		!e.ReprepareAllowed || e.ReasonCode != "INTENT_REPREPARE_REQUIRED" {
		t.Fatalf("recorded rejection is invisible on the options surface: %+v receipt=%+v", e, sub)
	}
	for _, variant := range after.Variants {
		if variant.Available || variant.Reason != "INTENT_REPREPARE_REQUIRED" || len(variant.Failures) == 0 ||
			variant.Failures[0].ReasonCode != "INTENT_REPREPARE_REQUIRED" || variant.Failures[0].NextAction != c.PrepareNewVersion {
			t.Fatalf("variant still offered on a terminal rejection: %+v", variant)
		}
	}

	// G8: every read surface carries the identical facts, and no surface can offer
	// an edit/replay of the rejected request.
	read, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	if read.State != "PENDING" || read.Submission == nil {
		t.Fatalf("intent read lost the rejection: state=%s submission=%+v", read.State, read.Submission)
	}
	authority := *read.Submission
	if e.ReceiptState != string(authority.Outcome) || e.RetryDisposition != string(authority.RetryDisposition) ||
		e.NextAction != authority.NextAction || e.ReprepareAllowed != authority.ReprepareAllowed ||
		authority.BusinessOutcome != "REJECTED_NOT_CONSUMED" || authority.ConsumptionDisposition != "NOT_CONSUMED" ||
		authority.Consumed == nil || *authority.Consumed || authority.RedispatchSameIntent {
		t.Fatalf("options and intent read disagree with the durable receipt: options=%+v read=%+v", e, authority)
	}
	// The Decision itself was untouched by the topology drift, so the workflow
	// projection and the options read must still report the same version token the
	// intent froze at prepare, and the Decision must remain re-preparable.
	approval := guidanceApprovalVersion(t, f, prepared.Content.Review.TaskID)
	if approval == nil || approval.Status != "PENDING" || approval.DecisionVersion != prepared.Content.Review.PreparedDecisionVersion ||
		approval.DecisionVersion != e.CurrentDecisionVersion || approval.DecisionVersion == approval.DecisionID {
		t.Fatalf("workflow Decision fact=%+v options=%+v", approval, e)
	}
	encoded, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"OUTCOME_UNKNOWN", `"attemptState":"NOT_ATTEMPTED"`, `"receiptState":"NOT_OBSERVED"`, `"nextAction":"EDIT_SELECTION"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("options envelope still carries the pseudo-closure %q: %s", forbidden, encoded)
		}
	}
}
