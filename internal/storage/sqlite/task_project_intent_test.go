package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestProjectPlanningIntentPersistsWithoutRepositoryOrAuthorization(t *testing.T) {
	f, initial := projectIntentFixture(t)
	ctx := context.Background()
	store := NewTaskStore(f.db)
	service := task.NewService(store, nil, NewModuleStore(f.db), nil, func() (string, error) { return "project-planning-task", nil }, nil)
	input := task.CreateInput{AISessionID: f.session, RequestID: "project-planning", ProjectID: f.project, FeatureAreaID: f.area, ModuleID: initial.ModuleID, Goal: "determine repositories later"}
	created, err := service.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.RepositoryID != "" || created.BaseCommit != "" || created.CurrentDefinitionRevisionID == "" || created.ReviewApprovalID != "" || created.CurrentExecutionID != "" {
		t.Fatalf("planning authority: %+v", created)
	}
	replay, err := service.Create(ctx, input)
	if err != nil || replay.ID != created.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var count int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM tasks WHERE id=? AND repository_id IS NULL AND base_commit=''`, created.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("planning storage=%d %v", count, err)
	}
	for _, table := range []string{"reviews", "runs", "execution_proposals", "review_grants"} {
		if ownedCount(t, f, table) != 0 {
			t.Fatalf("planning created %s", table)
		}
	}
	input.Goal = "different payload"
	if _, err = service.Create(ctx, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay conflict=%v", err)
	}
}

func TestReviewTargetsRejectBeforeDurableSideEffects(t *testing.T) {
	f, _ := projectIntentFixture(t)
	candidate := f.candidate
	// A malformed target set is rejected before admission. Valid frozen V2
	// targets are supported and must reach the dedicated multi-repository path.
	candidate.RepositoryTargets = []workset.Target{{RepositoryID: "", Read: true}}
	if _, err := f.reviews.Prepare(candidate); err == nil {
		t.Fatalf("prepare=%v", err)
	}
	if err := NewReviewStore(f.db).Create(context.Background(), candidate); err == nil {
		t.Fatalf("store bypass=%v", err)
	}
	if ownedCount(t, f, "reviews") != 0 || ownedCount(t, f, "runs") != 0 {
		t.Fatal("unsupported review persisted authority")
	}
}

// This fixture deliberately has zero repositories and uses the production
// initialize/bind protocol rather than the removed project-first Session API.
func projectIntentFixture(t *testing.T) (*reviewApprovalFixture, task.Task) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { db.Close() })
	p := project.Project{ID: "empty-project", Name: "Empty", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	sessions := NewAISessionStore(db)
	if _, err := sessions.InitializeSession(ctx, "test-scope", "initialize", "session", testNow); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.BindSessionProject(ctx, "test-scope", "bind", "session", p.ID, "selection", testNow); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "module", ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "area", ProjectID: p.ID, CreatedByAISessionID: "session", Title: "Investigate", ProblemStatement: "Determine scope", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	reviews, err := review.NewService(NewReviewStore(db), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &reviewApprovalFixture{db: db, project: p.ID, area: area.ID, session: "session", reviews: reviews}, task.Task{ModuleID: m.ID}
}
