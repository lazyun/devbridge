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
	"strings"
	"time"

	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Desktop owns presentation only. Reads and explicit commands stay on the
// actually attached Core endpoint; settings edited for a later restart cannot
// silently redirect an in-progress task operation.
func (a *App) consoleEndpoint() (string, error) {
	a.mu.RLock()
	supervisor := a.supervisor
	a.mu.RUnlock()
	if supervisor == nil {
		return "", errors.New("Core is unavailable")
	}
	core := supervisor.Snapshot().Core
	if (core.State != runtimecontrol.Running && core.State != runtimecontrol.External) || (core.Ownership != runtimecontrol.Managed && core.Ownership != runtimecontrol.Attached) {
		return "", errors.New("Core is not attached or running")
	}
	base, err := url.Parse(core.Endpoint)
	if err != nil || base.Scheme != "http" || base.User != nil || base.Port() == "" || !net.ParseIP(base.Hostname()).IsLoopback() || base.Path != "" && base.Path != "/" || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("explicit loopback Core endpoint required")
	}
	return base.String(), nil
}

func consoleReadPath(kind, projectID, entityID string, offset int, search string) (string, error) {
	if kind == "projects" {
		return "/v1/projects?limit=100", nil
	}
	if projectID == "" {
		return "", errors.New("exact project is required")
	}
	q := url.Values{"project_id": {projectID}}
	switch kind {
	case "tasks":
		if offset < 0 || offset > 100000 {
			return "", errors.New("invalid page")
		}
		q.Set("limit", "30")
		q.Set("offset", fmt.Sprint(offset))
		if search != "" {
			q.Set("search", search)
		}
		return "/ui/api/work-tabs/tasks?" + q.Encode(), nil
	case "task", "runs", "completion":
		if entityID == "" {
			return "", errors.New("exact task is required")
		}
		suffix := ""
		if kind == "runs" {
			suffix = "/runs"
			if offset < 0 || offset > 100000 {
				return "", errors.New("invalid run page")
			}
			q.Set("limit", "20")
			q.Set("offset", fmt.Sprint(offset))
		}
		if kind == "completion" {
			suffix = "/completion-plan"
		}
		return "/ui/api/tasks/" + url.PathEscape(entityID) + suffix + "?" + q.Encode(), nil
	case "run":
		if entityID == "" {
			return "", errors.New("exact run is required")
		}
		return "/v1/runs/" + url.PathEscape(entityID) + "?" + q.Encode(), nil
	}
	return "", errors.New("unsupported Console read")
}

func consoleHTTP(ctx context.Context, endpoint, method, path string, body []byte) (int, []byte, error) {
	target, err := coreURL(endpoint, path)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 12 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return 0, nil, err
	}
	if len(data) > 8<<20 {
		return 0, nil, errors.New("Console response exceeds limit")
	}
	if !json.Valid(data) {
		return 0, nil, errors.New("Core returned an unreadable response")
	}
	return response.StatusCode, data, nil
}

// ConsoleRead exposes a fixed set of scoped read models, never an arbitrary URL.
func (a *App) ConsoleRead(kind, projectID, entityID string, offset int, search string) (string, error) {
	path, err := consoleReadPath(kind, projectID, entityID, offset, search)
	if err != nil {
		return "", err
	}
	endpoint, err := a.consoleEndpoint()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	status, data, err := consoleHTTP(ctx, endpoint, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", decodeCoreError(bytes.NewReader(data), status)
	}
	return string(data), nil
}

type consoleTaskCommand struct {
	Action         string          `json:"action"`
	Target         json.RawMessage `json:"target"`
	RequestID      string          `json:"requestId"`
	Note           string          `json:"note,omitempty"`
	CompletionPlan json.RawMessage `json:"completionPlan,omitempty"`
}

func validateConsoleTaskCommand(taskID, body string) error {
	if taskID == "" || len(body) > 2<<20 {
		return errors.New("invalid task command")
	}
	var command consoleTaskCommand
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("multiple task commands")
	}
	switch command.Action {
	case "CONTINUE_TASK", "DEFER_TASK", "COMPLETE_TASK", "CANCEL_EXECUTION":
	default:
		return errors.New("this task action requires its dedicated workflow")
	}
	var target struct {
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(command.Target, &target) != nil || target.TaskID != taskID || command.RequestID == "" || len(command.RequestID) > 160 {
		return errors.New("exact task target and request identity required")
	}
	if command.Action == "COMPLETE_TASK" && (len(command.CompletionPlan) == 0 || string(command.CompletionPlan) == "null") {
		return errors.New("Core completion plan required")
	}
	return nil
}

// The UI persists the exact body before sending. No automatic retry, generated
// identity, capability expansion or replacement target is performed here.
func (a *App) ConsoleTaskAction(projectID, taskID, body string) (string, error) {
	if projectID == "" {
		return "", errors.New("exact project required")
	}
	if err := validateConsoleTaskCommand(taskID, body); err != nil {
		return "", err
	}
	endpoint, err := a.consoleEndpoint()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	path := "/ui/api/tasks/" + url.PathEscape(taskID) + "/action?" + url.Values{"project_id": {projectID}}.Encode()
	status, data, err := consoleHTTP(ctx, endpoint, http.MethodPost, path, []byte(body))
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(struct {
		Status int             `json:"status"`
		Data   json.RawMessage `json:"data"`
	}{status, json.RawMessage(data)})
	return string(result), err
}

// OpenConsoleTask is an explicit escape hatch for workflows whose structured
// approval forms are not implemented by this Desktop view. Keep exact context.
func (a *App) OpenConsoleTask(projectID, taskID string) error {
	if projectID == "" || taskID == "" {
		return errors.New("exact project and task required")
	}
	endpoint, err := a.consoleEndpoint()
	if err != nil {
		return err
	}
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	if ctx == nil {
		return errors.New("Desktop is unavailable")
	}
	fragment := url.Values{"project": {projectID}, "task": {taskID}, "tab": {"overview"}, "source": {"tasks"}}.Encode()
	wailsruntime.BrowserOpenURL(ctx, strings.TrimRight(endpoint, "/")+"/ui/#"+fragment)
	return nil
}
