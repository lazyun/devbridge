package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

var runtimeSupervisorTestEpoch = time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)

func deterministicRuntimeClock(start time.Time) func() time.Time {
	var mu sync.Mutex
	current := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(time.Second)
		return current
	}
}

type authorizedPlanSession struct {
	id        int
	started   chan struct{}
	release   chan struct{}
	threadID  string
	resumed   string
	panicTurn bool
}

func newAuthorizedPlanSession(id int) *authorizedPlanSession {
	return &authorizedPlanSession{id: id, started: make(chan struct{}), release: make(chan struct{})}
}

func (s *authorizedPlanSession) StartThread(context.Context, string) (string, error) {
	s.threadID = "thread-authorized-plan"
	return s.threadID, nil
}

func (s *authorizedPlanSession) ResumeThread(_ context.Context, threadID, _ string, _ *run.TokenCounts) (string, error) {
	s.resumed = threadID
	return threadID, nil
}

func (s *authorizedPlanSession) StartTurn(ctx context.Context, _ string, _ string) (exec.CodexTurnResult, error) {
	close(s.started)
	if s.panicTurn {
		panic("synthetic verification panic")
	}
	select {
	case <-s.release:
		result := exec.CodexTurnResult{TurnID: fmt.Sprintf("turn-authorized-plan-%d", s.id), Message: "synthetic success"}
		if s.id == 2 {
			zero := 0
			result.Commands = []exec.ObservedCommand{{Command: "go test ./...", ExitCode: &zero, Output: "ok"}}
		}
		return result, nil
	case <-ctx.Done():
		return exec.CodexTurnResult{}, ctx.Err()
	}
}

func (s *authorizedPlanSession) StartTurnWithSchema(ctx context.Context, threadID, prompt string, schema json.RawMessage) (exec.CodexTurnResult, error) {
	result, err := s.StartTurn(ctx, threadID, prompt)
	if err == nil && string(schema) == exec.ImplementationReportJSONSchema {
		result.Message = `{"resultDisposition":"COMPLETED","summary":"synthetic success"}`
	} else if err == nil {
		result.Message = `{"version":2,"summary":"verification facts recorded","checks":[{"index":0,"status":"PASSED","summary":"observed pass","evidence":"","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"criterion observed","evidence":"","reason":""}]}`
	}
	return result, err
}

func (*authorizedPlanSession) Close() error { return nil }

type authorizedPlanFactory struct {
	mu        sync.Mutex
	next      int
	created   chan *authorizedPlanSession
	configure func(*authorizedPlanSession)
	ceilings  []codex.AuthorizationCeiling
}

func (f *authorizedPlanFactory) StartForOperation(ctx context.Context, config codexconfig.Config, _, _ string, ceiling codex.AuthorizationCeiling) (exec.CodexSession, error) {
	f.mu.Lock()
	f.ceilings = append(f.ceilings, ceiling)
	f.mu.Unlock()
	return f.Start(ctx, config)
}

func (f *authorizedPlanFactory) Start(context.Context, codexconfig.Config) (exec.CodexSession, error) {
	f.mu.Lock()
	f.next++
	session := newAuthorizedPlanSession(f.next)
	if f.configure != nil {
		f.configure(session)
	}
	f.mu.Unlock()
	f.created <- session
	return session, nil
}

type authorizedPlanEvidence struct{}

func (authorizedPlanEvidence) CaptureSnapshot(context.Context, string) (string, error) {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

func (authorizedPlanEvidence) CollectRun(_ context.Context, _ string, base, start, end string) (evidence.GitEvidence, error) {
	return evidence.GitEvidence{BaseCommit: base, HeadCommit: base, StartSnapshot: start, EndSnapshot: end}, nil
}

func awaitAuthorizedPlanSession(t *testing.T, factory *authorizedPlanFactory) *authorizedPlanSession {
	t.Helper()
	select {
	case session := <-factory.created:
		select {
		case <-session.started:
			return session
		case <-time.After(2 * time.Second):
			t.Fatal("synthetic Codex turn did not start")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic Codex session was not created")
	}
	return nil
}

type authorizedPlanPanicFixture struct {
	ctx               context.Context
	coordinator       *exec.Coordinator
	root              string
	db                *sqlite.DB
	areaID            string
	taskValue         task.Task
	work              task.WorkSpec
	verification      task.VerificationSpec
	proposalValue     executionproposal.Proposal
	implementationRun run.Run
	proposals         *sqlite.ExecutionProposalStore
	runs              *run.Service
	factory           *authorizedPlanFactory
	supervisor        *exec.RuntimeSupervisor
	now               time.Time
}

func newAuthorizedPlanPanicFixture(t *testing.T, authored ...string) *authorizedPlanPanicFixture {
	t.Helper()
	ctx := aisession.WithCallerScope(context.Background(), "runtime-supervisor-test")
	root := t.TempDir()
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	now := runtimeSupervisorTestEpoch
	clock := deterministicRuntimeClock(now.Add(10 * time.Minute))
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	p := project.Project{ID: "project-panic-plan", Name: "panic plan", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	repo := repository.Repository{ID: "repository-panic-plan", ProjectID: p.ID, Name: "repository", Path: filepath.Join(root, "source"), CreatedAt: now, UpdatedAt: now}
	if _, _, err = sqlite.NewRepositoryStore(db).PutIfAbsent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "module-panic-plan", ProjectID: p.ID, Key: "runtime", Name: "Runtime", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewModuleStore(db).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	ai := aisession.Session{ID: "session-panic-plan", ProjectID: p.ID, CreatedAt: now, UpdatedAt: now}
	sessions := sqlite.NewAISessionStore(db)
	if _, err = sessions.InitializeSession(ctx, "runtime-supervisor-test", "initialize-panic-plan", ai.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = sessions.BindSessionProject(ctx, "runtime-supervisor-test", "bind-panic-plan", ai.ID, p.ID, "selection-panic-plan", now); err != nil {
		t.Fatal(err)
	}
	areaID := "feature-panic-plan"
	if err = sqlite.NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: areaID, ProjectID: p.ID, CreatedByAISessionID: ai.ID, Title: "Runtime panic safety", ProblemStatement: "Recover exact current Run", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	taskValue := task.Task{ID: "task-panic-plan", ProjectID: p.ID, FeatureAreaID: areaID, ModuleID: m.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, ProblemStatement: "Implement and verify", BaseCommit: base, Title: "Runtime safety", Status: task.StatusActive, Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewTaskStore(db).Create(ctx, taskValue); err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{Goal: "Implement panic safety", ImplementationMode: task.ImplementationCodingAgent, CodexInstruction: "Implement the confirmed change.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Go packages", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"tests pass"}, CodexInstruction: "Run the confirmed tests without modifying files."}
	if len(authored) > 0 {
		work.CodexInstruction, verification.CodexInstruction = authored[0], authored[0]
	}
	taskValue, proposalValue, implementationRun := approveAndReserveAuthorizedPlan(t, ctx, db, taskValue, ai.ID, work, verification)
	proposals := sqlite.NewExecutionProposalStore(db)
	var idMu sync.Mutex
	nextID := 0
	ids := func() (string, error) {
		idMu.Lock()
		defer idMu.Unlock()
		nextID++
		return fmt.Sprintf("panic-resource-%d", nextID), nil
	}
	runs := run.NewService(sqlite.NewRunStore(db), ids, clock)
	factory := &authorizedPlanFactory{created: make(chan *authorizedPlanSession, 2)}
	coordinator := &exec.Coordinator{Repositories: protocolRepositories{RepositoryStore: sqlite.NewRepositoryStore(db), base: base},
		CodexConfigs:          sqlite.NewCodexConfigStore(db),
		Tasks:                 task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, ids, clock),
		Executions:            &taskgoal.ExecutionAdapter{Proposals: sqlite.NewExecutionProposalStore(db)},
		Runs:                  runs,
		Worktrees:             &worktree.DurableManager{External: protocolTree{root: filepath.Join(root, "worktrees")}, Store: sqlite.NewWorktreeStore(db)},
		Workspaces:            workspace.NewService(sqlite.NewWorkspaceStore(db), ids, clock),
		Codex:                 factory,
		Evidence:              authorizedPlanEvidence{},
		Conversations:         sqlite.NewAgentConversationStore(db),
		Handoffs:              proposals,
		RuntimeConfigProvider: codexruntime.Default,
	}
	supervisor, err := exec.NewRuntimeSupervisor(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	return &authorizedPlanPanicFixture{ctx: ctx, coordinator: coordinator, root: root, db: db, areaID: areaID, taskValue: taskValue, work: work, verification: verification, proposalValue: proposalValue, implementationRun: implementationRun, proposals: proposals, runs: runs, factory: factory, supervisor: supervisor, now: now}
}

func (f *authorizedPlanPanicFixture) start(t *testing.T, complete func(context.Context, executionproposal.Outcome, string, string) error) run.Run {
	t.Helper()
	started, err := f.supervisor.StartAuthorizedPlan(f.ctx, f.taskValue.ID, f.work, f.verification,
		func(ctx context.Context) (run.Run, error) {
			return f.implementationRun, nil
		},
		func(ctx context.Context) (run.Run, error) {
			_, value, reserveErr := f.proposals.ReserveVerificationRun(ctx, f.proposalValue.ID, "run-panic-verification", f.now.Add(5*time.Second))
			return value, reserveErr
		}, complete)
	if err != nil {
		t.Fatal(err)
	}
	return started
}

// approveAndReserveAuthorizedPlan uses the same durable Review approval and
// START_AUTHORIZED reservation chain as production. workflowtest's older
// convenience helper uses an unscoped Background context, which is no longer
// admissible once the Session is initialized with a caller identity.
func approveAndReserveAuthorizedPlan(t *testing.T, ctx context.Context, db *sqlite.DB, value task.Task, session string, work task.WorkSpec, verification task.VerificationSpec) (task.Task, executionproposal.Proposal, run.Run) {
	t.Helper()
	now := runtimeSupervisorTestEpoch
	config, err := sqlite.NewCodexConfigStore(db).Get(ctx, value.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	reviewService, _ := review.NewService(sqlite.NewReviewStore(db), nil, func() time.Time { return now })
	reviewValue := review.Review{
		ReviewDepth: task.ReviewDepthDeep, TaskID: value.ID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID,
		CreatedByAISessionID: session, AuthorKind: string(review.AuthorAISession), AuthorID: session,
		RepositoryID: value.RepositoryID, CodexConfigID: value.CodexConfigID, BaseCommit: value.BaseCommit,
		BackendSelection: ptr(config.Selection("runtime-supervisor-test")), RuntimeConfig: runtimeConfig,
		Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: value.ProblemStatement,
		Instruction: "Synthetic read-only review", RepositoryProvenance: &review.RepositoryProvenance{RepositoryID: value.RepositoryID, Commit: value.BaseCommit, RepositoryHEAD: value.BaseCommit, BranchKind: review.ProvenanceBranch, BranchRef: "refs/heads/dev", InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: now},
	}
	admission, err := reviewService.AdmitStart(ctx, reviewValue, "review-"+value.ID, strings.Repeat("a", 64), reviewValue.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(review.ImplementationResult{ReviewDisposition: review.DispositionApprovalReady, Version: 1, Summary: "Synthetic approved intent", WorkSpec: work, VerificationSpec: verification})
	if err != nil {
		t.Fatal(err)
	}
	// Complete the prerequisite Review through the real coordinator with an
	// immediate fake backend response, never by seeding terminal Run status.
	fake := &authorizedPlanReviewFactory{session: &authorizedPlanReviewSession{report: string(raw)}}
	reviewRuns := run.NewService(sqlite.NewRunStore(db), nil, nil)
	reviewCoordinator := &exec.Coordinator{
		Repositories: protocolRepositories{RepositoryStore: sqlite.NewRepositoryStore(db), base: value.BaseCommit},
		CodexConfigs: sqlite.NewCodexConfigStore(db), Reviews: reviewService, Runs: reviewRuns,
		Worktrees:     &worktree.DurableManager{External: protocolTree{root: t.TempDir()}, Store: sqlite.NewWorktreeStore(db)},
		Workspaces:    workspace.NewService(sqlite.NewWorkspaceStore(db), nil, nil),
		Conversations: sqlite.NewAgentConversationStore(db), Codex: fake,
		Evidence: authorizedPlanEvidence{}, RuntimeConfigProvider: codexruntime.Default,
	}
	prepared, err := reviewCoordinator.PrepareScopeReview(ctx, admission.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := reviewCoordinator.ExecuteRun(ctx, prepared, nil)
	if err != nil || finished.Status != run.StatusSucceeded || finished.Result == nil || fake.session.turns != 1 || fake.ceiling != codex.AuthorizationReadOnly {
		t.Fatalf("prerequisite Review completion=%+v err=%v fake=%+v", finished, err, fake)
	}
	// Native completion supplies transport evidence. The fixture's separate GPT
	// submission creates the formal proposal before the user approval step.
	analysis := &runanalysis.Service{Store: sqlite.NewRunAnalysisStore(db), Analyze: exec.AnalyzeRunReport}
	source, err := analysis.Get(ctx, session, finished.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = analysis.Submit(ctx, runanalysis.Input{AISessionID: session, RunID: finished.ID, RequestID: "analysis-" + finished.ID, SourceHash: source.SourceHash, Report: raw}); err != nil {
		t.Fatal(err)
	}
	progression := sqlite.NewWorkflowProgressionStore(db)
	progression.SetRuntimeConfigProvider(codexruntime.Default)
	resolved, err := progression.Resolve(ctx, wp.ResolveInput{AISessionID: session, TaskID: value.ID, DecisionID: wp.DecisionID(admission.Review.ID), RequestID: "approve-" + value.ID, Option: "APPROVE", BackendSelection: ptr(config.Selection("runtime-supervisor-test")), NextRunRuntimeConfig: &runtimeConfig, StartEffect: wp.StartEffectStartAuthorized, StartRequestID: "start-" + value.ID})
	if err != nil {
		t.Fatal(err)
	}
	proposal, reserved, err := progression.ReserveTaskExecution(ctx, session, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status == executionproposal.StatusAdmitted && reserved.ID == "" {
		proposal, reserved, err = sqlite.NewExecutionProposalStore(db).PrepareAdmittedExecution(ctx, proposal.ID, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if resolved.Task == nil {
		t.Fatal("approval did not return Task")
	}
	return *resolved.Task, proposal, reserved
}

// This fake supplies only transport output. Coordinator owns lifecycle,
// provenance, result attachment, quiescence and terminalization.
type authorizedPlanReviewSession struct {
	protocolSession
	report string
}

func (s *authorizedPlanReviewSession) StartTurn(ctx context.Context, threadID, prompt string) (exec.CodexTurnResult, error) {
	result, err := s.protocolSession.StartTurn(ctx, threadID, prompt)
	result.TurnID = "prerequisite-review-turn"
	result.Message = "Synthetic read-only review completed."
	return result, err
}

func (s *authorizedPlanReviewSession) StartTurnWithSchema(ctx context.Context, threadID, prompt string, _ json.RawMessage) (exec.CodexTurnResult, error) {
	result, err := s.protocolSession.StartTurn(ctx, threadID, prompt)
	result.TurnID = "prerequisite-review-turn"
	result.Message = s.report
	return result, err
}

type authorizedPlanReviewFactory struct {
	session *authorizedPlanReviewSession
	ceiling codex.AuthorizationCeiling
}

func (f *authorizedPlanReviewFactory) Start(context.Context, codexconfig.Config) (exec.CodexSession, error) {
	return f.session, nil
}
func (f *authorizedPlanReviewFactory) StartForOperation(ctx context.Context, config codexconfig.Config, _, _ string, ceiling codex.AuthorizationCeiling) (exec.CodexSession, error) {
	f.ceiling = ceiling
	if ceiling != codex.AuthorizationReadOnly {
		return nil, errors.New("prerequisite Review must be read-only")
	}
	return f.Start(ctx, config)
}

func ptr[T any](value T) *T { return &value }

func waitAuthorizedPlanRun(t *testing.T, runs *run.Service, id string, terminal bool) run.Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		value, err := runs.Get(context.Background(), id)
		if err == nil && terminal == (value.Status == run.StatusSucceeded || value.Status == run.StatusFailed || value.Status == run.StatusInfrastructureFailed || value.Status == run.StatusCanceled) {
			return value
		}
		time.Sleep(time.Millisecond)
	}
	value, err := runs.Get(context.Background(), id)
	t.Fatalf("Run %s did not reach expected terminal=%v: %+v err=%v", id, terminal, value, err)
	return run.Run{}
}

func TestRuntimeSupervisorAuthorizedPlanTransfersSQLiteRuntimeFenceToVerification(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "runtime-supervisor-test")
	root := t.TempDir()
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := runtimeSupervisorTestEpoch.Add(24 * time.Hour)
	clock := deterministicRuntimeClock(now.Add(10 * time.Minute))
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	projectValue := project.Project{ID: "project-authorized-plan", Name: "authorized plan", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewProjectStore(db).Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	repositoryValue := repository.Repository{ID: "repository-authorized-plan", ProjectID: projectValue.ID, Name: "repository", Path: filepath.Join(root, "source"), CreatedAt: now, UpdatedAt: now}
	if _, _, err = sqlite.NewRepositoryStore(db).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	moduleValue := module.Module{ID: "module-authorized-plan", ProjectID: projectValue.ID, Key: "runtime", Name: "Runtime", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewModuleStore(db).Create(ctx, moduleValue); err != nil {
		t.Fatal(err)
	}
	sessionValue := aisession.Session{ID: "session-authorized-plan", ProjectID: projectValue.ID, CreatedAt: now, UpdatedAt: now}
	sessions := sqlite.NewAISessionStore(db)
	if _, err = sessions.InitializeSession(ctx, "runtime-supervisor-test", "initialize-authorized-plan", sessionValue.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = sessions.BindSessionProject(ctx, "runtime-supervisor-test", "bind-authorized-plan", sessionValue.ID, projectValue.ID, "selection-authorized-plan", now); err != nil {
		t.Fatal(err)
	}
	areaValue := featurearea.FeatureArea{ID: "feature-authorized-plan", ProjectID: projectValue.ID, CreatedByAISessionID: sessionValue.ID, Title: "Runtime safety", ProblemStatement: "Preserve one fenced execution", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewFeatureAreaStore(db).Create(ctx, areaValue); err != nil {
		t.Fatal(err)
	}
	taskValue := task.Task{ID: "task-authorized-plan", ProjectID: projectValue.ID, FeatureAreaID: areaValue.ID, ModuleID: moduleValue.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID, ProblemStatement: "Implement and verify", BaseCommit: base, Title: "Runtime safety", Status: task.StatusActive, Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewTaskStore(db).Create(ctx, taskValue); err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{Goal: "Implement runtime safety", ImplementationMode: task.ImplementationCodingAgent, CodexInstruction: "Implement the confirmed runtime safety change.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Go packages", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"tests pass"}, CodexInstruction: "Run the confirmed Go tests without modifying files."}
	taskValue, proposalValue, implementationRun := workflowtest.ApproveAndReserve(t, db, taskValue, sessionValue.ID, work, verification)
	proposalStore := sqlite.NewExecutionProposalStore(db)
	var idMu sync.Mutex
	nextID := 0
	ids := func() (string, error) {
		idMu.Lock()
		defer idMu.Unlock()
		nextID++
		return fmt.Sprintf("runtime-resource-%d", nextID), nil
	}
	runs := run.NewService(sqlite.NewRunStore(db), ids, clock)
	factory := &authorizedPlanFactory{created: make(chan *authorizedPlanSession, 2)}
	coordinator := &exec.Coordinator{Repositories: protocolRepositories{RepositoryStore: sqlite.NewRepositoryStore(db), base: base},
		CodexConfigs:          sqlite.NewCodexConfigStore(db),
		Tasks:                 task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, ids, clock),
		Executions:            &taskgoal.ExecutionAdapter{Proposals: sqlite.NewExecutionProposalStore(db)},
		Runs:                  runs,
		Worktrees:             &worktree.DurableManager{External: protocolTree{root: filepath.Join(root, "worktrees")}, Store: sqlite.NewWorktreeStore(db)},
		Workspaces:            workspace.NewService(sqlite.NewWorkspaceStore(db), ids, clock),
		Codex:                 factory,
		Evidence:              authorizedPlanEvidence{},
		Conversations:         sqlite.NewAgentConversationStore(db),
		Handoffs:              proposalStore,
		RuntimeConfigProvider: codexruntime.Default,
	}
	supervisor, err := exec.NewRuntimeSupervisor(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Shutdown()

	var oldImplementationContext context.Context
	completed := make(chan executionproposal.Outcome, 1)
	started, err := supervisor.StartAuthorizedPlan(ctx, taskValue.ID, work, verification,
		func(reservationContext context.Context) (run.Run, error) {
			return implementationRun, nil
		},
		func(reservationContext context.Context) (run.Run, error) {
			oldImplementationContext = reservationContext
			_, reserved, reserveErr := proposalStore.ReserveVerificationRun(reservationContext, proposalValue.ID, "run-authorized-verification", now.Add(5*time.Second))
			return reserved, reserveErr
		},
		func(completionContext context.Context, outcome executionproposal.Outcome, code, message string) error {
			_, completeErr := proposalStore.Complete(completionContext, proposalValue.ID, outcome, code, message, now.Add(6*time.Second))
			completed <- outcome
			return completeErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if started.ID != proposalValue.ImplementationRunID || started.RuntimeEpoch != db.RuntimeEpoch() {
		t.Fatalf("implementation reservation=%+v current epoch=%q", started, db.RuntimeEpoch())
	}

	implementationSession := awaitAuthorizedPlanSession(t, factory)
	close(implementationSession.release)
	verificationSession := awaitAuthorizedPlanSession(t, factory)
	if oldImplementationContext == nil {
		t.Fatal("verification reservation did not retain the implementation fence for the handoff assertion")
	}

	implementation, err := runs.Get(ctx, proposalValue.ImplementationRunID)
	if err != nil {
		t.Fatal(err)
	}
	verificationRun, err := runs.Get(ctx, "run-authorized-verification")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := sqlite.NewAgentConversationStore(db).GetClaim(ctx, areaValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if implementation.Status != run.StatusSucceeded || verificationRun.Status != run.StatusRunning || implementation.RuntimeEpoch != db.RuntimeEpoch() || verificationRun.RuntimeEpoch != db.RuntimeEpoch() {
		t.Fatalf("implementation=%+v verification=%+v epoch=%q", implementation, verificationRun, db.RuntimeEpoch())
	}
	if claim.Kind != agentconversation.Execution || claim.OperationID != proposalValue.ID || claim.RunID != verificationRun.ID || claim.RuntimeEpoch != db.RuntimeEpoch() {
		t.Fatalf("verification did not own exact durable claim: %+v", claim)
	}
	implementationBoundary, err := sqlite.NewAgentConversationStore(db).Boundary(ctx, implementation.ID)
	if err != nil {
		t.Fatal(err)
	}
	verificationBoundary, err := sqlite.NewAgentConversationStore(db).Boundary(ctx, verificationRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if implementation.ThreadID == "" || implementation.ThreadID != verificationRun.ThreadID || verificationSession.resumed != implementation.ThreadID || implementationBoundary.AgentConversationID != verificationBoundary.AgentConversationID || verificationBoundary.EntryBoundaryTurnID != implementation.TurnID {
		t.Fatalf("conversation continuity implementation=%+v verification=%+v implementationBoundary=%+v verificationBoundary=%+v resumed=%q", implementation, verificationRun, implementationBoundary, verificationBoundary, verificationSession.resumed)
	}
	if _, err = runs.AttachEndSnapshot(oldImplementationContext, implementation.ID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("old implementation fence write=%v, want %v", err, runtimeepoch.ErrFenced)
	}

	close(verificationSession.release)
	select {
	case outcome := <-completed:
		if outcome != executionproposal.OutcomeSucceeded {
			t.Fatalf("execution outcome=%s", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authorized plan did not complete")
	}
	proposalValue, err = proposalStore.Get(ctx, proposalValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	taskValue, err = task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, ids, clock).Get(ctx, taskValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	verificationRun, err = runs.Get(ctx, verificationRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proposalValue.Outcome != executionproposal.OutcomeSucceeded || taskValue.Status != task.StatusActive || verificationRun.Status != run.StatusSucceeded || verificationRun.VerificationReport != nil || verificationRun.AnalysisState != "REQUIRED" {
		t.Fatalf("terminal proposal=%+v task=%+v verification=%+v", proposalValue, taskValue, verificationRun)
	}
	if _, err = sqlite.NewAgentConversationStore(db).GetClaim(ctx, areaValue.ID); !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("terminal product completion retained Feature Area claim: %v", err)
	}
}

func TestRuntimeSupervisorVerificationPanicFailsCurrentSQLiteRun(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	f.factory.configure = func(session *authorizedPlanSession) {
		if session.id == 2 {
			session.panicTurn = true
		}
	}
	var completed atomic.Bool
	f.start(t, func(ctx context.Context, outcome executionproposal.Outcome, code, message string) error {
		completed.Store(true)
		_, err := f.proposals.Complete(ctx, f.proposalValue.ID, outcome, code, message, f.now.Add(6*time.Second))
		return err
	})
	implementationSession := awaitAuthorizedPlanSession(t, f.factory)
	close(implementationSession.release)
	_ = awaitAuthorizedPlanSession(t, f.factory)
	verificationRun := waitAuthorizedPlanRun(t, f.runs, "run-panic-verification", true)
	implementationRun, err := f.runs.Get(context.Background(), f.proposalValue.ImplementationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Load() || implementationRun.Status != run.StatusSucceeded || verificationRun.Status != run.StatusInfrastructureFailed || verificationRun.Failure == nil || verificationRun.Failure.Code != "runtime_session_panic" {
		t.Fatalf("completed=%v implementation=%+v verification=%+v", completed.Load(), implementationRun, verificationRun)
	}
	claim, err := sqlite.NewAgentConversationStore(f.db).GetClaim(context.Background(), f.areaID)
	if err != nil || claim.RunID != verificationRun.ID || claim.RuntimeEpoch != verificationRun.RuntimeEpoch {
		t.Fatalf("panic claim=%+v err=%v", claim, err)
	}
	boundary, err := sqlite.NewAgentConversationStore(f.db).Boundary(context.Background(), verificationRun.ID)
	if err != nil || boundary.Advance != agentconversation.Unknown || boundary.Quiesced {
		t.Fatalf("panic boundary=%+v err=%v", boundary, err)
	}
	state, err := sqlite.NewAgentConversationStore(f.db).State(context.Background(), f.areaID)
	if err != nil || state.Status != "RECOVERY_REQUIRED" {
		t.Fatalf("panic recovery state=%+v err=%v", state, err)
	}
}

func TestRuntimeSupervisorCompletionPanicPreservesTerminalVerificationForRestart(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	implementation := f.start(t, func(context.Context, executionproposal.Outcome, string, string) error {
		panic("synthetic product completion panic")
	})
	implementationSession := awaitAuthorizedPlanSession(t, f.factory)
	close(implementationSession.release)
	verificationSession := awaitAuthorizedPlanSession(t, f.factory)
	live, ok := f.supervisor.GetSession("run-panic-verification")
	if !ok {
		t.Fatal("verification session was not live before completion panic")
	}
	close(verificationSession.release)
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	verificationRun, waitErr := live.Wait(waitCtx)
	if waitErr == nil || verificationRun.Status != run.StatusSucceeded || verificationRun.Failure != nil {
		t.Fatalf("completion panic verification=%+v err=%v", verificationRun, waitErr)
	}
	implementation, err := f.runs.Get(context.Background(), implementation.ID)
	if err != nil || implementation.Status != run.StatusSucceeded {
		t.Fatalf("completion panic rewrote implementation=%+v err=%v", implementation, err)
	}
	proposalValue, err := f.proposals.Get(context.Background(), f.proposalValue.ID)
	if err != nil || proposalValue.Outcome != executionproposal.OutcomeRunning {
		t.Fatalf("completion panic proposal=%+v err=%v", proposalValue, err)
	}
	claim, err := sqlite.NewAgentConversationStore(f.db).GetClaim(context.Background(), f.areaID)
	if err != nil || claim.RunID != verificationRun.ID {
		t.Fatalf("completion panic claim=%+v err=%v", claim, err)
	}
	state, err := sqlite.NewAgentConversationStore(f.db).State(context.Background(), f.areaID)
	if err != nil || state.Status != "RECOVERY_REQUIRED" {
		t.Fatalf("completion panic recovery state=%+v err=%v", state, err)
	}
	if err = f.supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	f.db, err = sqlite.Open(context.Background(), f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	if err = sqlite.NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	state, err = sqlite.NewAgentConversationStore(f.db).State(context.Background(), f.areaID)
	if err != nil || state.Status != "READY" || state.Claim != nil {
		t.Fatalf("restart recovery state=%+v err=%v", state, err)
	}
	proposalValue, err = sqlite.NewExecutionProposalStore(f.db).Get(context.Background(), f.proposalValue.ID)
	if err != nil || proposalValue.Outcome == executionproposal.OutcomeRunning || proposalValue.Outcome == executionproposal.OutcomePending {
		t.Fatalf("restart left execution aggregate in flight: %+v err=%v", proposalValue, err)
	}
}

// These tests exercise runtime finalization. Fixture setup is already a
// completed read-only operation with immutable authored provenance.

type mutatedVerificationEvidence struct {
	authorizedPlanEvidence
	captures int
}

func (e *mutatedVerificationEvidence) CaptureSnapshot(context.Context, string) (string, error) {
	e.captures++
	if e.captures == 4 {
		return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil
	}
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

func TestVerificationReportMutationPreservesSQLiteTaskAndReleasesClaim(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	f.coordinator.Evidence = &mutatedVerificationEvidence{}
	completed := make(chan error, 1)
	f.start(t, func(ctx context.Context, outcome executionproposal.Outcome, code, message string) error {
		_, err := f.proposals.Complete(ctx, f.proposalValue.ID, outcome, code, message, f.now.Add(time.Hour))
		completed <- err
		return err
	})
	implementation := awaitAuthorizedPlanSession(t, f.factory)
	close(implementation.release)
	verification := awaitAuthorizedPlanSession(t, f.factory)
	close(verification.release)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completion missing")
	}
	owner, err := sqlite.NewTaskStore(f.db).Get(context.Background(), f.taskValue.ID)
	if err != nil || owner.Status != task.StatusActive {
		t.Fatalf("owner=%+v err=%v", owner, err)
	}
	execution, err := f.proposals.Get(context.Background(), f.proposalValue.ID)
	if err != nil || execution.Outcome != executionproposal.OutcomeFailed {
		t.Fatalf("execution=%+v err=%v", execution, err)
	}
	impl, err := f.runs.Get(context.Background(), execution.ImplementationRunID)
	if err != nil || impl.Status != run.StatusSucceeded {
		t.Fatalf("implementation=%+v err=%v", impl, err)
	}
	verify, err := f.runs.Get(context.Background(), execution.VerificationRunID)
	if err != nil || verify.Status != run.StatusInfrastructureFailed || verify.Failure == nil || verify.Failure.Code != "VERIFICATION_MUTATED_WORKTREE" || verify.VerificationReport != nil {
		t.Fatalf("verification=%+v err=%v", verify, err)
	}
	f.factory.mu.Lock()
	ceilings := append([]codex.AuthorizationCeiling(nil), f.factory.ceilings...)
	f.factory.mu.Unlock()
	if len(ceilings) != 2 || ceilings[0] != codex.AuthorizationWorkspaceWrite || ceilings[1] != codex.AuthorizationReadOnly {
		t.Fatalf("authorization ceilings=%v", ceilings)
	}
	var claims int
	if err = f.db.SQL().QueryRow(`SELECT COUNT(*) FROM feature_area_agent_claims WHERE feature_area_id=?`, f.areaID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("claims=%d err=%v", claims, err)
	}
	receipts, err := sqlite.NewDecisionResolutionStore(f.db).ListTaskAcceptance(context.Background(), owner.ID)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("invented acceptance=%+v err=%v", receipts, err)
	}
}
