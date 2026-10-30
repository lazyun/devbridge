package executionproposal

import (
	"context"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/run"
)

type HandoffState string

const (
	HandoffRegistered HandoffState = "REGISTERED"
	HandoffPreparing  HandoffState = "PREPARING"
	HandoffAccepted   HandoffState = "ACCEPTED"
	HandoffRejected   HandoffState = "REJECTED"
	HandoffUnknown    HandoffState = "UNKNOWN"
	HandoffTerminal   HandoffState = "TERMINAL"
)

// Handoff is a delivery receipt, never an authorization or backend acceptance.
// AcceptedAt proves Supervisor ownership; only Run Thread/Turn facts prove
// backend acceptance. No record is synthesized for a legacy Run.
type Handoff struct {
	RunID        string       `json:"runId"`
	TaskID       string       `json:"taskId,omitempty"`
	ExecutionID  string       `json:"executionId,omitempty"`
	RuntimeEpoch string       `json:"runtimeEpoch,omitempty"`
	Attempt      int          `json:"attempt"`
	State        HandoffState `json:"state"`
	Code         string       `json:"code,omitempty"`
	OwnerKind    string       `json:"ownerKind"`
	AcceptedAt   *time.Time   `json:"acceptedAt,omitempty"`
	// RecordExists, RecordedState and RecordedRuntimeEpoch are the unmodified
	// runtime_handoffs row. State remains the compatibility/effective view and
	// may become TERMINAL when the Run ends; it must never erase this evidence.
	RecordExists          bool         `json:"recordExists"`
	RecordedState         HandoffState `json:"recordedState,omitempty"`
	RecordedRuntimeEpoch  string       `json:"recordedRuntimeEpoch,omitempty"`
	RecordedCode          string       `json:"recordedCode,omitempty"`
	RecordEpochMatchesRun bool         `json:"recordEpochMatchesRun,omitempty"`
	RunEpochCurrent       bool         `json:"runEpochCurrent,omitempty"`
	// RunStatus is an observation of the associated Run, independent from the
	// durable Supervisor receipt. A terminal Run must not be interpreted as a
	// successful handoff when accepted_at was never recorded.
	RunStatus                   run.Status `json:"runStatus,omitempty"`
	SupportsCoreRestartReattach bool       `json:"supportsCoreRestartReattach"`
}

// Accepted reports a current readable acceptance outcome. Historical command
// projection must use WasAccepted so an epoch-derived UNKNOWN does not acquire
// current acceptance semantics merely because accepted_at is retained.
func (h Handoff) Accepted() bool {
	return h.WasAccepted() && (h.State == HandoffAccepted || h.State == HandoffTerminal)
}

// WasAccepted reports only the historical persisted Supervisor ownership
// fact. It deliberately does not answer whether the Run is terminal, belongs
// to the current epoch, or is eligible for another dispatch.
func (h Handoff) WasAccepted() bool {
	return h.RunID != "" && h.AcceptedAt != nil
}

// RecordedAccepted is the stricter proof used by parent-command evidence
// projection. It binds accepted_at to this Run's original handoff record and
// epoch without changing the established WasAccepted/Accepted contract used
// by existing dispatcher callers.
func (h Handoff) RecordedAccepted() bool {
	return h.WasAccepted() && h.RecordExists && h.RecordedState == HandoffAccepted && h.RecordedRuntimeEpoch == h.RuntimeEpoch
}

type HandoffError struct{ Handoff Handoff }

func (e *HandoffError) Error() string {
	return fmt.Sprintf("runtime handoff %s: %s", e.Handoff.State, e.Handoff.Code)
}
func (e *HandoffError) Unwrap() error { return ErrConflict }

type handoffStore interface {
	ReadHandoff(context.Context, string, string) (Handoff, error)
	ClaimHandoff(context.Context, string, string, time.Time) (Handoff, bool, error)
	RecordHandoff(context.Context, string, string, HandoffState, string, time.Time) (Handoff, error)
}
type reservedExecutionStore interface {
	ReserveExecution(context.Context, string, bool, time.Time) (Proposal, run.Run, error)
}

// ReadHandoff is pure persisted evidence reading after the surface authorizes
// the exact Project. Old epochs cannot be presented as current live ownership.
func (s *Service) ReadHandoff(ctx context.Context, projectID, runID string) (Handoff, error) {
	if !validID(projectID) || !validID(runID) {
		return Handoff{}, ErrInvalidArgument
	}
	store, ok := s.store.(handoffStore)
	if !ok {
		return Handoff{}, fmt.Errorf("runtime handoff unavailable")
	}
	return store.ReadHandoff(ctx, projectID, runID)
}
