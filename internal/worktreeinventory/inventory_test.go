package worktreeinventory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type reposFixture []repository.Repository

func (v reposFixture) List(context.Context) ([]repository.Repository, error) { return v, nil }

type worktreesFixture []worktree.Worktree

func (v worktreesFixture) List(context.Context) ([]worktree.Worktree, error) { return v, nil }

type spacesFixture []workspace.Workspace

func (v spacesFixture) List(context.Context) ([]workspace.Workspace, error) { return v, nil }

type runsFixture []run.Run

func (v runsFixture) List(context.Context) ([]run.Run, error) { return v, nil }

type cycleFixture struct{}

func (cycleFixture) Current(context.Context, string, string) (developmentcycle.Cycle, error) {
	return developmentcycle.Cycle{DevelopmentRef: "refs/heads/main"}, nil
}

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s (%v)", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func TestInventoryRegistrationDirectoryAndPureRead(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "formal")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "init", "-b", "main")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base")
	base := fixtureGit(t, repo, "rev-parse", "HEAD")
	known := worktreesFixture{}
	spaces := spacesFixture{}
	runs := runsFixture{}
	paths := map[string]string{}
	for _, name := range []string{"integrated", "dirty", "active", "unmerged", "missing", "leftover", "removed", "line\nbreak"} {
		path := filepath.Join(root, name)
		paths[name] = path
		branch := strings.ReplaceAll(name, "\n", "-")
		fixtureGit(t, repo, "worktree", "add", "-b", branch, path, base)
		known = append(known, worktree.Worktree{ID: name, RepositoryID: "repo", Path: path, RunID: "run-" + name, UpdatedAt: time.Unix(1, 0)})
		spaces = append(spaces, workspace.Workspace{ProjectID: "project", RepositoryID: "repo", FeatureAreaID: "area-" + name, WorktreeID: name})
		runs = append(runs, run.Run{ID: "run-" + name, ProjectID: "project", TaskID: "task-" + name, FeatureAreaID: "area-" + name, WorktreeID: name, Status: run.StatusSucceeded, UpdatedAt: time.Unix(2, 0)})
	}
	if err := os.WriteFile(filepath.Join(paths["dirty"], "untracked.txt"), []byte("do not delete"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, paths["unmerged"], "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "unique")
	for i := range runs {
		if runs[i].WorktreeID == "active" {
			runs[i].Status = run.StatusRunning
			runs[i].OperationKind = run.OperationExecution
			runs[i].OperationID = "execution-active"
		}
	}
	// Simulate already broken/removed states by moving only isolated fixture
	// directories. The inventory never invokes cleanup or mutates these facts.
	for _, name := range []string{"leftover", "removed"} {
		if err := os.Rename(filepath.Join(repo, ".git", "worktrees", name), filepath.Join(t.TempDir(), "registration")); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"missing", "removed"} {
		if err := os.Rename(paths[name], filepath.Join(t.TempDir(), "old-directory")); err != nil {
			t.Fatal(err)
		}
	}
	before := fixtureGit(t, repo, "worktree", "list", "--porcelain", "-z")
	indexBefore, err := os.Stat(filepath.Join(repo, ".git", "index"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s := Service{Dependencies{Repositories: reposFixture{{ID: "repo", ProjectID: "project", Path: repo}, {ID: "foreign", ProjectID: "other", Path: filepath.Join(root, "not-readable")}}, Worktrees: known, Workspaces: spaces, Runs: runs, Conversations: conversationFixture{status: "READY"}, DevelopmentCycles: cycleFixture{}, Git: bridgegit.NewClient(bridgegit.Config{})}}
	first, err := s.Read(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Item{}
	for _, v := range first.Items {
		got[v.WorktreeID] = v
	}
	expect := map[string]string{"": "PROTECTED_FORMAL", "integrated": "SAFE_TO_CLEAN", "dirty": "PROTECTED_DIRTY", "active": "ACTIVE", "unmerged": "PROTECTED_UNMERGED", "missing": "MISSING_DIRECTORY", "leftover": "CLEANUP_PENDING", "removed": "REMOVED", "line\nbreak": "SAFE_TO_CLEAN"}
	if len(first.Items) != len(expect) {
		t.Fatalf("items=%+v", first.Items)
	}
	for id, state := range expect {
		if got[id].CleanupState != state {
			t.Errorf("%q: want %s, got %+v", id, state, got[id])
		}
	}
	if !got["missing"].GitRegistered || got["missing"].DirectoryExists {
		t.Fatal("missing directory facts collapsed")
	}
	if got["leftover"].GitRegistered || !got["leftover"].DirectoryExists {
		t.Fatal("leftover directory facts collapsed")
	}
	if got["removed"].GitRegistered || got["removed"].DirectoryExists {
		t.Fatal("removed facts wrong")
	}
	if got["integrated"].Head != base || got["integrated"].IntegrationTargetHead != base || got["unmerged"].HasUniqueCommits == nil || !*got["unmerged"].HasUniqueCommits {
		t.Fatal("Git ancestry facts missing")
	}
	if got["active"].ActiveOperation == nil || got["active"].ActiveOperation.ID != "execution-active" || got["active"].TaskID != "task-active" {
		t.Fatal("exact owner missing")
	}
	again, err := s.Read(ctx, "project")
	if err != nil {
		t.Fatal(err)
	}
	first.RefreshedAt = again.RefreshedAt
	if !reflect.DeepEqual(first, again) {
		t.Fatal("refresh changed inventory facts")
	}
	if after := fixtureGit(t, repo, "worktree", "list", "--porcelain", "-z"); before != after {
		t.Fatal("refresh mutated Git registrations")
	}
	if content, err := os.ReadFile(filepath.Join(paths["dirty"], "untracked.txt")); err != nil || string(content) != "do not delete" {
		t.Fatal("refresh changed dirty files")
	}
	indexAfter, err := os.Stat(filepath.Join(repo, ".git", "index"))
	if indexBefore == nil {
		if !os.IsNotExist(err) {
			t.Fatal("refresh created index")
		}
	} else if err != nil || !indexBefore.ModTime().Equal(indexAfter.ModTime()) {
		t.Fatal("refresh rewrote index")
	}
	text := RenderText(again)
	for _, term := range []string{"gitRegistered=false", "directoryExists=true", "MISSING_DIRECTORY", "CLEANUP_PENDING", "REMOVED", "activeOperation=EXECUTION:execution-active runId=run-active", "hasUniqueCommits=true", "worktreeId=active repositoryId=repo featureAreaId=area-active", "lastActivityAt=1970-01-01T00:00:02Z"} {
		if !strings.Contains(text, term) {
			t.Errorf("text missing %s", term)
		}
	}
}

func TestCleanupNeverAuthorizesUnknownOrFormal(t *testing.T) {
	v := Item{Kind: "MANAGED", GitRegistered: true, GitRegistrationKnown: true, DirectoryExists: true, DirectoryExistenceKnown: true, WorkingTreeState: "CLEAN", IntegrationState: "INTEGRATED", ActivityState: "IDLE"}
	for _, mutate := range []func(*Item){func(v *Item) { v.Kind = "FORMAL" }, func(v *Item) { v.GitRegistrationKnown = false }, func(v *Item) { v.DirectoryExistenceKnown = false }, func(v *Item) { v.WorkingTreeState = "UNKNOWN" }, func(v *Item) { v.IntegrationState = "UNKNOWN" }, func(v *Item) { v.ActivityState = "UNKNOWN" }, func(v *Item) { v.Locked = true }} {
		copy := v
		mutate(&copy)
		if CleanupState(copy) == "SAFE_TO_CLEAN" {
			t.Fatalf("unsafe recommendation: %+v", copy)
		}
	}
}

type conversationFixture struct {
	status string
	err    error
}

func (v conversationFixture) State(context.Context, string) (agentconversation.State, error) {
	return agentconversation.State{Status: v.status}, v.err
}

type inventoryGitFixture struct{ path string }

func (v inventoryGitFixture) RegisteredWorktrees(context.Context, string) ([]bridgegit.RegisteredWorktree, error) {
	return []bridgegit.RegisteredWorktree{{Path: v.path, Head: "candidate"}}, nil
}
func (v inventoryGitFixture) InventoryStatus(context.Context, string) (string, bridgegit.Status, error) {
	return v.path, bridgegit.Status{}, nil
}
func (v inventoryGitFixture) InventoryCommit(context.Context, string, string) (string, error) {
	return "target", nil
}
func (v inventoryGitFixture) InventoryAncestor(context.Context, string, string, string) (bool, error) {
	return true, nil
}

func TestInventoryActivityFailsClosedAndSelectsNewestScopedRun(t *testing.T) {
	path := t.TempDir()
	deps := Dependencies{Repositories: reposFixture{{ID: "repo", ProjectID: "project", Path: t.TempDir()}}, Worktrees: worktreesFixture{{ID: "worktree", RepositoryID: "repo", Path: path}}, Workspaces: spacesFixture{{ID: "workspace", ProjectID: "project", WorktreeID: "worktree", FeatureAreaID: "area"}}, Runs: runsFixture{}, DevelopmentCycles: cycleFixture{}, Git: inventoryGitFixture{path: path}}
	read := func() Item {
		t.Helper()
		s := Service{deps}
		v, e := s.Read(context.Background(), "project")
		if e != nil {
			t.Fatal(e)
		}
		for _, item := range v.Items {
			if item.WorktreeID == "worktree" {
				return item
			}
		}
		t.Fatal("missing worktree")
		return Item{}
	}
	for _, status := range []string{"MISSING_DEPENDENCY", "", "RECOVERY_REQUIRED"} {
		if status == "MISSING_DEPENDENCY" {
			deps.Conversations = nil
		} else {
			deps.Conversations = conversationFixture{status: status}
		}
		got := read()
		if got.ActivityState != "UNKNOWN" || got.CleanupState != "UNKNOWN" || len(got.ReadErrors) == 0 {
			t.Fatalf("%s: %+v", status, got)
		}
	}
	for _, status := range []string{"READY", "UNAVAILABLE"} {
		deps.Conversations = conversationFixture{status: status}
		if got := read(); got.CleanupState != "SAFE_TO_CLEAN" {
			t.Fatalf("known %s idle=%+v", status, got)
		}
	}
	newest := run.Run{ID: "new", WorkspaceID: "workspace", TaskID: "task-new", OperationKind: run.OperationExecution, OperationID: "execution-new", Status: run.StatusRunning, UpdatedAt: time.Unix(3, 0)}
	older := run.Run{ID: "old", ProjectID: "project", WorktreeID: "worktree", TaskID: "task-old", OperationKind: run.OperationExecution, OperationID: "execution-old", Status: run.StatusRunning, UpdatedAt: time.Unix(2, 0)}
	foreign := run.Run{ID: "foreign", ProjectID: "foreign", WorktreeID: "worktree", Status: run.StatusRunning, UpdatedAt: time.Unix(9, 0)}
	for _, runs := range []runsFixture{{newest, older, foreign}, {older, foreign, newest}} {
		deps.Runs = runs
		got := read()
		if got.CleanupState != "ACTIVE" || got.TaskID != "task-new" || got.ActiveOperation == nil || got.ActiveOperation.RunID != "new" || !got.LastActivityAt.Equal(newest.UpdatedAt) {
			t.Fatalf("activity=%+v", got)
		}
	}
	deps.Runs = runsFixture{foreign}
	if got := read(); got.ActiveOperation != nil || got.TaskID != "" {
		t.Fatalf("foreign operation included=%+v", got)
	}
}
