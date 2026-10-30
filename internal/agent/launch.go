package agent

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/agentpolicy"
)

// Factory is the neutral launch boundary for explicitly registered agents.
// Legacy native launch/recovery remains separate; this interface does not
// assert support for private checkpoints, same-turn recovery, or sandboxing.
type Factory interface {
	AgentDescriptor() Descriptor
	StartAgent(context.Context, LaunchRequest) (Session, error)
}

type BackendConfig struct {
	Fingerprint string
	ID          string
	Name        string
	Executable  string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type RuntimeSelection struct {
	Model           string
	ReasoningEffort string
	ServiceTier     string
}

// LaunchRequest carries frozen launch inputs, never the task instruction.
// A cooperative adapter configures Policy.SystemText separately, reports only
// LAUNCH_CONFIGURED through OnPolicyConfigured, and must propagate callback
// failure before returning a session that can execute a turn. The callback is
// not a delivery acknowledgement from the agent or a durable decision receipt.
type LaunchRequest struct {
	AgentConfigFingerprint string
	RunID                  string
	Backend                BackendConfig
	Runtime                RuntimeSelection
	CWD                    string
	Policy                 *agentpolicy.Envelope
	OnPolicyConfigured     func(agentpolicy.LaunchReceipt) error
	RuntimeBinding         RuntimeBinding
}
