package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

// The real Worktree Manager owns its reserved-path guard. Only Git IO is held
// at a barrier so two starts deterministically overlap the creation window.
type upperPreparationGit struct {
	entered, release chan struct{}
	once             sync.Once
	mu               sync.Mutex
	adds             int
	foreignBranch    bool
}

func (g *upperPreparationGit) ResolveCommit(ctx context.Context, _, base string) (string, error) {
	var err error
	g.once.Do(func() {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return base, err
}
func (g *upperPreparationGit) BranchExists(context.Context, string, string) (bool, error) {
	return g.foreignBranch, nil
}
func (g *upperPreparationGit) WorktreeAdd(_ context.Context, _, path, _, _ string) error {
	g.mu.Lock()
	g.adds++
	g.mu.Unlock()
	return os.MkdirAll(path, 0o700)
}
func (*upperPreparationGit) WorktreeRemove(context.Context, string, string) error { return nil }
func (*upperPreparationGit) DeleteBranch(context.Context, string, string) error   { return nil }
func (*upperPreparationGit) ValidateManagedWorktree(_ context.Context, _, path, _ string) error {
	_, err := os.Stat(path)
	return err
}

type upperPreparationRuns struct {
	*run.Service
	mu       sync.Mutex
	bindings []run.RepositoryBinding
	puts     int
}

func (r *upperPreparationRuns) ListRepositoryBindings(context.Context, string) ([]run.RepositoryBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]run.RepositoryBinding(nil), r.bindings...), nil
}
func (r *upperPreparationRuns) PutRepositoryBinding(_ context.Context, binding run.RepositoryBinding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.puts++
	r.bindings[0] = binding
	return nil
}

type upperPreparationAccess struct {
	mu                     sync.Mutex
	target                 review.UpperTarget
	state                  string
	prepareCalls, failures int
	current                bool
}

func (u *upperPreparationAccess) UpperReviewAccessForSession(context.Context, string, string) (review.UpperAccess, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return review.UpperAccess{Target: u.target, State: u.state, ProtocolVersion: 2}, nil
}
func (u *upperPreparationAccess) CheckPreparationTarget(_ context.Context, target review.UpperTarget) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.current || target != u.target {
		return review.ErrTargetStale
	}
	return nil
}
func (u *upperPreparationAccess) Prepare(ctx context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	if err := u.CheckPreparationTarget(ctx, target); err != nil {
		return review.UpperAccess{}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prepareCalls++
	u.state = "ACTIVE"
	return review.UpperAccess{Target: target, State: u.state, ProtocolVersion: 2}, nil
}
func (u *upperPreparationAccess) RecordTargetPreparationFailure(context.Context, review.UpperTarget, string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failures++
	return nil
}

type upperPreparationFixture struct {
	c        *Coordinator
	runs     *upperPreparationRuns
	upper    *upperPreparationAccess
	git      *upperPreparationGit
	value    review.Review
	reserved run.Run
	release  func()
}

func newUpperPreparationFixture(t *testing.T) *upperPreparationFixture {
	t.Helper()
	base := strings.Repeat("a", 40)
	reserved := run.Run{ID: "upper-run", ReviewID: "upper-review", TaskID: "task-1", ProjectID: "project-1", FeatureAreaID: "area-1", OperationKind: run.OperationReview, OperationID: "upper-review", Purpose: run.PurposeScopeReview, Status: run.StatusCreated, RuntimeEpoch: "epoch-1"}
	runs := &upperPreparationRuns{Service: run.NewService(run.NewMemoryStore(reserved), nil, nil)}
	spaces := workspace.NewService(workspace.NewMemoryStore(), nil, nil)
	planned, err := spaces.PlanMembers(context.Background(), reserved.ProjectID, reserved.FeatureAreaID, []workset.Target{{RepositoryID: "repo-1", OriginCommit: base, Read: true, Write: workset.WriteScope{Kind: "NONE"}}})
	if err != nil {
		t.Fatal(err)
	}
	target := planned.Targets[0]
	runs.bindings = []run.RepositoryBinding{{RunID: reserved.ID, ReviewID: reserved.ReviewID, ProjectID: reserved.ProjectID, FeatureAreaID: reserved.FeatureAreaID, WorkspaceID: planned.WorkspaceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, OriginCommit: target.OriginCommit, Preparation: "PENDING"}}
	git := &upperPreparationGit{entered: make(chan struct{}), release: make(chan struct{})}
	trees, err := worktree.NewManager(filepath.Join(t.TempDir(), "worktrees"), git)
	if err != nil {
		t.Fatal(err)
	}
	upper := &upperPreparationAccess{target: review.UpperTarget{AISessionID: "session-1", FeatureAreaID: reserved.FeatureAreaID, TaskID: reserved.TaskID, ReviewID: reserved.ReviewID, RunID: reserved.ID, RuntimeEpoch: reserved.RuntimeEpoch, GrantGeneration: 1}, state: "PREPARING", current: true}
	value := review.Review{ID: reserved.ReviewID, ExecutorKind: review.ExecutorUpperLLM, TaskID: reserved.TaskID, ProjectID: reserved.ProjectID, FeatureAreaID: reserved.FeatureAreaID, CreatedByAISessionID: upper.target.AISessionID, WorkspaceID: planned.WorkspaceID, RepositoryTargets: planned.Targets}
	coordinator := &Coordinator{Repositories: multiRepositories{values: map[string]repository.Repository{"repo-1": {ID: "repo-1", ProjectID: reserved.ProjectID, Path: t.TempDir()}}}, Runs: runs, Workspaces: spaces, Worktrees: trees, UpperReviews: upper}
	var once sync.Once
	release := func() { once.Do(func() { close(git.release) }) }
	t.Cleanup(release)
	return &upperPreparationFixture{c: coordinator, runs: runs, upper: upper, git: git, value: value, reserved: reserved, release: release}
}

type upperPreparationResult struct {
	run run.Run
	err error
}

func (f *upperPreparationFixture) start(ctx context.Context, reserved run.Run) <-chan upperPreparationResult {
	done := make(chan upperPreparationResult, 1)
	go func() {
		value, err := f.c.StartUpperReview(ctx, f.value, reserved)
		done <- upperPreparationResult{value, err}
	}()
	return done
}

func awaitUpperPreparation(t *testing.T, done <-chan upperPreparationResult) upperPreparationResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("Upper preparation did not finish")
		return upperPreparationResult{}
	}
}

func TestUpperReviewConcurrentPreparationSharesExactResult(t *testing.T) {
	f := newUpperPreparationFixture(t)
	first := f.start(context.Background(), f.reserved)
	select {
	case <-f.git.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first worktree creation never reached barrier")
	}
	preparing, err := f.runs.Get(context.Background(), f.reserved.ID)
	if err != nil || preparing.Status != run.StatusPreparing {
		t.Fatalf("first reservation=%+v %v", preparing, err)
	}
	for _, distinct := range []string{"review", "run"} {
		value, reserved := f.value, f.reserved
		if distinct == "review" {
			value.ID, reserved.ReviewID, reserved.OperationID = "another-review", "another-review", "another-review"
		} else {
			reserved.ID = "another-run"
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := f.c.StartUpperReview(ctx, value, reserved)
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			f.release()
			_ = awaitUpperPreparation(t, first)
			t.Fatalf("different %s waited on unrelated preparation or acquired authority: %v", distinct, err)
		}
	}
	second := f.start(context.Background(), preparing)
	var early *upperPreparationResult
	select {
	case result := <-second:
		early = &result
	case <-time.After(100 * time.Millisecond):
	}
	f.release()
	a := awaitUpperPreparation(t, first)
	b := upperPreparationResult{}
	if early != nil {
		b = *early
	} else {
		b = awaitUpperPreparation(t, second)
	}
	if a.err != nil || b.err != nil || a.run.Status != run.StatusRunning || !reflect.DeepEqual(a.run, b.run) {
		t.Fatalf("same admission did not share preparation: first=%s/%v second=%s/%v", a.run.Status, a.err, b.run.Status, b.err)
	}
	if f.git.adds != 1 || f.runs.puts != 1 || f.upper.prepareCalls != 1 || f.upper.failures != 0 {
		t.Fatalf("duplicate effects: git=%d bindings=%d prepare=%d failures=%d", f.git.adds, f.runs.puts, f.upper.prepareCalls, f.upper.failures)
	}
	// A queued caller can still carry the original CREATED reservation.
	replay, err := f.c.StartUpperReview(context.Background(), f.value, f.reserved)
	if err != nil || !reflect.DeepEqual(replay, a.run) || f.runs.puts != 1 || f.upper.prepareCalls != 1 {
		t.Fatalf("stale CREATED snapshot did not read completed preparation: %+v %v", replay, err)
	}
	// Grant projection alone does not prove a current claim after cancellation.
	f.upper.current = false
	if _, err := f.c.StartUpperReview(context.Background(), f.value, f.reserved); !errors.Is(err, review.ErrTargetStale) {
		t.Fatalf("inactive owner reused ACTIVE grant: %v", err)
	}
}

func TestUpperReviewCanceledPreparationWaiterDoesNotAffectLeader(t *testing.T) {
	f := newUpperPreparationFixture(t)
	first := f.start(context.Background(), f.reserved)
	select {
	case <-f.git.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first worktree creation never reached barrier")
	}
	preparing, err := f.runs.Get(context.Background(), f.reserved.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	second := f.start(ctx, preparing)
	select {
	case got := <-second:
		cancel()
		f.release()
		_ = awaitUpperPreparation(t, first)
		t.Fatalf("waiter returned while leader was still preparing: %v", got.err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	got := awaitUpperPreparation(t, second)
	if !errors.Is(got.err, context.Canceled) || f.upper.failures != 0 {
		f.release()
		_ = awaitUpperPreparation(t, first)
		t.Fatalf("canceled waiter changed leader: %v failures=%d", got.err, f.upper.failures)
	}
	f.release()
	leader := awaitUpperPreparation(t, first)
	if leader.err != nil || leader.run.Status != run.StatusRunning || f.git.adds != 1 || f.upper.prepareCalls != 1 || f.upper.failures != 0 {
		t.Fatalf("leader did not complete after canceled waiter: %+v", leader)
	}
}

func TestUpperReviewPreparationKeepsForeignCollisionAndEpochFences(t *testing.T) {
	t.Run("foreign-branch", func(t *testing.T) {
		f := newUpperPreparationFixture(t)
		f.git.foreignBranch = true
		f.release()
		if _, err := f.c.StartUpperReview(context.Background(), f.value, f.reserved); !errors.Is(err, worktree.ErrCollision) {
			t.Fatalf("foreign branch collision accepted: %v", err)
		}
		if f.git.adds != 0 || f.upper.prepareCalls != 0 {
			t.Fatal("foreign collision acquired preparation authority")
		}
	})
	t.Run("stale-epoch", func(t *testing.T) {
		f := newUpperPreparationFixture(t)
		stale := f.reserved
		stale.RuntimeEpoch = "prior-epoch"
		if _, err := f.c.StartUpperReview(context.Background(), f.value, stale); !errors.Is(err, review.ErrTargetStale) {
			t.Fatalf("stale epoch adopted live authority: %v", err)
		}
		if f.git.adds != 0 || f.upper.prepareCalls != 0 || f.upper.failures != 0 {
			t.Fatal("stale epoch changed current preparation")
		}
	})
	t.Run("stale-generation", func(t *testing.T) {
		f := newUpperPreparationFixture(t)
		f.upper.target.GrantGeneration = 2
		if _, err := f.c.StartUpperReview(context.Background(), f.value, f.reserved); !errors.Is(err, review.ErrTargetStale) {
			t.Fatalf("old admission adopted recovered grant: %v", err)
		}
		if f.git.adds != 0 || f.upper.prepareCalls != 0 || f.upper.failures != 0 {
			t.Fatal("old admission changed recovered preparation")
		}
	})
}
