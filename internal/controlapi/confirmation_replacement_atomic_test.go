package controlapi_test

import (
	"errors"
	"reflect"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
)

func TestConfirmationReprepareUnsubmittedStaleAtomicReceipt(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "stale-approval")
	source, _ := prepareLifecycleReviewDecision(t, f, "REVISE", "stale-source")
	request := reprepareSourceRequest(f, source, "stale-replacement", "")
	if _, err := f.facade.ReprepareConfirmation(f.ctx, request); err == nil {
		t.Fatal("current unsubmitted source was replaced")
	}
	f.appendTopologyMember(t, "stale-member")
	before := f.countRows(t, `SELECT count(*) FROM confirmation_intents`)
	if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER replacement_fault BEFORE INSERT ON confirmation_replacement_receipts BEGIN SELECT RAISE(ABORT,'replacement receipt fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.facade.ReprepareConfirmation(f.ctx, request); err == nil {
		t.Fatal("receipt fault was not reached")
	}
	stored, err := f.plane.Confirmations.Get(f.ctx, f.session, source.IntentID)
	if err != nil || stored.State != "PENDING" || f.countRows(t, `SELECT count(*) FROM confirmation_intents`) != before || f.countRows(t, `SELECT count(*) FROM confirmation_replacement_receipts`) != 0 {
		t.Fatalf("receipt failure left a durable prefix: %s %v", stored.State, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER replacement_fault`); err != nil {
		t.Fatal(err)
	}
	replacement, err := f.facade.ReprepareConfirmation(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.IntentID == source.IntentID || replacement.Version != 1 || replacement.ReplacementReceipt == nil || replacement.ReplacementReceipt.Outcome != "REPLACED" {
		t.Fatalf("incomplete replacement: %+v", replacement)
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_commit_requests WHERE intent_id IN (?,?)`, source.IntentID, replacement.IntentID) != 0 {
		t.Fatal("replacement fabricated a submission")
	}
	// Advancing the replacement and restarting Core must not cause exact replay
	// to consult today's guards, overwrite the receipt, or recreate the child.
	f.appendTopologyMember(t, "next-stale-member")
	if _, err = f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, replacement, "next-replacement", "")); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane = nil
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.facade.ReprepareConfirmation(f.ctx, request)
	if err != nil || replay.IntentID != replacement.IntentID || replay.State != "SUPERSEDED" || !reflect.DeepEqual(replay.ReplacementReceipt, replacement.ReplacementReceipt) {
		t.Fatalf("cold replay lost exact historical receipt/state: %+v %v", replay, err)
	}
	read, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: source.IntentID, Version: source.Version, Purpose: source.Purpose, ContentHash: source.ContentHash, RequestID: request.RequestID})
	if err != nil || !reflect.DeepEqual(read.ReplacementReceipt, replacement.ReplacementReceipt) || read.Submission != nil {
		t.Fatalf("replacement readback lost receipt or fabricated submission: %+v %v", read, err)
	}
	changed := request
	changed.ReviewFeedback = "Changed submitted preparation input"
	changed.SubmittedHash = request.SubmittedHash // Caller digests cannot hide changed input.
	if _, err = f.facade.ReprepareConfirmation(f.ctx, changed); !errors.Is(err, c.ErrRequestConflict) {
		t.Fatalf("changed replay: %v", err)
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_replacement_receipts`) != 2 {
		t.Fatal("replay created a new replacement")
	}
}

func TestConfirmationPrepareCannotBypassReplacementProtocol(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "bypass-approval")
	source, _ := prepareLifecycleReviewDecision(t, f, "REVISE", "bypass-source")
	in := source.Content
	in.RequestID = "bypass-new"
	in.SourceIntentID = source.IntentID
	before := f.countRows(t, `SELECT count(*) FROM confirmation_intents`)
	if _, err := f.facade.PrepareConfirmation(f.ctx, in); err == nil {
		t.Fatal("public prepare bypassed replacement")
	}
	if _, err := f.plane.Confirmations.Prepare(f.ctx, f.project, in); err == nil {
		t.Fatal("store prepare bypassed replacement")
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents`) != before {
		t.Fatal("bypass wrote an intent")
	}
}

func TestConfirmationReplacementReadDoesNotReturnAnotherSubmission(t *testing.T) {
	f, source, _, _ := rejectedReviewDecisionSource(t, "replacement-request-read")
	request := reprepareSourceRequest(f, source, "read-replacement", "")
	out, err := f.facade.ReprepareConfirmation(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	input := c.ReadInput{AISessionID: f.session, IntentID: source.IntentID, Version: source.Version, Purpose: source.Purpose, ContentHash: source.ContentHash, RequestID: request.RequestID}
	read, err := f.facade.ReadConfirmation(f.ctx, input)
	if err != nil || read.Submission != nil || !reflect.DeepEqual(read.ReplacementReceipt, out.ReplacementReceipt) {
		t.Fatalf("replacement request leaked unrelated submission: %+v %v", read, err)
	}
	input.RequestID, input.ReplacementRequestID = "", request.RequestID
	read, err = f.facade.ReadConfirmation(f.ctx, input)
	if err != nil || !reflect.DeepEqual(read.ReplacementReceipt, out.ReplacementReceipt) || read.Submission != nil {
		t.Fatalf("explicit replacement receipt read: %+v %v", read, err)
	}
	input.ReplacementRequestID = "absent-replacement"
	read, err = f.facade.ReadConfirmation(f.ctx, input)
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "REPLACEMENT_RECEIPT_NOT_FOUND" || read.State != "SUPERSEDED" || read.Submission != nil || read.ReplacementReceipt != nil {
		t.Fatalf("absent receipt changed source or fabricated outcome: %+v %v", read, err)
	}
	input.ReplacementRequestID = request.RequestID
	input.IntentID, input.ContentHash = out.IntentID, out.ContentHash
	read, err = f.facade.ReadConfirmation(f.ctx, input)
	if err == nil || read.Submission != nil || read.ReplacementReceipt != nil {
		t.Fatalf("cross-source request read: %+v %v", read, err)
	}
}

func TestConfirmationReplacementRequiresCurrentDecisionSelector(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "selector-approval")
	old, _ := prepareLifecycleReviewDecision(t, f, "REVISE", "selector-old")
	if _, err := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(old)); err != nil {
		t.Fatal(err)
	}
	current, _ := prepareLifecycleReviewDecision(t, f, "REVISE", "selector-current")
	f.appendTopologyMember(t, "selector-drift")
	before := f.countRows(t, `SELECT count(*) FROM confirmation_intents`)
	if _, err := f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, old, "selector-loser", "")); err == nil {
		t.Fatalf("non-current pending intent gained replacement authority: %v", err)
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents`) != before {
		t.Fatal("non-current source created a child")
	}
	if _, err := f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, current, "selector-winner", "")); err != nil {
		t.Fatal(err)
	}
}
