package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// invalidResultRawResult is a retained raw Review result that is not a usable
// formal result: it claims APPROVAL_READY without carrying a proposal, so the
// write-side materializer records it as INVALID (reason RESULT_INVALID).
const invalidResultRawResult = `{"version":1,"reviewDisposition":"APPROVAL_READY","summary":"missing required proposal"}`

// orphanInvalidMaterialization reproduces the durable shape the live Project was
// observed in: the exact raw result IS retained as an INVALID materialization,
// but no review_approval_decisions row exists for its resolution Decision. The
// materialization is written by the production writer and the Review is then
// completed, so the completion pass skips the Review (its candidate query
// excludes Reviews that already carry a materialization) and the Decision row is
// never created.
func orphanInvalidMaterialization(t *testing.T, f *reviewApprovalFixture, reviewID, runID, raw string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordReviewResultMaterializationTx(ctx, tx, reviewID, runID, "INVALID", raw, "RESULT_INVALID", "Review result is not a usable formal result", "", "", f.now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func decisionRowCount(t *testing.T, f *reviewApprovalFixture, decisionID string) int {
	t.Helper()
	var n int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM review_approval_decisions WHERE decision_id=?`, decisionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func receiptRowCount(t *testing.T, f *reviewApprovalFixture, decisionID string) int {
	t.Helper()
	var n int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, decisionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// reviewDecisionSnapshotFor reads the one Decision reducer through an ordinary
// read transaction, the same way every read surface does.
func reviewDecisionSnapshotFor(t *testing.T, f *reviewApprovalFixture, decisionID string) ReviewDecisionSnapshot {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot, err := reviewDecisionSnapshotTx(ctx, tx, decisionID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (f *reviewApprovalFixture) prepareReviewDecision(t *testing.T, ctx context.Context, action string, body *confirmation.ReviewBody, request string) confirmation.Intent {
	t.Helper()
	prepared, err := NewConfirmationStore(f.db).Prepare(ctx, f.project, confirmation.PrepareInput{AISessionID: f.session, RequestID: request, SubmittedHash: workflowHash(request), Purpose: confirmation.Review, Action: action, Review: body, Variants: []confirmation.Variant{{ID: "revise", Label: "同一任务重新审查", Summary: "Re-review the same Task"}}})
	if err != nil {
		t.Fatalf("prepare %s: %v", request, err)
	}
	return prepared
}

func reviewDecisionCommit(t *testing.T, f *reviewApprovalFixture, prepared confirmation.Intent, request string) (confirmation.Intent, error) {
	t.Helper()
	in := confirmation.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: request, ContentHash: prepared.ContentHash, VariantID: "revise", SubmittedHash: prepared.ContentHash}
	out, _, err := NewConfirmationStore(f.db).BeginCommit(aisession.WithCallerScope(context.Background(), "owned-caller"), in, nil)
	return out, err
}

func invalidResultBody(f *reviewApprovalFixture, reviewID, feedback, decisionVersion string) *confirmation.ReviewBody {
	return &confirmation.ReviewBody{TaskID: f.candidate.TaskID, FeatureAreaID: f.area, ReviewID: reviewID, DecisionID: wp.InvalidResultDecisionID(reviewID), PreparedDecisionVersion: decisionVersion, ReviewFeedback: feedback}
}

func taskGate(t *testing.T, f *reviewApprovalFixture) (currentReview, phase, status string) {
	t.Helper()
	if err := f.db.sql.QueryRow(`SELECT COALESCE(current_review_id,''),phase,status FROM tasks WHERE id=?`, f.candidate.TaskID).Scan(&currentReview, &phase, &status); err != nil {
		t.Fatal(err)
	}
	return
}

func successorCounts(t *testing.T, f *reviewApprovalFixture, decisionID string) (reviews, runs, starts, resolutions, authorityRows int) {
	t.Helper()
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM reviews WHERE task_id=?`, f.candidate.TaskID).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM runs WHERE review_id IN (SELECT id FROM reviews WHERE task_id=?)`, f.candidate.TaskID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_review_start_requests WHERE review_id IN (SELECT id FROM reviews WHERE task_id=?)`, f.candidate.TaskID).Scan(&starts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM review_invalid_result_resolutions WHERE decision_id=?`, decisionID).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM review_approval_decisions WHERE decision_id=?`, decisionID).Scan(&authorityRows); err != nil {
		t.Fatal(err)
	}
	return
}

// G1/G2/G3: the orphan shape the live Project is in now agrees across the read
// projection and the commit writer, because both resolve the same reducer. The
// writer repairs the missing Decision row from the retained raw result inside the
// transaction that consumes it, so the same-Task successor path runs exactly once.
func TestInvalidReviewResultOrphanDecisionReadAndCommitAgreeAndResolveOnce(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	a := f.start(t)
	orphanInvalidMaterialization(t, f, a.Review.ID, a.Run.ID, invalidResultRawResult)
	f.complete(t, a, invalidResultRawResult)

	decisionID := wp.InvalidResultDecisionID(a.Review.ID)
	if got := decisionRowCount(t, f, decisionID); got != 0 {
		t.Fatalf("fixture must start without a durable Decision row, got %d", got)
	}
	snapshot := reviewDecisionSnapshotFor(t, f, decisionID)
	if snapshot.Status != reviewDecisionRecoverable || !snapshot.CanCommit() || !snapshot.CanPrepare() || !snapshot.CanReprepare() || snapshot.ProjectedStatus() != reviewDecisionPending {
		t.Fatalf("orphan Decision snapshot=%+v", snapshot)
	}
	g := f.guidance(t, "REVIEWING", "REVIEW_RESULT_RESOLUTION_REQUIRED")
	if g.Approval == nil || g.Approval.Status != "PENDING" || g.Approval.DecisionID != decisionID || g.Approval.DecisionVersion != snapshot.Version || g.Approval.ReasonCode != "" {
		t.Fatalf("read projection Approval=%+v reducer=%+v", g.Approval, snapshot)
	}
	if !slices.Equal(g.AvailableActions, snapshot.AvailableActions) {
		t.Fatalf("projection actions=%v reducer actions=%v", g.AvailableActions, snapshot.AvailableActions)
	}
	// §9: the workspace still offers REQUEST_RE_REVIEW because the Decision really is
	// actionable, so its copy has to send the user to a freshly prepared (or
	// formally replaced) confirmation and must never imply an unknown outcome or a
	// non-pending Decision the writer would then contradict.
	if !strings.Contains(g.NextAction.Message, "confirmation_reprepare") || !strings.Contains(g.NextAction.Message, "preparing a new confirmation") ||
		strings.Contains(g.NextAction.Message, "no longer pending") || strings.Contains(g.NextAction.Message, "UNKNOWN") {
		t.Fatalf("workspace re-review guidance does not point at the formal replacement prepare: %q", g.NextAction.Message)
	}

	prepared := f.prepareReviewDecision(t, ctx, "REVISE", invalidResultBody(f, a.Review.ID, "Re-review the retained result after resolving its materialization failure.", snapshot.Version), "authority-prepare")
	if prepared.Content.Review.PreparedDecisionVersion != snapshot.Version {
		t.Fatalf("prepare dropped the frozen Decision version: %q", prepared.Content.Review.PreparedDecisionVersion)
	}
	if _, err := reviewDecisionCommit(t, f, prepared, "authority-commit"); err != nil {
		t.Fatalf("commit of a reducer-PENDING Decision: %v", err)
	}
	// The repair materialized exactly the Decision row the completion writer
	// should have created, and consumed nothing else.
	if got := decisionRowCount(t, f, decisionID); got != 1 {
		t.Fatalf("repaired Decision rows=%d", got)
	}
	if got := receiptRowCount(t, f, decisionID); got != 0 {
		t.Fatalf("confirmation commit consumed the Decision: receipts=%d", got)
	}
	reviews, _, _, resolutions, _ := successorCounts(t, f, decisionID)
	if reviews != 1 || resolutions != 0 {
		t.Fatalf("confirmation commit created business effects: reviews=%d resolutions=%d", reviews, resolutions)
	}
	if current, phase, status := taskGate(t, f); current != a.Review.ID || phase != "REVIEWING" || status != "ACTIVE" {
		t.Fatalf("Task gate moved by the commit: %s/%s/%s", current, phase, status)
	}

	in := f.input(a, "REVISE")
	in.DecisionID, in.RequestID = decisionID, "confirmation_"+confirmation.ValueHash([]string{prepared.IntentID, "RESOLVE_REVIEW_DECISION"})[:32]
	ctx = taskaction.WithIntent(ctx, prepared.IntentID)
	out, err := f.store.Resolve(ctx, in)
	if err != nil || out.SuccessorReviewID == "" || out.SuccessorRunID == "" || out.Task == nil || out.Task.Phase != task.PhaseReviewing {
		t.Fatalf("repaired Decision resolution=%+v err=%v", out, err)
	}
	if _, err = f.store.Resolve(ctx, in); err != nil {
		t.Fatalf("repaired Decision resolution replay: %v", err)
	}
	reviews, runs, starts, resolutions, authority := successorCounts(t, f, decisionID)
	if reviews != 2 || runs != 2 || starts != 2 || resolutions != 1 || authority != 1 {
		t.Fatalf("successor graph reviews=%d runs=%d starts=%d resolutions=%d decisionRows=%d", reviews, runs, starts, resolutions, authority)
	}
	if got := receiptRowCount(t, f, decisionID); got != 1 {
		t.Fatalf("resolution receipts=%d", got)
	}
	if current, _, _ := taskGate(t, f); current == a.Review.ID || current == "" {
		t.Fatalf("Task gate did not move to the successor: %s", current)
	}
	// The consumed Decision is no longer actionable anywhere, and its version moved.
	consumed := reviewDecisionSnapshotFor(t, f, decisionID)
	if consumed.Status != reviewDecisionSuperseded || consumed.CanCommit() || consumed.CanReprepare() || consumed.ProjectedStatus() == reviewDecisionPending || consumed.ReasonCode != "DECISION_SUPERSEDED" {
		t.Fatalf("consumed Decision snapshot=%+v", consumed)
	}
	if consumed.Version == snapshot.Version || consumed.DecisionID == consumed.Version {
		t.Fatalf("Decision version is not a distinct status token: %q", consumed.Version)
	}
}

// G3/G5: a durable PENDING Decision commits, and a frozen version token that no
// longer matches the reducer's token is refused precisely, with zero side effects.
func TestInvalidReviewResultDecisionCommitValidatesTheFrozenVersionToken(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	a := f.start(t)
	f.complete(t, a, invalidResultRawResult)
	decisionID := wp.InvalidResultDecisionID(a.Review.ID)
	pending := reviewDecisionSnapshotFor(t, f, decisionID)
	if pending.Status != reviewDecisionPending || !pending.Durable || !pending.CanCommit() {
		t.Fatalf("normal invalid-result Decision snapshot=%+v", pending)
	}
	if g := f.guidance(t, "REVIEWING", "REVIEW_RESULT_RESOLUTION_REQUIRED"); g.Approval == nil || g.Approval.DecisionVersion != pending.Version {
		t.Fatalf("projection version=%+v reducer=%q", g.Approval, pending.Version)
	}

	drift := invalidResultBody(f, a.Review.ID, "Re-review the retained result.", strings.Repeat("0", 64))
	stale := f.prepareReviewDecision(t, ctx, "REVISE", drift, "drift-prepare")
	_, err := reviewDecisionCommit(t, f, stale, "drift-commit")
	var failure *confirmation.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("commit with a drifted Decision token err=%v", err)
	}
	if failure.Code != "DECISION_VERSION_CHANGED" || failure.Stage != "UNIQUE_CONSUMPTION" || failure.NextAction != confirmation.PrepareNewVersion {
		t.Fatalf("drift failure=%+v", failure)
	}
	if got := receiptRowCount(t, f, decisionID); got != 0 {
		t.Fatalf("refused commit consumed the Decision: receipts=%d", got)
	}
	if reviews, _, _, resolutions, _ := successorCounts(t, f, decisionID); reviews != 1 || resolutions != 0 {
		t.Fatalf("refused commit created successor effects: reviews=%d resolutions=%d", reviews, resolutions)
	}
	if _, e := NewConfirmationStore(f.db).DiscardProposal(ctx, confirmation.ReadInput{AISessionID: f.session, IntentID: stale.IntentID, Version: stale.Version, Purpose: stale.Purpose, ContentHash: stale.ContentHash}); e != nil {
		t.Fatal(e)
	}
	// The same intent content prepared with the current token commits.
	current := f.prepareReviewDecision(t, ctx, "REVISE", invalidResultBody(f, a.Review.ID, "Re-review the retained result.", pending.Version), "current-prepare")
	if _, err = reviewDecisionCommit(t, f, current, "current-commit"); err != nil {
		t.Fatalf("commit with the current Decision token: %v", err)
	}
}

// G4: once the Decision is terminal, no read surface offers its resolution action
// and no write entry accepts a frozen confirmation of it.
func TestInvalidReviewResultTerminalDecisionOffersNothingAndCommitsNothing(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	a := f.start(t)
	f.complete(t, a, invalidResultRawResult)
	decisionID := wp.InvalidResultDecisionID(a.Review.ID)
	pending := reviewDecisionSnapshotFor(t, f, decisionID)
	in := f.input(a, "REVISE")
	in.DecisionID, in.RequestID = decisionID, "terminal-resolve"
	if out, err := f.store.Resolve(ctx, in); err != nil || out.SuccessorReviewID == "" {
		t.Fatalf("resolve: %+v %v", out, err)
	}
	superseded := reviewDecisionSnapshotFor(t, f, decisionID)
	if superseded.Status != reviewDecisionSuperseded || superseded.ReasonCode != "DECISION_SUPERSEDED" || superseded.CanCommit() {
		t.Fatalf("superseded snapshot=%+v", superseded)
	}
	g, err := f.store.Read(ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	if containsString(g.AvailableActions, "REQUEST_RE_REVIEW") {
		t.Fatalf("projection still offers the resolved Decision action: %+v", g.AvailableActions)
	}
	if g.Approval != nil && g.Approval.DecisionID == decisionID {
		t.Fatalf("projection still presents the resolved Decision: %+v", g.Approval)
	}
	// The frozen Review identity is itself gone, so the writer refuses on the
	// earlier identity gate; the Decision gate below is the same reducer's view.
	stale := f.prepareReviewDecision(t, ctx, "REVISE", invalidResultBody(f, a.Review.ID, "Re-review the retained result.", pending.Version), "terminal-prepare")
	_, err = reviewDecisionCommit(t, f, stale, "terminal-commit")
	var failure *confirmation.Failure
	if !errors.As(err, &failure) || failure.Code != "CURRENT_REVIEW_CHANGED" {
		t.Fatalf("commit of a superseded Decision err=%v failure=%+v", err, failure)
	}
	if reviews, _, _, resolutions, _ := successorCounts(t, f, decisionID); reviews != 2 || resolutions != 1 {
		t.Fatalf("refused commit duplicated the successor graph: reviews=%d resolutions=%d", reviews, resolutions)
	}
}

// G2: the Decision gate keeps its own code even while the frozen Review identity
// and Task phase still match, so a terminal Decision is never reported as a mere
// version drift. This injected receipt is the torn durable state that gate
// defends.
func TestConfirmationDecisionGateCodesTerminalAuthorityPrecisely(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	a := f.start(t)
	f.complete(t, a, invalidResultRawResult)
	decisionID := wp.InvalidResultDecisionID(a.Review.ID)
	pending := reviewDecisionSnapshotFor(t, f, decisionID)
	if pending.Status != reviewDecisionPending {
		t.Fatalf("snapshot=%+v", pending)
	}
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO decision_resolution_receipts(decision_id,project_id,decision_kind,subject_kind,subject_id,option,request_id,result_kind,result_id,resolved_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, decisionID, f.project, wp.KindInvalidReviewResultResolution, "TASK_REVIEW_GATE", f.candidate.TaskID, "REVISE", "injected-resolve", "TASK_REVIEW_GATE", a.Review.ID, formatTime(f.now)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	torn := reviewDecisionSnapshotFor(t, f, decisionID)
	if torn.Status != reviewDecisionConsumed || torn.ReasonCode != "DECISION_NOT_PENDING" || torn.CanCommit() {
		t.Fatalf("torn snapshot=%+v", torn)
	}
	prepared := f.prepareReviewDecision(t, ctx, "REVISE", invalidResultBody(f, a.Review.ID, "Re-review the retained result.", pending.Version), "torn-prepare")
	_, err = reviewDecisionCommit(t, f, prepared, "torn-commit")
	var failure *confirmation.Failure
	if !errors.As(err, &failure) || failure.Code != "DECISION_NOT_PENDING" {
		t.Fatalf("commit against a consumed Decision err=%v failure=%+v", err, failure)
	}
}

// An abandoned Task resolves its Decision with no successor: the reducer reports
// it consumed and no longer pending, never as a version drift.
func TestInvalidReviewResultAbandonedDecisionCodesNotPending(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	a := f.start(t)
	f.complete(t, a, invalidResultRawResult)
	decisionID := wp.InvalidResultDecisionID(a.Review.ID)
	pending := reviewDecisionSnapshotFor(t, f, decisionID)
	in := f.input(a, "REJECT")
	in.DecisionID, in.RequestID = decisionID, "abandon-resolve"
	in.ReviewFeedback = ""
	if out, err := f.store.Resolve(ctx, in); err != nil || out.Task == nil || out.Task.Status != task.StatusCanceled {
		t.Fatalf("abandon: %+v %v", out, err)
	}
	abandoned := reviewDecisionSnapshotFor(t, f, decisionID)
	if abandoned.Status != reviewDecisionConsumed || abandoned.ReasonCode != "DECISION_NOT_PENDING" || abandoned.CanCommit() {
		t.Fatalf("abandoned snapshot=%+v", abandoned)
	}
	stale := f.prepareReviewDecision(t, ctx, "REVISE", invalidResultBody(f, a.Review.ID, "Re-review the retained result.", pending.Version), "abandoned-prepare")
	_, err := reviewDecisionCommit(t, f, stale, "abandoned-commit")
	var failure *confirmation.Failure
	// The canceled Task fails the earlier phase gate; the reducer independently
	// reports the Decision as consumed and no longer pending.
	if !errors.As(err, &failure) || failure.Code != "TASK_PHASE_CHANGED" {
		t.Fatalf("commit of a consumed Decision err=%v failure=%+v", err, failure)
	}
}

// A Decision identity with no durable backing at all is not a version drift: it is
// its own code, and no surface may present it as pending.
func TestInvalidReviewResultWithoutAuthorityIsNotPending(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	a := f.start(t)
	unknown := reviewDecisionSnapshotFor(t, f, "NOT_A_DECISION:review_missing")
	if unknown.Status != reviewDecisionNoAuthority || unknown.ReasonCode != "DECISION_NOT_PENDING" || unknown.CanPrepare() {
		t.Fatalf("unknown Decision identity snapshot=%+v", unknown)
	}
	running := reviewDecisionSnapshotFor(t, f, wp.InvalidResultDecisionID(a.Review.ID))
	if running.Status != reviewDecisionNoAuthority || running.ReasonCode != "DECISION_NOT_PENDING" {
		t.Fatalf("running Review snapshot=%+v", running)
	}
	prepared := f.prepareReviewDecision(t, ctx, "REVISE", invalidResultBody(f, a.Review.ID, "Re-review the retained result.", running.Version), "no-authority-prepare")
	_, err := reviewDecisionCommit(t, f, prepared, "no-authority-commit")
	var failure *confirmation.Failure
	if !errors.As(err, &failure) || failure.Code != "DECISION_NOT_PENDING" {
		t.Fatalf("commit without authority err=%v failure=%+v", err, failure)
	}
}

// The reducer's token is a status identity, never the Decision ID: it is stable
// across repeated reads of identical state and moves when the state moves.
func TestReviewDecisionVersionTokenIsStableAndDistinct(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	f.complete(t, a, invalidResultRawResult)
	decisionID := wp.InvalidResultDecisionID(a.Review.ID)
	first := reviewDecisionSnapshotFor(t, f, decisionID)
	second := reviewDecisionSnapshotFor(t, f, decisionID)
	if first.Version != second.Version || first.Version == "" {
		t.Fatalf("version unstable: %q vs %q", first.Version, second.Version)
	}
	if first.Version == decisionID || strings.HasPrefix(first.Version, wp.KindInvalidReviewResultResolution) {
		t.Fatalf("version carries the Decision identity: %q", first.Version)
	}
	if len(first.Version) != 64 {
		t.Fatalf("version token shape: %q", first.Version)
	}
}
