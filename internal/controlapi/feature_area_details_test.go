package controlapi_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workspace"
)

func TestFeatureAreaDetailsForSessionReadsExactHistoryWithoutMutation(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	semantic := f.createTask(t)
	reviewValue := f.codingReview(t, review.Review{ReviewDepth: "DEEP", TaskID: semantic.ID, ID: "review_details", ProjectID: f.projectID, FeatureAreaID: f.areaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EngineeringGoal: "exact review", Focus: review.Focus{Kind: review.FocusRepository}, CreatedAt: f.now})
	if err := sqlite.NewReviewStore(f.plane.DB).Create(ctx, reviewValue); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.DB.SQL().ExecContext(ctx, `UPDATE reviews SET final_codex_prompt='immutable review prompt',prompt_sha256='b6f7d4a6e135c14e49dba96a9f772430ed0cc5fd0b79e4bd66fd4e5e5f1ae4f9',prompt_bytes=23,prompt_sent_at=? WHERE id=?`, f.now.Format(time.RFC3339Nano), reviewValue.ID); err != nil {
		t.Fatal(err)
	}
	before := featureAreaPresentationCounts(t, ctx, f)
	details, err := f.facade.FeatureAreaDetailsForSession(ctx, f.sessionID, f.areaID, controlapi.ListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	assertFeatureAreaPresentationCounts(t, ctx, f, before)
	if len(details.Reviews) != 1 || details.Reviews[0].ID != reviewValue.ID || details.Reviews[0].FinalCodexPrompt != "immutable review prompt" {
		t.Fatalf("review prompt/history lost: %+v", details.Reviews)
	}
	if len(details.Tasks) != 1 {
		t.Fatalf("task/execution identities lost: %+v", details)
	}
	if details.Runs == nil {
		t.Fatal("runs collection must remain explicit even when no history exists")
	}
	if details.RecoveryConversations == nil {
		t.Fatal("recovery conversation history must be an explicit read collection")
	}
}

func TestFeatureAreaDetailsForSessionRejectsForeignProject(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	foreign, err := f.plane.Projects.Create(ctx, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	sessionID := f.bindProject(t, foreign.ID, "foreign-details")
	_, err = f.facade.FeatureAreaDetailsForSession(ctx, sessionID, f.areaID, controlapi.ListOptions{Limit: 1})
	var apiErr *controlapi.Error
	if !errors.As(err, &apiErr) || apiErr.Code != controlapi.CodeNotFound {
		t.Fatalf("err=%v, want not found", err)
	}
}

func TestFeatureAreaDetailsForSessionPagesPastOneHundredAcrossFeatureAreas(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	foreignArea := featurearea.FeatureArea{ID: "foreign-history-area", ProjectID: f.projectID, CreatedByAISessionID: f.sessionID, Title: "Foreign history", ProblemStatement: "must remain filtered", Status: featurearea.StatusActive, CreatedAt: f.now, UpdatedAt: f.now}
	if err := sqlite.NewFeatureAreaStore(f.plane.DB).Create(ctx, foreignArea); err != nil {
		t.Fatal(err)
	}
	tasks := sqlite.NewTaskStore(f.plane.DB)

	for i := 0; i < 105; i++ {
		for _, areaID := range []string{f.areaID, foreignArea.ID} {
			prefix := "owned"
			if areaID == foreignArea.ID {
				prefix = "foreign"
			}
			taskID := fmt.Sprintf("%s-history-task-%03d", prefix, i)
			created := f.now.Add(time.Duration(i) * time.Second)
			value := task.Task{ID: taskID, ProjectID: f.projectID, FeatureAreaID: areaID, RepositoryID: f.repositoryID, ModuleID: f.moduleID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: taskID, Goal: taskID, Status: task.StatusActive, Phase: task.PhaseReviewRequired, Model: "gpt-5.6-luna", ReasoningEffort: "low", CreatedAt: created, UpdatedAt: created}
			if err := tasks.Create(ctx, value); err != nil {
				t.Fatalf("create task %s: %v", taskID, err)
			}

		}
	}
	before := featureAreaPresentationCounts(t, ctx, f)
	firstPage, err := f.facade.FeatureAreaDetailsForSession(ctx, f.sessionID, f.areaID, controlapi.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"tasks"} {
		if !firstPage.SectionHasMore[section] {
			t.Fatalf("%s did not advertise history beyond the first 100: %+v", section, firstPage.SectionHasMore)
		}
	}
	details, err := f.facade.FeatureAreaDetailsForSession(ctx, f.sessionID, f.areaID, controlapi.ListOptions{Limit: 10, Offset: 100})
	if err != nil {
		t.Fatal(err)
	}
	assertFeatureAreaPresentationCounts(t, ctx, f, before)
	if details.SectionTotals["tasks"] != 105 || details.SectionTotals["taskPlans"] != 0 || details.SectionTotals["taskDefinitionRevisions"] != 0 {
		t.Fatalf("unfiltered or truncated totals: %+v", details.SectionTotals)
	}
	if len(details.Tasks) != 5 || len(details.TaskPlans) != 0 || len(details.TaskDefinitionRevisions) != 0 || !details.HasPrevious || details.HasNext {
		t.Fatalf("second page was not bounded consistently: tasks=%d plans=%d revisions=%d previous=%v next=%v", len(details.Tasks), len(details.TaskPlans), len(details.TaskDefinitionRevisions), details.HasPrevious, details.HasNext)
	}
	for _, value := range details.Tasks {
		if value.FeatureAreaID != f.areaID {
			t.Fatalf("foreign task escaped FeatureArea filter: %+v", value)
		}
	}
	for _, value := range details.TaskPlans {
		if value.ProjectID != f.projectID || value.TaskID[:len("owned-")] != "owned-" {
			t.Fatalf("foreign plan escaped FeatureArea filter: %+v", value)
		}
	}
}

func TestFeatureAreaDetailsForSessionReadsExactQuarantinedWorkspace(t *testing.T) {
	f := newOwnershipWorkflowFixture(t, featurearea.StatusActive)
	ctx := f.ctx
	initializePresentationRepository(t, f)
	semantic := f.createTask(t)
	reviewValue := f.codingReview(t, review.Review{ReviewDepth: "DEEP", TaskID: semantic.ID, ID: "review_quarantined_workspace", ProjectID: f.projectID, FeatureAreaID: f.areaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EngineeringGoal: "preserve unsafe workspace", Focus: review.Focus{Kind: review.FocusRepository}, CreatedAt: f.now})
	if err := sqlite.NewReviewStore(f.plane.DB).Create(ctx, reviewValue); err != nil {
		t.Fatal(err)
	}
	runValue, err := sqlite.NewRunStore(f.plane.DB).CreateProductScopeReviewAttempt(ctx, reviewValue.ID, "run_quarantined_workspace", "inspect", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	workspaceValue, err := f.plane.Workspaces.EnsureForFeatureArea(ctx, reviewValue.ProjectID, reviewValue.RepositoryID, reviewValue.FeatureAreaID, reviewValue.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.Runs.Transition(ctx, runValue.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.Runs.AttachWorkspace(ctx, runValue.ID, workspaceValue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().ExecContext(ctx, `UPDATE workspaces SET runtime_quarantined=1,status=?,updated_at=? WHERE id=?`, workspace.StatusContinuityUnavailable, f.now.Add(time.Second).Format(time.RFC3339Nano), workspaceValue.ID); err != nil {
		t.Fatal(err)
	}
	before := featureAreaPresentationCounts(t, ctx, f)
	details, err := f.facade.FeatureAreaDetailsForSession(ctx, f.sessionID, f.areaID, controlapi.ListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	assertFeatureAreaPresentationCounts(t, ctx, f, before)
	if len(details.Workspaces) != 1 || details.Workspaces[0].ID != workspaceValue.ID || details.Workspaces[0].FeatureAreaID != reviewValue.FeatureAreaID || !details.Workspaces[0].RuntimeQuarantined || details.Workspaces[0].Status != workspace.StatusContinuityUnavailable {
		t.Fatalf("quarantined exact workspace history lost: %+v", details.Workspaces)
	}
	if details.SectionTotals["workspaces"] != 1 || details.SectionHasMore["workspaces"] {
		t.Fatalf("workspace pagination metadata=%+v/%+v", details.SectionTotals, details.SectionHasMore)
	}
}
