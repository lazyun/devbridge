package confirmation

import (
	"testing"
	"time"
)

func futureIntent(state string, commit *CommitInput) *Intent {
	now := time.Now()
	return &Intent{State: state, ExpiresAt: now.Add(time.Hour), CreatedAt: now, Commit: commit}
}

func rejection(next NextAction) *Submission {
	return &Submission{
		Outcome:    RejectedNotConsumed,
		Recorded:   true,
		NextAction: next,
		Failure:    &Failure{Code: "BACKEND_SELECTION_NOT_APPLICABLE", Source: "CONFIRMATION", Stage: "CONFIRMATION_PREFLIGHT", Message: "m", NextAction: next},
	}
}

// §3/§6: retryDisposition is derived from the durable receipt alone. The table is
// the frozen mapping every public surface must agree on; no surface re-derives it.
func TestRetryDispositionFollowsTheFrozenConsumptionTable(t *testing.T) {
	cases := []struct {
		name   string
		intent *Intent
		sub    *Submission
		want   RetryDisposition
	}{
		{"recorded rejection with an editable action", futureIntent("PENDING", nil), rejection(EditSelection), RetryEditSelectionAndResubmit},
		{"editable rejection on a superseded intent must re-prepare", futureIntent("SUPERSEDED", nil), rejection(EditSelection), RetryReprepareRequired},
		{"editable rejection on a terminal intent must re-prepare", futureIntent("PARTIAL", nil), rejection(EditSelection), RetryReprepareRequired},
		{"editable rejection behind a consumed choice stays fenced", futureIntent("PENDING", &CommitInput{RequestID: "r1"}), rejection(EditSelection), RetryReprepareRequired},
		{"expired editable rejection can no longer be confirmed", func() *Intent {
			intent := futureIntent("PENDING", nil)
			intent.ExpiresAt = time.Now().Add(-time.Minute)
			return intent
		}(), rejection(EditSelection), RetryReprepareRequired},
		{"stale rejection must re-prepare", futureIntent("PENDING", nil), rejection(PrepareNewVersion), RetryReprepareRequired},
		{"read-current-target rejection re-prepares, never retries in place", futureIntent("PENDING", nil), rejection(ReadCurrentTarget), RetryReprepareRequired},
		{"receipt-read rejection offers no in-card retry", futureIntent("PENDING", nil), rejection(ReadOriginalReceipt), RetryNone},
		{"resume rejection offers no in-card retry", futureIntent("PENDING", nil), rejection(ContinueOriginalSteps), RetryNone},
		{"blocked rejection offers no in-card retry", futureIntent("PENDING", nil), rejection(AccessBlocked), RetryNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.intent.Submission = tc.sub
			NormalizePublicIntent(tc.intent)
			if got := tc.intent.Submission.RetryDisposition; got != tc.want {
				t.Fatalf("retryDisposition=%s want %s", got, tc.want)
			}
			if tc.want == RetryEditSelectionAndResubmit {
				s := tc.intent.Submission
				if !s.Recorded || s.Outcome != RejectedNotConsumed || s.NextAction != EditSelection || s.BusinessOutcome != "REJECTED_NOT_CONSUMED" || s.ConsumptionDisposition != "NOT_CONSUMED" || s.Consumed == nil || *s.Consumed || s.RedispatchSameIntent {
					t.Fatalf("edit-and-resubmit was derived without the full §3 fact set: %+v", s)
				}
			}
		})
	}
}

// §3.7 and §5: an unknown or consumed result never enters the editable-retry path,
// and a client-injected disposition cannot survive the read boundary.
func TestRetryDispositionFailsClosedForUnknownAndConsumed(t *testing.T) {
	cases := []struct {
		name string
		sub  *Submission
		want RetryDisposition
	}{
		{"unobserved request routes to the receipt read", &Submission{Outcome: Unknown, Recorded: false, NextAction: ReadOriginalReceipt}, RetryReadOriginalReceipt},
		{"unobserved request cannot be injected into an editable retry", &Submission{Outcome: Unknown, Recorded: false, NextAction: EditSelection, RetryDisposition: RetryEditSelectionAndResubmit}, RetryReadOriginalReceipt},
		{"recorded-but-unknown cannot be injected into an editable retry", &Submission{Outcome: Unknown, Recorded: true, NextAction: EditSelection, RetryDisposition: RetryEditSelectionAndResubmit}, RetryReadOriginalReceipt},
		{"consumed choice has no retry", func() *Submission {
			consumed := true
			return &Submission{Outcome: Consumed, Recorded: true, NextAction: ReadOriginalReceipt, Consumed: &consumed, RetryDisposition: RetryEditSelectionAndResubmit}
		}(), RetryNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent := futureIntent("PENDING", nil)
			if tc.sub.Outcome == Consumed {
				intent.Commit = &CommitInput{RequestID: "r1"}
			}
			intent.Submission = tc.sub
			NormalizePublicIntent(intent)
			if got := intent.Submission.RetryDisposition; got != tc.want {
				t.Fatalf("retryDisposition=%s want %s (injected %s)", got, tc.want, RetryEditSelectionAndResubmit)
			}
			if intent.Submission.RedispatchSameIntent {
				t.Fatalf("redispatchSameIntent must stay false: %+v", intent.Submission)
			}
		})
	}
}

// A nil intent and a submission-less intent are ordinary reads, not retry states.
func TestRetryDispositionLeavesSubmissionlessIntentAlone(t *testing.T) {
	NormalizePublicIntent(nil)
	intent := futureIntent("PENDING", nil)
	NormalizePublicIntent(intent)
	if intent.Submission != nil {
		t.Fatalf("no submission may be fabricated: %+v", intent.Submission)
	}
}
