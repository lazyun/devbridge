package bridgehttp

import (
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"io"
	"net/http"
)

func (s *Server) resolveTaskAction(w http.ResponseWriter, r *http.Request) {
	var input consoleapi.TaskActionInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Invalid Task action"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Task action must contain one JSON value"})
		return
	}
	value, err := s.console.ResolveTaskAction(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"), input)
	respondTaskAction(w, value, err)
}

// Preserve exact committed start identity when a later read/handoff fails.
// The HTTP failure remains a failure, and unverified/staged objects never
// acquire a receipt merely because their IDs happen to be populated.
func respondTaskAction(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := apiErrorStatus(controlapi.MapError(err))
		if status == http.StatusForbidden || status == http.StatusNotFound {
			// A late scope/ownership denial cannot disclose even previously
			// observed identities to the presently unauthorized caller.
			writeError(w, err)
			return
		}
	}
	partial, ok := value.(executionproposal.StartResult)
	if !ok {
		if pointer, pointerOK := value.(*executionproposal.StartResult); pointerOK && pointer != nil {
			partial, ok = *pointer, true
		}
	}
	if err == nil || !ok || partial.Receipt == nil || partial.Receipt.ProjectID == "" || partial.Receipt.TaskID == "" || partial.Receipt.TaskDefinitionRevisionID == "" || partial.Receipt.OriginalExecutionID == "" {
		respond(w, http.StatusOK, value, err)
		return
	}
	var progressionErr *wp.Error
	if errors.As(err, &progressionErr) {
		writeJSON(w, http.StatusConflict, struct {
			*wp.Error
			Partial executionproposal.StartResult `json:"partial"`
		}{progressionErr, partial})
		return
	}
	var apiErr *controlapi.Error
	if !errors.As(err, &apiErr) {
		apiErr = controlapi.MapError(err)
	}
	writeJSON(w, apiErrorStatus(apiErr), struct {
		Code    string                        `json:"code"`
		Message string                        `json:"message"`
		Partial executionproposal.StartResult `json:"partial"`
	}{string(apiErr.Code), apiErr.Message, partial})
}

// readLocalApproveAndStartReceipt is a narrow, side-effect-free receipt read.
// It does not route through resolveTaskAction and cannot create or replay a
// workflow command.
func (s *Server) readLocalApproveAndStartReceipt(w http.ResponseWriter, r *http.Request) {
	var input consoleapi.TaskActionInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Invalid local approve-and-start receipt identity"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Receipt identity must contain one JSON value"})
		return
	}
	value, err := s.console.ReadLocalApproveAndStartReceipt(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"), input)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) prepareTaskCompletion(w http.ResponseWriter, r *http.Request) {
	value, err := s.console.PrepareTaskCompletion(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}
