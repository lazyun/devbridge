package worktree

import (
	"context"
	"errors"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThreeIndependentRepositoryMembersRecoverPhysicalCreation(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "managed")
	manager, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]CreateInput, 3)
	trees := make([]Worktree, 3)
	for i, name := range []string{"app", "sdk", "backend"} {
		repo, _ := worktreeRepo(t)
		writeWorktree(t, filepath.Join(repo, "identity.txt"), name)
		gitWorktree(t, repo, "add", ".")
		gitWorktree(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", name)
		base := strings.TrimSpace(gitWorktree(t, repo, "rev-parse", "HEAD"))
		inputs[i] = CreateInput{RepositoryID: name, RepositoryPath: repo, FeatureAreaID: "area", WorkspaceID: "workspace", MemberID: "member-" + name, MemberVersion: 1, RunID: "logical-run", BaseCommit: base}
		trees[i], err = manager.Create(ctx, inputs[i])
		if err != nil {
			t.Fatal(err)
		}
		writeWorktree(t, filepath.Join(trees[i].Path, "pending.txt"), name+" work")
	}
	if trees[0].Path == trees[1].Path || trees[0].ID == trees[1].ID || trees[0].Branch == trees[1].Branch {
		t.Fatal("member collision")
	}
	// New process has no metadata: same frozen member intent recovers physical
	// worktree identity, retaining dirty work and original baselines in all repos.
	reopened, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	for i, in := range inputs {
		in.RunID = "later-task-run"
		recovered, e := reopened.Create(ctx, in)
		if e != nil || recovered.ID != trees[i].ID || recovered.BaseCommit != in.BaseCommit {
			t.Fatalf("recovery=%+v err=%v", recovered, e)
		}
		content, e := os.ReadFile(filepath.Join(recovered.Path, "pending.txt"))
		if e != nil || string(content) != in.RepositoryID+" work" {
			t.Fatal("lost dirty work", e)
		}
	}
	broken := inputs[1]
	broken.MemberID = "new-member"
	broken.BaseCommit = "ffffffffffffffffffffffffffffffffffffffff"
	if _, err = reopened.Create(ctx, broken); err == nil {
		t.Fatal("missing required baseline accepted")
	}
	for _, tree := range trees {
		if _, err = os.Stat(filepath.Join(tree.Path, "pending.txt")); err != nil {
			t.Fatal("partial failure lost other member", err)
		}
	}
}

// failingMemberStore models response loss after the physical Git operation and
// before its durable attach. All data remains within the test's temp directory.
type failingMemberStore struct {
	fail   bool
	values map[string]Worktree
}

func (s *failingMemberStore) Create(_ context.Context, v Worktree) error {
	if s.fail {
		s.fail = false
		return errors.New("injected metadata write failure")
	}
	s.values[v.ID] = v
	return nil
}
func (s *failingMemberStore) Get(_ context.Context, id string) (Worktree, error) {
	v, ok := s.values[id]
	if !ok {
		return v, ErrNotFound
	}
	return v, nil
}
func (s *failingMemberStore) List(context.Context) ([]Worktree, error) {
	var out []Worktree
	for _, v := range s.values {
		out = append(out, v)
	}
	return out, nil
}
func (s *failingMemberStore) Update(_ context.Context, id string, fn func(*Worktree) error) (Worktree, error) {
	v, ok := s.values[id]
	if !ok {
		return v, ErrNotFound
	}
	if err := fn(&v); err != nil {
		return v, err
	}
	s.values[id] = v
	return v, nil
}
func TestMemberDurableCreationRecoversMetadataFailure(t *testing.T) {
	ctx := context.Background()
	repo, base := worktreeRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	external, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	store := &failingMemberStore{fail: true, values: map[string]Worktree{}}
	durable := &DurableManager{External: external, Store: store}
	input := CreateInput{RepositoryID: "app", RepositoryPath: repo, FeatureAreaID: "area", WorkspaceID: "workspace", MemberID: "member-app", MemberVersion: 1, RunID: "run", BaseCommit: base}
	if _, err = durable.Create(ctx, input); err == nil {
		t.Fatal("metadata failure missing")
	}
	physical, err := external.List(ctx)
	if err != nil || len(physical) != 1 {
		t.Fatalf("physical facts=%+v %v", physical, err)
	}
	writeWorktree(t, filepath.Join(physical[0].Path, "pending.txt"), "preserve")
	restarted, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	durable.External = restarted
	recovered, err := durable.Create(ctx, input)
	if err != nil || recovered.ID != physical[0].ID {
		t.Fatalf("recovered=%+v %v", recovered, err)
	}
	raw, err := os.ReadFile(filepath.Join(recovered.Path, "pending.txt"))
	if err != nil || string(raw) != "preserve" {
		t.Fatal("recovery discarded work", err)
	}
	if _, err = durable.Create(ctx, input); err != nil {
		t.Fatal("replay failed", err)
	}
}

func TestInspectionWorkspaceAttachReplaysPhysicalWorktreeAfterMetadataGap(t *testing.T) {
	ctx := context.Background()
	repo, base := worktreeRepo(t)
	workspaceStore := workspace.NewMemoryStore()
	workspaceService := workspace.NewService(workspaceStore, nil, nil)
	targets := []workset.Target{{RepositoryID: "app", MemberID: "member-inspection-app", MemberVersion: 1, OriginCommit: base, Read: true, ReadScope: workset.ReadScope{Kind: "REPOSITORY"}, Write: workset.WriteScope{Kind: "NONE"}}}
	plan, err := workspaceService.PlanInspectionMembers(ctx, "project", "inspection-task", "probe", targets)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := workspaceService.EnsurePlannedInspectionMembers(ctx, "project", "inspection-task", "probe", plan.WorkspaceID, plan.Targets)
	if err != nil {
		t.Fatal(err)
	}
	member, err := resource.MemberForRepository("app")
	if err != nil || member.WorktreeID != "" || member.OriginCommit != base {
		t.Fatalf("initial member=%+v err=%v", member, err)
	}

	root := filepath.Join(t.TempDir(), "managed")
	external, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	metadata := &failingMemberStore{fail: true, values: map[string]Worktree{}}
	durable := &DurableManager{External: external, Store: metadata}
	input := CreateInput{PreserveOnFailure: true, RepositoryID: "app", RepositoryPath: repo, OwnerKind: "PROJECT_INSPECTION", OwnerID: "probe", WorkspaceID: resource.ID, MemberID: member.ID, MemberVersion: member.Version, RunID: "inspection-run", BaseCommit: member.OriginCommit}
	if _, err = durable.Create(ctx, input); err == nil {
		t.Fatal("simulated post-physical metadata loss was accepted")
	}
	physical, err := external.List(ctx)
	if err != nil || len(physical) != 1 {
		t.Fatalf("physical worktree=%+v err=%v", physical, err)
	}
	writeWorktree(t, filepath.Join(physical[0].Path, "preserve.txt"), "do not delete")

	restarted, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	durable.External = restarted
	recovered, err := durable.Create(ctx, input)
	if err != nil || recovered.ID != physical[0].ID || recovered.BaseCommit != base {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if _, err = workspaceService.AttachMemberWorktree(ctx, resource.ID, member.ID, recovered.ID); err != nil {
		t.Fatal(err)
	}
	current, err := workspaceService.Get(ctx, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := current.MemberForRepository("app")
	if err != nil || attached.WorktreeID != recovered.ID || attached.OriginCommit != base || attached.Preparation != "READY" {
		t.Fatalf("attached=%+v err=%v", attached, err)
	}
	if _, err = durable.Create(ctx, input); err != nil {
		t.Fatalf("exact replay after attach: %v", err)
	}
	items, err := restarted.List(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("duplicate physical worktree after replay: %+v err=%v", items, err)
	}
	if raw, err := os.ReadFile(filepath.Join(recovered.Path, "preserve.txt")); err != nil || string(raw) != "do not delete" {
		t.Fatalf("replay replaced or removed physical worktree: %q %v", raw, err)
	}
}

func TestAddingMembersNeverNestsBelowLegacyAuthorizedRoot(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "managed")
	manager, err := NewManager(root, bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	appRepo, appBase := worktreeRepo(t)
	app, err := manager.Create(ctx, CreateInput{RepositoryID: "app", RepositoryPath: appRepo, FeatureAreaID: "area", WorkspaceID: "legacy-workspace", RunID: "legacy-run", BaseCommit: appBase})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := os.OpenRoot(app.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	for _, repoID := range []string{"sdk", "backend"} {
		repo, base := worktreeRepo(t)
		member, err := manager.Create(ctx, CreateInput{RepositoryID: repoID, RepositoryPath: repo, FeatureAreaID: "area", WorkspaceID: "legacy-workspace", MemberID: "member-" + repoID, MemberVersion: 1, RunID: "later-run", BaseCommit: base})
		if err != nil {
			t.Fatal(err)
		}
		if within(app.Path, member.Path) || within(member.Path, app.Path) {
			t.Fatalf("overlapping roots: %s %s", app.Path, member.Path)
		}
		relative, err := filepath.Rel(app.Path, filepath.Join(member.Path, "tracked.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if f, err := opened.Open(relative); err == nil {
			f.Close()
			t.Fatal("old grant accessed new member")
		}
	}
}

func TestProjectInspectionWorktreeUsesProbeNamespaceAndExactReplay(t *testing.T) {
	ctx := context.Background()
	repo, base := worktreeRepo(t)
	manager, err := NewManager(filepath.Join(t.TempDir(), "managed"), bridgegit.NewClient(bridgegit.Config{}))
	if err != nil {
		t.Fatal(err)
	}
	in := CreateInput{RepositoryID: "repo", RepositoryPath: repo, OwnerKind: "PROJECT_INSPECTION", OwnerID: "probe-1", WorkspaceID: "workspace", MemberID: "member", MemberVersion: 1, RunID: "run", BaseCommit: base}
	first, err := manager.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Branch, "PROJECT_INSPECTION-probe-1") {
		t.Fatalf("inspection branch did not use probe owner: %s", first.Branch)
	}
	if _, err = manager.Create(ctx, in); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	in.OwnerID = "probe-2"
	second, err := manager.Create(ctx, in)
	if err != nil || second.ID == first.ID || second.Path == first.Path || second.Branch == first.Branch {
		t.Fatalf("inspection owner reused physical worktree: first=%+v second=%+v err=%v", first, second, err)
	}
}
