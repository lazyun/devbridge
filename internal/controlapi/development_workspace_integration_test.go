package controlapi_test

import (
	"fmt"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"reflect"
	"testing"
	"time"
)

func TestDevelopmentWorkspaceAllCurrentAreasPureReadAndIndependentSelection(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	store := sqlite.NewFeatureAreaStore(f.plane.DB)
	for i := 0; i < 105; i++ {
		a := featurearea.FeatureArea{ID: fmt.Sprintf("inventory-%03d", i), ProjectID: f.projectID, CreatedByAISessionID: f.sessionID, Title: fmt.Sprintf("Area %d", i), ProblemStatement: "Inventory test", Status: featurearea.StatusActive, CreatedAt: f.now, UpdatedAt: f.now.Add(time.Duration(i+1) * time.Second)}
		if err := store.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	before := featureAreaPresentationCounts(t, ctx, f)
	first, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, "", "")
	if err != nil || len(first.FeatureAreas) != 106 || first.SelectedFeatureAreaID != "inventory-104" || first.PresentationID == "" || first.RefreshedAt.IsZero() {
		t.Fatalf("%+v %v", first, err)
	}
	selected, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, f.areaID, "")
	if err != nil || selected.SelectedFeatureAreaID != f.areaID {
		t.Fatalf("%+v %v", selected, err)
	}
	// An unrelated newly updated area must not steal the explicit selection.
	if _, err := f.plane.DB.SQL().ExecContext(ctx, `UPDATE feature_areas SET updated_at=? WHERE id='inventory-001'`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	again, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, f.areaID, "")
	if err != nil || again.SelectedFeatureAreaID != f.areaID || again.PresentationID != first.PresentationID {
		t.Fatalf("%+v %v", again, err)
	}
	independent, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, "", "")
	if err != nil || independent.SelectedFeatureAreaID != "inventory-001" {
		t.Fatalf("another entry inherited selection: %+v %v", independent, err)
	}
	after := featureAreaPresentationCounts(t, ctx, f)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read mutated workflow: %+v %+v", before, after)
	}
	if _, err := f.facade.RetireFeatureArea(ctx, f.sessionID, again.SelectedFeatureAreaID, "inventory-retire"); err != nil {
		t.Fatal(err)
	}
	next, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, "", "")
	if err != nil || next.SelectedFeatureAreaID == again.SelectedFeatureAreaID {
		t.Fatalf("invalid selection retained: %+v %v", next, err)
	}
}

func TestWorkspaceReviewRecoveryProjection(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusDiscovering)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	semantic := f.createTask(t)
	owner, err := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{ReviewDepth: "DEEP", TaskID: semantic.ID, ProjectID: f.projectID, FeatureAreaID: f.areaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "首次接入 Dev Bridge Card 布局回归修复", Instruction: "Inspect source"}))
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := sqlite.NewRunStore(f.plane.DB).CreateProductScopeReviewAttempt(ctx, owner.ID, "recovery-review-run", owner.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	// Only the isolated fixture database is changed; no runtime is started.
	if _, err := f.plane.DB.SQL().ExecContext(ctx, `UPDATE runs SET status='INFRASTRUCTURE_FAILED', failure_code='bridge_shutdown_interrupted' WHERE id=?`, reserved.ID); err != nil {
		t.Fatal(err)
	}
	before := featureAreaPresentationCounts(t, ctx, f)
	out, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, f.areaID, "")
	if err != nil || out.ReadError != "" || out.SelectedWorkReadError != "" || out.SelectedTaskID != semantic.ID || out.SelectedWork == nil || out.SelectedWork.Kind != "REVIEW" || out.SelectedWork.OwnerTaskID != semantic.ID || out.SelectedWork.OperationID != owner.ID || out.Presentation == nil {
		t.Fatalf("projection=%+v error=%v", out, err)
	}
	if out.Presentation.CurrentState != "RECOVERY_REQUIRED" || out.Presentation.NextAction.Code != "RECOVER_AGENT_OPERATION" {
		t.Fatalf("presentation=%+v", out.Presentation)
	}
	if out.FeatureAreas[0].CurrentTaskID != out.SelectedTaskID || out.FeatureAreas[0].CurrentTaskStatus != "ACTIVE" || out.FeatureAreas[0].CurrentAction.Code != "RECOVER_AGENT_OPERATION" {
		t.Fatalf("current work inventory=%+v", out.FeatureAreas)
	}
	if out.SelectedWork.RunID != reserved.ID || out.SelectedWork.Status != "INFRASTRUCTURE_FAILED" {
		t.Fatalf("work=%+v", out.SelectedWork)
	}
	assertFeatureAreaPresentationCounts(t, ctx, f, before)
}

func TestWorkspaceDevelopmentDetailRemainsTaskFirst(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	initializePresentationRepository(t, f)
	selected := f.createTask(t)
	out, err := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.sessionID, f.areaID, selected.ID)
	if err != nil || out.SelectedWorkReadError != "" || out.SelectedTaskID != selected.ID || out.Presentation == nil || out.Presentation.Details.TaskID != selected.ID {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestWorkspaceExplicitProjectInspectionProjectsExactOperation(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	semantic := f.createTask(t)
	if prior, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, f.areaID, semantic.ID); err != nil || prior.SelectedFeatureAreaID != f.areaID || prior.SelectedTaskID != semantic.ID {
		t.Fatalf("prior workspace=%+v err=%v", prior, err)
	}
	store := sqlite.NewContextProbeStore(f.plane.DB)
	probe := contextprobe.Probe{ID: "workspace-inspection", AISessionID: f.sessionID, ProjectID: f.projectID, RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopePaths, Paths: []string{"AGENTS.md"}}, CodexInstruction: "Read project guidance", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: f.now, UpdatedAt: f.now}
	if err := store.Create(ctx, probe); err != nil {
		t.Fatal(err)
	}
	probe, err := store.Update(ctx, probe.ID, func(value *contextprobe.Probe) error {
		value.Status = contextprobe.StatusRunning
		value.StartedAt = &f.now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, "", probe.TaskID)
	if err != nil || out.SelectedFeatureAreaID != "" || out.SelectedTaskID != "" || out.SelectedWork == nil {
		t.Fatalf("workspace=%+v err=%v", out, err)
	}
	if out.SelectedWork.Kind != "INSPECTION" || out.SelectedWork.OperationID != probe.ID || out.SelectedWork.OwnerTaskID != "" || out.SelectedWork.RunID != probe.RunID || out.SelectedWork.Status != "RUNNING" || !out.SelectedWork.UpdatedAt.Equal(probe.UpdatedAt) {
		t.Fatalf("inspection operation=%+v probe=%+v", out.SelectedWork, probe)
	}
	refreshed, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, "", probe.TaskID)
	if err != nil || refreshed.SelectedFeatureAreaID != "" || refreshed.SelectedTaskID != "" || refreshed.SelectedWork == nil || refreshed.SelectedWork.Kind != "INSPECTION" || refreshed.SelectedWork.OperationID != probe.ID || refreshed.SelectedWork.OwnerTaskID != "" {
		t.Fatalf("inspection refresh=%+v err=%v", refreshed, err)
	}
	independent, err := f.facade.DevelopmentWorkspaceForSession(ctx, f.sessionID, "", "")
	if err != nil || independent.SelectedTaskID != semantic.ID {
		t.Fatalf("independent entry drifted to inspection: %+v %v", independent, err)
	}
}

func TestWorkspaceReviewRequiredHasNoSelectedOperation(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	initializePresentationRepository(t, f)
	semantic := f.createTask(t)
	out, err := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.sessionID, f.areaID, semantic.ID)
	if err != nil || out.SelectedTaskID != semantic.ID || out.SelectedWork != nil {
		t.Fatalf("workspace=%+v err=%v", out, err)
	}
}
