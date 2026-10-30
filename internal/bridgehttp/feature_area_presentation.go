package bridgehttp

import (
	"net/http"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
)

func (s *Server) getFeatureAreaPresentation(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" && key != "ai_session_id" && key != "format" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Presentation accepts only project_id, ai_session_id and format"})
			return
		}
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "text" {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "format must be json or text"})
		return
	}
	value, err := s.console.GetFeatureAreaPresentation(r.Context(), r.URL.Query().Get("project_id"), r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	if err == nil && format == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(workflowpresentation.RenderText(value)))
		return
	}
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getDevelopmentWorkspace(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for key := range q {
		switch key {
		case "project_id", "ai_session_id", "feature_area_id", "task_id", "format":
		default:
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Unknown workspace read parameter"})
			return
		}
	}
	if format := q.Get("format"); format != "" && format != "json" && format != "text" {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "format must be json or text"})
		return
	}
	v, err := s.console.GetDevelopmentWorkspace(r.Context(), q.Get("project_id"), q.Get("ai_session_id"), q.Get("feature_area_id"), q.Get("task_id"))
	w.Header().Set("Cache-Control", "no-store")
	if err == nil && q.Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(workflowpresentation.RenderWorkspaceText(v)))
		return
	}
	respond(w, http.StatusOK, v, err)
}
