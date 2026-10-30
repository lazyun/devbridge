// Package codexruntime owns the Codex model and reasoning-effort allowlist
// shared by Core startup, the Desktop, the GPT-facing workflow state, and the
// durable Review/Task execution selection.
package codexruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// modelCapabilities supplies an offline baseline until a live catalog is read.
var modelCapabilities = [...]ModelCapability{
	{ModelID: "gpt-5.6-luna", SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"}, DefaultReasoningEffort: "medium", FastServiceTier: "priority"},
	{ModelID: "gpt-5.6-terra", SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, DefaultReasoningEffort: "medium", FastServiceTier: "priority"},
	{ModelID: "gpt-5.6-sol", SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, DefaultReasoningEffort: "low", FastServiceTier: "priority"},
	{ModelID: "gpt-6-astra", SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max", "ultra"}, DefaultReasoningEffort: "low", FastServiceTier: "priority"},
}

// ModelCapability is the bounded static capability catalog extracted from the
// installed Codex App Server model catalog on 2026-09-12. It is provenance for
// the product baseline, never a claim of live account availability.
type ModelCapability struct {
	ModelID                   string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string
	FastServiceTier           string
}

// ModelCandidates returns a detached copy of the offline baseline.
func ModelCandidates() []string {
	out := make([]string, 0, len(modelCapabilities))
	for _, capability := range modelCapabilities {
		out = append(out, capability.ModelID)
	}
	return out
}

func capabilityFor(model string) (ModelCapability, bool) {
	for _, capability := range modelCapabilities {
		if capability.ModelID == model {
			capability.SupportedReasoningEfforts = append([]string(nil), capability.SupportedReasoningEfforts...)
			return capability, true
		}
	}
	return ModelCapability{}, false
}

// AllReasoningEfforts defines the baseline display order. Additional catalog
// efforts follow it in their advertised order.
var AllReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

const (
	MaxModels       = 128
	MaxModelIDBytes = 128
)

// Allowlist is the runtime-authoritative set of models and reasoning efforts
// the Desktop has enabled. It is validated structurally and never infers a
// model or reasoning effort from task complexity.
type Allowlist struct {
	// Read-only configured registry inventory, never a model-settings grant.
	AgentConfigFingerprints map[string]string `json:"agentConfigFingerprints,omitempty"`

	// ModelConfigs is the canonical runtime configuration. Models and
	// ReasoningEfforts remain an input/output compatibility projection for old
	// Desktop settings and CLI callers; Authority normalizes them on ingress.
	ModelConfigs     []CodingAgentModelConfig
	Models           []string
	ReasoningEfforts []string
}

// CodingAgentModelConfig gives each Coding Agent model its own enabled
// state, compatible reasoning efforts, and default. A disabled entry remains
// present so changing one model never loses another model's preference.
type CodingAgentModelConfig struct {
	ModelID                   string   `json:"modelId"`
	Enabled                   bool     `json:"enabled"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort    string   `json:"defaultReasoningEffort"`
	// nil retains the legacy baseline; a discovered empty tier means Fast is unavailable.
	FastServiceTier *string `json:"fastServiceTier,omitempty"`
}

// FastTier resolves explicit catalog metadata or the legacy offline baseline.
func (c CodingAgentModelConfig) FastTier() string {
	if c.FastServiceTier != nil {
		return *c.FastServiceTier
	}
	capability, _ := capabilityFor(c.ModelID)
	return capability.FastServiceTier
}

// Default returns the complete enabled runtime inventory.
func Default() Allowlist {
	configs := make([]CodingAgentModelConfig, 0, len(modelCapabilities))
	for _, capability := range modelCapabilities {
		configs = append(configs, CodingAgentModelConfig{ModelID: capability.ModelID, Enabled: true, SupportedReasoningEfforts: append([]string(nil), capability.SupportedReasoningEfforts...), DefaultReasoningEffort: capability.DefaultReasoningEffort})
	}
	return allowlistFromConfigs(configs)
}

// Clone returns a detached value. Callers may mutate the returned slices
// without changing the source allowlist.
func (a Allowlist) Clone() Allowlist {
	return Allowlist{
		AgentConfigFingerprints: cloneFingerprints(a.AgentConfigFingerprints),
		ModelConfigs:            cloneModelConfigs(a.ModelConfigs),
		Models:                  append([]string(nil), a.Models...),
		ReasoningEfforts:        append([]string(nil), a.ReasoningEfforts...),
	}
}

// ModelConfig returns a detached configuration for model.
func (a Allowlist) ModelConfig(model string) (CodingAgentModelConfig, bool) {
	for _, config := range a.ModelConfigs {
		if config.ModelID == model {
			return cloneModelConfigs([]CodingAgentModelConfig{config})[0], true
		}
	}
	return CodingAgentModelConfig{}, false
}

// ValidateSelection validates the model/effort pair against the current
// per-model runtime authority. Operation-level callers use this after they
// have resolved an omitted effort through DefaultReasoningEffort.
func (a Allowlist) ValidateSelection(model, effort string) error {
	config, ok := a.ModelConfig(model)
	if !ok || !config.Enabled {
		return &SelectionError{Kind: ModelNotAllowed, Value: model, Candidates: append([]string(nil), a.Models...)}
	}
	if !contains(config.SupportedReasoningEfforts, effort) {
		return &SelectionError{Kind: ReasoningEffortNotAllowed, Value: effort, Candidates: append([]string(nil), config.SupportedReasoningEfforts...)}
	}
	return nil
}

// ResolveSelection applies the selected model's default only when the
// operation omitted an effort; an explicit effort always wins.
func (a Allowlist) ResolveSelection(model, explicitEffort string) (string, error) {
	config, ok := a.ModelConfig(model)
	if !ok || !config.Enabled {
		return "", &SelectionError{Kind: ModelNotAllowed, Value: model, Candidates: append([]string(nil), a.Models...)}
	}
	effort := explicitEffort
	if effort == "" {
		effort = config.DefaultReasoningEffort
	}
	if err := a.ValidateSelection(model, effort); err != nil {
		return "", err
	}
	return effort, nil
}

// ParseStrict parses explicit CLI inventory values without broadening invalid
// configuration to the defaults. An omitted dimension receives its default.
func ParseStrict(models, reasoningEfforts string) (Allowlist, error) {
	if strings.TrimSpace(reasoningEfforts) == "" {
		value := Default()
		if strings.TrimSpace(models) == "" {
			return value, nil
		}
		enabled := splitNonEmpty(models)
		if err := ValidateModels(enabled); err != nil {
			return Allowlist{}, err
		}
		for i := range value.ModelConfigs {
			value.ModelConfigs[i].Enabled = contains(enabled, value.ModelConfigs[i].ModelID)
		}
		return value.Normalize()
	}
	value := Default()
	// This is the legacy CLI shape. Clear the canonical defaults so it is
	// normalized from the explicit legacy dimensions below.
	value.ModelConfigs = nil
	if strings.TrimSpace(models) != "" {
		value.Models = splitNonEmpty(models)
	}
	value.ReasoningEfforts = splitNonEmpty(reasoningEfforts)
	if err := value.Validate(); err != nil {
		return Allowlist{}, err
	}
	return value.Normalize()
}

// ParseModelConfigsStrict parses the Desktop's canonical model-local runtime
// configuration, including capabilities persisted from model/list.
func ParseModelConfigsStrict(raw string) (Allowlist, error) {
	var configs []CodingAgentModelConfig
	if err := json.Unmarshal([]byte(raw), &configs); err != nil {
		return Allowlist{}, fmt.Errorf("decode coding agent model configs: %w", err)
	}
	return (Allowlist{ModelConfigs: configs}).Normalize()
}

func splitNonEmpty(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// Valid reports whether both enabled inventories satisfy their stable
// contracts.
func (a Allowlist) Valid() bool {
	return a.Validate() == nil
}

// Validate checks the persisted catalog structure and enabled selection.
func (a Allowlist) Validate() error {
	if err := validateFingerprints(a.AgentConfigFingerprints); err != nil {
		return err
	}
	if len(a.ModelConfigs) > 0 {
		return validateModelConfigs(a.ModelConfigs)
	}
	if err := ValidateModels(a.Models); err != nil {
		return err
	}
	if len(a.ReasoningEfforts) == 0 {
		return errors.New("at least one reasoning effort is required")
	}
	seen := make(map[string]struct{}, len(a.ReasoningEfforts))
	for _, effort := range a.ReasoningEfforts {
		if !contains(AllReasoningEfforts, effort) {
			return fmt.Errorf("unsupported reasoning effort %q", effort)
		}
		if _, ok := seen[effort]; ok {
			return fmt.Errorf("duplicate reasoning effort %q", effort)
		}
		seen[effort] = struct{}{}
	}
	return nil
}

// Normalize converts the legacy global inventory into model-local defaults
// once. It also materializes disabled known models, retaining their defaults
// when settings are subsequently saved.
func (a Allowlist) Normalize() (Allowlist, error) {
	if err := validateFingerprints(a.AgentConfigFingerprints); err != nil {
		return Allowlist{}, err
	}
	if len(a.ModelConfigs) > 0 {
		if err := validateModelConfigs(a.ModelConfigs); err != nil {
			return Allowlist{}, err
		}
		out := allowlistFromConfigs(a.ModelConfigs)
		out.AgentConfigFingerprints = cloneFingerprints(a.AgentConfigFingerprints)
		return out, nil
	}
	if err := a.Validate(); err != nil {
		return Allowlist{}, err
	}
	configs := make([]CodingAgentModelConfig, 0, len(modelCapabilities))
	for _, capability := range modelCapabilities {
		enabled := contains(a.Models, capability.ModelID)
		allowed := intersect(capability.SupportedReasoningEfforts, a.ReasoningEfforts)
		if len(allowed) == 0 {
			allowed = capability.SupportedReasoningEfforts
		}
		configs = append(configs, CodingAgentModelConfig{ModelID: capability.ModelID, Enabled: enabled, SupportedReasoningEfforts: capability.SupportedReasoningEfforts, DefaultReasoningEffort: defaultReasoningEffort(allowed)})
	}
	out := allowlistFromConfigs(configs)
	out.AgentConfigFingerprints = cloneFingerprints(a.AgentConfigFingerprints)
	return out, nil
}

func validateModelConfigs(configs []CodingAgentModelConfig) error {
	if len(configs) == 0 || len(configs) > MaxModels {
		return fmt.Errorf("coding agent model configs must contain between 1 and %d entries", MaxModels)
	}
	seen := make(map[string]struct{}, len(configs))
	enabled := 0
	for _, config := range configs {
		if err := ValidateModelID(config.ModelID); err != nil {
			return err
		}
		if _, ok := seen[config.ModelID]; ok {
			return fmt.Errorf("duplicate model ID %q", config.ModelID)
		}
		seen[config.ModelID] = struct{}{}
		if config.Enabled {
			enabled++
		}
		if len(config.SupportedReasoningEfforts) == 0 || len(config.SupportedReasoningEfforts) > 32 {
			return fmt.Errorf("model %q must advertise between 1 and 32 reasoning efforts", config.ModelID)
		}
		seenEfforts := make(map[string]bool)
		for _, effort := range config.SupportedReasoningEfforts {
			if err := ValidateModelID(effort); err != nil || seenEfforts[effort] {
				return fmt.Errorf("model %q has an invalid or duplicate reasoning effort %q", config.ModelID, effort)
			}
			seenEfforts[effort] = true
		}
		if config.FastServiceTier != nil && *config.FastServiceTier != "" && *config.FastServiceTier != "priority" {
			return fmt.Errorf("model %q has an unsupported Fast service tier", config.ModelID)
		}
		if !contains(config.SupportedReasoningEfforts, config.DefaultReasoningEffort) {
			return fmt.Errorf("model %q default reasoning effort must be supported", config.ModelID)
		}
	}
	if enabled == 0 {
		return errors.New("at least one coding agent model is required")
	}
	return nil
}

func defaultReasoningEffort(efforts []string) string {
	if contains(efforts, "medium") {
		return "medium"
	}
	return efforts[0]
}
func intersect(left, right []string) []string {
	var out []string
	for _, value := range left {
		if contains(right, value) {
			out = append(out, value)
		}
	}
	return out
}
func cloneModelConfigs(configs []CodingAgentModelConfig) []CodingAgentModelConfig {
	out := make([]CodingAgentModelConfig, len(configs))
	for i, config := range configs {
		out[i] = config
		out[i].SupportedReasoningEfforts = append([]string(nil), config.SupportedReasoningEfforts...)
		if config.FastServiceTier != nil {
			tier := *config.FastServiceTier
			out[i].FastServiceTier = &tier
		}
	}
	return out
}
func allowlistFromConfigs(configs []CodingAgentModelConfig) Allowlist {
	out := Allowlist{ModelConfigs: cloneModelConfigs(configs)}
	efforts := make(map[string]bool)
	for _, config := range configs {
		if config.Enabled {
			out.Models = append(out.Models, config.ModelID)
			for _, effort := range config.SupportedReasoningEfforts {
				efforts[effort] = true
			}
		}
	}
	for _, effort := range AllReasoningEfforts {
		if efforts[effort] {
			out.ReasoningEfforts = append(out.ReasoningEfforts, effort)
			delete(efforts, effort)
		}
	}
	// Keep future catalog values in their advertised order.
	for _, config := range configs {
		for _, effort := range config.SupportedReasoningEfforts {
			if efforts[effort] {
				out.ReasoningEfforts = append(out.ReasoningEfforts, effort)
				delete(efforts, effort)
			}
		}
	}
	return out
}

// ValidateModels validates the legacy CLI subset of the offline inventory.
// Dynamic models use ModelConfigs to carry their advertised capabilities.
func ValidateModels(models []string) error {
	if len(models) == 0 || len(models) > MaxModels {
		return fmt.Errorf("allowed models must contain between 1 and %d values", MaxModels)
	}
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if err := ValidateModelID(model); err != nil {
			return err
		}
		if _, known := capabilityFor(model); !known {
			return fmt.Errorf("unsupported model %q", model)
		}
		if _, ok := seen[model]; ok {
			return fmt.Errorf("duplicate model ID %q", model)
		}
		seen[model] = struct{}{}
	}
	return nil
}

// ValidateModelID accepts bounded Codex/OpenAI model identifiers without
// constraining them to the product defaults. Commas are reserved by the CLI
// inventory encoding and whitespace/control characters are never valid.
func ValidateModelID(model string) error {
	if model == "" || len(model) > MaxModelIDBytes || !utf8.ValidString(model) || strings.TrimSpace(model) != model {
		return fmt.Errorf("invalid model ID %q", model)
	}
	for _, r := range model {
		if r == ',' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("invalid model ID %q", model)
		}
	}
	return nil
}

// AllowModel reports whether the model is currently enabled.
func (a Allowlist) AllowModel(model string) bool { return contains(a.Models, model) }

// AllowReasoningEffort reports whether the reasoning effort is currently
// enabled.
func (a Allowlist) AllowReasoningEffort(effort string) bool {
	return contains(a.ReasoningEfforts, effort)
}

// ValidateModel returns a stable error naming the value and the candidate
// universe when the model is not allowed. GPT owns the choice; Bridge never
// imputes a fallback.
func (a Allowlist) ValidateModel(model string) error {
	if a.AllowModel(model) {
		return nil
	}
	return &SelectionError{Kind: ModelNotAllowed, Value: model, Candidates: append([]string(nil), a.Models...)}
}

// ValidateReasoningEffort returns a stable error naming the value and the
// candidate universe when the reasoning effort is not allowed.
func (a Allowlist) ValidateReasoningEffort(effort string) error {
	if a.AllowReasoningEffort(effort) {
		return nil
	}
	return &SelectionError{Kind: ReasoningEffortNotAllowed, Value: effort, Candidates: append([]string(nil), a.ReasoningEfforts...)}
}

type SelectionKind string

const (
	ModelNotAllowed                 SelectionKind = "MODEL_NOT_ALLOWED"
	ExecutionPolicyNotAllowed       SelectionKind = "EXECUTION_POLICY_NOT_ALLOWED"
	AgentConfigurationUnavailable   SelectionKind = "AGENT_CONFIGURATION_UNAVAILABLE"
	ReasoningEffortNotAllowed       SelectionKind = "REASONING_EFFORT_NOT_ALLOWED"
	BackendConfigurationUnavailable SelectionKind = "CODEX_BACKEND_CONFIGURATION_UNAVAILABLE"
	FastModeNotSupported            SelectionKind = "CODEX_FAST_MODE_NOT_SUPPORTED"
	RuntimeConfigurationUnknown     SelectionKind = "RUNTIME_CONFIGURATION_UNKNOWN"
	FrozenConfigurationMismatch     SelectionKind = "FROZEN_RUNTIME_CONFIGURATION_MISMATCH"
)

// SelectionError is the stable product error returned when GPT selects a value
// outside the runtime allowlist. The candidates are exposed so the model can
// immediately choose an allowed value.
type SelectionError struct {
	Kind       SelectionKind
	Value      string
	Candidates []string
	// Cause preserves wrapped domain evidence without parsing its message.
	Cause error
}

func (e *SelectionError) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case BackendConfigurationUnavailable:
		return "Enabled Codex backend configuration is required"
	case FastModeNotSupported:
		return "Codex Fast Mode is unavailable for the selected model"
	case RuntimeConfigurationUnknown:
		return "The frozen runtime configuration is incomplete"
	case FrozenConfigurationMismatch:
		return "The frozen runtime configuration no longer matches the supported capability"
	}
	return string(e.Kind) + ": " + e.Value
}

// Unwrap retains the original error for errors.Is/As consumers.
func (e *SelectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// Snapshot is one immutable inventory generation.
type Snapshot struct {
	Revision  uint64
	Allowlist Allowlist
}

func (s Snapshot) Clone() Snapshot {
	s.Allowlist = s.Allowlist.Clone()
	return s
}

var ErrStaleRevision = errors.New("Codex runtime inventory revision is stale")

// Authority owns the process-wide runtime inventory. Complete snapshots are
// replaced atomically under one mutex so models and efforts cannot be observed
// from different generations.
type Authority struct {
	mu                             sync.RWMutex
	revision                       uint64
	value                          Allowlist
	agentConfigFingerprintProvider func() map[string]string
}

func NewAuthority(initial Allowlist) (*Authority, error) {
	initial.AgentConfigFingerprints = nil // Only a configured factory provider can publish these.
	if len(initial.ModelConfigs) == 0 && len(initial.Models) == 0 && len(initial.ReasoningEfforts) == 0 {
		initial = Default()
	}
	var err error
	if initial, err = initial.Normalize(); err != nil {
		return nil, err
	}
	return &Authority{revision: 1, value: initial.Clone()}, nil
}

func (a *Authority) Snapshot() Snapshot {
	if a == nil {
		return Snapshot{}
	}
	a.mu.RLock()
	out, provider := Snapshot{Revision: a.revision, Allowlist: a.value.Clone()}, a.agentConfigFingerprintProvider
	a.mu.RUnlock()
	out.Allowlist.AgentConfigFingerprints = nil
	if provider != nil {
		out.Allowlist.AgentConfigFingerprints = cloneFingerprints(provider())
	}
	return out
}

// ExecutionAllowlist implements the request-time provider used by application
// and adapter layers.
func (a *Authority) ExecutionAllowlist() Allowlist { return a.Snapshot().Allowlist }

// Apply atomically replaces the whole inventory when expectedRevision still
// names the current generation.
func (a *Authority) Apply(expectedRevision uint64, next Allowlist) (Snapshot, error) {
	next.AgentConfigFingerprints = nil // Settings input is not configured agent provenance.
	if a == nil {
		return Snapshot{}, errors.New("Codex runtime inventory authority is unavailable")
	}
	var err error
	if next, err = next.Normalize(); err != nil {
		return Snapshot{}, err
	}
	a.mu.Lock()
	var resultErr error
	if expectedRevision != a.revision {
		resultErr = ErrStaleRevision
	} else {
		a.revision++
		a.value = next.Clone()
	}
	out, provider := Snapshot{Revision: a.revision, Allowlist: a.value.Clone()}, a.agentConfigFingerprintProvider
	a.mu.Unlock()
	if provider != nil {
		out.Allowlist.AgentConfigFingerprints = cloneFingerprints(provider())
	}
	return out, resultErr
}

// SetAgentConfigFingerprintProvider installs Core's current configured-factory
// inventory. Snapshot reads it separately from mutable model preferences; client
// settings cannot substitute launcher fingerprints. Consumers recheck exact
// fingerprints at confirmation and dispatch, not just inventory revision.
func (a *Authority) SetAgentConfigFingerprintProvider(provider func() map[string]string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.agentConfigFingerprintProvider = provider
	a.mu.Unlock()
}

func cloneFingerprints(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
func validateFingerprints(input map[string]string) error {
	for id, fingerprint := range input {
		if agentpolicy.ValidateSelection(id, agentpolicy.PolicyCooperativeTrustedV1) != nil || !agentpolicy.ValidConfigFingerprint(fingerprint) {
			return errors.New("invalid configured agent fingerprint inventory")
		}
	}
	return nil
}
