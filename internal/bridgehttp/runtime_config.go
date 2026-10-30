package bridgehttp

import (
	"errors"
	"net/http"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
)

type codexRuntimeInventory struct {
	Revision                uint64                                `json:"revision"`
	CodingAgentModels       []codexruntime.CodingAgentModelConfig `json:"codingAgentModels"`
	AllowedModels           []string                              `json:"allowedModels"`
	AllowedReasoningEfforts []string                              `json:"allowedReasoningEfforts"`
	PiAgentEnabled          bool                                  `json:"piAgentEnabled"`
}

type applyCodexRuntimeInventoryRequest struct {
	ExpectedRevision  uint64                                `json:"expectedRevision"`
	CodingAgentModels []codexruntime.CodingAgentModelConfig `json:"codingAgentModels"`
}

func runtimeInventoryOutput(value codexruntime.Snapshot) codexRuntimeInventory {
	return codexRuntimeInventory{
		Revision:                value.Revision,
		CodingAgentModels:       value.Allowlist.ModelConfigs,
		AllowedModels:           append([]string(nil), value.Allowlist.Models...),
		AllowedReasoningEfforts: append([]string(nil), value.Allowlist.ReasoningEfforts...),
	}
}

func (s *Server) getCodexRuntimeInventory(w http.ResponseWriter, _ *http.Request) {
	value, err := s.api.CodexRuntimeInventory()
	if err != nil {
		writeError(w, err)
		return
	}
	output := runtimeInventoryOutput(value)
	output.PiAgentEnabled = s.plane != nil && s.plane.Coordinator != nil && s.plane.Coordinator.PiAgent != nil
	writeJSON(w, http.StatusOK, output)
}

func (s *Server) applyCodexRuntimeInventory(w http.ResponseWriter, r *http.Request) {
	var input applyCodexRuntimeInventoryRequest
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedRevision == 0 {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "expectedRevision is required"})
		return
	}
	next := codexruntime.Allowlist{ModelConfigs: input.CodingAgentModels}
	value, err := s.api.ApplyCodexRuntimeInventory(input.ExpectedRevision, next)
	if errors.Is(err, codexruntime.ErrStaleRevision) {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeConflict, Message: "Codex runtime inventory changed; retry from the current revision"})
		return
	}
	if err != nil {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, runtimeInventoryOutput(value))
}
