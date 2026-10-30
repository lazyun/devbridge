package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newWorkspaceReproFixture(t *testing.T, scope string) (context.Context, BridgeService, string, *controlapi.Facade, *controlplane.ControlPlane) {
	t.Helper()
	ctx := context.Background()
	ctx = aisession.WithCallerScope(ctx, scope)
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	p, err := plane.Projects.Create(ctx, "Workspace repro")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	repoPath := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(repoPath, 0700); err != nil {
		t.Fatal(err)
	}
	repoValue := repository.Repository{ID: "repro-" + scope + "-repository", ProjectID: p.ID, Name: "Source", Path: repoPath, CreatedAt: now, UpdatedAt: now}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repoValue); err != nil {
		t.Fatal(err)
	}
	gitInitRepo(t, repoPath)
	if _, err := plane.Modules.EnsureUnclassified(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: scope}
	session, err := plane.AIContext.InitializeSession(ctx, ref, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.AIContext.BindProject(ctx, ref, session.AISessionID, p.ID, "bind"); err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	static, err := NewService(BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true}, plane.AIContext, facade)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, static, session.AISessionID, facade, plane
}

func TestReproWorkspaceReadAfterFeatureAreaCreate(t *testing.T) {
	ctx, static, aiSessionID, facade, _ := newWorkspaceReproFixture(t, "repro-workspace")
	created, err := facade.CreateFeatureAreaWithInitialReview(ctx, controlapi.FeatureAreaCreateInput{AISessionID: aiSessionID, Title: "First task only", ProblemStatement: "No Task yet."})
	if err != nil {
		t.Fatal(err)
	}
	out, err := WorkspaceGetCore(ctx, static, WorkspaceGetInput{AISessionID: aiSessionID, FeatureAreaID: created.FeatureArea.ID, Include: []string{"TASK"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range out.Result.Partitions {
		t.Logf("partition %s generation=%s seq=%d stale=%v readError=%q", part.Name, part.Generation, part.SourceSequence, part.Stale, part.ReadError)
		if part.Stale {
			t.Errorf("partition %s is stale: %s", part.Name, part.ReadError)
		}
	}
	if out.Workflow.RefreshRequired {
		t.Errorf("workflow refresh required: state=%s nextAction=%s", out.Workflow.State, out.Workflow.NextAction.Code)
	}
	found := false
	for i := range out.Workflow.Capabilities {
		if out.Workflow.Capabilities[i].Action == "CREATE_TASK" {
			found = true
			if !out.Workflow.Capabilities[i].Available {
				t.Errorf("CREATE_TASK unavailable: reason=%s", out.Workflow.Capabilities[i].Reason)
			}
		}
	}
	if !found {
		t.Error("CREATE_TASK capability missing from workflow")
	}
}

func TestReproFeatureAreaCreateEnvelopeReadbackFresh(t *testing.T) {
	ctx, static, aiSessionID, facade, _ := newWorkspaceReproFixture(t, "repro-envelope")
	created, err := facade.CreateFeatureAreaWithInitialReview(ctx, controlapi.FeatureAreaCreateInput{AISessionID: aiSessionID, Title: "Envelope only", ProblemStatement: "No composition."})
	if err != nil {
		t.Fatal(err)
	}
	a := created.FeatureArea
	receipt := map[string]any{
		"id": a.ID, "status": string(a.Status), "title": a.Title, "problemStatement": a.ProblemStatement,
		"projectId": a.ProjectID, "createdAt": a.CreatedAt.UTC().Format(timeFormat), "updatedAt": a.UpdatedAt.UTC().Format(timeFormat),
	}
	result := &sdkmcp.CallToolResult{StructuredContent: receipt, IsError: false}
	enveloped, err := surfaceBusinessResult(ctx, static, &sdkmcp.CallToolParamsRaw{Name: "feature_area_create", Arguments: json.RawMessage(`{"aiSessionId":"` + aiSessionID + `"}`)}, result, nil)
	if err != nil {
		t.Fatal(err)
	}
	callResult, ok := enveloped.(*sdkmcp.CallToolResult)
	if !ok {
		t.Fatalf("envelope result type=%T", enveloped)
	}
	value, ok := callResult.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("envelope content type=%T", callResult.StructuredContent)
	}
	encoded, _ := json.Marshal(value)
	var decoded struct {
		Workflow struct {
			CurrentState string `json:"currentState"`
			NextAction   struct {
				Code string `json:"code"`
			} `json:"nextAction"`
			ReadError string `json:"readError"`
			Stale     bool   `json:"stale"`
			Refresh   bool   `json:"refreshRequired"`
		} `json:"workflow"`
		RefreshRequired bool `json:"refreshRequired"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RefreshRequired || decoded.Workflow.Refresh || decoded.Workflow.Stale {
		t.Errorf("envelope refresh required: refreshRequired=%v workflow=%s", decoded.RefreshRequired, encoded)
	}
	if decoded.Workflow.ReadError != "" {
		t.Errorf("envelope workflow readError=%q", decoded.Workflow.ReadError)
	}
	if decoded.Workflow.CurrentState != "FEATURE_AREA_IDLE" {
		t.Errorf("envelope state=%q", decoded.Workflow.CurrentState)
	}
	if decoded.Workflow.NextAction.Code != "CREATE_TASK" {
		t.Errorf("envelope nextAction=%+v", decoded.Workflow.NextAction)
	}
}

func gitInitRepo(t *testing.T, path string) {
	t.Helper()
	execGit(t, path, "init")
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("presentation fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	execGit(t, path, "add", "tracked.txt")
	execGit(t, path, "-c", "user.name=Dev Bridge Test", "-c", "user.email=test@example.invalid", "commit", "-m", "fixture")
}

func execGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}