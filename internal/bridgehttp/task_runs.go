package bridgehttp

import (
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/run"
	"net/http"
)

func (s *Server) listProductTaskRuns(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" && key != "limit" && key != "offset" && key != "purpose" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Task Runs accepts only project_id, limit, offset and purpose"})
			return
		}
	}
	page, ok := pageRequest(w, r)
	if !ok {
		return
	}
	value, err := s.console.ListTaskRunsFiltered(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"), consoleapi.TaskRunFilter{PageRequest: page, Purpose: run.Purpose(r.URL.Query().Get("purpose"))})
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getProductTaskDefinitionRevision(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Task definition revision accepts only project_id"})
			return
		}
	}
	value, err := s.console.GetTaskDefinitionRevision(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"), r.PathValue("revision"))
	respond(w, http.StatusOK, value, err)
}
