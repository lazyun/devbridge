package bridgehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlplane"
)

func TestProductionProjectRegistrationPersistsCanonicalGitRootOnly(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	repositoryPath := committedRepository(t, filepath.Join(root, "source"))
	selectedPath := filepath.Join(repositoryPath, "nested", "child")
	if err := os.MkdirAll(selectedPath, 0o700); err != nil {
		t.Fatal(err)
	}

	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"modules", "feature_areas", "project_contexts", "codex_configs", "ai_sessions", "tasks", "runs"}
	baseline := tableCounts(t, plane, tables)

	var registered projectRegistrationOutput
	postJSON(t, trustedRegistrationHandler(server.Handler()), "/v1/projects", projectRegistrationInput{Name: "Dev Bridge", RepositoryPath: selectedPath}, http.StatusCreated, &registered)
	if registered.ProjectID == "" || registered.RepositoryID == "" || registered.Name != "Dev Bridge" || registered.RepositoryPath != repositoryPath {
		t.Fatalf("registration = %+v", registered)
	}

	repositories, err := plane.Repositories.List(ctx)
	if err != nil || len(repositories) != 1 || repositories[0].ProjectID != registered.ProjectID || repositories[0].Path != repositoryPath {
		t.Fatalf("repositories = %+v, err = %v", repositories, err)
	}
	projects, err := plane.AIContext.ListProjects(ctx, aicontext.ListOptions{Limit: 10})
	if err != nil || len(projects) != 1 || projects[0].ID != registered.ProjectID || projects[0].RepositoryCount != 1 {
		t.Fatalf("project_list source = %+v, err = %v", projects, err)
	}
	for _, table := range tables {
		var count int
		if err := plane.DB.SQL().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != baseline[table] {
			t.Fatalf("registration changed %s rows from %d to %d", table, baseline[table], count)
		}
	}
	scoped := aisession.WithCallerScope(ctx, "registration-legacy-test")
	ref := aiprojectselection.ClientBindingReference{CallerScope: "registration-legacy-test"}
	initialized, err := plane.AIContext.InitializeSession(scoped, ref, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := plane.AIContext.BindProject(scoped, ref, initialized.AISessionID, registered.ProjectID, "bind")
	if err != nil || opened.BoundProjectID != registered.ProjectID {
		t.Fatalf("project binding source=%+v err=%v", opened, err)
	}

	assertRegistrationError(t, trustedRegistrationHandler(server.Handler()), projectRegistrationInput{Name: "Duplicate", RepositoryPath: repositoryPath}, http.StatusConflict, "PROJECT_ALREADY_REGISTERED", registered.ProjectID)
	assertRegistrationError(t, trustedRegistrationHandler(server.Handler()), projectRegistrationInput{Name: "Missing", RepositoryPath: filepath.Join(root, "missing")}, http.StatusBadRequest, "INVALID_PATH", "")
	assertRegistrationError(t, trustedRegistrationHandler(server.Handler()), projectRegistrationInput{Name: "Plain", RepositoryPath: t.TempDir()}, http.StatusBadRequest, "NOT_GIT_REPOSITORY", "")
	values, err := plane.Projects.List(ctx)
	if err != nil || len(values) != 1 {
		t.Fatalf("failed/duplicate registration created projects: %+v err=%v", values, err)
	}

	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	plane, err = controlplane.Open(ctx, controlplane.Config{DataRoot: dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err = New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/projects?limit=10", nil)
	recorder := httptest.NewRecorder()
	trustedRegistrationHandler(server.Handler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("restart list status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var page consoleapi.Page[consoleapi.ProjectSummary]
	if err := json.NewDecoder(recorder.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != registered.ProjectID || page.Items[0].RepositoryCount != 1 {
		t.Fatalf("restart list = %+v", page)
	}
}

func tableCounts(t *testing.T, plane *controlplane.ControlPlane, tables []string) map[string]int {
	t.Helper()
	result := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		if err := plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		result[table] = count
	}
	return result
}

func TestProductionProjectRegistrationRejectsUnknownFieldsAndNeverRequiresAdmin(t *testing.T) {
	plane, err := controlplane.Open(context.Background(), controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/projects", bytes.NewBufferString(`{"name":"x","repository_path":"/tmp","fixture":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	trustedRegistrationHandler(server.Handler()).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func assertRegistrationError(t *testing.T, handler http.Handler, input projectRegistrationInput, wantStatus int, wantCode, wantProjectID string) {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/projects", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["code"] != wantCode {
		t.Fatalf("code = %v, want %s", response["code"], wantCode)
	}
	if wantProjectID != "" && response["project_id"] != wantProjectID {
		t.Fatalf("project_id = %v, want %s", response["project_id"], wantProjectID)
	}
}

func committedRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, path, "init", "-b", "main")
	git(t, path, "config", "user.name", "Project registration test")
	git(t, path, "config", "user.email", "registration@example.invalid")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "README.md")
	git(t, path, "commit", "-m", "initial")
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestAggregateProjectRegistrationHTTPEmptyAppendAndReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := trustedRegistrationHandler(server.Handler())
	input := projectRegistrationInput{RequestID: "logical-create", Name: "Logical", Repositories: []projectregistration.RepositoryInput{}}
	var empty projectRegistrationOutput
	postJSON(t, handler, "/v1/projects", input, http.StatusCreated, &empty)
	if empty.ProjectID == "" || len(empty.Repositories) != 0 {
		t.Fatalf("empty=%+v", empty)
	}
	repos := []projectregistration.RepositoryInput{}
	for _, name := range []string{"app", "sdk", "backend"} {
		repos = append(repos, projectregistration.RepositoryInput{Path: committedRepository(t, filepath.Join(root, name)), Alias: name})
	}
	appendInput := projectRegistrationInput{RequestID: "append-three", ProjectID: empty.ProjectID, Repositories: repos}
	var appended, replayed projectRegistrationOutput
	postJSON(t, handler, "/v1/projects", appendInput, http.StatusCreated, &appended)
	postJSON(t, handler, "/v1/projects", appendInput, http.StatusCreated, &replayed)
	if len(appended.Repositories) != 3 || !reflect.DeepEqual(appended, replayed) {
		t.Fatalf("append=%+v replay=%+v", appended, replayed)
	}
	bad := projectRegistrationInput{RequestID: "invalid-batch", Name: "No partial project", Repositories: []projectregistration.RepositoryInput{{Path: committedRepository(t, filepath.Join(root, "fresh")), Alias: "fresh"}, {Path: filepath.Join(root, "absent"), Alias: "bad"}}}
	assertRegistrationError(t, handler, bad, http.StatusBadRequest, "INVALID_PATH", "")
	all, err := plane.Projects.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("partial projects=%+v err=%v", all, err)
	}
	appendInput.Repositories = appendInput.Repositories[:1]
	assertRegistrationError(t, handler, appendInput, http.StatusConflict, "CONFLICT", "")
}

// Model the connection facts installed by net/http on an isolated loopback
// listener; this does not create or weaken the production admission policy.
func trustedRegistrationHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "127.0.0.1:45678"
		r.Host = "127.0.0.1:8081"
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8081}))
		next.ServeHTTP(w, r)
	})
}
