package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClientRepositoryTruth(t *testing.T) {
	repo, first := makeRepo(t)
	client := NewClient(Config{})
	head, err := client.ResolveHEAD(context.Background(), repo)
	if err != nil || head != first {
		t.Fatalf("ResolveHEAD = %q, %v; want %q", head, err, first)
	}
	writeFile(t, filepath.Join(repo, "tracked.txt"), "changed\n")
	writeFile(t, filepath.Join(repo, "z-new.txt"), "new z\n")
	writeFile(t, filepath.Join(repo, "a-new.txt"), "new a\n")
	status, err := client.Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty() {
		t.Fatal("dirty status reported clean")
	}
	files, err := client.ChangedFiles(context.Background(), repo, first)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a-new.txt", "tracked.txt", "z-new.txt"}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("ChangedFiles=%v want %v", files, want)
	}
	diff, err := client.Diff(context.Background(), repo, first)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"tracked.txt", "a-new.txt", "new a", "z-new.txt"} {
		if !strings.Contains(diff, part) {
			t.Errorf("diff does not contain %q", part)
		}
	}
}

func TestClientResolveArbitraryCommitAndCommittedDiff(t *testing.T) {
	repo, first := makeRepo(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "second\n")
	gitRun(t, repo, "add", "tracked.txt")
	gitRun(t, repo, "commit", "-m", "second")
	second := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	client := NewClient(Config{})
	got, err := client.ResolveCommit(context.Background(), repo, first)
	if err != nil || got != first {
		t.Fatalf("resolve=%q,%v", got, err)
	}
	files, err := client.ChangedFiles(context.Background(), repo, first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{"tracked.txt"}) {
		t.Fatalf("files=%v", files)
	}
	diff, err := client.Diff(context.Background(), repo, first)
	if err != nil || !strings.Contains(diff, "second") {
		t.Fatalf("diff=%q err=%v", diff, err)
	}
	head, _ := client.ResolveHEAD(context.Background(), repo)
	if head != second {
		t.Fatalf("head=%s want %s", head, second)
	}
}

func TestClientCleanAndOutputLimit(t *testing.T) {
	repo, base := makeRepo(t)
	client := NewClient(Config{})
	clean, err := client.IsClean(context.Background(), repo)
	if err != nil || !clean {
		t.Fatalf("clean=%v err=%v", clean, err)
	}
	writeFile(t, filepath.Join(repo, "large.txt"), strings.Repeat("x", 256))
	small := NewClient(Config{MaxOutput: 32})
	_, err = small.Diff(context.Background(), repo, base)
	var limit *OutputLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("expected OutputLimitError, got %v", err)
	}
}

func TestClientCancellationAndCommandError(t *testing.T) {
	repo, _ := makeRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewClient(Config{}).ResolveHEAD(ctx, repo)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	_, err = NewClient(Config{}).ResolveCommit(context.Background(), repo, "missing")
	var command *CommandError
	if !errors.As(err, &command) {
		t.Fatalf("expected CommandError, got %v", err)
	}
}

func TestClientCurrentBranch(t *testing.T) {
	repo, _ := makeRepo(t)
	branch, err := NewClient(Config{}).CurrentBranch(context.Background(), repo)
	if err != nil || branch != "main" {
		t.Fatalf("branch=%q err=%v", branch, err)
	}
}

func TestInspectRepositoryStateSeparatesBranchDetachedAndDirtyFacts(t *testing.T) {
	repo, head := makeRepo(t)
	client := NewClient(Config{})
	state, err := client.InspectRepositoryState(context.Background(), repo)
	if err != nil || state.HeadCommit != head || state.BranchRef != "refs/heads/main" || state.Detached || state.WorkingTreeDirty {
		t.Fatalf("branch state=%+v err=%v", state, err)
	}
	writeFile(t, filepath.Join(repo, "dirty.txt"), "uncommitted\n")
	state, err = client.InspectRepositoryState(context.Background(), repo)
	if err != nil || !state.WorkingTreeDirty {
		t.Fatalf("dirty state=%+v err=%v", state, err)
	}
	gitRun(t, repo, "checkout", "--detach", head)
	state, err = client.InspectRepositoryState(context.Background(), repo)
	if err != nil || !state.Detached || state.BranchRef != "" || state.HeadCommit != head {
		t.Fatalf("detached state=%+v err=%v", state, err)
	}
}

func TestSearchFixedUsesLiteralTrackedSearchWithPathRestriction(t *testing.T) {
	repo, _ := makeRepo(t)
	writeFile(t, filepath.Join(repo, "scope.txt"), "needle.* literal\nneedle plain\n")
	writeFile(t, filepath.Join(repo, "other.txt"), "needle.* outside restriction\n")
	gitRun(t, repo, "add", "scope.txt", "other.txt")
	gitRun(t, repo, "commit", "-m", "search fixture")
	matches, err := NewClient(Config{}).SearchFixed(context.Background(), repo, "needle.*", []string{"scope.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := []SearchMatch{{Path: "scope.txt", Line: 1, Snippet: "needle.* literal"}}
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf("matches=%+v want=%+v", matches, want)
	}
}

func TestSearchFixedDoesNotApplySemanticFilenameExclusions(t *testing.T) {
	repo, _ := makeRepo(t)
	writeFile(t, filepath.Join(repo, "service.go"), "review-needle production\n")
	writeFile(t, filepath.Join(repo, "service_test.go"), "review-needle unit\n")
	if err := os.MkdirAll(filepath.Join(repo, "tests"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "tests", "fixture.go"), "review-needle fixture\n")
	gitRun(t, repo, "add", "service.go", "service_test.go", "tests/fixture.go")
	gitRun(t, repo, "commit", "-m", "production search fixture")
	matches, err := NewClient(Config{}).SearchFixed(context.Background(), repo, "review-needle", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []SearchMatch{{Path: "service.go", Line: 1, Snippet: "review-needle production"}, {Path: "service_test.go", Line: 1, Snippet: "review-needle unit"}, {Path: "tests/fixture.go", Line: 1, Snippet: "review-needle fixture"}}
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf("matches=%+v want=%+v", matches, want)
	}
}

func TestSearchFixedReadsLiveTrackedWorkingTreeContent(t *testing.T) {
	repo, _ := makeRepo(t)
	writeFile(t, filepath.Join(repo, "tracked.txt"), "live-working-tree-needle\n")
	matches, err := NewClient(Config{}).SearchFixed(context.Background(), repo, "live-working-tree-needle", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []SearchMatch{{Path: "tracked.txt", Line: 1, Snippet: "live-working-tree-needle"}}
	if !reflect.DeepEqual(matches, want) {
		t.Fatalf("matches=%+v want=%+v", matches, want)
	}
}

func TestSnapshotCapturesPerRunDeltaWithoutMutatingRealIndex(t *testing.T) {
	repo, _ := makeRepo(t)
	writeFile(t, filepath.Join(repo, "second.txt"), "keep then delete\n")
	gitRun(t, repo, "add", "second.txt")
	gitRun(t, repo, "commit", "-m", "add second file")

	// Preserve a deliberately staged change across every snapshot operation.
	writeFile(t, filepath.Join(repo, "tracked.txt"), "staged before runs\n")
	gitRun(t, repo, "add", "tracked.txt")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "dirty before run one\n")
	writeFile(t, filepath.Join(repo, "preexisting-untracked.txt"), "not made by a run\n")
	indexBefore := gitRun(t, repo, "diff", "--cached", "--binary")

	client := NewClient(Config{})
	startOne, err := client.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "tracked.txt"), "changed by run one\n")
	writeFile(t, filepath.Join(repo, "run-one.txt"), "created by run one\n")
	if err := os.Remove(filepath.Join(repo, "second.txt")); err != nil {
		t.Fatal(err)
	}
	endOne, err := client.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	filesOne, err := client.ChangedFilesBetween(context.Background(), repo, startOne, endOne)
	if err != nil {
		t.Fatal(err)
	}
	wantOne := []string{"run-one.txt", "second.txt", "tracked.txt"}
	if !reflect.DeepEqual(filesOne, wantOne) {
		t.Fatalf("run one files=%v want=%v", filesOne, wantOne)
	}
	diffOne, err := client.DiffSnapshots(context.Background(), repo, startOne, endOne)
	if err != nil || !strings.Contains(diffOne, "created by run one") || !strings.Contains(diffOne, "deleted file mode") {
		t.Fatalf("run one diff=%q err=%v", diffOne, err)
	}

	startTwo, err := client.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if startTwo != endOne {
		t.Fatalf("unchanged boundary trees differ: end one=%s start two=%s", endOne, startTwo)
	}
	writeFile(t, filepath.Join(repo, "run-two.txt"), "created by run two\n")
	endTwo, err := client.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	filesTwo, err := client.ChangedFilesBetween(context.Background(), repo, startTwo, endTwo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filesTwo, []string{"run-two.txt"}) {
		t.Fatalf("run two files=%v", filesTwo)
	}
	if indexAfter := gitRun(t, repo, "diff", "--cached", "--binary"); indexAfter != indexBefore {
		t.Fatalf("snapshot mutated real index:\nbefore:\n%s\nafter:\n%s", indexBefore, indexAfter)
	}
}

func TestCommandContextActuallyStopsProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("process cancellation test")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "git")
	writeFile(t, script, "#!/bin/sh\nsleep 10\n")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := NewClient(Config{Executable: script}).ResolveHEAD(ctx, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
}

func makeRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.email", "devbridge@example.test")
	gitRun(t, repo, "config", "user.name", "Dev Bridge Test")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "first\n")
	gitRun(t, repo, "add", "tracked.txt")
	gitRun(t, repo, "commit", "-m", "first")
	return repo, strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
}
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
