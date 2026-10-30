package agent

import (
	"errors"
	"fmt"
	"strings"
)

// Capabilities are explicit adapter declarations, not permissions, runtime
// qualification, or evidence that a particular request succeeded. Core must
// still validate the frozen execution and exact observed receipts.
type Capability string

const (
	CapabilityCooperativePolicy    Capability = "cooperative_policy"
	CapabilityThreadResume         Capability = "thread_resume"
	CapabilityStructuredOutput     Capability = "structured_output"
	CapabilityTurnStarted          Capability = "turn_started"
	CapabilityInterrupt            Capability = "interrupt"
	CapabilityUsage                Capability = "usage_observation"
	CapabilityRuntimeBinding       Capability = "runtime_binding"
	CapabilityHostTools            Capability = "host_tools"
	CapabilityServiceTier          Capability = "service_tier"
	CapabilityQualifiedLaunch      Capability = "qualified_launch"
	CapabilitySameTurnRecovery     Capability = "same_turn_recovery"
	CapabilityAuthorizationCeiling Capability = "authorization_ceiling"
)

type Descriptor struct {
	ConfigFingerprint string `json:"configFingerprint,omitempty"`
	ID                string
	Capabilities      []Capability
}

type Described interface{ AgentDescriptor() Descriptor }

var (
	ErrAgentUnavailable      = errors.New("agent is not registered")
	ErrCapabilityUnsupported = errors.New("agent capability is unsupported")
)

type CapabilityError struct {
	AgentID string
	Missing []Capability
}

func (e *CapabilityError) Error() string {
	missing := make([]string, len(e.Missing))
	for i, capability := range e.Missing {
		missing[i] = string(capability)
	}
	return fmt.Sprintf("agent %q does not support required capabilities: %s", e.AgentID, strings.Join(missing, ", "))
}
func (e *CapabilityError) Unwrap() error { return ErrCapabilityUnsupported }

// Registration keeps selection independent of the launch implementation. The
// generic value is the caller's typed launch handle, not an untyped payload.
type Registration[T any] struct {
	Descriptor Descriptor
	Value      T
}

// Registry is immutable after construction. Selection is exact and never
// falls back to another agent when a capability or identity is unavailable.
type Registry[T any] struct{ entries map[string]Registration[T] }

func NewRegistry[T any](entries ...Registration[T]) (*Registry[T], error) {
	r := &Registry[T]{entries: make(map[string]Registration[T], len(entries))}
	for _, entry := range entries {
		id := entry.Descriptor.ID
		if id == "" || strings.TrimSpace(id) != id {
			return nil, errors.New("agent identity is required")
		}
		if _, exists := r.entries[id]; exists {
			return nil, fmt.Errorf("duplicate agent identity %q", id)
		}
		entry.Descriptor.Capabilities = append([]Capability(nil), entry.Descriptor.Capabilities...)
		r.entries[id] = entry
	}
	return r, nil
}

func (r *Registry[T]) Select(id string, required ...Capability) (T, error) {
	var zero T
	if r == nil {
		return zero, fmt.Errorf("%s: %w", id, ErrAgentUnavailable)
	}
	entry, ok := r.entries[id]
	if !ok {
		return zero, fmt.Errorf("%s: %w", id, ErrAgentUnavailable)
	}
	supported := make(map[Capability]bool, len(entry.Descriptor.Capabilities))
	for _, capability := range entry.Descriptor.Capabilities {
		supported[capability] = true
	}
	var missing []Capability
	seen := make(map[Capability]bool)
	for _, capability := range required {
		if !supported[capability] && !seen[capability] {
			missing = append(missing, capability)
			seen[capability] = true
		}
	}
	if len(missing) != 0 {
		return zero, &CapabilityError{AgentID: id, Missing: missing}
	}
	return entry.Value, nil
}
