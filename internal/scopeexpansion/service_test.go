package scopeexpansion

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
)

type memoryStore struct{ values map[string]Expansion }

func (s *memoryStore) Create(_ context.Context, value Expansion) error {
	for _, existing := range s.values {
		if existing.ReviewID == value.ReviewID && existing.Status == StatusPendingConfirmation {
			return ErrConflict
		}
	}
	s.values[value.ID] = Clone(value)
	return nil
}
func (s *memoryStore) Get(_ context.Context, id string) (Expansion, error) {
	value, ok := s.values[id]
	if !ok {
		return Expansion{}, ErrNotFound
	}
	return Clone(value), nil
}
func (s *memoryStore) ListByProject(_ context.Context, projectID string) ([]Expansion, error) {
	values := []Expansion{}
	for _, value := range s.values {
		if value.ProjectID == projectID {
			values = append(values, Clone(value))
		}
	}
	return values, nil
}
func (s *memoryStore) Decide(_ context.Context, id string, status Status, now time.Time) (Expansion, error) {
	value, ok := s.values[id]
	if !ok {
		return Expansion{}, ErrNotFound
	}
	if value.Status != StatusPendingConfirmation {
		return value, &TransitionError{From: value.Status, To: status}
	}
	value.Status, value.UpdatedAt, value.DecidedAt = status, now, &now
	s.values[id] = Clone(value)
	return Clone(value), nil
}

type sessionLookup struct{ value aisession.Session }

func (s sessionLookup) Get(context.Context, string) (aisession.Session, error) { return s.value, nil }

type reviewLookup map[string]review.Review

func (s reviewLookup) Get(_ context.Context, id string) (review.Review, error) {
	v, ok := s[id]
	if !ok {
		return review.Review{}, review.ErrNotFound
	}
	return v, nil
}

type repositoryLookup struct{ value repository.Repository }

func (s repositoryLookup) Get(context.Context, string) (repository.Repository, error) {
	return s.value, nil
}

type grantLookup map[string]reviewgrant.Grant

func (s grantLookup) Get(_ context.Context, id string) (reviewgrant.Grant, error) {
	v, ok := s[id]
	if !ok {
		return reviewgrant.Grant{}, reviewgrant.ErrNotFound
	}
	return v, nil
}

type expansionFixture struct {
	service *Service
	store   *memoryStore
	reviews reviewLookup
	grants  grantLookup
	owner   review.Review
}

func newExpansionFixture(t *testing.T) expansionFixture {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{"current/child", "additional", "newer", "tests"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	owner := review.Review{TaskID: "task-a", ID: "review_a", ProjectID: "project-a", FeatureAreaID: "feature-a", RepositoryID: "repo-a"}
	reviews := reviewLookup{owner.ID: owner}
	store := &memoryStore{values: map[string]Expansion{}}
	next := 0
	service, err := NewService(store, sessionLookup{aisession.Session{ID: "session-a", ProjectID: owner.ProjectID}}, reviews, repositoryLookup{repository.Repository{ID: owner.RepositoryID, ProjectID: owner.ProjectID, Path: root}}, func() (string, error) { next++; return fmt.Sprintf("expansion-%d", next), nil }, func() time.Time { return time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	grants := grantLookup{owner.ID: {ReviewID: owner.ID, FeatureAreaID: owner.FeatureAreaID, ProjectID: owner.ProjectID, RepositoryID: owner.RepositoryID, Mode: reviewgrant.ModeFocused, InitialScope: paths("current")}}
	service.SetReviewGrantLookup(grants)
	return expansionFixture{service, store, reviews, grants, owner}
}
func paths(values ...string) run.ReviewScope {
	return run.ReviewScope{Kind: run.ReviewScopePaths, Paths: values}
}
func (f expansionFixture) input() CreateInput {
	return CreateInput{AISessionID: "session-a", ReviewID: f.owner.ID, CurrentScope: paths("current"), AdditionalScope: paths("additional"), Reason: "Inspect the exact adjacent boundary."}
}

func TestScopeExpansionRequiresExactReviewOwnerAndOneDecision(t *testing.T) {
	f := newExpansionFixture(t)
	ctx := context.Background()
	value, err := f.service.Create(ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if value.ReviewID != f.owner.ID || value.FeatureAreaID != f.owner.FeatureAreaID || value.RepositoryID != f.owner.RepositoryID || value.LegacyTaskID != "" || value.Status != StatusPendingConfirmation {
		t.Fatalf("wrong owner: %+v", value)
	}
	if _, err := f.service.Create(ctx, f.input()); !errors.Is(err, ErrConflict) {
		t.Fatalf("second pending expansion=%v", err)
	}
	listed, err := f.service.ListByProject(ctx, f.owner.ProjectID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	listed[0].AdditionalScope.Paths[0] = "mutated"
	stored, err := f.service.Get(ctx, "session-a", value.ID)
	if err != nil || stored.AdditionalScope.Paths[0] != "additional" {
		t.Fatalf("mutable projection=%+v err=%v", stored, err)
	}
	allowed, err := f.service.Allow(ctx, "session-a", value.ID)
	if err != nil || allowed.Status != StatusAllowed || allowed.DecidedAt == nil {
		t.Fatalf("allowed=%+v err=%v", allowed, err)
	}
	var transition *TransitionError
	if _, err := f.service.Allow(ctx, "session-a", value.ID); !errors.As(err, &transition) {
		t.Fatalf("repeated decision=%v", err)
	}
}

func TestScopeExpansionAuthorizesExactTaskReviewGrantChainWithoutRun(t *testing.T) {
	f := newExpansionFixture(t)
	ctx := context.Background()
	if err := f.service.AuthorizeReviewScope(ctx, f.owner.ID, paths("current/child")); err != nil {
		t.Fatal(err)
	}
	value, err := f.service.Create(ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeReviewScope(ctx, f.owner.ID, paths("current", "additional")); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending expansion authorized=%v", err)
	}
	if _, err := f.service.Allow(ctx, "session-a", value.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeReviewScope(ctx, f.owner.ID, paths("current", "additional")); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeReviewScope(ctx, f.owner.ID, paths("newer")); !errors.Is(err, ErrConflict) {
		t.Fatalf("unauthorized addition=%v", err)
	}
	input := f.input()
	input.CurrentScope = paths("additional", "current")
	input.AdditionalScope = run.ReviewScope{Kind: run.ReviewScopeRepository}
	broader, err := f.service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Allow(ctx, "session-a", broader.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeReviewScope(ctx, f.owner.ID, run.ReviewScope{Kind: run.ReviewScopeRepository}); err != nil {
		t.Fatal(err)
	}
	// Even a repository grant remains a structural repository boundary.
	if err := f.service.AuthorizeReviewScope(ctx, f.owner.ID, paths("../outside")); !errors.Is(err, reviewaccess.ErrScopeForbidden) {
		t.Fatalf("repository escape authorized=%v", err)
	}
	if _, err := f.service.Create(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale proposal=%v", err)
	}
}

func TestScopeExpansionRejectsForeignLegacyAndMissingExactGrant(t *testing.T) {
	for _, scenario := range []string{"foreign-project", "legacy-review", "missing-grant", "foreign-grant", "wrong-repository", "wrong-feature-area"} {
		t.Run(scenario, func(t *testing.T) {
			f := newExpansionFixture(t)
			ctx := context.Background()
			owner := f.owner
			grant := f.grants[owner.ID]
			switch scenario {
			case "foreign-project":
				owner.ProjectID = "foreign"
			case "legacy-review":
				owner.TaskID = ""
			case "missing-grant":
				delete(f.grants, owner.ID)
			case "foreign-grant":
				grant.ReviewID = "review_other"
			case "wrong-repository":
				grant.RepositoryID = "repo-other"
			case "wrong-feature-area":
				grant.FeatureAreaID = "feature-other"
			}
			f.reviews[owner.ID] = owner
			if scenario != "missing-grant" {
				f.grants[owner.ID] = grant
			}
			if _, err := f.service.Create(ctx, f.input()); err == nil {
				t.Fatal("invalid owner/grant accepted")
			}
			if len(f.store.values) != 0 {
				t.Fatal("invalid expansion was persisted")
			}
		})
	}
	f := newExpansionFixture(t)
	f.service.SetReviewGrantLookup(nil)
	if _, err := f.service.Create(context.Background(), f.input()); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing grant dependency accepted=%v", err)
	}
}

func TestScopeExpansionCannotBorrowAnotherReviewAuthorization(t *testing.T) {
	f := newExpansionFixture(t)
	ctx := context.Background()
	other := f.owner
	other.ID = "review_other"
	f.reviews[other.ID] = other
	grant := f.grants[f.owner.ID]
	grant.ReviewID = other.ID
	f.grants[other.ID] = grant
	value, err := f.service.Create(ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Allow(ctx, "session-a", value.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeReviewScope(ctx, other.ID, paths("current", "additional")); !errors.Is(err, ErrConflict) {
		t.Fatalf("another Review inherited expansion=%v", err)
	}
	// Task-era records remain visible audit and never become grant authority.
	f.store.values["legacy"] = Expansion{ID: "legacy", ProjectID: f.owner.ProjectID, LegacyTaskID: "old-task", ReviewID: other.ID, FeatureAreaID: other.FeatureAreaID, RepositoryID: other.RepositoryID, Status: StatusPendingConfirmation, CurrentScope: paths("current"), AdditionalScope: paths("newer")}
	for _, decide := range []func(context.Context, string, string) (Expansion, error){f.service.Allow, f.service.Reject} {
		if _, err := decide(ctx, "session-a", "legacy"); !errors.Is(err, ErrConflict) {
			t.Fatalf("legacy decision=%v", err)
		}
	}
	legacy := f.store.values["legacy"]
	legacy.Status = StatusAllowed
	f.store.values[legacy.ID] = legacy
	if err := f.service.AuthorizeReviewScope(ctx, other.ID, paths("newer")); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy Task expansion became authority=%v", err)
	}
}

func TestScopeExpansionRejectsStaleDecisionAndForbiddenOrCoveredPaths(t *testing.T) {
	f := newExpansionFixture(t)
	ctx := context.Background()
	for _, scope := range []run.ReviewScope{paths("current/child"), run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{"current"}}} {
		input := f.input()
		input.AdditionalScope = scope
		if _, err := f.service.Create(ctx, input); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("invalid additional scope=%+v err=%v", scope, err)
		}
	}
	forbidden := f.input()
	forbidden.AdditionalScope = paths("../outside")
	if _, err := f.service.Create(ctx, forbidden); !errors.Is(err, reviewaccess.ErrScopeForbidden) {
		t.Fatalf("forbidden path=%v", err)
	}
	testLike := f.input()
	testLike.AdditionalScope = paths("tests")
	value, err := f.service.Create(ctx, testLike)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Reject(ctx, "session-a", value.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AuthorizeReviewScope(ctx, f.owner.ID, paths("tests")); !errors.Is(err, ErrConflict) {
		t.Fatalf("rejection became authority=%v", err)
	}
	pending, err := f.service.Create(ctx, f.input())
	if err != nil {
		t.Fatal(err)
	}
	concurrent := Clone(pending)
	concurrent.ID = "concurrent-allowed"
	concurrent.Status = StatusAllowed
	concurrent.AdditionalScope = paths("newer")
	f.store.values[concurrent.ID] = concurrent
	if _, err := f.service.Allow(ctx, "session-a", pending.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale approval=%v", err)
	}
	current, err := f.service.Get(ctx, "session-a", pending.ID)
	if err != nil || current.Status != StatusPendingConfirmation {
		t.Fatalf("stale decision mutated=%+v err=%v", current, err)
	}
}
