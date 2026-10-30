package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func TestOpenMigratesAndRecoversBeforeReady(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "data")
	opened, err := dataroot.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, opened.Paths.State)
	if err != nil {
		t.Fatal(err)
	}
	runService := run.NewService(sqlite.NewRunStore(db), func() (string, error) { return "run-bootstrap", nil }, nil)
	runValue := createClaimedPreparingRun(t, &ControlPlane{DB: db, Runs: runService})
	db.Close()
	opened.Close()
	plane, err := Open(ctx, Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	if !plane.Ready {
		t.Fatal("control plane not ready")
	}
	got, err := plane.Runs.Get(ctx, runValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != run.StatusInfrastructureFailed || got.Failure == nil || got.Failure.Code != recovery.InterruptedCode {
		t.Fatalf("run=%+v", got)
	}
	if plane.Supervisor == nil {
		t.Fatal("runtime supervisor was not constructed")
	}
	if _, ok := plane.Supervisor.GetSession(runValue.ID); ok {
		t.Fatal("runtime supervisor reconstructed a pre-restart live session")
	}
}

func TestOpenRecoversInterruptedContextProbeBeforeReady(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repoPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoPath, "tracked.txt"), []byte("production\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 6, 0, 0, 0, time.UTC)
	plane, err := Open(ctx, Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	projectValue := project.Project{ID: "project-probe", Name: "Probe", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewProjectStore(plane.DB).Create(ctx, projectValue); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repository.Repository{ID: "repo-probe", ProjectID: projectValue.ID, Name: "Repo", Path: repoPath, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.ProjectSelection.Select(ctx, projectValue.ID); err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "controlplane-bootstrap-probe"}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "bootstrap-probe-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "bootstrap-probe-bind")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := plane.ContextProbes.Create(ctx, contextprobe.CreateInput{AISessionID: session.Context.Session.ID, RepositoryID: "repo-probe", CodexConfigID: codexconfig.DefaultConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.ContextProbes.Start(ctx, probe.ID); err != nil {
		t.Fatal(err)
	}
	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	plane, err = Open(ctx, Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	probe, err = plane.ContextProbes.Get(ctx, probe.ID)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Status != contextprobe.StatusFailed || probe.FinishedAt == nil {
		t.Fatalf("recovered probe=%+v", probe)
	}
	contextValue, err := plane.ProjectContexts.Get(ctx, projectValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contextValue.Status != projectcontext.StatusFailed {
		t.Fatalf("recovered context=%+v", contextValue)
	}
}
