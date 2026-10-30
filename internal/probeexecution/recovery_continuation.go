package probeexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/contextprobe"
)

// InspectionContinuationInput authorizes a new turn only through a durable
// RECOVERED_PARTIAL receipt. ActorID is derived by the authenticated facade.
type InspectionContinuationInput struct {
	RecoveryStatusID     string `json:"recoveryStatusId"`
	RequestID            string `json:"requestId"`
	ActorID              string `json:"-"`
	Instruction          string `json:"instruction"`
	PayloadHash          string `json:"payloadHash"`
	ExpectedRuntimeEpoch string `json:"expectedRuntimeEpoch"`
}

func (in InspectionContinuationInput) ComputePayloadHash() string {
	in.PayloadHash = ""
	in.ActorID = ""
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type inspectionContinuationStore interface {
	ContinueInspection(context.Context, InspectionContinuationInput) (CanonicalState, error)
}

func (r *CanonicalRunner) ContinueInspection(ctx context.Context, in InspectionContinuationInput) (contextprobe.Probe, error) {
	store, ok := r.Store.(inspectionContinuationStore)
	if !ok || in.RequestID == "" || in.ActorID == "" || in.PayloadHash != in.ComputePayloadHash() {
		return contextprobe.Probe{}, contextprobe.ErrInvalidArgument
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	state, err := store.ContinueInspection(ctx, in)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if state.State != "RUN_CREATED" || r.workers[state.Spec.Probe.ID] != nil {
		return state.Spec.Probe, nil
	}
	if state.ContinuationRecoveryStatusID != in.RecoveryStatusID || state.Run.TurnID != "" {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	if state.ContinuationFreshThread {
		if state.ContinuationSourceTurnID != "" || state.Run.ThreadID != "" || state.Conversation.BackendThreadID != "" || state.Conversation.PredecessorID == "" {
			return contextprobe.Probe{}, contextprobe.ErrConflict
		}
	} else if state.ContinuationSourceTurnID == "" || state.Run.ThreadID == "" {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	config, err := r.Configs.Get(ctx, state.Spec.BackendSelection.ConfigID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if !config.Enabled || config.Fingerprint() != state.Spec.BackendSelection.ConfigFingerprint {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	r.spawn(ctx, state, config, false)
	return state.Spec.Probe, nil
}
func (r *Runner) ContinueInspection(ctx context.Context, in InspectionContinuationInput) (contextprobe.Probe, error) {
	if r.Canonical == nil {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	return r.Canonical.ContinueInspection(ctx, in)
}
