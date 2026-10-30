package classification_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/classification"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func TestClassifyIsProjectScopedOneWayInactiveAndProvenanced(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 4, 2, 0, 0, 0, time.UTC)
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	projects := sqlite.NewProjectStore(db)
	for _, value := range []project.Project{{ID: "project", Name: "P", CreatedAt: now, UpdatedAt: now}, {ID: "foreign", Name: "F", CreatedAt: now, UpdatedAt: now}} {
		if err := projects.Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	repositories := sqlite.NewRepositoryStore(db)
	if _, _, err := repositories.PutIfAbsent(ctx, repository.Repository{ID: "repo", ProjectID: "project", Name: "R", Path: filepath.Join(t.TempDir(), "repo"), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	modules := sqlite.NewModuleStore(db)
	for _, value := range []module.Module{
		{ID: "unclassified", ProjectID: "project", Key: module.UnclassifiedKey, Name: module.UnclassifiedName, SystemOwned: true, CreatedAt: now, UpdatedAt: now},
		{ID: "core", ProjectID: "project", Key: "core", Name: "Core", Description: "Core logic", CreatedAt: now, UpdatedAt: now},
		{ID: "other", ProjectID: "project", Key: "other", Name: "Other", CreatedAt: now, UpdatedAt: now},
		{ID: "foreign-module", ProjectID: "foreign", Key: "foreign", Name: "Foreign", CreatedAt: now, UpdatedAt: now},
	} {
		if err := modules.Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	sessions := sqlite.NewAISessionStore(db)
	for _, value := range []aisession.Session{{ID: "session-one", ProjectID: "project", CreatedAt: now, UpdatedAt: now}, {ID: "session-two", ProjectID: "project", CreatedAt: now, UpdatedAt: now}, {ID: "foreign-session", ProjectID: "foreign", CreatedAt: now, UpdatedAt: now}} {
		if _, err := db.SQL().ExecContext(ctx, `INSERT INTO ai_sessions(id,project_id,created_at,updated_at,caller_scope) VALUES(?,NULL,?,?,?)`, value.ID, value.CreatedAt.UTC().Format(time.RFC3339Nano), value.UpdatedAt.UTC().Format(time.RFC3339Nano), "classification-"+value.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL().ExecContext(ctx, `UPDATE ai_sessions SET project_id=? WHERE id=?`, value.ProjectID, value.ID); err != nil {
			t.Fatal(err)
		}
	}
	tasks := sqlite.NewTaskStore(db)
	createTask := func(id, moduleID string, status task.Status) {
		t.Helper()
		if err := sqlite.NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: "feature-" + id, ProjectID: "project", CreatedByAISessionID: "session-one", Title: id, ProblemStatement: id, Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "confirmed classification fixture", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckGitAssertion, Command: "git diff --check", Required: true}}, AcceptanceCriteria: []string{"the confirmed diff check passes"}, CodexInstruction: "Verify the completed implementation without modifying files."}
		if err := tasks.Create(ctx, task.Task{ID: id, ProjectID: "project", FeatureAreaID: "feature-" + id, ModuleID: moduleID, RepositoryID: "repo", CodexConfigID: codexconfig.DefaultConfigID, Goal: id, WorkSpec: task.WorkSpec{Goal: id, CodexInstruction: id, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: verification, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: task.StatusActive, Phase: task.PhaseReviewRequired, RequiredReviewDepth: task.ReviewDepthPointer(task.ReviewDepthFast), Type: task.TypeDevelopment, Scope: task.ScopeFeatureArea, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if status == task.StatusSucceeded {
			if _, err := db.SQL().ExecContext(ctx, `UPDATE tasks SET status=?,required_review_depth=NULL WHERE id=?`, status, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A terminal Task shaped like a completed implementation/verification plan
	// remains eligible for the one-way semantic classification operation.
	createTask("shared-task", "unclassified", task.StatusSucceeded)
	createTask("active-task", "unclassified", task.StatusReady)
	createTask("regular-task", "core", task.StatusReady)
	runs := classificationRuns{{ID: "active-run", TaskID: "active-task", Status: run.StatusPreparing}}
	service, err := classification.NewService(sqlite.NewClassificationStore(db), sessions, modules, tasks, runs, func() (string, error) { return "classification", nil }, func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}

	classified, err := service.Classify(ctx, "session-two", "shared-task", "core")
	if err != nil {
		t.Fatal(err)
	}
	if classified.ModuleID != "core" {
		t.Fatalf("classified = %+v", classified)
	}
	var provenanceSession string
	if err := db.SQL().QueryRowContext(ctx, `SELECT ai_session_id FROM task_classifications WHERE task_id='shared-task'`).Scan(&provenanceSession); err != nil || provenanceSession != "session-two" {
		t.Fatalf("provenance=%q err=%v", provenanceSession, err)
	}
	events, err := sqlite.NewEventStore(db).List(ctx, event.Filter{TaskID: "shared-task"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range events {
		if value.Type == event.TaskClassified {
			found = true
		}
	}
	if !found {
		t.Fatal("task.classified event not persisted")
	}

	for name, values := range map[string][3]string{
		"regular to regular": {"session-one", "regular-task", "other"},
		"active run":         {"session-one", "active-task", "core"},
		"foreign session":    {"foreign-session", "active-task", "foreign-module"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.Classify(ctx, values[0], values[1], values[2])
			if !errors.Is(err, classification.ErrConflict) && !errors.Is(err, classification.ErrNotFound) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

type classificationRuns []run.Run

func (r classificationRuns) List(context.Context) ([]run.Run, error) {
	return append([]run.Run(nil), r...), nil
}
