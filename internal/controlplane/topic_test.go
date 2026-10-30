package controlplane_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"
)

func TestFeatureAreaPrerequisiteFailureCreatesNoExecutionState(t *testing.T) {
	ctx := context.Background()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	projectValue, err := plane.Projects.Create(ctx, "Topic-only Project")
	if err != nil {
		t.Fatal(err)
	}
	opened := bindFixtureSession(t, ctx, plane, "topic-prerequisite", projectValue.ID)
	_, err = plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: opened.Session.ID, Title: "Investigate startup", ProblemStatement: "Startup intermittently fails"})
	if err != nil {
		t.Fatalf("FeatureArea creation no longer requires a Development Branch: %v", err)
	}
	for _, table := range []string{"tasks", "runs", "workspaces", "execution_proposals", "task_plans"} {
		var count int
		if err := plane.DB.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("Feature Area creation created %d rows in %s", count, table)
		}
	}
}

func TestDiscoveringFeatureAreaOwnsSemanticTaskBeforeReview(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "repository")
	baseCommit := initializeContextFlowRepository(t, repositoryPath)
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	projectValue, err := plane.Projects.Create(ctx, "Linked Topic")
	if err != nil {
		t.Fatal(err)
	}
	repositoryValue, err := plane.Repositories.RegisterInProject(ctx, projectValue.ID, repositoryPath, "Repository")
	if err != nil {
		t.Fatal(err)
	}
	opened := bindFixtureSession(t, ctx, plane, "topic-discovering", projectValue.ID)
	ctx = aisession.WithCallerScope(ctx, "topic-discovering")
	if _, err = plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: projectValue.ID, RepositoryID: repositoryValue.ID, DevelopmentRef: "refs/heads/main", RequestID: "fixture-branch"}); err != nil {
		t.Fatal(err)
	}
	featureAreaValue, err := plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: opened.Session.ID, Title: "Improve guidance", ProblemStatement: "Improve the repository guidance."})
	if err != nil {
		t.Fatal(err)
	}
	unclassified, err := plane.Modules.EnsureUnclassified(ctx, projectValue.ID)
	if err != nil || unclassified.Key != module.UnclassifiedKey {
		t.Fatalf("unclassified=%+v err=%v", unclassified, err)
	}
	api, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := api.CreatePlanningTaskForSession(ctx, controlapi.CreatePlanningTaskInput{AISessionID: opened.Session.ID, FeatureAreaID: featureAreaValue.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID, ModuleID: unclassified.ID, Title: "Improve guidance", ProblemStatement: featureAreaValue.ProblemStatement, BaseRevision: baseCommit, RequestID: "intent"})
	if err != nil {
		t.Fatal(err)
	}
	if intent.Status != task.StatusActive || intent.Phase != task.PhaseReviewRequired {
		t.Fatalf("Task=%+v", intent)
	}
	var count int
	for _, table := range []string{"reviews", "execution_proposals"} {
		if err = plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("early %s authority: %d %v", table, count, err)
		}
	}
	approved := workflowtest.Approve(t, plane.DB, intent, opened.Session.ID, task.WorkSpec{Goal: "Improve guidance", CodexInstruction: "Read and improve the current guidance", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture waiver"}})
	if approved.ID != intent.ID || approved.Phase != task.PhaseImplementationReady {
		t.Fatalf("approval changed identity: %+v", approved)
	}
	area, err := plane.FeatureAreas.GetForSession(ctx, opened.Session.ID, featureAreaValue.ID)
	if err != nil || area.Status != featurearea.StatusActive {
		t.Fatalf("activation=%+v %v", area, err)
	}
	if err = plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM tasks").Scan(&count); err != nil || count != 1 {
		t.Fatalf("Task count=%d %v", count, err)
	}
	if err = plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM execution_proposals").Scan(&count); err != nil || count != 1 {
		t.Fatalf("approved Task execution authorization count=%d %v", count, err)
	}
}
