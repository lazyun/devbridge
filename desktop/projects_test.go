package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/desktop/internal/settings"
)

type projectAPIFake struct {
	projects      []coreProject
	registration  coreRegistration
	listErr       error
	registerErr   error
	listCalls     int
	registerCalls int
	input         RegisterProjectInput
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func (f *projectAPIFake) ListProjects(context.Context, string) ([]coreProject, error) {
	f.listCalls++
	return append([]coreProject(nil), f.projects...), f.listErr
}

func (f *projectAPIFake) RegisterProject(_ context.Context, _ string, input RegisterProjectInput) (coreRegistration, error) {
	f.registerCalls++
	f.input = input
	return f.registration, f.registerErr
}

func projectTestApp(api projectCoreAPI, ready bool) *App {
	app := newApp(settings.Paths{}, nil)
	app.projectAPI = api
	app.coreReadyCheck = func() bool { return ready }
	app.settings.CoreEndpoint = "http://127.0.0.1:8081"
	return app
}

func TestDesktopProjectListUsesCoreSourceOfTruth(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	api := &projectAPIFake{projects: []coreProject{{ID: "project-one", Name: "One", RepositoryCount: 1, CreatedAt: now, UpdatedAt: now}}}
	app := projectTestApp(api, true)
	result := app.ListProjects()
	if !result.CoreReady || result.Error != nil || len(result.Items) != 1 || result.Items[0].ID != "project-one" || result.Items[0].RepositoryCount != 1 {
		t.Fatalf("list = %+v", result)
	}
	if api.listCalls != 1 {
		t.Fatalf("list calls = %d", api.listCalls)
	}
}

func TestHTTPProjectCoreAPIUsesProductionRoutesAndWireContract(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if strings.Contains(request.URL.Path, "/admin/") {
			t.Fatalf("production client used admin path %q", request.URL.Path)
		}
		response := &http.Response{Header: make(http.Header)}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/projects":
			response.StatusCode = http.StatusOK
			response.Body = io.NopCloser(strings.NewReader(`{"items":[{"id":"project-one","name":"One","repository_count":1}]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/v1/projects":
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != `{"name":"One","project_id":"","repositories":null,"repository_path":"/source/one","request_id":""}` {
				t.Fatalf("body = %s", body)
			}
			response.StatusCode = http.StatusCreated
			response.Body = io.NopCloser(strings.NewReader(`{"project_id":"project-one","repository_id":"repo-one","name":"One","repository_path":"/source/one"}`))
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.String())
		}
		return response, nil
	})}
	api := newHTTPProjectCoreAPI(client)
	projects, err := api.ListProjects(context.Background(), "http://127.0.0.1:8081")
	if err != nil || len(projects) != 1 || projects[0].RepositoryCount != 1 {
		t.Fatalf("projects = %+v, err = %v", projects, err)
	}
	registered, err := api.RegisterProject(context.Background(), "http://127.0.0.1:8081", RegisterProjectInput{Name: "One", RepositoryPath: "/source/one"})
	if err != nil || registered.ProjectID != "project-one" || registered.RepositoryPath != "/source/one" {
		t.Fatalf("registration = %+v, err = %v", registered, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestHTTPProjectCoreAPIPreservesDuplicateIdentity(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"PROJECT_ALREADY_REGISTERED","message":"Repository is already registered as One.","project_id":"project-one","repository_id":"repo-one","name":"One","repository_path":"/source/one"}`))}, nil
	})}
	api := newHTTPProjectCoreAPI(client)
	value, err := api.RegisterProject(context.Background(), "http://127.0.0.1:8081", RegisterProjectInput{Name: "Again", RepositoryPath: "/source/one"})
	var apiErr *coreAPIError
	if !errors.As(err, &apiErr) || apiErr.Code != desktopCodeProjectAlreadyRegistered || value.ProjectID != "project-one" {
		t.Fatalf("value = %+v, err = %#v", value, err)
	}
}

func TestDesktopProjectOperationsReturnCoreUnavailableWithoutCallingAPI(t *testing.T) {
	api := &projectAPIFake{}
	app := projectTestApp(api, false)
	list := app.ListProjects()
	if list.Error == nil || list.Error.Code != desktopCodeCoreUnavailable || list.CoreReady {
		t.Fatalf("list = %+v", list)
	}
	registered := app.RegisterProject(RegisterProjectInput{Name: "One", RepositoryPath: "/repo"})
	if registered.Error == nil || registered.Error.Code != desktopCodeCoreUnavailable {
		t.Fatalf("registration = %+v", registered)
	}
	if api.listCalls != 0 || api.registerCalls != 0 {
		t.Fatalf("unready Core was called: %d/%d", api.listCalls, api.registerCalls)
	}
}

func TestDesktopProjectRegistrationMapsSuccessAndStructuredDuplicate(t *testing.T) {
	api := &projectAPIFake{registration: coreRegistration{ProjectID: "project-new", RepositoryID: "repo-new", Name: "New", RepositoryPath: "/source/new"}}
	app := projectTestApp(api, true)
	result := app.RegisterProject(RegisterProjectInput{Name: "New", RepositoryPath: "/selected/new"})
	if !result.Created || result.Error != nil || result.ProjectID != "project-new" || api.input.Name != "New" || api.input.RepositoryPath != "/selected/new" {
		t.Fatalf("registration = %+v, input = %+v", result, api.input)
	}

	api.registerErr = &coreAPIError{Code: desktopCodeProjectAlreadyRegistered, Message: "Repository is already registered as New.", ProjectID: "project-new", RepositoryID: "repo-new", Name: "New", RepositoryPath: "/source/new"}
	result = app.RegisterProject(RegisterProjectInput{Name: "Again", RepositoryPath: "/source/new"})
	if result.Created || result.Error == nil || result.Error.Code != desktopCodeProjectAlreadyRegistered || result.ProjectID != "project-new" {
		t.Fatalf("duplicate = %+v", result)
	}
}

func TestDesktopProjectRegistrationMapsValidationAndTransportErrors(t *testing.T) {
	api := &projectAPIFake{registerErr: &coreAPIError{Code: desktopCodeNotGitRepository, Message: "Selected folder is not a usable Git repository."}}
	app := projectTestApp(api, true)
	result := app.RegisterProject(RegisterProjectInput{Name: "Plain", RepositoryPath: "/plain"})
	if result.Error == nil || result.Error.Code != desktopCodeNotGitRepository {
		t.Fatalf("not Git = %+v", result)
	}
	api.registerErr = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	result = app.RegisterProject(RegisterProjectInput{Name: "Offline", RepositoryPath: "/repo"})
	if result.Error == nil || result.Error.Code != desktopCodeCoreUnavailable {
		t.Fatalf("transport = %+v", result)
	}
}

func TestChooseProjectDirectoryUsesDedicatedNativePickerContract(t *testing.T) {
	app := projectTestApp(&projectAPIFake{}, true)
	selected := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	app.pickProjectDirectory = func(context.Context, string) (string, error) { return selected, nil }
	value, err := app.ChooseProjectDirectory("")
	if err != nil || value.RepositoryPath != selected || value.DefaultName != "repository" {
		t.Fatalf("selection = %+v, err = %v", value, err)
	}
	app.pickProjectDirectory = func(context.Context, string) (string, error) { return "", nil }
	value, err = app.ChooseProjectDirectory(selected)
	if err != nil || value.RepositoryPath != "" {
		t.Fatalf("cancel = %+v, err = %v", value, err)
	}
}

func TestDesktopProductionProjectFlowDoesNotReferenceAdminFixture(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	root := filepath.Dir(current)
	for _, name := range []string{"projects.go", filepath.Join("frontend", "src", "main.js"), filepath.Join("frontend", "src", "project-registration.js")} {
		contents, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contents), "/admin/test/v1/fixture") {
			t.Fatalf("production Desktop file %s references the admin fixture", name)
		}
	}
}
