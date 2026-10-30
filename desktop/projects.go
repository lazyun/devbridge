package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	desktopCodeInvalidPath              = "INVALID_PATH"
	desktopCodeNotGitRepository         = "NOT_GIT_REPOSITORY"
	desktopCodeProjectAlreadyRegistered = "PROJECT_ALREADY_REGISTERED"
	desktopCodeUnsupportedOperation     = "UNSUPPORTED_OPERATION"
	desktopCodeNotFound                 = "NOT_FOUND"
	desktopCodeInvalidRequest           = "INVALID_REQUEST"
	desktopCodeConflict                 = "CONFLICT"
	desktopCodePermissionDenied         = "PERMISSION_DENIED"
	desktopCodeCoreError                = "CORE_ERROR"
	desktopCodeCoreUnavailable          = "CORE_UNAVAILABLE"
	desktopCodeRegistrationFailed       = "REGISTRATION_FAILED"
)

type DesktopProject struct {
	ID                          string `json:"id"`
	Name                        string `json:"name"`
	Selected                    bool   `json:"selected"`
	ProjectContextStatus        string `json:"projectContextStatus,omitempty"`
	ProjectContextRefreshing    bool   `json:"projectContextRefreshing"`
	ProjectContextRefreshFailed bool   `json:"projectContextRefreshFailed"`
	RepositoryCount             int    `json:"repositoryCount"`
	CreatedAt                   string `json:"createdAt"`
	UpdatedAt                   string `json:"updatedAt"`
}

type DesktopProjectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type DesktopProjectList struct {
	Items     []DesktopProject     `json:"items"`
	CoreReady bool                 `json:"coreReady"`
	Error     *DesktopProjectError `json:"error,omitempty"`
}

type ProjectDirectorySelection struct {
	RepositoryPath string `json:"repositoryPath"`
	DefaultName    string `json:"defaultName"`
}

type ProjectRepositoryInput struct {
	Path  string `json:"path"`
	Alias string `json:"alias,omitempty"`
}
type RegisteredRepository struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}
type RegisterProjectInput struct {
	RequestID      string                   `json:"requestId"`
	ProjectID      string                   `json:"projectId,omitempty"`
	Repositories   []ProjectRepositoryInput `json:"repositories"`
	Name           string                   `json:"name"`
	RepositoryPath string                   `json:"repositoryPath"`
}

type RegisterProjectResult struct {
	RequestID      string                 `json:"requestId,omitempty"`
	Repositories   []RegisteredRepository `json:"repositories"`
	ProjectID      string                 `json:"projectId,omitempty"`
	RepositoryID   string                 `json:"repositoryId,omitempty"`
	Name           string                 `json:"name,omitempty"`
	RepositoryPath string                 `json:"repositoryPath,omitempty"`
	Created        bool                   `json:"created"`
	Error          *DesktopProjectError   `json:"error,omitempty"`
}

type coreProject struct {
	ID                          string    `json:"id"`
	Name                        string    `json:"name"`
	Selected                    bool      `json:"selected"`
	ProjectContextStatus        string    `json:"project_context_status"`
	ProjectContextRefreshing    bool      `json:"project_context_refreshing"`
	ProjectContextRefreshFailed bool      `json:"project_context_refresh_failed"`
	RepositoryCount             int       `json:"repository_count"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
}

type coreProjectPage struct {
	Items []coreProject `json:"items"`
}

type coreRegistration struct {
	RequestID      string                 `json:"request_id"`
	Repositories   []RegisteredRepository `json:"repositories"`
	ProjectID      string                 `json:"project_id"`
	RepositoryID   string                 `json:"repository_id"`
	Name           string                 `json:"name"`
	RepositoryPath string                 `json:"repository_path"`
}

type coreAPIError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	ProjectID      string `json:"project_id"`
	RepositoryID   string `json:"repository_id"`
	Name           string `json:"name"`
	RepositoryPath string `json:"repository_path"`
}

func (e *coreAPIError) Error() string { return e.Code + ": " + e.Message }

// httpStatusError is a structured error for a reachable Core whose response
// carries no parseable product error payload. Core reachability is not API
// contract compatibility: HTTP 404/405 from a live Core must never surface as
// CORE_UNAVAILABLE.
type httpStatusError struct {
	status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("Core returned HTTP %d", e.status)
}

type projectCoreAPI interface {
	ListProjects(context.Context, string) ([]coreProject, error)
	RegisterProject(context.Context, string, RegisterProjectInput) (coreRegistration, error)
}

type httpProjectCoreAPI struct {
	client *http.Client
}

func newHTTPProjectCoreAPI(client *http.Client) projectCoreAPI {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return &httpProjectCoreAPI{client: client}
}

func (c *httpProjectCoreAPI) ListProjects(ctx context.Context, endpoint string) ([]coreProject, error) {
	target, err := coreURL(endpoint, "/v1/projects?limit=100")
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, decodeCoreError(response.Body, response.StatusCode)
	}
	var page coreProjectPage
	if err := decodeCoreJSON(response.Body, &page); err != nil {
		return nil, err
	}
	if page.Items == nil {
		page.Items = []coreProject{}
	}
	return page.Items, nil
}

func (c *httpProjectCoreAPI) RegisterProject(ctx context.Context, endpoint string, input RegisterProjectInput) (coreRegistration, error) {
	payload, err := json.Marshal(map[string]any{"request_id": input.RequestID, "project_id": input.ProjectID, "repositories": input.Repositories, "name": input.Name, "repository_path": input.RepositoryPath})
	if err != nil {
		return coreRegistration{}, err
	}
	target, err := coreURL(endpoint, "/v1/projects")
	if err != nil {
		return coreRegistration{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return coreRegistration{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return coreRegistration{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		apiErr := decodeCoreError(response.Body, response.StatusCode)
		var typed *coreAPIError
		if errors.As(apiErr, &typed) {
			return coreRegistration{ProjectID: typed.ProjectID, RepositoryID: typed.RepositoryID, Name: typed.Name, RepositoryPath: typed.RepositoryPath}, apiErr
		}
		return coreRegistration{}, apiErr
	}
	var value coreRegistration
	if err := decodeCoreJSON(response.Body, &value); err != nil {
		return coreRegistration{}, err
	}
	return value, nil
}

func coreURL(endpoint, path string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("invalid Core endpoint")
	}
	reference, _ := url.Parse(path)
	return base.ResolveReference(reference).String(), nil
}

func decodeCoreJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode Core response: %w", err)
	}
	return nil
}

func decodeCoreError(reader io.Reader, status int) error {
	var value coreAPIError
	if err := decodeCoreJSON(reader, &value); err != nil || value.Code == "" {
		return &httpStatusError{status: status}
	}
	return &value
}

// isCoreTransportError classifies transport-layer failures (connection
// refused, DNS failure, deadline exceeded) as Core unavailability.
func isCoreTransportError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// http.Client wraps every transport failure in *url.Error: connection
	// refused, DNS, client timeout, and canceled request contexts alike.
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// ChooseProjectDirectory uses the native folder picker. Core remains
// authoritative for access, Git, canonical-root, and duplicate validation.
func (a *App) ChooseProjectDirectory(current string) (ProjectDirectorySelection, error) {
	a.mu.RLock()
	ctx := a.ctx
	picker := a.pickProjectDirectory
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if picker == nil {
		picker = nativeProjectDirectoryPicker
	}
	selected, err := picker(ctx, current)
	if err != nil {
		return ProjectDirectorySelection{}, err
	}
	if selected == "" {
		return ProjectDirectorySelection{}, nil
	}
	absolute, err := filepath.Abs(selected)
	if err != nil {
		return ProjectDirectorySelection{}, fmt.Errorf("resolve selected repository directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	return ProjectDirectorySelection{RepositoryPath: absolute, DefaultName: filepath.Base(absolute)}, nil
}

func nativeProjectDirectoryPicker(ctx context.Context, current string) (string, error) {
	return wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{
		Title:            "Choose a local Git repository",
		DefaultDirectory: current,
	})
}

func (a *App) ListProjects() DesktopProjectList {
	if !a.coreReadyForProjects() {
		return DesktopProjectList{Items: []DesktopProject{}, CoreReady: false, Error: &DesktopProjectError{Code: desktopCodeCoreUnavailable, Message: "Dev Bridge Core is not ready."}}
	}
	ctx, endpoint, api := a.projectRequestContext()
	values, err := api.ListProjects(ctx, endpoint)
	if err != nil {
		return DesktopProjectList{Items: []DesktopProject{}, CoreReady: false, Error: mapDesktopProjectError(err, false)}
	}
	items := make([]DesktopProject, 0, len(values))
	for _, value := range values {
		items = append(items, DesktopProject{
			ID: value.ID, Name: value.Name, Selected: value.Selected,
			ProjectContextStatus: value.ProjectContextStatus, ProjectContextRefreshing: value.ProjectContextRefreshing,
			ProjectContextRefreshFailed: value.ProjectContextRefreshFailed, RepositoryCount: value.RepositoryCount,
			CreatedAt: value.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.Format(time.RFC3339Nano),
		})
	}
	return DesktopProjectList{Items: items, CoreReady: true}
}

func (a *App) RegisterProject(input RegisterProjectInput) RegisterProjectResult {
	if !a.coreReadyForProjects() {
		return RegisterProjectResult{Error: &DesktopProjectError{Code: desktopCodeCoreUnavailable, Message: "Dev Bridge Core is not ready."}}
	}
	ctx, endpoint, api := a.projectRequestContext()
	value, err := api.RegisterProject(ctx, endpoint, input)
	result := RegisterProjectResult{RequestID: value.RequestID, Repositories: value.Repositories, ProjectID: value.ProjectID, RepositoryID: value.RepositoryID, Name: value.Name, RepositoryPath: value.RepositoryPath}
	if err != nil {
		result.Error = mapDesktopProjectError(err, true)
		return result
	}
	result.Created = true
	return result
}

func (a *App) projectRequestContext() (context.Context, string, projectCoreAPI) {
	a.mu.RLock()
	ctx := a.ctx
	endpoint := a.settings.CoreEndpoint
	api := a.projectAPI
	a.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if api == nil {
		api = newHTTPProjectCoreAPI(nil)
	}
	return ctx, endpoint, api
}

func (a *App) coreReadyForProjects() bool {
	a.mu.RLock()
	check := a.coreReadyCheck
	supervisor := a.supervisor
	a.mu.RUnlock()
	if check != nil {
		return check()
	}
	if supervisor == nil {
		return false
	}
	core := supervisor.Snapshot().Core
	return (core.State == runtimecontrol.Running || core.State == runtimecontrol.External) &&
		(core.Ownership == runtimecontrol.Managed || core.Ownership == runtimecontrol.Attached)
}

func mapDesktopProjectError(err error, registration bool) *DesktopProjectError {
	var apiErr *coreAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case desktopCodeInvalidPath, desktopCodeNotGitRepository, desktopCodeProjectAlreadyRegistered, "INVALID_ARGUMENT":
			return &DesktopProjectError{Code: apiErr.Code, Message: apiErr.Message}
		case desktopCodeNotFound, desktopCodeInvalidRequest, desktopCodeConflict, desktopCodePermissionDenied, "HEAD_UNAVAILABLE", "REPOSITORY_ALREADY_OWNED", "REGISTRATION_REQUEST_CONFLICT":
			return &DesktopProjectError{Code: apiErr.Code, Message: apiErr.Message}
		}
		return &DesktopProjectError{Code: desktopCodeRegistrationFailed, Message: "Dev Bridge Core could not complete the project request."}
	}
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) {
		switch {
		case statusErr.status == http.StatusNotFound:
			return &DesktopProjectError{Code: desktopCodeNotFound, Message: fmt.Sprintf("Dev Bridge Core API route is missing (HTTP %d). Update Desktop to match the installed Core.", statusErr.status)}
		case statusErr.status == http.StatusMethodNotAllowed:
			return &DesktopProjectError{Code: desktopCodeUnsupportedOperation, Message: fmt.Sprintf("Dev Bridge Core rejected the request method (HTTP %d). Update Desktop to match the installed Core.", statusErr.status)}
		case statusErr.status == http.StatusConflict:
			return &DesktopProjectError{Code: desktopCodeConflict, Message: fmt.Sprintf("Dev Bridge Core reports a conflicting state (HTTP %d).", statusErr.status)}
		case statusErr.status == http.StatusUnauthorized, statusErr.status == http.StatusForbidden:
			return &DesktopProjectError{Code: desktopCodePermissionDenied, Message: fmt.Sprintf("Dev Bridge Core refused the request (HTTP %d).", statusErr.status)}
		case statusErr.status >= http.StatusInternalServerError:
			return &DesktopProjectError{Code: desktopCodeCoreError, Message: fmt.Sprintf("Dev Bridge Core failed the request (HTTP %d).", statusErr.status)}
		case statusErr.status >= http.StatusBadRequest:
			return &DesktopProjectError{Code: desktopCodeInvalidRequest, Message: fmt.Sprintf("Dev Bridge Core rejected the request (HTTP %d).", statusErr.status)}
		default:
			return &DesktopProjectError{Code: desktopCodeCoreError, Message: fmt.Sprintf("Dev Bridge Core returned an unexpected response (HTTP %d).", statusErr.status)}
		}
	}
	if isCoreTransportError(err) {
		return &DesktopProjectError{Code: desktopCodeCoreUnavailable, Message: "Dev Bridge Core is unavailable. Start it and try again."}
	}
	if registration {
		return &DesktopProjectError{Code: desktopCodeRegistrationFailed, Message: "Dev Bridge Core returned an unexpected registration response; retry the same request."}
	}
	return &DesktopProjectError{Code: desktopCodeCoreError, Message: "Dev Bridge Core returned an unexpected response."}
}
