package executionproposal_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	"github.com/lazyun/devbridge/internal/workflowtest"
)

type fixedBase struct{ commit string }

func (f fixedBase) ResolveBase(context.Context, string, string) (string, error) { return f.commit, nil }

type reservingStarter struct{}

func (reservingStarter) StartAuthorizedRun(ctx context.Context, _ string, _ string, _ []string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	return reserve(ctx)
}

type failingHandoffStarter struct{ calls atomic.Int64 }

func (s *failingHandoffStarter) StartAuthorizedRun(ctx context.Context, _ string, _ string, _ []string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	s.calls.Add(1)
	reserved, err := reserve(ctx)
	if err != nil {
		return reserved, err
	}
	return reserved, errors.New("injected external handoff failure")
}

type guardedHandoffStarter struct {
	mu       sync.Mutex
	active   bool
	accepted int
	entered  chan struct{}
	release  chan struct{}
}

func (s *guardedHandoffStarter) StartAuthorizedRun(ctx context.Context, taskID string, _ string, _ []string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	reserved, err := reserve(ctx)
	if err != nil {
		return reserved, err
	}
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return run.Run{}, &execution.TaskAlreadyHasActiveRunError{TaskID: taskID, RunID: reserved.ID}
	}
	s.active = true
	s.accepted++
	s.mu.Unlock()
	close(s.entered)
	<-s.release
	return reserved, nil
}

type fixture struct {
	ctx        context.Context
	root       string
	db         *sqlite.DB
	now        time.Time
	session    aisession.Session
	tasks      *task.Service
	plans      *taskplan.Service
	source     taskplan.Plan
	executions *executionproposal.Service
	runSeq     atomic.Int64
	clock      atomic.Int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 9, 10, 11, 0, time.UTC)
	projectValue := project.Project{ID: "project-a", Name: "A", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewProjectStore(db).Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	projectB := project.Project{ID: "project-b", Name: "B", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewProjectStore(db).Create(ctx, projectB); err != nil {
		t.Fatal(err)
	}
	moduleValue := module.Module{ID: "module-a", ProjectID: projectValue.ID, Key: "core", Name: "Core", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModuleStore(db).Create(ctx, moduleValue); err != nil {
		t.Fatal(err)
	}
	repositoryValue := repository.Repository{ID: "repo-a", ProjectID: projectValue.ID, Name: "A", Path: filepath.Join(root, "repo"), CreatedAt: now, UpdatedAt: now}
	if _, _, err := sqlite.NewRepositoryStore(db).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	session := aisession.Session{ID: "session-a", ProjectID: projectValue.ID, CreatedAt: now, UpdatedAt: now}
	sessions := sqlite.NewAISessionStore(db)
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO ai_sessions(id,project_id,created_at,updated_at,caller_scope) VALUES(?,NULL,?,?,?)`, session.ID, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), "execution-proposal-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE ai_sessions SET project_id=? WHERE id=?`, session.ProjectID, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO ai_sessions(id,project_id,created_at,updated_at,caller_scope) VALUES(?,NULL,?,?,?)`, "session-b", now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), "execution-proposal-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE ai_sessions SET project_id=? WHERE id=?`, projectB.ID, "session-b"); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: "feature-a", ProjectID: projectValue.ID, CreatedByAISessionID: session.ID, Title: "Execution intent", ProblemStatement: "Prepare exact plan", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tasks := task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, nil, func() time.Time { return now })
	planSeq := 0
	plans, err := taskplan.NewService(sqlite.NewTaskPlanStore(db), sessions, tasks, sqlite.NewRepositoryStore(db), func() (string, error) {
		planSeq++
		return "task-plan-" + string(rune('0'+planSeq)), nil
	}, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{ctx: ctx, root: root, db: db, now: now, session: session, tasks: tasks, plans: plans}
	f.executions = f.newExecutionService(t)
	return f
}

func (f *fixture) newExecutionService(t *testing.T) *executionproposal.Service {
	return f.newExecutionServiceWithStarter(t, reservingStarter{})
}

func (f *fixture) newExecutionServiceWithStarter(t *testing.T, starter executionproposal.Starter) *executionproposal.Service {
	t.Helper()
	var proposalSeq atomic.Int64
	service, err := executionproposal.NewService(sqlite.NewExecutionProposalStore(f.db), sqlite.NewAISessionStore(f.db), f.plans, f.tasks, starter, func() (string, error) {
		return "execution-proposal-" + string(rune('0'+proposalSeq.Add(1))), nil
	}, func() (string, error) {
		return "run-" + string(rune('0'+f.runSeq.Add(1))), nil
	}, func() time.Time { return f.now.Add(time.Duration(f.clock.Add(1)) * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func (f *fixture) reserveTask(t *testing.T) (executionproposal.Proposal, run.Run) {
	t.Helper()
	value := task.Task{ID: "task-a", Title: "Build it", Goal: "Build it", ProblemStatement: "Build it", ProjectID: f.session.ProjectID, FeatureAreaID: "feature-a", ModuleID: "module-a", RepositoryID: "repo-a", CodexConfigID: codexconfig.DefaultConfigID, Model: "gpt-5.6-terra", ReasoningEffort: "high", BaseCommit: strings.Repeat("a", 40), Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: f.now, UpdatedAt: f.now}
	if err := sqlite.NewTaskStore(f.db).Create(f.ctx, value); err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Build it", Constraints: []string{"stay scoped"}, CodexInstruction: "Build it", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "fixture", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -d .", Required: true}}, AcceptanceCriteria: []string{"bounded result"}, CodexInstruction: "Verify read-only"}
	_, candidate, reserved := workflowtest.ApproveAndReserve(t, f.db, value, f.session.ID, work, verification)
	source, err := sqlite.NewTaskPlanStore(f.db).Get(f.ctx, candidate.TaskPlanID)
	if err != nil {
		t.Fatal(err)
	}
	f.source = source
	return candidate, reserved
}

func (s reservingStarter) StartAuthorizedPlan(ctx context.Context, id string, _ task.WorkSpec, _ task.VerificationSpec, reserve, verification func(context.Context) (run.Run, error), complete func(context.Context, executionproposal.Outcome, string, string) error) (run.Run, error) {
	return s.StartAuthorizedRun(ctx, id, "", nil, reserve)
}
func (s *failingHandoffStarter) StartAuthorizedPlan(ctx context.Context, id string, _ task.WorkSpec, _ task.VerificationSpec, reserve, verification func(context.Context) (run.Run, error), complete func(context.Context, executionproposal.Outcome, string, string) error) (run.Run, error) {
	return s.StartAuthorizedRun(ctx, id, "", nil, reserve)
}
func (s *guardedHandoffStarter) StartAuthorizedPlan(ctx context.Context, id string, _ task.WorkSpec, _ task.VerificationSpec, reserve, verification func(context.Context) (run.Run, error), complete func(context.Context, executionproposal.Outcome, string, string) error) (run.Run, error) {
	return s.StartAuthorizedRun(ctx, id, "", nil, reserve)
}

func TestTaskOwnedExecutionHandoffFailureAndResponseLossReuseReservation(t *testing.T) {
	f := newFixture(t)
	defer f.db.Close()
	candidate, reserved := f.reserveTask(t)
	starter := &failingHandoffStarter{}
	service := f.newExecutionServiceWithStarter(t, starter)
	for i := 0; i < 2; i++ {
		result, err := service.AuthorizeAndStart(f.ctx, f.session.ID, candidate.ID)
		if err == nil || result.Proposal.ID != candidate.ID || result.Run.ID != reserved.ID {
			t.Fatalf("handoff replay: %+v %v", result, err)
		}
	}
	var executions, runs int
	f.db.SQL().QueryRow(`SELECT count(*) FROM execution_proposals`).Scan(&executions)
	f.db.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, candidate.ID).Scan(&runs)
	if executions != 1 || runs != 1 || starter.calls.Load() != 2 {
		t.Fatalf("duplicate business attempt: %d %d", executions, runs)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(f.ctx, f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.db = db
	defer db.Close()
	sessions := sqlite.NewAISessionStore(db)
	f.tasks = task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, nil, nil)
	f.plans, err = taskplan.NewService(sqlite.NewTaskPlanStore(db), sessions, f.tasks, sqlite.NewRepositoryStore(db), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.newExecutionService(t).AuthorizeAndStart(f.ctx, f.session.ID, candidate.ID)
	if err == nil || result.Proposal.ID != candidate.ID || result.Run.ID != "" {
		t.Fatalf("cold implicit restart must remain closed: %+v %v", result, err)
	}
}

func TestTaskOwnedConcurrentExecuteReturnsOneAcceptedRuntimeHandoff(t *testing.T) {
	f := newFixture(t)
	defer f.db.Close()
	candidate, reserved := f.reserveTask(t)
	starter := &guardedHandoffStarter{entered: make(chan struct{}), release: make(chan struct{})}
	service := f.newExecutionServiceWithStarter(t, starter)
	first := make(chan error, 1)
	go func() { _, err := service.AuthorizeAndStart(f.ctx, f.session.ID, candidate.ID); first <- err }()
	select {
	case <-starter.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handoff did not enter")
	}
	result, err := service.AuthorizeAndStart(f.ctx, f.session.ID, candidate.ID)
	close(starter.release)
	if err == nil || result.Run.ID != reserved.ID {
		t.Fatalf("concurrent duplicate handoff must report the existing reservation: %+v %v", result, err)
	}
	if err = <-first; err == nil {
		t.Fatal("in-process starter must not be treated as an authoritative runtime acceptance")
	}
	if starter.accepted != 1 {
		t.Fatal("runtime handoff duplicated")
	}
}

func TestTaskOwnedExecutionOwnershipAndFrozenHistory(t *testing.T) {
	f := newFixture(t)
	defer f.db.Close()
	candidate, reserved := f.reserveTask(t)
	if _, err := f.executions.Get(f.ctx, "session-b", candidate.ID); !errors.Is(err, executionproposal.ErrNotFound) {
		t.Fatalf("foreign session read: %v", err)
	}
	if _, err := f.executions.ResumeReserved(f.ctx, f.session.ID, candidate, run.Run{ID: reserved.ID, TaskID: "foreign", ExecutionProposalID: candidate.ID}); !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("foreign Run handoff: %v", err)
	}
	if _, err := f.executions.GetForProject(f.ctx, "foreign", candidate.ID); !errors.Is(err, executionproposal.ErrNotFound) {
		t.Fatalf("foreign history: %v", err)
	}
	history, err := f.executions.GetForProject(f.ctx, candidate.ProjectID, candidate.ID)
	if err != nil || history.TaskDefinitionRevisionID != candidate.TaskDefinitionRevisionID || history.ImplementationRunID != reserved.ID {
		t.Fatalf("frozen history: %+v %v", history, err)
	}
	if _, err := f.executions.Create(f.ctx, executionproposal.CreateInput{AISessionID: f.session.ID, TaskPlanID: f.source.ID}); err == nil {
		t.Fatal("old proposal creation bypassed Task admission")
	}
}
