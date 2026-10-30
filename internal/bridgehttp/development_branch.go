package bridgehttp

import (
	"net/http"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/developmentcycle"
)

// The local Console supplies an explicit trusted Project identity. Both reads
// and mutations use the same application operations as the AI adapters.
func (s *Server) getDevelopmentContext(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Development context accepts only project_id"})
			return
		}
	}
	value, err := s.api.DevelopmentContext(r.Context(), r.URL.Query().Get("project_id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) selectDevelopmentBranch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProjectID      string `json:"project_id"`
		RepositoryID   string `json:"repository_id"`
		DevelopmentRef string `json:"development_ref"`
		RequestID      string `json:"request_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.SelectDevelopmentBranch(r.Context(), developmentcycle.SelectInput{ProjectID: input.ProjectID, RepositoryID: input.RepositoryID, DevelopmentRef: input.DevelopmentRef, RequestID: input.RequestID})
	respond(w, http.StatusOK, value, err)
}
