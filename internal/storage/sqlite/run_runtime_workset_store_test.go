package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestRunRuntimeWorksetStorePersistsValidatedImmutableLayout(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	f := runtimeWorksetFixture(t, db)
	proposal, err := NewExecutionProposalStore(db).Get(ctx, f.run.ExecutionProposalID)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	value := workset.RuntimeWorkset{
		ProtocolVersion:          workset.RuntimeProtocolVersion,
		ProjectID:                f.project.ID,
		FeatureAreaID:            f.task.FeatureAreaID,
		TaskID:                   f.task.ID,
		TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID,
		ExecutionID:              f.run.ExecutionProposalID,
		RunID:                    f.run.ID,
		WorkspaceID:              "workspace-runtime-workset",
		PrimaryMemberID:          "member-runtime-workset",
		Members: []workset.RuntimeMemberBinding{{
			RepositoryID: f.repository.ID, RepositoryAlias: "app", MemberID: "member-runtime-workset", MemberVersion: 1,
			WorktreeID: "worktree-runtime-workset", CanonicalRoot: "/managed/runtime-workset", RootFileIdentity: "root-file",
			OriginCommit: commit, StartHEAD: commit, StartSnapshot: strings.Repeat("b", 64), Read: true,
			Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"internal"}}, RuntimeRootIdentity: "root-runtime",
		}},
		BackendConfigID: f.task.CodexConfigID, BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high",
		RuntimeAdapterIdentity: "codex-app-server", RuntimeProtocolIdentity: "1", CreatedAt: time.Now().UTC(),
	}
	value, err = value.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	store := NewRunStore(db)
	bound, err := store.BindRuntimeWorkset(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	if bound.RuntimeWorkset == nil || bound.RuntimeWorkset.ContentHash != value.ContentHash {
		t.Fatalf("bound runtime workset missing: %#v", bound.RuntimeWorkset)
	}
	reopened, err := store.RuntimeWorkset(ctx, f.run.ID)
	if err != nil || reopened.ContentHash != value.ContentHash || reopened.Members[0].CanonicalRoot != value.Members[0].CanonicalRoot {
		t.Fatalf("runtime workset did not round trip: %#v err=%v", reopened, err)
	}
	if _, err = store.BindRuntimeWorkset(ctx, value); err != nil {
		t.Fatalf("identical replay rejected: %v", err)
	}
	changed := value
	changed.Members = append([]workset.RuntimeMemberBinding(nil), value.Members...)
	changed.Members[0].CanonicalRoot = "/managed/replaced"
	changed.ContentHash = ""
	changed, err = changed.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BindRuntimeWorkset(ctx, changed); !errors.Is(err, review.ErrConflict) {
		t.Fatalf("changed runtime layout accepted: %v", err)
	}
}

// runtimeWorksetFixture uses the production session-initialization API. It is
// intentionally independent from the historical seed helper, which creates a
// legacy pre-initialization session for tests of migration compatibility.
func runtimeWorksetFixture(t *testing.T, db *DB) fixture {
	t.Helper()
	ctx := context.Background()
	p := project.Project{ID: "project-runtime-workset", Name: "Runtime workset", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	sessions := NewAISessionStore(db)
	const scope, sessionID = "runtime-workset-test", "runtime-workset-session"
	if _, err := sessions.InitializeSession(ctx, scope, "runtime-workset-initialize", sessionID, testNow); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.BindSessionProject(ctx, scope, "runtime-workset-bind-project", sessionID, p.ID, "runtime-workset-selection", testNow); err != nil {
		t.Fatal(err)
	}
	session := aisession.Session{ID: sessionID, ProjectID: p.ID, CreatedAt: testNow, UpdatedAt: testNow}
	repo := repository.Repository{ID: "repo-runtime-workset", ProjectID: p.ID, Name: "Repo", Path: "/source/runtime-workset", CreatedAt: testNow, UpdatedAt: testNow}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	mod := module.Module{ID: "module-runtime-workset", ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, mod); err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "area-runtime-workset", ProjectID: p.ID, CreatedByAISessionID: session.ID, Title: "Runtime", ProblemStatement: "Runtime workset fixture", Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{Goal: "runtime", Constraints: []string{"bounded"}, CodexInstruction: "runtime", ImplementationMode: task.ImplementationCodingAgent, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture waiver"}}
	taskValue := task.Task{ID: "task-runtime-workset", ProjectID: p.ID, FeatureAreaID: area.ID, ModuleID: mod.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, Model: "gpt-5.6-terra", ReasoningEffort: "high", Goal: work.Goal, Constraints: work.Constraints, WorkSpec: work, VerificationSpec: verification, BaseCommit: strings.Repeat("a", 40), Title: "Runtime workset", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewTaskStore(db).Create(ctx, taskValue); err != nil {
		t.Fatal(err)
	}
	executionID := seedExecutionOwner(t, db, taskValue, session, "execution-runtime-workset")
	runValue, err := NewRunStore(db).CreateExecutionAttempt(ctx, taskValue.ID, "run-runtime-workset", run.PurposeImplementation, executionID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	storedTask, err := NewTaskStore(db).Get(ctx, taskValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{project: p, repository: repo, task: storedTask, run: runValue}
}
