package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
	_ "modernc.org/sqlite"
)

var testNow = time.Date(2026, 9, 2, 1, 2, 3, 456, time.UTC)

var currentSchemaFixture struct {
	sync.Once
	image []byte
	err   error
}

// seedBoundSession installs a historical, already-bound Session for storage
// tests. Production creation is intentionally closed at AISessionStore.Create;
// these tests exercise stores below the Session initialization boundary and
// therefore seed the durable row explicitly.
func seedBoundSession(t *testing.T, db *DB, value aisession.Session) {
	t.Helper()
	if value.ID == "" || value.ProjectID == "" || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() {
		t.Fatalf("invalid bound Session fixture: %+v", value)
	}
	scope := "storage-fixture-" + value.ID
	if _, err := db.sql.ExecContext(context.Background(), `INSERT INTO ai_sessions(id,project_id,created_at,updated_at,caller_scope) VALUES(?,NULL,?,?,?)`, value.ID, formatTime(value.CreatedAt), formatTime(value.UpdatedAt), scope); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(context.Background(), `UPDATE ai_sessions SET project_id=?,updated_at=? WHERE id=? AND project_id IS NULL`, value.ProjectID, formatTime(value.UpdatedAt), value.ID); err != nil {
		t.Fatal(err)
	}
}

func openTestDB(t *testing.T, root string) *DB {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "devbridge.db")); errors.Is(err, os.ErrNotExist) {
		currentSchemaFixture.Do(func() {
			templateRoot, err := os.MkdirTemp("", "devbridge-sqlite-schema-")
			if err != nil {
				currentSchemaFixture.err = err
				return
			}
			defer os.RemoveAll(templateRoot)
			path := filepath.Join(templateRoot, "devbridge.db")
			raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
			if err == nil {
				err = ApplyMigrations(context.Background(), raw, DefaultMigrations())
			}
			if raw != nil {
				err = errors.Join(err, raw.Close())
			}
			if err == nil {
				currentSchemaFixture.image, err = os.ReadFile(path)
			}
			currentSchemaFixture.err = err
		})
		if currentSchemaFixture.err != nil {
			t.Fatalf("build current-schema fixture: %v", currentSchemaFixture.err)
		}
		if err := os.WriteFile(filepath.Join(root, "devbridge.db"), currentSchemaFixture.image, 0o600); err != nil {
			t.Fatalf("install current-schema fixture: %v", err)
		}
	} else if err != nil {
		t.Fatalf("inspect test database: %v", err)
	}
	db, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

type fixture struct {
	project    project.Project
	repository repository.Repository
	task       task.Task
	run        run.Run
}

func seed(t *testing.T, db *DB, suffix string, authorize ...bool) fixture {
	t.Helper()
	ctx := context.Background()
	p := project.Project{ID: "project-" + suffix, Name: "project " + suffix, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := repository.Repository{ID: "repo-" + suffix, ProjectID: p.ID, Name: "repo", Path: filepath.Join(db.stateRoot, "source-"+suffix), CreatedAt: testNow, UpdatedAt: testNow}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, r); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "module-" + suffix, ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	session := aisession.Session{ID: "fixture-session-" + suffix, ProjectID: p.ID, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, db, session)
	area := featurearea.FeatureArea{ID: "fixture-feature-" + suffix, ProjectID: p.ID, CreatedByAISessionID: session.ID, Title: "Fixture capability", ProblemStatement: "Fixture engineering intent", Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{Goal: "goal", Constraints: []string{"bounded"}, CodexInstruction: "goal", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test explicitly waives verification"}}
	taskValue := task.Task{ID: "task-" + suffix, ProjectID: p.ID, FeatureAreaID: area.ID, ModuleID: m.ID, RepositoryID: r.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "goal", Constraints: []string{"bounded"}, WorkSpec: work, VerificationSpec: verification, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "Fixture task", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewTaskStore(db).Create(ctx, taskValue); err != nil {
		t.Fatal(err)
	}
	executionID := seedExecutionOwner(t, db, taskValue, session, "fixture-execution-"+suffix)
	var runValue run.Run
	var err error
	if len(authorize) > 0 && authorize[0] {
		proposals := NewExecutionProposalStore(db)
		_, runValue, err = proposals.ConsumeAndCreateRun(ctx, executionID, "run-"+suffix, testNow)
	} else {
		runValue, err = NewRunStore(db).CreateExecutionAttempt(ctx, taskValue.ID, "run-"+suffix, run.PurposeImplementation, executionID, testNow)
	}
	if err != nil {
		t.Fatal(err)
	}
	taskValue, err = NewTaskStore(db).Get(ctx, taskValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{p, r, taskValue, runValue}
}

func TestMigrationLifecycle(t *testing.T) {
	root := t.TempDir()
	db, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(DefaultMigrations()) {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(DefaultMigrations()) {
		t.Fatalf("second count=%d err=%v", count, err)
	}
	migrations := DefaultMigrations()
	futureVersion := migrations[len(migrations)-1].Version + 1
	if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(?,?)`, futureVersion, formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(context.Background(), root); err == nil {
		t.Fatal("newer schema accepted")
	}
}

func TestMigrationV12ThroughV16AddsScopeExpansionTopicStateAndExecutionUniqueness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade-v12.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	migrations := historicalMigrations()
	if err := ApplyMigrations(context.Background(), raw, migrations[:12]); err != nil {
		t.Fatalf("apply v12: %v", err)
	}
	var absent int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='scope_expansions'`).Scan(&absent); err != nil || absent != 0 {
		t.Fatalf("scope_expansions before v13=%d err=%v", absent, err)
	}
	if err := ApplyMigrations(context.Background(), raw, migrations); err != nil {
		t.Fatalf("upgrade v12 to v15: %v", err)
	}
	var version, present int
	if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != historicalMigrations()[len(historicalMigrations())-1].Version {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='scope_expansions'`).Scan(&present); err != nil || present != 1 {
		t.Fatalf("scope_expansions after v13=%d err=%v", present, err)
	}
	var genericCards int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND (name LIKE '%card%' OR name LIKE '%interaction%')`).Scan(&genericCards); err != nil || genericCards != 0 {
		t.Fatalf("generic UI persistence tables=%d err=%v", genericCards, err)
	}
}

func TestMigrationV13ThroughV15DeterministicallyBackfillsFeatureAreasAndPreservesExecutionProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade-v13.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ctx := context.Background()
	migrations := historicalMigrations()
	if err := ApplyMigrations(ctx, raw, migrations[:13]); err != nil {
		t.Fatalf("apply v13: %v", err)
	}
	stamp := formatTime(testNow)
	work := `{"version":1,"goal":"Historical executable goal","constraints":[],"codexInstruction":"Implement the historical goal.","executionScope":{"kind":"REPOSITORY"}}`
	verification := `{"version":1,"mode":"NONE","waiver":{"source":"USER_WAIVED","reason":"Historical fixture waiver"}}`
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,name,created_at,updated_at) VALUES('v14-project','V14',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('v14-repo','v14-project','Repo','/v14/repo',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('v14-module','v14-project','core','Core',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('v14-session','v14-project',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,work_spec_json,verification_spec_json,problem_statement) VALUES('v14-task','v14-project','v14-module','v14-repo',?,'Historical executable goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',?,?,?,?, 'Historical problem')`, []any{codexconfig.DefaultConfigID, stamp, stamp, work, verification}},
		{`INSERT INTO task_plans(id,ai_session_id,project_id,task_id,work_spec_json,verification_spec_json,status,created_at,updated_at,confirmed_at,applied_at) VALUES('v14-plan','v14-session','v14-project','v14-task',?,?,'APPLIED',?,?,?,?)`, []any{work, verification, stamp, stamp, stamp, stamp}},
		{`UPDATE tasks SET confirmed_task_plan_id='v14-plan' WHERE id='v14-task'`, nil},
		{`INSERT INTO execution_proposals(id,ai_session_id,project_id,task_id,task_plan_id,status,created_at,updated_at,work_spec_json,verification_spec_json,outcome) VALUES('v14-execution','v14-session','v14-project','v14-task','v14-plan','PENDING_CONFIRMATION',?,?,?,?, 'PENDING')`, []any{stamp, stamp, work, verification}},
		{`INSERT INTO runs(id,task_id,attempt,status,created_at,updated_at,purpose,execution_proposal_id,codex_instruction) VALUES('v14-run','v14-task',1,'SUCCEEDED',?,?,'IMPLEMENTATION','v14-execution','Historical implementation instruction')`, []any{stamp, stamp}},
	}
	for _, statement := range statements {
		if _, err := raw.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed v13: %v\n%s", err, statement.query)
		}
	}
	if err := ApplyMigrations(ctx, raw, migrations); err != nil {
		t.Fatalf("upgrade v13 to v15: %v", err)
	}
	var featureAreaID, featureAreaProject, title, problem, status, linkedTopic, planTitle string
	if err := raw.QueryRowContext(ctx, `SELECT id,project_id,title,problem_statement,status FROM feature_areas`).Scan(&featureAreaID, &featureAreaProject, &title, &problem, &status); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT feature_area_id FROM tasks WHERE id='v14-task'`).Scan(&linkedTopic); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT title FROM task_plans WHERE id='v14-plan'`).Scan(&planTitle); err != nil {
		t.Fatal(err)
	}
	if featureAreaID != "featureArea_backfill_v14-task" || linkedTopic != featureAreaID || featureAreaProject != "v14-project" || title != "Historical problem" || problem != "Historical problem" || status != string(featurearea.StatusActive) || planTitle != "Historical executable goal" {
		t.Fatalf("backfill featureArea=%q linked=%q project=%q title=%q problem=%q status=%q planTitle=%q", featureAreaID, linkedTopic, featureAreaProject, title, problem, status, planTitle)
	}
	var plans, executions, runs int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_plans WHERE id='v14-plan' AND task_id='v14-task'`).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_proposals WHERE id='v14-execution' AND task_plan_id='v14-plan' AND task_id='v14-task'`).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id='v14-run' AND task_id='v14-task' AND execution_proposal_id='v14-execution' AND purpose='IMPLEMENTATION' AND codex_instruction='Historical implementation instruction'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if plans != 1 || executions != 1 || runs != 1 {
		t.Fatalf("preserved plans=%d executions=%d runs=%d", plans, executions, runs)
	}
	rows, err := raw.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v14 migration left a foreign-key violation")
	}
}

func TestWorkspaceAndRunContinuitySurviveReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	seeded := seed(t, db, "workspace-reopen")
	worktreeValue := worktree.Worktree{
		ID: "worktree-reopen", RepositoryID: seeded.repository.ID, RunID: seeded.run.ID,
		Path: filepath.Join(root, "worktrees", "workspace-reopen"), Branch: "devbridge/task-workspace-reopen/workspace-reopen",
		BaseCommit: seeded.task.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow,
	}
	if err := NewWorktreeStore(db).Create(ctx, worktreeValue); err != nil {
		t.Fatal(err)
	}
	service := workspace.NewService(NewWorkspaceStore(db), func() (string, error) { return "workspace-reopen", nil }, func() time.Time { return testNow })
	created, err := service.EnsureForFeatureArea(ctx, seeded.task.ProjectID, seeded.task.RepositoryID, seeded.task.FeatureAreaID, seeded.task.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AttachWorktree(ctx, created.ID, worktreeValue.ID); err != nil {
		t.Fatal(err)
	}
	baseline := workspace.TokenCounts{InputTokens: 90, OutputTokens: 10, TotalTokens: 100}
	if _, err := service.RecordTokenBaseline(ctx, created.ID, &baseline); err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow })
	if _, err := runs.Transition(ctx, seeded.run.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AttachWorkspace(ctx, seeded.run.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AttachWorktree(ctx, seeded.run.ID, worktreeValue.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AttachStartSnapshot(ctx, seeded.run.ID, strings.Repeat("1", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AttachThread(ctx, seeded.run.ID, "thread-reopen"); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Transition(ctx, seeded.run.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AttachTurn(ctx, seeded.run.ID, "turn-reopen"); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AttachEndSnapshot(ctx, seeded.run.ID, strings.Repeat("2", 40)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = openTestDB(t, root)
	defer db.Close()
	reopenedWorkspace, err := NewWorkspaceStore(db).GetByFeatureArea(ctx, seeded.task.FeatureAreaID)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedWorkspace.ID != created.ID || reopenedWorkspace.WorktreeID != worktreeValue.ID || reopenedWorkspace.LegacyThreadID != "" || !reopenedWorkspace.TokenBaselineAvailable || reopenedWorkspace.TokenBaseline != baseline {
		t.Fatalf("workspace after reopen=%+v", reopenedWorkspace)
	}
	reopenedRun, err := NewRunStore(db).Get(ctx, seeded.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedRun.WorkspaceID != created.ID || reopenedRun.ThreadID != "thread-reopen" || reopenedRun.TurnID != "turn-reopen" || reopenedRun.StartSnapshot != strings.Repeat("1", 40) || reopenedRun.EndSnapshot != strings.Repeat("2", 40) {
		t.Fatalf("run after reopen=%+v", reopenedRun)
	}
	reopenedService := workspace.NewService(NewWorkspaceStore(db), func() (string, error) { return "workspace-duplicate", nil }, func() time.Time { return testNow })
	if _, err := reopenedService.EnsureForFeatureArea(ctx, seeded.task.ProjectID, seeded.task.RepositoryID, seeded.task.FeatureAreaID, seeded.task.BaseCommit); err != nil {
		t.Fatalf("duplicate workspace after reopen error=%v", err)
	}
}

func TestWorkKindPersistsAcrossReopenAndUpdate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	seeded := seed(t, db, "work-kind")
	value, err := NewTaskStore(db).Get(ctx, seeded.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.WorkKind != "" {
		t.Fatalf("seeded task work kind=%q, want empty", value.WorkKind)
	}
	service := task.NewService(NewTaskStore(db), nil, nil, nil, nil, func() time.Time { return testNow })
	classified, err := service.ClassifyWorkKind(ctx, seeded.task.ID, task.WorkKindBugFix)
	if err != nil {
		t.Fatal(err)
	}
	if classified.WorkKind != task.WorkKindBugFix {
		t.Fatalf("classified work kind=%q", classified.WorkKind)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	reopened, err := NewTaskStore(db).Get(ctx, seeded.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.WorkKind != task.WorkKindBugFix {
		t.Fatalf("work kind after reopen=%q", reopened.WorkKind)
	}
	service = task.NewService(NewTaskStore(db), nil, nil, nil, nil, func() time.Time { return testNow.Add(time.Hour) })
	if _, err := service.ClassifyWorkKind(ctx, seeded.task.ID, ""); err != nil {
		t.Fatalf("clearing work kind: %v", err)
	}
	cleared, err := NewTaskStore(db).Get(ctx, seeded.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.WorkKind != "" {
		t.Fatalf("cleared work kind=%q", cleared.WorkKind)
	}
}

func TestExecutionSelectionPersistsAcrossReopenWithoutFabrication(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	current, _ := revisionTaskFixture(t, db, "execution-selection")
	seeded := fixture{task: current}
	taskValue, err := NewTaskStore(db).Get(ctx, seeded.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Historical rows stay null; Bridge never fabricates old selections.
	if taskValue.Model != "gpt-5.6-luna" || taskValue.ReasoningEffort != "low" {
		t.Fatalf("historical task selection fabricated: %#v", taskValue)
	}
	taskService := task.NewService(NewTaskStore(db), nil, nil, nil, nil, func() time.Time { return testNow })
	classified, err := taskService.ClassifyExecutionSelection(ctx, seeded.task.ID, "gpt-5.6-sol", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	if classified.Model != "gpt-5.6-sol" || classified.ReasoningEffort != "xhigh" {
		t.Fatalf("classification=%+v", classified)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	persisted, err := NewTaskStore(db).Get(ctx, seeded.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Model != "gpt-5.6-sol" || persisted.ReasoningEffort != "xhigh" {
		t.Fatalf("Task selection after reopen=%+v", persisted)
	}
}

func TestReviewSelectionPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	seeded := seed(t, db, "review-selection")
	session := aisession.Session{ID: "session-review-selection", ProjectID: seeded.project.ID, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, db, session)
	featureAreaValue := featurearea.FeatureArea{ID: "featureArea-review-selection", ProjectID: seeded.project.ID, Title: "Review selection", ProblemStatement: "goal", CreatedByAISessionID: session.ID, Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, featureAreaValue); err != nil {
		t.Fatal(err)
	}
	store := NewReviewStore(db)
	owner := seeded.task
	owner.ID = "selection-intent"
	owner.FeatureAreaID = featureAreaValue.ID
	owner.Status, owner.Phase = task.StatusActive, task.PhaseReviewRequired
	owner.CurrentReviewID, owner.CurrentExecutionID, owner.CurrentDefinitionRevisionID, owner.ReviewApprovalID, owner.ConfirmedTaskPlanID = "", "", "", "", ""
	if err := NewTaskStore(db).Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	value := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: owner.ID, ID: "review_selection_reopen", ProjectID: seeded.project.ID, FeatureAreaID: "featureArea-review-selection", CreatedByAISessionID: "session-review-selection", AuthorKind: string(review.AuthorAISession), AuthorID: "session-review-selection", RepositoryID: seeded.task.RepositoryID, CodexConfigID: seeded.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, seeded.task.CodexConfigID), BaseCommit: seeded.task.BaseCommit, Focus: review.Focus{Kind: review.FocusFocused, Summary: "selection", Paths: []string{"a.txt"}}, EngineeringGoal: "goal", Instruction: "instruction", Model: "gpt-5.6-terra", ReasoningEffort: "high", CreatedAt: testNow}
	if err := store.Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	reopened, err := NewReviewStore(db).Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Model != "gpt-5.6-terra" || reopened.ReasoningEffort != "high" {
		t.Fatalf("Review selection after reopen=%+v", reopened)
	}
}

func TestMigrationsUpgradeV3ThroughV9ToV14(t *testing.T) {
	migrations := historicalMigrations()
	for _, startVersion := range []int{3, 4, 5, 6, 7, 8, 9} {
		t.Run(fmt.Sprintf("v%d_to_v14", startVersion), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "upgrade.db")
			raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			ctx := context.Background()
			if err := ApplyMigrations(ctx, raw, migrations[:startVersion]); err != nil {
				t.Fatalf("apply through v%d: %v", startVersion, err)
			}
			stamp := formatTime(testNow)
			if _, err := raw.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('upgrade-project','Upgrade',?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('upgrade-repo','upgrade-project','Repo','/upgrade/repo',?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('upgrade-module','upgrade-project','core','Core',?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at) VALUES('upgrade-task','upgrade-project','upgrade-module','upgrade-repo',?,'Goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','RUNNING',?,?)`, codexconfig.DefaultConfigID, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`INSERT INTO runs(id,task_id,attempt,status,created_at,updated_at) VALUES('upgrade-run','upgrade-task',1,'WAITING_APPROVAL',?,?)`, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if startVersion >= 4 {
				if _, err := raw.Exec(`INSERT INTO project_selection(singleton,project_id,updated_at) VALUES(1,'upgrade-project',?)`, stamp); err != nil {
					t.Fatal(err)
				}
				if _, err := raw.Exec(`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('upgrade-session','upgrade-project',?,?)`, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if startVersion >= 5 {
				if _, err := raw.Exec(`INSERT INTO task_proposals(id,ai_session_id,project_id,kind,module_id,repository_id,codex_config_id,goal,constraints_json,base_revision,status,created_at,updated_at,confirmed_at) VALUES('upgrade-proposal','upgrade-session','upgrade-project','CREATE_TASK','upgrade-module','upgrade-repo',?,'Goal','{"version":1,"items":[]}','main','CONFIRMED',?,?,?)`, codexconfig.DefaultConfigID, stamp, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}

			if err := ApplyMigrations(ctx, raw, migrations); err != nil {
				t.Fatalf("upgrade v%d to v14: %v", startVersion, err)
			}
			var version int
			if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != historicalMigrations()[len(historicalMigrations())-1].Version {
				t.Fatalf("version=%d err=%v", version, err)
			}
			var indexSQL string
			if err := raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='runs_one_active_per_task'`).Scan(&indexSQL); err != nil || !strings.Contains(indexSQL, "WAITING_APPROVAL") {
				t.Fatalf("v6 index=%q err=%v", indexSQL, err)
			}
			var executionProposalTable string
			if err := raw.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='execution_proposals'`).Scan(&executionProposalTable); err != nil || executionProposalTable != "execution_proposals" {
				t.Fatalf("v7 execution proposal table=%q err=%v", executionProposalTable, err)
			}
			var runStatus string
			if err := raw.QueryRow(`SELECT status FROM runs WHERE id='upgrade-run'`).Scan(&runStatus); err != nil || runStatus != "WAITING_APPROVAL" {
				t.Fatalf("run status=%q err=%v", runStatus, err)
			}
			var workSpecJSON, verificationSpecJSON, purpose string
			if err := raw.QueryRow(`SELECT work_spec_json,verification_spec_json FROM tasks WHERE id='upgrade-task'`).Scan(&workSpecJSON, &verificationSpecJSON); err != nil || !strings.Contains(workSpecJSON, `"codexInstruction":"Goal"`) || !strings.Contains(workSpecJSON, `"kind":"LEGACY_UNSPECIFIED"`) || !strings.Contains(verificationSpecJSON, `"mode":"NONE"`) || !strings.Contains(verificationSpecJSON, `"source":"LEGACY_UNSPECIFIED"`) {
				t.Fatalf("v10 task specs work=%q verification=%q err=%v", workSpecJSON, verificationSpecJSON, err)
			}
			if err := raw.QueryRow(`SELECT purpose FROM runs WHERE id='upgrade-run'`).Scan(&purpose); err != nil || purpose != "IMPLEMENTATION" {
				t.Fatalf("v10 run purpose=%q err=%v", purpose, err)
			}
			if startVersion >= 4 {
				var projectID string
				if err := raw.QueryRow(`SELECT project_id FROM ai_sessions WHERE id='upgrade-session'`).Scan(&projectID); err != nil || projectID != "upgrade-project" {
					t.Fatalf("AI session project=%q err=%v", projectID, err)
				}
			}
			if startVersion >= 5 {
				var status string
				if err := raw.QueryRow(`SELECT status FROM task_proposals WHERE id='upgrade-proposal'`).Scan(&status); err != nil || status != "CONFIRMED" {
					t.Fatalf("proposal status=%q err=%v", status, err)
				}
			}
		})
	}
}

func TestMigrationV10BackfillsAppliedTaskPlanAndExecutionProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v10.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ctx := context.Background()
	migrations := historicalMigrations()
	if err := ApplyMigrations(ctx, raw, migrations[:10]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	work := `{"version":1,"goal":"Legacy goal","constraints":[],"codexInstruction":"Legacy exact instruction","executionScope":{"kind":"REPOSITORY"}}`
	verification := `{"version":1,"mode":"NONE","waiver":{"source":"USER_WAIVED","reason":"legacy user waiver"}}`
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Project',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('r','p','Repo','/tmp/repo',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('m','p','core','Core',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,work_spec_json,verification_spec_json) VALUES('t','p','m','r',?,'Legacy goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',?,?,?,?)`, []any{codexconfig.DefaultConfigID, stamp, stamp, work, verification}},
		{`INSERT INTO task_proposals(id,ai_session_id,project_id,kind,module_id,repository_id,codex_config_id,goal,constraints_json,base_revision,status,created_at,updated_at,work_spec_json,verification_spec_json) VALUES('tp','s','p','CREATE_TASK','m','r',?,'Legacy goal','{"version":1,"items":[]}','main','PENDING_CONFIRMATION',?,?,?,?)`, []any{codexconfig.DefaultConfigID, stamp, stamp, work, verification}},
	}
	for _, statement := range statements {
		if _, err := raw.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed v10: %v", err)
		}
	}
	if _, err := raw.ExecContext(ctx, `UPDATE task_proposals SET status='CONFIRMED',confirmed_at=?,updated_at=? WHERE id='tp'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE task_proposals SET status='CONSUMED',result_task_id='t',consumed_at=?,updated_at=? WHERE id='tp'`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO execution_proposals(id,ai_session_id,project_id,task_id,source_task_proposal_id,status,created_at,updated_at,work_spec_json,verification_spec_json,outcome) VALUES('ep','s','p','t','tp','PENDING_CONFIRMATION',?,?,?,?, 'PENDING')`, stamp, stamp, work, verification); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, raw, migrations); err != nil {
		t.Fatal(err)
	}
	var planID, taskPlanID, executionPlanID, planStatus string
	if err := raw.QueryRowContext(ctx, `SELECT id,status FROM task_plans WHERE legacy_task_proposal_id='tp'`).Scan(&planID, &planStatus); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT confirmed_task_plan_id FROM tasks WHERE id='t'`).Scan(&taskPlanID); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT task_plan_id FROM execution_proposals WHERE id='ep'`).Scan(&executionPlanID); err != nil {
		t.Fatal(err)
	}
	if planStatus != "APPLIED" || planID == "" || taskPlanID != planID || executionPlanID != planID {
		t.Fatalf("migration provenance plan=%q status=%q task=%q execution=%q", planID, planStatus, taskPlanID, executionPlanID)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "failed.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	migrations := []Migration{{Version: 1, SQL: `CREATE TABLE stable(id INTEGER);`}, {Version: 2, SQL: `CREATE TABLE transient(id INTEGER); INSERT INTO missing_table VALUES(1);`}}
	if err := ApplyMigrations(context.Background(), raw, migrations); err == nil {
		t.Fatal("bad migration succeeded")
	}
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='transient'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("transient table count=%d err=%v", count, err)
	}
}

func TestMigrationV2BackfillsExistingTasksAndRunUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ctx := context.Background()
	if err := ApplyMigrations(ctx, raw, []Migration{{Version: 1, SQL: schemaV1}}); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	if _, err := raw.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','P',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('r','p','R','/tmp/r',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO tasks(id,project_id,repository_id,goal,constraints_json,base_commit,status,created_at,updated_at) VALUES('t','p','r','goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO runs(id,task_id,attempt,status,created_at,updated_at) VALUES('active','t',1,'RUNNING',?,?),('terminal','t',2,'SUCCEEDED',?,?)`, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, raw, historicalMigrations()); err != nil {
		t.Fatal(err)
	}
	var moduleID, configID string
	if err := raw.QueryRow(`SELECT module_id,codex_config_id FROM tasks WHERE id='t'`).Scan(&moduleID, &configID); err != nil {
		t.Fatal(err)
	}
	if moduleID == "" || configID != codexconfig.DefaultConfigID {
		t.Fatalf("module=%q config=%q", moduleID, configID)
	}
	var moduleProject string
	if err := raw.QueryRow(`SELECT project_id FROM modules WHERE id=?`, moduleID).Scan(&moduleProject); err != nil || moduleProject != "p" {
		t.Fatalf("module project=%q err=%v", moduleProject, err)
	}
	var activeUsage, terminalUsage string
	if err := raw.QueryRow(`SELECT status FROM run_token_usage WHERE run_id='active'`).Scan(&activeUsage); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT status FROM run_token_usage WHERE run_id='terminal'`).Scan(&terminalUsage); err != nil {
		t.Fatal(err)
	}
	if activeUsage != string(run.TokenUsagePending) || terminalUsage != string(run.TokenUsageUnavailable) {
		t.Fatalf("active=%s terminal=%s", activeUsage, terminalUsage)
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration left a foreign-key violation")
	}
}

func TestModuleAndCodexConfigDurableRegistries(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	projects := NewProjectStore(db)
	for _, id := range []string{"p1", "p2"} {
		if err := projects.Create(ctx, project.Project{ID: id, Name: id, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	modules := NewModuleStore(db)
	for _, value := range []module.Module{{ID: "m1", ProjectID: "p1", Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}, {ID: "m2", ProjectID: "p2", Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}} {
		if err := modules.Create(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := modules.Create(ctx, module.Module{ID: "duplicate", ProjectID: "p1", Key: "core", Name: "Duplicate", CreatedAt: testNow, UpdatedAt: testNow}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate module err=%v", err)
	}
	parent := "m2"
	if err := modules.Create(ctx, module.Module{ID: "cross", ProjectID: "p1", Key: "cross", Name: "Cross", ParentID: &parent, CreatedAt: testNow, UpdatedAt: testNow}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-project parent err=%v", err)
	}
	listed, err := modules.ListByProject(ctx, "p1")
	if err != nil || len(listed) != 1 || listed[0].ID != "m1" {
		t.Fatalf("modules=%+v err=%v", listed, err)
	}
	configs := NewCodexConfigStore(db)
	value := codexconfig.Config{ID: "custom", Name: "Custom", CodexPath: "/opt/codex/bin/codex", Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}
	if err := configs.Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	got, err := configs.Get(ctx, value.ID)
	if err != nil || got != value {
		t.Fatalf("config=%+v err=%v", got, err)
	}
	values, err := configs.List(ctx)
	if err != nil || len(values) != 2 {
		t.Fatalf("configs=%+v err=%v", values, err)
	}
}

func TestProjectModuleMapInitialReviewGrantAndFocusedRunSurviveReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	f := seed(t, db, "focused-durable")
	moduleStore := NewModuleStore(db)
	if err := moduleStore.ReplaceContextMap(ctx, f.project.ID, []module.Module{
		{ID: f.task.ModuleID, ProjectID: f.project.ID, Key: "core", Name: "Core", Description: "Owns the primary path.", CreatedAt: testNow, UpdatedAt: testNow, ProductionFacts: []module.ProductionFacts{{RepositoryID: f.repository.ID, RepositoryName: f.repository.Name, Responsibility: "Owns the primary path.", ResponsibilitySource: module.ResponsibilityGuidance, ProductionRoots: []string{"internal/core"}, Entrypoints: []string{"internal/core/main.go"}, RelatedModules: []string{"support"}}}},
		{ID: "module-support", ProjectID: f.project.ID, Key: "support", Name: "Support", Description: "Supports core.", CreatedAt: testNow, UpdatedAt: testNow, ProductionFacts: []module.ProductionFacts{{RepositoryID: f.repository.ID, RepositoryName: f.repository.Name, Responsibility: "Supports core.", ResponsibilitySource: module.ResponsibilityDerived, ProductionRoots: []string{"internal/support"}, Entrypoints: []string{}, RelatedModules: []string{}}}},
	}, testNow); err != nil {
		t.Fatal(err)
	}
	grantStore := NewReviewGrantStore(db)
	// This Review owns a separate capability from the seeded Execution.
	reviewArea := featurearea.FeatureArea{ID: "focused-review-area", ProjectID: f.project.ID, CreatedByAISessionID: "fixture-session-focused-durable", Title: "Review capability", ProblemStatement: "Explore source", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, reviewArea); err != nil {
		t.Fatal(err)
	}
	f.task.FeatureAreaID = reviewArea.ID
	reviewID := seedFixtureReview(t, db, f, "review_focused")
	grant := reviewgrant.Grant{ReviewID: reviewID, FeatureAreaID: f.task.FeatureAreaID, ProjectID: f.project.ID, RepositoryID: f.repository.ID, Mode: reviewgrant.ModeFocused, InitialScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/core"}}, SelectedModules: []reviewgrant.ModuleSelection{{Key: "core", Role: reviewgrant.RolePrimary}}, CreatedAt: testNow}
	if err := grantStore.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(db), func() (string, error) { return "run-focused-second", nil }, func() time.Time { return testNow.Add(time.Minute) })
	plan := run.ReviewPlan{Mode: run.ReviewModeFocused, InitialScope: grant.InitialScope, SelectedModules: []run.ReviewModuleSelection{{Key: "core", Role: run.ReviewModulePrimary}}}
	created, err := runs.CreateFocusedScopeReviewForReview(ctx, reviewID, "Review only enough to form the Task.", run.ReviewScope{Kind: run.ReviewScopeRepository}, plan)
	if err != nil {
		t.Fatal(err)
	}
	repositoryWide := seed(t, db, "repository-wide-grant")
	repositoryReviewID := seedFixtureReview(t, db, repositoryWide, "review_repository_grant")
	repositoryGrant := reviewgrant.Grant{ReviewID: repositoryReviewID, FeatureAreaID: repositoryWide.task.FeatureAreaID, ProjectID: repositoryWide.project.ID, RepositoryID: repositoryWide.repository.ID, Mode: reviewgrant.ModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}, SelectedModules: []reviewgrant.ModuleSelection{}, CreatedAt: testNow}
	if err := grantStore.Create(ctx, repositoryGrant); err != nil {
		t.Fatal(err)
	}
	session := aisession.Session{ID: "session-repository-wide-grant", ProjectID: repositoryWide.project.ID, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, db, session)
	expansion := scopeexpansion.Expansion{ID: "expansion-to-repository", AISessionID: session.ID, ProjectID: repositoryWide.project.ID, ReviewID: repositoryReviewID, FeatureAreaID: repositoryWide.task.FeatureAreaID, RepositoryID: repositoryWide.repository.ID, CurrentScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"internal/core"}}, AdditionalScope: run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}, Reason: "The exact dependency now requires repository-wide production review.", Status: scopeexpansion.StatusPendingConfirmation, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewScopeExpansionStore(db).Create(ctx, expansion); !errors.Is(err, scopeexpansion.ErrConflict) {
		t.Fatalf("repository grant accepted redundant expansion: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	modules, err := NewModuleStore(db).ListByProject(ctx, f.project.ID)
	if err != nil || len(modules) != 2 || len(modules[0].ProductionFacts) != 1 {
		t.Fatalf("reopened Module Map = %+v err=%v", modules, err)
	}
	reopenedGrant, err := NewReviewGrantStore(db).Get(ctx, reviewID)
	if err != nil || reopenedGrant.Mode != reviewgrant.ModeFocused || !reflect.DeepEqual(reopenedGrant.InitialScope.Paths, grant.InitialScope.Paths) || !reflect.DeepEqual(reopenedGrant.SelectedModules, grant.SelectedModules) {
		t.Fatalf("reopened initial grant = %+v err=%v", reopenedGrant, err)
	}
	reopenedRun, err := NewRunStore(db).Get(ctx, created.ID)
	if err != nil || reopenedRun.ReviewScope.Kind != run.ReviewScopeRepository || !reflect.DeepEqual(reopenedRun.ReviewPlan.InitialScope.Paths, []string{"internal/core"}) || !reflect.DeepEqual(reopenedRun.ReviewPlan.SelectedModules, plan.SelectedModules) {
		t.Fatalf("reopened focused Run = %+v err=%v", reopenedRun, err)
	}
	reopenedRepositoryGrant, err := NewReviewGrantStore(db).Get(ctx, repositoryReviewID)
	if err != nil || reopenedRepositoryGrant.InitialScope.Kind != run.ReviewScopeRepository || len(reopenedRepositoryGrant.InitialScope.Paths) != 0 {
		t.Fatalf("reopened repository-wide initial grant = %+v err=%v", reopenedRepositoryGrant, err)
	}
	reopenedExpansion, err := NewScopeExpansionStore(db).Get(ctx, expansion.ID)
	if !errors.Is(err, scopeexpansion.ErrNotFound) {
		t.Fatalf("reopened narrow-to-repository expansion = %+v err=%v", reopenedExpansion, err)
	}
}

func TestRunTokenUsagePersistsExactDelta(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f := seed(t, db, "usage")
	service := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow.Add(time.Minute) })
	value, _ := service.Transition(context.Background(), f.run.ID, run.StatusPreparing)
	value, _ = service.Transition(context.Background(), value.ID, run.StatusRunning)
	value, _ = service.Transition(context.Background(), value.ID, run.StatusCollecting)
	window := int64(128000)
	observation := &run.TokenUsageObservation{Reliable: true, StartCumulative: run.TokenCounts{InputTokens: 10, CachedInputTokens: 3, CacheWriteInputTokens: 1, OutputTokens: 2, ReasoningOutputTokens: 1, TotalTokens: 12}, EndCumulative: run.TokenCounts{InputTokens: 30, CachedInputTokens: 8, CacheWriteInputTokens: 4, OutputTokens: 12, ReasoningOutputTokens: 6, TotalTokens: 42}, ModelContextWindow: &window}
	value, err := service.SucceedWithUsage(context.Background(), value.ID, run.Result{}, observation)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := NewRunStore(db).Get(context.Background(), value.ID)
	if err != nil || stored.TokenUsage.Status != run.TokenUsageAvailable || stored.TokenUsage.Usage == nil {
		t.Fatalf("stored=%+v err=%v", stored.TokenUsage, err)
	}
	if stored.TokenUsage.Usage.TotalTokens != 30 || stored.TokenUsage.Usage.CacheWriteInputTokens != 3 || stored.TokenUsage.Usage.ReasoningOutputTokens != 5 || stored.TokenUsage.ModelContextWindow == nil || *stored.TokenUsage.ModelContextWindow != window {
		t.Fatalf("stored usage=%+v", stored.TokenUsage)
	}
}

func TestOneActiveRunPerTaskPartialIndex(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	fixture := seed(t, db, "one-active")
	service := run.NewService(NewRunStore(db), func() (string, error) { return "run-second", nil }, nil)
	if _, err := service.Transition(context.Background(), fixture.run.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,task_id,execution_proposal_id,purpose,attempt,status,created_at,updated_at,runtime_epoch,agent_execution_id) VALUES('run-second',?,?,'EXECUTION',?,?,?,'VERIFICATION',3,'CREATED',?,?,?,?)`, fixture.project.ID, fixture.task.FeatureAreaID, fixture.run.ExecutionProposalID, fixture.task.ID, fixture.run.ExecutionProposalID, formatTime(testNow), formatTime(testNow), db.RuntimeEpoch(), fixture.run.AgentExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO run_token_usage(run_id,status,updated_at) VALUES('run-second','PENDING',?)`, formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	second, err := service.Get(context.Background(), "run-second")
	if err != nil {
		t.Fatal(err)
	}
	assertSecondBlocked := func(status run.Status) {
		t.Helper()
		// Exercise the database partial index independently of the stronger
		// runtime claim fence, which also rejects this unclaimed second Run.
		if _, err := db.sql.Exec(`UPDATE runs SET status='PREPARING' WHERE id=?`, second.ID); !errors.Is(conflict(err), ErrConflict) {
			t.Fatalf("second active transition while first is %s err=%v", status, err)
		}
	}
	assertSecondBlocked(run.StatusPreparing)
	for _, status := range []run.Status{run.StatusRunning, run.StatusWaitingApproval} {
		if _, err := service.Transition(context.Background(), fixture.run.ID, status); err != nil {
			t.Fatalf("transition first run to %s: %v", status, err)
		}
		assertSecondBlocked(status)
	}
	if _, err := service.Transition(context.Background(), fixture.run.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(context.Background(), fixture.run.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	assertSecondBlocked(run.StatusCollecting)
}

func TestDurableTaskRejectsCodexConfigAndBaseSwitch(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f := seed(t, db, "immutable")
	store := NewTaskStore(db)
	if _, err := store.Update(context.Background(), f.task.ID, func(value *task.Task) error { value.CodexConfigID = "other"; return nil }); !errors.Is(err, task.ErrCodexConfigImmutable) {
		t.Fatalf("config mutation err=%v", err)
	}
	if _, err := store.Update(context.Background(), f.task.ID, func(value *task.Task) error {
		value.BaseCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		return nil
	}); !errors.Is(err, task.ErrBaseCommitImmutable) {
		t.Fatalf("base mutation err=%v", err)
	}
}

func TestDurableRoundTripAndEventFilters(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	f := seed(t, db, "durable")
	ctx := context.Background()
	worktreeValue := worktree.Worktree{ID: "wt-durable", RepositoryID: f.repository.ID, RunID: f.run.ID, Path: filepath.Join(root, "worktrees", "run-durable"), Branch: "devbridge/task/run", BaseCommit: f.task.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewWorktreeStore(db).Create(ctx, worktreeValue); err != nil {
		t.Fatal(err)
	}
	artifactValue := artifact.Artifact{ID: "artifact-durable", RunID: f.run.ID, Kind: artifact.KindGitDiff, Path: filepath.Join(root, "artifacts", "run-durable", "artifact-durable"), SizeBytes: 4, SHA256: "abcd", CreatedAt: testNow}
	if err := NewArtifactStore(db).Create(ctx, artifactValue); err != nil {
		t.Fatal(err)
	}
	evidenceValue := evidence.GitEvidence{RunID: f.run.ID, BaseCommit: f.task.BaseCommit, HeadCommit: f.task.BaseCommit, Dirty: true, ChangedFiles: []string{"b.go", "a.go"}, DiffArtifactID: artifactValue.ID, CreatedAt: testNow}
	if err := NewEvidenceStore(db).Save(ctx, evidenceValue); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	if got, err := NewProjectStore(db).Get(ctx, f.project.ID); err != nil || got.Name != f.project.Name {
		t.Fatalf("project=%+v err=%v", got, err)
	}
	if got, err := NewRepositoryStore(db).Get(ctx, f.repository.ID); err != nil || got.ProjectID != f.project.ID {
		t.Fatalf("repository=%+v err=%v", got, err)
	}
	if got, err := NewTaskStore(db).Get(ctx, f.task.ID); err != nil || got.ProjectID != f.project.ID || len(got.Constraints) != 1 {
		t.Fatalf("task=%+v err=%v", got, err)
	}
	if got, err := NewRunStore(db).Get(ctx, f.run.ID); err != nil || got.Attempt != f.run.Attempt {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	if got, err := NewWorktreeStore(db).Get(ctx, worktreeValue.ID); err != nil || got.Path != worktreeValue.Path {
		t.Fatalf("worktree=%+v err=%v", got, err)
	}
	if got, err := NewArtifactStore(db).Get(ctx, artifactValue.ID); err != nil || got.SHA256 != "abcd" {
		t.Fatalf("artifact=%+v err=%v", got, err)
	}
	if got, err := NewEvidenceStore(db).Get(ctx, f.run.ID); err != nil || len(got.ChangedFiles) != 2 || got.DiffArtifactID != artifactValue.ID {
		t.Fatalf("evidence=%+v err=%v", got, err)
	}
	events, err := NewEventStore(db).List(ctx, event.Filter{ProjectID: f.project.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 12 {
		t.Fatalf("events=%d %+v", len(events), events)
	}
	for i, v := range events {
		if i > 0 && v.Sequence <= events[i-1].Sequence {
			t.Fatal("non-monotonic events")
		}
		if string(v.Payload) == "" || !stringContains(string(v.Payload), `"version":1`) {
			t.Fatalf("payload=%s", v.Payload)
		}
	}
	byRun, err := NewEventStore(db).List(ctx, event.Filter{RunID: f.run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(byRun) != 4 {
		t.Fatalf("run events=%d", len(byRun))
	}
}

func TestTaskGoalsRoundTrip(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	current, _ := revisionTaskFixture(t, db, "goals")
	f := fixture{task: current}
	ctx := context.Background()
	taskStore := NewTaskStore(db)
	goals := []task.Goal{
		{ID: "goal_a", TaskID: f.task.ID, Position: 0, Title: "first", CreatedAt: testNow},
		{ID: "goal_b", TaskID: f.task.ID, Position: 1, Title: "second", CreatedAt: testNow},
	}
	if err := taskStore.DefineGoals(ctx, f.task.ID, goals); err != nil {
		t.Fatal(err)
	}
	if err := taskStore.DefineGoals(ctx, f.task.ID, goals); err == nil {
		t.Fatal("expected duplicate definition conflict")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	taskStore = NewTaskStore(db)
	listed, err := taskStore.Goals(ctx, f.task.ID)
	if err != nil || len(listed) != 2 || listed[0].Title != "first" || listed[1].Position != 1 {
		t.Fatalf("goals=%+v err=%v", listed, err)
	}
}

func TestExecutionGoalProgressLifecycle(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	current, session := revisionTaskFixture(t, db, "progress")
	current.WorkSpec = task.WorkSpec{Goal: "Track goals", Constraints: current.Constraints, CodexInstruction: "Implement goals", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	current.VerificationSpec = task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture"}}
	taskStore := NewTaskStore(db)
	if err := taskStore.DefineGoals(ctx, current.ID, []task.Goal{{ID: "goal_a", TaskID: current.ID, Position: 0, Title: "first", CreatedAt: testNow}, {ID: "goal_b", TaskID: current.ID, Position: 1, Title: "second", CreatedAt: testNow}}); err != nil {
		t.Fatal(err)
	}
	current = approveFixtureTask(t, db, current, session)
	value, reserved, err := NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, session.ID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	executionProposalStore := NewExecutionProposalStore(db)
	if value.Status == executionproposal.StatusAdmitted && reserved.ID == "" {
		value, reserved, err = continueAdmittedExecution(ctx, db, value.ID, testNow)
		if err != nil {
			t.Fatal(err)
		}
	}
	runService := run.NewService(NewRunStore(db), nil, nil)
	if _, err := runService.Transition(ctx, reserved.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(db)
	if _, err := conversations.BeginRun(ctx, reserved.ID); err != nil {
		t.Fatal(err)
	}
	if err := conversations.BindThread(ctx, reserved.ID, "thread-goal-progress"); err != nil {
		t.Fatal(err)
	}
	if _, err := runService.AttachThread(ctx, reserved.ID, "thread-goal-progress"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusRunning} {
		if _, err := runService.Transition(ctx, reserved.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	if err := conversations.MarkTurnPending(ctx, reserved.ID); err != nil {
		t.Fatal(err)
	}
	if err := taskStore.InitializeGoalProgress(ctx, reserved.ExecutionProposalID, current.ID, current.ProjectID, testNow); err != nil {
		t.Fatal(err)
	}
	if err := taskStore.InitializeGoalProgress(ctx, reserved.ExecutionProposalID, current.ID, current.ProjectID, testNow); err != nil {
		t.Fatal(err)
	}
	progress, err := taskStore.GoalProgress(ctx, reserved.ExecutionProposalID)
	if err != nil || len(progress) != 2 || progress[0].Status != taskgoal.StatusPending {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	if err := taskStore.UpdateGoalProgress(ctx, reserved.ExecutionProposalID, "goal_a", taskgoal.StatusRunning, testNow); err != nil {
		t.Fatal(err)
	}
	if err := taskStore.UpdateGoalProgress(ctx, reserved.ExecutionProposalID, "goal_a", taskgoal.StatusPending, testNow); err == nil {
		t.Fatal("expected invalid transition conflict")
	}
	if err := taskStore.UpdateGoalProgress(ctx, reserved.ExecutionProposalID, "goal_missing", taskgoal.StatusRunning, testNow); err == nil {
		t.Fatal("expected unknown goal conflict")
	}
	if err := taskStore.UpdateGoalProgress(ctx, reserved.ExecutionProposalID, "goal_a", taskgoal.StatusCompleted, testNow); err != nil {
		t.Fatal(err)
	}
	progress, err = taskStore.GoalProgress(ctx, reserved.ExecutionProposalID)
	if err != nil || progress[0].Status != taskgoal.StatusCompleted {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	loaded, err := executionProposalStore.GetByID(ctx, reserved.ExecutionProposalID)
	if err != nil || loaded.TaskID != current.ID || loaded.ProjectID != current.ProjectID {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestRepositoryPathConflictAcrossProjects(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	p1 := project.Project{ID: "p1", Name: "one", CreatedAt: testNow, UpdatedAt: testNow}
	p2 := project.Project{ID: "p2", Name: "two", CreatedAt: testNow, UpdatedAt: testNow}
	store := NewProjectStore(db)
	store.Create(ctx, p1)
	store.Create(ctx, p2)
	path := filepath.Join(db.stateRoot, "repo")
	repos := NewRepositoryStore(db)
	if _, created, err := repos.PutIfAbsent(ctx, repository.Repository{ID: "r1", ProjectID: p1.ID, Name: "r", Path: path, CreatedAt: testNow, UpdatedAt: testNow}); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if _, _, err := repos.PutIfAbsent(ctx, repository.Repository{ID: "r2", ProjectID: p2.ID, Name: "r", Path: path, CreatedAt: testNow, UpdatedAt: testNow}); !errors.Is(err, repository.ErrPathConflict) {
		t.Fatalf("err=%v", err)
	}
	if _, created, err := repos.PutIfAbsent(ctx, repository.Repository{ID: "r3", ProjectID: p1.ID, Name: "second", Path: filepath.Join(db.stateRoot, "repo-2"), CreatedAt: testNow, UpdatedAt: testNow}); err != nil || !created {
		t.Fatalf("second repository created=%v err=%v", created, err)
	}
	values, err := repos.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owned := 0
	for _, value := range values {
		if value.ProjectID == p1.ID {
			owned++
		}
	}
	if owned != 2 {
		t.Fatalf("project repositories=%d", owned)
	}
	moduleP2 := module.Module{ID: "module-p2", ProjectID: p2.ID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, moduleP2); err != nil {
		t.Fatal(err)
	}
	mismatch := task.Task{ID: "mismatched-task", ProjectID: p2.ID, ModuleID: moduleP2.ID, RepositoryID: "r1", CodexConfigID: codexconfig.DefaultConfigID, Goal: "invalid membership", WorkSpec: task.WorkSpec{Goal: "invalid membership", CodexInstruction: "invalid membership", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "Fixture task", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewTaskStore(db).Create(ctx, mismatch); !errors.Is(err, ErrConflict) && !errors.Is(err, repository.ErrProjectMismatch) {
		t.Fatalf("mismatched membership err=%v", err)
	}
}

func TestConcurrentReviewRunAttemptAllocation(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f, area, session := reviewFixture(t, db, "attempts")
	v := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ID: "review_concurrent_attempt", ProjectID: f.project.ID, FeatureAreaID: area.ID, CreatedByAISessionID: session.ID, AuthorKind: string(review.AuthorAISession), AuthorID: session.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, f.task.CodexConfigID), BaseCommit: f.task.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "goal", Instruction: "review", CreatedAt: testNow}
	if err := NewReviewStore(db).Create(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	const n = 24
	var wg sync.WaitGroup
	results := make(chan run.Run, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := NewRunStore(db).CreateProductScopeReviewAttempt(context.Background(), v.ID, fmt.Sprintf("concurrent-review-%d", i), v.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository}, testNow)
			if err != nil {
				errs <- err
			} else {
				results <- r
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	success := 0
	for r := range results {
		success++
		if r.Attempt != 1 || r.TaskID != v.TaskID || r.OperationID != v.ID {
			t.Fatalf("Review attempt=%+v", r)
		}
	}
	if success != 1 || len(errs) != n-1 {
		t.Fatalf("success=%d conflicts=%d", success, len(errs))
	}
	var reviewAttempt, taskAttempt int
	if err := db.sql.QueryRow(`SELECT next_attempt FROM reviews WHERE id=?`, v.ID).Scan(&reviewAttempt); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`SELECT next_attempt FROM tasks WHERE id=?`, f.task.ID).Scan(&taskAttempt); err != nil {
		t.Fatal(err)
	}
	if reviewAttempt != 0 || taskAttempt != 1 {
		t.Fatalf("Review attempts=%d Task attempts=%d", reviewAttempt, taskAttempt)
	}
}

func TestConcurrentCreatesConflictsTransitionsAndEvents(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	projects := NewProjectStore(db)
	const projectsN = 12
	var wg sync.WaitGroup
	errs := make(chan error, projectsN)
	for i := 0; i < projectsN; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := project.Project{ID: fmt.Sprintf("parallel-project-%02d", i), Name: "parallel", CreatedAt: testNow, UpdatedAt: testNow}
			errs <- projects.Create(ctx, value)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p1 := project.Project{ID: "conflict-p1", Name: "one", CreatedAt: testNow, UpdatedAt: testNow}
	p2 := project.Project{ID: "conflict-p2", Name: "two", CreatedAt: testNow, UpdatedAt: testNow}
	if err := projects.Create(ctx, p1); err != nil {
		t.Fatal(err)
	}
	if err := projects.Create(ctx, p2); err != nil {
		t.Fatal(err)
	}
	repos := NewRepositoryStore(db)
	path := filepath.Join(db.stateRoot, "one-canonical-path")
	repoResults := make(chan error, 2)
	for index, p := range []project.Project{p1, p2} {
		wg.Add(1)
		go func(index int, p project.Project) {
			defer wg.Done()
			_, _, err := repos.PutIfAbsent(ctx, repository.Repository{ID: fmt.Sprintf("conflict-repo-%d", index), ProjectID: p.ID, Name: "repo", Path: path, CreatedAt: testNow, UpdatedAt: testNow})
			repoResults <- err
		}(index, p)
	}
	wg.Wait()
	close(repoResults)
	successes, conflicts := 0, 0
	for err := range repoResults {
		if err == nil {
			successes++
		} else if errors.Is(err, repository.ErrPathConflict) || errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("repository successes=%d conflicts=%d", successes, conflicts)
	}
	var winning repository.Repository
	allRepos, err := repos.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range allRepos {
		if value.Path == path {
			winning = value
		}
	}
	winningModule := module.Module{ID: "winning-module", ProjectID: winning.ProjectID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, winningModule); err != nil {
		t.Fatal(err)
	}
	session := aisession.Session{ID: "concurrent-session", ProjectID: winning.ProjectID, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, db, session)
	if err := NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: "concurrent-area", ProjectID: winning.ProjectID, CreatedByAISessionID: session.ID, Title: "Concurrent", ProblemStatement: "Concurrent tasks", Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	taskStore := NewTaskStore(db)
	const tasksN = 12
	taskErrs := make(chan error, tasksN)
	for i := 0; i < tasksN; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			taskErrs <- taskStore.Create(ctx, task.Task{ID: fmt.Sprintf("parallel-task-%02d", i), ProjectID: winning.ProjectID, FeatureAreaID: "concurrent-area", ModuleID: winningModule.ID, RepositoryID: winning.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "goal", Constraints: []string{}, WorkSpec: task.WorkSpec{Goal: "goal", Constraints: []string{}, CodexInstruction: "goal", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "Fixture task", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow})
		}(i)
	}
	wg.Wait()
	close(taskErrs)
	for err := range taskErrs {
		if err != nil {
			t.Fatal(err)
		}
	}
	transitionID := "parallel-task-00"
	transitionErrs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := taskStore.Update(ctx, transitionID, func(value *task.Task) error {
				if value.WorkKind != "" {
					return errors.New("already transitioned")
				}
				value.WorkKind = task.WorkKindBugFix
				value.UpdatedAt = testNow.Add(time.Second)
				return nil
			})
			transitionErrs <- err
		}()
	}
	wg.Wait()
	close(transitionErrs)
	transitionSuccess := 0
	for err := range transitionErrs {
		if err == nil {
			transitionSuccess++
		}
	}
	if transitionSuccess != 1 {
		t.Fatalf("transition successes=%d", transitionSuccess)
	}
	events, err := NewEventStore(db).List(ctx, event.Filter{TaskID: transitionID})
	if err != nil {
		t.Fatal(err)
	}
	statusEvents := 0
	for _, value := range events {
		if value.Type == event.TaskStatusChanged {
			statusEvents++
		}
	}
	if statusEvents != 0 {
		t.Fatalf("status events=%d", statusEvents)
	}
}

func TestUpdateAndEventAreAtomicOnCallbackFailure(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f := seed(t, db, "atomic")
	ctx := context.Background()
	before, _ := NewEventStore(db).List(ctx, event.Filter{TaskID: f.task.ID})
	_, err := NewTaskStore(db).Update(ctx, f.task.ID, func(value *task.Task) error { value.Status = task.StatusRunning; return errors.New("injected") })
	if err == nil {
		t.Fatal("update succeeded")
	}
	got, err := NewTaskStore(db).Get(ctx, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != task.StatusActive {
		t.Fatalf("status=%s", got.Status)
	}
	after, _ := NewEventStore(db).List(ctx, event.Filter{TaskID: f.task.ID})
	if len(after) != len(before) {
		t.Fatalf("events before=%d after=%d", len(before), len(after))
	}
	duplicate := f.task
	duplicate.Goal = "duplicate"
	if err := NewTaskStore(db).Create(ctx, duplicate); err == nil {
		t.Fatal("duplicate task creation succeeded")
	}
	finalEvents, _ := NewEventStore(db).List(ctx, event.Filter{TaskID: f.task.ID})
	if len(finalEvents) != len(before) {
		t.Fatalf("event appended without state: before=%d after=%d", len(before), len(finalEvents))
	}
}

func TestStateRollsBackWhenEventAppendFails(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f := seed(t, db, "event-failure")
	ctx := context.Background()
	if _, err := db.sql.Exec(`DROP TABLE events`); err != nil {
		t.Fatal(err)
	}
	service := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow })
	if _, err := service.Transition(ctx, f.run.ID, run.StatusPreparing); err == nil {
		t.Fatal("transition succeeded without event ledger")
	}
	got, err := NewRunStore(db).Get(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != run.StatusCreated {
		t.Fatalf("status changed without event: %s", got.Status)
	}
}

func TestTerminalTimestampAndUsageRollBackWhenEventAppendFails(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f := seed(t, db, "usage-event-failure")
	ctx := context.Background()
	service := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow.Add(time.Minute) })
	value, err := service.Transition(ctx, f.run.ID, run.StatusPreparing)
	if err != nil {
		t.Fatal(err)
	}
	value, err = service.Transition(ctx, value.ID, run.StatusRunning)
	if err != nil {
		t.Fatal(err)
	}
	value, err = service.Transition(ctx, value.ID, run.StatusCollecting)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TABLE events`); err != nil {
		t.Fatal(err)
	}
	usage := &run.TokenUsageObservation{Reliable: true, EndCumulative: run.TokenCounts{InputTokens: 5, OutputTokens: 3, TotalTokens: 8}}
	if _, err := service.SucceedWithUsage(ctx, value.ID, run.Result{}, usage); err == nil {
		t.Fatal("terminal transition succeeded without event ledger")
	}
	got, err := NewRunStore(db).Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != run.StatusCollecting || got.FinishedAt != nil || got.TokenUsage.Status != run.TokenUsagePending || got.TokenUsage.Usage != nil {
		t.Fatalf("run changed without event: %+v", got)
	}
}

func TestRecoveryTransitionsAndIdempotency(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	ctx := context.Background()
	statuses := []run.Status{run.StatusCreated, run.StatusPreparing, run.StatusRunning, run.StatusWaitingApproval, run.StatusCollecting, run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled}
	fixtures := make([]fixture, 0, len(statuses))
	for i, status := range statuses {
		f := seed(t, db, fmt.Sprintf("recovery-%d", i), status == run.StatusCanceled)
		fixtures = append(fixtures, f)
		runs := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow })
		if status == run.StatusPreparing || status == run.StatusRunning || status == run.StatusWaitingApproval || status == run.StatusCollecting || status == run.StatusSucceeded || status == run.StatusCanceled {
			if _, err := runs.Transition(ctx, f.run.ID, run.StatusPreparing); err != nil {
				t.Fatal(err)
			}
			if _, err := NewAgentConversationStore(db).BeginRun(ctx, f.run.ID); err != nil {
				t.Fatal(err)
			}
			if status == run.StatusCanceled {
				if err := NewAgentConversationStore(db).BindThread(ctx, f.run.ID, "thread-recovery-canceled"); err != nil {
					t.Fatal(err)
				}
				if _, err := runs.AttachThread(ctx, f.run.ID, "thread-recovery-canceled"); err != nil {
					t.Fatal(err)
				}
			}
		}
		if status == run.StatusRunning || status == run.StatusWaitingApproval || status == run.StatusCollecting || status == run.StatusSucceeded || status == run.StatusCanceled {
			if _, err := runs.Transition(ctx, f.run.ID, run.StatusRunning); err != nil {
				t.Fatal(err)
			}
		}
		if status == run.StatusWaitingApproval {
			if _, err := runs.Transition(ctx, f.run.ID, run.StatusWaitingApproval); err != nil {
				t.Fatal(err)
			}
		}
		if status == run.StatusCollecting || status == run.StatusSucceeded {
			if _, err := runs.Transition(ctx, f.run.ID, run.StatusCollecting); err != nil {
				t.Fatal(err)
			}
		}
		if status == run.StatusSucceeded {
			if _, err := runs.Succeed(ctx, f.run.ID, run.Result{}); err != nil {
				t.Fatal(err)
			}
		}
		if status == run.StatusFailed || status == run.StatusInfrastructureFailed || status == run.StatusCanceled {
			kind := run.FailureTask
			if status == run.StatusInfrastructureFailed {
				kind = run.FailureInfrastructure
			}
			if status == run.StatusCanceled {
				kind = run.FailureCanceled
				if _, err := runs.AttachTurn(ctx, f.run.ID, "turn-recovery-canceled"); err != nil {
					t.Fatal(err)
				}
				conversations := NewAgentConversationStore(db)
				if err := conversations.MarkTurnPending(ctx, f.run.ID); err != nil {
					t.Fatal(err)
				}
				input := executioncancel.Input{ProjectID: f.project.ID, AISessionID: fmt.Sprintf("fixture-session-recovery-%d", i), ExecutionID: f.run.ExecutionProposalID, RunID: f.run.ID, RequestID: "cancel-recovery-history"}
				target := dispatchCancellation(t, agentStorageFixture{db: db}, input)
				if err := conversations.RecordFinalization(ctx, f.run.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: target.TurnID, Quiesced: true, Canceled: true}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := runs.Fail(ctx, f.run.ID, run.Failure{Kind: kind, Code: "terminal-test", Message: "terminal"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	missing := fixtures[2]
	wt := worktree.Worktree{ID: "wt-missing", RepositoryID: missing.repository.ID, RunID: missing.run.ID, Path: filepath.Join(root, "missing"), Branch: "branch", BaseCommit: missing.task.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewWorktreeStore(db).Create(ctx, wt); err != nil {
		t.Fatal(err)
	}
	retained := fixtures[3]
	retainedPath := filepath.Join(root, "retained")
	if err := os.MkdirAll(retainedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := NewWorktreeStore(db).Create(ctx, worktree.Worktree{ID: "wt-retained", RepositoryID: retained.repository.ID, RunID: retained.run.ID, Path: retainedPath, Branch: "branch2", BaseCommit: retained.task.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = openTestDB(t, root)
	service := recovery.NewService(NewRecoveryStore(db), func() time.Time { return testNow.Add(time.Hour) })
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	for i, status := range statuses {
		got, err := NewRunStore(db).Get(ctx, fixtures[i].run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if status == run.StatusCreated || status == run.StatusPreparing || status == run.StatusRunning || status == run.StatusWaitingApproval || status == run.StatusCollecting {
			if got.Status != run.StatusInfrastructureFailed || got.Failure == nil || got.Failure.Code != recovery.InterruptedCode || got.FinishedAt == nil || !got.FinishedAt.Equal(testNow.Add(time.Hour)) || got.TokenUsage.Status != run.TokenUsageUnavailable {
				t.Fatalf("run %d=%+v", i, got)
			}
			parent, _ := NewTaskStore(db).Get(ctx, fixtures[i].task.ID)
			if parent.Status != task.StatusActive {
				t.Fatalf("task %d status=%s", i, parent.Status)
			}
		} else if got.Status != status {
			t.Fatalf("run %d status=%s want=%s", i, got.Status, status)
		} else if status == run.StatusSucceeded || status == run.StatusFailed || status == run.StatusInfrastructureFailed || status == run.StatusCanceled {
			if got.FinishedAt == nil || !got.FinishedAt.Equal(testNow) {
				t.Fatalf("terminal run %d finish moved: %+v", i, got.FinishedAt)
			}
		}
	}
	missingGot, _ := NewWorktreeStore(db).Get(ctx, "wt-missing")
	if missingGot.Status != worktree.StatusMissing {
		t.Fatalf("missing status=%s", missingGot.Status)
	}
	retainedGot, _ := NewWorktreeStore(db).Get(ctx, "wt-retained")
	if retainedGot.Status != worktree.StatusReady {
		t.Fatalf("retained status=%s", retainedGot.Status)
	}
	if _, err := os.Stat(retainedPath); err != nil {
		t.Fatal("retained worktree removed")
	}
	eventsBefore, _ := NewEventStore(db).List(ctx, event.Filter{})
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	eventsAfter, _ := NewEventStore(db).List(ctx, event.Filter{})
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("recovery not idempotent: before=%d after=%d", len(eventsBefore), len(eventsAfter))
	}
	for i, status := range statuses {
		if status != run.StatusPreparing && status != run.StatusRunning && status != run.StatusWaitingApproval && status != run.StatusCollecting {
			continue
		}
		got, err := NewRunStore(db).Get(ctx, fixtures[i].run.ID)
		if err != nil || got.FinishedAt == nil || !got.FinishedAt.Equal(testNow.Add(time.Hour)) {
			t.Fatalf("second recovery run %d=%+v err=%v", i, got, err)
		}
	}
	db.Close()
}

// seedExecutionOwner constructs a real frozen Task/plan/execution chain
// for storage tests which previously allocated Task-only implementation Runs.
func seedExecutionOwner(t *testing.T, db *DB, current task.Task, session aisession.Session, id string) string {
	t.Helper()
	current = approveFixtureTask(t, db, current, session)
	value, err := NewWorkflowProgressionStore(db).AuthorizeTaskExecution(context.Background(), session.ID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	return value.ID
}

func seedFixtureReview(t *testing.T, db *DB, f fixture, id string) string {
	t.Helper()
	var sessionID string
	if err := db.sql.QueryRow(`SELECT created_by_ai_session_id FROM feature_areas WHERE id=?`, f.task.FeatureAreaID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	owner := f.task
	owner.ID = "intent-" + id
	owner.Status, owner.Phase = task.StatusActive, task.PhaseReviewRequired
	owner.CurrentReviewID, owner.CurrentExecutionID, owner.CurrentDefinitionRevisionID, owner.ReviewApprovalID, owner.ConfirmedTaskPlanID = "", "", "", "", ""
	if err := NewTaskStore(db).Create(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	v := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: owner.ID, ID: id, ProjectID: f.project.ID, FeatureAreaID: f.task.FeatureAreaID, CreatedByAISessionID: sessionID, AuthorKind: string(review.AuthorAISession), AuthorID: sessionID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, BackendSelection: reviewFixtureSelection(t, db, f.task.CodexConfigID), BaseCommit: f.task.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Review fixture", Instruction: "Inspect repository", CreatedAt: testNow}
	if err := NewReviewStore(db).Create(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return id
}
