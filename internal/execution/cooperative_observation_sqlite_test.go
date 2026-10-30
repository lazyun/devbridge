package execution

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
)

// Use the production schema, owner admission, binding validator and persistence
// store. Only metadata/evidence errors are injected; no model is launched.
func cooperativeObservationSQLiteFixture(t *testing.T) (*sqlite.RunStore, run.RepositoryObservation) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	now := time.Now().UTC()
	base := strings.Repeat("a", 40)
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := project.Project{ID: "observation-project", Name: "Observation", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	sessions := sqlite.NewAISessionStore(db)
	session := "observation-session"
	if _, err = sessions.InitializeSession(ctx, "observation-test", "initialize", session, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = sessions.BindSessionProject(ctx, "observation-test", "bind", session, p.ID, "selection", now); err != nil {
		t.Fatal(err)
	}
	repo := repository.Repository{ID: "observation-repo", ProjectID: p.ID, Name: "Repo", Path: filepath.Join(root, "source"), CreatedAt: now, UpdatedAt: now}
	if _, _, err = sqlite.NewRepositoryStore(db).PutIfAbsent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "observation-area", ProjectID: p.ID, CreatedByAISessionID: session, Title: "Observation", ProblemStatement: "Preserve unknown observation", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	mod := module.Module{ID: "observation-module", ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewModuleStore(db).Create(ctx, mod); err != nil {
		t.Fatal(err)
	}
	value := task.Task{ID: "observation-task", ModuleID: mod.ID, ProjectID: p.ID, FeatureAreaID: area.ID, CodexConfigID: codexconfig.DefaultConfigID, Model: "gpt-5.6-terra", ReasoningEffort: "high", Goal: "observe", Title: "observe", WorkSpec: task.WorkSpec{Goal: "observe", CodexInstruction: "observe", ImplementationMode: task.ImplementationCodingAgent, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "fixture"}}, BaseCommit: "", Type: task.TypeDevelopment, Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewTaskStore(db).Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewService(sqlite.NewWorkspaceStore(db), func() (string, error) { return "observation-workspace", nil }, func() time.Time { return now })
	planned, err := ws.PlanMembers(ctx, p.ID, area.ID, []workset.Target{{RepositoryID: repo.ID, OriginCommit: base, Read: true, Write: workset.WriteScope{Kind: "NONE"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ws.EnsurePlannedMembers(ctx, p.ID, area.ID, planned.WorkspaceID, planned.Targets); err != nil {
		t.Fatal(err)
	}
	config, err := sqlite.NewCodexConfigStore(db).Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selected := config.Selection("observation-test")
	reviews, err := review.NewService(sqlite.NewReviewStore(db), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	admission, err := reviews.AdmitStart(ctx, review.Review{TaskID: value.ID, ProjectID: p.ID, FeatureAreaID: area.ID, CreatedByAISessionID: session, AuthorKind: string(review.AuthorAISession), AuthorID: session, WorkspaceID: planned.WorkspaceID, RepositoryTargets: planned.Targets, PrimaryMemberID: planned.Targets[0].MemberID, ExecutorKind: review.ExecutorCodingAgent, CodexConfigID: config.ID, BackendSelection: &selected, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: value.Model, ReasoningEffort: value.ReasoningEffort}, Model: value.Model, ReasoningEffort: value.ReasoningEffort, ReviewDepth: task.ReviewDepthDeep, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Observe frozen source", Instruction: "Read source"}, "observation-review", strings.Repeat("b", 64), "Read source", scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
	if err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewRunStore(db)
	target := planned.Targets[0]
	binding := run.RepositoryBinding{RunID: admission.Run.ID, ReviewID: admission.Review.ID, ProjectID: p.ID, FeatureAreaID: area.ID, WorkspaceID: planned.WorkspaceID, RepositoryID: repo.ID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, OriginCommit: base, Preparation: "PENDING", RepositoryAlias: "app", StartHEAD: base, StartSnapshot: base, IntendedRead: true, IntendedWrite: target.Write, RuntimeRootIdentity: "frozen-root"}
	if err = store.PutRepositoryBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	return store, run.RepositoryObservation{RunID: admission.Run.ID, RepositoryID: repo.ID, EndHEAD: base, EndSnapshot: base, EndRootIdentity: "frozen-root", ChangedPaths: []string{"observed.txt"}, DiffDigest: strings.Repeat("c", 64), ObservationState: "NO_CHANGE", ObservedAt: now}
}

func TestCooperativeReviewMetadataUnknownPersistsThroughSQLite(t *testing.T) {
	for _, name := range []string{"metadata-failed-after-known", "initial-unknown-then-mutation", "known-mutation"} {
		t.Run(name, func(t *testing.T) {
			store, observation := cooperativeObservationSQLiteFixture(t)
			state := observation.ObservationState
			var cause, metadataErr, mutation error
			switch name {
			case "metadata-failed-after-known":
				metadataErr = errors.New("index observation failed")
			case "initial-unknown-then-mutation":
				state = "UNKNOWN"
				cause = errors.New("source observation failed")
				observation = run.RepositoryObservation{RunID: observation.RunID, RepositoryID: observation.RepositoryID, ObservationState: state, ObservedAt: observation.ObservedAt}
				mutation = errors.New("later metadata changed")
			case "known-mutation":
				mutation = errors.New("metadata changed")
			}
			reduced, state, err := mergeReviewMetadataObservation(observation, state, cause, metadataErr, mutation)
			want := "UNKNOWN"
			if name == "known-mutation" {
				want = "SCOPE_CONFLICT"
			}
			if err == nil || state != want || reduced.ObservationState != want {
				t.Fatalf("reducer state=%s observation=%+v err=%v", state, reduced, err)
			}
			if err = store.ObserveRepositoryBinding(context.Background(), reduced); err != nil {
				t.Fatalf("real SQLite rejected reducer output: %+v: %v", reduced, err)
			}
			rows, err := store.ListRepositoryBindings(context.Background(), reduced.RunID)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			got := rows[0]
			if got.ObservationState != want || got.ObservedAt == nil {
				t.Fatalf("durable terminal state=%+v", got)
			}
			if want == "UNKNOWN" && (got.EndHEAD != "" || got.EndSnapshot != "" || got.EndRootIdentity != "" || len(got.ChangedPaths) != 0 || got.DiffDigest != "") {
				t.Fatalf("UNKNOWN retained invented end facts: %+v", got)
			}
			if want == "SCOPE_CONFLICT" && got.EndHEAD == "" {
				t.Fatal("complete mutation lost known evidence")
			}
		})
	}
}
