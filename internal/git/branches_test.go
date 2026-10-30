package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLocalBranchInventoryDoesNotInferUnbornOrDetachedBranches(t *testing.T) {
	path := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "only/development")
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %s %v", out, err)
	}
	client := NewClient(Config{})
	empty, err := client.LocalBranches(context.Background(), path)
	if err != nil || len(empty.Branches) != 0 || empty.CheckoutRef != "refs/heads/only/development" || empty.Detached {
		t.Fatalf("unborn: %+v %v", empty, err)
	}
	gitRun(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "initial")
	one, err := client.LocalBranches(context.Background(), path)
	if err != nil || len(one.Branches) != 1 || one.Branches[0].FullRef != "refs/heads/only/development" || !one.Branches[0].RepositoryCheckout {
		t.Fatalf("single: %+v %v", one, err)
	}
	gitRun(t, path, "checkout", "--detach")
	detached, err := client.LocalBranches(context.Background(), path)
	if err != nil || !detached.Detached || len(detached.Branches) != 1 || detached.Branches[0].RepositoryCheckout {
		t.Fatalf("detached: %+v %v", detached, err)
	}
}

func TestLocalBranchInventoryIgnoresInheritedRepositoryOverrides(t *testing.T) {
	first, _ := makeRepo(t)
	second, _ := makeRepo(t)
	gitRun(t, first, "branch", "only-first")
	gitRun(t, second, "branch", "only-second")
	t.Setenv("GIT_DIR", filepath.Join(second, ".git"))
	t.Setenv("GIT_WORK_TREE", second)
	t.Setenv("GIT_NAMESPACE", "hidden")
	got, err := NewClient(Config{}).LocalBranches(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, branch := range got.Branches {
		if branch.ShortName == "only-second" {
			t.Fatal("observed another Repository")
		}
		if branch.ShortName == "only-first" {
			found = true
		}
	}
	if !found {
		t.Fatalf("registered Repository missing: %+v", got)
	}
}
