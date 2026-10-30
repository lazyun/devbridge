package reviewdrift_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/repository"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewdrift"
)

type fakeReviews struct{ value productreview.Review }

func (f *fakeReviews) Get(context.Context, string) (productreview.Review, error) {
	return f.value, nil
}

type fakeProvenance struct {
	value productreview.RepositoryProvenance
	err   error
}

func (f *fakeProvenance) RepositoryProvenance(context.Context, string) (productreview.RepositoryProvenance, error) {
	return f.value, f.err
}

type fakeRepositories struct{ value repository.Repository }

func (f *fakeRepositories) Get(context.Context, string) (repository.Repository, error) {
	return f.value, nil
}

func createGitRepo(t *testing.T) (string, string) {
	t.Helper()
	path := t.TempDir()
	run := func(args ...string) {
		command := exec.Command("git", args...)
		command.Dir = path
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.MkdirAll(filepath.Join(path, "reviewed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "reviewed/interface.go"), []byte("package app\n\nconst Name = \"v1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "unrelated/other.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	run("add", ".")
	run("-c", "user.name=Drift", "-c", "user.email=drift@example.invalid", "commit", "-qm", "reviewed state")
	head, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return path, string(head[:len(head)-1])
}

func commitAll(t *testing.T, repoPath, message string) string {
	t.Helper()
	command := exec.Command("git", "-C", repoPath, "add", "-A")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", repoPath, "-c", "user.name=Drift", "-c", "user.email=drift@example.invalid", "commit", "-qm", message)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	head, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(head[:len(head)-1])
}

func timeNow() time.Time { return time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC) }

func driftService(t *testing.T, repoPath string, provenance productreview.RepositoryProvenance, provenanceErr error, focusPaths []string) *reviewdrift.Service {
	t.Helper()
	service, err := reviewdrift.New(
		&fakeReviews{value: productreview.Review{ID: "review_1", Focus: productreview.Focus{Kind: productreview.FocusFocused, Paths: focusPaths}}},
		&fakeProvenance{value: provenance, err: provenanceErr},
		&fakeRepositories{value: repository.Repository{ID: provenance.RepositoryID, Path: repoPath}},
		git.NewClient(git.Config{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestUnknownProvenanceIsFactuallyUnavailable(t *testing.T) {
	repoPath, _ := createGitRepo(t)
	service := driftService(t, repoPath, productreview.RepositoryProvenance{}, productreview.ErrProvenanceNotFound, nil)
	drift, err := service.RepositoryDrift(context.Background(), "review_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if drift.Available || drift.UnavailableReason == "" || drift.ReviewCommit != "" {
		t.Fatalf("unknown provenance drift = %+v", drift)
	}
	if drift.RepositoryChanged || drift.ChangedFileCount != 0 || drift.FocusPathsChanged != nil {
		t.Fatalf("unavailable drift fabricated facts: %+v", drift)
	}
}

func TestUnresolvableReviewCommitIsFactuallyUnavailable(t *testing.T) {
	repoPath, _ := createGitRepo(t)
	service := driftService(t, repoPath, productreview.RepositoryProvenance{
		ReviewID: "review_1", RepositoryID: "repo_1", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CapturedAt: timeNow(),
	}, nil, nil)
	drift, err := service.RepositoryDrift(context.Background(), "review_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if drift.Available || drift.ReviewCommit == "" || !strings.Contains(drift.UnavailableReason, "no longer resolvable") {
		t.Fatalf("unresolvable commit drift = %+v", drift)
	}
}

func TestUnchangedHeadReportsZeroDrift(t *testing.T) {
	repoPath, head := createGitRepo(t)
	service := driftService(t, repoPath, productreview.RepositoryProvenance{ReviewID: "review_1", RepositoryID: "repo_1", Commit: head, CapturedAt: timeNow()}, nil, []string{"reviewed/interface.go"})
	drift, err := service.RepositoryDrift(context.Background(), "review_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Available || drift.RepositoryChanged || drift.ChangedFileCount != 0 || drift.CurrentCommit != head || len(drift.FocusPathsChanged) != 0 {
		t.Fatalf("unchanged drift = %+v", drift)
	}
}

func TestChangedReviewedPathsAreBoundedAndCounted(t *testing.T) {
	repoPath, base := createGitRepo(t)
	// Three explicitly reviewed paths change; two unrelated files change too.
	if err := os.MkdirAll(filepath.Join(repoPath, "reviewed"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(repoPath, "reviewed", name), []byte("package reviewed // changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoPath, "unrelated/one.go"), []byte("package other // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "unrelated/two.go"), []byte("package other // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := commitAll(t, repoPath, "team changes")
	service := driftService(t, repoPath, productreview.RepositoryProvenance{ReviewID: "review_1", RepositoryID: "repo_1", Commit: base, CapturedAt: timeNow()}, nil,
		[]string{"reviewed/a.go", "reviewed/b.go", "reviewed/c.go"})
	drift, err := service.RepositoryDrift(context.Background(), "review_1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Available || !drift.RepositoryChanged || drift.ChangedFileCount != 5 {
		t.Fatalf("drift = %+v", drift)
	}
	if drift.ChangedFocusPathCount != 3 || drift.OtherChangedFileCount != 2 {
		t.Fatalf("counts = %+v", drift)
	}
	if len(drift.FocusPathsChanged) != 2 || !drift.Truncated {
		t.Fatalf("bounded list = %+v truncated=%v", drift.FocusPathsChanged, drift.Truncated)
	}
	if drift.FocusPathsChanged[0].Path != "reviewed/a.go" || drift.FocusPathsChanged[1].Path != "reviewed/b.go" {
		t.Fatalf("bounded list order = %+v", drift.FocusPathsChanged)
	}
	_ = next
}

func TestRepositoryLevelFactsWithoutRecordedPaths(t *testing.T) {
	repoPath, base := createGitRepo(t)
	if err := os.WriteFile(filepath.Join(repoPath, "unrelated/new.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	next := commitAll(t, repoPath, "unrelated change")
	service := driftService(t, repoPath, productreview.RepositoryProvenance{ReviewID: "review_1", RepositoryID: "repo_1", Commit: base, CapturedAt: timeNow()}, nil, nil)
	drift, err := service.RepositoryDrift(context.Background(), "review_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Available || !drift.RepositoryChanged || drift.ChangedFileCount != 1 || drift.CurrentCommit != next {
		t.Fatalf("repository-level drift = %+v", drift)
	}
	if len(drift.FocusPathsChanged) != 0 || drift.ChangedFocusPathCount != 0 || drift.OtherChangedFileCount != 1 {
		t.Fatalf("no recorded paths must yield repository-level facts only: %+v", drift)
	}
}

func TestRenamedReviewedPathCarriesOldAndNewPath(t *testing.T) {
	repoPath, base := createGitRepo(t)
	if err := os.MkdirAll(filepath.Join(repoPath, "reviewed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(repoPath, "reviewed/interface.go"), filepath.Join(repoPath, "reviewed/api.go")); err != nil {
		t.Fatal(err)
	}
	next := commitAll(t, repoPath, "rename reviewed interface")
	service := driftService(t, repoPath, productreview.RepositoryProvenance{ReviewID: "review_1", RepositoryID: "repo_1", Commit: base, CapturedAt: timeNow()}, nil, []string{"reviewed/interface.go"})
	drift, err := service.RepositoryDrift(context.Background(), "review_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if drift.ChangedFocusPathCount != 1 || len(drift.FocusPathsChanged) != 1 {
		t.Fatalf("rename drift = %+v", drift)
	}
	change := drift.FocusPathsChanged[0]
	if change.Change != productreview.ChangeRenamed || change.OldPath != "reviewed/interface.go" || change.Path != "reviewed/api.go" {
		t.Fatalf("rename path change = %+v", change)
	}
	_ = next
}

func TestReviewWithoutProvenanceRecordErrors(t *testing.T) {
	service := driftService(t, t.TempDir(), productreview.RepositoryProvenance{}, errors.New("boom"), nil)
	if _, err := service.RepositoryDrift(context.Background(), "review_1", 0); err == nil {
		t.Fatal("store failure must surface, not fabricate drift")
	}
}

func TestDriftProjectionUsesFocusTerminologyWithoutSemanticJudgement(t *testing.T) {
	drift := productreview.RepositoryDrift{
		RepositoryID: "repo_1", ReviewCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CurrentCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Available: true,
		RepositoryChanged: true, ChangedFileCount: 3,
		FocusPathsChanged:     []productreview.FocusPathChange{{Change: productreview.ChangeModified, Path: "reviewed/interface.go"}},
		ChangedFocusPathCount: 1, OtherChangedFileCount: 2,
	}
	encoded, err := json.Marshal(drift)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	// Terminology: focus paths, never claims of actual inspection.
	if !strings.Contains(text, `"focusPathsChanged"`) || !strings.Contains(text, `"changedFocusPathCount"`) {
		t.Fatalf("projection lost focus terminology: %s", text)
	}
	if strings.Contains(text, "reviewedPathsChanged") || strings.Contains(text, "changedReviewedFileCount") {
		t.Fatalf("projection still claims inspected-path semantics: %s", text)
	}
	// No semantic staleness judgement exists anywhere in the factual facts.
	for _, forbidden := range []string{"stale", "invalid", "outdated", "must_review", "needs_refresh", "refresh_required"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("projection contains semantic judgement %q: %s", forbidden, text)
		}
	}
}
