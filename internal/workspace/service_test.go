package workspace

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceOwnsOneImmutableLineagePerFeatureArea(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	ids := []string{"workspace-one", "workspace-two"}
	service := NewService(NewMemoryStore(), func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}, func() time.Time { return now })

	created, err := service.EnsureForFeatureArea(ctx, "project", "repo", "task-one", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := service.EnsureForFeatureArea(ctx, "project", "repo", "task-one", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err != nil || again.ID != created.ID || again.BaseCommit != created.BaseCommit {
		t.Fatalf("duplicate task workspace error=%v", err)
	}
	created, err = service.AttachWorktree(ctx, created.ID, "worktree-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AttachWorktree(ctx, created.ID, "worktree-other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("worktree reassignment error=%v", err)
	}
	baseline := TokenCounts{InputTokens: 80, OutputTokens: 20, TotalTokens: 100}
	updated, err := service.RecordTokenBaseline(ctx, created.ID, &baseline)
	if err != nil {
		t.Fatal(err)
	}
	if updated.FeatureAreaID != "task-one" || updated.WorktreeID != "worktree-one" || updated.LegacyThreadID != "" || !updated.TokenBaselineAvailable || updated.TokenBaseline.TotalTokens != 100 {
		t.Fatalf("workspace=%+v", updated)
	}

}

func TestDifferentFeatureAreasReceiveDifferentWorkspaces(t *testing.T) {
	ctx := context.Background()
	ids := []string{"workspace-a", "workspace-b"}
	service := NewService(NewMemoryStore(), func() (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}, nil)
	a, err := service.EnsureForFeatureArea(ctx, "project", "repo", "task-a", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.EnsureForFeatureArea(ctx, "project", "repo", "task-b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.FeatureAreaID == b.FeatureAreaID {
		t.Fatalf("workspaces were shared: a=%+v b=%+v", a, b)
	}
}
