package bridgehttp

import (
	"net/http"

	"github.com/lazyun/devbridge/internal/controlapi"
)

// These closed wire DTOs intentionally have no force, outcome, blocker,
// target, registration-state or topology override fields. Unknown fields are
// rejected by decode before a Core method is invoked.
type repositoryUnlinkPrepareInput struct {
	RequestID    string `json:"request_id"`
	ProjectID    string `json:"project_id"`
	RepositoryID string `json:"repository_id"`
}

type repositoryUnlinkCommitInput struct {
	RequestID                string `json:"request_id"`
	ProjectID                string `json:"project_id"`
	RepositoryID             string `json:"repository_id"`
	ExpectedGeneration       int64  `json:"expected_registration_generation"`
	ExpectedTopologyRevision int64  `json:"expected_topology_revision"`
	Reason                   string `json:"reason"`
}

func (s *Server) prepareRepositoryUnlink(w http.ResponseWriter, r *http.Request) {
	var input repositoryUnlinkPrepareInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.PrepareRepositoryUnlink(r.Context(), controlapi.RepositoryUnlinkPrepareInput{RequestID: input.RequestID, ProjectID: input.ProjectID, RepositoryID: input.RepositoryID})
	respond(w, http.StatusOK, value, err)
}

func (s *Server) readRepositoryUnlink(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("project_id") == "" || r.URL.Query().Get("repository_id") == "" || len(r.URL.Query()) != 2 {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Repository unlink read accepts only project_id and repository_id"})
		return
	}
	value, err := s.api.ReadRepositoryUnlink(r.Context(), r.URL.Query().Get("project_id"), r.URL.Query().Get("repository_id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listRepositoryUnlink(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project_id")
	includeHistorical := r.URL.Query().Get("include_historical") == "true"
	if projectID == "" || len(r.URL.Query()) > 2 || (len(r.URL.Query()) == 2 && r.URL.Query().Get("include_historical") != "true") {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Repository unlink list accepts project_id and optional include_historical=true"})
		return
	}
	value, err := s.api.ListRepositoryUnlinkProjects(r.Context(), projectID, includeHistorical)
	respond(w, http.StatusOK, map[string]any{"projectId": projectID, "repositories": value}, err)
}

func (s *Server) commitRepositoryUnlink(w http.ResponseWriter, r *http.Request) {
	var input repositoryUnlinkCommitInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.CommitRepositoryUnlink(r.Context(), controlapi.RepositoryUnlinkCommitInput{RequestID: input.RequestID, ProjectID: input.ProjectID, RepositoryID: input.RepositoryID, ExpectedGeneration: input.ExpectedGeneration, ExpectedTopologyRevision: input.ExpectedTopologyRevision, Reason: input.Reason})
	respond(w, http.StatusOK, value, err)
}
