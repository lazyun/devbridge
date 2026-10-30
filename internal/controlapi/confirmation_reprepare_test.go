package controlapi_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/projectregistration"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// PREPARE_NEW_VERSION is a product action name (§11): it replaces a rejected
// intent with a brand-new intentId at version 1 and records lineage only through
// sourceIntentId. These gates drive the formal replacement service against a real
// control plane, real SQLite and the same prepare preflight and commit fence every
// public surface uses.

// appendTopologyMember adds one registered repository to the fixture Project, which
// is the smallest durable change that advances the Project topology revision while
// leaving the frozen Review Decision itself actionable.
func (f *upperLifecycleFixture) appendTopologyMember(t *testing.T, alias string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(f.dataRoot), alias)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("replacement member\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
	if _, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "append-" + alias, ProjectID: f.project, Repositories: []projectregistration.RepositoryInput{{Path: path, Alias: alias}}}); err != nil {
		t.Fatal(err)
	}
}

func (f *upperLifecycleFixture) countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.plane.DB.SQL().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rejectedReviewDecisionSource produces the exact durable shape a replacement must
// consume: a still-PENDING Review decision intent whose only submission is a
// recorded REJECTED_NOT_CONSUMED rejection routed to PREPARE_NEW_VERSION. The
// fourth value is the fixture's intent-row baseline, which also counts the
// lifecycle intents this confirmation flow created before the rejection.
func rejectedReviewDecisionSource(t *testing.T, name string) (*upperLifecycleFixture, c.Intent, c.CommitInput, int) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "approval-for-"+name)
	prepared, commit := prepareLifecycleReviewDecision(t, f, "REVISE", "prepare-"+name)
	f.appendTopologyMember(t, name+"-member")
	result, err := f.facade.CommitConfirmation(f.ctx, commit)
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "PROJECT_TOPOLOGY_CHANGED" || failure.NextAction != c.PrepareNewVersion {
		t.Fatalf("stale topology commit err=%v failure=%+v", err, failure)
	}
	sub := result.Submission
	if sub == nil || !sub.Recorded || sub.Outcome != c.RejectedNotConsumed || sub.Consumed == nil || *sub.Consumed ||
		sub.BusinessOutcome != "REJECTED_NOT_CONSUMED" || sub.ConsumptionDisposition != "NOT_CONSUMED" ||
		sub.RetryDisposition != c.RetryReprepareRequired || !sub.ReprepareAllowed || sub.RedispatchSameIntent {
		t.Fatalf("source rejection receipt=%+v", sub)
	}
	return f, prepared, commit, f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session)
}

func reprepareSourceRequest(f *upperLifecycleFixture, source c.Intent, requestID, feedback string) c.ReprepareInput {
	return c.ReprepareInput{AISessionID: f.session, SourceIntentID: source.IntentID, SourceVersion: source.Version, SourceContentHash: source.ContentHash, RequestID: requestID, ReviewFeedback: feedback}
}

func guidanceApprovalVersion(t *testing.T, f *upperLifecycleFixture, taskID string) *wp.Approval {
	t.Helper()
	g, err := f.facade.WorkflowGuidanceForResource(f.ctx, wp.ResourceScope{AISessionID: f.session, ProjectID: f.project, TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	return g.Approval
}

// G9/G11/G13 + successor: the replacement refreezes current facts, supersedes the
// rejected intent, creates no successor by itself, and is the only intent that ever
// consumes the Decision.
func TestConfirmationReprepareReplacesRejectedIntentAndRemainsTheOnlySuccessorAuthor(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f, source, _, intentsBefore := rejectedReviewDecisionSource(t, name)
	reviewsBefore := f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task)
	receiptsBefore := f.countRows(t, `SELECT count(*) FROM confirmation_commit_requests WHERE ai_session_id=?`, f.session)

	replacement, err := f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, source, "reprepare-"+name, ""))
	if err != nil {
		t.Fatalf("reprepare: %v", err)
	}
	if replacement.IntentID == source.IntentID || replacement.Version != 1 || replacement.Content.SourceIntentID != source.IntentID {
		t.Fatalf("replacement identity=%s version=%d lineage=%q source=%s", replacement.IntentID, replacement.Version, replacement.Content.SourceIntentID, source.IntentID)
	}
	if replacement.ContentHash == source.ContentHash {
		t.Fatal("replacement reused the superseded content hash")
	}
	body := replacement.Content.Review
	frozen := source.Content.Review
	if body == nil || frozen == nil {
		t.Fatalf("replacement body=%+v source=%+v", body, frozen)
	}
	if body.TaskID != f.task || body.FeatureAreaID != f.area || body.ReviewID != f.review || body.DecisionID != wp.DecisionID(f.review) {
		t.Fatalf("replacement carried a business identity override: %+v", body)
	}
	if body.ReviewFeedback != frozen.ReviewFeedback {
		t.Fatalf("unspecified feedback changed the frozen review feedback: %q != %q", body.ReviewFeedback, frozen.ReviewFeedback)
	}
	if body.PreparedTopologyRevision <= frozen.PreparedTopologyRevision || body.PreparedDevelopmentContextHash == "" {
		t.Fatalf("replacement did not refreeze the project snapshot: prepared=%+v current=%+v", frozen, body)
	}
	approval := guidanceApprovalVersion(t, f, f.task)
	if approval == nil || approval.DecisionID != body.DecisionID || approval.DecisionVersion == "" || approval.DecisionVersion != body.PreparedDecisionVersion || body.PreparedDecisionVersion == body.DecisionID {
		t.Fatalf("replacement Decision token=%q guidance=%+v", body.PreparedDecisionVersion, approval)
	}
	if replacement.State != "PENDING" || f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task) != reviewsBefore {
		t.Fatalf("replacement prepare created a successor or left a non-pending state: state=%s reviews=%d", replacement.State, f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task))
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_commit_requests WHERE ai_session_id=?`, f.session) != receiptsBefore {
		t.Fatalf("replacement prepare wrote a submission receipt")
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session) != intentsBefore+1 {
		t.Fatalf("replacement prepare created more than one intent")
	}

	// The rejected source keeps its history and is no longer commandable at all.
	sourceRead, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: source.IntentID, Version: source.Version, Purpose: source.Purpose, ContentHash: source.ContentHash})
	if err != nil {
		t.Fatal(err)
	}
	if sourceRead.State != "SUPERSEDED" || sourceRead.Submission == nil || sourceRead.Submission.Outcome != c.RejectedNotConsumed ||
		sourceRead.Submission.RetryDisposition != c.RetryReprepareRequired || sourceRead.Submission.RedispatchSameIntent {
		t.Fatalf("superseded source=%+v", sourceRead.Submission)
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: source.IntentID, Version: source.Version, Purpose: source.Purpose, Action: source.Action, RequestID: "commit-superseded-" + name, ContentHash: source.ContentHash, VariantID: "revise"}); err == nil {
		t.Fatal("a superseded intent accepted a commit")
	}
	if f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task) != reviewsBefore {
		t.Fatal("committing a superseded intent created a successor Review")
	}

	// Only the replacement consumes the Decision and creates the one successor.
	committed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: replacement.IntentID, Version: replacement.Version, Purpose: replacement.Purpose, Action: replacement.Action, RequestID: "commit-" + replacement.IntentID, ContentHash: replacement.ContentHash, VariantID: replacement.Content.Variants[0].ID})
	if err != nil {
		t.Fatalf("replacement commit: %v", err)
	}
	if committed.State != "COMPLETED" || f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task) != reviewsBefore+1 {
		t.Fatalf("replacement committed without exactly one successor: state=%s err=%v", committed.State, err)
	}
	// G19: a reopened replacement request after a lost response must not fork.
	if _, err = f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, source, "reprepare-fork-"+name, "")); err == nil {
		t.Fatal("reprepared a superseded source intent")
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session) != intentsBefore+1 {
		t.Fatalf("a reopened reprepare created a second replacement: %d", f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session))
	}
}

// G10/G11: the same request replays the same replacement; the same request with a
// different payload is a conflict, never a second intent.
func TestConfirmationReprepareReplaysExactRequestAndRefusesChangedPayload(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f, source, _, intentsBefore := rejectedReviewDecisionSource(t, name)
	request := reprepareSourceRequest(f, source, "reprepare-"+name, "")
	first, err := f.facade.ReprepareConfirmation(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.facade.ReprepareConfirmation(f.ctx, request)
	if err != nil || replay.IntentID != first.IntentID || replay.ContentHash != first.ContentHash || replay.Version != first.Version {
		t.Fatalf("replay=%+v first=%+v err=%v", replay, first, err)
	}
	changed := request
	changed.ReviewFeedback = "Narrower feedback that must not silently replace the frozen request."
	if _, err = f.facade.ReprepareConfirmation(f.ctx, changed); !errors.Is(err, c.ErrRequestConflict) {
		t.Fatalf("changed payload err=%v", err)
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session) != intentsBefore+1 {
		t.Fatalf("replayed or conflicting reprepare created an extra intent: %d", f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session))
	}
}

// G12: once the Decision is no longer actionable, reprepare refuses with a typed
// rejection and creates no intent at all.
func TestConfirmationReprepareRefusesWhenTheDecisionIsNoLongerActionable(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f, source, _, intentsBefore := rejectedReviewDecisionSource(t, name)
	if _, err := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(source)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.facade.ResolveReviewApproval(f.ctx, wp.ResolveInput{AISessionID: f.session, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), Option: "REJECT", RequestID: "operator-reject-" + name}); err != nil {
		t.Fatal(err)
	}
	_, err := f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, source, "reprepare-terminal-"+name, ""))
	var failure *c.Failure
	if err == nil || errors.As(err, &failure) && failure.NextAction == c.EditSelection {
		t.Fatalf("reprepared a terminal Decision: err=%v failure=%+v", err, failure)
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session) != intentsBefore {
		t.Fatalf("a terminal Decision still produced a replacement: %d", f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session))
	}
	if f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task) != 1 {
		t.Fatal("a refused reprepare created a successor Review")
	}
}

// G18: two concurrent replacement requests serialize on the source intent, so only
// one legal replacement exists and the loser is refused.
func TestConfirmationReprepareConcurrentRequestsCreateExactlyOneReplacement(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f, source, _, intentsBefore := rejectedReviewDecisionSource(t, name)
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, source, "reprepare-race-"+name+"-"+string(rune('a'+i)), ""))
			results[i] = err
		}(i)
	}
	wg.Wait()
	if (results[0] == nil) == (results[1] == nil) {
		t.Fatalf("concurrent reprepare did not produce exactly one legal result: %v / %v", results[0], results[1])
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session) != intentsBefore+1 {
		t.Fatalf("concurrent reprepare created %d intents", f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session))
	}
}

// §10 closed client surface: only the source identity is accepted, and a source that
// never carried a no-effect rejection is refused without any write.
func TestConfirmationReprepareRefusesForeignOrUnrejectedSources(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "approval-for-"+name)
	pending, _ := prepareLifecycleReviewDecision(t, f, "REVISE", "prepare-"+name)
	before := f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session)

	request := reprepareSourceRequest(f, pending, "reprepare-unattempted-"+name, "")
	if _, err := f.facade.ReprepareConfirmation(f.ctx, request); err == nil {
		t.Fatal("reprepared an intent with no recorded submission")
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session) != before {
		t.Fatalf("an unattempted source still produced a replacement: %d", f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session))
	}
	wrongHash := request
	wrongHash.SourceContentHash = strings.Repeat("a", 64)
	if _, err := f.facade.ReprepareConfirmation(f.ctx, wrongHash); err == nil {
		t.Fatal("reprepare accepted a mismatched source contentHash")
	}
	for _, missing := range []func(in *c.ReprepareInput){
		func(in *c.ReprepareInput) { in.RequestID = "" },
		func(in *c.ReprepareInput) { in.SourceVersion = 0 },
		func(in *c.ReprepareInput) { in.SourceContentHash = "" },
	} {
		invalid := request
		missing(&invalid)
		if _, err := f.facade.ReprepareConfirmation(f.ctx, invalid); err == nil {
			t.Fatal("reprepare accepted an incomplete closed request")
		}
	}
	if f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session) != before {
		t.Fatalf("a refused reprepare wrote an intent: %d", f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session))
	}
}

// G18: the replacement prepare races a competing user command on the same rejected
// intent. The fence keeps refusing the rejected intent while the replacement prepare
// is the one legal result, and a prepare never produces a successor Review.
func TestConfirmationReprepareRacesACompetingCommitOnTheRejectedIntent(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f, source, commit, intentsBefore := rejectedReviewDecisionSource(t, name)
	reviewsBefore := f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task)
	receiptsBefore := f.countRows(t, `SELECT count(*) FROM confirmation_commit_requests WHERE ai_session_id=?`, f.session)
	competing := commit
	competing.RequestID = "contending-commit-" + name

	errs := concurrent(2, func(index int) error {
		if index == 0 {
			_, err := f.facade.CommitConfirmation(f.ctx, competing)
			return err
		}
		_, err := f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, source, "reprepare-race-commit-"+name, ""))
		return err
	})
	var failure *c.Failure
	if errs[0] == nil || !errors.As(errs[0], &failure) || failure.NextAction != c.PrepareNewVersion {
		t.Fatalf("a fresh commit on a REPREPARE_REQUIRED rejection was accepted or misrouted: %v", errs[0])
	}
	if errs[1] != nil {
		t.Fatalf("the formal replacement lost to a command the fence must refuse: %v", errs[1])
	}
	if rows := f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session); rows != intentsBefore+1 {
		t.Fatalf("the race left %d intents over the baseline %d, want exactly one replacement", rows, intentsBefore)
	}
	if reviews := f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task); reviews != reviewsBefore {
		t.Fatalf("the race created a successor Review: %d -> %d", reviewsBefore, reviews)
	}
	// Both losers record their own refusal and nothing more: the rejected intent stays
	// terminal, and the replacement prepare stays receipt-free.
	if rows := f.countRows(t, `SELECT count(*) FROM confirmation_commit_requests WHERE intent_id=? AND ai_session_id=?`, source.IntentID, f.session); rows != 2 {
		t.Fatalf("the source intent carries %d submission rows, want its rejection plus the refused retry", rows)
	}
	if rows := f.countRows(t, `SELECT count(*) FROM confirmation_commit_requests WHERE ai_session_id=?`, f.session); rows != receiptsBefore+1 {
		t.Fatalf("the race wrote %d receipts over the baseline %d", rows, receiptsBefore)
	}
}

// G18 + §5: the Decision moves underneath an in-flight replacement prepare. Whoever
// wins, the read surfaces and the commit writer must then classify the surviving
// replacement identically - a prepared confirmation never outranks the Decision it
// targeted, and no successor Review can appear from either path.
func TestConfirmationReprepareRacesTheDecisionResolutionAndLeavesOneAuthority(t *testing.T) {
	name := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	f, source, _, intentsBefore := rejectedReviewDecisionSource(t, name)
	reviewsBefore := f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task)
	var replacement *c.Intent

	errs := concurrent(2, func(index int) error {
		if index == 0 {
			_, err := f.facade.ResolveReviewApproval(f.ctx, wp.ResolveInput{AISessionID: f.session, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), Option: "REJECT", RequestID: "operator-race-" + name})
			return err
		}
		out, err := f.facade.ReprepareConfirmation(f.ctx, reprepareSourceRequest(f, source, "reprepare-race-decision-"+name, ""))
		if err == nil {
			replacement = &out
		}
		return err
	})
	if errs[0] != nil {
		requireNodeFailure(t, errs[0], "TASK_ACTION_PENDING")
		if replacement == nil || errs[1] != nil {
			t.Fatalf("neither chain won: %v / %v", errs[0], errs[1])
		}
		if receipts := f.countRows(t, `SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.DecisionID(f.review)); receipts != 0 {
			t.Fatal("losing operator resolved the Decision")
		}
		return
	}
	if rows := f.countRows(t, `SELECT count(*) FROM confirmation_intents WHERE ai_session_id=?`, f.session); rows > intentsBefore+1 || rows < intentsBefore {
		t.Fatalf("the race left %d intents over the baseline %d", rows, intentsBefore)
	}
	if reviews := f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task); reviews != reviewsBefore {
		t.Fatalf("the race created a successor Review: %d -> %d", reviewsBefore, reviews)
	}
	// The Decision is terminal now, whichever order the two took: the operator
	// resolution left its durable receipt, and the Task projection no longer carries a
	// pending approval for it.
	if receipts := f.countRows(t, `SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.DecisionID(f.review)); receipts != 1 {
		t.Fatalf("the concurrent resolution recorded %d Decision receipts, want exactly one", receipts)
	}
	if replacement == nil {
		var failure *c.Failure
		if !errors.As(errs[1], &failure) || failure.NextAction == c.EditSelection {
			t.Fatalf("reprepare against a terminal Decision failed without a typed refusal: %v", errs[1])
		}
		return
	}
	options := optionsRead(t, f, *replacement)
	commitErr := func() error {
		_, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: replacement.IntentID, Version: replacement.Version, Purpose: replacement.Purpose, Action: replacement.Action, RequestID: "commit-raced-replacement-" + name, ContentHash: replacement.ContentHash, VariantID: replacement.Content.Variants[0].ID})
		return err
	}()
	if commitErr == nil || options.Eligibility.Available {
		t.Fatalf("a replacement whose Decision was resolved concurrently is still committable: options=%v commit=%v", jsonCard(options.Eligibility), commitErr)
	}
	// One reducer, one classification: whatever the terminal condition is named, the
	// options read and the writer refuse it identically. The per-variant failure is
	// where the options surface carries the route, so parity is asserted there.
	var failure *c.Failure
	if !errors.As(commitErr, &failure) || failure.Code != options.Eligibility.ReasonCode {
		t.Fatalf("commit refusal=%+v options eligibility=%s", failure, jsonCard(options.Eligibility))
	}
	// §13: a terminal Decision never comes back as an editable retry, and never as a
	// resend of the original request.
	if failure.NextAction != c.PrepareNewVersion && failure.NextAction != c.ReadCurrentTarget {
		t.Fatalf("terminal refusal routed to %q", failure.NextAction)
	}
	var routed bool
	for _, variant := range options.Variants {
		for _, refusal := range variant.Failures {
			if refusal.Code != failure.Code {
				continue
			}
			if refusal.NextAction != failure.NextAction {
				t.Fatalf("variant %s routed %q while the writer routed %q", variant.ID, refusal.NextAction, failure.NextAction)
			}
			routed = true
		}
	}
	if !routed {
		t.Fatalf("no options variant carried the writer's %s refusal: %s", failure.Code, jsonCard(options.Variants))
	}
	if reviews := f.countRows(t, `SELECT count(*) FROM reviews WHERE task_id=?`, f.task); reviews != reviewsBefore {
		t.Fatalf("the refused replacement commit created a successor: %d -> %d", reviewsBefore, reviews)
	}
}
