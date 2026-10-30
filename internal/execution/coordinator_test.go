package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type fakeRepositories struct {
	value repository.Repository
	head  string
	err   error
}

type fakeCodexConfigs struct {
	value codexconfig.Config
	err   error
}

func (f fakeCodexConfigs) Get(context.Context, string) (codexconfig.Config, error) {
	return f.value, f.err
}

func (f fakeRepositories) Get(context.Context, string) (repository.Repository, error) {
	return f.value, f.err
}
func (f fakeRepositories) CurrentHEAD(context.Context, string) (string, error) {
	return f.head, f.err
}

type fakeTasks struct {
	value    task.Task
	err      error
	revision *task.DefinitionRevision
}

func (f *fakeTasks) Get(context.Context, string) (task.Task, error) { return f.value, f.err }
func (f *fakeTasks) Transition(_ context.Context, _ string, to task.Status) (task.Task, error) {
	f.value.Status = to
	return f.value, nil
}

type recordingRuns struct {
	service           *run.Service
	store             *run.MemoryStore
	sequence          []run.Status
	validateExecution func(context.Context, run.Run) error
}

// Unit fixtures model the durable validator separately from lifecycle storage;
// SQLite integration tests exercise the actual ownership proof.
func (r *recordingRuns) ValidateRunExecution(ctx context.Context, value run.Run) error {
	if r.validateExecution != nil {
		return r.validateExecution(ctx, value)
	}
	return nil
}

func (r *recordingRuns) ValidateRunExecutionDispatch(ctx context.Context, value run.Run) error {
	return r.ValidateRunExecution(ctx, value)
}

func newRecordingRuns() *recordingRuns {
	store := run.NewMemoryStore()
	return &recordingRuns{store: store, service: run.NewService(store, func() (string, error) { return "run-1", nil }, nil)}
}
func (r *recordingRuns) Create(ctx context.Context, taskID string) (run.Run, error) {
	value, err := r.service.Create(ctx, taskID)
	if err == nil {
		r.sequence = append(r.sequence, value.Status)
	}
	return value, err
}
func (r *recordingRuns) Get(ctx context.Context, id string) (run.Run, error) {
	return r.service.Get(ctx, id)
}
func (r *recordingRuns) RecordPrompt(ctx context.Context, id, prompt string, sentAt time.Time) (run.Run, error) {
	return r.service.RecordPrompt(ctx, id, prompt, sentAt)
}
func (r *recordingRuns) Transition(ctx context.Context, id string, to run.Status) (run.Run, error) {
	value, err := r.service.Transition(ctx, id, to)
	if err == nil {
		r.sequence = append(r.sequence, to)
	}
	return value, err
}
func (r *recordingRuns) AttachWorktree(ctx context.Context, id, worktreeID string) (run.Run, error) {
	return r.service.AttachWorktree(ctx, id, worktreeID)
}
func (r *recordingRuns) AttachWorkspace(ctx context.Context, id, workspaceID string) (run.Run, error) {
	return r.service.AttachWorkspace(ctx, id, workspaceID)
}
func (r *recordingRuns) AttachThread(ctx context.Context, id, threadID string) (run.Run, error) {
	return r.service.AttachThread(ctx, id, threadID)
}
func (r *recordingRuns) AttachTurn(ctx context.Context, id, turnID string) (run.Run, error) {
	return r.service.AttachTurn(ctx, id, turnID)
}
func (r *recordingRuns) AttachStartSnapshot(ctx context.Context, id, snapshot string) (run.Run, error) {
	return r.service.AttachStartSnapshot(ctx, id, snapshot)
}
func (r *recordingRuns) AttachEndSnapshot(ctx context.Context, id, snapshot string) (run.Run, error) {
	return r.service.AttachEndSnapshot(ctx, id, snapshot)
}
func (r *recordingRuns) AttachResult(ctx context.Context, id string, result run.Result) (run.Run, error) {
	return r.service.AttachResult(ctx, id, result)
}
func (r *recordingRuns) Succeed(ctx context.Context, id string, result run.Result) (run.Run, error) {
	value, err := r.service.Succeed(ctx, id, result)
	if err == nil {
		r.sequence = append(r.sequence, run.StatusSucceeded)
	}
	return value, err
}
func (r *recordingRuns) Fail(ctx context.Context, id string, failure run.Failure) (run.Run, error) {
	value, err := r.service.Fail(ctx, id, failure)
	if err == nil {
		r.sequence = append(r.sequence, value.Status)
	}
	return value, err
}
func (r *recordingRuns) SucceedWithUsage(ctx context.Context, id string, result run.Result, usage *run.TokenUsageObservation) (run.Run, error) {
	value, err := r.service.SucceedWithUsage(ctx, id, result, usage)
	if err == nil {
		r.sequence = append(r.sequence, run.StatusSucceeded)
	}
	return value, err
}
func (r *recordingRuns) FailWithUsage(ctx context.Context, id string, failure run.Failure, usage *run.TokenUsageObservation) (run.Run, error) {
	value, err := r.service.FailWithUsage(ctx, id, failure, usage)
	if err == nil {
		r.sequence = append(r.sequence, value.Status)
	}
	return value, err
}

type fakeWorktrees struct {
	value   worktree.Worktree
	err     error
	input   worktree.CreateInput
	creates int
}

func (f *fakeWorktrees) Create(_ context.Context, input worktree.CreateInput) (worktree.Worktree, error) {
	f.input = input
	f.creates++
	return f.value, f.err
}
func (f *fakeWorktrees) Get(_ context.Context, id string) (worktree.Worktree, error) {
	if f.value.ID != id {
		return worktree.Worktree{}, worktree.ErrNotFound
	}
	return f.value, f.err
}

type fakeCollector struct {
	value      evidence.GitEvidence
	err        error
	path, base string
	captures   int
	stable     bool
}

func (f *fakeCollector) CaptureSnapshot(_ context.Context, path string) (string, error) {
	f.path = path
	f.captures++
	if f.err != nil {
		return "", f.err
	}
	if f.captures == 1 || f.stable {
		return strings.Repeat("1", 40), nil
	}
	return strings.Repeat("2", 40), nil
}
func (f *fakeCollector) CollectRun(_ context.Context, path, base, start, end string) (evidence.GitEvidence, error) {
	f.path, f.base = path, base
	f.value.StartSnapshot, f.value.EndSnapshot = start, end
	return f.value, f.err
}

type fakeFactory struct {
	session *fakeSession
	err     error
}

type sessionFactory struct{ session CodexSession }

func (f sessionFactory) Start(context.Context, codexconfig.Config) (CodexSession, error) {
	return f.session, nil
}

type recordingDiagnostics struct {
	taskID, threadID, message string
	calls                     int
}

type coordinatorReviewRuntime struct {
	evidence review.EvidenceSummary
	prompt   string
	sentAt   time.Time
	value    review.Review
}

func (f *coordinatorReviewRuntime) Get(context.Context, string) (review.Review, error) {
	return f.value, nil
}
func (f *coordinatorReviewRuntime) RecordPrompt(_ context.Context, _ string, prompt string, sentAt time.Time) (review.Review, error) {
	f.prompt, f.sentAt = prompt, sentAt
	f.value.FinalCodexPrompt = prompt
	return f.value, nil
}
func (*coordinatorReviewRuntime) RecordAccess(context.Context, string, string, review.AccessOutcome, string) error {
	return nil
}
func (f *coordinatorReviewRuntime) SummarizeEvidence(context.Context, string) (review.EvidenceSummary, error) {
	return f.evidence, nil
}

func (d *recordingDiagnostics) RecordThreadNameFailure(_ context.Context, taskID, threadID, message string, _ time.Time) error {
	d.taskID, d.threadID, d.message = taskID, threadID, message
	d.calls++
	return nil
}

func (f fakeFactory) Start(_ context.Context, selected codexconfig.Config) (CodexSession, error) {
	if f.session != nil {
		f.session.selected = selected
	}
	return f.session, f.err
}

type selectionAwareFactory struct {
	fakeFactory
}

func (f selectionAwareFactory) StartWithSelection(_ context.Context, selected codexconfig.Config, model, reasoningEffort string) (CodexSession, error) {
	if f.session != nil {
		f.session.selected = selected
		f.session.model, f.session.reasoningEffort = model, reasoningEffort
	}
	return f.session, f.err
}

type operationAwareRecordingFactory struct {
	fakeFactory
	ceilings *[]codex.AuthorizationCeiling
}

type runtimeSelectionRecordingFactory struct {
	fakeFactory
	model, effort, tier string
	ceiling             codex.AuthorizationCeiling
}

func (f *runtimeSelectionRecordingFactory) StartForRuntimeSelection(_ context.Context, selected codexconfig.Config, model, effort, tier string, ceiling codex.AuthorizationCeiling) (CodexSession, error) {
	f.model, f.effort, f.tier, f.ceiling = model, effort, tier, ceiling
	if f.session != nil {
		f.session.selected = selected
		f.session.model, f.session.reasoningEffort = model, effort
	}
	return f.session, f.err
}

func (f operationAwareRecordingFactory) StartForOperation(_ context.Context, selected codexconfig.Config, model, reasoningEffort string, ceiling codex.AuthorizationCeiling) (CodexSession, error) {
	*f.ceilings = append(*f.ceilings, ceiling)
	if f.session != nil {
		f.session.selected = selected
		f.session.model, f.session.reasoningEffort = model, reasoningEffort
	}
	return f.session, f.err
}

type fakeSession struct {
	threadErr, turnErr, closeErr error
	cwd, prompt, thread          string
	threadName                   string
	threadNameCalls              int
	threadNameErr                error
	closed                       int
	usage                        *run.TokenUsageObservation
	usageSequence                []*run.TokenUsageObservation
	turn                         int
	startCalls                   int
	resumeCalls                  int
	resumeBaseline               *run.TokenCounts
	selected                     codexconfig.Config
	model, reasoningEffort       string
	prompts                      []string
	commands                     []ObservedCommand
	message                      *string
	outputSchema                 json.RawMessage
}

type upperReviewCapture struct {
	target review.UpperTarget
	calls  int
	err    error
}

func (u *upperReviewCapture) UpperReviewAccessForSession(_ context.Context, sessionID, reviewID string) (review.UpperAccess, error) {
	if sessionID == "" || reviewID == "" {
		return review.UpperAccess{}, review.ErrNotFound
	}
	return review.UpperAccess{Target: review.UpperTarget{AISessionID: sessionID, FeatureAreaID: "area-1", TaskID: "task-1", ReviewID: reviewID, RunID: "upper-run", RuntimeEpoch: "epoch-1", GrantGeneration: 1}, State: "PREPARING"}, nil
}

func (u *upperReviewCapture) Prepare(_ context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	u.calls++
	u.target = target
	return review.UpperAccess{Target: target, State: "ACTIVE"}, u.err
}

func TestStartUpperReviewPreparesExistingWorkspaceWithoutCodex(t *testing.T) {
	coordinator, runs, _, trees, session, _ := successfulCoordinator()
	base := strings.Repeat("a", 40)
	value := review.Review{ID: "upper-review", ExecutorKind: review.ExecutorUpperLLM, TaskID: "task-1", ProjectID: "project-1", FeatureAreaID: "area-1", CreatedByAISessionID: "session-1", RepositoryID: "repo-1", BaseCommit: base}
	reserved := run.Run{ID: "upper-run", ReviewID: value.ID, TaskID: value.TaskID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, OperationKind: run.OperationReview, OperationID: value.ID, Purpose: run.PurposeScopeReview, Status: run.StatusCreated, RuntimeEpoch: "epoch-1"}
	runs.store = run.NewMemoryStore(reserved)
	runs.service = run.NewService(runs.store, nil, nil)
	runs.sequence = nil
	upper := &upperReviewCapture{err: errors.New("temporary prepare failure")}
	coordinator.UpperReviews = upper
	if _, err := coordinator.StartUpperReview(context.Background(), value, reserved); err == nil {
		t.Fatal("preparation failure was accepted")
	}
	started, err := runs.Get(context.Background(), reserved.ID)
	if err != nil || started.Status != run.StatusRunning || started.WorkspaceID == "" || started.WorktreeID != trees.value.ID {
		t.Fatalf("recoverable upper start=%+v err=%v", started, err)
	}
	if upper.calls != 1 || upper.target.RunID != reserved.ID || upper.target.RuntimeEpoch != "epoch-1" {
		t.Fatalf("upper prepare=%+v calls=%d", upper.target, upper.calls)
	}
	if session.startCalls != 0 || session.turn != 0 {
		t.Fatalf("Upper Review invoked Codex: starts=%d turns=%d", session.startCalls, session.turn)
	}
	if !reflect.DeepEqual(runs.sequence, []run.Status{run.StatusPreparing, run.StatusRunning}) {
		t.Fatalf("upper state sequence=%v", runs.sequence)
	}
	upper.err = nil
	if _, err = coordinator.StartUpperReview(context.Background(), value, started); err != nil || upper.calls != 2 || trees.creates != 1 {
		t.Fatalf("upper replay start err=%v calls=%d worktrees=%d", err, upper.calls, trees.creates)
	}
}

type multiReviewRuns struct {
	*recordingRuns
	bindings map[string]run.RepositoryBinding
}

func (r *multiReviewRuns) ListRepositoryBindings(_ context.Context, runID string) ([]run.RepositoryBinding, error) {
	values := make([]run.RepositoryBinding, 0)
	for _, value := range r.bindings {
		if value.RunID == runID {
			values = append(values, value)
		}
	}
	return values, nil
}
func (r *multiReviewRuns) PutRepositoryBinding(_ context.Context, value run.RepositoryBinding) error {
	previous, ok := r.bindings[value.RepositoryID]
	if ok && (previous.MemberID != value.MemberID || previous.OriginCommit != value.OriginCommit || previous.WorkspaceID != value.WorkspaceID) {
		return review.ErrConflict
	}
	r.bindings[value.RepositoryID] = value
	return nil
}

type multiRepositories struct {
	values map[string]repository.Repository
}

func (r multiRepositories) Get(_ context.Context, id string) (repository.Repository, error) {
	value, ok := r.values[id]
	if !ok {
		return repository.Repository{}, repository.ErrNotFound
	}
	return value, nil
}

type multiWorktrees struct{ values []worktree.Worktree }

func (m *multiWorktrees) Create(_ context.Context, input worktree.CreateInput) (worktree.Worktree, error) {
	value := worktree.Worktree{ID: "wt-" + input.RepositoryID, RepositoryID: input.RepositoryID, RunID: input.RunID, Path: "/tmp/" + input.RepositoryID, BaseCommit: input.BaseCommit, Status: worktree.StatusReady}
	m.values = append(m.values, value)
	return value, nil
}

func TestStartUpperReviewPreparesEveryFrozenMemberWithoutRunScalarAuthority(t *testing.T) {
	baseA, baseS := strings.Repeat("a", 40), strings.Repeat("b", 40)
	store := run.NewMemoryStore(run.Run{ID: "upper-run", ReviewID: "upper-review", TaskID: "task-1", ProjectID: "project-1", FeatureAreaID: "area-1", OperationKind: run.OperationReview, OperationID: "upper-review", Purpose: run.PurposeScopeReview, Status: run.StatusCreated, RuntimeEpoch: "epoch-1"})
	baseRuns := &recordingRuns{store: store, service: run.NewService(store, nil, nil)}
	targets := []workset.Target{{RepositoryID: "app", OriginCommit: baseA, Read: true, Write: workset.WriteScope{Kind: "NONE"}}, {RepositoryID: "sdk", OriginCommit: baseS, Read: true, Write: workset.WriteScope{Kind: "NONE"}}}
	workspaces := workspace.NewService(workspace.NewMemoryStore(), nil, nil)
	planned, err := workspaces.PlanMembers(context.Background(), "project-1", "area-1", targets)
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]run.RepositoryBinding{}
	for _, target := range planned.Targets {
		bindings[target.RepositoryID] = run.RepositoryBinding{RunID: "upper-run", ReviewID: "upper-review", ProjectID: "project-1", FeatureAreaID: "area-1", WorkspaceID: planned.WorkspaceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, OriginCommit: target.OriginCommit, Preparation: "PENDING"}
	}
	runs := &multiReviewRuns{recordingRuns: baseRuns, bindings: bindings}
	trees := &multiWorktrees{}
	upper := &upperReviewCapture{}
	coordinator := &Coordinator{Repositories: multiRepositories{values: map[string]repository.Repository{"app": {ID: "app", ProjectID: "project-1", Path: "/source/app"}, "sdk": {ID: "sdk", ProjectID: "project-1", Path: "/source/sdk"}}}, Runs: runs, Workspaces: workspaces, Worktrees: trees, UpperReviews: upper}
	value := review.Review{ID: "upper-review", ExecutorKind: review.ExecutorUpperLLM, TaskID: "task-1", ProjectID: "project-1", FeatureAreaID: "area-1", CreatedByAISessionID: "session-1", WorkspaceID: planned.WorkspaceID, RepositoryTargets: planned.Targets}
	reserved, _ := runs.Get(context.Background(), "upper-run")
	started, err := coordinator.StartUpperReview(context.Background(), value, reserved)
	if err != nil {
		t.Fatal(err)
	}
	if started.WorkspaceID != "" || started.WorktreeID != "" || started.Status != run.StatusRunning || len(trees.values) != 2 || upper.calls != 1 {
		t.Fatalf("run=%+v worktrees=%+v calls=%d", started, trees.values, upper.calls)
	}
	for repo, binding := range runs.bindings {
		if binding.Preparation != "READY" || binding.WorktreeID == "" {
			t.Fatalf("binding %s=%+v", repo, binding)
		}
	}
}

func (s *fakeSession) SetThreadName(_ context.Context, threadID, name string) error {
	s.thread = threadID
	s.threadName = name
	s.threadNameCalls++
	return s.threadNameErr
}

func (s *fakeSession) StartThread(_ context.Context, cwd string) (string, error) {
	s.cwd = cwd
	s.startCalls++
	if s.thread == "" {
		s.thread = "thread-1"
	}
	return s.thread, s.threadErr
}
func (s *fakeSession) ResumeThread(_ context.Context, threadID, cwd string, baseline *run.TokenCounts) (string, error) {
	s.cwd = cwd
	s.thread = threadID
	s.resumeCalls++
	if baseline != nil {
		copy := *baseline
		s.resumeBaseline = &copy
	}
	return threadID, s.threadErr
}
func (s *fakeSession) StartTurn(_ context.Context, _ string, prompt string) (CodexTurnResult, error) {
	s.prompt = prompt
	s.prompts = append(s.prompts, prompt)
	s.turn++
	usage := s.usage
	if s.turn <= len(s.usageSequence) {
		usage = s.usageSequence[s.turn-1]
	}
	message := "agent says complete"
	if s.message != nil {
		message = *s.message
	}
	return CodexTurnResult{TurnID: fmt.Sprintf("turn-%d", s.turn), Message: message, Commands: append([]ObservedCommand(nil), s.commands...), TokenUsage: usage}, s.turnErr
}
func (s *fakeSession) StartTurnWithSchema(ctx context.Context, threadID, prompt string, schema json.RawMessage) (CodexTurnResult, error) {
	s.outputSchema = append(json.RawMessage(nil), schema...)
	result, err := s.StartTurn(ctx, threadID, prompt)
	if string(schema) == ImplementationReportJSONSchema && s.message == nil {
		result.Message = `{"resultDisposition":"COMPLETED","summary":"agent says complete"}`
	}
	return result, err
}

func TestScopeReviewRejectsLegacyTaskCarrierBeforeBackendDispatch(t *testing.T) {
	coordinator, runs, tasks, trees, session, _ := successfulCoordinator()
	tasks.value.Status = task.StatusPlanning
	reserved, err := runs.service.CreateScopeReview(context.Background(), tasks.value.ID, "inspect", run.ReviewScope{Kind: run.ReviewScopeRepository})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.Tasks = nil
	prepared, err := coordinator.PrepareScopeReview(context.Background(), reserved.ID)
	if err == nil || prepared.Run.Status != run.StatusInfrastructureFailed || prepared.Run.Failure == nil || prepared.Run.Failure.Code != "SCOPE_REVIEW_CONTRACT_INVALID" {
		t.Fatalf("legacy Review accepted: %+v %v", prepared.Run, err)
	}
	if trees.creates != 0 || session.startCalls != 0 || session.resumeCalls != 0 || tasks.value.Status != task.StatusPlanning {
		t.Fatal("legacy carrier dispatched runtime or changed Task")
	}
}

// Seed the same immutable owner snapshot the SQLite Review allocator returns.
func reserveCoordinatorReview(t *testing.T, c *Coordinator, runs *recordingRuns, instruction string) run.Run {
	t.Helper()
	config := codexconfig.Config{ID: "config-1", Enabled: true}
	owner := review.Review{ReviewDepth: "DEEP", ID: "review-direct", TaskID: "task-direct", ProjectID: "project-1", FeatureAreaID: "feature-direct", RepositoryID: "repo-1", CodexConfigID: "config-1", BaseCommit: strings.Repeat("a", 40), RunID: "run-direct-review", Instruction: instruction, Focus: review.Focus{Kind: review.FocusRepository}, BackendSelection: &codexconfig.BackendSelection{ConfigID: config.ID, ConfigFingerprint: config.Fingerprint()}, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}}
	c.RuntimeConfigProvider = codexruntime.Default
	value := run.Run{TaskID: owner.TaskID, ID: owner.RunID, ProjectID: owner.ProjectID, FeatureAreaID: owner.FeatureAreaID, ReviewID: owner.ID, OperationKind: run.OperationReview, OperationID: owner.ID, Purpose: run.PurposeScopeReview, Attempt: 1, Status: run.StatusCreated, CodexInstruction: instruction, ReviewScope: run.ReviewScope{Kind: run.ReviewScopeRepository}, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
	runs.store = run.NewMemoryStore(value)
	runs.service = run.NewService(runs.store, nil, nil)
	c.Reviews = &coordinatorReviewRuntime{value: owner}
	c.Tasks = nil
	// The real conversation binding atomically records the same backend Thread
	// on the source Run. Keep that coupling in the shared review reservation
	// fixture so callers cannot accidentally replace it with a context-only
	// conversation and bypass the continuity protocol.
	c.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation-direct", FeatureAreaID: owner.FeatureAreaID, ContinuationSafe: true}, bindRun: func(runID, threadID string) error {
		_, err := runs.AttachThread(context.Background(), runID, threadID)
		return err
	}}
	return value
}

func TestScopeReviewMutationIsNotPolicedAndDoesNotAuthorizeTask(t *testing.T) {
	coordinator, runs, tasks, trees, _, _ := successfulCoordinator()
	trees.value.Path = t.TempDir()
	trees.value.RepositoryID = "repo-1"
	tasks.value.Status = task.StatusPlanning
	tasks.value.ProblemStatement = "inspect safely"
	tasks.value.WorkSpec.ExecutionScope = task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"unchanged.txt"}}
	reserved := reserveCoordinatorReview(t, coordinator, runs, "inspect only")
	prepared, err := coordinator.PrepareScopeReview(context.Background(), reserved.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := coordinator.ExecuteRun(context.Background(), prepared, nil)
	if err != nil || completed.Status != run.StatusSucceeded || completed.Failure != nil {
		t.Fatalf("trusted review=%+v err=%v", completed, err)
	}
	if tasks.value.Status != task.StatusPlanning {
		t.Fatalf("Scope Review failure changed Task status to %s", tasks.value.Status)
	}
	if tasks.value.ConfirmedTaskPlanID != "" || !reflect.DeepEqual(tasks.value.WorkSpec.ExecutionScope.Paths, []string{"unchanged.txt"}) {
		t.Fatalf("Scope Review mutation authorized implementation or changed ExecutionScope: %+v", tasks.value)
	}
	workspaceValue, getErr := coordinator.Workspaces.(*workspace.Service).GetByFeatureArea(context.Background(), reserved.FeatureAreaID)
	if getErr != nil || workspaceValue.Status != workspace.StatusReady {
		t.Fatalf("review workspace=%+v err=%v", workspaceValue, getErr)
	}
}

func TestScopeReviewDoesNotClassifyObservedCommandText(t *testing.T) {
	for _, command := range []string{
		`rg -n -g '*.go' -g '!*_test.go' 'CreateReview|FinalCodexPrompt|PromptSHA256|ReviewEvidence|reviewId' .`,
		`rg -n needle -g '!test/**' -g '!tests/**' -g '*.spec.*' .`,
		"go test ./...",
	} {
		t.Run(command, func(t *testing.T) {
			coordinator, runs, tasks, trees, session, collector := successfulCoordinator()
			trees.value.Path = t.TempDir()
			trees.value.RepositoryID = "repo-1"
			collector.stable = true
			session.commands = []ObservedCommand{{Command: command}}
			tasks.value.Status = task.StatusPlanning
			tasks.value.WorkSpec = task.WorkSpec{}
			tasks.value.VerificationSpec = task.VerificationSpec{}
			reserved := reserveCoordinatorReview(t, coordinator, runs, "follow the Reviewer role contract")
			prepared, err := coordinator.PrepareScopeReview(context.Background(), reserved.ID)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := coordinator.ExecuteRun(context.Background(), prepared, nil)
			if err != nil || completed.Status != run.StatusSucceeded || tasks.value.Status != task.StatusPlanning {
				t.Fatalf("observed command text affected Review lifecycle: run=%+v task=%+v err=%v", completed, tasks.value, err)
			}
		})
	}
}

func TestScopeReviewDoesNotPoliceObservedWorkingDirectoryText(t *testing.T) {
	coordinator, runs, tasks, trees, session, collector := successfulCoordinator()
	trees.value.Path = t.TempDir()
	trees.value.RepositoryID = "repo-1"
	collector.stable = true
	session.commands = []ObservedCommand{{Command: "rg needle .", CWD: t.TempDir()}}
	tasks.value.Status = task.StatusPlanning
	reserved := reserveCoordinatorReview(t, coordinator, runs, "inspect only")
	prepared, err := coordinator.PrepareScopeReview(context.Background(), reserved.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := coordinator.ExecuteRun(context.Background(), prepared, nil)
	if err != nil || completed.Status != run.StatusSucceeded || completed.Failure != nil {
		t.Fatalf("observed CWD text affected trusted Review=%+v err=%v", completed, err)
	}
}

func TestProductReviewTrustsAgentResultWithoutCapabilityEvidence(t *testing.T) {
	for _, test := range []struct {
		name        string
		evidence    review.EvidenceSummary
		emptyResult bool
		runtimeFail bool
		wantStatus  run.Status
		wantCode    string
	}{
		{name: "zero evidence", evidence: review.EvidenceSummary{}, wantStatus: run.StatusSucceeded},
		{name: "capability failure is diagnostic only", evidence: review.EvidenceSummary{Successes: 1, Failures: 1}, wantStatus: run.StatusSucceeded},
		{name: "recorded forbidden capability is diagnostic only", evidence: review.EvidenceSummary{Successes: 1, Forbidden: 1}, wantStatus: run.StatusSucceeded},
		{name: "empty result", evidence: review.EvidenceSummary{Successes: 1}, emptyResult: true, wantStatus: run.StatusSucceeded},
		{name: "runtime failure", evidence: review.EvidenceSummary{Successes: 1}, runtimeFail: true, wantStatus: run.StatusInfrastructureFailed, wantCode: "REVIEW_RUNTIME_FAILED"},
		{name: "successful inspection", evidence: review.EvidenceSummary{Successes: 1}, wantStatus: run.StatusSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logOutput bytes.Buffer
			ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &logOutput}))
			coordinator, runs, tasks, trees, session, collector := successfulCoordinator()
			trees.value.Path = t.TempDir()
			trees.value.RepositoryID = "repo-1"
			collector.stable = true
			if test.emptyResult {
				empty := "  \n"
				session.message = &empty
			}
			if test.runtimeFail {
				session.turnErr = errors.New("Codex App Server failed")
			}
			tasks.value.Status, tasks.value.ProblemStatement = task.StatusPlanning, "Understand production ownership."
			tasks.value.WorkSpec, tasks.value.VerificationSpec = task.WorkSpec{}, task.VerificationSpec{}
			config := codexconfig.Config{ID: "config-1", Enabled: true}
			reviews := &coordinatorReviewRuntime{evidence: test.evidence, value: review.Review{ReviewDepth: "DEEP", ID: "review-product", ProjectID: "project-1", RepositoryID: "repo-1", CodexConfigID: "config-1", BackendSelection: backendSelectionPtr(config.Selection("execution-unit-test")), RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, BaseCommit: strings.Repeat("a", 40), Model: "gpt-5.6-terra", ReasoningEffort: "high"}}
			coordinator.RuntimeConfigProvider = codexruntime.Default
			coordinator.Reviews = reviews
			coordinator.Tasks = nil
			reserved, err := runs.service.CreateFocusedScopeReview(ctx, tasks.value.ID, "Understand production ownership.", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeFocused, InitialScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/a"}}, SelectedModules: []run.ReviewModuleSelection{{Key: "a", Role: run.ReviewModulePrimary}}})
			if err != nil {
				t.Fatal(err)
			}
			reviews.value.Instruction = "Understand production ownership."
			reviews.value.RunID = reserved.ID
			reviews.value.TaskID = tasks.value.ID
			reserved.ReviewID, reserved.OperationID, reserved.OperationKind = "review-product", "review-product", run.OperationReview
			reviews.value.FeatureAreaID = "feature-one"
			reserved.ProjectID, reserved.FeatureAreaID = "project-1", "feature-one"
			runs.store = run.NewMemoryStore(reserved)
			runs.service = run.NewService(runs.store, nil, nil)
			coordinator.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "review-conversation", FeatureAreaID: "feature-one", ContinuationSafe: true}}
			prepared, err := coordinator.PrepareScopeReview(ctx, reserved.ID)
			if err != nil {
				t.Fatal(err)
			}
			completed, runErr := executeDirectFeatureAreaRun(ctx, coordinator, prepared)
			if completed.Status != test.wantStatus {
				t.Fatalf("status=%s want=%s run=%+v err=%v", completed.Status, test.wantStatus, completed, runErr)
			}
			if test.wantCode != "" {
				if runErr == nil || completed.Failure == nil || completed.Failure.Code != test.wantCode {
					t.Fatalf("failure=%+v err=%v, want %s", completed.Failure, runErr, test.wantCode)
				}
				if !test.emptyResult && !test.runtimeFail && (completed.Result == nil || strings.TrimSpace(completed.Result.AgentClaim.Message) == "") {
					t.Fatalf("failed Review did not retain its bounded AgentClaim: %+v", completed.Result)
				}
			} else if runErr != nil || completed.Result == nil || completed.Result.AgentClaim.Message == "" {
				t.Fatalf("successful Review=%+v err=%v", completed, runErr)
			}
			if reviews.prompt == "" || reviews.sentAt.IsZero() {
				t.Fatal("exact product Review prompt was not persisted before the turn")
			}
			workspaceValue, workspaceErr := coordinator.Workspaces.(*workspace.Service).GetByFeatureArea(ctx, reviews.value.FeatureAreaID)
			if workspaceErr != nil || workspaceValue.LegacyThreadID != "" || workspaceValue.FeatureAreaID != reviews.value.FeatureAreaID || session.startCalls != 1 || session.resumeCalls != 0 {
				t.Fatalf("product Review did not activate its tool-declared Workspace thread: workspace=%+v run=%+v starts=%d resumes=%d err=%v", workspaceValue, completed, session.startCalls, session.resumeCalls, workspaceErr)
			}
			if reviews.prompt != "Understand production ownership." || session.prompt != reviews.prompt {
				t.Fatalf("product Review prompt changed: persisted=%q backend=%q", reviews.prompt, session.prompt)
			}
			if bytes.Contains(logOutput.Bytes(), []byte(reviews.prompt)) {
				t.Fatalf("exact Review prompt leaked to operational logs: %s", logOutput.Bytes())
			}
			sum := sha256.Sum256([]byte(reviews.prompt))
			if !bytes.Contains(logOutput.Bytes(), []byte(hex.EncodeToString(sum[:]))) || !bytes.Contains(logOutput.Bytes(), []byte(fmt.Sprintf(`"bytes":%d`, len(reviews.prompt)))) {
				t.Fatalf("Review prompt fingerprint missing from logs: %s", logOutput.Bytes())
			}
		})
	}
}

func TestBoundedReviewResultPreservesUTF8Boundary(t *testing.T) {
	value := strings.Repeat("a", (64<<10)-1) + "界" + "tail"
	bounded := boundedReviewResult(value, 64<<10)
	if len(bounded) > 64<<10 || !strings.HasSuffix(bounded, "a") || strings.Contains(bounded, "tail") {
		t.Fatalf("bounded Review result bytes=%d suffix=%q", len(bounded), bounded[len(bounded)-4:])
	}
}

func TestProductReviewForwardsFrozenReviewSelectionToCodexStartup(t *testing.T) {
	ctx := context.Background()
	coordinator, runs, tasks, trees, session, collector := successfulCoordinator()
	trees.value.Path = t.TempDir()
	trees.value.RepositoryID = "repo-1"
	collector.stable = true
	tasks.value.Status, tasks.value.ProblemStatement = task.StatusPlanning, "Understand production ownership."
	tasks.value.WorkSpec, tasks.value.VerificationSpec = task.WorkSpec{}, task.VerificationSpec{}
	config := codexconfig.Config{ID: "config-1", Enabled: true}
	reviews := &coordinatorReviewRuntime{value: review.Review{ReviewDepth: "DEEP", ID: "review-product", ProjectID: "project-1", RepositoryID: "repo-1", CodexConfigID: "config-1", BackendSelection: backendSelectionPtr(config.Selection("execution-unit-test")), RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-sol", ReasoningEffort: "xhigh"}, BaseCommit: strings.Repeat("a", 40), Model: "gpt-5.6-sol", ReasoningEffort: "xhigh"}}
	coordinator.RuntimeConfigProvider = codexruntime.Default
	coordinator.Reviews = reviews
	coordinator.Tasks = nil
	coordinator.Codex = selectionAwareFactory{fakeFactory: fakeFactory{session: session}}
	reserved, err := runs.service.CreateFocusedScopeReview(ctx, tasks.value.ID, "Understand production ownership.", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeFocused, InitialScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/a"}}, SelectedModules: []run.ReviewModuleSelection{{Key: "a", Role: run.ReviewModulePrimary}}})
	if err != nil {
		t.Fatal(err)
	}
	reviews.value.Instruction = "Understand production ownership."
	reviews.value.RunID = reserved.ID
	reviews.value.TaskID = tasks.value.ID
	reserved.ReviewID, reserved.OperationID, reserved.OperationKind = "review-product", "review-product", run.OperationReview
	reviews.value.FeatureAreaID = "feature-one"
	reserved.ProjectID, reserved.FeatureAreaID = "project-1", "feature-one"
	runs.store = run.NewMemoryStore(reserved)
	runs.service = run.NewService(runs.store, nil, nil)
	coordinator.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "review-conversation", FeatureAreaID: "feature-one", ContinuationSafe: true}}
	prepared, err := coordinator.PrepareScopeReview(ctx, reserved.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, runErr := executeDirectFeatureAreaRun(ctx, coordinator, prepared)
	if completed.Status != run.StatusSucceeded {
		t.Fatalf("review=%+v err=%v", completed, runErr)
	}
	if session.model != "gpt-5.6-sol" || session.reasoningEffort != "xhigh" {
		t.Fatalf("frozen Review selection did not reach Codex startup: model=%q effort=%q", session.model, session.reasoningEffort)
	}
	if session.prompt != "Understand production ownership." {
		t.Fatalf("Review prompt changed: %q", session.prompt)
	}
}

func (s *fakeSession) Close() error { s.closed++; return s.closeErr }

func successfulCoordinator() (*Coordinator, *recordingRuns, *fakeTasks, *fakeWorktrees, *fakeSession, *fakeCollector) {
	base := strings.Repeat("a", 40)
	runs := newRecordingRuns()
	tasks := &fakeTasks{value: task.Task{ID: "task-1", ProjectID: "project-1", FeatureAreaID: "area-1", ModuleID: "module-1", RepositoryID: "repo-1", CodexConfigID: "config-1", Goal: "make change", Constraints: []string{"stay scoped"}, WorkSpec: task.WorkSpec{Goal: "make change", Constraints: []string{"stay scoped"}, CodexInstruction: "make change", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}, BaseCommit: base, Status: task.StatusActive, Type: task.TypeDevelopment, Phase: task.PhaseImplementing}}
	trees := &fakeWorktrees{value: worktree.Worktree{ID: "wt-1", Path: "/bridge/worktrees/run-1", BaseCommit: base, Status: worktree.StatusReady}}
	session := &fakeSession{}
	collector := &fakeCollector{value: evidence.GitEvidence{BaseCommit: base, HeadCommit: base, Dirty: true, ChangedFiles: []string{"new.txt"}, Diff: "diff"}}
	workspaces := workspace.NewService(workspace.NewMemoryStore(), func() (string, error) { return "workspace-1", nil }, nil)
	coordinator := &Coordinator{Repositories: fakeRepositories{value: repository.Repository{ID: "repo-1", ProjectID: "project-1", Path: "/source/repo"}, head: base}, CodexConfigs: fakeCodexConfigs{value: codexconfig.Config{ID: "config-1", Enabled: true}}, Tasks: tasks, Runs: runs, Worktrees: trees, Workspaces: workspaces, Codex: fakeFactory{session: session}, Evidence: collector}
	return coordinator, runs, tasks, trees, session, collector
}

// executeDirectFeatureAreaRun is deliberately test-only. Production direct
// Feature Area work must arrive through an Execution reservation, which owns
// the immutable backend selection. Older unit tests exercise the coordinator
// below that boundary, so they supply equivalent frozen evidence before they
// create a conversation.
func executeDirectFeatureAreaRun(ctx context.Context, c *Coordinator, prepared PreparedRun) (run.Run, error) {
	selection := c.configSelectionForTest()
	prepared.backendSelection = &selection
	if runtime, ok := c.Conversations.(interface {
		bindDirectFixtureRun(RunService, string, codexconfig.BackendSelection)
	}); ok {
		runtime.bindDirectFixtureRun(c.Runs, prepared.Run.ID, selection)
	}
	return c.ExecuteRun(ctx, prepared, nil)
}

func (c *Coordinator) configSelectionForTest() codexconfig.BackendSelection {
	return codexconfig.Config{ID: "config-1", Enabled: true}.Selection("execution-unit-test")
}

func backendSelectionPtr(value codexconfig.BackendSelection) *codexconfig.BackendSelection {
	return &value
}

func TestCoordinatorSuccessfulProgressionAndBoundary(t *testing.T) {
	coordinator, runs, tasks, trees, session, collector := successfulCoordinator()
	value, err := coordinator.StartRun(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []run.Status{run.StatusCreated, run.StatusPreparing, run.StatusRunning, run.StatusCollecting, run.StatusSucceeded}
	if !reflect.DeepEqual(runs.sequence, want) {
		t.Fatalf("sequence=%v want=%v", runs.sequence, want)
	}
	if value.Status != run.StatusSucceeded || value.Result == nil || value.Result.AgentClaim.Message != "agent says complete" {
		t.Fatalf("run=%+v", value)
	}
	if tasks.value.Status != task.StatusActive || tasks.value.Phase != task.PhaseImplementing {
		t.Fatalf("successful execution wrote Task lifecycle: status=%s phase=%s", tasks.value.Status, tasks.value.Phase)
	}
	if session.cwd != trees.value.Path || session.cwd == "/source/repo" {
		t.Fatalf("Codex cwd=%q", session.cwd)
	}
	if collector.path != trees.value.Path || collector.base != tasks.value.BaseCommit {
		t.Fatalf("collector path=%q base=%q", collector.path, collector.base)
	}
	if session.prompt != tasks.value.WorkSpec.CodexInstruction {
		t.Fatalf("implementation prompt changed: got %q want %q", session.prompt, tasks.value.WorkSpec.CodexInstruction)
	}
	if session.closed != 1 {
		t.Fatalf("close count=%d", session.closed)
	}
	if session.threadName != executorThreadLabel(tasks.value.Title, run.PurposeImplementation) || session.threadNameCalls != 1 {
		t.Fatalf("thread naming name=%q calls=%d", session.threadName, session.threadNameCalls)
	}
	if session.selected.ID != tasks.value.CodexConfigID {
		t.Fatalf("selected config=%+v", session.selected)
	}
}

func TestCoordinatorForwardsFrozenTaskSelectionToCodexStartup(t *testing.T) {
	coordinator, _, tasks, trees, session, collector := successfulCoordinator()
	tasks.value.Model, tasks.value.ReasoningEffort = "gpt-5.6-terra", "high"
	trees.value.Path = t.TempDir()
	collector.stable = true
	coordinator.Codex = selectionAwareFactory{fakeFactory: fakeFactory{session: session}}
	value, err := coordinator.StartRun(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != run.StatusSucceeded {
		t.Fatalf("run=%+v", value)
	}
	if session.model != "gpt-5.6-terra" || session.reasoningEffort != "high" {
		t.Fatalf("frozen selection did not reach Codex startup: model=%q effort=%q", session.model, session.reasoningEffort)
	}
	if session.prompt != tasks.value.WorkSpec.CodexInstruction {
		t.Fatalf("prompt changed by execution metadata: %q", session.prompt)
	}
}

func TestCoordinatorBindsAuthorizationCeilingByOperation(t *testing.T) {
	t.Run("Execution", func(t *testing.T) {
		coordinator, _, _, trees, session, collector := successfulCoordinator()
		trees.value.Path = t.TempDir()
		collector.stable = true
		var ceilings []codex.AuthorizationCeiling
		coordinator.Codex = operationAwareRecordingFactory{fakeFactory: fakeFactory{session: session}, ceilings: &ceilings}
		value, err := coordinator.StartRun(context.Background(), "task-1")
		if err != nil || value.Status != run.StatusSucceeded {
			t.Fatalf("Run=%+v err=%v", value, err)
		}
		if !reflect.DeepEqual(ceilings, []codex.AuthorizationCeiling{codex.AuthorizationWorkspaceWrite}) {
			t.Fatalf("ceilings=%v", ceilings)
		}
	})

	t.Run("Review", func(t *testing.T) {
		coordinator, runs, _, trees, session, _ := successfulCoordinator()
		trees.value.Path = t.TempDir()
		trees.value.RepositoryID = "repo-1"
		var ceilings []codex.AuthorizationCeiling
		coordinator.Codex = operationAwareRecordingFactory{fakeFactory: fakeFactory{session: session}, ceilings: &ceilings}
		coordinator.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "review-conversation", FeatureAreaID: "feature-direct", ContinuationSafe: true}, bindRun: func(runID, threadID string) error {
			_, err := runs.AttachThread(context.Background(), runID, threadID)
			return err
		}}
		reserved := reserveCoordinatorReview(t, coordinator, runs, "inspect only")
		prepared, err := coordinator.PrepareScopeReview(context.Background(), reserved.ID)
		if err != nil {
			t.Fatal(err)
		}
		value, err := coordinator.ExecuteRun(context.Background(), prepared, nil)
		if err != nil || value.Status != run.StatusSucceeded {
			t.Fatalf("Run=%+v err=%v", value, err)
		}
		if !reflect.DeepEqual(ceilings, []codex.AuthorizationCeiling{codex.AuthorizationReadOnly}) {
			t.Fatalf("ceilings=%v", ceilings)
		}
	})
}

func TestCoordinatorAllowlistChangeDoesNotRewriteFrozenTaskSelection(t *testing.T) {
	coordinator, _, tasks, trees, session, collector := successfulCoordinator()
	tasks.value.Model, tasks.value.ReasoningEffort = "gpt-5.6-sol", "xhigh"
	authority, err := codexruntime.NewAuthority(codexruntime.Allowlist{Models: []string{"gpt-5.6-sol"}, ReasoningEfforts: []string{"xhigh"}})
	if err != nil {
		t.Fatal(err)
	}
	before := authority.Snapshot()
	if _, err := authority.Apply(before.Revision, codexruntime.Allowlist{Models: []string{"gpt-6-astra"}, ReasoningEfforts: []string{"medium"}}); err != nil {
		t.Fatal(err)
	}
	trees.value.Path = t.TempDir()
	collector.stable = true
	modelBefore, effortBefore := tasks.value.Model, tasks.value.ReasoningEffort
	coordinator.Codex = selectionAwareFactory{fakeFactory: fakeFactory{session: session}}
	value, err := coordinator.StartRun(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != run.StatusSucceeded {
		t.Fatalf("run=%+v", value)
	}
	if tasks.value.Model != modelBefore || tasks.value.ReasoningEffort != effortBefore {
		t.Fatalf("frozen selection mutated: %q/%q", tasks.value.Model, tasks.value.ReasoningEffort)
	}
	if session.model != modelBefore || session.reasoningEffort != effortBefore {
		t.Fatalf("updated allowlist changed admitted session: %q/%q", session.model, session.reasoningEffort)
	}
}

func TestCoordinatorThreadNamingFailureIsDiagnosticOnly(t *testing.T) {
	coordinator, _, tasks, _, session, _ := successfulCoordinator()
	diagnostics := &recordingDiagnostics{}
	coordinator.Diagnostics = diagnostics
	session.threadNameErr = errors.New("name API unavailable")
	value, err := coordinator.StartRun(context.Background(), tasks.value.ID)
	if err != nil || value.Status != run.StatusSucceeded {
		t.Fatalf("run=%+v err=%v", value, err)
	}
	if diagnostics.calls != 1 || diagnostics.taskID != tasks.value.ID || diagnostics.threadID != value.ThreadID || !strings.Contains(diagnostics.message, "name API unavailable") {
		t.Fatalf("diagnostic=%+v", diagnostics)
	}
}

func TestCoordinatorFinalizesTurnTokenUsage(t *testing.T) {
	coordinator, _, _, _, session, _ := successfulCoordinator()
	session.usage = &run.TokenUsageObservation{Reliable: true, StartCumulative: run.TokenCounts{TotalTokens: 100}, EndCumulative: run.TokenCounts{InputTokens: 20, CachedInputTokens: 5, OutputTokens: 10, ReasoningOutputTokens: 4, TotalTokens: 130}}
	value, err := coordinator.StartRun(context.Background(), "task-1")
	if err != nil || value.TokenUsage.Status != run.TokenUsageAvailable || value.TokenUsage.Usage == nil || value.TokenUsage.Usage.TotalTokens != 30 {
		t.Fatalf("run=%+v err=%v", value, err)
	}
}

func TestCoordinatorReusesWorkspaceWorktreeAndThreadAcrossRuns(t *testing.T) {
	coordinator, runs, tasks, trees, session, _ := successfulCoordinator()
	coordinator.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "canonical", ContinuationSafe: true}}
	trees.value.Path = t.TempDir()
	trees.value.RepositoryID = "repo-1"
	nextRun := 0
	runs.service = run.NewService(run.NewMemoryStore(), func() (string, error) {
		nextRun++
		return fmt.Sprintf("run-%d", nextRun), nil
	}, nil)
	session.usageSequence = []*run.TokenUsageObservation{
		{Reliable: true, StartCumulative: run.TokenCounts{}, EndCumulative: run.TokenCounts{InputTokens: 80, OutputTokens: 20, TotalTokens: 100}},
		{Reliable: true, StartCumulative: run.TokenCounts{InputTokens: 80, OutputTokens: 20, TotalTokens: 100}, EndCumulative: run.TokenCounts{InputTokens: 120, OutputTokens: 30, TotalTokens: 150}},
	}

	firstPrepared, err := coordinator.PrepareRun(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := executeDirectFeatureAreaRun(context.Background(), coordinator, firstPrepared)
	if err != nil {
		t.Fatal(err)
	}
	tasks.value.Status = task.StatusActive
	reserved, err := runs.Create(context.Background(), "task-1")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := coordinator.PrepareReservedRun(context.Background(), reserved.ID, Instruction{CodexInstruction: "continue the same task"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := executeDirectFeatureAreaRun(context.Background(), coordinator, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.WorkspaceID == "" || first.WorkspaceID != second.WorkspaceID {
		t.Fatalf("workspace continuity: first=%+v second=%+v", first, second)
	}
	if first.WorktreeID == "" || first.WorktreeID != second.WorktreeID || trees.creates != 1 {
		t.Fatalf("worktree continuity: first=%+v second=%+v creates=%d", first, second, trees.creates)
	}
	if first.ThreadID == "" || first.ThreadID != second.ThreadID || first.TurnID == "" || first.TurnID == second.TurnID || session.startCalls != 1 || session.resumeCalls != 1 {
		t.Fatalf("Codex continuity: first=%+v second=%+v starts=%d resumes=%d", first, second, session.startCalls, session.resumeCalls)
	}
	if session.threadNameCalls != 1 {
		t.Fatalf("thread name was reset on resume: calls=%d", session.threadNameCalls)
	}
	if session.resumeBaseline == nil || session.resumeBaseline.TotalTokens != 100 {
		t.Fatalf("resume baseline=%+v", session.resumeBaseline)
	}
	if first.TokenUsage.Usage == nil || first.TokenUsage.Usage.TotalTokens != 100 || second.TokenUsage.Usage == nil || second.TokenUsage.Usage.TotalTokens != 50 {
		t.Fatalf("token accounting: first=%+v second=%+v", first.TokenUsage, second.TokenUsage)
	}
}

func TestCoordinatorApprovalPersistenceFailureDeclinesAndFailsClosed(t *testing.T) {
	coordinator, _, tasks, _, _, _ := successfulCoordinator()
	session := newPersistenceFailureSession()
	coordinator.Codex = sessionFactory{session: session}
	coordinator.Approvals = persistenceFailureApprovals{}
	value, err := coordinator.StartRun(context.Background(), "task-1")
	var infrastructure *InfrastructureError
	if !errors.As(err, &infrastructure) {
		t.Fatalf("err=%v", err)
	}
	if value.Status != run.StatusInfrastructureFailed || value.Failure == nil || value.Failure.Code != "approval_persistence_failed" || value.FinishedAt == nil {
		t.Fatalf("run=%+v", value)
	}
	if tasks.value.Status != task.StatusActive || tasks.value.Phase != task.PhaseImplementing {
		t.Fatalf("infrastructure failure wrote Task lifecycle: status=%s phase=%s", tasks.value.Status, tasks.value.Phase)
	}
	select {
	case decision := <-session.decisions:
		if decision.Type != codex.DecisionDecline {
			t.Fatalf("unrecorded approval decision=%s", decision.Type)
		}
	default:
		t.Fatal("unrecorded approval was not explicitly declined")
	}
}

// A workspace carries no backend thread continuity without AgentConversation.
func TestCoordinatorUsesFreshThreadWithoutConversationOwner(t *testing.T) {
	coordinator, runs, _, _, session, _ := successfulCoordinator()
	trees := coordinator.Worktrees.(*fakeWorktrees)
	trees.value.Path = t.TempDir()
	coordinator.Executions = fakeExecutionLookup{}
	value, err := coordinator.StartRun(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if value.Status != run.StatusSucceeded {
		t.Fatalf("run=%+v", value)
	}
	if value.ThreadID == "" || session.startCalls != 1 || session.resumeCalls != 0 {
		t.Fatalf("legacy selector affected Codex context: run=%+v starts=%d resumes=%d", value, session.startCalls, session.resumeCalls)
	}
	if len(runs.sequence) == 0 {
		t.Fatal("durable Run lifecycle was not exercised before the continuation rejection")
	}
}

type fakeExecutionLookup struct{}

func (fakeExecutionLookup) Get(context.Context, string) (taskgoal.Execution, error) {
	return taskgoal.Execution{ID: "execution-1", TaskID: "task-1", ProjectID: "project-1"}, nil
}

func TestCodexLaunchConfigUsesTaskSelection(t *testing.T) {
	base := codex.Config{CodexPath: "/base/codex"}
	if got := codexLaunchConfig(base, codexconfig.Config{CodexPath: "/selected/codex"}); got.CodexPath != "/selected/codex" {
		t.Fatalf("selected path=%q", got.CodexPath)
	}
	if got := codexLaunchConfig(base, codexconfig.Config{}); got.CodexPath != base.CodexPath {
		t.Fatalf("fallback path=%q", got.CodexPath)
	}
}

func TestCoordinatorFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*Coordinator, *fakeWorktrees, *fakeSession, *fakeCollector)
		want      run.Status
		infra     bool
	}{{"worktree", func(_ *Coordinator, w *fakeWorktrees, _ *fakeSession, _ *fakeCollector) {
		w.err = errors.New("git add failed")
	}, run.StatusInfrastructureFailed, true}, {"codex infrastructure", func(_ *Coordinator, _ *fakeWorktrees, s *fakeSession, _ *fakeCollector) {
		s.turnErr = errors.New("server exited")
	}, run.StatusInfrastructureFailed, true}, {"task outcome", func(_ *Coordinator, _ *fakeWorktrees, s *fakeSession, _ *fakeCollector) {
		s.turnErr = &TaskExecutionError{Err: errors.New("turn failed")}
	}, run.StatusFailed, false}, {"cancellation", func(_ *Coordinator, _ *fakeWorktrees, s *fakeSession, _ *fakeCollector) { s.turnErr = context.Canceled }, run.StatusCanceled, false}, {"evidence", func(_ *Coordinator, _ *fakeWorktrees, _ *fakeSession, e *fakeCollector) {
		e.err = errors.New("diff failed")
	}, run.StatusInfrastructureFailed, true}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			coordinator, _, _, trees, session, collector := successfulCoordinator()
			test.configure(coordinator, trees, session, collector)
			value, err := coordinator.StartRun(context.Background(), "task-1")
			if err == nil {
				t.Fatal("expected error")
			}
			if value.Status != test.want || value.Failure == nil {
				t.Fatalf("run=%+v err=%v", value, err)
			}
			var infra *InfrastructureError
			if errors.As(err, &infra) != test.infra {
				t.Fatalf("infra=%v err=%T %v", test.infra, err, err)
			}
		})
	}
}

func TestNativeInterruptionRequiresExactTerminalIdentity(t *testing.T) {
	for _, name := range []string{"interrupted", "foreign-thread", "foreign-turn", "failed"} {
		t.Run(name, func(t *testing.T) {
			coordinator, _, _, _, session, _ := successfulCoordinator()
			native := &codex.TurnError{ThreadID: "thread-1", TurnID: "turn-1", Status: "interrupted"}
			switch name {
			case "foreign-thread":
				native.ThreadID = "other"
			case "foreign-turn":
				native.TurnID = "other"
			case "failed":
				native.Status = "failed"
			}
			session.turnErr = &TaskExecutionError{Err: native}
			value, err := coordinator.StartRun(context.Background(), "task-1")
			if err == nil || value.Failure == nil {
				t.Fatalf("missing failure: %+v %v", value, err)
			}
			if (value.Failure.Code == run.CodexTurnInterruptedCode) != (name == "interrupted") {
				t.Fatalf("interruption attribution: %+v", value.Failure)
			}
			if name == "interrupted" && value.Status != run.StatusInfrastructureFailed {
				t.Fatalf("interruption became engineering failure: %+v", value)
			}
		})
	}
}

func TestCoordinatorMakesWorkspaceReuseDecisionExplicit(t *testing.T) {
	for _, test := range []struct {
		name       string
		turnErr    error
		wantStatus workspace.Status
	}{
		{name: "task failure keeps valid lineage", turnErr: &TaskExecutionError{Err: errors.New("tests failed")}, wantStatus: workspace.StatusReady},
		{name: "correlated cancellation keeps valid lineage", turnErr: context.Canceled, wantStatus: workspace.StatusReady},
		{name: "transport failure invalidates uncertain lineage", turnErr: errors.New("app server exited"), wantStatus: workspace.StatusContinuityUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator, _, tasks, trees, session, _ := successfulCoordinator()
			trees.value.Path = t.TempDir()
			trees.value.RepositoryID = "repo-1"
			session.turnErr = test.turnErr
			_, _ = coordinator.StartRun(context.Background(), "task-1")
			workspaces := coordinator.Workspaces.(*workspace.Service)
			value, err := workspaces.GetByFeatureArea(context.Background(), tasks.value.FeatureAreaID)
			if err != nil {
				t.Fatal(err)
			}
			if value.Status != test.wantStatus {
				t.Fatalf("workspace status=%s want=%s", value.Status, test.wantStatus)
			}
		})
	}
}

func TestCoordinatorRepositoryLookupFailsBeforeRunCreation(t *testing.T) {
	coordinator, runs, _, _, _, _ := successfulCoordinator()
	coordinator.Repositories = fakeRepositories{err: errors.New("missing")}
	_, err := coordinator.StartRun(context.Background(), "task-1")
	if err == nil || len(runs.sequence) != 0 {
		t.Fatalf("err=%v sequence=%v", err, runs.sequence)
	}
}

func TestCoordinatorRejectsSourceAsManagedCWD(t *testing.T) {
	coordinator, _, _, trees, session, _ := successfulCoordinator()
	trees.value.Path = "/source/repo"
	value, err := coordinator.StartRun(context.Background(), "task-1")
	if err == nil || value.Status != run.StatusInfrastructureFailed {
		t.Fatalf("run=%+v err=%v", value, err)
	}
	if session.cwd != "" {
		t.Fatalf("Codex was started with %q", session.cwd)
	}
}

type persistenceFailureSession struct {
	requests  chan codex.ApprovalRequest
	decisions chan codex.ApprovalDecision
}

func newPersistenceFailureSession() *persistenceFailureSession {
	request := codex.ApprovalRequest{
		ID:               "approval-unrecorded",
		ThreadID:         "thread-1",
		TurnID:           "turn-1",
		ItemID:           "item-1",
		Kind:             codex.ApprovalKindCommandExecution,
		RequestPayload:   []byte(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1"}`),
		AllowedDecisions: []codex.DecisionType{codex.DecisionApproveOnce, codex.DecisionDecline},
	}
	requests := make(chan codex.ApprovalRequest, 1)
	requests <- request
	return &persistenceFailureSession{requests: requests, decisions: make(chan codex.ApprovalDecision, 1)}
}

func (s *persistenceFailureSession) StartThread(context.Context, string) (string, error) {
	return "thread-1", nil
}
func (s *persistenceFailureSession) StartTurn(ctx context.Context, _, _ string) (CodexTurnResult, error) {
	<-ctx.Done()
	return CodexTurnResult{TurnID: "turn-1"}, ctx.Err()
}
func (s *persistenceFailureSession) NextApproval(ctx context.Context) (codex.ApprovalRequest, error) {
	select {
	case request := <-s.requests:
		return request, nil
	case <-ctx.Done():
		return codex.ApprovalRequest{}, ctx.Err()
	}
}
func (s *persistenceFailureSession) ResolveApproval(_ context.Context, _ string, decision codex.ApprovalDecision) error {
	s.decisions <- decision
	return nil
}
func (s *persistenceFailureSession) Close() error { return nil }

type persistenceFailureApprovals struct{}

func (persistenceFailureApprovals) Register(string, approval.Resolver, context.CancelFunc) (func(), error) {
	return func() {}, nil
}
func (persistenceFailureApprovals) Request(context.Context, string, codex.ApprovalRequest) (approval.Approval, error) {
	return approval.Approval{}, errors.New("injected durable approval failure")
}
func (persistenceFailureApprovals) Wait(context.Context, string) error { return nil }

func (f *fakeTasks) GetDefinitionRevision(_ context.Context, id string) (task.DefinitionRevision, error) {
	if f.revision == nil || f.revision.ID != id {
		return task.DefinitionRevision{}, errors.New("revision missing")
	}
	return *f.revision, nil
}
func (r *recordingRuns) GetByInteraction(ctx context.Context, thread, turn string) (run.Run, error) {
	return r.service.GetByInteraction(ctx, thread, turn)
}

type executionSnapshotLookup struct{ value taskgoal.Execution }

func (s executionSnapshotLookup) Get(context.Context, string) (taskgoal.Execution, error) {
	return s.value, nil
}

func TestReservedExecutionDispatchUsesFrozenRuntimeConfigAndPrompt(t *testing.T) {
	for _, runtimeConfig := range []codexruntime.NextRunRuntimeConfig{
		{Model: "gpt-6-astra", ReasoningEffort: "ultra", CodexFastMode: true, ServiceTier: "priority"},
		{Model: "gpt-5.6-terra", ReasoningEffort: "xhigh", CodexFastMode: true, ServiceTier: "priority"},
		{Model: "gpt-5.6-luna", ReasoningEffort: "low", CodexFastMode: false},
		{Model: "gpt-5.6-sol", ReasoningEffort: "xhigh", CodexFastMode: false},
	} {
		t.Run(runtimeConfig.Model+"/"+runtimeConfig.ReasoningEffort, func(t *testing.T) {
			coordinator, runs, tasks, _, session, _ := successfulCoordinator()
			tasks.value.FeatureAreaID = "feature-exact"
			tasks.value.Status = task.StatusActive
			revisionID := "definition-exact"
			tasks.revision = &task.DefinitionRevision{ID: revisionID, TaskID: tasks.value.ID, Ordinal: 1, Definition: task.DefinitionFromTask(tasks.value, nil)}
			value := run.Run{ID: "run-exact", ProjectID: tasks.value.ProjectID, FeatureAreaID: tasks.value.FeatureAreaID, TaskID: tasks.value.ID, AgentExecutionID: "execution-exact", OperationKind: run.OperationExecution, OperationID: "execution-exact", ExecutionProposalID: "execution-exact", Purpose: run.PurposeImplementation, Status: run.StatusCreated, Attempt: 1, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
			proposal := taskgoal.Execution{ID: value.ExecutionProposalID, TaskID: tasks.value.ID, ProjectID: tasks.value.ProjectID, Status: "CONSUMED", Outcome: "RUNNING", RunID: value.ID, TaskDefinitionRevisionID: revisionID, RuntimeConfig: runtimeConfig}
			config := codexconfig.Config{ID: "config-1", Enabled: true}
			proposal.BackendSelection = &codexconfig.BackendSelection{ConfigID: config.ID, ConfigFingerprint: config.Fingerprint()}
			runs.store = run.NewMemoryStore(value)
			runs.service = run.NewService(runs.store, nil, nil)
			coordinator.Executions = executionSnapshotLookup{value: proposal}
			coordinator.RuntimeConfigProvider = codexruntime.Default
			coordinator.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "runtime-config-conversation", FeatureAreaID: tasks.value.FeatureAreaID, ContinuationSafe: true}, bindRun: func(runID, threadID string) error {
				_, err := runs.AttachThread(context.Background(), runID, threadID)
				return err
			}}
			factory := &runtimeSelectionRecordingFactory{fakeFactory: fakeFactory{session: session}}
			coordinator.Codex = factory
			prepared, err := coordinator.PrepareReservedRun(context.Background(), value.ID, Instruction{CodexInstruction: tasks.value.WorkSpec.CodexInstruction, ExecutionScope: tasks.value.WorkSpec.ExecutionScope})
			if err != nil {
				t.Fatal(err)
			}
			completed, err := coordinator.ExecuteRun(context.Background(), prepared, nil)
			if err != nil || completed.Status != run.StatusSucceeded {
				t.Fatalf("completed=%+v err=%v", completed, err)
			}
			if factory.model != runtimeConfig.Model || factory.effort != runtimeConfig.ReasoningEffort || factory.tier != runtimeConfig.ServiceTier || factory.ceiling != codex.AuthorizationWorkspaceWrite {
				t.Fatalf("runtime dispatch=%q/%q/%q/%q", factory.model, factory.effort, factory.tier, factory.ceiling)
			}
			if session.prompt != tasks.value.WorkSpec.CodexInstruction {
				t.Fatalf("prompt changed: got %q want %q", session.prompt, tasks.value.WorkSpec.CodexInstruction)
			}
			if sha256.Sum256([]byte(session.prompt)) != sha256.Sum256([]byte(tasks.revision.Definition.WorkSpec.CodexInstruction)) {
				t.Fatal("runtime configuration changed authored revision prompt SHA")
			}
		})
	}
}

func TestReservedExecutionRequiresConsumedExactOwnerBeforeDispatch(t *testing.T) {
	for _, condition := range []string{"pending", "confirmed", "completed", "wrong-run", "wrong-project", "wrong-feature"} {
		t.Run(condition, func(t *testing.T) {
			coordinator, runs, tasks, trees, session, _ := successfulCoordinator()
			tasks.value.FeatureAreaID = "feature-exact"
			tasks.value.Status = task.StatusActive
			value := run.Run{ID: "run-exact", ProjectID: tasks.value.ProjectID, FeatureAreaID: tasks.value.FeatureAreaID, TaskID: tasks.value.ID, OperationKind: run.OperationExecution, OperationID: "execution-exact", ExecutionProposalID: "execution-exact", Purpose: run.PurposeImplementation, Status: run.StatusCreated, Attempt: 1, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
			proposal := taskgoal.Execution{ID: value.ExecutionProposalID, TaskID: tasks.value.ID, ProjectID: tasks.value.ProjectID, Status: "CONSUMED", Outcome: "RUNNING", RunID: value.ID, TaskDefinitionRevisionID: "definition-exact"}
			switch condition {
			case "pending":
				proposal.Status = "PENDING_CONFIRMATION"
			case "confirmed":
				proposal.Status = "CONFIRMED"
			case "completed":
				proposal.Outcome = "SUCCEEDED"
			case "wrong-run":
				proposal.RunID = "other-run"
			case "wrong-project":
				proposal.ProjectID = "other-project"
			case "wrong-feature":
				value.FeatureAreaID = "other-feature"
			}
			runs.store = run.NewMemoryStore(value)
			runs.service = run.NewService(runs.store, nil, nil)
			coordinator.Executions = executionSnapshotLookup{value: proposal}
			prepared, err := coordinator.PrepareReservedRun(context.Background(), value.ID, Instruction{CodexInstruction: "implement"})
			if err == nil || prepared.Run.Failure == nil || prepared.Run.Failure.Code != "EXECUTION_CONTEXT_UNAVAILABLE" {
				t.Fatalf("unconsumed or mismatched owner accepted: %+v err=%v", prepared.Run, err)
			}
			if trees.creates != 0 || session.startCalls != 0 || tasks.value.Status != task.StatusActive {
				t.Fatal("invalid Execution dispatched or moved Task to RUNNING")
			}
		})
	}
}

func (s *persistenceFailureSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, schema json.RawMessage) (CodexTurnResult, error) {
	result, err := s.StartTurn(ctx, thread, prompt)
	if string(schema) == ImplementationReportJSONSchema {
		raw, _ := json.Marshal(ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}

// Directive §3: a terminal or legacy Status must be rejected through a typed
// error before any execution side effect: no Run is created or advanced, no
// workspace/worktree is touched, no runtime is dispatched, and the Task keeps
// its byte-identical lifecycle facts.
func TestCoordinatorRejectsTerminalAndLegacyStatusesBeforeAnySideEffect(t *testing.T) {
	for _, status := range []task.Status{task.StatusSucceeded, task.StatusFailed, task.StatusCanceled, task.StatusReady, task.StatusRunning, task.StatusPlanning} {
		t.Run(string(status), func(t *testing.T) {
			coordinator, runs, tasks, trees, session, collector := successfulCoordinator()
			tasks.value.Status = status
			before := tasks.value
			value, err := coordinator.StartRun(context.Background(), "task-1")
			var notExecutable *TaskNotExecutableError
			if !errors.As(err, &notExecutable) || value.ID != "" {
				t.Fatalf("StartRun admitted non-executable status: run=%+v err=%v", value, err)
			}
			if notExecutable.TaskID != "task-1" || notExecutable.Status != string(status) {
				t.Fatalf("typed rejection=%+v", notExecutable)
			}
			if !reflect.DeepEqual(before, tasks.value) {
				t.Fatalf("rejection mutated Task lifecycle: %+v", tasks.value)
			}
			if len(runs.sequence) != 0 {
				t.Fatalf("rejection created or advanced Runs: %v", runs.sequence)
			}
			if trees.creates != 0 || session.startCalls != 0 || collector.captures != 0 {
				t.Fatalf("rejection dispatched: worktrees=%d starts=%d captures=%d", trees.creates, session.startCalls, collector.captures)
			}
		})
	}
}

// The reserved-Run admission path rejects the same statuses before the Run
// leaves CREATED; only the typed preparation failure is persisted.
func TestReservedExecutionRejectsTerminalAndLegacyStatusesBeforePreparing(t *testing.T) {
	for _, status := range []task.Status{task.StatusSucceeded, task.StatusFailed, task.StatusCanceled} {
		t.Run(string(status), func(t *testing.T) {
			coordinator, runs, tasks, trees, session, _ := successfulCoordinator()
			tasks.value.Status = status
			before := tasks.value
			reserved, err := runs.Create(context.Background(), "task-1")
			if err != nil {
				t.Fatal(err)
			}
			runs.sequence = nil
			prepared, err := coordinator.PrepareReservedRun(context.Background(), reserved.ID, Instruction{CodexInstruction: "make change"})
			var notExecutable *TaskNotExecutableError
			if !errors.As(err, &notExecutable) {
				t.Fatalf("reserved Run admitted non-executable status: prepared=%+v err=%v", prepared, err)
			}
			for _, recorded := range runs.sequence {
				if recorded == run.StatusPreparing || recorded == run.StatusRunning {
					t.Fatalf("rejected reserved Run advanced: %v", runs.sequence)
				}
			}
			if prepared.Run.Status != run.StatusInfrastructureFailed || prepared.Run.Failure == nil || prepared.Run.Failure.Code != "task_not_executable" {
				t.Fatalf("typed preparation failure not persisted: %+v", prepared.Run)
			}
			if !reflect.DeepEqual(before, tasks.value) || trees.creates != 0 || session.startCalls != 0 {
				t.Fatalf("reserved rejection caused side effects: task=%+v worktrees=%d starts=%d", tasks.value, trees.creates, session.startCalls)
			}
		})
	}
}

func (r *recordingRuns) PrepareNaturalResult(ctx context.Context, id string) error {
	return r.service.PrepareNaturalResult(ctx, id)
}
func (r *recordingRuns) RecordExecutionEvidence(ctx context.Context, id string, value run.ExecutionEvidence) error {
	return r.service.RecordExecutionEvidence(ctx, id, value)
}
