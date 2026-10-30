package controlplane

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/developmentcycle"
)

func branchGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = path
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func branchFixture(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	branchGit(t, path, "init", "-b", "trunk")
	branchGit(t, path, "config", "user.email", "fixture@example.invalid")
	branchGit(t, path, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	branchGit(t, path, "add", ".")
	branchGit(t, path, "commit", "-m", "initial")
	branchGit(t, path, "branch", "dev/v0.1.0")
	branchGit(t, path, "branch", "experiment/test")
	branchGit(t, path, "tag", "tag-only")
	branchGit(t, path, "update-ref", "refs/remotes/origin/remote-only", "HEAD")
	branchGit(t, path, "symbolic-ref", "refs/heads/alias", "refs/heads/trunk")
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("staged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	branchGit(t, path, "add", "tracked.txt")
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("unstaged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Hash every file, including index, refs, HEAD, logs, tracked and untracked
// content. No status command is used: even an index refresh would be detected.
func branchSnapshot(t *testing.T, path string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(path, p)
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		result[rel] = fmt.Sprintf("%v:%x", info.Mode(), sha256.Sum256(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDevelopmentBranchDurableSelectionAndGitFacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repoPath := branchFixture(t)
	plane, err := Open(ctx, Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { plane.Close() }()
	p, err := plane.Projects.Create(ctx, "Development fixture")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := plane.Repositories.RegisterInProject(ctx, p.ID, repoPath, "First repository")
	if err != nil {
		t.Fatal(err)
	}
	projectContext := func() developmentcycle.Context {
		t.Helper()
		c, e := plane.DevelopmentCycles.Context(ctx, p.ID)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	initial := projectContext()
	if !initial.DevelopmentBranchSelectionRequired || initial.Repositories[0].DevelopmentCycle != nil {
		t.Fatalf("fabricated selection: %+v", initial)
	}
	branches := initial.Repositories[0].Branches
	names := []string{}
	for _, b := range branches {
		names = append(names, b.FullRef)
	}
	if !reflect.DeepEqual(names, []string{"refs/heads/dev/v0.1.0", "refs/heads/experiment/test", "refs/heads/trunk"}) {
		t.Fatalf("branches: %v", names)
	}
	before := branchSnapshot(t, repoPath)
	in := developmentcycle.SelectInput{ProjectID: p.ID, RepositoryID: repo.ID, DevelopmentRef: "refs/heads/dev/v0.1.0", RequestID: "first"}
	first, err := plane.DevelopmentCycles.Select(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	selected := projectContext().Repositories[0]
	if selected.DevelopmentCycle.ID != first.ID || selected.RefChanged == nil || *selected.RefChanged || selected.RepositoryCheckoutMatchesDevelopmentRef == nil || *selected.RepositoryCheckoutMatchesDevelopmentRef {
		t.Fatalf("selected facts: %+v", selected)
	}
	if !reflect.DeepEqual(before, branchSnapshot(t, repoPath)) {
		t.Fatal("selection/inventory changed Git or files")
	}
	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	plane, err = Open(ctx, Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := plane.DevelopmentCycles.Select(ctx, in)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
	if projectContext().Repositories[0].DevelopmentCycle.ID != first.ID {
		t.Fatal("restart lost current cycle")
	}
	conflicting := in
	conflicting.DevelopmentRef = "refs/heads/experiment/test"
	if _, err := plane.DevelopmentCycles.Select(ctx, conflicting); !errors.Is(err, developmentcycle.ErrRequestConflict) {
		t.Fatalf("conflict: %v", err)
	}
	conflicting.RequestID = "second"
	second, err := plane.DevelopmentCycles.Select(ctx, conflicting)
	if err != nil {
		t.Fatal(err)
	}
	var oldRef, oldHead, oldCreated, ended, reason string
	err = plane.DB.SQL().QueryRow(`SELECT development_ref,selected_head_sha,created_at,superseded_at,end_reason FROM development_cycles WHERE id=?`, first.ID).Scan(&oldRef, &oldHead, &oldCreated, &ended, &reason)
	if err != nil || oldRef != first.DevelopmentRef || oldHead != first.SelectedHeadSHA || ended == "" || reason != "BRANCH_RESELECTED" {
		t.Fatalf("history: %s %s %s %v", oldRef, oldHead, reason, err)
	}
	replay, err = plane.DevelopmentCycles.Select(ctx, in)
	if err != nil || !reflect.DeepEqual(first, replay) || projectContext().Repositories[0].DevelopmentCycle.ID != second.ID {
		t.Fatal("historical replay changed original result/current")
	}
	if !reflect.DeepEqual(before, branchSnapshot(t, repoPath)) {
		t.Fatal("reselection changed Git")
	}
	// An external ref move is observed without resetting the selected baseline.
	tree := branchGit(t, repoPath, "rev-parse", "HEAD^{tree}")
	moved := branchGit(t, repoPath, "commit-tree", tree, "-p", first.SelectedHeadSHA, "-m", "external commit")
	branchGit(t, repoPath, "update-ref", second.DevelopmentRef, moved)
	afterExternalMove := branchSnapshot(t, repoPath)
	drift := projectContext().Repositories[0]
	if drift.CurrentHeadSHA != moved || drift.SelectedHeadSHA != second.SelectedHeadSHA || drift.RefChanged == nil || !*drift.RefChanged || drift.DevelopmentCycle.ID != second.ID {
		t.Fatalf("drift: %+v", drift)
	}
	if !reflect.DeepEqual(afterExternalMove, branchSnapshot(t, repoPath)) {
		t.Fatal("drift read mutated Git")
	}
	branchGit(t, repoPath, "update-ref", "-d", second.DevelopmentRef)
	missing := projectContext().Repositories[0]
	if missing.DevelopmentRefStatus != "MISSING" || missing.StatusCode != "DEVELOPMENT_REF_MISSING" || missing.DevelopmentCycle.ID != second.ID || missing.CurrentHeadSHA != "" || missing.RefChanged != nil {
		t.Fatalf("missing: %+v", missing)
	}
	if _, err := plane.DevelopmentCycles.Select(ctx, conflicting); err != nil {
		t.Fatalf("deleted branch replay: %v", err)
	}
	freshMissing := conflicting
	freshMissing.RequestID = "missing"
	if _, err := plane.DevelopmentCycles.Select(ctx, freshMissing); !errors.Is(err, developmentcycle.ErrBranchNotFound) {
		t.Fatalf("missing select: %v", err)
	}
	// Detach without disturbing the dirty fixture; inventory keeps local refs.
	branchGit(t, repoPath, "update-ref", "--no-deref", "HEAD", first.SelectedHeadSHA)
	detached := projectContext().Repositories[0]
	if !detached.Detached || detached.CheckoutRef != "" || *detached.RepositoryCheckoutMatchesDevelopmentRef {
		t.Fatalf("detached: %+v", detached)
	}
	for _, ref := range []string{"trunk", "HEAD", first.SelectedHeadSHA, "refs/tags/tag-only", "refs/remotes/origin/remote-only", "refs/heads/alias", "refs/heads/trunk^{commit}"} {
		invalid := in
		invalid.RequestID = "invalid-" + ref
		invalid.DevelopmentRef = ref
		_, e := plane.DevelopmentCycles.Select(ctx, invalid)
		if !errors.Is(e, developmentcycle.ErrInvalidArgument) && !errors.Is(e, developmentcycle.ErrBranchNotFound) {
			t.Fatalf("accepted invalid ref %s: %v", ref, e)
		}
	}
	// Different repositories may have identical branch names with independent authority.
	repo2, err := plane.Repositories.RegisterInProject(ctx, p.ID, branchFixture(t), "Second repository")
	if err != nil {
		t.Fatal(err)
	}
	two := in
	two.RepositoryID = repo2.ID
	two.RequestID = "repository-two"
	if _, err := plane.DevelopmentCycles.Select(ctx, two); err != nil {
		t.Fatal(err)
	}
	both := projectContext()
	if len(both.Repositories) != 2 || both.DevelopmentBranchSelectionRequired {
		t.Fatalf("multi repository: %+v", both)
	}
	other, err := plane.Projects.Create(ctx, "Other project")
	if err != nil {
		t.Fatal(err)
	}
	mismatch := in
	mismatch.ProjectID = other.ID
	mismatch.RequestID = "mismatch"
	if _, err := plane.DevelopmentCycles.Select(ctx, mismatch); !errors.Is(err, developmentcycle.ErrRepositoryMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestDevelopmentBranchConcurrentSelectionAndDatabaseGuards(t *testing.T) {
	ctx := context.Background()
	plane, err := Open(ctx, Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	p, err := plane.Projects.Create(ctx, "Concurrent")
	if err != nil {
		t.Fatal(err)
	}
	path := branchFixture(t)
	repo, err := plane.Repositories.RegisterInProject(ctx, p.ID, path, "Repo")
	if err != nil {
		t.Fatal(err)
	}
	in := developmentcycle.SelectInput{ProjectID: p.ID, RepositoryID: repo.ID, DevelopmentRef: "refs/heads/trunk", RequestID: "same-request"}
	var wg sync.WaitGroup
	values := make(chan developmentcycle.Cycle, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c, e := plane.DevelopmentCycles.Select(ctx, in); values <- c; errs <- e }()
	}
	wg.Wait()
	close(values)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for c := range values {
		if id == "" {
			id = c.ID
		}
		if c.ID != id {
			t.Fatal("concurrent replay created duplicate cycle")
		}
	}
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := in
			v.RequestID = fmt.Sprintf("request-%d", i)
			if _, e := plane.DevelopmentCycles.Select(ctx, v); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	var count, current int
	if err := plane.DB.SQL().QueryRow(`SELECT COUNT(*),SUM(superseded_at IS NULL) FROM development_cycles`).Scan(&count, &current); err != nil || count != 13 || current != 1 {
		t.Fatalf("uniqueness: %d %d %v", count, current, err)
	}
	for _, query := range []string{
		`UPDATE development_cycles SET development_ref='refs/heads/rewritten'`,
		`UPDATE development_cycles SET selected_head_sha='rewritten'`,
		`UPDATE development_cycles SET created_at='rewritten'`,
		`UPDATE development_cycles SET superseded_at=NULL,end_reason=NULL WHERE superseded_at IS NOT NULL`,
		`DELETE FROM development_cycles`,
		`UPDATE development_branch_requests SET result_json='{}'`,
		`DELETE FROM development_branch_requests`,
		`INSERT INTO development_cycles(id,project_id,repository_id,development_ref,selected_head_sha,created_at,request_id) SELECT 'duplicate',project_id,repository_id,development_ref,selected_head_sha,created_at,'duplicate' FROM development_cycles WHERE superseded_at IS NULL`,
	} {
		if _, e := plane.DB.SQL().Exec(query); e == nil {
			t.Fatalf("database accepted %s", query)
		}
	}
}
