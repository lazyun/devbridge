package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateLocalBranchExactReplayConflictAndNoCheckout(t *testing.T) {
	path, base := makeRepo(t)
	client, ctx := NewClient(Config{}), context.Background()
	before := gitRun(t, path, "symbolic-ref", "HEAD")
	value, err := client.CreateLocalBranch(ctx, path, "refs/heads/new-work", base)
	if err != nil || value.HeadSHA != base || value.RepositoryCheckout {
		t.Fatalf("create=%+v err=%v", value, err)
	}
	if _, err := client.CreateLocalBranch(ctx, path, value.FullRef, base); err != nil {
		t.Fatal(err)
	}
	gitRun(t, path, "commit", "--allow-empty", "-m", "next")
	changed := strings.TrimSpace(gitRun(t, path, "rev-parse", "HEAD"))
	if _, err := client.CreateLocalBranch(ctx, path, value.FullRef, changed); !errors.Is(err, ErrBranchConflict) {
		t.Fatalf("conflict=%v", err)
	}
	if got := gitRun(t, path, "symbolic-ref", "HEAD"); got != before {
		t.Fatalf("checkout changed: %s", got)
	}
	if got := strings.TrimSpace(gitRun(t, path, "rev-parse", value.FullRef)); got != base {
		t.Fatal("existing branch overwritten")
	}
	for _, invalid := range []string{"HEAD", "refs/tags/tag", "refs/heads/a..b", "refs/heads/a\nrefs/heads/b"} {
		if _, err := client.CreateLocalBranch(ctx, path, invalid, base); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

func TestCreateLocalBranchRejectsSymbolicAliasAndInheritedRepository(t *testing.T) {
	path, base := makeRepo(t)
	other, _ := makeRepo(t)
	gitRun(t, path, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	client := NewClient(Config{})
	if _, err := client.CreateLocalBranch(context.Background(), path, "refs/heads/alias", base); err == nil {
		t.Fatal("symbolic branch overwritten")
	}
	if _, err := client.CreateLocalBranch(context.Background(), path, "refs/heads/scoped", base); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{path, other} {
		inventory, err := client.LocalBranches(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, branch := range inventory.Branches {
			found = found || branch.ShortName == "scoped"
		}
		if found != (repo == path) {
			t.Fatal("branch created outside authorized Repository")
		}
	}
}
