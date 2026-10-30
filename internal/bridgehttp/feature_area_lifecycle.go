package bridgehttp

import (
	"github.com/lazyun/devbridge/internal/featurearea"
	"net/http"
)

type featureAreaLifecycleRequest struct {
	AISessionID   string                        `json:"aiSessionId"`
	FeatureAreaID string                        `json:"featureAreaId,omitempty"`
	RequestID     string                        `json:"requestId"`
	Children      []featurearea.ChildDefinition `json:"children,omitempty"`
}

func (s *Server) registerFeatureAreaLifecycle(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/feature-areas", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			AISessionID      string `json:"aiSessionId"`
			Title            string `json:"title"`
			ProblemStatement string `json:"problemStatement"`
		}
		if !decode(w, r, &input) {
			return
		}
		value, err := s.api.CreateFeatureArea(r.Context(), featurearea.CreateInput{AISessionID: input.AISessionID, Title: input.Title, ProblemStatement: input.ProblemStatement})
		writeResult(w, value, err, http.StatusCreated)
	})
	mux.HandleFunc("POST /v1/feature-areas/{featureAreaID}/activate", func(w http.ResponseWriter, r *http.Request) {
		var input featureAreaLifecycleRequest
		if !decode(w, r, &input) {
			return
		}
		value, err := s.api.ActivateFeatureArea(r.Context(), input.AISessionID, r.PathValue("featureAreaID"), input.RequestID)
		writeResult(w, value, err, http.StatusOK)
	})
	mux.HandleFunc("POST /v1/feature-areas/{featureAreaID}/retire", func(w http.ResponseWriter, r *http.Request) {
		var input featureAreaLifecycleRequest
		if !decode(w, r, &input) {
			return
		}
		value, err := s.api.RetireFeatureArea(r.Context(), input.AISessionID, r.PathValue("featureAreaID"), input.RequestID)
		writeResult(w, value, err, http.StatusOK)
	})
	mux.HandleFunc("POST /v1/feature-areas/{featureAreaID}/decompose", func(w http.ResponseWriter, r *http.Request) {
		var input featureAreaLifecycleRequest
		if !decode(w, r, &input) {
			return
		}
		value, err := s.api.DecomposeFeatureArea(r.Context(), featurearea.DecomposeInput{AISessionID: input.AISessionID, FeatureAreaID: r.PathValue("featureAreaID"), RequestID: input.RequestID, Children: input.Children})
		writeResult(w, value, err, http.StatusOK)
	})
	mux.HandleFunc("POST /v1/agent-context-reset", func(w http.ResponseWriter, r *http.Request) {
		var input featureAreaLifecycleRequest
		if !decode(w, r, &input) {
			return
		}
		value, err := s.api.ResetFeatureAreaAgentContext(r.Context(), input.AISessionID, input.FeatureAreaID, input.RequestID)
		writeResult(w, value, err, http.StatusOK)
	})
}
