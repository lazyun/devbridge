package sqlite

import (
	"encoding/json"
	"fmt"

	"github.com/lazyun/devbridge/internal/task"
)

func legacySpecs(goal string, constraints []string, reason string) (task.WorkSpec, task.VerificationSpec) {
	return task.WorkSpec{Goal: goal, Constraints: append([]string(nil), constraints...), CodexInstruction: goal, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeLegacyUnspecified}}, task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverLegacy, Reason: reason}}
}

type workSpecEnvelope struct {
	Version int `json:"version"`
	task.WorkSpec
}

type verificationSpecEnvelope struct {
	Version int `json:"version"`
	task.VerificationSpec
}

func encodeWorkSpec(value task.WorkSpec) (string, error) {
	if err := validateStoredWorkSpec(value, false); err != nil {
		return "", err
	}
	data, err := json.Marshal(workSpecEnvelope{Version: 1, WorkSpec: task.CloneWorkSpec(value)})
	return string(data), err
}

func decodeWorkSpec(data string) (task.WorkSpec, error) {
	var envelope workSpecEnvelope
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return task.WorkSpec{}, fmt.Errorf("decode work spec: %w", err)
	}
	if envelope.Version != 1 {
		return task.WorkSpec{}, fmt.Errorf("unsupported work spec version %d", envelope.Version)
	}
	if err := validateStoredWorkSpec(envelope.WorkSpec, true); err != nil {
		return task.WorkSpec{}, fmt.Errorf("validate persisted work spec: %w", err)
	}
	return task.CloneWorkSpec(envelope.WorkSpec), nil
}

// REPOSITORY_TARGETS is meaningful only once the surrounding immutable task
// definition supplies the exact target set. The storage envelope must retain
// that shape without recasting it as a scalar repository scope; definition
// decoding validates the complete v2 authority before exposing a Task.
func validateStoredWorkSpec(value task.WorkSpec, persisted bool) error {
	if value.ExecutionScope.Kind == task.ExecutionScopeTargets {
		return task.ValidateTargetWorkSpec(value)
	}
	if persisted {
		return task.ValidatePersistedWorkSpec(value)
	}
	return task.ValidateWorkSpec(value)
}

func encodeVerificationSpec(value task.VerificationSpec) (string, error) {
	if err := task.ValidatePersistedVerificationSpec(value); err != nil {
		return "", err
	}
	data, err := json.Marshal(verificationSpecEnvelope{Version: 1, VerificationSpec: task.CloneVerificationSpec(value)})
	return string(data), err
}

func decodeVerificationSpec(data string) (task.VerificationSpec, error) {
	var envelope verificationSpecEnvelope
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		return task.VerificationSpec{}, fmt.Errorf("decode verification spec: %w", err)
	}
	if envelope.Version != 1 {
		return task.VerificationSpec{}, fmt.Errorf("unsupported verification spec version %d", envelope.Version)
	}
	if err := task.ValidatePersistedVerificationSpec(envelope.VerificationSpec); err != nil {
		return task.VerificationSpec{}, fmt.Errorf("validate persisted verification spec: %w", err)
	}
	return task.CloneVerificationSpec(envelope.VerificationSpec), nil
}
