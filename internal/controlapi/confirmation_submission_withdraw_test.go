package controlapi_test

import (
	"sync"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

func withdrawalFixture(t *testing.T) (*compositionFixture, c.Intent, c.CommitInput) {
	t.Helper()
	f := newCompositionFixture(t)
	i, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("withdraw-prepare", compositionUpperVariant("upper", "Read only", "Inspect the source without changing files.")))
	if err != nil {
		t.Fatal(err)
	}
	return f, i, c.CommitInput{AISessionID: f.sessionID, IntentID: i.IntentID, Version: i.Version, Purpose: i.Purpose, Action: i.Action, RequestID: "original-uncertain", ContentHash: i.ContentHash, VariantID: "upper"}
}

func TestConfirmationSubmissionWithdrawalFencesLateCommit(t *testing.T) {
	f, i, request := withdrawalFixture(t)
	before, err := f.read(i, request.RequestID)
	if err != nil || before.Submission.Recorded {
		t.Fatalf("read: %+v %v", before.Submission, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		out, err := f.facade.WithdrawConfirmationSubmission(f.ctx, request)
		if err != nil || out.Commit != nil || out.Submission == nil || !out.Submission.Recorded || out.Submission.RetryDisposition != c.RetryEditSelectionAndResubmit {
			t.Fatalf("withdraw: %+v %v", out, err)
		}
	}
	late, err := f.facade.CommitConfirmation(f.ctx, request)
	if err == nil || late.Submission.Outcome != c.RejectedNotConsumed {
		t.Fatalf("late commit: %+v %v", late.Submission, err)
	}
	changed := request
	changed.Selection.ProjectID = "foreign-project"
	if _, err := f.facade.WithdrawConfirmationSubmission(f.ctx, changed); err == nil {
		t.Fatal("same requestId with changed payload accepted")
	}
	if a, b, r := f.counts(t); a != 0 || b != 0 || r != 0 {
		t.Fatalf("objects: %d %d %d", a, b, r)
	}
	request.RequestID = "new-user-confirmation"
	out, err := f.facade.CommitConfirmation(f.ctx, request)
	if err != nil || out.Submission.Outcome != c.Consumed {
		t.Fatalf("explicit new confirmation: %+v %v", out.Submission, err)
	}
}

func TestConfirmationSubmissionWithdrawalPreservesConsumedResult(t *testing.T) {
	f, _, request := withdrawalFixture(t)
	committed, err := f.facade.CommitConfirmation(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	a, b, r := f.counts(t)
	out, err := f.facade.WithdrawConfirmationSubmission(f.ctx, request)
	if err != nil || out.Submission.Outcome != c.Consumed || out.State != committed.State {
		t.Fatalf("consumed withdrawal: %+v %v", out, err)
	}
	x, y, z := f.counts(t)
	if a != x || b != y || r != z {
		t.Fatal("withdrawal changed business objects")
	}
	changed := request
	changed.VariantID = "foreign"
	if _, err = f.facade.WithdrawConfirmationSubmission(f.ctx, changed); err == nil {
		t.Fatal("changed payload accepted")
	}
}

func TestConfirmationSubmissionWithdrawalRacesOriginalCommit(t *testing.T) {
	f, i, request := withdrawalFixture(t)
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	go func() { defer wg.Done(); <-start; _, _ = f.facade.WithdrawConfirmationSubmission(f.ctx, request) }()
	go func() { defer wg.Done(); <-start; _, _ = f.facade.CommitConfirmation(f.ctx, request) }()
	close(start)
	wg.Wait()
	out, err := f.read(i, request.RequestID)
	if err != nil || out.Submission == nil || !out.Submission.Recorded {
		t.Fatalf("race result: %+v %v", out, err)
	}
	a, b, r := f.counts(t)
	if out.Submission.Outcome == c.RejectedNotConsumed {
		if out.Commit != nil || a+b+r != 0 {
			t.Fatal("rejected request produced effects")
		}
	} else if out.Submission.Outcome != c.Consumed || a != 1 || b != 1 || r != 1 {
		t.Fatalf("unexpected race result: %+v objects=%d/%d/%d", out.Submission, a, b, r)
	}
}

func TestConfirmationSubmissionWithdrawalRefusesForeignIdentity(t *testing.T) {
	f, _, request := withdrawalFixture(t)
	request.ContentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := f.facade.WithdrawConfirmationSubmission(f.ctx, request); err == nil {
		t.Fatal("foreign content accepted")
	}
	var count int
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT count(*) FROM confirmation_commit_requests").Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign request persisted: %d %v", count, err)
	}
}

func TestConfirmationSubmissionWithdrawalMCPRetainsExactPayload(t *testing.T) {
	s := newCardLifecycleStack(t)
	intent := s.prepareReviewDecision(s.nextID("withdraw-wire-prepare"), "")
	args := map[string]any{"aiSessionId": intent.AISessionID, "intentId": intent.IntentID, "version": intent.Version, "purpose": string(intent.Purpose), "action": intent.Action, "requestId": s.nextID("withdraw-wire-original"), "contentHash": intent.ContentHash, "variantId": "revise", "selection": map[string]any{}}
	result, out := s.call("confirmation_submission_withdraw", args)
	if result.IsError || out.Intent == nil || out.Intent.Submission == nil || out.Intent.Submission.RetryDisposition != c.RetryEditSelectionAndResubmit {
		t.Fatalf("wire withdrawal: %s", s.lastRaw)
	}
	hash := out.Intent.Submission.PayloadSHA256
	result, out = s.call("confirmation_commit", args)
	if !result.IsError || out.Intent == nil || out.Intent.Commit != nil || out.Intent.Submission.PayloadSHA256 != hash || out.Intent.Submission.Outcome != c.RejectedNotConsumed {
		t.Fatalf("late wire submission: %s", s.lastRaw)
	}
}
