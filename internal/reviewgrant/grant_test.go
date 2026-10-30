package reviewgrant

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/run"
)

func TestEnsurePersistsImmutableFocusedInitialScopeIdempotently(t *testing.T) {
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	service, err := NewService(NewMemoryStore(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := Grant{
		ReviewID: "task-a", FeatureAreaID: "feature-a", ProjectID: "project-a", RepositoryID: "repo-a", Mode: ModeFocused,
		InitialScope:    run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/api", "internal/workflow"}},
		SelectedModules: []ModuleSelection{{Key: "api", Role: RolePrimary}, {Key: "workflow", Role: RoleSupporting}},
	}
	first, err := service.Ensure(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.InitialScope.Paths[0] = "mutated"
	input.SelectedModules[0].Key = "mutated"
	stored, err := service.Get(context.Background(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.InitialScope.Paths, []string{"internal/api", "internal/workflow"}) || stored.SelectedModules[0].Key != "api" || !stored.CreatedAt.Equal(now) {
		t.Fatalf("stored grant = %+v", stored)
	}
	repeated, err := service.Ensure(context.Background(), first)
	if err != nil || !reflect.DeepEqual(repeated, first) {
		t.Fatalf("idempotent Ensure = %+v err=%v", repeated, err)
	}
	conflict := first
	conflict.InitialScope.Paths = []string{"internal/api"}
	if _, err := service.Ensure(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed initial authority error = %v", err)
	}
}

func TestEnsurePersistsRepositoryGrantWithoutModuleDecision(t *testing.T) {
	service, err := NewService(NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Ensure(context.Background(), Grant{ReviewID: "task-a", FeatureAreaID: "feature-a", ProjectID: "project-a", RepositoryID: "repo-a", Mode: ModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil || value.Mode != ModeRepository || value.InitialScope.Kind != run.ReviewScopeRepository || value.InitialScope.Paths == nil || len(value.SelectedModules) != 0 {
		t.Fatalf("repository grant = %+v err=%v", value, err)
	}
}

func TestFocusedGrantRequiresPrimaryModule(t *testing.T) {
	service, err := NewService(NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Ensure(context.Background(), Grant{ReviewID: "task-a", FeatureAreaID: "feature-a", ProjectID: "project-a", RepositoryID: "repo-a", Mode: ModeFocused, InitialScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/api"}}, SelectedModules: []ModuleSelection{{Key: "api", Role: RoleSupporting}}})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("supporting-only grant error = %v", err)
	}
}
