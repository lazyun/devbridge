package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func reviewFixtureSelection(t *testing.T, db *DB, configID string) *codexconfig.BackendSelection {
	t.Helper()
	config, err := NewCodexConfigStore(db).Get(context.Background(), configID)
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("sqlite-review-fixture")
	return &selection
}

func reviewFixture(t *testing.T, db *DB, suffix string) (fixture, featurearea.FeatureArea, aisession.Session) {
	t.Helper()
	f := seed(t, db, suffix)
	ctx := context.Background()
	s := aisession.Session{ID: "session-" + suffix, ProjectID: f.project.ID, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, db, s)
	featureAreaValue := featurearea.FeatureArea{ID: "featureArea-" + suffix, ProjectID: f.project.ID, CreatedByAISessionID: s.ID, Title: "Review featureArea", ProblemStatement: "Review the production path", Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, featureAreaValue); err != nil {
		t.Fatal(err)
	}
	intent := task.Task{ID: "review-intent-" + suffix, Title: "Review intent", ProjectID: f.project.ID, FeatureAreaID: featureAreaValue.ID, ModuleID: f.task.ModuleID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BaseCommit: f.task.BaseCommit, Goal: "Review production", ProblemStatement: "Review production", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewTaskStore(db).Create(ctx, intent); err != nil {
		t.Fatal(err)
	}
	f.task = intent
	return f, featureAreaValue, s
}
func TestReviewStorePersistsPromptEvidenceAndConvergentRunLink(t *testing.T) {
	for name, instruction := range map[string]string{"leading": " \tinspect", "trailing": "inspect \t", "newlines": "\r\ninspect\n\n", "unicode": "检查 e\u0301 🚀", "symbols": "<$&`{}\\> # +=", "long": " \n" + strings.Repeat("界", 21000) + "\n "} {
		for _, depth := range []task.ReviewDepth{task.ReviewDepthFast, task.ReviewDepthDeep} {
			t.Run(name+"/"+string(depth), func(t *testing.T) { testReviewStorePromptByteIdentity(t, instruction, depth) })
		}
	}
}
func testReviewStorePromptByteIdentity(t *testing.T, instruction string, depth task.ReviewDepth) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f, top, sess := reviewFixture(t, db, "review")
	store := NewReviewStore(db)
	ctx := context.Background()
	v := review.Review{ReviewDepth: depth, TaskID: f.task.ID, ID: "review_opaque", ProjectID: f.project.ID, FeatureAreaID: top.ID, CreatedByAISessionID: sess.ID, AuthorKind: string(review.AuthorAISession), AuthorID: sess.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, f.task.CodexConfigID), BaseCommit: f.task.BaseCommit, CreatedAt: testNow, Focus: review.Focus{Kind: review.FocusFocused, Summary: "payments", Paths: []string{"internal/payments"}}, EngineeringGoal: "Find production defects", Instruction: instruction}
	v.Focus.Modules = []run.ReviewModuleSelection{{Key: "payments", Role: run.ReviewModulePrimary}, {Key: "storage", Role: run.ReviewModuleSupporting}}
	if err := store.Create(ctx, v); err != nil {
		t.Fatal(err)
	}
	var createErr error
	f.run, createErr = NewRunStore(db).CreateProductScopeReviewAttempt(ctx, v.ID, "run-owned-review", v.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository}, testNow)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err := store.LinkRun(ctx, v.ID, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordPrompt(ctx, v.ID, v.Instruction+"injected", testNow.Add(time.Minute)); err == nil {
		t.Fatal("mismatched Review prompt persisted")
	}
	prompt := v.Instruction
	got, err := store.RecordPrompt(ctx, v.ID, prompt, testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(prompt))
	if got.ReviewDepth != depth || got.Instruction != instruction || got.FinalCodexPrompt != instruction || got.PromptSHA256 != hex.EncodeToString(sum[:]) || got.PromptBytes != int64(len([]byte(prompt))) || got.RunID != f.run.ID {
		t.Fatalf("stored review=%+v", got)
	}
	if len(got.Focus.Modules) != 2 || got.Focus.Modules[0] != v.Focus.Modules[0] || got.Focus.Modules[1] != v.Focus.Modules[1] {
		t.Fatalf("durable module roles lost: %+v", got.Focus)
	}
	for _, o := range []review.AccessOutcome{review.AccessSuccess, review.AccessForbidden, review.AccessFailure} {
		if err := store.RecordAccess(ctx, review.AccessEvidence{ID: "evidence-" + string(o), ReviewID: v.ID, Operation: "review_get", Outcome: o, RecordedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err := store.SummarizeEvidence(ctx, v.ID)
	if err != nil || evidence.Successes != 1 || evidence.Forbidden != 1 || evidence.Failures != 1 {
		t.Fatalf("summary=%+v err=%v", evidence, err)
	}
	if err := store.RecordStartFailure(ctx, v.ID, review.StartFailure{Code: "START_FAILED", Blocker: "worker unavailable", RecordedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
}
func TestReviewStoreAllowsMultipleReviewsPerTopic(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f, top, sess := reviewFixture(t, db, "many")
	store := NewReviewStore(db)
	for _, id := range []string{"review_one", "review_two"} {
		if id == "review_two" {
			if err := store.RecordStartFailure(context.Background(), "review_one", review.StartFailure{Code: "START_FAILED", Blocker: "synthetic", RecordedAt: testNow}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.Create(context.Background(), review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ID: id, ProjectID: f.project.ID, FeatureAreaID: top.ID, CreatedByAISessionID: sess.ID, AuthorKind: string(review.AuthorAISession), AuthorID: sess.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, f.task.CodexConfigID), BaseCommit: f.task.BaseCommit, CreatedAt: testNow, Focus: review.Focus{Kind: review.FocusRepository, Summary: "all"}, EngineeringGoal: "goal", Instruction: "instruction"}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListByFeatureArea(context.Background(), top.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
}

func TestProductReviewRunCreationLinksBothSidesAtomically(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f, top, sess := reviewFixture(t, db, "atomic-link")
	reviews := NewReviewStore(db)
	value := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ID: "review_atomic", ProjectID: f.project.ID, FeatureAreaID: top.ID, CreatedByAISessionID: sess.ID, AuthorKind: string(review.AuthorAISession), AuthorID: sess.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, f.task.CodexConfigID), BaseCommit: f.task.BaseCommit, CreatedAt: testNow, Focus: review.Focus{Kind: review.FocusFocused, Paths: []string{"internal/a"}}, EngineeringGoal: "goal", Instruction: "instruction"}
	if err := reviews.Create(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	runValue, err := NewRunStore(db).CreateProductScopeReviewAttempt(context.Background(), value.ID, "run-review-atomic", "goal", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeFocused, InitialScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/a"}}}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := reviews.Get(context.Background(), value.ID)
	if err != nil || linked.RunID != runValue.ID || runValue.ReviewID != value.ID {
		t.Fatalf("Review/Run link review=%+v run=%+v err=%v", linked, runValue, err)
	}
}
func TestReviewStoreRejectsCrossTaskRunLink(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f, top, sess := reviewFixture(t, db, "link")
	other := seed(t, db, "other")
	store := NewReviewStore(db)
	v := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ID: "review_link", ProjectID: f.project.ID, FeatureAreaID: top.ID, CreatedByAISessionID: sess.ID, AuthorKind: string(review.AuthorAISession), AuthorID: sess.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, f.task.CodexConfigID), BaseCommit: f.task.BaseCommit, CreatedAt: time.Now(), Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "goal", Instruction: "instruction"}
	if err := store.Create(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkRun(context.Background(), v.ID, other.run.ID); err != review.ErrConflict {
		t.Fatalf("err=%v", err)
	}
}

func TestReviewContinuationClaimIsDurableAndExactlyOnceAfterTerminalState(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f, top, sess := reviewFixture(t, db, "continuation")
	store := NewReviewStore(db)
	ctx := context.Background()
	value := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ID: "review_continuation", ProjectID: f.project.ID, FeatureAreaID: top.ID, CreatedByAISessionID: sess.ID, AuthorKind: string(review.AuthorAISession), AuthorID: sess.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, f.task.CodexConfigID), BaseCommit: f.task.BaseCommit, CreatedAt: testNow, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "goal", Instruction: "instruction"}
	if err := store.Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	var createErr error
	f.run, createErr = NewRunStore(db).CreateProductScopeReviewAttempt(ctx, value.ID, "run-owned-continuation", "review", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository}, testNow)
	if createErr != nil {
		t.Fatal(createErr)
	}
	if err := store.LinkRun(ctx, value.ID, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimContinuation(ctx, value.ID, testNow.Add(time.Minute)); err != nil || claimed {
		t.Fatalf("non-terminal claim=%t err=%v", claimed, err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE runs SET status='SUCCEEDED' WHERE id=?`, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimContinuation(ctx, value.ID, testNow.Add(2*time.Minute)); err != nil || !claimed {
		t.Fatalf("terminal claim=%t err=%v", claimed, err)
	}
	if claimed, err := store.ClaimContinuation(ctx, value.ID, testNow.Add(3*time.Minute)); err != nil || claimed {
		t.Fatalf("duplicate claim=%t err=%v", claimed, err)
	}
	got, err := store.Get(ctx, value.ID)
	if err != nil || got.ContinuationAttemptedAt == nil || !got.ContinuationAttemptedAt.Equal(testNow.Add(2*time.Minute)) {
		t.Fatalf("persisted continuation=%+v err=%v", got.ContinuationAttemptedAt, err)
	}
}
