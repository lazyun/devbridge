package controlapi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type integratedBaseResolver struct{ commit string }

func (r integratedBaseResolver) ResolveBase(context.Context, string, string) (string, error) {
	return r.commit, nil
}

type integratedWorktrees struct {
	root  string
	mu    sync.Mutex
	items map[string]worktree.Worktree
	count int
}

func (w *integratedWorktrees) Create(_ context.Context, input worktree.CreateInput) (worktree.Worktree, error) {
	ownerID := input.WorkspaceID
	if ownerID == "" {
		ownerID = input.RunID
	}
	path := filepath.Join(w.root, ownerID)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return worktree.Worktree{}, err
	}
	value := worktree.Worktree{ID: "worktree-" + ownerID, RepositoryID: input.RepositoryID, RepositoryPath: input.RepositoryPath, RunID: input.RunID, Path: path, Branch: "devbridge/" + input.FeatureAreaID + "/" + ownerID, BaseCommit: input.BaseCommit, Status: worktree.StatusReady}
	w.mu.Lock()
	w.count++
	w.items[value.ID] = value
	w.mu.Unlock()
	return value, nil
}

func (w *integratedWorktrees) Get(_ context.Context, id string) (worktree.Worktree, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	value, ok := w.items[id]
	if !ok {
		return worktree.Worktree{}, worktree.ErrNotFound
	}
	return value, nil
}

// The synthetic builder validates its own durable reload identity. Physical
// Git metadata validation belongs to the real worktree manager tests.
func (w *integratedWorktrees) Validate(ctx context.Context, value worktree.Worktree) error {
	stored, err := w.Get(ctx, value.ID)
	if err != nil {
		return err
	}
	if stored.Path != value.Path || stored.RepositoryID != value.RepositoryID || stored.BaseCommit != value.BaseCommit || stored.Branch != value.Branch {
		return worktree.ErrNotOwned
	}
	_, err = os.Stat(value.Path)
	return err
}

type integratedCollector struct{}

func (integratedCollector) CaptureSnapshot(_ context.Context, path string) (string, error) {
	if strings.Contains(path, "workspace") {
		return strings.Repeat("2", 40), nil
	}
	return strings.Repeat("1", 40), nil
}
func (integratedCollector) CollectRun(_ context.Context, path, base, start, end string) (evidence.GitEvidence, error) {
	return evidence.GitEvidence{BaseCommit: base, HeadCommit: base, StartSnapshot: start, EndSnapshot: end, ChangedFiles: []string{filepath.Join(path, "change.go")}}, nil
}

type integratedCodexSession struct {
	prompts      chan string
	mu           sync.Mutex
	turn         int
	starts       int
	resumes      int
	lastBaseline *run.TokenCounts
}

func (s *integratedCodexSession) StartThread(context.Context, string) (string, error) {
	s.mu.Lock()
	s.starts++
	s.mu.Unlock()
	return "thread-integrated", nil
}

func (s *integratedCodexSession) ResumeThread(_ context.Context, threadID, _ string, baseline *run.TokenCounts) (string, error) {
	s.mu.Lock()
	s.resumes++
	if baseline != nil {
		copy := *baseline
		s.lastBaseline = &copy
	}
	s.mu.Unlock()
	return threadID, nil
}

func (s *integratedCodexSession) StartTurn(ctx context.Context, _, prompt string) (execution.CodexTurnResult, error) {
	s.mu.Lock()
	s.turn++
	turnID := fmt.Sprintf("turn-integrated-%d", s.turn)
	s.mu.Unlock()
	s.prompts <- prompt
	<-ctx.Done()
	return execution.CodexTurnResult{TurnID: turnID}, ctx.Err()
}

func (*integratedCodexSession) Close() error { return nil }

type integratedCodexFactory struct{ session *integratedCodexSession }

func (f integratedCodexFactory) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	return f.session, nil
}

func TestPersistedPlanningTaskPlanFeedsAsyncRuntimeSupervisor(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 3, 8, 9, 10, 0, time.UTC)
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	projects := sqlite.NewProjectStore(db)
	projectValue := project.Project{ID: "project-integrated", Name: "Integrated", CreatedAt: now, UpdatedAt: now}
	if err := projects.Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	repositories := sqlite.NewRepositoryStore(db)
	repositoryValue := repository.Repository{ID: "repo-integrated", ProjectID: projectValue.ID, Name: "Repo", Path: filepath.Join(root, "source"), CreatedAt: now, UpdatedAt: now}
	if _, _, err := repositories.PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	modules := sqlite.NewModuleStore(db)
	moduleValue := module.Module{ID: "module-integrated", ProjectID: projectValue.ID, Key: "core", Name: "Core", CreatedAt: now, UpdatedAt: now}
	if err := modules.Create(ctx, moduleValue); err != nil {
		t.Fatal(err)
	}
	sessions := aisession.NewService(sqlite.NewAISessionStore(db), projects, func() (string, error) { return "session-integrated", nil }, func() time.Time { return now })
	ctx = aisession.WithCallerScope(ctx, "workflow-runtime-integration")
	ref := aiprojectselection.ClientBindingReference{CallerScope: "workflow-runtime-integration"}
	initialized, err := sessions.InitializeSession(ctx, ref, "workflow-runtime-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	session, _, _, err := sessions.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "workflow-runtime-bind")
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: "feature-integrated", ProjectID: projectValue.ID, CreatedByAISessionID: session.ID, Title: "Integrated ownership", ProblemStatement: "Execute exact plan", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	tasks := task.NewService(sqlite.NewTaskStore(db), integratedBaseResolver{commit: base}, modules, sqlite.NewCodexConfigStore(db), func() (string, error) { return "task-integrated", nil }, func() time.Time { return now })
	taskValue, err := tasks.Create(ctx, task.CreateInput{AISessionID: session.ID, RequestID: "integrated-task-create", ProjectID: projectValue.ID, FeatureAreaID: "feature-integrated", ModuleID: moduleValue.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID, Model: "gpt-5.6-luna", ReasoningEffort: "low", ProblemStatement: "Integrate runtime", BaseRevision: "main", Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	taskValue = workflowtest.Approve(t, db, taskValue, session.ID, task.WorkSpec{Goal: "Integrate runtime", Constraints: []string{"no new features"}, CodexInstruction: "Integrate runtime", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}, ImplementationMode: task.ImplementationCodingAgent}, task.VerificationSpec{Mode: task.VerificationModeNone})
	if values, err := sqlite.NewRunStore(db).List(ctx); err != nil || len(values) != 1 || values[0].Purpose != run.PurposeScopeReview {
		t.Fatalf("approval started execution: %+v %v", values, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if persisted, err := sqlite.NewAISessionStore(db).Get(ctx, session.ID); err != nil || persisted.ProjectID != projectValue.ID {
		t.Fatalf("persisted session=%+v err=%v", persisted, err)
	}
	repositories = sqlite.NewRepositoryStore(db)
	tasks = task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, nil, func() time.Time { return now })
	plans, err := taskplan.NewService(sqlite.NewTaskPlanStore(db), sqlite.NewAISessionStore(db), tasks, repositories, func() (string, error) { return "unused-plan", nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(sqlite.NewRunStore(db), nil, func() time.Time { return now })
	workspaces := workspace.NewService(sqlite.NewWorkspaceStore(db), nil, func() time.Time { return now })
	codexSession := &integratedCodexSession{prompts: make(chan string, 4)}
	worktreeManager := &integratedWorktrees{root: filepath.Join(root, "worktrees"), items: make(map[string]worktree.Worktree)}
	proposalStore := sqlite.NewExecutionProposalStore(db)
	coordinator := &execution.Coordinator{Repositories: repositories, CodexConfigs: sqlite.NewCodexConfigStore(db), Tasks: tasks, Executions: &taskgoal.ExecutionAdapter{Proposals: proposalStore}, Conversations: sqlite.NewAgentConversationStore(db), Runs: runs, Worktrees: &worktree.DurableManager{External: worktreeManager, Store: sqlite.NewWorktreeStore(db)}, Workspaces: workspaces, Codex: integratedCodexFactory{session: codexSession}, Evidence: integratedCollector{}, Handoffs: proposalStore, RuntimeConfigProvider: codexruntime.Default}
	supervisor, err := execution.NewRuntimeSupervisor(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Shutdown() })
	executionProposalNumber := 0
	runNumber := 0
	executionProposals, err := executionproposal.NewService(sqlite.NewExecutionProposalStore(db), sqlite.NewAISessionStore(db), plans, tasks, supervisor, func() (string, error) {
		executionProposalNumber++
		return "execution-proposal-" + string(rune('0'+executionProposalNumber)), nil
	}, func() (string, error) {
		runNumber++
		return "run-integrated-" + string(rune('0'+runNumber)), nil
	}, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatal(err)
	}
	api := &Facade{deps: Dependencies{AISessions: sqlite.NewAISessionStore(db), Tasks: tasks, TaskPlans: plans, ExecutionProposals: executionProposals, Runs: runs, Runner: supervisor, WorkflowProgression: sqlite.NewWorkflowProgressionStore(db)}}
	caller, cancelCaller := context.WithCancel(ctx)
	startResult, err := api.StartTaskExecution(caller, session.ID, taskValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	cancelCaller()
	startedRun := startResult.Run
	if startResult.Proposal.Status != executionproposal.StatusConsumed || startResult.Proposal.RunID != startedRun.ID {
		t.Fatalf("execution start=%+v", startResult)
	}
	if startedRun.TaskID != taskValue.ID || startedRun.Status != run.StatusPreparing {
		t.Fatalf("started run=%+v", startedRun)
	}
	select {
	case prompt := <-codexSession.prompts:
		if !strings.Contains(prompt, "Integrate runtime") {
			t.Fatalf("create prompt=%q", prompt)
		}
	case <-time.After(2 * time.Second):
		stored, readErr := runs.Get(ctx, startedRun.ID)
		t.Fatalf("async runtime did not accept the proposal-created task: status=%s failure=%+v read=%v", stored.Status, stored.Failure, readErr)
	}
	if _, ok := supervisor.GetSession(startedRun.ID); !ok {
		t.Fatal("caller cancellation ended the handed-off run")
	}
	cancelCtx, cancelRun := context.WithTimeout(ctx, 2*time.Second)
	defer cancelRun()
	if _, err := api.CancelRunForSession(cancelCtx, session.ID, startedRun.ID); err == nil || MapError(err).Code != CodeInvalidArgument {
		t.Fatalf("legacy cancellation bypassed the durable Execution operation: %v", err)
	}
	if _, ok := supervisor.GetSession(startedRun.ID); !ok {
		t.Fatal("rejected legacy cancellation ended the owned Execution session")
	}
	// This older planning fixture has no backend interrupt primitive. Join its
	// runtime through owner shutdown before reading final Thread/Turn facts;
	// formal exact cancellation has separate interrupt-capable fixtures.
	if err := supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
	firstRun, err := runs.Get(ctx, startedRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	workspaceValue, err := workspaces.GetByFeatureArea(ctx, taskValue.FeatureAreaID)
	if err != nil {
		t.Fatal(err)
	}
	if firstRun.WorkspaceID == "" || firstRun.WorkspaceID != workspaceValue.ID {
		t.Fatalf("run/workspace ownership: run=%+v workspace=%+v", firstRun, workspaceValue)
	}
	if firstRun.WorktreeID == "" || firstRun.WorktreeID != workspaceValue.WorktreeID || worktreeManager.count != 1 {
		t.Fatalf("worktree ownership: run=%+v creates=%d", firstRun, worktreeManager.count)
	}
	conversation, err := sqlite.NewAgentConversationStore(db).Current(ctx, taskValue.FeatureAreaID)
	if err != nil || firstRun.ThreadID == "" || firstRun.ThreadID != conversation.BackendThreadID || firstRun.TurnID == "" {
		t.Fatalf("thread/turn ownership: run=%+v workspace=%+v", firstRun, workspaceValue)
	}
	codexSession.mu.Lock()
	starts, resumes := codexSession.starts, codexSession.resumes
	codexSession.mu.Unlock()
	if starts != 1 || resumes != 0 {
		t.Fatalf("Codex lifecycle starts=%d resumes=%d", starts, resumes)
	}
}

func (s *integratedCodexSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, schema json.RawMessage) (execution.CodexTurnResult, error) {
	result, err := s.StartTurn(ctx, thread, prompt)
	if string(schema) == execution.ImplementationReportJSONSchema && err == nil {
		raw, _ := json.Marshal(execution.ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}
