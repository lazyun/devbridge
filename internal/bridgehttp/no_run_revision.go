package bridgehttp

import (
	"net/http"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func (s *Server) stopAndReviseNoRunTask(w http.ResponseWriter, r *http.Request) {
	var in wp.StopAndReviseNoRunTask
	if !decode(w, r, &in) {
		return
	}
	value, err := s.api.StopAndReviseNoRunTaskForOperator(r.Context(), in.ProjectID, in)
	respond(w, http.StatusOK, value, err)
}
