package controlapi_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// compositionUpperStarter is the test double for scopereview.UpperStarter. It
// returns the already-reserved durable Run and never prepares a workspace or
// launches a Coding Agent. The production coordinator remains the real
// UpperReviewAccess/UpperReviewResults provider used for readback.
type compositionUpperStarter struct{}

func (compositionUpperStarter) StartUpperReview(_ context.Context, _ review.Review, reserved run.Run) (run.Run, error) {
	return reserved, nil
}

// compositionFixture enters through the same Project/Session, confirmation,
// Review admission and SQLite services as production. It deliberately replaces
// the Scope Review starter and the Task service with the focused test doubles
// from feature_area_startup_test.go, while wiring the real confirmation store,
// Session context and Upper Review read services.
//
// The shared newStartupFixture helper cannot be used directly here because its
// ownership fixture still calls the retired aicontext.Service.OpenSession, which
// now unconditionally returns SESSION_INITIALIZATION_REQUIRED. This fixture
// performs the equivalent setup through the current InitializeSession /
// BindProject path without editing the shared helper.
type compositionFixture struct {
	t            *testing.T
	ctx          context.Context
	plane        *controlplane.ControlPlane
	facade       *controlapi.Facade
	projectID    string
	sessionID    string
	areaID       string
	repositoryID string
}

func newCompositionFixture(t *testing.T) *compositionFixture {
	t.Helper()
	callerScope := "confirmation-composition-" + strings.ReplaceAll(t.Name(), "/", "-")
	ctx := aisession.WithCallerScope(context.Background(), callerScope)
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })

	project, err := plane.Projects.Create(ctx, "Composition project")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	repositoryPath := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	repositoryValue := repository.Repository{ID: "composition-repository", ProjectID: project.ID, Name: "Source", Path: repositoryPath, CreatedAt: now, UpdatedAt: now}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.Modules.EnsureUnclassified(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	compositionGit(t, repositoryPath, "init")
	if err := os.WriteFile(filepath.Join(repositoryPath, "tracked.txt"), []byte("composition fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	compositionGit(t, repositoryPath, "add", "tracked.txt")
	compositionGit(t, repositoryPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")

	ref := aiprojectselection.ClientBindingReference{CallerScope: callerScope}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := initialized.AISessionID
	if _, err := plane.AIContext.BindProject(ctx, ref, sessionID, project.ID, "bind"); err != nil {
		t.Fatal(err)
	}

	area := featurearea.FeatureArea{ID: "composition-feature", ProjectID: project.ID, CreatedByAISessionID: sessionID, Title: "Composition area", ProblemStatement: "Drive the confirmation composition chain", Status: featurearea.StatusDiscovering, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewFeatureAreaStore(plane.DB).Create(ctx, area); err != nil {
		t.Fatal(err)
	}

	scopeService, err := scopereview.NewService(plane.AISessions, plane.Repositories, plane.Runs, startupRunner{}, sqlite.NewReviewStore(plane.DB))
	if err != nil {
		t.Fatal(err)
	}
	scopeService.SetReviews(plane.Reviews)
	scopeService.SetInitialGrantService(plane.ReviewGrants)
	scopeService.SetUpperStarter(compositionUpperStarter{})
	scope := &startupScopeReviews{Service: scopeService}
	tasks := &startupTasks{Service: plane.Tasks}
	startup := startupContext{plane.AIContext}

	facade, err := controlapi.New(controlapi.Dependencies{
		StartupContext:         startup,
		DevelopmentCycles:      plane.DevelopmentCycles,
		Projects:               plane.Projects,
		ProjectRegistration:    plane.ProjectRegistration,
		ProjectSelection:       plane.ProjectSelection,
		AISessions:             plane.AISessions,
		Repositories:           plane.Repositories,
		Modules:                plane.Modules,
		ProjectContexts:        plane.ProjectContexts,
		ContextProbes:          plane.ContextProbes,
		ContextProbeRunner:     plane.ContextProbeRunner,
		Classifications:        plane.Classifications,
		FeatureAreas:           plane.FeatureAreas,
		AgentConversations:     plane.AgentConversations,
		Documents:              plane.Documents,
		DecisionResolutions:    plane.DecisionResolutions,
		Reviews:                plane.Reviews,
		Tasks:                  tasks,
		TaskPlans:              plane.TaskPlans,
		ScopeReviews:           scope,
		ScopeExpansions:        plane.ScopeExpansions,
		ExecutionProposals:     plane.ExecutionProposals,
		ExecutionCancellations: plane.ExecutionCancellations,
		ExecutionGoals:         plane.ExecutionGoals,
		Runs:                   plane.Runs,
		Workspaces:             plane.Workspaces,
		Evidence:               plane.Evidence,
		Artifacts:              plane.Artifacts,
		Events:                 plane.Events,
		Approvals:              plane.Approvals,
		Runner:                 plane.Supervisor,
		ExecutionAllowlist:     plane.CodexRuntime,
		Confirmations:          plane.Confirmations,
		ConfirmationSessions:   plane.AIContext,
		UpperReviewAccess:      plane.UpperReviewAccess,
		UpperReviewPreparer:    plane.Coordinator,
		UpperReviewResults:     plane.UpperReviewResults,
	})
	if err != nil {
		t.Fatal(err)
	}

	branch := compositionGit(t, repositoryPath, "rev-parse", "--abbrev-ref", "HEAD")
	if _, err := facade.SelectDevelopmentBranchForSession(ctx, developmentcycle.SelectInput{AISessionID: sessionID, ProjectID: project.ID, RepositoryID: repositoryValue.ID, DevelopmentRef: "refs/heads/" + branch, RequestID: "composition-select"}); err != nil {
		t.Fatalf("select development branch %q: %v", branch, err)
	}

	return &compositionFixture{t: t, ctx: ctx, plane: plane, facade: facade, projectID: project.ID, sessionID: sessionID, areaID: area.ID, repositoryID: repositoryValue.ID}
}

func compositionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

func (f *compositionFixture) taskPrepare(requestID string, variants ...confirmation.Variant) confirmation.PrepareInput {
	return confirmation.PrepareInput{
		AISessionID: f.sessionID,
		RequestID:   requestID,
		Purpose:     confirmation.Task,
		Action:      "CREATE_TASK_AND_REVIEW",
		Task: &confirmation.TaskBody{
			FeatureAreaID:    f.areaID,
			RepositoryID:     f.repositoryID,
			Title:            "Composition review task",
			ProblemStatement: "Inspect the repository read-only without modifying files.",
			Constraints:      []string{"read only"},
			StartReview:      true,
			ReviewDepth:      task.ReviewDepthDeep,
			ReviewPlan:       run.ReviewPlan{Mode: run.ReviewModeRepository},
		},
		Variants: variants,
	}
}

func compositionUpperVariant(id, summary, instruction string) confirmation.Variant {
	return confirmation.Variant{
		ID:                id,
		Label:             id,
		Summary:           summary,
		ExecutorKind:      string(review.ExecutorUpperLLM),
		ReviewInstruction: instruction,
	}
}

func (f *compositionFixture) commit(prepared confirmation.Intent, requestID, variantID string) (confirmation.Intent, error) {
	return f.facade.CommitConfirmation(f.ctx, confirmation.CommitInput{
		AISessionID: f.sessionID,
		IntentID:    prepared.IntentID,
		Version:     prepared.Version,
		Purpose:     prepared.Purpose,
		Action:      prepared.Action,
		RequestID:   requestID,
		ContentHash: prepared.ContentHash,
		VariantID:   variantID,
	})
}

func (f *compositionFixture) read(prepared confirmation.Intent, requestID string) (confirmation.Intent, error) {
	return f.facade.ReadConfirmation(f.ctx, confirmation.ReadInput{AISessionID: f.sessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: requestID})
}

func (f *compositionFixture) counts(t *testing.T) (tasks, reviews, runs int) {
	t.Helper()
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT (SELECT COUNT(*) FROM tasks),(SELECT COUNT(*) FROM reviews),(SELECT COUNT(*) FROM runs)`).Scan(&tasks, &reviews, &runs); err != nil {
		t.Fatal(err)
	}
	return
}

func compositionStep(t *testing.T, intent confirmation.Intent, kind string) confirmation.Step {
	t.Helper()
	for _, step := range intent.Steps {
		if step.Kind == kind {
			return step
		}
	}
	t.Fatalf("intent %s has no %s step: %+v", intent.IntentID, kind, intent.Steps)
	return confirmation.Step{}
}

func compositionSubmission(t *testing.T, intent confirmation.Intent) confirmation.Submission {
	t.Helper()
	if intent.Submission == nil {
		t.Fatalf("intent %s has no submission receipt: %+v", intent.IntentID, intent)
	}
	return *intent.Submission
}

// G1: one prepared variant, explicitly committed, creates exactly one durable
// Task + Review + Run through the real confirmation chain.
func TestConfirmationCompositionTaskAndReviewSingleVariant(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g1-prepare", compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files.")))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.State != "PENDING" || len(prepared.Content.Variants) != 1 {
		t.Fatalf("prepared=%+v", prepared)
	}

	committed, err := f.commit(prepared, "composition-g1-commit", "upper")
	if err != nil {
		t.Fatalf("commit: %+v; receipt=%+v", err, committed)
	}
	if committed.State != "COMPLETED" {
		t.Fatalf("commit state=%s steps=%+v", committed.State, committed.Steps)
	}
	submission := compositionSubmission(t, committed)
	if !submission.Recorded || submission.Outcome != confirmation.Consumed {
		t.Fatalf("submission=%+v", submission)
	}
	if submission.Request.VariantID != "upper" {
		t.Fatalf("recorded variant=%q", submission.Request.VariantID)
	}
	if submission.ReceiptRequestID != "composition-g1-commit" || submission.PayloadSHA256 == "" {
		t.Fatalf("receipt identity=%q payload=%q", submission.ReceiptRequestID, submission.PayloadSHA256)
	}

	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
		t.Fatalf("business objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}
	taskStep := compositionStep(t, committed, "CREATE_TASK")
	reviewStep := compositionStep(t, committed, "START_REVIEW")
	if taskStep.TaskID == "" || reviewStep.ReviewID == "" || reviewStep.RunID == "" {
		t.Fatalf("task step=%+v review step=%+v", taskStep, reviewStep)
	}

	owner, err := f.plane.Tasks.Get(f.ctx, taskStep.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.ProjectID != f.projectID || owner.FeatureAreaID != f.areaID || owner.RepositoryID != f.repositoryID {
		t.Fatalf("task ownership=%+v", owner)
	}
	if owner.CurrentReviewID != reviewStep.ReviewID {
		t.Fatalf("task current review=%q want=%q", owner.CurrentReviewID, reviewStep.ReviewID)
	}
	reviewValue, err := f.plane.Reviews.Get(f.ctx, reviewStep.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewValue.ProjectID != f.projectID || reviewValue.FeatureAreaID != f.areaID || reviewValue.TaskID != taskStep.TaskID {
		t.Fatalf("review ownership=%+v", reviewValue)
	}
	runValue, err := f.plane.Runs.Get(f.ctx, reviewStep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if runValue.TaskID != taskStep.TaskID || runValue.ReviewID != reviewStep.ReviewID || runValue.ProjectID != f.projectID || runValue.FeatureAreaID != f.areaID {
		t.Fatalf("run ownership=%+v", runValue)
	}
	if runValue.Purpose != run.PurposeScopeReview || runValue.OperationKind != run.OperationReview {
		t.Fatalf("run purpose/kind=%s/%s", runValue.Purpose, runValue.OperationKind)
	}
}

// G5: a host error before Core leaves no durable request, so a readback of an
// invented request id is explicitly NOT_OBSERVED and creates nothing.
func TestConfirmationReadbackUnknownRequestNotObserved(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g5-prepare", compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files.")))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	missing := fmt.Sprintf("confirm-never-sent-%d", time.Now().UnixNano())
	read, err := f.read(prepared, missing)
	if err != nil {
		t.Fatalf("read unknown request: %v", err)
	}
	submission := compositionSubmission(t, read)
	if submission.Recorded {
		t.Fatalf("unknown request reported as recorded: %+v", submission)
	}
	if submission.Outcome != confirmation.Unknown {
		t.Fatalf("unknown request outcome=%q", submission.Outcome)
	}
	if submission.NextAction != confirmation.ReadOriginalReceipt {
		t.Fatalf("unknown request next action=%q", submission.NextAction)
	}
	if submission.ReceiptRequestID != "" || submission.PayloadSHA256 != "" {
		t.Fatalf("unknown request leaked receipt=%q payload=%q", submission.ReceiptRequestID, submission.PayloadSHA256)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
		t.Fatalf("unknown request created objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}
}

// G8: the request id is part of the durable receipt identity. Another request
// id cannot observe it, and a mismatched Version or IntentID is rejected.
func TestConfirmationReadbackIdentityMismatch(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g8-prepare", compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files.")))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.commit(prepared, "composition-g8-a", "upper"); err != nil {
		t.Fatalf("commit A: %v", err)
	}

	readA, err := f.read(prepared, "composition-g8-a")
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	if sub := compositionSubmission(t, readA); !sub.Recorded || sub.Outcome != confirmation.Consumed {
		t.Fatalf("read A submission=%+v", sub)
	}

	readB, err := f.read(prepared, "composition-g8-b")
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if sub := compositionSubmission(t, readB); sub.Recorded || sub.Outcome != confirmation.Unknown {
		t.Fatalf("request B observed A's receipt: %+v", sub)
	}

	againA, err := f.read(prepared, "composition-g8-a")
	if err != nil {
		t.Fatalf("re-read A: %v", err)
	}
	if sub := compositionSubmission(t, againA); !sub.Recorded || sub.Outcome != confirmation.Consumed {
		t.Fatalf("re-read A submission=%+v", sub)
	}

	mismatchedVersion := confirmation.ReadInput{AISessionID: f.sessionID, IntentID: prepared.IntentID, Version: prepared.Version + 1, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: "composition-g8-a"}
	if _, err := f.facade.ReadConfirmation(f.ctx, mismatchedVersion); err == nil {
		t.Fatal("mismatched Version was accepted")
	}
	mismatchedIntent := confirmation.ReadInput{AISessionID: f.sessionID, IntentID: "confirm_does_not_exist", Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: "composition-g8-a"}
	if _, err := f.facade.ReadConfirmation(f.ctx, mismatchedIntent); err == nil {
		t.Fatal("mismatched IntentID was accepted")
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
		t.Fatalf("readback changed objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}
}

// G9: a reused request id with different commit content is a conflict and must
// not create a second effect.
func TestConfirmationReceiptPayloadConflict(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g9-prepare",
		compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files."),
		compositionUpperVariant("upper-other", "Inspect the other way", "Inspect the repository read-only and report. Do not modify files."),
	))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.commit(prepared, "composition-g9-commit", "upper"); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
		t.Fatalf("first commit objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}

	_, err = f.commit(prepared, "composition-g9-commit", "upper-other")
	if !errors.Is(err, confirmation.ErrRequestConflict) {
		t.Fatalf("conflicting commit err=%v want ErrRequestConflict", err)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
		t.Fatalf("conflict created objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}
}

// G13: replaying the identical commit returns the original durable steps and
// never duplicates business objects.
func TestConfirmationReceiptReplayIdempotency(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g13-prepare", compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files.")))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	first, err := f.commit(prepared, "composition-g13-commit", "upper")
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second, err := f.commit(prepared, "composition-g13-commit", "upper")
	if err != nil {
		t.Fatalf("replay commit: %v", err)
	}
	if second.State != "COMPLETED" || len(second.Steps) != len(first.Steps) {
		t.Fatalf("replay intent=%+v first=%+v", second, first)
	}
	for i := range first.Steps {
		left, right := first.Steps[i], second.Steps[i]
		if left.Kind != right.Kind || left.RequestID != right.RequestID || left.State != right.State || left.TaskID != right.TaskID || left.ReviewID != right.ReviewID || left.RunID != right.RunID {
			t.Fatalf("replay step[%d] changed: %+v / %+v", i, left, right)
		}
	}
	if sub := compositionSubmission(t, second); !sub.Recorded || sub.Outcome != confirmation.Consumed || sub.ReceiptRequestID != "composition-g13-commit" {
		t.Fatalf("replay submission=%+v", sub)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
		t.Fatalf("replay duplicated objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}
}

// G14: the receipt and its business identities survive a full reopen of the
// DataRoot; readback must not depend on in-process confirmation state.
func TestConfirmationReadbackReopenRecoversReceipt(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g14-prepare", compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files.")))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	committed, err := f.commit(prepared, "composition-g14-commit", "upper")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	taskID := compositionStep(t, committed, "CREATE_TASK").TaskID
	reviewStep := compositionStep(t, committed, "START_REVIEW")

	dataRoot := f.plane.DataPaths.Root
	if dataRoot == "" {
		t.Fatal("fixture DataRoot is empty")
	}
	if err := f.plane.Close(); err != nil {
		t.Fatalf("close plane: %v", err)
	}
	reopened, err := controlplane.Open(f.ctx, controlplane.Config{DataRoot: dataRoot})
	if err != nil {
		t.Fatalf("reopen plane: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	facade, err := controlapi.NewFromControlPlane(reopened)
	if err != nil {
		t.Fatalf("rebuild facade: %v", err)
	}

	read, err := facade.ReadConfirmation(f.ctx, confirmation.ReadInput{AISessionID: f.sessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: "composition-g14-commit"})
	if err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if read.State != "COMPLETED" {
		t.Fatalf("reopened state=%s steps=%+v", read.State, read.Steps)
	}
	submission := compositionSubmission(t, read)
	if !submission.Recorded || submission.Outcome != confirmation.Consumed || submission.Request.VariantID != "upper" {
		t.Fatalf("reopened submission=%+v", submission)
	}
	owner, err := reopened.Tasks.Get(f.ctx, taskID)
	if err != nil {
		t.Fatalf("reopened task: %v", err)
	}
	if owner.CurrentReviewID != reviewStep.ReviewID {
		t.Fatalf("reopened task review=%q want=%q", owner.CurrentReviewID, reviewStep.ReviewID)
	}
	if _, err := reopened.Reviews.Get(f.ctx, reviewStep.ReviewID); err != nil {
		t.Fatalf("reopened review: %v", err)
	}
	if _, err := reopened.Runs.Get(f.ctx, reviewStep.RunID); err != nil {
		t.Fatalf("reopened run: %v", err)
	}
}

// G3: invalid variant identities are rejected before any business effect.
func TestConfirmationVariantInvalidBeforeEffect(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g3-prepare",
		compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files."),
		compositionUpperVariant("upper-other", "Inspect the other way", "Inspect the repository read-only and report. Do not modify files."),
	))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// A second intent owns a variant id that must not be accepted by the first.
	other, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g3-other-prepare", compositionUpperVariant("alien", "Alien variant", "Inspect the repository read-only without changes.")))
	if err != nil {
		t.Fatalf("prepare other: %v", err)
	}
	_ = other

	cases := []struct {
		name      string
		requestID string
		variantID string
	}{
		{"empty", "composition-g3-empty", ""},
		{"unknown", "composition-g3-unknown", "unknown"},
		{"foreign", "composition-g3-foreign", "alien"},
	}
	for _, tc := range cases {
		_, err := f.commit(prepared, tc.requestID, tc.variantID)
		if err == nil {
			t.Fatalf("variant %s was accepted", tc.name)
		}
		if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
			t.Fatalf("variant %s created objects tasks/reviews/runs=%d/%d/%d", tc.name, tasks, reviews, runs)
		}
	}
}

// G2: with multiple prepared variants, the explicit second choice is the one
// recorded and admitted.
func TestConfirmationVariantSecondSelectionExplicit(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g2-prepare",
		compositionUpperVariant("first", "Inspect the repository", "Inspect the repository read-only. Do not modify files."),
		compositionUpperVariant("second", "Inspect the other way", "Inspect the repository read-only and report findings. Do not modify files."),
	))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(prepared.Content.Variants) != 2 {
		t.Fatalf("variants=%+v", prepared.Content.Variants)
	}
	committed, err := f.commit(prepared, "composition-g2-commit", "second")
	if err != nil {
		t.Fatalf("commit second: %v", err)
	}
	submission := compositionSubmission(t, committed)
	if submission.Request.VariantID != "second" {
		t.Fatalf("recorded variant=%q want second", submission.Request.VariantID)
	}
	reviewStep := compositionStep(t, committed, "START_REVIEW")
	reviewValue, err := f.plane.Reviews.Get(f.ctx, reviewStep.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewValue.Instruction != "Inspect the repository read-only and report findings. Do not modify files." {
		t.Fatalf("admitted review used variant instruction %q", reviewValue.Instruction)
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
		t.Fatalf("second variant objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}
}

// G16: Core never infers a variant merely because the intent holds exactly one.
// An empty variantId is rejected before any Task/Review/Run effect and, when a
// rejection receipt is recorded, it is REJECTED_NOT_CONSUMED rather than a
// consumption.
func TestConfirmationSingleVariantDoesNotInferMissingVariantID(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-g16-single-prepare", compositionUpperVariant("only", "Inspect the repository", "Inspect the repository read-only. Do not modify files.")))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(prepared.Content.Variants) != 1 {
		t.Fatalf("variants=%+v", prepared.Content.Variants)
	}
	if _, err := f.commit(prepared, "composition-g16-single-commit", ""); err == nil {
		t.Fatal("Core inferred the single variant instead of rejecting an empty variantId")
	}
	if tasks, reviews, runs := f.counts(t); tasks != 0 || reviews != 0 || runs != 0 {
		t.Fatalf("empty variantId created objects tasks/reviews/runs=%d/%d/%d", tasks, reviews, runs)
	}
	read, readErr := f.read(prepared, "composition-g16-single-commit")
	if readErr != nil {
		t.Fatalf("rejected receipt readback failed: %v", readErr)
	}
	submission := compositionSubmission(t, read)
	if !submission.Recorded || submission.Outcome != confirmation.RejectedNotConsumed {
		t.Fatalf("empty variantId receipt=%+v", submission)
	}
}

func TestConfirmationPendingCompositionProjectsOriginalReceiptInWorkspace(t *testing.T) {
	f := newCompositionFixture(t)
	prepared, err := f.facade.PrepareConfirmation(f.ctx, f.taskPrepare("composition-workspace-prepare", compositionUpperVariant("upper", "Inspect the repository", "Inspect the repository read-only. Do not modify files.")))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	in := confirmation.CommitInput{AISessionID: f.sessionID, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "composition-workspace-commit", ContentHash: prepared.ContentHash, VariantID: "upper", SubmittedHash: strings.Repeat("a", 64)}
	steps := []confirmation.Step{{Kind: "CREATE_TASK", RequestID: in.RequestID + ":create", State: "PENDING"}, {Kind: "START_REVIEW", RequestID: in.RequestID + ":review", State: "PENDING"}}
	started, fresh, err := f.plane.Confirmations.BeginCommit(f.ctx, in, steps)
	if err != nil || !fresh || started.State != "CONFIRMING" {
		t.Fatalf("begin commit: fresh=%v state=%s err=%v", fresh, started.State, err)
	}

	workspace, err := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.sessionID, f.areaID, "")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if len(workspace.PendingConfirmations) != 1 {
		t.Fatalf("pending confirmations=%+v", workspace.PendingConfirmations)
	}
	pending := workspace.PendingConfirmations[0]
	if pending.IntentID != prepared.IntentID || pending.RequestID != in.RequestID || pending.Outcome != string(confirmation.Consumed) || !pending.OutcomeRecorded {
		t.Fatalf("pending confirmation=%+v", pending)
	}
	for _, area := range workspace.FeatureAreas {
		if area.ID == f.areaID && area.CurrentAction.Code != "READ_CONFIRMATION_RESULT" {
			t.Fatalf("current action=%+v", area.CurrentAction)
		}
	}
}
