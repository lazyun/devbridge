package bridgehttp

import (
	"net/http"

	"github.com/lazyun/devbridge/internal/executioncancel"
)

func (s *Server) cancelOperation(w http.ResponseWriter, r *http.Request) {
	var input executioncancel.Input
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.CancelExecutionForSession(r.Context(), input)
	respond(w, http.StatusOK, value, err)
}
