package bridgehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/developmentcycle"
)

func TestConsoleDevelopmentBranchSharedSelectionAndConflicts(t *testing.T) {
	fixture := newConsoleFixtureWithBaseline(t, false)
	defer fixture.plane.Close()
	git(t, fixture.repositoryPath, "branch", "dev/console")
	path := "/v1/development-context?project_id=" + fixture.projectID
	status, _, body := getRaw(t, fixture.handler, path)
	if status != http.StatusOK || !strings.Contains(body, `"developmentBranchSelectionRequired":true`) || !strings.Contains(body, `"fullRef":"refs/heads/dev/console"`) {
		t.Fatalf("initial context: %d %s", status, body)
	}
	input := map[string]string{"project_id": fixture.projectID, "repository_id": fixture.repositoryID, "development_ref": "refs/heads/dev/console", "request_id": "console-selection-1"}
	var selected, retry developmentcycle.Cycle
	postJSON(t, fixture.handler, "/v1/development-branch-selection", input, http.StatusOK, &selected)
	postJSON(t, fixture.handler, "/v1/development-branch-selection", input, http.StatusOK, &retry)
	if selected.ID == "" || selected.ID != retry.ID {
		t.Fatalf("retry changed result: %#v %#v", selected, retry)
	}
	contextValue, err := fixture.plane.DevelopmentCycles.Context(context.Background(), fixture.projectID)
	if err != nil || contextValue.Repositories[0].DevelopmentRef != "refs/heads/dev/console" {
		t.Fatalf("shared authority: %#v %v", contextValue, err)
	}
	if current := git(t, fixture.repositoryPath, "symbolic-ref", "HEAD"); current != "refs/heads/main" {
		t.Fatalf("selection changed checkout: %s", current)
	}
	input["development_ref"] = "refs/heads/main"
	var problem map[string]string
	postDevelopmentError(t, fixture.handler, input, http.StatusConflict, &problem)
	if problem["code"] != "REQUEST_CONFLICT" {
		t.Fatalf("conflict lost: %#v", problem)
	}
	input["request_id"] = "missing-branch"
	input["development_ref"] = "refs/heads/missing"
	postDevelopmentError(t, fixture.handler, input, http.StatusNotFound, &problem)
	if problem["code"] != "DEVELOPMENT_BRANCH_NOT_FOUND" {
		t.Fatalf("missing branch lost: %#v", problem)
	}
	other, err := fixture.plane.Projects.Create(context.Background(), "Other Project")
	if err != nil {
		t.Fatal(err)
	}
	input["request_id"] = "wrong-project"
	input["project_id"] = other.ID
	input["development_ref"] = "refs/heads/main"
	postDevelopmentError(t, fixture.handler, input, http.StatusConflict, &problem)
	if problem["code"] != "REPOSITORY_PROJECT_MISMATCH" {
		t.Fatalf("scope conflict lost: %#v", problem)
	}
	status, _, body = getRaw(t, fixture.handler, path)
	if status != http.StatusOK || !strings.Contains(body, `"developmentRef":"refs/heads/dev/console"`) || !strings.Contains(body, `"repositoryCheckoutMatchesDevelopmentRef":false`) {
		t.Fatalf("selected context: %d %s", status, body)
	}
}

func postDevelopmentError(t *testing.T, handler http.Handler, input any, status int, output any) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/development-branch-selection", bytes.NewReader(data))
	request.RemoteAddr = "127.0.0.1:45678"
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != status {
		t.Fatalf("status %d want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), output); err != nil {
		t.Fatal(err)
	}
}
