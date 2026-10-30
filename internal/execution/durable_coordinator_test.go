package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type persistedWorktreeFactory struct {
	root string
	now  time.Time
}

func (persistedWorktreeFactory) Validate(context.Context, worktree.Worktree) error { return nil }

func (f persistedWorktreeFactory) Create(_ context.Context, input worktree.CreateInput) (worktree.Worktree, error) {
	path := filepath.Join(f.root, input.RunID)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return worktree.Worktree{}, err
	}
	return worktree.Worktree{ID: "wt-" + input.RunID, RepositoryID: input.RepositoryID, RepositoryPath: input.RepositoryPath, RunID: input.RunID, Path: path, Branch: "devbridge/" + input.FeatureAreaID + "/" + input.RunID, BaseCommit: input.BaseCommit, Status: worktree.StatusReady, CreatedAt: f.now, UpdatedAt: f.now}, nil
}

func TestDurableCoordinatorLifecycleSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 2, 4, 5, 6, 7, time.UTC)
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	projects := sqlite.NewProjectStore(db)
	repositories := sqlite.NewRepositoryStore(db)
	modules := sqlite.NewModuleStore(db)
	configs := sqlite.NewCodexConfigStore(db)
	tasks := sqlite.NewTaskStore(db)
	runs := sqlite.NewRunStore(db)
	worktrees := sqlite.NewWorktreeStore(db)
	workspaceStore := sqlite.NewWorkspaceStore(db)
	artifacts := sqlite.NewArtifactStore(db)
	evidenceStore := sqlite.NewEvidenceStore(db)
	p := project.Project{ID: "project-durable", Name: "durable", CreatedAt: now, UpdatedAt: now}
	if err := projects.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	repo := repository.Repository{ID: "repo-durable", ProjectID: p.ID, Name: "repo", Path: filepath.Join(root, "source"), CreatedAt: now, UpdatedAt: now}
	if _, _, err := repositories.PutIfAbsent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	moduleValue := module.Module{ID: "module-durable", ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: now, UpdatedAt: now}
	if err := modules.Create(ctx, moduleValue); err != nil {
		t.Fatal(err)
	}
	// Runtime admission accepts only a Session created through the session-first
	// protocol and durably bound to its Project.  Keep this integration fixture
	// on that public store path instead of reviving the retired direct Create
	// shortcut.
	session := aisession.Session{ID: "session-durable", ProjectID: p.ID, CreatedAt: now, UpdatedAt: now}
	sessions := sqlite.NewAISessionStore(db)
	if _, err := sessions.InitializeSession(ctx, "execution-durable-fixture", "initialize-durable", session.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.BindSessionProject(ctx, "execution-durable-fixture", "bind-durable", session.ID, p.ID, "selection-durable", now); err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "feature-durable", ProjectID: p.ID, CreatedByAISessionID: session.ID, Title: "Durable lifecycle", ProblemStatement: "Persist exact operation", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	taskValue := task.Task{ID: "task-durable", ProjectID: p.ID, FeatureAreaID: area.ID, ModuleID: moduleValue.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "persist it", Constraints: []string{"stay local"}, WorkSpec: task.WorkSpec{Goal: "persist it", Constraints: []string{"stay local"}, CodexInstruction: "persist it", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}, BaseCommit: base, Title: "Durable lifecycle", Status: task.StatusActive, Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
	if err := tasks.Create(ctx, taskValue); err != nil {
		t.Fatal(err)
	}
	taskValue, _, reserved := workflowtest.ApproveAndReserve(t, db, taskValue, session.ID, taskValue.WorkSpec, taskValue.VerificationSpec)
	proposals := sqlite.NewExecutionProposalStore(db)
	artifactSequence := 0
	fs, err := artifact.NewFilesystem(root, func() (string, error) { artifactSequence++; return fmt.Sprintf("artifact-%d", artifactSequence), nil }, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	runService := run.NewService(runs, func() (string, error) { return "run-durable", nil }, func() time.Time { return now })
	taskService := task.NewService(tasks, nil, nil, nil, nil, func() time.Time { return now })
	workspaceService := workspace.NewService(workspaceStore, func() (string, error) { return "workspace-durable", nil }, func() time.Time { return now })
	collector := &fakeCollector{value: evidence.GitEvidence{BaseCommit: base, HeadCommit: base, Dirty: true, ChangedFiles: []string{"changed.go"}, Diff: "diff --git changed.go"}}
	coordinator := &Coordinator{Repositories: repositories, CodexConfigs: configs, RuntimeConfigProvider: codexruntime.Default, Tasks: taskService, Executions: &taskgoal.ExecutionAdapter{Proposals: proposals}, Conversations: sqlite.NewAgentConversationStore(db), Runs: runService, Worktrees: &worktree.DurableManager{External: persistedWorktreeFactory{root: filepath.Join(root, "worktrees"), now: now}, Store: worktrees}, Workspaces: workspaceService, Codex: fakeFactory{session: &fakeSession{}}, Evidence: collector, Results: &DurableResultRecorder{Filesystem: fs, Artifacts: artifacts, Evidence: evidenceStore, Now: func() time.Time { return now }}}
	prepared, err := coordinator.PrepareReservedRun(ctx, reserved.ID, Instruction{CodexInstruction: taskValue.WorkSpec.CodexInstruction})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.ExecuteRun(ctx, prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != run.StatusSucceeded || result.Result == nil {
		t.Fatalf("run=%+v", result)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	storedRun, err := sqlite.NewRunStore(db).Get(ctx, result.ID)
	if err != nil || storedRun.Status != run.StatusSucceeded {
		t.Fatalf("stored run=%+v err=%v", storedRun, err)
	}
	storedTask, err := sqlite.NewTaskStore(db).Get(ctx, taskValue.ID)
	if err != nil || storedTask.Status != task.StatusActive || storedTask.Phase != task.PhaseVerificationReady {
		t.Fatalf("stored task=%+v err=%v", storedTask, err)
	}
	storedEvidence, err := sqlite.NewEvidenceStore(db).Get(ctx, result.ID)
	if err != nil || storedEvidence.DiffArtifactID == "" || len(storedEvidence.ChangedFiles) != 1 {
		t.Fatalf("evidence=%+v err=%v", storedEvidence, err)
	}
	storedArtifacts, err := sqlite.NewArtifactStore(db).ListByRun(ctx, result.ID)
	if err != nil || len(storedArtifacts) != 2 {
		t.Fatalf("artifacts=%+v err=%v", storedArtifacts, err)
	}
	for _, value := range storedArtifacts {
		if _, err := os.Stat(value.Path); err != nil {
			t.Fatalf("artifact file: %v", err)
		}
	}
	events, err := sqlite.NewEventStore(db).List(ctx, event.Filter{RunID: result.ID})
	if err != nil {
		t.Fatal(err)
	}
	types := make([]string, 0, len(events))
	for _, value := range events {
		types = append(types, value.Type)
	}
	for _, want := range []string{event.RunCreated, event.WorktreeCreated, event.ArtifactRecorded, event.EvidenceRecorded, event.RunStatusChanged} {
		found := false
		for _, got := range types {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing event %q in %v", want, types)
		}
	}
}

type metadataProbe struct{ exists bool }

func (p *metadataProbe) Create(_ context.Context, value artifact.Artifact) error {
	_, err := os.Stat(value.Path)
	p.exists = err == nil
	return nil
}

type evidenceProbe struct{}

func (evidenceProbe) Save(context.Context, evidence.GitEvidence) error { return nil }

func TestDurableResultFinalizesFileBeforeMetadata(t *testing.T) {
	sequence := 0
	fs, err := artifact.NewFilesystem(t.TempDir(), func() (string, error) { sequence++; return fmt.Sprintf("artifact-%d", sequence), nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	probe := &metadataProbe{}
	recorder := &DurableResultRecorder{Filesystem: fs, Artifacts: probe, Evidence: evidenceProbe{}}
	if _, err := recorder.Record(context.Background(), "run-fixed", evidence.AgentClaim{Message: "claim"}, evidence.GitEvidence{Diff: "diff"}); err != nil {
		t.Fatal(err)
	}
	if !probe.exists {
		t.Fatal("metadata inserted before finalized artifact existed")
	}
}
