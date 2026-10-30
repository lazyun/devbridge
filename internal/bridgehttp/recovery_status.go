package bridgehttp

import (
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"net/http"
)

// Each endpoint has a closed DTO. Objective admission, assessment and outcome
// are never accepted from HTTP clients.
func (s *Server) prepareRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AISessionID string              `json:"aiSessionId"`
		Spec        recoverystatus.Spec `json:"spec"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.api.PrepareRecoveryStatusForSession(r.Context(), in.AISessionID, in.Spec)
	respond(w, http.StatusOK, out, err)
}
func (s *Server) startRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AISessionID string                           `json:"aiSessionId"`
		Command     controlapi.RecoveryStatusCommand `json:"command"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Command.ID != r.PathValue("id") {
		http.Error(w, "Recovery identity mismatch", 400)
		return
	}
	out, err := s.api.StartRecoveryStatusForSession(r.Context(), in.AISessionID, in.Command)
	respond(w, http.StatusOK, out, err)
}
func (s *Server) resolveRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AISessionID string                                  `json:"aiSessionId"`
		Command     controlapi.RecoveryStatusResolveCommand `json:"command"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Command.ID != r.PathValue("id") {
		http.Error(w, "Recovery identity mismatch", 400)
		return
	}
	out, err := s.api.ResolveRecoveryStatusForSession(r.Context(), in.AISessionID, in.Command)
	respond(w, http.StatusOK, out, err)
}
func (s *Server) readRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	out, err := s.api.ReadRecoveryStatusForSession(r.Context(), r.URL.Query().Get("aiSessionId"), r.PathValue("id"))
	respond(w, http.StatusOK, out, err)
}
func (s *Server) continueInspectionRecovery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AISessionID string                                     `json:"aiSessionId"`
		Command     probeexecution.InspectionContinuationInput `json:"command"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Command.RecoveryStatusID != r.PathValue("id") {
		http.Error(w, "Recovery identity mismatch", http.StatusBadRequest)
		return
	}
	out, err := s.api.ContinueInspectionRecoveryForSession(r.Context(), in.AISessionID, in.Command)
	respond(w, http.StatusOK, out, err)
}
func (s *Server) prepareProductRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	var in recoverystatus.Spec
	if !decode(w, r, &in) {
		return
	}
	out, err := s.api.PrepareRecoveryStatusForOperator(r.Context(), in)
	respond(w, http.StatusOK, out, err)
}
func (s *Server) startProductRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	var in controlapi.RecoveryStatusCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ID != r.PathValue("id") {
		http.Error(w, "Recovery identity mismatch", 400)
		return
	}
	out, err := s.api.StartRecoveryStatusForOperator(r.Context(), in)
	respond(w, http.StatusOK, out, err)
}
func (s *Server) resolveProductRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	var in controlapi.RecoveryStatusResolveCommand
	if !decode(w, r, &in) {
		return
	}
	if in.ID != r.PathValue("id") {
		http.Error(w, "Recovery identity mismatch", 400)
		return
	}
	out, err := s.api.ResolveRecoveryStatusForOperator(r.Context(), in)
	respond(w, http.StatusOK, out, err)
}
func (s *Server) readProductRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	out, err := s.api.ReadRecoveryStatusForOperator(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	respond(w, http.StatusOK, out, err)
}
func (s *Server) continueProductInspectionRecovery(w http.ResponseWriter, r *http.Request) {
	var in probeexecution.InspectionContinuationInput
	if !decode(w, r, &in) {
		return
	}
	if in.RecoveryStatusID != r.PathValue("id") {
		http.Error(w, "Recovery identity mismatch", http.StatusBadRequest)
		return
	}
	out, err := s.api.ContinueInspectionRecoveryForOperator(r.Context(), r.URL.Query().Get("project_id"), in)
	respond(w, http.StatusOK, out, err)
}
