package bridgehttp

import (
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"testing"
)

func TestConsoleDirectReviewRunWithVisibleTask(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.operatorContext(t)
	area, err := f.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: f.sessionID, Title: "Task-owned Review", ProblemStatement: "Inspect before implementation intent."})
	if err != nil {
		t.Fatal(err)
	}
	owner := createPlanningTaskForArea(t, f, area.ID, "Understand ownership")
	value, err := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: owner.ID, ProjectID: f.projectID, FeatureAreaID: area.ID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseCommit: f.baseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect only.", EngineeringGoal: "Understand ownership"}))
	if err != nil {
		t.Fatal(err)
	}
	operation, err := f.plane.Runs.CreateFocusedScopeReviewForReview(ctx, value.ID, value.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	control, err := controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(f.plane, control)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := reader.ListRuns(ctx, f.projectID, consoleapi.RunListFilter{})
	if err != nil || len(runs.Items) != 1 {
		t.Fatalf("Review Runs=%+v %v", runs, err)
	}
	detail, err := reader.GetRun(ctx, f.projectID, operation.ID)
	if err != nil || detail.Run.OperationID != value.ID || detail.Run.TaskID != owner.ID || detail.RepositoryID != f.repositoryID {
		t.Fatalf("Review Run=%+v %v", detail, err)
	}
	if _, err := reader.GetRunPrompt(ctx, f.projectID, operation.ID); err != nil {
		t.Fatal(err)
	}
	other, err := f.plane.Projects.Create(ctx, "Other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetRun(ctx, other.ID, operation.ID); err == nil {
		t.Fatal("cross-Project Review exposed")
	}
	if values, err := f.plane.Tasks.List(ctx); err != nil || len(values) != 1 {
		t.Fatalf("Review read fabricated Task: %+v %v", values, err)
	}
}

func TestConsoleRetainsTerminalFeatureAreaHistory(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.operatorContext(t)
	area, err := f.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: f.sessionID, Title: "History", ProblemStatement: "Retain ownership history."})
	if err != nil {
		t.Fatal(err)
	}
	control, err := controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(f.plane, control)
	if err != nil {
		t.Fatal(err)
	}
	before, err := reader.ListProductFeatureAreas(ctx, f.projectID)
	if err != nil || len(before) != 1 || before[0].Status != "DISCOVERING" {
		t.Fatalf("discovering=%+v %v", before, err)
	}
	if _, err = f.plane.FeatureAreas.Activate(ctx, f.sessionID, area.ID, "activate-history"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.FeatureAreas.Retire(ctx, f.sessionID, area.ID, "retire-history"); err != nil {
		t.Fatal(err)
	}
	waitConsoleRetirement(t, f.plane, area.ID)
	after, err := reader.ListProductFeatureAreas(ctx, f.projectID)
	if err != nil || len(after) != 1 || after[0].Status != "RETIRED" || after[0].AgentOperationState != "UNAVAILABLE" {
		t.Fatalf("retired=%+v %v", after, err)
	}
}
