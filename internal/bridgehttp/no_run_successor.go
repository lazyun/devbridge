package bridgehttp

import (
	"net/http"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type successorPrepareRequest struct {
	wp.PrepareSuccessorExecution
}

func (s *Server) prepareSuccessorExecution(w http.ResponseWriter, r *http.Request) {
	var input successorPrepareRequest
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.PrepareSuccessorExecutionForOperator(r.Context(), input.ProjectID, input.PrepareSuccessorExecution)
	respond(w, http.StatusOK, value, err)
}
