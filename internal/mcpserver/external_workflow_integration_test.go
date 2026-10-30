package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/mcpserver"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type continuityReviewService struct {
	*mcpserver.StaticService
	plane              *controlplane.ControlPlane
	starts             []scopereview.StartInput
	featureAreaCreates int
}

// initializeBoundProjectSession models the public session-first admission flow.
// Tests must not use the retired no-session project_open shortcut: a Session is
// created explicitly and the chosen Project is then bound with its own stable
// request receipt.
func initializeBoundProjectSession(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, projectID, requestID string) (string, map[string]any) {
	t.Helper()
	initialized := callToolMap(t, ctx, session, "session_initialize", map[string]any{"requestId": requestID + "-session"})
	initialization := initialized
	aiSessionID, _ := initialization["aiSessionId"].(string)
	if aiSessionID == "" || initialization["state"] != "PROJECT_SELECTION_REQUIRED" || initialization["boundProjectId"] != nil {
		t.Fatalf("session_initialize receipt=%+v", initialized)
	}
	opened := callToolMap(t, ctx, session, "project_open", map[string]any{"aiSessionId": aiSessionID, "projectId": projectID, "requestId": requestID + "-binding"})
	binding := opened
	if binding["aiSessionId"] != aiSessionID || binding["boundProjectId"] != projectID || binding["state"] != "PROJECT_BOUND" {
		t.Fatalf("project_open receipt=%+v", opened)
	}
	return aiSessionID, binding
}

func (s *continuityReviewService) CreateFeatureArea(ctx context.Context, input featurearea.CreateInput) (featurearea.FeatureArea, error) {
	s.featureAreaCreates++
	return s.StaticService.CreateFeatureArea(ctx, input)
}

func (s *continuityReviewService) StartScopeReview(ctx context.Context, input scopereview.StartInput) (run.Run, error) {
	s.starts = append(s.starts, input)
	value, err := s.plane.Reviews.Get(ctx, input.ReviewID)
	if err != nil {
		return run.Run{}, err
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	plan := run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope}
	if value.Focus.Kind == productreview.FocusFocused {
		plan.Mode = run.ReviewModeFocused
		plan.InitialScope = run.ReviewScope{Kind: run.ReviewScopePaths, Paths: value.Focus.Paths}
		plan.SelectedModules = value.Focus.Modules
	}
	return s.plane.Runs.CreateFocusedScopeReviewForReview(ctx, input.ReviewID, value.Instruction, scope, plan)
}

func (s *continuityReviewService) StartAdmittedReview(ctx context.Context, input scopereview.AdmittedStartInput) (productreview.StartAdmission, error) {
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	plan := run.ReviewPlan{Mode: run.ReviewMode(input.Review.Focus.Kind), InitialScope: scope, SelectedModules: input.Review.Focus.Modules}
	if input.Review.Focus.Kind == productreview.FocusFocused {
		plan.InitialScope = run.ReviewScope{Kind: run.ReviewScopePaths, Paths: input.Review.Focus.Paths}
	}
	value, err := s.plane.Reviews.AdmitStart(ctx, input.Review, input.RequestID, input.PayloadSHA256, input.Review.Instruction, scope, plan)
	if err == nil {
		s.starts = append(s.starts, scopereview.StartInput{AISessionID: input.AISessionID, ReviewID: value.Review.ID})
	}
	return value, err
}

func assertCurrentProductionToolTransition(t *testing.T, retired, replacements []string) {
	t.Helper()
	_, _, baseURL := startTestServer(t, readyService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "current-catalog-transition", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]bool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = true
	}
	for _, name := range retired {
		if byName[name] {
			t.Fatalf("retired model tool remains visible: %s", name)
		}
	}
	for _, name := range replacements {
		if !byName[name] {
			t.Fatalf("current replacement tool missing: %s", name)
		}
	}
}

func TestExternalWorkflowProjectsTopicAndBlocksUnapprovedDevelopment(t *testing.T) {
	assertCurrentProductionToolTransition(t, []string{"project_open", "feature_area_create", "task_create", "task_review_start"}, []string{"project_setup_prepare", "feature_area_prepare", "task_prepare", "review_prepare"})
}

func TestWorkflowGetReturnsOnlyEnabledAllowlistValues(t *testing.T) {
	assertCurrentProductionToolTransition(t, []string{"project_open"}, []string{"workspace_get"})
}

// Task-owned continuation selection is retired. The external schema must not
// expose any legacy AgentContext vocabulary.
func TestTaskCreateRejectsReviewAgentContextContinuationBeforePersistence(t *testing.T) {
	assertCurrentProductionToolTransition(t, []string{"task_create"}, []string{"task_prepare"})
}

func TestTopicPersistsAndReviewRemainsAllowedDuringUsableContextRefresh(t *testing.T) {
	assertCurrentProductionToolTransition(t, []string{"feature_area_create"}, []string{"feature_area_prepare"})
}

func TestTopicReviewStartResolvesFocusedModulePlanAndPrunesUnselectedModule(t *testing.T) {
	assertCurrentProductionToolTransition(t, []string{"task_review_start"}, []string{"review_prepare"})
}

func TestProjectOwnedTopicContinuityAcrossAISessions(t *testing.T) {
	assertCurrentProductionToolTransition(t, []string{"project_open"}, []string{"project_setup_prepare"})
}

func TestProjectOpenProjectsMultipleUsableFeatureAreasWithoutChoosingNewest(t *testing.T) {
	assertCurrentProductionToolTransition(t, []string{"project_open"}, []string{"project_list"})
}

func callToolErrorText(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) string {
	t.Helper()
	name, arguments = surfaceMutationTestArgs(name, arguments)
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("%s unexpectedly succeeded: %+v", name, result.StructuredContent)
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func createExternalWorkflowRepository(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("onboarding\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for relative, contents := range map[string]string{"primary/main.go": "package primary\n", "support/helper.go": "package support\n", "unrelated/other.go": "package unrelated\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(path, relative)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, relative), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Dev Bridge Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "initial"}} {
		command := exec.Command("git", args...)
		command.Dir = path
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return path
}

func selectCommitTestBaseline(t *testing.T, plane *controlplane.ControlPlane, projectID, repoID string) {
	t.Helper()
	ctx := context.Background()
	c, err := plane.DevelopmentCycles.Context(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range c.Repositories {
		if r.RepositoryID == repoID {
			if r.DevelopmentCycle != nil {
				return
			}
			if len(r.Branches) == 0 {
				t.Fatal("missing branch")
			}
			_, err = plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: projectID, RepositoryID: repoID, DevelopmentRef: r.Branches[0].FullRef, RequestID: "fixture-baseline-" + repoID})
			if err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing fixture repository")
}

func externalIntent(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, sid, area, repo string) string {
	t.Helper()
	created := callToolMap(t, ctx, session, "task_create", map[string]any{"aiSessionId": sid, "featureAreaId": area, "repositoryId": repo, "requestId": "intent-" + area, "title": "Engineering intent", "problemStatement": "Improve this capability."})
	return created["id"].(string)
}

// Access the concrete consolidated workflow capability array without inventing
// the removed workflow_get boolean map.
func workspaceCapability(t *testing.T, envelope map[string]any, action string) bool {
	t.Helper()
	workflow, ok := envelope["workflow"].(map[string]any)
	if !ok {
		t.Fatalf("missing workflow envelope: %+v", envelope)
	}
	for _, raw := range workflow["capabilities"].([]any) {
		cap := raw.(map[string]any)
		if cap["action"] == action && cap["available"] == true {
			return true
		}
	}
	return false
}
