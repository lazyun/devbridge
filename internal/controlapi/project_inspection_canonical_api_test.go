package controlapi_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/task"
)

type projectInspectionAPIFixture struct {
	ctx     context.Context
	plane   *controlplane.ControlPlane
	facade  *controlapi.Facade
	session string
	project string
	repos   map[string]string
}

func newProjectInspectionAPIFixture(t *testing.T) *projectInspectionAPIFixture {
	t.Helper()
	caller := "project-inspection-" + strings.ReplaceAll(t.Name(), "/", "-")
	f := &projectInspectionAPIFixture{ctx: aisession.WithCallerScope(context.Background(), caller), repos: map[string]string{}}
	root := t.TempDir()
	var err error
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.plane.Close() })
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	git := func(path string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("git %v: %s: %v", args, out, runErr)
		}
		return strings.TrimSpace(string(out))
	}
	inputs := []projectregistration.RepositoryInput{}
	for _, name := range []string{"app", "sdk", "backend"} {
		path := filepath.Join(root, name)
		if err = os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		git(path, "init", "-b", "main")
		if err = os.WriteFile(filepath.Join(path, "same.txt"), []byte(name+" source\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(path, "add", "same.txt")
		git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
		inputs = append(inputs, projectregistration.RepositoryInput{Path: path, Alias: name})
	}
	registered, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "register", Name: "Project Inspection", Repositories: inputs})
	if err != nil {
		t.Fatal(err)
	}
	f.project = registered.Project.ID
	ref := aiprojectselection.ClientBindingReference{CallerScope: caller}
	initialized, err := f.plane.AIContext.InitializeSession(f.ctx, ref, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	f.session = initialized.AISessionID
	if _, err = f.plane.AIContext.BindProject(f.ctx, ref, f.session, f.project, "bind"); err != nil {
		t.Fatal(err)
	}
	repositories, err := f.plane.Repositories.List(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range repositories {
		f.repos[value.Name] = value.ID
	}
	return f
}

func projectInspectionCounts(t *testing.T, f *projectInspectionAPIFixture) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"context_probes", "tasks", "workspaces", "workspace_members", "worktrees", "agent_conversations", "project_inspection_agent_claims", "runs", "events"} {
		var count int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		out[table] = count
	}
	return out
}

func TestProjectInspectionPrepareConfirmProductionFacade(t *testing.T) {
	f := newProjectInspectionAPIFixture(t)
	config, err := f.plane.CodexConfigs.Get(f.ctx, "codex_config_default")
	if err != nil {
		t.Fatal(err)
	}
	before := projectInspectionCounts(t, f)
	input := contextprobe.PrepareInspectionInput{
		AISessionID:         f.session,
		RequestID:           "canonical-inspection-api",
		CodexInstruction:    "Inspect the exact app and sdk repositories and return structured evidence.",
		Backend:             config.Selection("test").Input(),
		Model:               "gpt-5.6-terra",
		ReasoningEffort:     "high",
		PrimaryRepositoryID: f.repos["app"],
		Targets: []contextprobe.TargetSelection{
			{RepositoryID: f.repos["sdk"], Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}},
			{RepositoryID: f.repos["app"], Scope: contextprobe.Scope{Kind: contextprobe.ScopePaths, Paths: []string{"same.txt"}}},
		},
	}
	prepared, err := f.facade.PrepareProjectInspection(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := f.facade.PrepareProjectInspection(f.ctx, input)
	if err != nil || !reflect.DeepEqual(prepared, replayed) {
		t.Fatalf("prepare replay changed: %+v %+v %v", prepared, replayed, err)
	}
	if after := projectInspectionCounts(t, f); !reflect.DeepEqual(before, after) {
		t.Fatalf("prepare produced effects: before=%v after=%v", before, after)
	}
	seenTargets := map[string]bool{}
	for _, target := range prepared.Targets {
		seenTargets[target.RepositoryID] = true
	}
	if len(prepared.Targets) != 2 || !seenTargets[f.repos["app"]] || !seenTargets[f.repos["sdk"]] {
		t.Fatalf("target set=%+v", prepared.Targets)
	}
	for _, target := range prepared.Targets {
		if target.RepositoryID == f.repos["backend"] {
			t.Fatal("unselected backend entered target set")
		}
	}
	confirmed, err := f.facade.ConfirmProjectInspection(f.ctx, contextprobe.ConfirmInspectionInput{Plan: prepared, PayloadSHA256: prepared.PayloadSHA256})
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.facade.ConfirmProjectInspection(f.ctx, contextprobe.ConfirmInspectionInput{Plan: prepared, PayloadSHA256: prepared.PayloadSHA256})
	if err != nil || again.ID != confirmed.ID || again.TaskID != confirmed.TaskID {
		t.Fatalf("confirm replay created a different owner: %+v %+v %v", confirmed, again, err)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, confirmed.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Scope != task.ScopeProject || owner.FeatureAreaID != "" || owner.Type != task.TypeInspection || owner.CurrentDefinitionRevisionID != "" || owner.CurrentExecutionID != "" {
		t.Fatalf("invalid Project Inspection owner: %+v", owner)
	}
	if confirmed.RunID != "" || confirmed.WorkspaceID != "" || confirmed.ThreadID != "" || confirmed.TurnID != "" {
		t.Fatalf("confirm started runtime: %+v", confirmed)
	}
	mutated := prepared
	mutated.Model = "gpt-6-astra"
	if _, err = f.facade.ConfirmProjectInspection(f.ctx, contextprobe.ConfirmInspectionInput{Plan: mutated, PayloadSHA256: prepared.PayloadSHA256}); err == nil {
		if err == nil {
			t.Fatal("changed payload replay succeeded")
		}
	}
	if api, ok := err.(*controlapi.Error); !ok || api.Code != controlapi.CodeConflict {
		t.Fatalf("changed payload error=%v", err)
	}
	afterConfirm := projectInspectionCounts(t, f)
	if afterConfirm["context_probes"] != before["context_probes"]+1 || afterConfirm["tasks"] != before["tasks"]+1 || afterConfirm["workspaces"] != before["workspaces"] || afterConfirm["runs"] != before["runs"] || afterConfirm["agent_conversations"] != before["agent_conversations"] {
		t.Fatalf("unexpected confirm effects: before=%v after=%v", before, afterConfirm)
	}
}
