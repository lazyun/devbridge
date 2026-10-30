package bridgehttp

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/run"
)

type projectSelectionAction struct {
	ProjectID string `json:"project_id"`
}

func (s *Server) listConsoleProjects(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	value, err := s.console.ListProjects(r.Context(), page)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getConsoleProjectSelection(w http.ResponseWriter, r *http.Request) {
	value, err := s.console.CurrentSelection(r.Context())
	respond(w, http.StatusOK, value, err)
}

func (s *Server) selectConsoleProject(w http.ResponseWriter, r *http.Request) {
	var input projectSelectionAction
	if !decode(w, r, &input) {
		return
	}
	value, err := s.console.SelectProject(r.Context(), input.ProjectID)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getConsoleProjectOverview(w http.ResponseWriter, r *http.Request) {
	value, err := s.console.GetProjectOverview(r.Context(), r.URL.Query().Get("project_id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleRepositories(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	value, err := s.console.ListRepositories(r.Context(), r.URL.Query().Get("project_id"), page)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleModules(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	value, err := s.console.ListModules(r.Context(), r.URL.Query().Get("project_id"), page)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleAISessions(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	value, err := s.console.ListAISessions(r.Context(), r.URL.Query().Get("project_id"), page)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleContextProbes(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	value, err := s.console.ListContextProbes(r.Context(), r.URL.Query().Get("project_id"), page)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleTasks(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	filter := consoleapi.TaskListFilter{PageRequest: page, WorkKind: r.URL.Query().Get("work_kind")}
	value, err := s.console.ListTasks(r.Context(), r.URL.Query().Get("project_id"), filter)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getConsoleReviewPrompt(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Review prompt diagnostics accept only project_id"})
			return
		}
	}
	value, err := s.console.GetReviewPrompt(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getConsoleReviewRepositoryDrift(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Review repository drift accepts only project_id"})
			return
		}
	}
	value, err := s.console.ReviewRepositoryDrift(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"), 0)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getConsoleRunPrompt(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" && key != "task_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Run prompt diagnostics accept only project_id and task_id"})
			return
		}
	}
	if taskID := r.URL.Query().Get("task_id"); taskID != "" {
		value, err := s.console.GetRunPromptForTask(r.Context(), r.URL.Query().Get("project_id"), taskID, r.PathValue("id"))
		respond(w, http.StatusOK, value, err)
		return
	}
	value, err := s.console.GetRunPrompt(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getConsoleContextProbePrompt(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Context Probe prompt diagnostics accept only project_id"})
			return
		}
	}
	value, err := s.console.GetContextProbePrompt(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleRuns(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	activeOnly, ok := boolQuery(w, r, "active")
	if !ok {
		return
	}
	filter := consoleapi.RunListFilter{PageRequest: page, ActiveOnly: activeOnly, Status: run.Status(r.URL.Query().Get("status")), Scope: r.URL.Query().Get("scope"), FeatureAreaID: r.URL.Query().Get("feature_area_id")}
	value, err := s.console.ListRuns(r.Context(), r.URL.Query().Get("project_id"), filter)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleApprovals(w http.ResponseWriter, r *http.Request) {
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	pendingOnly, ok := boolQuery(w, r, "pending")
	if !ok {
		return
	}
	value, err := s.console.ListApprovals(r.Context(), r.URL.Query().Get("project_id"), consoleapi.ApprovalListFilter{PageRequest: page, PendingOnly: pendingOnly})
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listConsoleLogs(w http.ResponseWriter, r *http.Request) {
	limit, ok := intQuery(w, r, "limit")
	if !ok {
		return
	}
	if limit < 0 || limit > consoleapi.MaxLogLimit {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "limit must be between 1 and 500"})
		return
	}
	value, err := s.console.ListLogs(r.Context(), consoleapi.LogListFilter{
		Limit:         limit,
		Level:         r.URL.Query().Get("level"),
		Process:       r.URL.Query().Get("process"),
		Component:     r.URL.Query().Get("component"),
		Event:         r.URL.Query().Get("event"),
		FeatureAreaID: r.URL.Query().Get("feature_area_id"),
		TaskID:        r.URL.Query().Get("task_id"),
		ExecutionID:   r.URL.Query().Get("execution_id"),
		RunID:         r.URL.Query().Get("run_id"),
	})
	respond(w, http.StatusOK, value, err)
}

func pageRequest(w http.ResponseWriter, r *http.Request) (consoleapi.PageRequest, bool) {
	limit, ok := intQuery(w, r, "limit")
	if !ok {
		return consoleapi.PageRequest{}, false
	}
	offset, ok := intQuery(w, r, "offset")
	if !ok {
		return consoleapi.PageRequest{}, false
	}
	return consoleapi.PageRequest{Limit: limit, Offset: offset}, true
}

func intQuery(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: name + " must be an integer"})
		return 0, false
	}
	return value, true
}

func boolQuery(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return false, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: name + " must be true or false"})
		return false, false
	}
	return value, true
}
