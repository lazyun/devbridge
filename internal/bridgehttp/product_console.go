package bridgehttp

import (
	"net/http"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
)

func (s *Server) listProductFeatureAreas(w http.ResponseWriter, r *http.Request) {
	value, err := s.console.ListProductFeatureAreas(r.Context(), r.URL.Query().Get("project_id"))
	respond(w, http.StatusOK, map[string]any{"items": value}, err)
}

func (s *Server) listProductModules(w http.ResponseWriter, r *http.Request) {
	value, err := s.console.ListProductModules(r.Context(), r.URL.Query().Get("project_id"))
	respond(w, http.StatusOK, map[string]any{"items": value}, err)
}

func (s *Server) getProductProjectWorkspace(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Project workspace accepts only project_id"})
			return
		}
	}
	value, err := s.console.GetProductProjectWorkspace(r.Context(), r.URL.Query().Get("project_id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getProductDocument(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Document detail accepts only project_id"})
			return
		}
	}
	value, err := s.console.GetProductDocument(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getProductReviewResult(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Review result accepts only project_id"})
			return
		}
	}
	value, err := s.console.GetProductReviewResult(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) closeProductFeatureArea(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProjectID string `json:"project_id"`
		RequestID string `json:"request_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := s.console.CloseProductFeatureArea(r.Context(), input.ProjectID, r.PathValue("id"), input.RequestID)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getProductWorkTab(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("tab") == "tasks" {
		page, ok := pageRequest(w, r)
		if !ok {
			return
		}
		for key := range r.URL.Query() {
			switch key {
			case "project_id", "limit", "offset", "search", "feature_area_id", "status", "phase", "branch", "kind":
			default:
				writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Unsupported Task list filter"})
				return
			}
		}
		q := r.URL.Query()
		value, err := s.console.ListProductTasks(r.Context(), q.Get("project_id"), consoleapi.ProductTaskFilter{PageRequest: page, Search: q.Get("search"), FeatureAreaID: q.Get("feature_area_id"), Status: q.Get("status"), Phase: q.Get("phase"), Branch: q.Get("branch"), Kind: q.Get("kind")})
		respond(w, http.StatusOK, value, err)
		return
	}
	value, err := s.console.GetProductWorkTab(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("tab"))
	respond(w, http.StatusOK, map[string]any{"items": value.Executions}, err)
}
func (s *Server) cancelProductExecution(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedUpperVersion *agentconversation.UpperVersion `json:"expectedUpperVersion,omitempty"`
		RunID                string                          `json:"runId"`
		RequestID            string                          `json:"requestId"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, err := s.console.CancelProductExecution(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"), in.RunID, in.RequestID, in.ExpectedUpperVersion)
	respond(w, http.StatusOK, value, err)
}
func (s *Server) getProductTask(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" && key != "feature_area_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Task detail accepts only project_id and feature_area_id"})
			return
		}
	}
	value, err := s.console.GetProductTask(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	if expected := r.URL.Query().Get("feature_area_id"); err == nil && expected != "" && value.FeatureAreaID != expected {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "Task not found in FeatureArea"})
		return
	}
	respond(w, http.StatusOK, value, err)
}
