package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/workset"
)

func TestMemberResourceAdditionPreservesOriginAndFrozenAuthorization(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, func() (string, error) { return "workspace", nil }, nil)
	w, err := service.EnsureForFeatureArea(ctx, "project", "app", "area", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	app, _ := w.MemberForRepository("app")
	w, err = service.AttachMemberWorktree(ctx, w.ID, app.ID, "app-worktree")
	if err != nil {
		t.Fatal(err)
	}
	frozen := workset.Set{Version: 1, WorkspaceID: w.ID, Targets: []workset.Target{{RepositoryID: "app", MemberID: app.ID, MemberVersion: 1, OriginCommit: app.OriginCommit, Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}}
	before, _ := frozen.Fingerprint()
	for _, repo := range []string{"sdk", "backend"} {
		w, err = service.EnsureForFeatureArea(ctx, "project", repo, "area", strings.Repeat("b", 40))
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(w.Members) != 3 {
		t.Fatalf("members=%+v", w.Members)
	}
	if err = w.ValidateWorkset(frozen); err != nil {
		t.Fatal(err)
	}
	after, _ := frozen.Fingerprint()
	if before != after {
		t.Fatal("resource addition expanded old grant")
	}
	w, err = service.EnsureForFeatureArea(ctx, "project", "app", "area", strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	reused, _ := w.MemberForRepository("app")
	if reused.OriginCommit != app.OriginCommit || reused.WorktreeID != "app-worktree" {
		t.Fatal("later Task replaced existing work")
	}
	if _, err = service.AttachWorktree(ctx, w.ID, "arbitrary"); !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous scalar attach: %v", err)
	}
	if _, err = w.MemberForRepository("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing repo fell back")
	}
	sdk, _ := w.MemberForRepository("sdk")
	frozen.Targets = append(frozen.Targets, workset.Target{RepositoryID: "sdk", MemberID: sdk.ID, MemberVersion: 1, OriginCommit: sdk.OriginCommit, Read: true, Write: workset.WriteScope{Kind: "NONE"}})
	if err = w.ValidateWorkset(frozen); err == nil {
		t.Fatal("unprepared required member accepted")
	}
	w.Members[0].OriginCommit = strings.Repeat("d", 40)
	actual, _ := store.Get(ctx, w.ID)
	m, _ := actual.MemberForRepository("app")
	if m.OriginCommit != app.OriginCommit {
		t.Fatal("read alias mutated store")
	}
	if _, err = store.Update(ctx, w.ID, func(v *Workspace) error { v.Members = nil; return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal("historical members removed")
	}
}

func TestPlanMembersHasNoSideEffectsAndEnsureMaterializesExactFrozenSet(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, func() (string, error) { return "random-workspace", nil }, nil)
	targets := []workset.Target{
		{RepositoryID: "sdk", OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
		{RepositoryID: "app", OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
	}
	planned, err := service.PlanMembers(ctx, "project", "area", targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Targets) != 2 || planned.Targets[0].RepositoryID != "app" || planned.Targets[0].MemberID == "" || planned.WorkspaceID == "" {
		t.Fatalf("plan=%+v", planned)
	}
	if _, err = store.GetByFeatureArea(ctx, "area"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("planning created workspace: %v", err)
	}
	value, err := service.EnsurePlannedMembers(ctx, "project", "area", planned.WorkspaceID, planned.Targets)
	if err != nil {
		t.Fatal(err)
	}
	if value.RepositoryID != "" || value.BaseCommit != "" || len(value.Members) != 2 {
		t.Fatalf("multi workspace retained scalar authority: %+v", value)
	}
	for _, target := range planned.Targets {
		member, err := value.MemberForRepository(target.RepositoryID)
		if err != nil || member.ID != target.MemberID || member.OriginCommit != target.OriginCommit {
			t.Fatalf("member for %s=%+v err=%v", target.RepositoryID, member, err)
		}
	}
	if _, err = service.EnsurePlannedMembers(ctx, "project", "area", planned.WorkspaceID, []workset.Target{{RepositoryID: "app", MemberID: planned.Targets[0].MemberID, MemberVersion: 1, OriginCommit: strings.Repeat("c", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed frozen baseline accepted: %v", err)
	}
}

func TestInspectionPlanIsOwnerScopedReadOnlyAndMaterializesOnlyExactReceipt(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, nil, nil)
	targets := []workset.Target{{RepositoryID: "sdk", OriginCommit: strings.Repeat("b", 40), Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}}, {RepositoryID: "app", OriginCommit: strings.Repeat("a", 40), Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}}}
	planned, err := service.PlanInspectionMembers(ctx, "project", "inspection-task", "probe-a", targets)
	if err != nil || planned.WorkspaceID == "" || planned.Targets[0].RepositoryID != "app" {
		t.Fatalf("plan=%+v err=%v", planned, err)
	}
	if _, err = service.GetByOwner(ctx, OwnerProjectInspection, "probe-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("planning created workspace: %v", err)
	}
	value, err := service.EnsurePlannedInspectionMembers(ctx, "project", "inspection-task", "probe-a", planned.WorkspaceID, planned.Targets)
	if err != nil {
		t.Fatal(err)
	}
	if value.FeatureAreaID != "" || value.OwnerKind != OwnerProjectInspection || value.OwnerID != "probe-a" || value.InspectionTaskID != "inspection-task" || len(value.Members) != 2 {
		t.Fatalf("inspection workspace=%+v", value)
	}
	if _, err = service.EnsurePlannedInspectionMembers(ctx, "project", "inspection-task", "probe-a", planned.WorkspaceID, append(planned.Targets, workset.Target{RepositoryID: "other", MemberID: "member-foreign", MemberVersion: 1, OriginCommit: strings.Repeat("c", 40), Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}})); !errors.Is(err, ErrConflict) {
		t.Fatalf("expanded receipt accepted: %v", err)
	}
	probeBPlan, err := service.PlanInspectionMembers(ctx, "project", "inspection-task-b", "probe-b", targets)
	if err != nil {
		t.Fatal(err)
	}
	probeB, err := service.EnsurePlannedInspectionMembers(ctx, "project", "inspection-task-b", "probe-b", probeBPlan.WorkspaceID, probeBPlan.Targets)
	if err != nil || probeB.ID == value.ID || probeB.OwnerID != "probe-b" {
		t.Fatalf("different inspection owner reused workspace: %+v err=%v", probeB, err)
	}
	feature, err := service.EnsureForFeatureArea(ctx, "project", "app", "area", strings.Repeat("a", 40))
	if err != nil || feature.ID == value.ID || feature.ID == probeB.ID || feature.OwnerKind != OwnerFeatureArea {
		t.Fatalf("FeatureArea reused inspection workspace: %+v err=%v", feature, err)
	}
	if _, err = service.PlanInspectionMembers(ctx, "project", "inspection-task", "probe-a", []workset.Target{{RepositoryID: "app", MemberID: "wrong", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("same owner changed contender accepted: %v", err)
	}
	confirmed := []workset.Target{{RepositoryID: "confirmed", MemberID: "member_from_confirmed_probe", MemberVersion: 1, OriginCommit: strings.Repeat("d", 40), Read: true, ReadScope: workset.ReadScope{Kind: "PATHS", Paths: []string{"docs"}}, Write: workset.WriteScope{Kind: "NONE"}}}
	fromReceipt, err := service.PlanInspectionMembers(ctx, "project", "inspection-task", "probe-receipt", confirmed)
	if err != nil || fromReceipt.Targets[0].MemberID != confirmed[0].MemberID {
		t.Fatalf("confirmed member identity was regenerated: %+v err=%v", fromReceipt, err)
	}
	if _, err = service.PlanInspectionMembers(ctx, "project", "inspection-task", "probe-c", []workset.Target{{RepositoryID: "app", OriginCommit: strings.Repeat("a", 40), Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "REPOSITORY"}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("write-enabled inspection target accepted: %v", err)
	}
}

func TestInspectionWorkspaceLaterMemberCannotExpandFrozenRuntimeWorkset(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, nil, nil)
	commit := strings.Repeat("a", 40)
	targets := []workset.Target{{RepositoryID: "app", MemberID: "member-app", MemberVersion: 1, OriginCommit: commit, Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}}}
	planned, err := service.PlanInspectionMembers(ctx, "project", "task", "probe", targets)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.EnsurePlannedInspectionMembers(ctx, "project", "task", "probe", planned.WorkspaceID, planned.Targets)
	if err != nil {
		t.Fatal(err)
	}
	frozen := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersionProjectInspection, OwnerKind: workset.RuntimeOwnerProjectInspection, ProjectID: "project", InspectionOwnerTaskID: "task", InspectionOperationID: "probe", RunID: "run", WorkspaceID: resource.ID, PrimaryMemberID: "member-app", Members: []workset.RuntimeMemberBinding{{RepositoryID: "app", RepositoryAlias: "app", MemberID: "member-app", MemberVersion: 1, WorktreeID: "wt-app", CanonicalRoot: "/managed/app", RootFileIdentity: "root-app", OriginCommit: commit, StartHEAD: commit, StartSnapshot: strings.Repeat("b", 64), Read: true, IntendedReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "runtime-app"}}, BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "0.149.0", CreatedAt: time.Now().UTC()}
	frozen, err = frozen.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	hash := frozen.ContentHash
	_, err = store.Update(ctx, resource.ID, func(value *Workspace) error {
		now := time.Now().UTC()
		value.Members = append(value.Members, Member{ID: "member-later", WorkspaceID: value.ID, RepositoryID: "later", Version: 1, Alias: "later", OriginCommit: strings.Repeat("d", 40), Preparation: "PENDING", CreatedAt: now, UpdatedAt: now})
		value.UpdatedAt = now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.Get(ctx, resource.ID)
	if err != nil || len(current.Members) != 2 {
		t.Fatalf("workspace member addition=%+v err=%v", current, err)
	}
	if len(frozen.Members) != 1 || frozen.Members[0].RepositoryID != "app" || frozen.ContentHash != hash || frozen.Validate() != nil {
		t.Fatalf("workspace growth expanded frozen authority: %+v", frozen)
	}
}
