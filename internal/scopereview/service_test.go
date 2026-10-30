package scopereview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
)

type scopeReviewSessionLookup struct{ value aisession.Session }

func (s scopeReviewSessionLookup) Get(context.Context, string) (aisession.Session, error) {
	return s.value, nil
}

type scopeReviewRepositoryLookup struct{ value repository.Repository }

func (s scopeReviewRepositoryLookup) Get(context.Context, string) (repository.Repository, error) {
	return s.value, nil
}

type scopeReviewDirectStarter struct{}

func (scopeReviewDirectStarter) StartReview(ctx context.Context, _ string, create func(context.Context) (run.Run, error)) (run.Run, error) {
	return create(ctx)
}

type admittedUpperReviewStore struct {
	result review.StartAdmission
	calls  int
}

func (s *admittedUpperReviewStore) Get(context.Context, string) (review.Review, error) {
	return s.result.Review, nil
}
func (s *admittedUpperReviewStore) AdmitStart(context.Context, review.Review, string, string, string, run.ReviewScope, run.ReviewPlan) (review.StartAdmission, error) {
	s.calls++
	return s.result, nil
}

type rejectedReservedStarter struct{ called bool }

func (s *rejectedReservedStarter) StartReview(context.Context, string, func(context.Context) (run.Run, error)) (run.Run, error) {
	s.called = true
	return run.Run{}, errors.New("Codex starter must not be called")
}

type upperStarterCapture struct{ calls int }

func (s *upperStarterCapture) StartUpperReview(_ context.Context, v review.Review, r run.Run) (run.Run, error) {
	s.calls++
	if v.ID != r.ReviewID {
		return run.Run{}, errors.New("owner mismatch")
	}
	r.Status = run.StatusRunning
	return r, nil
}

type authorizedStartQueueFake struct {
	values []review.AuthorizedStart
}

func (q *authorizedStartQueueFake) PendingAuthorizedReviewStarts(_ context.Context, after string) ([]review.AuthorizedStart, error) {
	var out []review.AuthorizedStart
	for _, value := range q.values {
		if value.Admission.Review.ID > after {
			out = append(out, value)
		}
	}
	if len(out) > 0 {
		q.values = nil
	}
	return out, nil
}

type reservedStartCapture struct {
	called   int
	reviewID string
	reserved run.Run
	err      error
}

func (s *reservedStartCapture) StartReview(context.Context, string, func(context.Context) (run.Run, error)) (run.Run, error) {
	return run.Run{}, errors.New("continuation must use the original reserved Run")
}

func (s *reservedStartCapture) StartReservedReview(_ context.Context, reviewID string, reserved run.Run) (run.Run, error) {
	s.called++
	s.reviewID, s.reserved = reviewID, reserved
	return reserved, s.err
}

func TestAuthorizedContinuationDispatchesOnlyTheExactAdmittedReviewRun(t *testing.T) {
	value := review.Review{ID: "review-successor-2", StartRequestID: "re-review-start", RunID: "run-review-successor-2", TaskID: "task-owner", ProjectID: "project-owner", FeatureAreaID: "area-owner", ExecutorKind: review.ExecutorCodingAgent}
	reserved := run.Run{ID: value.RunID, ReviewID: value.ID, TaskID: value.TaskID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, OperationKind: run.OperationReview, OperationID: value.ID, Purpose: run.PurposeScopeReview}
	queue := &authorizedStartQueueFake{values: []review.AuthorizedStart{{Admission: review.StartAdmission{Review: value, Run: reserved, Replayed: true}, RequestID: value.StartRequestID, PayloadSHA256: strings.Repeat("a", 64)}}}
	starter := &reservedStartCapture{}
	service := &Service{starter: starter, startQueue: queue}
	continuation := service.AuthorizedContinuation()
	continuation(context.Background())
	if starter.called != 1 || starter.reviewID != value.ID || starter.reserved.ID != reserved.ID || starter.reserved.OperationID != value.ID {
		t.Fatalf("continuation did not dispatch exact admitted reservation: calls=%d review=%q run=%+v", starter.called, starter.reviewID, starter.reserved)
	}
	// A second poll sees no successor past the consumed cursor. The worker does
	// not synthesize a second Review or Run after the original handoff.
	continuation(context.Background())
	if starter.called != 1 {
		t.Fatalf("continuation dispatched the same reservation twice: calls=%d", starter.called)
	}
}

func TestAuthorizedContinuationRejectsReservationIdentityDrift(t *testing.T) {
	value := review.Review{ID: "review-successor-2", StartRequestID: "re-review-start", RunID: "run-review-successor-2", TaskID: "task-owner", ProjectID: "project-owner", FeatureAreaID: "area-owner", ExecutorKind: review.ExecutorCodingAgent}
	reserved := run.Run{ID: value.RunID, ReviewID: value.ID, TaskID: "different-task", ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, OperationKind: run.OperationReview, OperationID: value.ID, Purpose: run.PurposeScopeReview}
	queue := &authorizedStartQueueFake{values: []review.AuthorizedStart{{Admission: review.StartAdmission{Review: value, Run: reserved, Replayed: true}, RequestID: value.StartRequestID, PayloadSHA256: strings.Repeat("a", 64)}}}
	starter := &reservedStartCapture{}
	service := &Service{starter: starter, startQueue: queue}
	service.AuthorizedContinuation()(context.Background())
	if starter.called != 0 {
		t.Fatalf("identity-mismatched Run reached the runtime starter: calls=%d", starter.called)
	}
}

func TestAuthorizedContinuationUsesExistingUpperReviewPreparer(t *testing.T) {
	value := review.Review{ID: "review-upper-successor-2", StartRequestID: "re-review-start", RunID: "run-upper-successor-2", TaskID: "task-owner", ProjectID: "project-owner", FeatureAreaID: "area-owner", ExecutorKind: review.ExecutorUpperLLM}
	reserved := run.Run{ID: value.RunID, ReviewID: value.ID, TaskID: value.TaskID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, OperationKind: run.OperationReview, OperationID: value.ID, Purpose: run.PurposeScopeReview}
	queue := &authorizedStartQueueFake{values: []review.AuthorizedStart{{Admission: review.StartAdmission{Review: value, Run: reserved, Replayed: true}, RequestID: value.StartRequestID, PayloadSHA256: strings.Repeat("b", 64)}}}
	upper := &upperStarterCapture{}
	reservedStarter := &reservedStartCapture{}
	service := &Service{starter: reservedStarter, upperStarter: upper, startQueue: queue}
	service.AuthorizedContinuation()(context.Background())
	if upper.calls != 1 || reservedStarter.called != 0 {
		t.Fatalf("upper continuation used wrong preparer: upper=%d reserved=%d", upper.calls, reservedStarter.called)
	}
}

func TestStartAdmittedUpperReviewUsesUpperStarterAndReplayRecoversPreparation(t *testing.T) {
	v := review.Review{ID: "upper-review", ExecutorKind: review.ExecutorUpperLLM, TaskID: "task", ProjectID: "project", FeatureAreaID: "area", CreatedByAISessionID: "session", RepositoryID: "repo", BaseCommit: strings.Repeat("a", 40), Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "inspect", Instruction: "inspect"}
	store := &admittedUpperReviewStore{result: review.StartAdmission{Review: v, Run: run.Run{ID: "run", ReviewID: v.ID, TaskID: v.TaskID, ProjectID: v.ProjectID, FeatureAreaID: v.FeatureAreaID, Purpose: run.PurposeScopeReview}}}
	reserved := &rejectedReservedStarter{}
	service, err := NewService(scopeReviewSessionLookup{aisession.Session{ID: "session", ProjectID: "project"}}, scopeReviewRepositoryLookup{repository.Repository{ID: "repo", ProjectID: "project", Path: t.TempDir()}}, &scopeReviewRunCapture{}, reserved, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetReviews(store)
	upper := &upperStarterCapture{}
	service.SetUpperStarter(upper)
	for i := 0; i < 2; i++ {
		out, e := service.StartAdmitted(context.Background(), AdmittedStartInput{AISessionID: "session", RequestID: "request", PayloadSHA256: "hash", Review: v})
		if e != nil || out.Run.Status != run.StatusRunning {
			t.Fatalf("upper admitted=%+v err=%v", out, e)
		}
	}
	if reserved.called || upper.calls != 2 || store.calls != 2 {
		t.Fatalf("reserved=%v upper=%d admission=%d", reserved.called, upper.calls, store.calls)
	}
}

type scopeReviewRunCapture struct {
	grants *reviewgrant.Service
	value  run.Run
}

func (r *scopeReviewRunCapture) CreateScopeReview(context.Context, string, string, run.ReviewScope) (run.Run, error) {
	return run.Run{}, errors.New("legacy Review creation was used")
}

func (r *scopeReviewRunCapture) CreateFocusedScopeReviewForReview(ctx context.Context, reviewID, instruction string, scope run.ReviewScope, plan run.ReviewPlan) (run.Run, error) {
	if _, err := r.grants.Get(ctx, reviewID); err != nil {
		return run.Run{}, errors.New("initial Review grant was not durable before Run creation")
	}
	r.value = run.Run{TaskID: "task-a", ID: "run-a", ReviewID: reviewID, Purpose: run.PurposeScopeReview, CodexInstruction: instruction, ReviewScope: scope, ReviewPlan: plan}
	return r.value, nil
}

func TestCanonicalizeScopeEnforcesExistingContainedPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "parser"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("read me"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	got, err := canonicalizeScope(root, run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/parser", "README.md"}})
	if err != nil || got.Kind != run.ReviewScopePaths || !reflect.DeepEqual(got.Paths, []string{"internal/parser", "README.md"}) {
		t.Fatalf("scope=%+v err=%v", got, err)
	}
	for _, scope := range []run.ReviewScope{
		{Kind: run.ReviewScopePaths, Paths: []string{}},
		{Kind: run.ReviewScopePaths, Paths: []string{"../outside"}},
		{Kind: run.ReviewScopePaths, Paths: []string{"/absolute"}},
		{Kind: run.ReviewScopePaths, Paths: []string{"missing"}},
		{Kind: run.ReviewScopePaths, Paths: []string{"escape"}},
		{Kind: run.ReviewScopeRepository, Paths: []string{"README.md"}},
	} {
		if _, err := canonicalizeScope(root, scope); err == nil {
			t.Fatalf("accepted invalid scope %+v", scope)
		}
	}
	if _, err := canonicalizeScope(root, run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"escape"}}); !errors.Is(err, reviewaccess.ErrScopeForbidden) {
		t.Fatalf("symlink escape did not retain structural Review boundary code: %v", err)
	}
	repositoryScope, err := canonicalizeScope(root, run.ReviewScope{Kind: run.ReviewScopeRepository})
	if err != nil || repositoryScope.Kind != run.ReviewScopeRepository || repositoryScope.Paths == nil || len(repositoryScope.Paths) != 0 {
		t.Fatalf("repository scope=%+v err=%v", repositoryScope, err)
	}
}

func TestCanonicalizeScopeDoesNotRejectTestLikePathNames(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"test", "pkg"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"test/fixture.go", "pkg/service_test.go"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"test", "test/fixture.go", "pkg/service_test.go"} {
		got, err := CanonicalizeScope(root, run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{path}})
		if err != nil || len(got.Paths) != 1 || got.Paths[0] != path {
			t.Fatalf("test-like path %q received semantic rejection: scope=%+v error=%v", path, got, err)
		}
	}
}

type scopeProductReviewLookup struct{ value review.Review }

func (s scopeProductReviewLookup) Get(_ context.Context, id string) (review.Review, error) {
	if id != s.value.ID {
		return review.Review{}, review.ErrNotFound
	}
	return s.value, nil
}

func TestProductReviewStartDerivesOwnerFocusAndInstructionAndPersistsExactGrant(t *testing.T) {
	for _, kind := range []review.FocusKind{review.FocusFocused, review.FocusRepository} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "production"), 0700); err != nil {
				t.Fatal(err)
			}
			grants, _ := reviewgrant.NewService(reviewgrant.NewMemoryStore(), func() time.Time { return time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) })
			capture := &scopeReviewRunCapture{grants: grants}
			v := review.Review{ReviewDepth: "DEEP", TaskID: "task-a", ID: "review_current", ProjectID: "project-a", FeatureAreaID: "feature-a", RepositoryID: "repo-a", Instruction: "Exact authored instruction", Focus: review.Focus{Kind: kind}}
			if kind == review.FocusFocused {
				v.Focus.Paths = []string{"production"}
				v.Focus.Modules = []run.ReviewModuleSelection{{Key: "primary", Role: run.ReviewModulePrimary}, {Key: "support", Role: run.ReviewModuleSupporting}}
			}
			service, err := NewService(scopeReviewSessionLookup{aisession.Session{ID: "session-a", ProjectID: "project-a"}}, scopeReviewRepositoryLookup{repository.Repository{ID: "repo-a", ProjectID: "project-a", Path: root}}, capture, scopeReviewDirectStarter{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			service.SetReviews(scopeProductReviewLookup{v})
			service.SetInitialGrantService(grants)
			started, err := service.Start(ctx, StartInput{AISessionID: "session-a", ReviewID: v.ID})
			if err != nil {
				t.Fatal(err)
			}
			grant, err := grants.Get(ctx, v.ID)
			if err != nil {
				t.Fatal(err)
			}
			if started.TaskID != v.TaskID || started.ReviewID != v.ID || started.CodexInstruction != v.Instruction || started.ReviewScope.Kind != run.ReviewScopeRepository || started.ReviewPlan.Mode != run.ReviewMode(kind) {
				t.Fatalf("derived Review Run=%+v", started)
			}
			if grant.ReviewID != v.ID || grant.FeatureAreaID != v.FeatureAreaID || grant.ProjectID != v.ProjectID || grant.RepositoryID != v.RepositoryID || grant.Mode != reviewgrant.ModeRepository {
				t.Fatalf("grant=%+v", grant)
			}
			if kind == review.FocusFocused && !reflect.DeepEqual(started.ReviewPlan.InitialScope.Paths, v.Focus.Paths) {
				t.Fatalf("focus=%+v", started.ReviewPlan)
			}
			if !reflect.DeepEqual(started.ReviewPlan.SelectedModules, v.Focus.Modules) {
				t.Fatalf("module roles lost: %+v", started.ReviewPlan)
			}
			if _, err = service.Start(ctx, StartInput{AISessionID: "session-a"}); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("accepted missing Review owner: %v", err)
			}
		})
	}
}

func TestEmptyReviewInstructionNeverFallsBackToEngineeringGoal(t *testing.T) {
	capture := &scopeReviewRunCapture{}
	service, err := NewService(scopeReviewSessionLookup{aisession.Session{ID: "session", ProjectID: "project"}}, scopeReviewRepositoryLookup{repository.Repository{ID: "repo", ProjectID: "project", Path: t.TempDir()}}, capture, scopeReviewDirectStarter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, instruction := range []string{"", " \n\t"} {
		value := review.Review{ReviewDepth: "DEEP", ID: "review_empty", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", CreatedByAISessionID: "session", RepositoryID: "repo", EngineeringGoal: "must never be selected", Instruction: instruction, Focus: review.Focus{Kind: review.FocusRepository}}
		service.SetReviews(scopeProductReviewLookup{value})
		if _, err := service.Start(context.Background(), StartInput{AISessionID: "session", ReviewID: value.ID}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("fallback accepted: %v", err)
		}
		if _, err := service.StartAdmitted(context.Background(), AdmittedStartInput{AISessionID: "session", RequestID: "request", PayloadSHA256: "hash", Review: value}); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("admitted fallback accepted: %v", err)
		}
		if capture.value.ID != "" {
			t.Fatal("empty instruction allocated Run")
		}
	}
}
