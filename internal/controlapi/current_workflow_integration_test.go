package controlapi_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func TestCurrentWorkflowUsesDurableLineageBeyondOneHundredHistoricalRecords(t *testing.T) {
	ctx := context.Background()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	projectValue, err := plane.Projects.Create(ctx, "History scale")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 4, 3, 4, 5, 0, time.UTC)
	repositoryValue := repository.Repository{ID: "repository-history", ProjectID: projectValue.ID, Name: "Primary", Path: filepath.Join(t.TempDir(), "repository"), CreatedAt: now, UpdatedAt: now}
	if err := os.MkdirAll(repositoryValue.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryValue.Path, "tracked.txt"), []byte("production\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repositoryValue); err != nil {
		t.Fatal(err)
	}
	moduleValue, err := plane.Modules.EnsureUnclassified(ctx, projectValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, sessionID := bindControlAPIFixtureProject(t, ctx, plane, "current-workflow-history", projectValue.ID, "history")
	session, err := plane.AIContext.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	initialContext := session.ProjectContext
	featureAreaStore, taskStore := sqlite.NewFeatureAreaStore(plane.DB), sqlite.NewTaskStore(plane.DB)
	runStore := sqlite.NewRunStore(plane.DB)
	work := task.WorkSpec{Goal: "current goal", Constraints: []string{}, CodexInstruction: "implement current goal", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	activeTopic := featurearea.FeatureArea{ID: "featureArea-current", ProjectID: projectValue.ID, CreatedByAISessionID: sessionID, Title: "Current work", ProblemStatement: "Current work must win", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err := featureAreaStore.Create(ctx, activeTopic); err != nil {
		t.Fatal(err)
	}
	currentTask := task.Task{ID: "task-current", ProjectID: projectValue.ID, FeatureAreaID: activeTopic.ID, ModuleID: moduleValue.ID, RepositoryID: repositoryValue.ID, CodexConfigID: codexconfig.DefaultConfigID, ProblemStatement: work.Goal, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "Current Task", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
	if err := taskStore.Create(ctx, currentTask); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 101; index++ {
		stamp := now.Add(time.Duration(index+1) * time.Minute)
		suffix := fmt.Sprintf("%03d", index)
		historicalTopic := featurearea.FeatureArea{ID: "featureArea-history-" + suffix, ProjectID: projectValue.ID, CreatedByAISessionID: sessionID, Title: "Historical", ProblemStatement: "Completed history", Status: featurearea.StatusActive, CreatedAt: stamp, UpdatedAt: stamp}
		if err := featureAreaStore.Create(ctx, historicalTopic); err != nil {
			t.Fatal(err)
		}
		if _, err := plane.AgentConversations.Reset(ctx, historicalTopic.ID, "fixture-history-"+suffix); err != nil {
			t.Fatal(err)
		}
		historicalTask := currentTask
		historicalTask.ID, historicalTask.FeatureAreaID = "task-history-"+suffix, historicalTopic.ID
		historicalTask.CreatedAt, historicalTask.UpdatedAt = stamp, stamp
		if err := taskStore.Create(ctx, historicalTask); err != nil {
			t.Fatal(err)
		}
		historicalReview, err := plane.Reviews.Create(ctx, codingReviewForPlane(t, ctx, plane, review.Review{ReviewDepth: "DEEP", TaskID: historicalTask.ID, ProjectID: projectValue.ID, FeatureAreaID: historicalTopic.ID, CreatedByAISessionID: sessionID, RepositoryID: repositoryValue.ID, CodexConfigID: currentTask.CodexConfigID, BaseCommit: currentTask.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Historical review", Instruction: "Inspect history"}))
		if err != nil {
			t.Fatal(err)
		}
		historicalRun, err := runStore.CreateProductScopeReviewAttempt(ctx, historicalReview.ID, "review-history-"+suffix, "review", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}, stamp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plane.Runs.Fail(ctx, historicalRun.ID, run.Failure{Kind: run.FailureTask, Code: "fixture", Message: "historical fixture"}); err != nil {
			t.Fatal(err)
		}
		if _, err := plane.FeatureAreas.Retire(ctx, sessionID, historicalTopic.ID, "retire-history"); err != nil {
			t.Fatal(err)
		}
		// Complete the synthetic history through the canonical coordinator.
		// Pending authorizations otherwise leave 101 background retirements
		// changing the project's event generation during the snapshot assertion.
		plane.Retirement.Advance(ctx, historicalTopic.ID)
		if err := featureAreaStore.FinishRetirement(ctx, historicalTopic.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 101; index++ {
		stamp := now.Add(time.Duration(index+1) * time.Hour)
		id := fmt.Sprintf("featureArea-unstarted-newer-%03d", index)
		unstarted := featurearea.FeatureArea{ID: id, ProjectID: projectValue.ID, CreatedByAISessionID: sessionID, Title: "Unstarted newer goal", ProblemStatement: "Must not displace active work", Status: featurearea.StatusActive, CreatedAt: stamp, UpdatedAt: stamp}
		if err := featureAreaStore.Create(ctx, unstarted); err != nil {
			t.Fatal(err)
		}
	}
	_, replacementID := bindControlAPIFixtureProject(t, ctx, plane, "current-workflow-history", projectValue.ID, "history-replacement")
	replacement, err := plane.AIContext.GetSession(ctx, replacementID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ProjectContext.ContextFingerprint == "" || replacement.ProjectContext.ContextFingerprint != initialContext.ContextFingerprint || !replacement.ProjectContext.UpdatedAt.Equal(initialContext.UpdatedAt) {
		t.Fatalf("historical FeatureAreas invalidated Project Context: initial=%+v replacement=%+v", initialContext, replacement.ProjectContext)
	}
	contextModules, err := plane.AIContext.ListModules(ctx, replacementID, aicontext.ListOptions{Limit: 100})
	if err != nil || len(contextModules) < 2 || len(contextModules[len(contextModules)-1].ProductionFacts) != 1 {
		t.Fatalf("historical FeatureAreas displaced Project Module Map: %+v err=%v", contextModules, err)
	}

	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	current, err := facade.CurrentWorkflowForSession(ctx, replacementID, "")
	if err != nil {
		t.Fatal(err)
	}
	if current.Topic == nil || current.Topic.ID != activeTopic.ID || current.Task == nil || current.Task.ID != currentTask.ID || current.TaskPlan != nil || current.Execution != nil || len(current.Runs) != 0 {
		t.Fatalf("current workflow was dominated by history: %+v", current)
	}
}

func TestCurrentWorkflowExposesMultipleUsableFeatureAreasWithoutChoosingNewest(t *testing.T) {
	ctx := context.Background()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	projectValue, err := plane.Projects.Create(ctx, "Multiple goals")
	if err != nil {
		t.Fatal(err)
	}
	ctx, sessionID := bindControlAPIFixtureProject(t, ctx, plane, "current-workflow-multiple", projectValue.ID, "multiple")
	now := time.Date(2026, 9, 4, 4, 5, 6, 0, time.UTC)
	store := sqlite.NewFeatureAreaStore(plane.DB)
	for index, id := range []string{"featureArea-first", "featureArea-newest"} {
		stamp := now.Add(time.Duration(index) * time.Minute)
		if err := store.Create(ctx, featurearea.FeatureArea{ID: id, ProjectID: projectValue.ID, CreatedByAISessionID: sessionID, Title: id, ProblemStatement: id, Status: featurearea.StatusActive, CreatedAt: stamp, UpdatedAt: stamp}); err != nil {
			t.Fatal(err)
		}
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	current, err := facade.CurrentWorkflowForSession(ctx, sessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if current.Topic != nil || len(current.AvailableFeatureAreas) != 2 || current.AvailableFeatureAreas[0].ID != "featureArea-newest" || current.AvailableFeatureAreas[1].ID != "featureArea-first" {
		t.Fatalf("ambiguous usable FeatureAreas = %+v", current)
	}
}
