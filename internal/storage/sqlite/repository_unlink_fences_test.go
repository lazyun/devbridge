package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

func TestRepositoryUnlinkFencesStaleProjectContextAndModuleTopology(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 9, 17, 4, 0, 0, 0, time.UTC)
	if err := NewProjectStore(db).Create(ctx, project.Project{ID: "fence-project", Name: "Fence", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repos := NewRepositoryStore(db)
	for _, value := range []repository.Repository{
		{ID: "fence-app", ProjectID: "fence-project", Name: "App", Path: "/fence/app", CreatedAt: now, UpdatedAt: now},
		{ID: "fence-sdk", ProjectID: "fence-project", Name: "SDK", Path: "/fence/sdk", CreatedAt: now, UpdatedAt: now},
	} {
		if _, _, err := repos.PutIfAbsent(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	contexts := NewProjectContextStore(db)
	if _, err := contexts.EnsureRequired(ctx, "fence-project", now); err != nil {
		t.Fatal(err)
	}
	staleTopology, err := repos.TopologyRevision(ctx, "fence-project")
	if err != nil {
		t.Fatal(err)
	}
	modules := NewModuleStore(db)
	oldMap := []module.Module{
		{ID: "module-app", ProjectID: "fence-project", Key: "app", Name: "App", CreatedAt: now, UpdatedAt: now, ProductionFacts: []module.ProductionFacts{{RepositoryID: "fence-app", Responsibility: "application", ResponsibilitySource: module.ResponsibilityDerived, ProductionRoots: []string{"cmd/app"}, Entrypoints: []string{"cmd/app/main.go"}, RelatedModules: []string{}}}},
		{ID: "module-sdk", ProjectID: "fence-project", Key: "sdk", Name: "SDK", CreatedAt: now, UpdatedAt: now, ProductionFacts: []module.ProductionFacts{{RepositoryID: "fence-sdk", Responsibility: "library", ResponsibilitySource: module.ResponsibilityDerived, ProductionRoots: []string{"pkg/sdk"}, Entrypoints: []string{"pkg/sdk/sdk.go"}, RelatedModules: []string{}}}},
	}
	if err := modules.ReplaceContextMapAtTopology(ctx, "fence-project", oldMap, now, staleTopology); err != nil {
		t.Fatal(err)
	}
	if _, err := contexts.MarkBootstrappedAtTopology(ctx, "fence-project", projectcontext.StatusReady, "old-fingerprint", "old-identity", "old guidance", []string{"old snapshot"}, staleTopology, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	unlinks := NewRepositoryUnlinkStore(db)
	prepared, err := unlinks.Eligibility(ctx, "fence-project", "fence-app")
	if err != nil || !prepared.CanUnlink {
		t.Fatalf("eligibility=%+v err=%v", prepared, err)
	}
	if _, err = unlinks.Commit(ctx, repositoryunlink.CommitInput{RequestID: "unlink-app", ProjectID: "fence-project", RepositoryID: "fence-app", ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.ProjectTopologyRevision, Reason: "remove app"}); err != nil {
		t.Fatal(err)
	}

	if err := modules.ReplaceContextMapAtTopology(ctx, "fence-project", oldMap, now.Add(2*time.Second), staleTopology); !errors.Is(err, projectcontext.ErrConflict) {
		t.Fatalf("stale module map error=%v", err)
	}
	if _, err := contexts.MarkBootstrappedAtTopology(ctx, "fence-project", projectcontext.StatusReady, "old-fingerprint", "old-identity", "old guidance", []string{"old snapshot"}, staleTopology, now.Add(2*time.Second)); !errors.Is(err, projectcontext.ErrConflict) {
		t.Fatalf("stale context error=%v", err)
	}
	invalidated, err := contexts.Get(ctx, "fence-project")
	if err != nil || invalidated.Status != projectcontext.StatusRequired || invalidated.ContextFingerprint != "" || invalidated.Result != "" {
		t.Fatalf("invalidated context=%+v err=%v", invalidated, err)
	}

	activeTopology, err := repos.TopologyRevision(ctx, "fence-project")
	if err != nil || activeTopology != staleTopology+1 {
		t.Fatalf("active topology=%d err=%v", activeTopology, err)
	}
	newMap := []module.Module{oldMap[1]}
	if err := modules.ReplaceContextMapAtTopology(ctx, "fence-project", newMap, now.Add(3*time.Second), activeTopology); err != nil {
		t.Fatal(err)
	}
	if _, err := contexts.MarkBootstrappedAtTopology(ctx, "fence-project", projectcontext.StatusReady, "sdk-fingerprint", "sdk-identity", "sdk guidance", []string{"sdk snapshot"}, activeTopology, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	current, err := modules.ListByProject(ctx, "fence-project")
	if err != nil || len(current) != 2 || current[0].Key != "app" || len(current[0].ProductionFacts) != 0 || current[1].Key != "sdk" || len(current[1].ProductionFacts) != 1 || current[1].ProductionFacts[0].RepositoryID != "fence-sdk" {
		t.Fatalf("current module map=%+v err=%v", current, err)
	}
	historical, err := repos.Get(ctx, "fence-app")
	if err != nil || historical.RegistrationState != repository.RegistrationUnlinked || historical.UnlinkRequestID != "unlink-app" || historical.UnlinkedAt == nil {
		t.Fatalf("historical repository=%+v err=%v", historical, err)
	}
}

func TestRepositoryUnlinkRejectsOldEngineeringWriterAndConfirmationWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 9, 17, 5, 0, 0, 0, time.UTC)
	if err := NewProjectStore(db).Create(ctx, project.Project{ID: "writer-project", Name: "Writer", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repos := NewRepositoryStore(db)
	if _, _, err := repos.PutIfAbsent(ctx, repository.Repository{ID: "writer-repository", ProjectID: "writer-project", Name: "Writer", Path: "/writer", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := NewModuleStore(db).Create(ctx, module.Module{ID: "writer-module", ProjectID: "writer-project", Key: "writer", Name: "Writer", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sessions := NewAISessionStore(db)
	if _, err := sessions.InitializeSession(ctx, "writer-fixture", "writer-initialize", "writer-session", now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.BindSessionProject(ctx, "writer-fixture", "writer-bind", "writer-session", "writer-project", "writer-selection", now); err != nil {
		t.Fatal(err)
	}
	if err := NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: "writer-area", ProjectID: "writer-project", CreatedByAISessionID: "writer-session", Title: "Writer", ProblemStatement: "Writer", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := NewTaskStore(db).Create(ctx, task.Task{ID: "writer-task", Type: task.TypeDevelopment, Scope: task.ScopeFeatureArea, ProjectID: "writer-project", FeatureAreaID: "writer-area", ModuleID: "writer-module", RepositoryID: "writer-repository", CodexConfigID: codexconfig.DefaultConfigID, Goal: "historical task", WorkSpec: task.WorkSpec{Goal: "historical task", CodexInstruction: "Implement the approved task.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waives verification"}}, RequiredReviewDepth: task.ReviewDepthPointer(task.ReviewDepthFast), Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	plans := NewTaskPlanStore(db)
	if err := plans.Create(ctx, taskplan.Plan{ID: "writer-plan", AISessionID: "writer-session", ProjectID: "writer-project", TaskID: "writer-task", Title: "old confirmed plan", WorkSpec: task.WorkSpec{Goal: "historical task", CodexInstruction: "Implement the approved task.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waives verification"}}, Status: taskplan.StatusPendingConfirmation, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE tasks SET status='CANCELED',updated_at=? WHERE id=?`, formatTime(now.Add(time.Second)), "writer-task"); err != nil {
		t.Fatal(err)
	}
	prepared, err := NewRepositoryUnlinkStore(db).Eligibility(ctx, "writer-project", "writer-repository")
	if err != nil || !prepared.CanUnlink {
		t.Fatalf("eligibility=%+v err=%v", prepared, err)
	}
	if _, err = NewRepositoryUnlinkStore(db).Commit(ctx, repositoryunlink.CommitInput{RequestID: "unlink-writer", ProjectID: "writer-project", RepositoryID: "writer-repository", ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.ProjectTopologyRevision, Reason: "retire writer"}); err != nil {
		t.Fatal(err)
	}

	if _, err := NewDevelopmentCycleStore(db).Select(ctx, developmentcycle.Cycle{ID: "late-cycle", ProjectID: "writer-project", RepositoryID: "writer-repository", DevelopmentRef: "refs/heads/main", SelectedHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RequestID: "late-cycle"}); !errors.Is(err, repository.ErrRepositoryUnlinked) {
		t.Fatalf("late engineering writer error=%v", err)
	}
	if _, err := plans.Confirm(ctx, "writer-plan", now.Add(time.Second)); err == nil {
		t.Fatalf("late confirmation error=%v", err)
	}
	plan, err := plans.Get(ctx, "writer-plan")
	if err != nil || plan.Status != taskplan.StatusPendingConfirmation || plan.ConfirmedAt != nil {
		t.Fatalf("plan side effect=%+v err=%v", plan, err)
	}
	var cycles, events int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM development_cycles WHERE project_id='writer-project'`).Scan(&cycles); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE project_id='writer-project' AND type IN ('development_cycle.selected','task_plan.confirmed')`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if cycles != 0 || events != 0 {
		t.Fatalf("late writer committed cycles=%d events=%d", cycles, events)
	}
}

func TestRepositoryUnlinkRequestIDConflictVariantsPreserveOriginalResult(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 9, 17, 6, 0, 0, 0, time.UTC)
	if err := NewProjectStore(db).Create(ctx, project.Project{ID: "receipt-project", Name: "Receipt", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repos := NewRepositoryStore(db)
	for _, value := range []repository.Repository{
		{ID: "receipt-one", ProjectID: "receipt-project", Name: "One", Path: "/receipt/one", CreatedAt: now, UpdatedAt: now},
		{ID: "receipt-two", ProjectID: "receipt-project", Name: "Two", Path: "/receipt/two", CreatedAt: now, UpdatedAt: now},
	} {
		if _, _, err := repos.PutIfAbsent(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	store := NewRepositoryUnlinkStore(db)
	prepared, err := store.Eligibility(ctx, "receipt-project", "receipt-one")
	if err != nil || !prepared.CanUnlink {
		t.Fatalf("eligibility=%+v err=%v", prepared, err)
	}
	original := repositoryunlink.CommitInput{RequestID: "same-request", ProjectID: "receipt-project", RepositoryID: "receipt-one", ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.ProjectTopologyRevision, Reason: "original reason"}
	first, err := store.Commit(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.sql.QueryRowContext(ctx, `SELECT result_json FROM project_repository_unlink_receipts WHERE request_id=?`, original.RequestID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []repositoryunlink.CommitInput{
		{RequestID: original.RequestID, ProjectID: original.ProjectID, RepositoryID: "receipt-two", ExpectedGeneration: 1, ExpectedTopologyRevision: 2, Reason: original.Reason},
		{RequestID: original.RequestID, ProjectID: original.ProjectID, RepositoryID: original.RepositoryID, ExpectedGeneration: original.ExpectedGeneration + 1, ExpectedTopologyRevision: original.ExpectedTopologyRevision, Reason: original.Reason},
		{RequestID: original.RequestID, ProjectID: original.ProjectID, RepositoryID: original.RepositoryID, ExpectedGeneration: original.ExpectedGeneration, ExpectedTopologyRevision: original.ExpectedTopologyRevision, Reason: "changed reason"},
	} {
		if _, err := store.Commit(ctx, changed); !errors.Is(err, repositoryunlink.ErrRequestConflict) {
			t.Fatalf("changed=%+v err=%v", changed, err)
		}
	}
	var after string
	if err := db.sql.QueryRowContext(ctx, `SELECT result_json FROM project_repository_unlink_receipts WHERE request_id=?`, original.RequestID).Scan(&after); err != nil || after != before {
		t.Fatalf("receipt changed before=%q after=%q err=%v", before, after, err)
	}
	one, err := repos.Get(ctx, "receipt-one")
	if err != nil || one.RegistrationState != repository.RegistrationUnlinked || one.RegistrationGeneration != first.RegistrationGeneration {
		t.Fatalf("original result changed=%+v err=%v", one, err)
	}
	two, err := repos.Get(ctx, "receipt-two")
	if err != nil || two.RegistrationState != repository.RegistrationActive || two.RegistrationGeneration != 1 {
		t.Fatalf("other repository changed=%+v err=%v", two, err)
	}
}
