package controlplane_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"
)

type contextFlowProbeFactory struct {
	onStart func(context.Context) error
	done    chan struct{}
}

func (f *contextFlowProbeFactory) Start(ctx context.Context, _ codexconfig.Config) (probeexecution.CodexSession, error) {
	if f.onStart != nil {
		if err := f.onStart(ctx); err != nil {
			return nil, err
		}
	}
	return &contextFlowProbeSession{done: f.done}, nil
}

type contextFlowProbeSession struct {
	done chan struct{}
}

func (s *contextFlowProbeSession) StartThread(context.Context, string) (string, error) {
	return "thread-context-flow", nil
}

func (s *contextFlowProbeSession) StartTurn(context.Context, string, string) (probeexecution.CodexTurnResult, error) {
	return probeexecution.CodexTurnResult{
		TurnID:  "turn-context-flow",
		Message: "durable project guidance",
		TokenUsage: &run.TokenUsageObservation{
			Reliable:        true,
			StartCumulative: run.TokenCounts{TotalTokens: 10},
			EndCumulative:   run.TokenCounts{InputTokens: 3, OutputTokens: 2, TotalTokens: 15},
		},
	}, nil
}

func (s *contextFlowProbeSession) Close() error {
	close(s.done)
	return nil
}

func initializeContextFlowRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "AGENTS.md"), []byte("# Durable fixture guidance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "src", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init", "-b", "main"},
		{"add", "AGENTS.md", "src/main.go"},
		{"-c", "user.name=Dev Bridge Test", "-c", "user.email=test@devbridge.invalid", "commit", "-m", "fixture"},
	}
	for _, arguments := range commands {
		command := exec.Command("git", append([]string{"-C", path}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	command := exec.Command("git", "-C", path, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(output[:len(output)-1])
}

func TestProjectContextProbePersistsBeforeUnclassifiedTaskCreation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	baseCommit := initializeContextFlowRepository(t, repositoryPath)
	stateRoot := filepath.Join(root, "state")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	projectValue, err := plane.Projects.Create(ctx, "Context flow")
	if err != nil {
		t.Fatal(err)
	}
	repositoryValue, err := plane.Repositories.RegisterInProject(ctx, projectValue.ID, repositoryPath, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.Modules.Create(ctx, module.CreateInput{ProjectID: projectValue.ID, Key: "existing", Name: "Existing", Description: "Existing classified work"}); err != nil {
		t.Fatal(err)
	}
	sessionValue := bindFixtureSession(t, ctx, plane, "context-probe-persist", projectValue.ID)
	contextValue := sessionValue.ProjectContext
	if contextValue.Status != projectcontext.StatusReady || contextValue.ContextFingerprint == "" {
		t.Fatalf("root-guided Project context=%+v", contextValue)
	}
	probe, err := plane.ContextProbes.Create(ctx, contextprobe.CreateInput{
		AISessionID: sessionValue.Session.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID,
		Scope: contextprobe.Scope{Kind: contextprobe.ScopePaths, Paths: []string{"AGENTS.md"}}, CodexInstruction: "Read only the confirmed root guidance.",
	})
	if err != nil || probe.Status != contextprobe.StatusCreated {
		t.Fatalf("created probe=%+v err=%v", probe, err)
	}
	done := make(chan struct{})
	runner, err := probeexecution.NewRunner(plane.ContextProbes, plane.Repositories, plane.CodexConfigs, &contextFlowProbeFactory{done: done, onStart: func(handoff context.Context) error {
		fence, ok := runtimeepoch.FromContext(handoff)
		if !ok || fence.RunID == "" {
			return errors.New("Inspection backend launched without Run fence")
		}
		r, err := plane.Runs.Get(handoff, fence.RunID)
		if err != nil {
			return err
		}
		e, err := sqlite.NewAgentExecutionStore(plane.DB).Get(handoff, r.AgentExecutionID)
		if err != nil {
			return err
		}
		boundProbe, err := plane.ContextProbes.Get(handoff, probe.ID)
		if err != nil {
			return err
		}
		if boundProbe.AgentExecutionID != e.ID {
			return errors.New("Inspection correlation does not use durable Execution identity")
		}
		if e.Kind != agentexecution.KindInspection || e.TaskID != r.TaskID || e.ProjectID != projectValue.ID || e.FeatureAreaID != "" || e.SemanticOwnerKind != "INSPECTION" || e.SemanticOwnerID != probe.ID || r.ContextProbeID != probe.ID || r.RuntimeEpoch != fence.Epoch {
			return errors.New("Inspection backend launched without exact Task Execution Run ownership")
		}
		var claims int
		if err := plane.DB.SQL().QueryRowContext(handoff, `SELECT COUNT(*) FROM feature_area_agent_claims WHERE run_id=?`, r.ID).Scan(&claims); err != nil {
			return err
		}
		if claims != 0 {
			return errors.New("Project Inspection unexpectedly acquired FeatureArea claim")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Start(ctx, sessionValue.Session.ID, probe.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("context probe did not complete")
	}
	runner.Shutdown()
	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}

	plane, err = controlplane.Open(ctx, controlplane.Config{DataRoot: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	contextValue, err = plane.ProjectContexts.Get(ctx, projectValue.ID)
	if err != nil || contextValue.Status != projectcontext.StatusReady || contextValue.LatestProbeID != probe.ID || contextValue.Result != "durable project guidance" {
		t.Fatalf("durable ProjectContext=%+v err=%v", contextValue, err)
	}
	unclassified, err := plane.Modules.EnsureUnclassified(ctx, projectValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	area := activateContextFixtureArea(t, plane, projectValue.ID, sessionValue.Session.ID)
	taskValue, err := plane.Tasks.Create(ctx, task.CreateInput{
		AISessionID: sessionValue.Session.ID, RequestID: "ai-context-task", ProjectID: projectValue.ID, FeatureAreaID: area.ID, ModuleID: unclassified.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID,
		ProblemStatement: "Use acquired context", BaseRevision: baseCommit, Planning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved := workflowtest.Approve(t, plane.DB, taskValue, sessionValue.Session.ID,
		task.WorkSpec{Goal: "Use acquired context", CodexInstruction: "Update only AGENTS.md as explicitly requested.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"AGENTS.md"}}},
		task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture waiver"}})
	if approved.ModuleID != unclassified.ID || !unclassified.SystemOwned || approved.Phase != task.PhaseImplementationReady || approved.WorkSpec.ExecutionScope.Paths[0] != "AGENTS.md" {
		t.Fatalf("approved Task=%+v", approved)
	}

}

func TestSessionOpenWithoutRootAgentsMarksNoGuidanceWithoutProbe(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "tracked.txt"), []byte("production\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	projectValue, err := plane.Projects.Create(ctx, "No guidance")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repository.Repository{ID: "repo-no-guidance", ProjectID: projectValue.ID, Name: "Repo", Path: repositoryPath, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	sessionValue := bindFixtureSession(t, ctx, plane, "no-guidance", projectValue.ID)
	if sessionValue.ProjectContext.Status != projectcontext.StatusNoGuidance || sessionValue.ProjectContext.LatestProbeID != "" {
		t.Fatalf("ProjectContext=%+v", sessionValue.ProjectContext)
	}
	probes, err := plane.ContextProbes.ListForSession(ctx, sessionValue.Session.ID)
	if err != nil || len(probes) != 0 {
		t.Fatalf("missing root AGENTS created ContextProbe/Codex work: %+v err=%v", probes, err)
	}
}

func TestAISessionBindingIsScopedAndDurable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := plane.AIContext.InitializeSession(ctx, aiprojectselection.ClientBindingReference{}, "initialize", ""); !errors.Is(err, aisession.ErrInvalidInitialization) {
		t.Fatalf("InitializeSession without caller identity error = %v", err)
	}

	now := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	projectOne := project.Project{ID: "project-one", Name: "One", CreatedAt: now, UpdatedAt: now}
	projectTwo := project.Project{ID: "project-two", Name: "Two", CreatedAt: now, UpdatedAt: now}
	for _, value := range []project.Project{projectOne, projectTwo} {
		if err := sqlite.NewProjectStore(plane.DB).Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	moduleOne := module.Module{ID: "module-one", ProjectID: projectOne.ID, Key: "one", Name: "One", CreatedAt: now, UpdatedAt: now}
	moduleTwo := module.Module{ID: "module-two", ProjectID: projectTwo.ID, Key: "two", Name: "Two", CreatedAt: now, UpdatedAt: now}
	for _, value := range []module.Module{moduleOne, moduleTwo} {
		if err := sqlite.NewModuleStore(plane.DB).Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	repositoryOne := repository.Repository{ID: "repo-one", ProjectID: projectOne.ID, Name: "One", Path: filepath.Join(root, "repo-one"), CreatedAt: now, UpdatedAt: now}
	repositoryTwo := repository.Repository{ID: "repo-two", ProjectID: projectTwo.ID, Name: "Two", Path: filepath.Join(root, "repo-two"), CreatedAt: now, UpdatedAt: now}
	for _, path := range []string{repositoryOne.Path, repositoryTwo.Path} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("production\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []repository.Repository{repositoryOne, repositoryTwo} {
		if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	sessionOne := bindFixtureSession(t, ctx, plane, "scoped-durable-one", projectOne.ID)
	sessionOnePeer := bindFixtureSession(t, ctx, plane, "scoped-durable-peer", projectOne.ID)
	if _, err := plane.ProjectSelection.Select(ctx, projectTwo.ID); err != nil {
		t.Fatal(err)
	}
	sessionTwo := bindFixtureSession(t, ctx, plane, "scoped-durable-two", projectTwo.ID)
	areaOne := activateContextFixtureArea(t, plane, projectOne.ID, sessionOne.Session.ID)
	areaTwo := activateContextFixtureArea(t, plane, projectTwo.ID, sessionTwo.Session.ID)
	taskOne := task.Task{ID: "task-one", ProjectID: projectOne.ID, FeatureAreaID: areaOne.ID, ModuleID: moduleOne.ID, RepositoryID: repositoryOne.ID, CodexConfigID: codexconfig.DefaultConfigID, ProblemStatement: "one problem", BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: task.StatusActive, Phase: task.PhaseReviewRequired, Title: "Intent", CreatedAt: now, UpdatedAt: now}
	taskTwo := task.Task{ID: "task-two", ProjectID: projectTwo.ID, FeatureAreaID: areaTwo.ID, ModuleID: moduleTwo.ID, RepositoryID: repositoryTwo.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "two goal", WorkSpec: task.WorkSpec{Goal: "two goal", CodexInstruction: "two goal", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}, BaseCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Status: task.StatusActive, Phase: task.PhaseReviewRequired, Title: "Intent", CreatedAt: now, UpdatedAt: now}
	for _, value := range []task.Task{taskOne, taskTwo} {
		if err := sqlite.NewTaskStore(plane.DB).Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}

	api, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := plane.AIContext.ListProjects(ctx, aicontext.ListOptions{})
	if err != nil || len(projects) != 2 {
		t.Fatalf("project bootstrap=%+v err=%v", projects, err)
	}
	if _, err := api.SelectProject(ctx, projectTwo.ID); err != nil {
		t.Fatal(err)
	}
	if sessionOne.Session.ProjectID != projectOne.ID || sessionOnePeer.Session.ProjectID != projectOne.ID || sessionTwo.Session.ProjectID != projectTwo.ID {
		t.Fatalf("session bindings = %q, %q", sessionOne.Session.ProjectID, sessionTwo.Session.ProjectID)
	}

	modules, err := plane.AIContext.ListModules(ctx, sessionOne.Session.ID, aicontext.ListOptions{})
	if err != nil || len(modules) != 3 || modules[0].Key != module.UnclassifiedKey || modules[1].ID != moduleOne.ID || modules[2].Key != "tracked-txt" || len(modules[2].ProductionFacts) != 1 {
		t.Fatalf("session-one modules = %+v, err=%v", modules, err)
	}
	tasks, err := plane.AIContext.ListTasks(ctx, sessionOne.Session.ID, aicontext.ListOptions{})
	if err != nil || len(tasks) != 1 || tasks[0].ID != taskOne.ID {
		t.Fatalf("session-one tasks = %+v, err=%v", tasks, err)
	}
	peerTasks, err := plane.AIContext.ListTasks(ctx, sessionOnePeer.Session.ID, aicontext.ListOptions{})
	if err != nil || len(peerTasks) != 1 || peerTasks[0].ID != taskOne.ID {
		t.Fatalf("peer session tasks = %+v, err=%v", peerTasks, err)
	}
	approved := workflowtest.Approve(t, plane.DB, taskOne, sessionOne.Session.ID,
		task.WorkSpec{Goal: "implement from first session", CodexInstruction: "implement from first session", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}},
		task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture waiver"}})
	peer, err := plane.AIContext.GetTask(ctx, sessionOnePeer.Session.ID, approved.ID)
	if err != nil || peer.CurrentDefinitionRevisionID != approved.CurrentDefinitionRevisionID || peer.Phase != task.PhaseImplementationReady {
		t.Fatalf("peer approval state=%+v %v", peer, err)
	}
	if _, err := plane.AIContext.GetTask(ctx, sessionOne.Session.ID, taskTwo.ID); !errors.Is(err, aicontext.ErrNotFound) {
		t.Fatalf("cross-project GetTask error = %v", err)
	}
	if _, err := plane.AIContext.GetTask(ctx, sessionOne.Session.ID, "missing-task"); !errors.Is(err, aicontext.ErrNotFound) {
		t.Fatalf("missing GetTask error = %v", err)
	}

	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	plane, err = controlplane.Open(ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	durable, err := plane.AIContext.GetSession(ctx, sessionOne.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if durable.Session.ProjectID != projectOne.ID {
		t.Fatalf("durable binding = %q, want %q", durable.Session.ProjectID, projectOne.ID)
	}
	if reopened, err := plane.AIContext.OpenProject(ctx, sessionOne.Session.ID, projectOne.ID); err != nil || reopened.Session.ID != sessionOne.Session.ID || reopened.Session.ProjectID != projectOne.ID {
		t.Fatalf("restart same-project reopen = %+v, err=%v", reopened, err)
	}
	if _, err := plane.AIContext.OpenProject(ctx, sessionOne.Session.ID, projectTwo.ID); !errors.Is(err, aicontext.ErrSessionProjectAlreadyBound) {
		t.Fatalf("restart different-project rebind error = %v", err)
	}
	current, err := plane.ProjectSelection.Current(ctx)
	if err != nil || current.CurrentSelectedProjectID != projectTwo.ID {
		t.Fatalf("durable selection = %+v, err=%v", current, err)
	}
	if _, err := plane.DB.SQL().ExecContext(ctx, `UPDATE ai_sessions SET project_id=? WHERE id=?`, projectTwo.ID, sessionOne.Session.ID); err == nil {
		t.Fatal("database allowed an AI session Project binding update")
	}
}

func activateContextFixtureArea(t *testing.T, plane *controlplane.ControlPlane, projectID, sessionID string) featurearea.FeatureArea {
	t.Helper()
	ctx := context.Background()
	if sessionID == "" {
		t.Fatal("fixture FeatureArea requires a Session initialized and bound through the public protocol")
	}
	now := time.Now().UTC()
	area := featurearea.FeatureArea{ID: "fixture-area-" + projectID, ProjectID: projectID, CreatedByAISessionID: sessionID, Title: "Explicit capability", ProblemStatement: "Task session fixture", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewFeatureAreaStore(plane.DB).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	return area
}
