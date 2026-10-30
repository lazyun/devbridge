package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
)

func TestWorkspaceMemberStoreExactIdentityAndRollback(t *testing.T) {
	f, _ := projectIntentFixture(t)
	ctx := context.Background()
	for _, id := range []string{"app", "sdk", "backend"} {
		r := repository.Repository{ID: id, ProjectID: f.project, Name: "same-name", Path: filepath.Join(t.TempDir(), "source"), CreatedAt: testNow, UpdatedAt: testNow}
		if _, _, err := NewRepositoryStore(f.db).PutIfAbsent(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	store := NewWorkspaceStore(f.db)
	svc := workspace.NewService(store, nil, nil)
	first, err := svc.EnsureForFeatureArea(ctx, f.project, "app", f.area, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sdk", "backend"} {
		if _, err = svc.EnsureForFeatureArea(ctx, f.project, id, f.area, strings.Repeat("b", 40)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Get(ctx, first.ID)
	if err != nil || len(got.Members) != 3 {
		t.Fatalf("members=%+v err=%v", got, err)
	}
	app, err := got.MemberForRepository("app")
	if err != nil || app.OriginCommit != first.BaseCommit {
		t.Fatal("origin changed", err)
	}
	if _, err = store.Update(ctx, got.ID, func(v *workspace.Workspace) error { v.Members[0].OriginCommit = strings.Repeat("c", 40); return nil }); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("origin mutation=%v", err)
	}
	if _, err = svc.EnsureForFeatureArea(ctx, f.project, "unregistered", f.area, strings.Repeat("d", 40)); err == nil {
		t.Fatal("unregistered member attached")
	}
	restored, err := store.GetByFeatureArea(ctx, f.area)
	if err != nil || len(restored.Members) != 3 || restored.ID != first.ID {
		t.Fatalf("partial member write=%+v %v", restored, err)
	}
	if _, err = f.db.sql.Exec(`DELETE FROM workspace_members WHERE id=?`, app.ID); err == nil {
		t.Fatal("historical member deleted")
	}
}

func TestWorkspaceMemberIDCollisionCannotModifyAnotherWorkspace(t *testing.T) {
	f, _ := projectIntentFixture(t)
	ctx := context.Background()
	r := repository.Repository{ID: "app", ProjectID: f.project, Name: "App", Path: filepath.Join(t.TempDir(), "source"), CreatedAt: testNow, UpdatedAt: testNow}
	if _, _, err := NewRepositoryStore(f.db).PutIfAbsent(ctx, r); err != nil {
		t.Fatal(err)
	}
	store := NewWorkspaceStore(f.db)
	svc := workspace.NewService(store, nil, nil)
	first, err := svc.EnsureForFeatureArea(ctx, f.project, r.ID, f.area, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "other-area", ProjectID: f.project, CreatedByAISessionID: f.session, Title: "Other", ProblemStatement: "Another workspace", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}
	if err = NewFeatureAreaStore(f.db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	other := workspace.Workspace{ID: "other-workspace", ProjectID: f.project, RepositoryID: r.ID, FeatureAreaID: area.ID, BaseCommit: first.BaseCommit, Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}
	collision := first.Members[0]
	collision.WorkspaceID = other.ID
	collision.Preparation = "FAILED"
	collision.Failure = "must not overwrite another owner"
	other.Members = []workspace.Member{collision}
	if err = store.Create(ctx, other); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("ID collision error=%v", err)
	}
	if _, err = store.Get(ctx, other.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("partial workspace exists: %v", err)
	}
	got, err := store.Get(ctx, first.ID)
	if err != nil || got.Members[0].Preparation != "PENDING" || got.Members[0].Failure != "" {
		t.Fatalf("other owner's state modified: %+v %v", got, err)
	}
}

// V82 adds the Project Inspection owner variant.  It must retain the v75
// FeatureArea multi-repository representation: the workspace itself has no
// scalar repository/base, while its immutable member set carries both.
func TestV82MigrationPreservesFeatureAreaMultiRepositoryWorkspaceMaterialization(t *testing.T) {
	f, _ := projectIntentFixture(t)
	ctx := context.Background()
	for _, id := range []string{"app", "sdk"} {
		if _, _, err := NewRepositoryStore(f.db).PutIfAbsent(ctx, repository.Repository{ID: id, ProjectID: f.project, Name: id, Path: filepath.Join(t.TempDir(), id), CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	svc := workspace.NewService(NewWorkspaceStore(f.db), nil, nil)
	planned, err := svc.PlanMembers(ctx, f.project, f.area, []workset.Target{{RepositoryID: "app", OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}, {RepositoryID: "sdk", OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := svc.EnsurePlannedMembers(ctx, f.project, f.area, planned.WorkspaceID, planned.Targets)
	if err != nil {
		t.Fatal(err)
	}
	if value.RepositoryID != "" || value.BaseCommit != "" || len(value.Members) != 2 {
		t.Fatalf("workspace=%+v", value)
	}
	reopened, err := NewWorkspaceStore(f.db).Get(ctx, value.ID)
	if err != nil || reopened.RepositoryID != "" || reopened.BaseCommit != "" || len(reopened.Members) != 2 {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
}

func TestWorkspaceStorePersistsProjectInspectionOwnerWithoutFeatureArea(t *testing.T) {
	f, _ := projectIntentFixture(t)
	ctx := context.Background()
	for _, id := range []string{"inspection-app", "inspection-sdk"} {
		if _, _, err := NewRepositoryStore(f.db).PutIfAbsent(ctx, repository.Repository{ID: id, ProjectID: f.project, Name: id, Path: filepath.Join(t.TempDir(), id), CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	probes := NewContextProbeStore(f.db)
	probe := contextprobe.Probe{ID: "inspection-probe", AISessionID: f.session, ProjectID: f.project, RepositoryID: "inspection-app", CodexConfigID: "codex_config_default", Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository, Paths: []string{}}, CodexInstruction: "inspect exact roots", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: testNow, UpdatedAt: testNow}
	if err := probes.Create(ctx, probe); err != nil {
		t.Fatal(err)
	}
	probe, err := probes.Get(ctx, probe.ID)
	if err != nil || probe.TaskID == "" {
		t.Fatalf("probe=%+v err=%v", probe, err)
	}
	svc := workspace.NewService(NewWorkspaceStore(f.db), nil, nil)
	targets := []workset.Target{{RepositoryID: "inspection-app", MemberID: "inspection-member-app", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, ReadScope: workset.ReadScope{Kind: "PATHS", Paths: []string{"docs"}}, Write: workset.WriteScope{Kind: "NONE"}}, {RepositoryID: "inspection-sdk", MemberID: "inspection-member-sdk", MemberVersion: 1, OriginCommit: strings.Repeat("b", 40), Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}}}
	planned, err := svc.PlanInspectionMembers(ctx, f.project, probe.TaskID, probe.ID, targets)
	if err != nil {
		t.Fatal(err)
	}
	value, err := svc.EnsurePlannedInspectionMembers(ctx, f.project, probe.TaskID, probe.ID, planned.WorkspaceID, planned.Targets)
	if err != nil {
		t.Fatal(err)
	}
	if value.FeatureAreaID != "" || value.OwnerKind != workspace.OwnerProjectInspection || value.OwnerID != probe.ID || value.InspectionTaskID != probe.TaskID || value.InspectionID != probe.ID {
		t.Fatalf("workspace=%+v", value)
	}
	reopened, err := NewWorkspaceStore(f.db).GetByOwner(ctx, workspace.OwnerProjectInspection, probe.ID)
	if err != nil || reopened.ID != value.ID || len(reopened.Members) != 2 || reopened.FeatureAreaID != "" {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	if _, err = NewWorkspaceStore(f.db).GetByFeatureArea(ctx, probe.ID); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("inspection appeared as FeatureArea workspace: %v", err)
	}
}
