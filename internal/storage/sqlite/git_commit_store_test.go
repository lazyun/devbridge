package sqlite

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

func commitTestGit(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = path
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, out, e)
	}
	return strings.TrimSpace(string(out))
}
func commitTestWrite(t *testing.T, path, text string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(text), 0600); e != nil {
		t.Fatal(e)
	}
}
func attachCommitWorkspace(t *testing.T, f *reviewApprovalFixture, taskID, runID string) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	var repo, repoID string
	if e := f.db.sql.QueryRow(`SELECT r.canonical_path,r.id FROM repositories r JOIN tasks t ON t.repository_id=r.id WHERE t.id=?`, taskID).Scan(&repo, &repoID); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(repo, 0700); e != nil {
		t.Fatal(e)
	}
	commitTestGit(t, repo, "init", "-b", "develop")
	commitTestGit(t, repo, "config", "user.name", "Test")
	commitTestGit(t, repo, "config", "user.email", "test@example.invalid")
	commitTestWrite(t, filepath.Join(repo, "base.txt"), "base\n")
	commitTestGit(t, repo, "add", "-A")
	commitTestGit(t, repo, "commit", "-m", "base")
	base := commitTestGit(t, repo, "rev-parse", "HEAD")
	source := filepath.Join(t.TempDir(), "area")
	commitTestGit(t, repo, "worktree", "add", "-b", "area", source, base)
	w := workspace.Workspace{ID: "git-workspace", ProjectID: f.project, RepositoryID: repoID, FeatureAreaID: f.area, BaseCommit: strings.Repeat("a", 40), Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if e := NewWorkspaceStore(f.db).Create(ctx, w); e != nil {
		t.Fatal(e)
	}
	var e error
	wt := worktree.Worktree{ID: "commit-worktree", RepositoryID: repoID, RunID: runID, Path: source, Branch: "area", BaseCommit: w.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if e = NewWorktreeStore(f.db).Create(ctx, wt); e != nil {
		t.Fatal(e)
	}
	if _, e = NewWorkspaceStore(f.db).Update(ctx, w.ID, func(v *workspace.Workspace) error { v.WorktreeID = wt.ID; return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = NewDevelopmentCycleStore(f.db).Select(ctx, developmentcycle.Cycle{ID: "commit-cycle", ProjectID: f.project, RepositoryID: repoID, DevelopmentRef: "refs/heads/develop", SelectedHeadSHA: base, CreatedAt: testNow, RequestID: "select-dev"}); e != nil {
		t.Fatal(e)
	}
	commitTestWrite(t, filepath.Join(source, "task.txt"), "accepted\n")
	snapshot, e := git.NewClient(git.Config{}).Snapshot(ctx, source)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.sql.Exec(`UPDATE runs SET end_snapshot=?,workspace_id=? WHERE id=?`, snapshot, w.ID, runID); e != nil {
		t.Fatal(e)
	}
	return repo, source, base
}
func TestTaskAutoCommitAndFeatureAreaDurableClosedLoop(t *testing.T) {
	for _, mode := range []string{"accept", "snapshot-drift", "commit-response-loss", "submit-response-loss", "request-changes", "reverification", "reject"} {
		t.Run(mode, func(t *testing.T) {
			f, owner, e := acceptedExecutionFixture(t, "-git")
			e = terminalExecutionWithReport(t, f, e)
			repo, source, base := attachCommitWorkspace(t, f, owner.ID, e.VerificationRunID)
			ctx := context.Background()
			g := git.NewClient(git.Config{})
			input := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), ExecutionID: e.ID, Option: wp.AcceptanceAccept, RequestID: "git-accept"}
			if mode == "snapshot-drift" {
				commitTestWrite(t, filepath.Join(source, "drift.txt"), "later\n")
			}
			if mode == "commit-response-loss" {
				if _, err := f.db.sql.Exec(`CREATE TRIGGER fail_commit_checkpoint BEFORE UPDATE OF commit_sha ON tasks BEGIN SELECT RAISE(ABORT,'simulated response loss'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "request-changes" {
				input.Option = wp.AcceptanceRequestChanges
				input.NextWorkSpec = &e.WorkSpec
				input.NextVerificationSpec = &e.VerificationSpec
			}
			if mode == "reverification" {
				input.Option = wp.AcceptanceRequestReverification
			}
			if mode == "reject" {
				input.Option = wp.AcceptanceReject
			}
			out, err := f.store.ResolveTaskAcceptance(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if input.Option != wp.AcceptanceAccept {
				if out.Task.CommitSHA != "" {
					t.Fatal("committed without acceptance")
				}
				head, _ := g.ResolveHEAD(ctx, source)
				if head != base {
					t.Fatal("Git changed without acceptance")
				}
				return
			}
			if mode == "snapshot-drift" {
				if out.CommitError != git.ErrSnapshotDrift.Error() || out.Task.CommitSHA != "" {
					t.Fatalf("%+v", out)
				}
				persisted, err := NewTaskStore(f.db).Get(ctx, owner.ID)
				if err != nil || persisted.Status != task.StatusActive || persisted.Phase != task.PhaseRecoveryRequired {
					t.Fatalf("drift lifecycle=%+v err=%v", persisted, err)
				}
				guidance, err := f.store.Read(ctx, f.project, f.area)
				if err != nil || guidance.CurrentState != "TASK_RECOVERY_REQUIRED" || guidance.NextAction.Code != "RESOLVE_TASK_RECOVERY" {
					t.Fatalf("%+v %v", guidance, err)
				}
				if _, err = f.store.RetryTaskCommit(ctx, f.project, owner.ID); err == nil || !strings.Contains(err.Error(), "TASK_NOT_ACCEPTED") {
					t.Fatalf("drift retry bypassed the recovery fence: %v", err)
				}
				head, _ := g.ResolveHEAD(ctx, source)
				if head != base {
					t.Fatal("drift committed")
				}
				return
			}
			if mode != "commit-response-loss" && out.Task.Status != task.StatusSucceeded {
				t.Fatal("accepted delivery did not terminalize")
			}
			if mode == "commit-response-loss" {
				if out.CommitError == "" || out.Task.CommitSHA != "" {
					t.Fatal("failure injection did not hold DB checkpoint")
				}
				if out.Task.Status != task.StatusActive || out.Task.Phase != task.PhaseDelivering {
					t.Fatalf("response loss lost the durable admission: %+v", out.Task)
				}
				head, _ := g.ResolveHEAD(ctx, source)
				if head == base {
					t.Fatal("Git did not commit")
				}
				if _, err = f.db.sql.Exec(`DROP TRIGGER fail_commit_checkpoint`); err != nil {
					t.Fatal(err)
				}
				out.Task, err = f.store.RetryTaskCommit(ctx, f.project, owner.ID)
				if err != nil {
					t.Fatal(err)
				}
				if out.Task.CommitSHA != head {
					t.Fatal("recovery created another commit")
				}
			}
			if out.Task.CommitSHA == "" || out.Task.CommittedAt == nil {
				t.Fatalf("missing checkpoint: %+v error=%s", out.Task, out.CommitError)
			}
			if err = g.RequireClean(ctx, source); err != nil {
				t.Fatal(err)
			}
			cap, err := f.store.CommitCapability(ctx, f.project, f.area)
			if err != nil || !cap.CanCommit || cap.CommitStatus != "UNCOMMITTED" {
				t.Fatalf("cap=%+v %v", cap, err)
			}
			if mode == "submit-response-loss" {
				if _, err = f.db.sql.Exec(`CREATE TRIGGER fail_merge_checkpoint BEFORE UPDATE OF merged_commit ON feature_areas BEGIN SELECT RAISE(ABORT,'simulated response loss'); END`); err != nil {
					t.Fatal(err)
				}
			}
			area, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "submit-1")
			if mode == "submit-response-loss" {
				if _, retryErr := f.store.CommitFeatureArea(ctx, f.project, f.area, "different-request"); !errors.Is(retryErr, git.ErrRecoveryRequired) {
					t.Fatalf("different receipt did not fail closed: %v", retryErr)
				}
				if err == nil {
					t.Fatal("missing injected failure")
				}
				head, _ := g.ResolveHEAD(ctx, repo)
				if head != out.Task.CommitSHA {
					t.Fatal("missing Git merge")
				}
				if _, err = f.db.sql.Exec(`DROP TRIGGER fail_merge_checkpoint`); err != nil {
					t.Fatal(err)
				}
				area, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "submit-1")
				if err != nil {
					t.Fatal(err)
				}
				if area.MergedCommit != head {
					t.Fatal("duplicate merge")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			merged, err := NewTaskStore(f.db).Get(ctx, owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			if merged.MergedCommit != area.MergedCommit || merged.MergedAt == nil {
				t.Fatal("task merge checkpoint missing")
			}
			cap, err = f.store.CommitCapability(ctx, f.project, f.area)
			if err != nil || cap.CommitStatus != "COMMITTED" {
				t.Fatalf("%+v %v", cap, err)
			}
			for _, p := range []string{repo, source} {
				head, _ := g.ResolveHEAD(ctx, p)
				if head != area.MergedCommit {
					t.Fatal("HEAD mismatch")
				}
				if err = g.RequireClean(ctx, p); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestNewFeatureAreaCommitBaselineAndReviewProjection(t *testing.T) {
	f, owner, e := acceptedExecutionFixture(t, "-baseline")
	e = terminalExecutionWithReport(t, f, e)
	repo, _, _ := attachCommitWorkspace(t, f, owner.ID, e.VerificationRunID)
	commitTestWrite(t, filepath.Join(repo, "advanced-dev.txt"), "new development HEAD\n")
	commitTestGit(t, repo, "add", "-A")
	commitTestGit(t, repo, "commit", "-m", "advance dev")
	base := commitTestGit(t, repo, "rev-parse", "HEAD")
	area := featurearea.FeatureArea{ID: "new-area", ProjectID: f.project, Title: "New area", ProblemStatement: "Intent", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(f.db).Create(context.Background(), area); err != nil {
		t.Fatal(err)
	}
	got, err := NewFeatureAreaStore(f.db).Get(context.Background(), area.ID)
	if err != nil || got.MergedCommit != base || got.MergedAt == nil || !got.MergedAt.Equal(testNow) {
		t.Fatalf("%+v %v", got, err)
	}
	cap, err := f.store.CommitCapability(context.Background(), f.project, area.ID)
	if err != nil || cap.CommitStatus != "COMMITTED" {
		t.Fatalf("%+v %v", cap, err)
	}
}
func TestRepositoryCommandLockSerializesOnlySameKey(t *testing.T) {
	unlock := lockGitCommand("test-repo/ref")
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); release := lockGitCommand("test-repo/ref"); defer release(); close(entered) }()
	independent := lockGitCommand("other-repo/ref")
	independent()
	select {
	case <-entered:
		t.Fatal("concurrent repository submit entered")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lock not released")
	}
}

func TestTaskCommitHistorySurvivesNextTaskAndSecondSubmit(t *testing.T) {
	f, first, e := acceptedExecutionFixture(t, "-history")
	e = terminalExecutionWithReport(t, f, e)
	_, source, _ := attachCommitWorkspace(t, f, first.ID, e.VerificationRunID)
	ctx := context.Background()
	accept := func(executionID string) task.Task {
		t.Helper()
		out, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(executionID), ExecutionID: executionID, Option: wp.AcceptanceAccept, RequestID: "accept-" + executionID})
		if err != nil || out.CommitError != "" {
			t.Fatalf("accept %v %s", err, out.CommitError)
		}
		return out.Task
	}
	first = accept(e.ID)
	firstArea, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "first-submit")
	if err != nil {
		t.Fatal(err)
	}
	f.candidate.EngineeringGoal = "Second independent Task"
	f.newIntent(t, "second-independent")
	admission := f.start(t)
	f.complete(t, admission, approvedResult(t))
	input := f.input(admission, "APPROVE")
	input.RequestID = "approve-second"
	next, err := f.store.Resolve(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if next.Task.CommitSHA != "" {
		t.Fatal("new Task inherited checkpoint")
	}
	previous, err := NewTaskStore(f.db).Get(ctx, first.ID)
	if err != nil || previous.CommitSHA != first.CommitSHA || previous.MergedCommit != firstArea.MergedCommit {
		t.Fatalf("previous changed: %+v %v", previous, err)
	}
	cap, err := f.store.CommitCapability(ctx, f.project, f.area)
	if err != nil || cap.CommitStatus != "UNCOMMITTED" || cap.CanCommit {
		t.Fatalf("new Task capability %+v %v", cap, err)
	}
	e, err = f.store.AuthorizeTaskExecution(ctx, f.session, next.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	e = terminalExecutionWithReport(t, f, e)
	commitTestWrite(t, filepath.Join(source, "task-two.txt"), "second accepted Task\n")
	snapshot, err := git.NewClient(git.Config{}).Snapshot(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE runs SET end_snapshot=?,workspace_id=? WHERE id=?`, snapshot, "git-workspace", e.VerificationRunID); err != nil {
		t.Fatal(err)
	}
	second := accept(e.ID)
	if second.CommitSHA == first.CommitSHA {
		t.Fatal("second task missing separate commit")
	}
	area, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "second-submit")
	if err != nil {
		t.Fatal(err)
	}
	previous, _ = NewTaskStore(f.db).Get(ctx, first.ID)
	second, _ = NewTaskStore(f.db).Get(ctx, second.ID)
	if previous.MergedCommit != firstArea.MergedCommit || second.MergedCommit != area.MergedCommit || area.MergedCommit == firstArea.MergedCommit {
		t.Fatal("merge history overwritten")
	}
	replay, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "first-submit")
	if err != nil || replay.MergedCommit != firstArea.MergedCommit {
		t.Fatalf("historical request replay %+v %v", replay, err)
	}
}
func TestReviewOnlyDoesNotChangeCommitProjection(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	before, err := f.store.CommitCapability(ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	a := f.start(t)
	during, err := f.store.CommitCapability(ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	f.complete(t, a, approvedResult(t))
	after, err := f.store.CommitCapability(ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	if before.CommitStatus != "UNCOMMITTED" || during.CommitStatus != before.CommitStatus || after.CommitStatus != before.CommitStatus {
		t.Fatalf("%+v %+v %+v", before, during, after)
	}
	if during.CanCommit || after.CanCommit {
		t.Fatal("Review allows commit")
	}
}

func TestTwoFeatureAreasSerializeSubmissionsAndMergeDivergence(t *testing.T) {
	ctx := context.Background()
	f, first, e := acceptedExecutionFixture(t, "-parallel")
	e = terminalExecutionWithReport(t, f, e)
	repo, sourceA, base := attachCommitWorkspace(t, f, first.ID, e.VerificationRunID)
	areaB := featurearea.FeatureArea{ID: "area-b", ProjectID: f.project, CreatedByAISessionID: f.session, Title: "Parallel area", ProblemStatement: "Independent work", Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(f.db).Create(ctx, areaB); err != nil {
		t.Fatal(err)
	}
	b := &reviewApprovalFixture{db: f.db, store: f.store, project: f.project, area: areaB.ID, session: f.session, candidate: f.candidate, reviews: f.reviews, now: f.now.Add(time.Minute)}
	b.candidate.FeatureAreaID = areaB.ID
	b.candidate.EngineeringGoal = "Parallel Task"
	b.newIntent(t, "parallel-independent")
	admission := b.start(t)
	b.complete(t, admission, approvedResult(t))
	input := b.input(admission, "APPROVE")
	input.RequestID = "parallel-approve"
	next, err := b.store.Resolve(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	eB, err := f.store.AuthorizeTaskExecution(ctx, f.session, next.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	eB = terminalExecutionWithReport(t, b, eB)
	sourceB := filepath.Join(t.TempDir(), "area-b")
	commitTestGit(t, repo, "worktree", "add", "-b", "area-b", sourceB, base)
	w := workspace.Workspace{ID: "workspace-b", ProjectID: f.project, RepositoryID: first.RepositoryID, FeatureAreaID: areaB.ID, BaseCommit: strings.Repeat("a", 40), Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if err = NewWorkspaceStore(f.db).Create(ctx, w); err != nil {
		t.Fatal(err)
	}
	wt := worktree.Worktree{ID: "worktree-b", RepositoryID: first.RepositoryID, RunID: eB.VerificationRunID, Path: sourceB, Branch: "area-b", BaseCommit: w.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if err = NewWorktreeStore(f.db).Create(ctx, wt); err != nil {
		t.Fatal(err)
	}
	if _, err = NewWorkspaceStore(f.db).Update(ctx, w.ID, func(v *workspace.Workspace) error { v.WorktreeID = wt.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	commitTestWrite(t, filepath.Join(sourceB, "parallel.txt"), "parallel\n")
	g := git.NewClient(git.Config{})
	snapshot, err := g.Snapshot(ctx, sourceB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE runs SET end_snapshot=?,workspace_id=? WHERE id=?`, snapshot, w.ID, eB.VerificationRunID); err != nil {
		t.Fatal(err)
	}
	for _, executionID := range []string{e.ID, eB.ID} {
		out, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(executionID), ExecutionID: executionID, Option: wp.AcceptanceAccept, RequestID: "accept-" + executionID})
		if err != nil || out.CommitError != "" {
			t.Fatalf("%v %s", err, out.CommitError)
		}
	}
	type result struct {
		area featurearea.FeatureArea
		err  error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, area := range []string{f.area, areaB.ID} {
		go func(id string) {
			<-start
			a, e := f.store.CommitFeatureArea(ctx, f.project, id, "submit-"+id)
			results <- result{a, e}
		}(area)
	}
	close(start)
	one, two := <-results, <-results
	if one.err != nil || two.err != nil {
		t.Fatalf("%v / %v", one.err, two.err)
	}
	head, err := g.ResolveHEAD(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	parents, err := g.CommitParents(ctx, repo, head)
	if err != nil || len(parents) != 2 {
		t.Fatalf("parallel merge parents %v %v", parents, err)
	}
	for _, path := range []string{sourceA, sourceB} {
		h, _ := g.ResolveHEAD(ctx, path)
		if h != one.area.MergedCommit && h != two.area.MergedCommit {
			t.Fatal("workspace not synced at its submission")
		}
		if err = g.RequireClean(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
}
