package codexruntime

import "github.com/lazyun/devbridge/internal/agentpolicy"

// NextRunRuntimeConfig is the immutable runtime and enforcement selection
// authorized for one successor Execution. ServiceTier is derived rather than
// trusted from clients: the installed Codex App Server exposes Fast as
// config.service_tier=priority.
type NextRunRuntimeConfig struct {
	// Empty fields retain the exact historical Codex/native policy and JSON.
	// Cooperative execution is a prospective user selection, never inferred
	// from project trust, backend availability, or a previous run.
	AgentID                string `json:"agentId,omitempty"`
	AgentConfigFingerprint string `json:"agentConfigFingerprint,omitempty"`
	ExecutionPolicy        string `json:"executionPolicy,omitempty"`
	Model                  string `json:"model"`
	ReasoningEffort        string `json:"reasoningEffort"`
	CodexFastMode          bool   `json:"codexFastMode"`
	ServiceTier            string `json:"serviceTier"`
}

func (c NextRunRuntimeConfig) Normalize(allowlist Allowlist) (NextRunRuntimeConfig, error) {
	if err := agentpolicy.ValidateFrozenSelection(c.AgentID, c.ExecutionPolicy, c.AgentConfigFingerprint); err != nil {
		return NextRunRuntimeConfig{}, &SelectionError{Kind: ExecutionPolicyNotAllowed, Value: c.ExecutionPolicy, Cause: err}
	}
	if c.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && allowlist.AgentConfigFingerprints[c.AgentID] != c.AgentConfigFingerprint {
		return NextRunRuntimeConfig{}, &SelectionError{Kind: AgentConfigurationUnavailable, Value: c.AgentID}
	}
	effort, err := allowlist.ResolveSelection(c.Model, c.ReasoningEffort)
	if err != nil {
		return NextRunRuntimeConfig{}, err
	}
	c.ReasoningEffort = effort
	if c.CodexFastMode {
		config, ok := allowlist.ModelConfig(c.Model)
		if !ok || config.FastTier() == "" {
			return NextRunRuntimeConfig{}, &SelectionError{Kind: FastModeNotSupported, Value: c.Model}
		}
		c.ServiceTier = config.FastTier()
	} else {
		c.ServiceTier = ""
	}
	if c.Model == "" {
		return NextRunRuntimeConfig{}, &SelectionError{Kind: ModelNotAllowed}
	}
	return c, nil
}

// ValidateFrozen rechecks an exact persisted selection without supplying defaults.
func (c NextRunRuntimeConfig) ValidateFrozen(allowlist Allowlist) error {
	if c.Model == "" || c.ReasoningEffort == "" {
		return &SelectionError{Kind: RuntimeConfigurationUnknown}
	}
	normalized, err := c.Normalize(allowlist)
	if err != nil {
		return err
	}
	if normalized != c {
		return &SelectionError{Kind: FrozenConfigurationMismatch}
	}
	return nil
}

type RuntimeOption struct {
	ModelID                   string   `json:"modelId"`
	Enabled                   bool     `json:"enabled"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
	CodexFastModeSupported    bool     `json:"codexFastModeSupported"`
}

func (a Allowlist) RuntimeOptions() []RuntimeOption {
	options := make([]RuntimeOption, 0, len(a.ModelConfigs))
	for _, m := range a.ModelConfigs {
		options = append(options, RuntimeOption{m.ModelID, m.Enabled, append([]string(nil), m.SupportedReasoningEfforts...), m.FastTier() != ""})
	}
	return options
}
