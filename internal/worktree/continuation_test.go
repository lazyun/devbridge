package worktree

import (
	"context"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"path/filepath"
	"testing"
)

func TestCanonicalWorktreeCrashRecoveryKeepsCommitsAndDirtyFiles(t *testing.T) {
	source, base := worktreeRepo(t)
	root := filepath.Join(t.TempDir(), "owned")
	ctx := context.Background()
	input := CreateInput{RepositoryID: "repository", RepositoryPath: source, FeatureAreaID: "area", WorkspaceID: "workspace", RunID: "first-run", BaseCommit: base}
	first, e := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if e != nil {
		t.Fatal(e)
	}
	v, e := first.Create(ctx, input)
	if e != nil {
		t.Fatal(e)
	}
	writeWorktree(t, filepath.Join(v.Path, "tracked.txt"), "committed Task work\n")
	gitWorktree(t, v.Path, "add", ".")
	gitWorktree(t, v.Path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "Task commit")
	writeWorktree(t, filepath.Join(v.Path, "pending.txt"), "uncommitted Task work\n")
	// Simulate death after physical worktree creation but before durable metadata
	// attachment: a new Manager only knows the persisted Workspace intent.
	reopened, e := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if e != nil {
		t.Fatal(e)
	}
	input.RunID = "later-run"
	recovered, e := reopened.Create(ctx, input)
	if e != nil || recovered.ID != v.ID || recovered.Path != v.Path || recovered.BaseCommit != base {
		t.Fatalf("recovered=%+v %v", recovered, e)
	}
	if gitWorktree(t, recovered.Path, "status", "--porcelain") == "" {
		t.Fatal("dirty work discarded")
	}
	if again, e := reopened.Create(ctx, input); e != nil || again.ID != v.ID {
		t.Fatalf("idempotent replay=%+v %v", again, e)
	}
	gitWorktree(t, recovered.Path, "checkout", "-qb", "foreign-branch")
	if _, e = reopened.Create(ctx, input); e == nil {
		t.Fatal("silently accepted changed branch identity")
	}
	input.FeatureAreaID = "foreign-area"
	if _, e = reopened.Create(ctx, input); e == nil {
		t.Fatal("silently accepted another owner")
	}
}
