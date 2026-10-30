package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/dataroot"
	bridgegit "github.com/lazyun/devbridge/internal/git"
)

func TestManagerCreatesExactIsolatedOwnedWorktreeAndRemovesIt(t *testing.T) {
	source, base := worktreeRepo(t)
	opened, err := dataroot.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	root := opened.Paths.Worktrees
	defer opened.Close()
	manager, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	value, err := manager.Create(context.Background(), CreateInput{RepositoryID: "repo-1", RepositoryPath: source, FeatureAreaID: "task-1", WorkspaceID: "run-1", RunID: "run-1", BaseCommit: base})
	if err != nil {
		t.Fatal(err)
	}
	if value.Path != filepath.Join(root, "run-1") || value.Branch != "devbridge/task-1/run-1" || value.ID != "wt_run-1" {
		t.Fatalf("worktree=%+v", value)
	}
	if within(opened.Paths.Root, source) {
		t.Fatalf("source repository unexpectedly stored in DataRoot: %s", source)
	}
	head := strings.TrimSpace(gitWorktree(t, value.Path, "rev-parse", "HEAD"))
	if head != base {
		t.Fatalf("head=%s want=%s", head, base)
	}
	writeWorktree(t, filepath.Join(value.Path, "tracked.txt"), "run mutation\n")
	sourceContents, err := os.ReadFile(filepath.Join(source, "tracked.txt"))
	if err != nil || string(sourceContents) != "source\n" {
		t.Fatalf("source=%q err=%v", sourceContents, err)
	}
	clean, err := bridgegit.NewClient(bridgegit.Config{}).IsClean(context.Background(), source)
	if err != nil || !clean {
		t.Fatalf("source working tree clean=%v err=%v", clean, err)
	}
	if err := manager.Remove(context.Background(), value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(value.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree path remains: %v", err)
	}
	exists, err := bridgegit.NewClient(bridgegit.Config{}).BranchExists(context.Background(), source, value.Branch)
	if err != nil || exists {
		t.Fatalf("branch exists=%v err=%v", exists, err)
	}
}

func TestManagerRejectsSymlinkWorktreeRoot(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "worktrees")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(link, bridgegit.NewClient(bridgegit.Config{})); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink root error = %v", err)
	}
}

func TestManagerRejectsRootReplacedBySymlinkBeforeCreate(t *testing.T) {
	source, base := worktreeRepo(t)
	parent := t.TempDir()
	root := filepath.Join(parent, "worktrees")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	escape := t.TempDir()
	if err := os.Symlink(escape, root); err != nil {
		t.Fatal(err)
	}
	_, err = manager.Create(context.Background(), CreateInput{RepositoryID: "repo", RepositoryPath: source, FeatureAreaID: "task", WorkspaceID: "run", RunID: "run", BaseCommit: base})
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("replaced root error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(escape, "run")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("worktree escaped root: %v", statErr)
	}
}

func TestManagerRejectsPathBranchAndUnknownRemovalCollisions(t *testing.T) {
	source, base := worktreeRepo(t)
	root := filepath.Join(t.TempDir(), "owned")
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	manager, _ := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	_, err := manager.Create(context.Background(), CreateInput{RepositoryID: "repo", RepositoryPath: source, FeatureAreaID: "task", WorkspaceID: "run", RunID: "run", BaseCommit: base})
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("path collision=%v", err)
	}
	if err := os.Remove(filepath.Join(root, "run")); err != nil {
		t.Fatal(err)
	}
	gitWorktree(t, source, "branch", "devbridge/task/run", base)
	_, err = manager.Create(context.Background(), CreateInput{RepositoryID: "repo", RepositoryPath: source, FeatureAreaID: "task", WorkspaceID: "run", RunID: "run", BaseCommit: base})
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("branch collision=%v", err)
	}
	if err := manager.Remove(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown removal=%v", err)
	}
}

func TestManagerRejectsNonImmutableBase(t *testing.T) {
	source, _ := worktreeRepo(t)
	manager, _ := NewManager(filepath.Join(t.TempDir(), "owned"), bridgegit.NewClient(bridgegit.Config{}))
	_, err := manager.Create(context.Background(), CreateInput{RepositoryID: "repo", RepositoryPath: source, FeatureAreaID: "task", WorkspaceID: "run", RunID: "run", BaseCommit: "HEAD"})
	if err == nil || !strings.Contains(err.Error(), "immutable full commit") {
		t.Fatalf("got %v", err)
	}
}

func TestManagerDoesNotCopyDirtySourceState(t *testing.T) {
	source, base := worktreeRepo(t)
	writeWorktree(t, filepath.Join(source, "tracked.txt"), "dirty source\n")
	writeWorktree(t, filepath.Join(source, "source-only.txt"), "not committed\n")
	manager, err := NewManager(filepath.Join(t.TempDir(), "owned"), bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	value, err := manager.Create(context.Background(), CreateInput{RepositoryID: "repo", RepositoryPath: source, FeatureAreaID: "task", WorkspaceID: "run", RunID: "run", BaseCommit: base})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(value.Path, "tracked.txt"))
	if err != nil || string(contents) != "source\n" {
		t.Fatalf("worktree tracked content = %q, %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(value.Path, "source-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dirty source file copied into worktree: %v", err)
	}
}

func TestSafePartProducesGitAndFilesystemSafeSegment(t *testing.T) {
	got, err := safePart("../task.lock/@{unsafe}")
	if err != nil || got != "task-lock-unsafe" {
		t.Fatalf("safePart = %q, %v", got, err)
	}
	if _, err := safePart("..."); err == nil {
		t.Fatal("punctuation-only id was accepted")
	}
}

type failingGit struct {
	branch  bool
	deleted bool
}

func (f *failingGit) ResolveCommit(context.Context, string, string) (string, error) {
	return strings.Repeat("a", 40), nil
}
func (f *failingGit) BranchExists(context.Context, string, string) (bool, error) {
	return f.branch, nil
}
func (f *failingGit) WorktreeAdd(_ context.Context, _ string, path, _ string, _ string) error {
	f.branch = true
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return errors.New("add failed")
}
func (f *failingGit) WorktreeRemove(context.Context, string, string) error { return nil }
func (f *failingGit) DeleteBranch(context.Context, string, string) error {
	f.deleted = true
	f.branch = false
	return nil
}
func TestManagerCleansOnlyItsPartialCreation(t *testing.T) {
	source := t.TempDir()
	root := filepath.Join(t.TempDir(), "owned")
	fake := &failingGit{}
	manager, _ := NewManager(root, fake)
	_, err := manager.Create(context.Background(), CreateInput{RepositoryID: "repo", RepositoryPath: source, FeatureAreaID: "task", WorkspaceID: "run", RunID: "run", BaseCommit: strings.Repeat("a", 40)})
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, statErr := os.Stat(filepath.Join(root, "run")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial path remains: %v", statErr)
	}
	if !fake.deleted {
		t.Fatal("owned partial branch was not cleaned")
	}
}

type blockingGit struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingGit) ResolveCommit(_ context.Context, _, commit string) (string, error) {
	f.once.Do(func() { close(f.entered) })
	<-f.release
	return commit, nil
}
func (*blockingGit) BranchExists(context.Context, string, string) (bool, error) { return false, nil }
func (*blockingGit) WorktreeAdd(context.Context, string, string, string, string) error {
	return errors.New("stop after reservation test")
}
func (*blockingGit) WorktreeRemove(context.Context, string, string) error { return nil }
func (*blockingGit) DeleteBranch(context.Context, string, string) error   { return nil }

func TestManagerReservesPathAcrossConcurrentCreation(t *testing.T) {
	fake := &blockingGit{entered: make(chan struct{}), release: make(chan struct{})}
	manager, err := NewManager(filepath.Join(t.TempDir(), "owned"), fake)
	if err != nil {
		t.Fatal(err)
	}
	input := CreateInput{RepositoryID: "repo", RepositoryPath: t.TempDir(), FeatureAreaID: "task", WorkspaceID: "same-run", RunID: "same-run", BaseCommit: strings.Repeat("a", 40)}
	firstDone := make(chan error, 1)
	go func() { _, err := manager.Create(context.Background(), input); firstDone <- err }()
	<-fake.entered
	if _, err := manager.Create(context.Background(), input); !errors.Is(err, ErrCollision) {
		t.Fatalf("concurrent create = %v, want collision", err)
	}
	close(fake.release)
	if err := <-firstDone; err == nil {
		t.Fatal("first fake creation unexpectedly succeeded")
	}
}

func worktreeRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitWorktree(t, repo, "init", "-b", "main")
	gitWorktree(t, repo, "config", "user.email", "devbridge@example.test")
	gitWorktree(t, repo, "config", "user.name", "Dev Bridge Test")
	writeWorktree(t, filepath.Join(repo, "tracked.txt"), "source\n")
	gitWorktree(t, repo, "add", ".")
	gitWorktree(t, repo, "commit", "-m", "initial")
	return repo, strings.TrimSpace(gitWorktree(t, repo, "rev-parse", "HEAD"))
}
func gitWorktree(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func writeWorktree(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
