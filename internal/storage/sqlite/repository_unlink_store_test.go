package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workspace"
)

func TestRepositoryUnlinkAtomicReplayAndHistoricalRead(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 9, 17, 3, 0, 0, 0, time.UTC)
	if err := NewProjectStore(db).Create(ctx, project.Project{ID: "unlink-project", Name: "Unlink", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repos := NewRepositoryStore(db)
	if _, _, err := repos.PutIfAbsent(ctx, repository.Repository{ID: "unlink-repository", ProjectID: "unlink-project", Name: "Source", Path: "/unlink/source", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDevelopmentCycleStore(db).Select(ctx, developmentcycle.Cycle{ID: "unlink-cycle", ProjectID: "unlink-project", RepositoryID: "unlink-repository", DevelopmentRef: "refs/heads/main", SelectedHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestID: "unlink-cycle"}); err != nil {
		t.Fatal(err)
	}
	store := NewRepositoryUnlinkStore(db)
	prepared, err := store.Eligibility(ctx, "unlink-project", "unlink-repository")
	if err != nil || !prepared.CanUnlink || prepared.RegistrationGeneration != 1 {
		t.Fatalf("prepare=%+v err=%v", prepared, err)
	}
	in := repositoryunlink.CommitInput{RequestID: "unlink-request", ProjectID: "unlink-project", RepositoryID: "unlink-repository", ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.ProjectTopologyRevision, Reason: "remove active resource"}
	first, err := store.Commit(ctx, in)
	if err != nil || first.RegistrationState != repository.RegistrationUnlinked || first.RegistrationGeneration != 2 || first.ProjectTopologyRevision != prepared.ProjectTopologyRevision+1 {
		t.Fatalf("commit=%+v err=%v", first, err)
	}
	replay, err := store.Commit(ctx, in)
	if err != nil || replay.RegistrationGeneration != first.RegistrationGeneration || replay.ProjectTopologyRevision != first.ProjectTopologyRevision {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if _, err := store.Commit(ctx, repositoryunlink.CommitInput{RequestID: in.RequestID, ProjectID: in.ProjectID, RepositoryID: in.RepositoryID, ExpectedGeneration: in.ExpectedGeneration, ExpectedTopologyRevision: in.ExpectedTopologyRevision, Reason: "different"}); !errors.Is(err, repositoryunlink.ErrRequestConflict) {
		t.Fatalf("conflict=%v", err)
	}
	historical, err := repos.Get(ctx, "unlink-repository")
	if err != nil || historical.RegistrationState != repository.RegistrationUnlinked || historical.UnlinkedAt == nil {
		t.Fatalf("historical=%+v err=%v", historical, err)
	}
	active, err := repos.List(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	all, err := repos.ListAll(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	if cycle, err := NewDevelopmentCycleStore(db).Current(ctx, "unlink-project", "unlink-repository"); !errors.Is(err, developmentcycle.ErrNotSelected) || cycle.ID != "" {
		t.Fatalf("cycle=%+v err=%v", cycle, err)
	}
}

func TestRepositoryUnlinkReviewBlockerExcludesTerminalHistory(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "unlink-review")
	repositoryID := "agent-repo-unlink-review"
	v := review.Review{ID: "review_unlink_history", TaskID: f.task.ID, ProjectID: f.project.ID, FeatureAreaID: f.area.ID, CreatedByAISessionID: f.session.ID, AuthorKind: string(review.AuthorAISession), AuthorID: f.session.ID, RepositoryID: repositoryID, BaseCommit: f.task.BaseCommit, CreatedAt: testNow, ReviewDepth: task.ReviewDepthFast, ExecutorKind: review.ExecutorUpperLLM, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "review", Instruction: "inspect"}
	if err := NewReviewStore(f.db).Create(ctx, v); err != nil {
		t.Fatal(err)
	}
	active, err := NewRunStore(f.db).CreateProductScopeReviewAttempt(ctx, v.ID, "unlink-review-run", v.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewReviewStore(f.db).LinkRun(ctx, v.ID, active.ID); err != nil {
		t.Fatal(err)
	}
	eligible, err := NewRepositoryUnlinkStore(f.db).Eligibility(ctx, f.project.ID, repositoryID)
	if err != nil || eligible.CanUnlink {
		t.Fatalf("active review=%+v err=%v", eligible, err)
	}
	if _, err := NewRunStore(f.db).Update(ctx, active.ID, func(r *run.Run) error { r.Status = run.StatusInfrastructureFailed; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE id=?`, formatTime(testNow.Add(time.Minute)), f.task.ID); err != nil {
		t.Fatal(err)
	}
	eligible, err = NewRepositoryUnlinkStore(f.db).Eligibility(ctx, f.project.ID, repositoryID)
	if err != nil || !eligible.CanUnlink {
		t.Fatalf("terminal history=%+v err=%v", eligible, err)
	}
}

func TestRepositoryUnlinkDecomposedWorkspaceIsHistorical(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "unlink-decomposed")
	repositoryID := "agent-repo-unlink-decomposed"
	areaID := "area-unlink-decomposed-history"
	workspaceID := "workspace-unlink-decomposed"
	if err := NewFeatureAreaStore(f.db).Create(ctx, featurearea.FeatureArea{ID: areaID, ProjectID: f.project.ID, CreatedByAISessionID: f.session.ID, Title: "Historical split parent", ProblemStatement: "Workspace history must not block unlink", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := NewWorkspaceStore(f.db).Create(ctx, workspace.Workspace{ID: workspaceID, ProjectID: f.project.ID, RepositoryID: repositoryID, FeatureAreaID: areaID, OwnerKind: workspace.OwnerFeatureArea, OwnerID: areaID, BaseCommit: f.task.BaseCommit, Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	active, err := NewRepositoryUnlinkStore(f.db).Eligibility(ctx, f.project.ID, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	var activeWorkspace bool
	for _, blocker := range active.Blockers {
		activeWorkspace = activeWorkspace || blocker.EntityType == "workspace_member"
	}
	if !activeWorkspace {
		t.Fatalf("active workspace member not blocked: %+v", active.Blockers)
	}
	child := featurearea.FeatureArea{ID: "area-unlink-decomposed-child", ProjectID: f.project.ID, CreatedByAISessionID: f.session.ID, Title: "Split child", ProblemStatement: "Current successor capability", Status: featurearea.StatusDiscovering, CreatedAt: testNow.Add(time.Minute), UpdatedAt: testNow.Add(time.Minute)}
	if _, err = NewFeatureAreaStore(f.db).ApplyLifecycle(ctx, featurearea.LifecycleMutation{ProjectID: f.project.ID, AISessionID: f.session.ID, FeatureAreaID: areaID, RequestID: "decompose-unlink-history", PayloadSHA256: strings.Repeat("a", 64), Action: featurearea.ActionDecompose, Children: []featurearea.FeatureArea{child}, At: testNow.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE id=?`, formatTime(testNow.Add(time.Minute)), f.task.ID); err != nil {
		t.Fatal(err)
	}
	historical, err := NewRepositoryUnlinkStore(f.db).Eligibility(ctx, f.project.ID, repositoryID)
	if err != nil || !historical.CanUnlink {
		t.Fatalf("decomposed workspace should be history: %+v err=%v", historical, err)
	}
	for _, blocker := range historical.Blockers {
		if blocker.EntityType == "workspace_member" {
			t.Fatalf("decomposed workspace remained a blocker: %+v", blocker)
		}
	}
	if _, err = repositoryunlink.NewService(NewRepositoryUnlinkStore(f.db)).Commit(ctx, repositoryunlink.CommitInput{RequestID: "unlink-decomposed", ProjectID: f.project.ID, RepositoryID: repositoryID, ExpectedGeneration: historical.RegistrationGeneration, ExpectedTopologyRevision: historical.ProjectTopologyRevision, Reason: "remove active resource"}); err != nil {
		t.Fatal(err)
	}
	if retained, err := NewWorkspaceStore(f.db).Get(ctx, workspaceID); err != nil || retained.ID != workspaceID {
		t.Fatalf("historical workspace=%+v err=%v", retained, err)
	}
}
