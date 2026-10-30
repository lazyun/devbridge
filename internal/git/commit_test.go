package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptedTaskCommitFenceAndRecovery(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "drift"}[drift], func(t *testing.T) {
			repo, pre := makeRepo(t)
			c := NewClient(Config{})
			ctx := context.Background()
			writeFile(t, filepath.Join(repo, "tracked.txt"), "accepted\n")
			writeFile(t, filepath.Join(repo, "new.txt"), "accepted new\n")
			snapshot, e := c.Snapshot(ctx, repo)
			if e != nil {
				t.Fatal(e)
			}
			if drift {
				writeFile(t, filepath.Join(repo, "later.txt"), "unverified\n")
			}
			got, e := c.FinalizeTask(ctx, repo, pre, snapshot, "task-1", "rev-1", "feature: A readable title")
			if drift {
				if !errors.Is(e, ErrSnapshotDrift) {
					t.Fatal(e)
				}
				h, _ := c.ResolveHEAD(ctx, repo)
				if h != pre {
					t.Fatal("drift committed")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if got == pre {
				t.Fatal("no commit")
			}
			replay, e := c.FinalizeTask(ctx, repo, pre, snapshot, "task-1", "rev-1", "feature: A readable title")
			if e != nil || replay != got {
				t.Fatalf("replay=%s %v", replay, e)
			}
			if e = c.RequireClean(ctx, repo); e != nil {
				t.Fatal(e)
			}
			tree, _ := c.CommitTree(ctx, repo, got)
			if tree != snapshot {
				t.Fatal("wrong accepted tree")
			}
			if count := strings.TrimSpace(gitRun(t, repo, "rev-list", "--count", pre+"..HEAD")); count != "1" {
				t.Fatal(count)
			}
		})
	}
}

func TestSelectiveTaskCommitPreservesUnrelatedDirtyBytesAndRealIndex(t *testing.T) {
	repo, pre := makeRepo(t)
	ctx := context.Background()
	c := NewClient(Config{})
	writeFile(t, filepath.Join(repo, "accepted.txt"), "accepted\n")
	if err := os.Remove(filepath.Join(repo, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "must survive\n")
	verified, err := c.Snapshot(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	// A caller may already have an unrelated real-index change. The private
	// delivery index must leave it alone too.
	gitRun(t, repo, "add", "unrelated.txt")
	beforeIndex := strings.TrimSpace(gitRun(t, repo, "write-tree"))
	got, tree, changed, err := c.FinalizeTaskPaths(ctx, repo, pre, verified, "task-selective", "revision-selective", "feature: selective", []string{"accepted.txt", "tracked.txt"})
	if err != nil || !changed || got == "" || tree == "" {
		t.Fatalf("commit=%q tree=%q changed=%t err=%v", got, tree, changed, err)
	}
	if value := strings.TrimSpace(gitRun(t, repo, "show", got+":accepted.txt")); value != "accepted" {
		t.Fatalf("accepted content %q", value)
	}
	if _, err := c.run(ctx, repo, "assert unrelated excluded", "cat-file", "-e", got+":unrelated.txt"); err == nil {
		t.Fatal("unrelated dirty file entered selective commit")
	}
	if _, err := c.run(ctx, repo, "assert accepted deletion", "cat-file", "-e", got+":tracked.txt"); err == nil {
		t.Fatal("accepted deletion was not committed")
	}
	if gotIndex := strings.TrimSpace(gitRun(t, repo, "write-tree")); gotIndex != beforeIndex {
		t.Fatalf("real index changed: before=%s after=%s", beforeIndex, gotIndex)
	}
	if value := strings.TrimSpace(gitRun(t, repo, "diff", "--cached", "--", "unrelated.txt")); value == "" {
		t.Fatal("unrelated staged bytes were cleared")
	}
	if value := strings.TrimSpace(gitRun(t, repo, "diff", "--", "accepted.txt")); value != "" {
		t.Fatal("accepted bytes should remain in worktree but match committed HEAD")
	}
	replay, replayTree, replayChanged, err := c.FinalizeTaskPaths(ctx, repo, pre, verified, "task-selective", "revision-selective", "feature: selective", []string{"accepted.txt", "tracked.txt"})
	if err != nil || replay != got || replayTree != tree || !replayChanged {
		t.Fatalf("replay commit=%q tree=%q changed=%t err=%v", replay, replayTree, replayChanged, err)
	}
}

func TestSelectiveTaskCommitRecoveryRejectsMatchingTrailersWithWrongTree(t *testing.T) {
	repo, pre := makeRepo(t)
	ctx := context.Background()
	c := NewClient(Config{})
	writeFile(t, filepath.Join(repo, "accepted.txt"), "accepted\n")
	verified, err := c.Snapshot(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "accepted.txt"), "forged\n")
	gitRun(t, repo, "add", "accepted.txt")
	message := "feature: selective proof"
	gitRun(t, repo, "commit", "-m", message+"\n\nDev-Bridge-Task: task-proof\nDev-Bridge-Task-Revision: revision-proof")
	if _, _, _, err = c.FinalizeTaskPaths(ctx, repo, pre, verified, "task-proof", "revision-proof", message, []string{"accepted.txt"}); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("matching trailers with wrong tree accepted: %v", err)
	}
}

func TestFeatureAreaMergeFFNormalConflictAndRecovery(t *testing.T) {
	for _, mode := range []string{"ff", "diverged", "conflict", "dirty", "contained", "noop"} {
		t.Run(mode, func(t *testing.T) {
			repo, base := makeRepo(t)
			ctx := context.Background()
			c := NewClient(Config{})
			branch := strings.TrimSpace(gitRun(t, repo, "symbolic-ref", "HEAD"))
			source := filepath.Join(t.TempDir(), "area")
			gitRun(t, repo, "worktree", "add", "-b", "area", source, base)
			file := "area.txt"
			if mode == "conflict" {
				file = "tracked.txt"
			}
			writeFile(t, filepath.Join(source, file), "area change\n")
			gitRun(t, source, "add", "-A")
			gitRun(t, source, "commit", "-m", "task one")
			writeFile(t, filepath.Join(source, "second.txt"), "second task\n")
			gitRun(t, source, "add", "-A")
			gitRun(t, source, "commit", "-m", "task two")
			sourceHead, _ := c.ResolveHEAD(ctx, source)
			if mode == "contained" || mode == "noop" {
				gitRun(t, repo, "merge", "--ff-only", sourceHead)
			}
			if mode == "diverged" || mode == "conflict" || mode == "contained" {
				file = "dev.txt"
				if mode == "conflict" {
					file = "tracked.txt"
				}
				writeFile(t, filepath.Join(repo, file), "dev change\n")
				gitRun(t, repo, "add", "-A")
				gitRun(t, repo, "commit", "-m", "other area")
			}
			expected, _ := c.ResolveHEAD(ctx, repo)
			if mode == "dirty" {
				writeFile(t, filepath.Join(repo, "unknown.txt"), "must remain\n")
			}
			result, e := c.MergeFeatureArea(ctx, source, "area", repo, branch, sourceHead, expected)
			if mode == "conflict" || mode == "dirty" {
				if e == nil {
					t.Fatal("accepted unsafe merge")
				}
				if mode == "conflict" && !errors.Is(e, ErrMergeConflict) {
					t.Fatal(e)
				}
				head, _ := c.ResolveHEAD(ctx, repo)
				if head != expected {
					t.Fatal("target changed")
				}
				if mode == "conflict" {
					if e = c.RequireClean(ctx, repo); e != nil {
						t.Fatal(e)
					}
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if mode == "ff" && result != sourceHead {
				t.Fatal("did not FF")
			}
			if mode == "diverged" {
				parents, _ := c.CommitParents(ctx, repo, result)
				if len(parents) != 2 {
					t.Fatal(parents)
				}
			}
			replay, e := c.MergeFeatureArea(ctx, source, "area", repo, branch, sourceHead, expected)
			if e != nil || replay != result {
				t.Fatalf("replay=%s %v", replay, e)
			}
			for _, p := range []string{repo, source} {
				head, _ := c.ResolveHEAD(ctx, p)
				if head != result {
					t.Fatal("workspace not synchronized")
				}
				if e = c.RequireClean(ctx, p); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestFrozenFeatureAreaMergePreservesSourceDirtyIndex(t *testing.T) {
	repo, base := makeRepo(t)
	ctx := context.Background()
	c := NewClient(Config{})
	branch := strings.TrimSpace(gitRun(t, repo, "symbolic-ref", "HEAD"))
	source := filepath.Join(t.TempDir(), "area")
	gitRun(t, repo, "worktree", "add", "-b", "area", source, base)
	writeFile(t, filepath.Join(source, "accepted.txt"), "accepted\n")
	gitRun(t, source, "add", "accepted.txt")
	gitRun(t, source, "commit", "-m", "accepted")
	sourceHead, _ := c.ResolveHEAD(ctx, source)
	// This is deliberately staged unrelated state left by selective delivery.
	writeFile(t, filepath.Join(source, "unrelated.txt"), "preserve\n")
	gitRun(t, source, "add", "unrelated.txt")
	expected, _ := c.ResolveHEAD(ctx, repo)
	got, err := c.MergeFeatureAreaFrozen(ctx, source, "area", repo, branch, sourceHead, expected)
	if err != nil || got == "" {
		t.Fatalf("merge=%q err=%v", got, err)
	}
	if head, _ := c.ResolveHEAD(ctx, source); head != sourceHead {
		t.Fatalf("source changed %s", head)
	}
	if staged := strings.TrimSpace(gitRun(t, source, "diff", "--cached", "--", "unrelated.txt")); staged == "" {
		t.Fatal("source index was modified")
	}
	if head, _ := c.ResolveHEAD(ctx, repo); head != got {
		t.Fatalf("target %s want %s", head, got)
	}
}

func TestFrozenFeatureAreaMergeRecoveryRejectsMatchingParentsWithWrongTree(t *testing.T) {
	repo, base := makeRepo(t)
	ctx := context.Background()
	c := NewClient(Config{})
	branch := strings.TrimSpace(gitRun(t, repo, "symbolic-ref", "HEAD"))
	sourcePath := filepath.Join(t.TempDir(), "area")
	gitRun(t, repo, "worktree", "add", "-b", "area-proof", sourcePath, base)
	writeFile(t, filepath.Join(sourcePath, "area.txt"), "accepted area bytes\n")
	gitRun(t, sourcePath, "add", "area.txt")
	gitRun(t, sourcePath, "commit", "-m", "accepted area")
	source, err := c.ResolveHEAD(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "development.txt"), "development bytes\n")
	gitRun(t, repo, "add", "development.txt")
	gitRun(t, repo, "commit", "-m", "development advanced")
	expected, err := c.ResolveHEAD(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	wrongTree, err := c.CommitTree(ctx, repo, expected)
	if err != nil {
		t.Fatal(err)
	}
	forged := strings.TrimSpace(gitRun(t, repo, "commit-tree", wrongTree, "-p", expected, "-p", source, "-m", "forged merge"))
	gitRun(t, repo, "reset", "--hard", forged)
	if _, err = c.MergeFeatureAreaFrozen(ctx, sourcePath, "area-proof", repo, branch, source, expected); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("matching merge parents with wrong tree accepted: %v", err)
	}
}
