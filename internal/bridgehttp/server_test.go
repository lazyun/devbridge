package bridgehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
)

func TestFixtureProvisioningIsExplicitAndProductionWorkflowStillRequiresConfirmation(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "bridgehttp-admin-fixture")
	root := t.TempDir()
	repositoryPath := filepath.Join(root, "source")
	if err := os.MkdirAll(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, repositoryPath, "init", "-b", "main")
	git(t, repositoryPath, "config", "user.name", "Bridge HTTP test")
	git(t, repositoryPath, "config", "user.email", "bridge-http@example.invalid")
	if err := os.WriteFile(filepath.Join(repositoryPath, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repositoryPath, "add", "tracked.txt")
	git(t, repositoryPath, "commit", "-m", "base")
	base := git(t, repositoryPath, "rev-parse", "HEAD")

	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, true)
	if err != nil {
		t.Fatal(err)
	}
	var provision ProvisionResult
	postJSON(t, server.Handler(), "/admin/test/v1/fixture", ProvisionRequest{SourceRepository: repositoryPath}, http.StatusCreated, &provision)
	if provision.ProjectID == "" || provision.RepositoryID == "" || provision.ModuleID == "" || provision.CodexConfigID == "" || provision.AISessionID == "" {
		t.Fatalf("incomplete provision result: %+v", provision)
	}
	for _, command := range []string{
		`rg -n -g '*.go' -g '!*_test.go' 'CreateReview|FinalCodexPrompt|PromptSHA256|ReviewEvidence|reviewId' .`,
		`rg -n needle -g '!test/**' -g '!tests/**' -g '*.spec.*' .`,
		"go test ./...",
		"sed -n 1,20p tracked.txt",
	} {
		postJSON(t, server.Handler(), "/admin/test/v1/review-command-observation", ReviewCommandObservationRequest{Command: command}, http.StatusOK, nil)
	}
	postJSON(t, server.Handler(), "/v1/task-proposals", map[string]any{}, http.StatusForbidden, nil)

	selectConsoleDevelopmentBaseline(t, plane, provision.ProjectID, provision.RepositoryID)
	area, err := plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: provision.AISessionID, Title: "HTTP intent", ProblemStatement: "change tracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.FeatureAreas.Activate(ctx, provision.AISessionID, area.ID, "http-activate"); err != nil {
		t.Fatal(err)
	}
	createInput := controlapi.CreatePlanningTaskInput{
		RequestID: "server-test-intent", AISessionID: provision.AISessionID, FeatureAreaID: area.ID, ModuleID: provision.ModuleID, RepositoryID: provision.RepositoryID, CodexConfigID: provision.CodexConfigID, Title: "change tracked.txt", ProblemStatement: "change tracked.txt", BaseRevision: base,
	}
	taskValue, err := server.api.CreatePlanningTaskForSession(ctx, createInput)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := server.api.CreatePlanningTaskForSession(ctx, createInput)
	if err != nil || replayed.ID != taskValue.ID {
		t.Fatalf("Task replay=%+v err=%v, want %s", replayed, err, taskValue.ID)
	}
	conflict := createInput
	conflict.ProblemStatement = "different semantic Task"
	if _, err := server.api.CreatePlanningTaskForSession(ctx, conflict); err == nil {
		t.Fatal("changed payload reused Task creation receipt")
	}
	if _, err := server.api.CreatePlanningTaskForSession(ctx, controlapi.CreatePlanningTaskInput{AISessionID: provision.AISessionID}); err == nil {
		t.Fatal("requestId-less production Task creation succeeded")
	}
	if values, listErr := plane.Tasks.List(ctx); listErr != nil || len(values) != 1 {
		t.Fatalf("Task count after response-loss retry = %d, err=%v", len(values), listErr)
	}

	if runs, err := plane.Runs.List(ctx); err != nil || len(runs) != 0 {
		t.Fatalf("task creation created Runs: %+v err=%v", runs, err)
	}

	plan := struct{ ID string }{ID: "obsolete"}
	postJSON(t, server.Handler(), "/v1/task-plans", taskPlanCreateInput{
		AISessionID: provision.AISessionID, TaskID: taskValue.ID,
		WorkSpec:         task.WorkSpec{Goal: "change tracked.txt", Constraints: []string{"bounded"}, CodexInstruction: "Change tracked.txt exactly as confirmed.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}},
		VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "HTTP test explicitly waives execution verification."}},
	}, http.StatusForbidden, nil)
	for _, path := range []string{
		"/v1/task-plans/" + plan.ID + "/confirm",
		"/v1/task-plans/" + plan.ID + "/reject",
		"/v1/task-plans/" + plan.ID + "/apply",
	} {
		postJSON(t, server.Handler(), path, map[string]string{"ai_session_id": provision.AISessionID}, http.StatusForbidden, nil)
	}
}

func TestFixtureProvisioningIsDisabledByDefault(t *testing.T) {
	plane, err := controlplane.Open(context.Background(), controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	postJSON(t, server.Handler(), "/admin/test/v1/fixture", ProvisionRequest{SourceRepository: t.TempDir()}, http.StatusForbidden, nil)
	postJSON(t, server.Handler(), "/admin/test/v1/review-command-observation", ReviewCommandObservationRequest{Command: "go test ./..."}, http.StatusForbidden, nil)
}

func TestValidateLoopbackListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:8080"} {
		if err := ValidateLoopbackListen(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{":8080", "0.0.0.0:8080", "192.0.2.1:8080", "localhost:8080"} {
		if err := ValidateLoopbackListen(address); err == nil {
			t.Fatalf("accepted non-explicit loopback address %q", address)
		}
	}
}

func postJSON(t *testing.T, handler http.Handler, target string, input any, wantStatus int, output any) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(data))
	request.RemoteAddr = "127.0.0.1:45678"
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		var failure map[string]any
		_ = json.NewDecoder(response.Body).Decode(&failure)
		t.Fatalf("POST %s status=%d want=%d response=%v", target, response.StatusCode, wantStatus, failure)
	}
	if output != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(bytes.TrimSpace(output))
}
