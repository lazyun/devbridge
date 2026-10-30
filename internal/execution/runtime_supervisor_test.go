package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type runtimeRepositories struct {
	values map[string]repository.Repository
}

func (r runtimeRepositories) Get(_ context.Context, id string) (repository.Repository, error) {
	value, ok := r.values[id]
	if !ok {
		return repository.Repository{}, repository.ErrNotFound
	}
	return value, nil
}

type runtimeConfigs struct{ values map[string]codexconfig.Config }

func (c runtimeConfigs) Get(_ context.Context, id string) (codexconfig.Config, error) {
	value, ok := c.values[id]
	if !ok {
		return codexconfig.Config{}, codexconfig.ErrNotFound
	}
	return value, nil
}

type runtimeWorktrees struct {
	mu        sync.Mutex
	root      string
	paths     map[string]string
	items     map[string]worktree.Worktree
	seedPaths []string
	fail      error
	entered   chan struct{}
}

func (w *runtimeWorktrees) Create(ctx context.Context, input worktree.CreateInput) (worktree.Worktree, error) {
	if w.entered != nil {
		select {
		case w.entered <- struct{}{}:
		default:
		}
	}
	if w.fail != nil {
		return worktree.Worktree{}, w.fail
	}
	if err := ctx.Err(); err != nil {
		return worktree.Worktree{}, err
	}
	ownerID := input.RunID
	if input.WorkspaceID != "" {
		ownerID = input.WorkspaceID
	}
	path := filepath.Join(w.root, ownerID)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return worktree.Worktree{}, err
	}
	for _, relative := range w.seedPaths {
		target := filepath.Join(path, filepath.FromSlash(relative))
		if filepath.Ext(relative) == "" {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return worktree.Worktree{}, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return worktree.Worktree{}, err
		}
		if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
			return worktree.Worktree{}, err
		}
	}
	w.mu.Lock()
	if w.paths == nil {
		w.paths = make(map[string]string)
	}
	w.paths[input.RunID] = path
	value := worktree.Worktree{ID: "wt-" + ownerID, RunID: input.RunID, RepositoryID: input.RepositoryID, RepositoryPath: input.RepositoryPath, Path: path, BaseCommit: input.BaseCommit, Status: worktree.StatusReady}
	if w.items == nil {
		w.items = make(map[string]worktree.Worktree)
	}
	w.items[value.ID] = value
	w.mu.Unlock()
	return value, nil
}

func (w *runtimeWorktrees) Get(_ context.Context, id string) (worktree.Worktree, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	value, ok := w.items[id]
	if !ok {
		return worktree.Worktree{}, worktree.ErrNotFound
	}
	return value, nil
}

type runtimeCollector struct {
	mu    sync.Mutex
	paths map[string]int
	next  int
}

func (c *runtimeCollector) CaptureSnapshot(ctx context.Context, _ string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	return fmt.Sprintf("%040x", c.next), nil
}

func (c *runtimeCollector) CollectRun(ctx context.Context, path, base, start, end string) (evidence.GitEvidence, error) {
	if err := ctx.Err(); err != nil {
		return evidence.GitEvidence{}, err
	}
	c.mu.Lock()
	if c.paths == nil {
		c.paths = make(map[string]int)
	}
	c.paths[path]++
	c.mu.Unlock()
	return evidence.GitEvidence{BaseCommit: base, HeadCommit: base, StartSnapshot: start, EndSnapshot: end, Dirty: true, ChangedFiles: []string{path + "/change.txt"}, Diff: "diff " + path}, nil
}

type blockingCodexSession struct {
	id       int64
	started  chan struct{}
	release  chan struct{}
	prompts  chan string
	cwd      string
	usage    *run.TokenUsageObservation
	message  string
	commands []ObservedCommand
	turnErr  error
	closed   atomic.Int32
}

func newBlockingCodexSession(id int64) *blockingCodexSession {
	return &blockingCodexSession{
		id:      id,
		started: make(chan struct{}),
		release: make(chan struct{}),
		prompts: make(chan string, 1),
		usage: &run.TokenUsageObservation{
			Reliable:        true,
			StartCumulative: run.TokenCounts{TotalTokens: id * 100},
			EndCumulative:   run.TokenCounts{InputTokens: id, OutputTokens: id, TotalTokens: id*100 + id*10},
		},
	}
}

func (s *blockingCodexSession) StartThread(_ context.Context, cwd string) (string, error) {
	s.cwd = cwd
	return fmt.Sprintf("thread-%d", s.id), nil
}

func (s *blockingCodexSession) ResumeThread(_ context.Context, threadID, cwd string, _ *run.TokenCounts) (string, error) {
	s.cwd = cwd
	return threadID, nil
}

func (s *blockingCodexSession) StartTurn(ctx context.Context, _ string, prompt string) (CodexTurnResult, error) {
	s.prompts <- prompt
	close(s.started)
	select {
	case <-s.release:
		message := s.message
		if message == "" {
			message = fmt.Sprintf("message-%d", s.id)
		}
		return CodexTurnResult{TurnID: fmt.Sprintf("turn-%d", s.id), Message: message, Commands: s.commands, TokenUsage: s.usage}, s.turnErr
	case <-ctx.Done():
		return CodexTurnResult{}, ctx.Err()
	}
}

func TestRuntimeSupervisorAuthorizedRunUsesExactReservationAndContinuationInstruction(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	defer fixture.supervisor.Shutdown()
	caller, cancelCaller := context.WithCancel(context.Background())
	reserved, err := fixture.supervisor.StartAuthorizedRun(caller, "task-a", "continue the verified fix", []string{"preserve durable task metadata"}, func(ctx context.Context) (run.Run, error) {
		return fixture.runs.Create(ctx, "task-a")
	})
	if err != nil {
		t.Fatal(err)
	}
	if reserved.ID != "run-1" || reserved.Status != run.StatusPreparing {
		t.Fatalf("reserved=%+v", reserved)
	}
	session := awaitBlockingSession(t, fixture.factory)
	select {
	case prompt := <-session.prompts:
		if prompt != "continue the verified fix" {
			t.Fatalf("prompt=%q", prompt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("continuation prompt not delivered")
	}
	cancelCaller()
	time.Sleep(10 * time.Millisecond)
	if current, _ := fixture.runs.Get(context.Background(), reserved.ID); current.Status != run.StatusRunning {
		t.Fatalf("caller cancellation ended authorized run: %+v", current)
	}
	reserveCalls := 0
	_, err = fixture.supervisor.StartAuthorizedRun(context.Background(), "task-a", "continue again", nil, func(ctx context.Context) (run.Run, error) {
		reserveCalls++
		return fixture.runs.Create(ctx, "task-a")
	})
	var active *TaskAlreadyHasActiveRunError
	if !errors.As(err, &active) || reserveCalls != 0 {
		t.Fatalf("second authorized start err=%v reserveCalls=%d", err, reserveCalls)
	}
	close(session.release)
	awaitRunStatus(t, fixture.runs, reserved.ID, run.StatusSucceeded)
}

func (s *blockingCodexSession) Close() error {
	s.closed.Add(1)
	return nil
}

type runtimeFactory struct {
	next     atomic.Int64
	created  chan *blockingCodexSession
	newValue func(int64) CodexSession
	fail     error
}

func newRuntimeFactory() *runtimeFactory {
	factory := &runtimeFactory{created: make(chan *blockingCodexSession, 16)}
	factory.newValue = func(id int64) CodexSession {
		session := newBlockingCodexSession(id)
		factory.created <- session
		return session
	}
	return factory
}

func (f *runtimeFactory) Start(context.Context, codexconfig.Config) (CodexSession, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	return f.newValue(f.next.Add(1)), nil
}

// supervisorTaskStore is a test-local Task store for the memory supervisor
// fixture. Production stores fence lifecycle fields inside generic Update, so
// fixtures seed authoritative facts directly here instead of routing them
// through production mutation paths. This fake is never wired into production
// code; the fenced behavior of real stores is covered by the task package.
type supervisorTaskStore struct {
	mu        sync.RWMutex
	data      map[string]task.Task
	revisions map[string]task.DefinitionRevision
}

func newSupervisorTaskStore() *supervisorTaskStore {
	return &supervisorTaskStore{data: make(map[string]task.Task), revisions: make(map[string]task.DefinitionRevision)}
}

func (s *supervisorTaskStore) Create(_ context.Context, value task.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[value.ID]; exists {
		return errors.New("task id already exists")
	}
	if err := s.snapshot(&value); err != nil {
		return err
	}
	s.data[value.ID] = value
	return nil
}

func (s *supervisorTaskStore) Get(_ context.Context, id string) (task.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.data[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	return value, nil
}

func (s *supervisorTaskStore) List(_ context.Context) ([]task.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]task.Task, 0, len(s.data))
	for _, value := range s.data {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

// Update applies the callback without the production semantic fence so the
// fixture can seed the complete authoritative fact set at test setup.
func (s *supervisorTaskStore) Update(_ context.Context, id string, update func(*task.Task) error) (task.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	if err := update(&value); err != nil {
		return task.Task{}, err
	}
	if err := s.snapshot(&value); err != nil {
		return task.Task{}, err
	}
	s.data[id] = value
	return value, nil
}

func (s *supervisorTaskStore) GetDefinitionRevision(_ context.Context, id string) (task.DefinitionRevision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.revisions[id]
	if !ok {
		return task.DefinitionRevision{}, task.ErrNotFound
	}
	raw, hash, err := task.EncodeDefinition(value.Definition)
	if err != nil {
		return value, err
	}
	value.Definition, err = task.DecodeDefinition(raw, hash)
	return value, err
}

func (s *supervisorTaskStore) snapshot(value *task.Task) error {
	if value.Phase.ReviewStage() && value.ReviewApprovalID == "" {
		return nil
	}
	definition := task.DefinitionFromTask(*value, nil)
	_, hash, err := task.EncodeDefinition(definition)
	if err != nil {
		return err
	}
	current := s.revisions[value.CurrentDefinitionRevisionID]
	if current.ID != "" && current.PayloadSHA256 == hash {
		return nil
	}
	ordinal := current.Ordinal + 1
	revision := task.DefinitionRevision{ID: fmt.Sprintf("task_definition_%s_%d_%s", value.ID, ordinal, hash[:16]), TaskID: value.ID, Ordinal: ordinal, Definition: definition, PayloadSHA256: hash, CreatedAt: value.UpdatedAt}
	s.revisions[revision.ID] = revision
	value.CurrentDefinitionRevisionID = revision.ID
	return nil
}

type runtimeFixture struct {
	supervisor  *RuntimeSupervisor
	coordinator *Coordinator
	runs        *run.Service
	tasks       *task.Service
	taskStore   *supervisorTaskStore
	factory     *runtimeFactory
	worktrees   *runtimeWorktrees
	collector   *runtimeCollector
	executions  *runtimePlanExecutions
	handoffs    *memoryRuntimeHandoffs
}

// memoryRuntimeHandoffs models only the durable supervisor receipt protocol.
// It deliberately does not manufacture Task, backend, or conversation proof.
type memoryRuntimeHandoffs struct {
	mu       sync.Mutex
	values   map[string]executionproposal.Handoff
	projects map[string]string
}

// legacyRuntimeHandoffs is only for the pre-existing generic supervisor unit
// tests. authorizePlan replaces it with memoryRuntimeHandoffs, whose receipts
// must be registered from an immutable execution reservation before claim.
type legacyRuntimeHandoffs struct{ memoryRuntimeHandoffs }

func (s *legacyRuntimeHandoffs) ClaimHandoff(ctx context.Context, projectID, runID string, now time.Time) (executionproposal.Handoff, bool, error) {
	s.mu.Lock()
	if s.values == nil {
		s.values = map[string]executionproposal.Handoff{}
	}
	if s.projects == nil {
		s.projects = map[string]string{}
	}
	if _, exists := s.values[runID]; !exists {
		s.values[runID] = executionproposal.Handoff{RunID: runID, State: executionproposal.HandoffRegistered, RecordExists: true, RecordedState: executionproposal.HandoffRegistered}
		s.projects[runID] = projectID
	}
	s.mu.Unlock()
	return s.memoryRuntimeHandoffs.ClaimHandoff(ctx, projectID, runID, now)
}

// Register is deliberately separate from ClaimHandoff. Test reservations must
// publish the same immutable Run identity that a durable proposal reservation
// would have written before Supervisor delivery is attempted.
func (s *memoryRuntimeHandoffs) Register(value run.Run) error {
	if value.ID == "" || value.ProjectID == "" || value.TaskID == "" || value.ExecutionProposalID == "" || value.RuntimeEpoch == "" || value.OperationKind != run.OperationExecution || value.OperationID != value.ExecutionProposalID {
		return errors.New("incomplete reserved execution handoff")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]executionproposal.Handoff{}
	}
	if s.projects == nil {
		s.projects = map[string]string{}
	}
	if _, exists := s.values[value.ID]; exists {
		return executionproposal.ErrConflict
	}
	s.values[value.ID] = executionproposal.Handoff{RunID: value.ID, TaskID: value.TaskID, ExecutionID: value.ExecutionProposalID, RuntimeEpoch: value.RuntimeEpoch, Attempt: value.Attempt, State: executionproposal.HandoffRegistered, OwnerKind: "EXECUTION_PROPOSAL", RecordExists: true, RecordedState: executionproposal.HandoffRegistered, RecordedRuntimeEpoch: value.RuntimeEpoch}
	s.projects[value.ID] = value.ProjectID
	return nil
}

func (s *memoryRuntimeHandoffs) ClaimHandoff(_ context.Context, projectID, runID string, _ time.Time) (executionproposal.Handoff, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]executionproposal.Handoff{}
	}
	value, exists := s.values[runID]
	if !exists || s.projects[runID] != projectID {
		return executionproposal.Handoff{}, false, executionproposal.ErrNotFound
	}
	if value.State != executionproposal.HandoffRegistered {
		return value, false, nil
	}
	value.State, value.Code = executionproposal.HandoffPreparing, "HANDOFF_IN_PROGRESS"
	s.values[runID] = value
	return value, true, nil
}

// runtimePlanExecutions is a narrow in-memory stand-in for the proposal
// lookup. It only exposes plans explicitly authorized by authorizePlan and
// binds each phase to the Run created for that exact plan.
type runtimePlanExecutions struct {
	mu     sync.Mutex
	values map[string]taskgoal.Execution
}

func (s *runtimePlanExecutions) authorize(id string, value taskgoal.Execution) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]taskgoal.Execution{}
	}
	s.values[id] = value
}

func (s *runtimePlanExecutions) bindRun(id string, purpose run.Purpose, runID string) (taskgoal.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok || value.Status != "CONSUMED" || value.Outcome != "RUNNING" || runID == "" {
		return taskgoal.Execution{}, errors.New("execution reservation is not authorized")
	}
	switch purpose {
	case run.PurposeImplementation:
		if value.RunID != "" {
			return taskgoal.Execution{}, executionproposal.ErrConflict
		}
		value.RunID = runID
	case run.PurposeVerification:
		if value.RunID == "" || value.VerificationRunID != "" {
			return taskgoal.Execution{}, executionproposal.ErrConflict
		}
		value.VerificationRunID = runID
	default:
		return taskgoal.Execution{}, errors.New("unsupported execution purpose")
	}
	s.values[id] = value
	return value, nil
}

func (s *runtimePlanExecutions) Get(_ context.Context, id string) (taskgoal.Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return taskgoal.Execution{}, errors.New("execution proposal not found")
	}
	return value, nil
}

type runtimePlanRunStore struct {
	evidence   map[string]run.ExecutionEvidence
	mu         sync.Mutex
	values     map[string]run.Run
	attempts   map[string]int
	executions *runtimePlanExecutions
}

func (s *runtimePlanRunStore) CreateAttempt(_ context.Context, taskID, runID string, now time.Time) (run.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.values[runID]; exists {
		return run.Run{}, errors.New("run id already exists")
	}
	s.attempts[taskID]++
	value := run.Run{ID: runID, TaskID: taskID, Purpose: run.PurposeImplementation, Attempt: s.attempts[taskID], Status: run.StatusCreated, CreatedAt: now, UpdatedAt: now, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
	s.values[runID] = value
	return value, nil
}

func (s *runtimePlanRunStore) CreateExecutionAttempt(_ context.Context, taskID, runID string, purpose run.Purpose, executionID string, now time.Time) (run.Run, error) {
	owner, err := s.executions.Get(context.Background(), executionID)
	if err != nil || owner.TaskID != taskID || owner.ProjectID == "" || owner.TaskDefinitionRevisionID == "" || owner.BackendSelection == nil || owner.RuntimeConfig.Model == "" {
		return run.Run{}, errors.New("execution reservation owner is incomplete")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.values[runID]; exists {
		return run.Run{}, errors.New("run id already exists")
	}
	s.attempts[taskID]++
	value := run.Run{ID: runID, TaskID: taskID, ProjectID: owner.ProjectID, FeatureAreaID: "area-" + taskID, AgentExecutionID: executionID, ExecutionProposalID: executionID, OperationKind: run.OperationExecution, OperationID: executionID, Purpose: purpose, Attempt: s.attempts[taskID], RuntimeEpoch: "runtime-" + runID, Status: run.StatusCreated, CreatedAt: now, UpdatedAt: now, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
	s.values[runID] = value
	return value, nil
}

func (s *runtimePlanRunStore) Get(_ context.Context, id string) (run.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return run.Run{}, run.ErrNotFound
	}
	return value, nil
}
func (s *runtimePlanRunStore) List(_ context.Context) ([]run.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]run.Run, 0, len(s.values))
	for _, value := range s.values {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].TaskID == values[j].TaskID {
			return values[i].Attempt < values[j].Attempt
		}
		return values[i].TaskID < values[j].TaskID
	})
	return values, nil
}
func (s *runtimePlanRunStore) Update(_ context.Context, id string, update func(*run.Run) error) (run.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return run.Run{}, run.ErrNotFound
	}
	previous := value
	if err := update(&value); err != nil {
		return run.Run{}, err
	}
	if !value.SameOwner(previous) {
		return run.Run{}, run.ErrOwnerImmutable
	}
	s.values[id] = value
	return value, nil
}
func (s *runtimePlanRunStore) ValidateRunExecution(_ context.Context, value run.Run) error {
	if !value.WorkflowOwned() && value.AgentExecutionID == "" {
		return nil
	}
	owner, err := s.executions.Get(context.Background(), value.ExecutionProposalID)
	if err != nil || owner.TaskID != value.TaskID || owner.ProjectID != value.ProjectID || owner.Status != "CONSUMED" || owner.Outcome != "RUNNING" {
		return errors.New("reserved execution owner validation failed")
	}
	if value.Purpose == run.PurposeImplementation && owner.RunID != value.ID || value.Purpose == run.PurposeVerification && owner.VerificationRunID != value.ID {
		return errors.New("reserved execution phase binding failed")
	}
	return nil
}
func (s *runtimePlanRunStore) ValidateRunExecutionDispatch(ctx context.Context, value run.Run) error {
	return s.ValidateRunExecution(ctx, value)
}

func (s *memoryRuntimeHandoffs) RecordHandoff(_ context.Context, projectID, runID string, state executionproposal.HandoffState, code string, now time.Time) (executionproposal.Handoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.values[runID]
	if value.RunID == "" || s.projects[runID] != projectID || value.State != executionproposal.HandoffPreparing {
		return executionproposal.Handoff{}, executionproposal.ErrConflict
	}
	value.State, value.Code = state, code
	if state == executionproposal.HandoffAccepted {
		value.AcceptedAt = &now
	}
	s.values[runID] = value
	return value, nil
}

func (s *memoryRuntimeHandoffs) ReadHandoff(_ context.Context, projectID, runID string) (executionproposal.Handoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[runID]
	if !ok || s.projects[runID] != projectID {
		return executionproposal.Handoff{}, executionproposal.ErrNotFound
	}
	return value, nil
}

func newRuntimeFixture(t *testing.T, taskIDs ...string) *runtimeFixture {
	t.Helper()
	base := strings.Repeat("a", 40)
	taskStore := newSupervisorTaskStore()
	for _, id := range taskIDs {
		err := taskStore.Create(context.Background(), task.Task{
			ID: id, FeatureAreaID: "area-" + id, ProjectID: "project-shared", ModuleID: "module-shared", RepositoryID: "repo-shared",
			CodexConfigID: "config-shared", Goal: "goal " + id, Type: task.TypeDevelopment,
			WorkSpec:         task.WorkSpec{Goal: "goal " + id, CodexInstruction: "goal " + id, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}},
			VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}},
			BaseCommit:       base, Status: task.StatusActive, Phase: task.PhaseImplementing,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	tasks := task.NewService(taskStore, nil, nil, nil, nil, nil)
	var nextRun atomic.Int64
	executions := &runtimePlanExecutions{}
	runStore := &runtimePlanRunStore{values: map[string]run.Run{}, attempts: map[string]int{}, executions: executions}
	runs := run.NewService(runStore, func() (string, error) {
		return fmt.Sprintf("run-%d", nextRun.Add(1)), nil
	}, nil)
	factory := newRuntimeFactory()
	trees := &runtimeWorktrees{root: filepath.Join(t.TempDir(), "worktrees"), paths: make(map[string]string), items: make(map[string]worktree.Worktree)}
	collector := &runtimeCollector{paths: make(map[string]int)}
	workspaces := workspace.NewService(workspace.NewMemoryStore(), nil, nil)
	handoffs := &memoryRuntimeHandoffs{}
	coordinator := &Coordinator{
		Repositories: runtimeRepositories{values: map[string]repository.Repository{"repo-shared": {ID: "repo-shared", ProjectID: "project-shared", Path: "/source/shared"}}},
		CodexConfigs: runtimeConfigs{values: map[string]codexconfig.Config{"config-shared": {ID: "config-shared", Enabled: true}}},
		Tasks:        tasks, Runs: runs, Worktrees: trees, Workspaces: workspaces, Codex: factory, Evidence: collector, Handoffs: &legacyRuntimeHandoffs{},
	}
	supervisor, err := NewRuntimeSupervisor(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeFixture{supervisor: supervisor, coordinator: coordinator, runs: runs, tasks: tasks, taskStore: taskStore, factory: factory, worktrees: trees, collector: collector, executions: executions, handoffs: handoffs}
}

func awaitBlockingSession(t *testing.T, factory *runtimeFactory) *blockingCodexSession {
	t.Helper()
	select {
	case session := <-factory.created:
		select {
		case <-session.started:
			return session
		case <-time.After(2 * time.Second):
			t.Fatal("Codex turn did not start")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Codex session was not created")
	}
	return nil
}

func awaitRunStatus(t *testing.T, runs *run.Service, id string, want run.Status) run.Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		value, err := runs.Get(context.Background(), id)
		if err == nil && value.Status == want {
			return value
		}
		time.Sleep(time.Millisecond)
	}
	value, err := runs.Get(context.Background(), id)
	t.Fatalf("run %s status=%s err=%v, want %s", id, value.Status, err, want)
	return run.Run{}
}

func TestRuntimeSupervisorStartHandoffDetachesCallerContext(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	caller, cancelCaller := context.WithCancel(context.Background())
	returned := make(chan run.Run, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := fixture.supervisor.StartRun(caller, "task-a")
		returned <- value
		errs <- err
	}()
	var started run.Run
	select {
	case started = <-returned:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartRun waited for Codex completion")
	}
	if started.Status != run.StatusPreparing {
		t.Fatalf("handoff status=%s", started.Status)
	}
	session := awaitBlockingSession(t, fixture.factory)
	cancelCaller()
	time.Sleep(10 * time.Millisecond)
	if current, _ := fixture.runs.Get(context.Background(), started.ID); current.Status != run.StatusRunning {
		t.Fatalf("caller cancellation changed owned run to %s", current.Status)
	}
	close(session.release)
	finished := awaitRunStatus(t, fixture.runs, started.ID, run.StatusSucceeded)
	if finished.StartedAt == nil || finished.FinishedAt == nil {
		t.Fatalf("run timing was not finalized: %+v", finished)
	}
	if err := fixture.supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSupervisorSameTaskRaceHasOneWinner(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	start := make(chan struct{})
	type outcome struct {
		value run.Run
		err   error
	}
	outcomes := make(chan outcome, 2)
	for range 2 {
		go func() {
			<-start
			value, err := fixture.supervisor.StartRun(context.Background(), "task-a")
			outcomes <- outcome{value: value, err: err}
		}()
	}
	close(start)
	first, second := <-outcomes, <-outcomes
	values := []outcome{first, second}
	winners := 0
	losers := 0
	var winner run.Run
	for _, value := range values {
		if value.err == nil {
			winners++
			winner = value.value
			continue
		}
		var active *TaskAlreadyHasActiveRunError
		if errors.As(value.err, &active) {
			losers++
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("outcomes=%+v", values)
	}
	awaitBlockingSession(t, fixture.factory)
	if _, err := fixture.supervisor.CancelRun(context.Background(), winner.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSupervisorDifferentTasksShareRepositoryButStayIsolated(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a", "task-b")
	results := make(chan run.Run, 2)
	errs := make(chan error, 2)
	for _, taskID := range []string{"task-a", "task-b"} {
		go func(id string) {
			value, err := fixture.supervisor.StartRun(context.Background(), id)
			results <- value
			errs <- err
		}(taskID)
	}
	runA, runB := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	sessionA := awaitBlockingSession(t, fixture.factory)
	sessionB := awaitBlockingSession(t, fixture.factory)
	liveA, _ := fixture.supervisor.GetSession(runA.ID)
	liveB, _ := fixture.supervisor.GetSession(runB.ID)
	sessionA = liveA.client.(*blockingCodexSession)
	sessionB = liveB.client.(*blockingCodexSession)
	if sessionA == sessionB || sessionA.cwd == sessionB.cwd {
		t.Fatalf("sessions or worktrees were shared: %p %q / %p %q", sessionA, sessionA.cwd, sessionB, sessionB.cwd)
	}
	liveA, okA := fixture.supervisor.GetSession(runA.ID)
	liveB, okB := fixture.supervisor.GetSession(runB.ID)
	if !okA || !okB || liveA == liveB || liveA.client == nil || liveB.client == nil || liveA.client == liveB.client {
		t.Fatalf("independent live clients not owned: a=%+v/%v b=%+v/%v", liveA, okA, liveB, okB)
	}
	close(sessionA.release)
	close(sessionB.release)
	finishedA := awaitRunStatus(t, fixture.runs, runA.ID, run.StatusSucceeded)
	finishedB := awaitRunStatus(t, fixture.runs, runB.ID, run.StatusSucceeded)
	paths := []string{finishedA.Result.GitEvidence.Diff, finishedB.Result.GitEvidence.Diff}
	if paths[0] == paths[1] {
		t.Fatalf("evidence was shared: %v", paths)
	}
	usage := []int64{finishedA.TokenUsage.Usage.TotalTokens, finishedB.TokenUsage.Usage.TotalTokens}
	sort.Slice(usage, func(i, j int) bool { return usage[i] < usage[j] })
	if fmt.Sprint(usage) != "[10 20]" {
		t.Fatalf("per-run usage=%v", usage)
	}
}

func TestRuntimeSupervisorCancelTargetsOnlyOneRun(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a", "task-b")
	runA, err := fixture.supervisor.StartRun(context.Background(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	runB, err := fixture.supervisor.StartRun(context.Background(), "task-b")
	if err != nil {
		t.Fatal(err)
	}
	sessionA := awaitBlockingSession(t, fixture.factory)
	sessionB := awaitBlockingSession(t, fixture.factory)
	liveA, _ := fixture.supervisor.GetSession(runA.ID)
	liveB, _ := fixture.supervisor.GetSession(runB.ID)
	sessionA = liveA.client.(*blockingCodexSession)
	sessionB = liveB.client.(*blockingCodexSession)
	canceled, err := fixture.supervisor.CancelRun(context.Background(), runA.ID)
	if err != nil || canceled.Status != run.StatusCanceled {
		t.Fatalf("canceled=%+v err=%v", canceled, err)
	}
	if current, _ := fixture.runs.Get(context.Background(), runB.ID); current.Status != run.StatusRunning {
		t.Fatalf("other run status=%s", current.Status)
	}
	if _, ok := fixture.supervisor.GetSession(runB.ID); !ok {
		t.Fatal("other run session was removed")
	}
	if sessionA.closed.Load() == 0 || sessionB.closed.Load() != 0 {
		t.Fatalf("close counts target=%d other=%d", sessionA.closed.Load(), sessionB.closed.Load())
	}
	close(sessionB.release)
	awaitRunStatus(t, fixture.runs, runB.ID, run.StatusSucceeded)
}

func TestRuntimeSupervisorShutdownCancelsAndJoinsAllSessions(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a", "task-b")
	runA, _ := fixture.supervisor.StartRun(context.Background(), "task-a")
	runB, _ := fixture.supervisor.StartRun(context.Background(), "task-b")
	sessionA := awaitBlockingSession(t, fixture.factory)
	sessionB := awaitBlockingSession(t, fixture.factory)
	if err := fixture.supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{runA.ID, runB.ID} {
		value := awaitRunStatus(t, fixture.runs, id, run.StatusInfrastructureFailed)
		if value.Failure == nil || value.Failure.Code != BridgeShutdownInterruptedCode {
			t.Fatalf("shutdown failure=%+v", value.Failure)
		}
		if _, ok := fixture.supervisor.GetSession(id); ok {
			t.Fatalf("session %s survived shutdown", id)
		}
	}
	select {
	case <-sessionA.started:
	default:
		t.Fatal("session A did not start")
	}
	if sessionA.closed.Load() == 0 || sessionB.closed.Load() == 0 {
		t.Fatalf("sessions not closed: %d/%d", sessionA.closed.Load(), sessionB.closed.Load())
	}
	if _, err := fixture.supervisor.StartRun(context.Background(), "task-a"); !errors.Is(err, ErrSupervisorShuttingDown) {
		t.Fatalf("post-shutdown start err=%v", err)
	}
}

func TestRuntimeSupervisorExplicitCancelOwnsShutdownRace(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	started, err := fixture.supervisor.StartRun(context.Background(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	awaitBlockingSession(t, fixture.factory)
	live, ok := fixture.supervisor.GetSession(started.ID)
	if !ok {
		t.Fatal("missing live session")
	}
	live.cancel(ErrRunCanceledByUser)
	if err := fixture.supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
	value := awaitRunStatus(t, fixture.runs, started.ID, run.StatusCanceled)
	if value.Failure == nil || value.Failure.Kind != run.FailureCanceled {
		t.Fatalf("explicit cancellation lost terminal ownership: %+v", value)
	}
}

func TestRuntimeSupervisorNaturalCompletionCancelRaceHasOneTerminalOwner(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		fixture := newRuntimeFixture(t, "task-a")
		started, err := fixture.supervisor.StartRun(context.Background(), "task-a")
		if err != nil {
			t.Fatal(err)
		}
		session := awaitBlockingSession(t, fixture.factory)
		cancelDone := make(chan struct{})
		go func() {
			_, _ = fixture.supervisor.CancelRun(context.Background(), started.ID)
			close(cancelDone)
		}()
		close(session.release)
		<-cancelDone
		value, err := fixture.runs.Get(context.Background(), started.ID)
		if err != nil {
			t.Fatal(err)
		}
		if value.Status != run.StatusSucceeded && value.Status != run.StatusCanceled {
			t.Fatalf("iteration %d left nonterminal status %s", iteration, value.Status)
		}
		if value.FinishedAt == nil {
			t.Fatalf("iteration %d terminal run omitted FinishedAt", iteration)
		}
		if _, ok := fixture.supervisor.GetSession(started.ID); ok {
			t.Fatalf("iteration %d retained a terminal session", iteration)
		}
	}
}

type approvalCodexSession struct {
	*blockingCodexSession
	request   codex.ApprovalRequest
	requested chan struct{}
	decisions chan codex.ApprovalDecision
	once      sync.Once
}

func newApprovalCodexSession(id int64) *approvalCodexSession {
	approvalID := fmt.Sprintf("approval-%d", id)
	return &approvalCodexSession{
		blockingCodexSession: newBlockingCodexSession(id),
		request:              codex.ApprovalRequest{ID: approvalID, ThreadID: fmt.Sprintf("thread-%d", id), TurnID: fmt.Sprintf("turn-%d", id), ItemID: fmt.Sprintf("item-%d", id), Kind: codex.ApprovalKindCommandExecution, RequestPayload: []byte(`{"reason":"test"}`), AllowedDecisions: []codex.DecisionType{codex.DecisionApproveOnce, codex.DecisionDecline}},
		requested:            make(chan struct{}),
		decisions:            make(chan codex.ApprovalDecision, 1),
	}
}

func (s *approvalCodexSession) StartTurn(ctx context.Context, _ string, _ string) (CodexTurnResult, error) {
	close(s.started)
	select {
	case decision := <-s.decisions:
		if decision.Type == codex.DecisionDecline {
			return CodexTurnResult{TurnID: fmt.Sprintf("turn-%d", s.id)}, &TaskExecutionError{Err: errors.New("declined")}
		}
		return CodexTurnResult{TurnID: fmt.Sprintf("turn-%d", s.id), Message: fmt.Sprintf("message-%d", s.id), TokenUsage: s.usage}, nil
	case <-ctx.Done():
		return CodexTurnResult{}, ctx.Err()
	}
}

func (s *approvalCodexSession) ResumeThread(_ context.Context, threadID, cwd string, _ *run.TokenCounts) (string, error) {
	s.cwd = cwd
	s.request.ThreadID = threadID
	return threadID, nil
}

func (s *approvalCodexSession) NextApproval(ctx context.Context) (codex.ApprovalRequest, error) {
	var value codex.ApprovalRequest
	delivered := false
	s.once.Do(func() {
		value = s.request
		delivered = true
		close(s.requested)
	})
	if delivered {
		return value, nil
	}
	<-ctx.Done()
	return codex.ApprovalRequest{}, ctx.Err()
}

func (s *approvalCodexSession) ResolveApproval(_ context.Context, id string, decision codex.ApprovalDecision) error {
	if id != s.request.ID {
		return fmt.Errorf("approval %s delivered to session for %s", id, s.request.ID)
	}
	s.decisions <- decision
	return nil
}

type testApprovalController struct {
	runs *run.Service

	mu        sync.Mutex
	sessions  map[string]approval.Resolver
	requests  map[string]string
	inFlight  map[string]chan struct{}
	requested chan string
}

func newTestApprovalController(runs *run.Service) *testApprovalController {
	return &testApprovalController{runs: runs, sessions: make(map[string]approval.Resolver), requests: make(map[string]string), inFlight: make(map[string]chan struct{}), requested: make(chan string, 16)}
}

func (c *testApprovalController) Register(runID string, resolver approval.Resolver, _ context.CancelFunc) (func(), error) {
	c.mu.Lock()
	c.sessions[runID] = resolver
	c.inFlight[runID] = make(chan struct{})
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		delete(c.sessions, runID)
		delete(c.inFlight, runID)
		c.mu.Unlock()
	}, nil
}

func (c *testApprovalController) Request(ctx context.Context, runID string, request codex.ApprovalRequest) (approval.Approval, error) {
	if _, err := c.runs.Transition(ctx, runID, run.StatusWaitingApproval); err != nil {
		return approval.Approval{}, err
	}
	c.mu.Lock()
	c.requests[request.ID] = runID
	c.mu.Unlock()
	c.requested <- request.ID
	return approval.Approval{ID: request.ID, RunID: runID, Status: approval.StatusPending}, nil
}

func (c *testApprovalController) Wait(ctx context.Context, runID string) error {
	c.mu.Lock()
	done := c.inFlight[runID]
	c.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *testApprovalController) decide(ctx context.Context, approvalID string, decision codex.ApprovalDecision) error {
	c.mu.Lock()
	runID := c.requests[approvalID]
	resolver := c.sessions[runID]
	done := c.inFlight[runID]
	c.mu.Unlock()
	if runID == "" || resolver == nil {
		return errors.New("approval is not correlated to a live run")
	}
	if err := resolver.ResolveApproval(ctx, approvalID, decision); err != nil {
		return err
	}
	if _, err := c.runs.Transition(ctx, runID, run.StatusRunning); err != nil {
		return err
	}
	close(done)
	return nil
}

func TestRuntimeSupervisorApprovalWaitDoesNotBlockOtherTaskAndStaysCorrelated(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a", "task-b")
	controller := newTestApprovalController(fixture.runs)
	fixture.coordinator.Approvals = controller
	sessions := make(chan *approvalCodexSession, 2)
	fixture.factory.newValue = func(id int64) CodexSession {
		session := newApprovalCodexSession(id)
		sessions <- session
		return session
	}

	runA, err := fixture.supervisor.StartRun(context.Background(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	sessionA := <-sessions
	select {
	case <-sessionA.requested:
	case <-time.After(2 * time.Second):
		t.Fatal("task A did not request approval")
	}
	awaitRunStatus(t, fixture.runs, runA.ID, run.StatusWaitingApproval)

	runB, err := fixture.supervisor.StartRun(context.Background(), "task-b")
	if err != nil {
		t.Fatal(err)
	}
	sessionB := <-sessions
	select {
	case <-sessionB.requested:
	case <-time.After(2 * time.Second):
		t.Fatal("task B was blocked by task A approval")
	}
	awaitRunStatus(t, fixture.runs, runB.ID, run.StatusWaitingApproval)

	if err := controller.decide(context.Background(), sessionA.request.ID, codex.ApprovalDecision{Type: codex.DecisionApproveOnce}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sessionB.decisions:
		t.Fatal("task A approval was routed to task B")
	default:
	}
	if err := controller.decide(context.Background(), sessionB.request.ID, codex.ApprovalDecision{Type: codex.DecisionApproveOnce}); err != nil {
		t.Fatal(err)
	}
	awaitRunStatus(t, fixture.runs, runA.ID, run.StatusSucceeded)
	awaitRunStatus(t, fixture.runs, runB.ID, run.StatusSucceeded)
}

func TestRuntimeSupervisorApprovalCorrelationAcrossReusedThreadTurns(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	fixture.authorizePlan(t, "task-a", task.WorkSpec{Goal: "implement", CodexInstruction: "implement", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}, "execution-first")
	conversation := &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "canonical", FeatureAreaID: "area-task-a", ContinuationSafe: true}, bindRun: func(runID, threadID string) error {
		_, err := fixture.runs.AttachThread(context.Background(), runID, threadID)
		return err
	}}
	fixture.coordinator.Conversations = conversation
	controller := newTestApprovalController(fixture.runs)
	fixture.coordinator.Approvals = controller
	sessions := make(chan *approvalCodexSession, 2)
	fixture.factory.newValue = func(id int64) CodexSession {
		session := newApprovalCodexSession(id)
		sessions <- session
		return session
	}

	first, err := fixture.supervisor.StartAuthorizedRun(context.Background(), "task-a", "implement", nil, func(ctx context.Context) (run.Run, error) {
		return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-first")
	})
	if err != nil {
		t.Fatal(err)
	}
	firstSession := awaitApprovalSession(t, sessions, "first")
	select {
	case <-firstSession.requested:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn did not request approval")
	}
	awaitRunStatus(t, fixture.runs, first.ID, run.StatusWaitingApproval)
	if err := controller.decide(context.Background(), firstSession.request.ID, codex.ApprovalDecision{Type: codex.DecisionApproveOnce}); err != nil {
		t.Fatal(err)
	}
	first = awaitRunStatus(t, fixture.runs, first.ID, run.StatusSucceeded)
	if conversation.conversation.BackendThreadID == "" || conversation.conversation.CurrentBoundaryTurnID == "" {
		t.Fatalf("first turn did not persist conversation continuity: %+v", conversation.conversation)
	}

	fixture.authorizePlan(t, "task-a", task.WorkSpec{Goal: "continue", CodexInstruction: "continue after the first approved turn", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}, "execution-second")
	// authorizePlan installs its default runtime helper. Preserve the canonical
	// conversation from the first reservation so this turn must resume it.
	fixture.coordinator.Conversations = conversation
	second, err := fixture.supervisor.StartAuthorizedRun(context.Background(), "task-a", "continue after the first approved turn", nil, func(ctx context.Context) (run.Run, error) {
		return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-second")
	})
	if err != nil {
		t.Fatal(err)
	}
	secondSession := awaitApprovalSession(t, sessions, "second")
	select {
	case <-secondSession.requested:
	case <-time.After(2 * time.Second):
		t.Fatal("second turn did not request approval")
	}
	awaitRunStatus(t, fixture.runs, second.ID, run.StatusWaitingApproval)
	if firstSession.request.ThreadID == "" || firstSession.request.ThreadID != secondSession.request.ThreadID || firstSession.request.TurnID == secondSession.request.TurnID {
		t.Fatalf("approval thread/turn correlation: first=%+v second=%+v", firstSession.request, secondSession.request)
	}
	controller.mu.Lock()
	firstOwner := controller.requests[firstSession.request.ID]
	secondOwner := controller.requests[secondSession.request.ID]
	controller.mu.Unlock()
	if firstOwner != first.ID || secondOwner != second.ID || firstOwner == secondOwner {
		t.Fatalf("approval Run owners: first=%q second=%q", firstOwner, secondOwner)
	}
	if err := controller.decide(context.Background(), secondSession.request.ID, codex.ApprovalDecision{Type: codex.DecisionApproveOnce}); err != nil {
		t.Fatal(err)
	}
	second = awaitRunStatus(t, fixture.runs, second.ID, run.StatusSucceeded)
	if first.WorkspaceID != second.WorkspaceID || first.WorktreeID != second.WorktreeID || first.ThreadID != second.ThreadID || first.TurnID == second.TurnID {
		t.Fatalf("continued Run identity: first=%+v second=%+v", first, second)
	}
}

func awaitApprovalSession(t *testing.T, sessions <-chan *approvalCodexSession, turn string) *approvalCodexSession {
	t.Helper()
	select {
	case session := <-sessions:
		return session
	case <-time.After(2 * time.Second):
		t.Fatalf("%s turn did not create an approval session", turn)
		return nil
	}
}

// preparingRuns injects around the durable Run PREPARING transition so handoff
// tests can fail or block after the Run exists but before supervisor
// acknowledgment completes. Mode 1 fails after the persisted transition;
// mode 2 blocks until the handoff context ends.
type preparingRuns struct {
	RunService
	entered chan struct{}
	mode    int
	closed  bool
}

func (p *preparingRuns) Transition(ctx context.Context, id string, to run.Status) (run.Run, error) {
	value, err := p.RunService.Transition(ctx, id, to)
	if err != nil || to != run.StatusPreparing {
		return value, err
	}
	if p.entered != nil && !p.closed {
		p.closed = true
		close(p.entered)
	}
	switch p.mode {
	case 1:
		return value, errors.New("injected run preparation failure")
	case 2:
		<-ctx.Done()
		return value, ctx.Err()
	}
	return value, nil
}

func TestRuntimeSupervisorHandoffRollbackLeavesNoGhostSession(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	fixture.coordinator.Runs = &preparingRuns{RunService: fixture.coordinator.Runs, mode: 1}
	value, err := fixture.supervisor.StartRun(context.Background(), "task-a")
	if err == nil {
		t.Fatal("handoff unexpectedly succeeded")
	}
	if value.ID == "" || value.Status != run.StatusInfrastructureFailed || value.FinishedAt == nil {
		t.Fatalf("rolled back run=%+v err=%v", value, err)
	}
	if _, ok := fixture.supervisor.GetSession(value.ID); ok {
		t.Fatal("failed handoff left a live session")
	}
	fixture.supervisor.mu.Lock()
	_, owned := fixture.supervisor.operationOwners[runtimeOwner{TaskID: "task-a"}]
	_, starting := fixture.supervisor.starting[runtimeOwner{TaskID: "task-a"}]
	fixture.supervisor.mu.Unlock()
	if owned || starting {
		t.Fatalf("failed handoff left task reservation: owned=%t starting=%t", owned, starting)
	}
	// A transport failure terminalizes only the Run/Execution transport. The
	// Task keeps its ACTIVE lifecycle and IMPLEMENTING stage authority.
	if got, getErr := fixture.tasks.Get(context.Background(), "task-a"); getErr != nil || got.Status != task.StatusActive || got.Phase != task.PhaseImplementing {
		t.Fatalf("failed handoff changed Task lifecycle: task=%+v err=%v", got, getErr)
	}
	retry, retryErr := fixture.supervisor.StartRun(context.Background(), "task-a")
	var notExecutable *TaskNotExecutableError
	if errors.As(retryErr, &notExecutable) || retry.ID == "" {
		t.Fatalf("ACTIVE Task was rejected after transport failure: run=%+v err=%v", retry, retryErr)
	}
}

func TestRuntimeSupervisorShutdownInterruptsAndJoinsInProgressHandoff(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	entered := make(chan struct{})
	fixture.coordinator.Runs = &preparingRuns{RunService: fixture.coordinator.Runs, entered: entered, mode: 2}
	type outcome struct {
		value run.Run
		err   error
	}
	result := make(chan outcome, 1)
	go func() {
		value, err := fixture.supervisor.StartRun(context.Background(), "task-a")
		result <- outcome{value: value, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handoff did not reach durable PREPARING state")
	}
	if err := fixture.supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err == nil || got.value.Status != run.StatusInfrastructureFailed || got.value.Failure == nil || got.value.Failure.Code != BridgeShutdownInterruptedCode {
			t.Fatalf("handoff result=%+v err=%v", got.value, got.err)
		}
		if _, ok := fixture.supervisor.GetSession(got.value.ID); ok {
			t.Fatal("interrupted handoff left a session")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown returned without joining the in-progress handoff")
	}
}

func TestRuntimeSupervisorStartsEmptyAcrossConstruction(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	first, err := NewRuntimeSupervisor(fixture.coordinator)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRuntimeSupervisor(fixture.coordinator)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.sessions) != 0 || len(second.sessions) != 0 || len(first.operationOwners) != 0 || len(second.operationOwners) != 0 {
		t.Fatal("new supervisor reconstructed live sessions from durable state")
	}
	_ = first.Shutdown()
	_ = second.Shutdown()
}

// authorizePlan records the frozen Task definition and reservation owner before
// either Run exists. reserveExecution then binds one immutable phase Run and a
// registered handoff receipt; no test path can manufacture either at claim
// time.
func (f *runtimeFixture) authorizePlan(t *testing.T, id string, work task.WorkSpec, verification task.VerificationSpec, executionID string) {
	t.Helper()
	value, err := f.taskStore.Update(context.Background(), id, func(v *task.Task) error {
		v.WorkSpec, v.VerificationSpec = task.CloneWorkSpec(work), task.CloneVerificationSpec(verification)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.coordinator.CodexConfigs.Get(context.Background(), value.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	f.executions.authorize(executionID, taskgoal.Execution{ID: executionID, TaskID: value.ID, ProjectID: value.ProjectID, Status: "CONSUMED", Outcome: "RUNNING", TaskDefinitionRevisionID: value.CurrentDefinitionRevisionID, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, BackendSelection: &codexconfig.BackendSelection{ConfigID: config.ID, ConfigFingerprint: config.Fingerprint()}})
	f.coordinator.Executions = f.executions
	f.coordinator.Handoffs = f.handoffs
	f.coordinator.RuntimeConfigProvider = codexruntime.Default
	f.coordinator.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation-" + id, FeatureAreaID: value.FeatureAreaID, ContinuationSafe: true}, bindRun: func(runID, threadID string) error {
		_, bindErr := f.runs.AttachThread(context.Background(), runID, threadID)
		return bindErr
	}}
}

func (f *runtimeFixture) reserveExecution(ctx context.Context, taskID string, purpose run.Purpose, executionID string) (run.Run, error) {
	value, err := f.runs.CreateForExecution(ctx, taskID, purpose, executionID)
	if err != nil {
		return value, err
	}
	if _, err = f.executions.bindRun(executionID, purpose, value.ID); err != nil {
		return value, err
	}
	if err = f.handoffs.Register(value); err != nil {
		return value, err
	}
	return value, nil
}

func (s *blockingCodexSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, schema json.RawMessage) (CodexTurnResult, error) {
	result, err := s.StartTurn(ctx, thread, prompt)
	if string(schema) == ImplementationReportJSONSchema {
		raw, _ := json.Marshal(ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}

func (s *approvalCodexSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, schema json.RawMessage) (CodexTurnResult, error) {
	result, err := s.StartTurn(ctx, thread, prompt)
	if string(schema) == ImplementationReportJSONSchema {
		raw, _ := json.Marshal(ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}

func (s *runtimePlanRunStore) PrepareNaturalResult(ctx context.Context, id string) error {
	_, err := s.Update(ctx, id, func(v *run.Run) error {
		if v.TurnID != "" {
			return errors.New("already dispatched")
		}
		v.ResultProtocol = run.NaturalResultProtocol
		return nil
	})
	return err
}
func (s *runtimePlanRunStore) RecordExecutionCommands(_ context.Context, id string, raw json.RawMessage) error {
	var ev run.ExecutionEvidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.evidence == nil {
		s.evidence = map[string]run.ExecutionEvidence{}
	}
	s.evidence[id] = ev
	return nil
}
