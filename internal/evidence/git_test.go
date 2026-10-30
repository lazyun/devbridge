package evidence

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	bridgegit "github.com/lazyun/devbridge/internal/git"
)

func TestCollectorUnchangedTrackedUntrackedAndCommitted(t *testing.T) {
	repo, base := evidenceRepo(t)
	collector := NewCollector(bridgegit.NewClient(bridgegit.Config{}))
	unchanged, err := collector.Collect(context.Background(), repo, base)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Dirty || len(unchanged.ChangedFiles) != 0 || unchanged.Diff != "" || unchanged.BaseCommit != base || unchanged.HeadCommit != base {
		t.Fatalf("unchanged=%+v", unchanged)
	}
	writeEvidence(t, filepath.Join(repo, "tracked.txt"), "modified\n")
	writeEvidence(t, filepath.Join(repo, "new.txt"), "untracked\n")
	dirty, err := collector.Collect(context.Background(), repo, base)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.Dirty || !reflect.DeepEqual(dirty.ChangedFiles, []string{"new.txt", "tracked.txt"}) || !strings.Contains(dirty.Diff, "untracked") {
		t.Fatalf("dirty=%+v", dirty)
	}
	gitEvidence(t, repo, "add", ".")
	gitEvidence(t, repo, "commit", "-m", "change")
	committed, err := collector.Collect(context.Background(), repo, base)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Dirty || len(committed.ChangedFiles) != 2 || committed.HeadCommit == base {
		t.Fatalf("committed=%+v", committed)
	}
}

func TestCollectorUsesWorktreeNotDirtySource(t *testing.T) {
	source, base := evidenceRepo(t)
	worktree := filepath.Join(t.TempDir(), "run")
	gitEvidence(t, source, "worktree", "add", "-b", "devbridge/test/run", worktree, base)
	writeEvidence(t, filepath.Join(source, "source-only.txt"), "dirty source\n")
	got, err := NewCollector(bridgegit.NewClient(bridgegit.Config{})).Collect(context.Background(), worktree, base)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dirty || len(got.ChangedFiles) != 0 || strings.Contains(got.Diff, "source-only") {
		t.Fatalf("source leaked into evidence: %+v", got)
	}
}

func TestArtifactsKeepClaimsSeparate(t *testing.T) {
	claim := AgentClaim{Message: "tests passed"}
	git := GitEvidence{Diff: "actual diff"}
	items := Artifacts(claim, git)
	if len(items) != 2 || items[0].Kind != ArtifactAgentMessage || items[1].Kind != ArtifactGitDiff {
		t.Fatalf("artifacts=%+v", items)
	}
}

func evidenceRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	gitEvidence(t, repo, "init", "-b", "main")
	gitEvidence(t, repo, "config", "user.email", "devbridge@example.test")
	gitEvidence(t, repo, "config", "user.name", "Dev Bridge Test")
	writeEvidence(t, filepath.Join(repo, "tracked.txt"), "initial\n")
	gitEvidence(t, repo, "add", ".")
	gitEvidence(t, repo, "commit", "-m", "initial")
	return repo, strings.TrimSpace(gitEvidence(t, repo, "rev-parse", "HEAD"))
}
func gitEvidence(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func writeEvidence(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
