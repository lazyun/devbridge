package projectselection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
)

func TestSelectIsViewOnlyAndDoesNotDriveProjectContext(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	projects := project.NewService(project.NewMemoryStore(), func() (string, error) { return "project", nil }, func() time.Time { return now })
	if _, err := projects.Create(ctx, "P"); err != nil {
		t.Fatal(err)
	}
	contexts := projectcontext.NewService(projectcontext.NewMemoryStore(), func() time.Time { now = now.Add(time.Second); return now })
	service := NewService(NewMemoryStore(), projects, func() time.Time { now = now.Add(time.Second); return now })
	if _, err := service.Select(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := contexts.Get(ctx, "project"); !errors.Is(err, projectcontext.ErrNotFound) {
		t.Fatalf("console selection created ProjectContext: %v", err)
	}
	if _, err := contexts.EnsureRequired(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := contexts.MarkRunning(ctx, "project", "probe-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := contexts.MarkFinished(ctx, "project", "probe-one", projectcontext.StatusReady, "result", "", "sha256:one"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Select(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	value, err := contexts.Get(ctx, "project")
	if err != nil || value.Status != projectcontext.StatusReady {
		t.Fatalf("usable context was invalidated: %+v err=%v", value, err)
	}
	if _, err := service.Select(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	value, err = contexts.Get(ctx, "project")
	if err != nil || value.Status != projectcontext.StatusReady {
		t.Fatalf("selection mutated usable context: %+v err=%v", value, err)
	}
}
