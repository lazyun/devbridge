package bridgehttp

import (
	"errors"
	"net/http"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/project"
)

// Project execution authorization is a local-user decision. These handlers
// are a trusted-host HTTP write surface and are not exposed as MCP/GPT tools.
type executionAuthorizationView struct {
	ProjectID        string `json:"project_id"`
	TrustedExecution bool   `json:"trusted_execution"`
	NetworkAccess    bool   `json:"network_access"`
	HeadRevision     int    `json:"head_revision"`
	AuthorizedAt     string `json:"authorized_at,omitempty"`
	UpdatedAt        string `json:"updated_at"`
}

type executionAuthorizationUpdateInput struct {
	TrustedExecution bool `json:"trusted_execution"`
	NetworkAccess    bool `json:"network_access"`
	ExpectedRevision int  `json:"expected_revision"`
}

func authorizationView(value project.ExecutionAuthorization) executionAuthorizationView {
	view := executionAuthorizationView{ProjectID: value.ProjectID, TrustedExecution: value.TrustedExecution, NetworkAccess: value.NetworkAccess, HeadRevision: value.HeadRevision, UpdatedAt: formatAuthorizationTime(value.UpdatedAt)}
	if !value.AuthorizedAt.IsZero() {
		view.AuthorizedAt = formatAuthorizationTime(value.AuthorizedAt)
	}
	return view
}

func formatAuthorizationTime(value interface{ Format(string) string }) string {
	return value.Format("2006-01-02T15:04:05Z07:00")
}

func (s *Server) getExecutionAuthorization(w http.ResponseWriter, r *http.Request) {
	if _, err := s.plane.Projects.Get(r.Context(), r.PathValue("id")); err != nil {
		// An unknown project must not masquerade as an unauthorized one.
		respond(w, http.StatusOK, nil, err)
		return
	}
	value, err := s.plane.ProjectAuthorizations.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, project.ErrAuthorizationNotFound) {
		// Absence is the default off state, not an error.
		writeJSON(w, http.StatusOK, executionAuthorizationView{ProjectID: r.PathValue("id")})
		return
	}
	respond(w, http.StatusOK, authorizationView(value), err)
}

func (s *Server) updateExecutionAuthorization(w http.ResponseWriter, r *http.Request) {
	var input executionAuthorizationUpdateInput
	if !decode(w, r, &input) {
		return
	}
	if _, err := s.plane.Projects.Get(r.Context(), r.PathValue("id")); err != nil {
		respond(w, http.StatusOK, nil, err)
		return
	}
	value, err := s.plane.ProjectAuthorizations.Update(r.Context(), r.PathValue("id"), input.ExpectedRevision, input.TrustedExecution, input.NetworkAccess)
	if err != nil {
		if errors.Is(err, project.ErrConflict) {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeConflict, Message: "expected_revision does not match the current authorization revision"})
			return
		}
		respond(w, http.StatusOK, nil, err)
		return
	}
	respond(w, http.StatusOK, authorizationView(value), nil)
}
