package controlapi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestReproWorkflowGuidanceForFeatureAreaWithoutTask(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "first-task-repro")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	p, err := plane.Projects.Create(ctx, "First task project")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	repoPath := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	repoValue := repository.Repository{ID: "repro-repository", ProjectID: p.ID, Name: "Source", Path: repoPath, CreatedAt: now, UpdatedAt: now}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repoValue); err != nil {
		t.Fatal(err)
	}
	initializePresentationRepository(t, ownershipWorkflowFixture{plane: plane, projectID: p.ID, repositoryID: repoValue.ID})
	ref := aiprojectselection.ClientBindingReference{CallerScope: "first-task-repro"}
	session, err := plane.AIContext.InitializeSession(ctx, ref, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := plane.AIContext.BindProject(ctx, ref, session.AISessionID, p.ID, "bind")
	if err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	created, err := facade.CreateFeatureAreaWithInitialReview(ctx, controlapi.FeatureAreaCreateInput{AISessionID: bound.AISessionID, Title: "First task only", ProblemStatement: "No Task yet."})
	if err != nil {
		t.Fatal(err)
	}
	// The plain create must not have started any work: no Task, no Review, no Run.
	if created.Startup != nil {
		t.Fatalf("plain create unexpectedly started work: %+v", created.Startup)
	}
	g, err := facade.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: bound.AISessionID, FeatureAreaID: created.FeatureArea.ID})
	if err != nil {
		t.Fatalf("guidance read after plain FeatureArea create returned error: %v", err)
	}
	if g.CurrentState != "FEATURE_AREA_IDLE" || g.NextAction.Code != "CREATE_TASK" || !g.CanCreateTask() {
		t.Fatalf("unexpected guidance: state=%q next=%q featureAreaStatus=%q", g.CurrentState, g.NextAction.Code, g.FeatureAreaStatus)
	}
	t.Logf("guidance OK: state=%s next=%s generation=%s refreshRequired=%v", g.CurrentState, g.NextAction.Code, g.Generation, g.RefreshRequired)
}
