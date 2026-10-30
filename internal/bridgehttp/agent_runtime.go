package bridgehttp

import (
	"net/http"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
)

func (s *Server) agentRuntimeState(w http.ResponseWriter, r *http.Request) {
	value, err := s.api.AgentRuntimeStateForSession(r.Context(), r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}
func (s *Server) resolveAgentRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AISessionID string                  `json:"ai_session_id"`
		Resolution  agentruntime.Resolution `json:"resolution"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Resolution.RunID != r.PathValue("id") {
		http.Error(w, "Run identity mismatch", http.StatusBadRequest)
		return
	}
	value, err := s.api.ResolveAgentRequestForSession(r.Context(), in.AISessionID, in.Resolution)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) resolveProductAgentRequest(w http.ResponseWriter, r *http.Request) {
	var in agentruntime.Resolution
	if !decode(w, r, &in) {
		return
	}
	if in.RunID != r.PathValue("id") {
		http.Error(w, "Run identity mismatch", http.StatusBadRequest)
		return
	}
	value, err := s.console.ResolveProductAgentRequest(r.Context(), in)
	respond(w, http.StatusOK, value, err)
}

// submitProductAgentUserDecision records one exact user choice through the
// canonical runtime-decision authority. It intentionally accepts no legacy
// Resolution or raw backend response: delivery occurs only after the durable
// UserDecision has been written by Core.
func (s *Server) submitProductAgentUserDecision(w http.ResponseWriter, r *http.Request) {
	var in agentruntime.UserDecisionInput
	if !decode(w, r, &in) {
		return
	}
	if in.RunID != r.PathValue("id") {
		http.Error(w, "Run identity mismatch", http.StatusBadRequest)
		return
	}
	value, err := s.api.SubmitAgentUserDecisionForOperator(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) submitAgentUserDecision(w http.ResponseWriter, r *http.Request) {
	var in agentruntime.UserDecisionInput
	if !decode(w, r, &in) {
		return
	}
	if in.RunID != r.PathValue("id") {
		http.Error(w, "Run identity mismatch", http.StatusBadRequest)
		return
	}
	value, err := s.api.SubmitAgentUserDecisionForOperator(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) recoverProductAgentRequest(w http.ResponseWriter, r *http.Request) {
	var in controlapi.AgentRequestRecoveryInput
	if !decode(w, r, &in) {
		return
	}
	if in.RunID != r.PathValue("id") {
		http.Error(w, "Run identity mismatch", http.StatusBadRequest)
		return
	}
	value, err := s.api.RecoverAgentRequestForOperator(r.Context(), in)
	respond(w, http.StatusOK, value, err)
}
func (s *Server) recoverAgentRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AISessionID string                               `json:"aiSessionId"`
		Recovery    controlapi.AgentRequestRecoveryInput `json:"recovery"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Recovery.RunID != r.PathValue("id") {
		http.Error(w, "Run identity mismatch", http.StatusBadRequest)
		return
	}
	value, err := s.api.RecoverAgentRequestForSession(r.Context(), in.AISessionID, in.Recovery)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) steerProductRun(w http.ResponseWriter, r *http.Request) {
	var in agentruntime.SteeringInput
	if !decode(w, r, &in) {
		return
	}
	if in.RunID != r.PathValue("id") {
		http.Error(w, "Run identity mismatch", http.StatusBadRequest)
		return
	}
	value, err := s.api.SteerRunForOperator(r.Context(), in)
	respond(w, http.StatusOK, value, err)
}
