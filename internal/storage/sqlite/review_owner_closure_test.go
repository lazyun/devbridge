package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

func TestDiscoveringReviewRuntimePersistsVisibleReviewTask(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	if err := NewProjectStore(db).Create(ctx, project.Project{ID: "p", Name: "p", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, repository.Repository{ID: "repo", ProjectID: "p", Name: "repo", Path: filepath.Join(t.TempDir(), "source"), CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	seedBoundSession(t, db, aisession.Session{ID: "session", ProjectID: "p", CreatedAt: testNow, UpdatedAt: testNow})
	if err := NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: "fa", ProjectID: "p", CreatedByAISessionID: "session", Title: "Discover", ProblemStatement: "Understand capability", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := NewModuleStore(db).Create(ctx, module.Module{ID: "m", ProjectID: "p", Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := NewTaskStore(db).Create(ctx, task.Task{ID: "intent", ProjectID: "p", FeatureAreaID: "fa", RepositoryID: "repo", ModuleID: "m", CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: strings.Repeat("a", 40), Title: "Understand capability", Goal: "Understand capability", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	reviews := NewReviewStore(db)
	v := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: "intent", ID: "review_direct", ProjectID: "p", FeatureAreaID: "fa", CreatedByAISessionID: "session", AuthorKind: string(review.AuthorAISession), AuthorID: "session", RepositoryID: "repo", CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: reviewFixtureSelection(t, db, codexconfig.DefaultConfigID), BaseCommit: strings.Repeat("a", 40), Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Understand capability", Instruction: "Inspect production", CreatedAt: testNow}
	if err := reviews.Create(ctx, v); err != nil {
		t.Fatal(err)
	}
	grants := NewReviewGrantStore(db)
	grant := reviewgrant.Grant{ReviewID: v.ID, ProjectID: v.ProjectID, FeatureAreaID: v.FeatureAreaID, RepositoryID: v.RepositoryID, Mode: reviewgrant.ModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}, CreatedAt: testNow}
	if err := grants.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(db), func() (string, error) { return "run-direct", nil }, nil)
	r, err := runs.CreateFocusedScopeReviewForReview(ctx, v.ID, v.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository})
	if err != nil {
		t.Fatal(err)
	}
	if r.TaskID != v.TaskID || r.ProjectID != "p" || r.FeatureAreaID != "fa" || r.OperationKind != run.OperationReview || r.OperationID != v.ID || r.Attempt != 1 {
		t.Fatalf("Run owner=%+v", r)
	}
	for name, mutate := range map[string]func(*run.Run){
		"project":        func(v *run.Run) { v.ProjectID = "other" },
		"feature area":   func(v *run.Run) { v.FeatureAreaID = "other" },
		"operation":      func(v *run.Run) { v.OperationID = "other" },
		"operation kind": func(v *run.Run) { v.OperationKind = run.OperationExecution },
		"Review":         func(v *run.Run) { v.ReviewID = "other" },
		"Task carrier":   func(v *run.Run) { v.ProjectID = "foreign-project" },
		"runtime epoch":  func(v *run.Run) { v.RuntimeEpoch = "other" },
	} {
		if _, err := NewRunStore(db).Update(ctx, r.ID, func(value *run.Run) error { mutate(value); return nil }); !errors.Is(err, run.ErrOwnerImmutable) {
			t.Fatalf("accepted %s reassignment: %v", name, err)
		}
	}
	unchanged, err := runs.Get(ctx, r.ID)
	if err != nil || !unchanged.SameOwner(r) {
		t.Fatalf("immutable owner changed: %+v err=%v", unchanged, err)
	}
	if _, err = runs.Transition(ctx, r.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(db)
	conversation, err := conversations.BeginRun(ctx, r.ID)
	if err != nil || conversation.FeatureAreaID != "fa" {
		t.Fatalf("conversation=%+v err=%v", conversation, err)
	}
	ws := workspace.NewService(NewWorkspaceStore(db), func() (string, error) { return "workspace-direct", nil }, nil)
	environment, err := ws.EnsureForFeatureArea(ctx, v.ProjectID, v.RepositoryID, v.FeatureAreaID, v.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if environment.ProjectID != v.ProjectID || environment.FeatureAreaID != v.FeatureAreaID || environment.LegacyThreadID != "" {
		t.Fatalf("environment=%+v", environment)
	}
	for _, mutate := range []func(*workspace.Workspace){
		func(v *workspace.Workspace) { v.ProjectID = "foreign-project" },
		func(v *workspace.Workspace) { v.FeatureAreaID = "other-area" },
		func(v *workspace.Workspace) { v.LegacyThreadID = "forged-context" },
		func(v *workspace.Workspace) { v.RuntimeQuarantined = true },
	} {
		if _, err := NewWorkspaceStore(db).Update(ctx, environment.ID, func(value *workspace.Workspace) error { mutate(value); return nil }); !errors.Is(err, workspace.ErrConflict) {
			t.Fatalf("environment callback changed owner or context authority: %v", err)
		}
	}
	if _, err = runs.AttachWorkspace(ctx, r.ID, environment.ID); err != nil {
		t.Fatal(err)
	}
	tree := worktree.Worktree{ID: "tree-direct", RepositoryID: "repo", RunID: r.ID, Path: filepath.Join(t.TempDir(), "worktree"), Branch: "review/direct", BaseCommit: v.BaseCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	if err = NewWorktreeStore(db).Create(ctx, tree); err != nil {
		t.Fatal(err)
	}
	if _, err = ws.AttachWorktree(ctx, environment.ID, tree.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.AttachWorktree(ctx, r.ID, tree.ID); err != nil {
		t.Fatal(err)
	}
	if err = conversations.BindThread(ctx, r.ID, "direct-thread"); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.AttachThread(ctx, r.ID, "direct-thread"); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Transition(ctx, r.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err = conversations.MarkTurnPending(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.AttachTurn(ctx, r.ID, "direct-turn"); err != nil {
		t.Fatal(err)
	}
	claim, err := conversations.AuthorizeRun(ctx, r.ID)
	if err != nil || claim.OperationID != v.ID || claim.Kind != agentconversation.Review {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if err = NewArtifactStore(db).Create(ctx, artifact.Artifact{ID: "artifact-direct", RunID: r.ID, Kind: artifact.KindGitDiff, Path: filepath.Join(t.TempDir(), "diff"), SHA256: strings.Repeat("b", 64), CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err = NewEvidenceStore(db).Save(ctx, evidence.GitEvidence{RunID: r.ID, BaseCommit: v.BaseCommit, HeadCommit: v.BaseCommit, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, err = NewApprovalStore(db).CreatePending(ctx, approval.Approval{ID: "approval-direct", RunID: r.ID, ThreadID: "direct-thread", TurnID: "direct-turn", Kind: approval.KindCommandExecution, AllowedDecisions: []approval.DecisionType{approval.DecisionDecline}, RequestPayload: []byte(`{}`), CreatedAt: testNow}, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunStore(db).CreateAttempt(ctx, "no-task", "unowned", testNow); !errors.Is(err, run.ErrNotFound) {
		t.Fatalf("unowned Run=%v", err)
	}
	var tasks int
	if err = db.sql.QueryRow(`SELECT COUNT(*) FROM tasks WHERE task_type='DEVELOPMENT'`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("hidden Tasks=%d err=%v", tasks, err)
	}
}
