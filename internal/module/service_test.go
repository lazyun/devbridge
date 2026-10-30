package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/project"
)

type projectLookup map[string]project.Project

func (p projectLookup) Get(_ context.Context, id string) (project.Project, error) {
	value, exists := p[id]
	if !exists {
		return project.Project{}, project.ErrNotFound
	}
	return value, nil
}

func TestCreateGetListAndProjectScopedKeys(t *testing.T) {
	store := NewMemoryStore()
	projects := projectLookup{"p1": {ID: "p1"}, "p2": {ID: "p2"}}
	next := 0
	service := NewService(store, projects, func() (string, error) { next++; return []string{"root", "child", "other"}[next-1], nil }, func() time.Time { return time.Unix(1, 0) })
	root, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", Key: "core", Name: "Core"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", Key: "core.api", Name: "API", ParentID: &root.ID})
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatalf("child=%+v", child)
	}
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "p2", Key: "core", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	values, err := service.ListByProject(context.Background(), "p1")
	if err != nil || len(values) != 2 {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	got, err := service.Get(context.Background(), root.ID)
	if err != nil || got.Key != root.Key {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if err := store.Create(context.Background(), Module{ID: "duplicate", ProjectID: "p1", Key: "core", Name: "Duplicate"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate err=%v", err)
	}
}

func TestParentValidation(t *testing.T) {
	store := NewMemoryStore()
	projects := projectLookup{"p1": {ID: "p1"}, "p2": {ID: "p2"}}
	now := time.Unix(1, 0)
	if err := store.Create(context.Background(), Module{ID: "p2-parent", ProjectID: "p2", Key: "parent", Name: "Parent", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, projects, func() (string, error) { return "self", nil }, nil)
	missing := "missing"
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", Key: "child", Name: "Child", ParentID: &missing}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing parent err=%v", err)
	}
	cross := "p2-parent"
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", Key: "child", Name: "Child", ParentID: &cross}); err == nil {
		t.Fatal("cross-project parent accepted")
	}
	self := "self"
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", Key: "child", Name: "Child", ParentID: &self}); err == nil {
		t.Fatal("self-parent accepted")
	}
}

func TestCreateRejectsCyclicExistingAncestry(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(1, 0)
	a, b := "a", "b"
	if err := store.Create(context.Background(), Module{ID: a, ProjectID: "p1", Key: "a", Name: "A", ParentID: &b, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), Module{ID: b, ProjectID: "p1", Key: "b", Name: "B", ParentID: &a, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, projectLookup{"p1": {ID: "p1"}}, func() (string, error) { return "c", nil }, nil)
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "p1", Key: "c", Name: "C", ParentID: &a}); err == nil {
		t.Fatal("cyclic ancestry accepted")
	}
}
