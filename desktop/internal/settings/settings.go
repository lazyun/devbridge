// Package settings stores local-only Dev Bridge Desktop configuration.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lazyun/devbridge/internal/codexruntime"
)

const applicationName = "devbridge-desktop"
const codingAgentConfigVersion = 2
const piAgentConfigVersion = 1

// PiAgentModelConfig is the PI Agent model configuration.
type PiAgentModelConfig struct {
	ModelID                   string   `json:"modelId"`
	Provider                  string   `json:"provider,omitempty"`
	Enabled                   bool     `json:"enabled"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts,omitempty"`
	DefaultReasoningEffort    string   `json:"defaultReasoningEffort,omitempty"`
	ContextWindow             string   `json:"contextWindow,omitempty"`
	MaxOutput                 string   `json:"maxOutput,omitempty"`
	SupportsThinking          bool     `json:"supportsThinking,omitempty"`
	SupportsImages            bool     `json:"supportsImages,omitempty"`
}

// Settings is the complete persisted local Desktop configuration. It contains no
// credentials; binary names may be command names resolved through PATH or paths.
// The OpenAI tunnel-client fields are optional ChatGPT-integration configuration:
// they never gate Core or MCP availability. The API key field holds only a
// reference (env:NAME or file:/path), never a secret value.
type Settings struct {
	DataRoot                string   `json:"dataRoot"`
	CoreBinary              string   `json:"coreBinary"`
	MCPBinary               string   `json:"mcpBinary"`
	LocalMCPBinary          string   `json:"localMCPBinary,omitempty"`
	CodexExecutable         string   `json:"codexExecutable"`
	PiExecutable            string   `json:"piExecutable,omitempty"`
	PiModel                 string   `json:"piModel,omitempty"`
	PiThinkingLevel         string   `json:"piThinkingLevel,omitempty"`
	OpenAITunnelExecutable  string   `json:"openAITunnelExecutable,omitempty"`
	OpenAITunnelID          string   `json:"openAITunnelID,omitempty"`
	AllowedModels           []string `json:"allowedModels,omitempty"`
	AllowedReasoningEfforts []string `json:"allowedReasoningEfforts,omitempty"`
	// CodingAgentModels is the v2 canonical runtime inventory. The two legacy
	// allowlist fields above are accepted only to migrate existing settings.
	CodingAgentModels        []codexruntime.CodingAgentModelConfig `json:"codingAgentModels,omitempty"`
	CodingAgentConfigVersion int                                   `json:"codingAgentConfigVersion"`
	// PiAgentModels is the PI Agent model inventory.
	PiAgentModels        []PiAgentModelConfig `json:"piAgentModels,omitempty"`
	PiAgentConfigVersion int                  `json:"piAgentConfigVersion"`
	CoreEndpoint         string               `json:"coreEndpoint"`
	MCPEndpoint          string               `json:"mcpEndpoint"`
	// LocalMCPEndpoint is optional so previously persisted settings remain
	// valid; Store.Load applies the product default when it is absent.
	LocalMCPEndpoint string `json:"localMCPEndpoint,omitempty"`
}

// Defaults returns a fresh, valid settings value suitable for local development.
func Defaults() Settings {
	paths, _ := DefaultPaths()
	return DefaultsFor(paths.DefaultDataRoot)
}

// DefaultsFor makes the platform-derived data directory explicit for tests and
// for first launch before any persisted Desktop configuration exists.
func DefaultsFor(dataRoot string) Settings {
	return Settings{
		DataRoot:                 dataRoot,
		CoreBinary:               "devbridge",
		MCPBinary:                "devbridge-mcp",
		LocalMCPBinary:           "devbridge-local-mcp",
		CodexExecutable:          "codex",
		PiExecutable:             "pi",
		PiModel:                  "",
		PiThinkingLevel:          "",
		CodingAgentModels:        codexruntime.Default().ModelConfigs,
		CodingAgentConfigVersion: codingAgentConfigVersion,
		PiAgentConfigVersion:     piAgentConfigVersion,
		CoreEndpoint:             "http://127.0.0.1:8081",
		MCPEndpoint:              "http://127.0.0.1:8080",
		LocalMCPEndpoint:         "http://127.0.0.1:8090",
	}
}

// Validate rejects incomplete settings, malformed endpoints, and values that
// cannot safely be passed to a child process.
func (s Settings) Validate() error {
	if s.CodingAgentConfigVersion != codingAgentConfigVersion {
		return fmt.Errorf("unsupported coding agent config version %d", s.CodingAgentConfigVersion)
	}
	if strings.TrimSpace(s.DataRoot) == "" {
		return errors.New("Dev Bridge data directory is required")
	}
	if strings.ContainsRune(s.DataRoot, '\x00') {
		return errors.New("Dev Bridge data directory contains a NUL byte")
	}
	if !filepath.IsAbs(s.DataRoot) {
		return errors.New("Dev Bridge data directory must be an absolute path")
	}
	for _, field := range []struct{ name, value string }{
		{"core binary", s.CoreBinary},
		{"MCP binary", s.MCPBinary},
		{"Codex executable", s.CodexExecutable},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
		if strings.ContainsRune(field.value, '\x00') {
			return fmt.Errorf("%s contains a NUL byte", field.name)
		}
	}
	// PI executable is optional but must be valid if provided
	if strings.TrimSpace(s.PiExecutable) != "" {
		if strings.ContainsRune(s.PiExecutable, '\x00') {
			return errors.New("PI executable contains a NUL byte")
		}
	}
	// PI model and thinking level are optional
	if strings.ContainsRune(s.PiModel, '\x00') {
		return errors.New("PI model contains a NUL byte")
	}
	if s.PiModel != "" {
		selected := false
		for _, model := range s.PiAgentModels {
			if model.Enabled && model.ModelID == s.PiModel {
				selected = true
				break
			}
		}
		if !selected {
			return errors.New("PI default model must be one of the selected enabled models")
		}
	}
	if strings.ContainsRune(s.PiThinkingLevel, '\x00') {
		return errors.New("PI thinking level contains a NUL byte")
	}
	if err := (codexruntime.Allowlist{ModelConfigs: s.CodingAgentModels}).Validate(); err != nil {
		return fmt.Errorf("invalid Codex runtime inventory: %w", err)
	}
	if err := validateTunnelExecutable(s.OpenAITunnelExecutable); err != nil {
		return err
	}
	if err := validateTunnelID(s.OpenAITunnelID); err != nil {
		return err
	}
	if err := validateEndpoint("Core endpoint", s.CoreEndpoint); err != nil {
		return err
	}
	if err := validateEndpoint("MCP endpoint", s.MCPEndpoint); err != nil {
		return err
	}
	if strings.TrimSpace(s.LocalMCPEndpoint) == "" {
		return nil
	}
	return validateEndpoint("Local MCP endpoint", s.LocalMCPEndpoint)
}

// validateTunnelExecutable accepts an empty value (tunnel not configured) or an
// absolute, NUL-free path. Existence and executability are checked by Desktop
// when the path is selected or launched, not here: a persisted path may point
// at a file that is temporarily absent.
func validateTunnelExecutable(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if strings.ContainsRune(path, '\x00') {
		return errors.New("OpenAI tunnel-client executable contains a NUL byte")
	}
	if !filepath.IsAbs(path) {
		return errors.New("OpenAI tunnel-client executable must be an absolute path")
	}
	return nil
}

// validateTunnelID accepts an empty value (tunnel not configured) or the
// tunnel identifier format the tunnel-client itself enforces.
func validateTunnelID(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	if !regexpTunnelID.MatchString(id) {
		return errors.New("OpenAI tunnel ID must match tunnel_ followed by 32 lowercase letters or digits")
	}
	return nil
}

// TunnelIDPattern is the exact identifier shape the tunnel-client run command
// enforces for --control-plane.tunnel-id.
const TunnelIDPattern = `^tunnel_[0-9a-z]{32}$`

var regexpTunnelID = regexp.MustCompile(TunnelIDPattern)

func validateEndpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s must be an http URL without credentials, query, or fragment", name)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("%s must not include a path", name)
	}
	if u.Port() == "" {
		return fmt.Errorf("%s must include a port", name)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("%s must use a loopback host", name)
	}
	return nil
}

// Paths identifies platform-appropriate, local-only locations used by Desktop.
type Paths struct {
	ConfigFile      string
	StateDir        string
	LogDir          string
	TunnelDir       string
	DefaultDataRoot string
}

// DefaultPaths derives paths from operating-system user directories. It does not
// create any files or directories.
func DefaultPaths() (Paths, error) {
	configBase, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("find user config directory: %w", err)
	}
	// Desktop runtime state is durable local application data, not disposable
	// cache data. Keeping it beside configuration also produces the expected
	// Application Support location on macOS.
	return PathsFor(configBase, configBase), nil
}

// PathsFor is the deterministic form of DefaultPaths, useful for embedding and tests.
func PathsFor(configBase, stateBase string) Paths {
	configDir := filepath.Join(configBase, applicationName)
	stateDir := filepath.Join(stateBase, applicationName)
	return Paths{
		ConfigFile:      filepath.Join(configDir, "settings.json"),
		StateDir:        stateDir,
		LogDir:          filepath.Join(stateDir, "logs"),
		TunnelDir:       filepath.Join(stateDir, "tunnel"),
		DefaultDataRoot: filepath.Join(stateBase, "DevBridge", "data"),
	}
}

// Store persists Settings in a single JSON file.
type Store struct{ paths Paths }

// NewStore returns a store using paths. ConfigFile must be set before Load or Save.
func NewStore(paths Paths) Store { return Store{paths: paths} }

// Paths returns the locations selected for this store.
func (s Store) Paths() Paths { return s.paths }

// Load returns Defaults when no settings file exists. Invalid persisted settings
// are reported rather than silently replaced.
func (s Store) Load() (Settings, error) {
	if s.paths.ConfigFile == "" {
		return Settings{}, errors.New("settings config file path is required")
	}
	b, err := os.ReadFile(s.paths.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultsFor(s.paths.DefaultDataRoot), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(b, &rawFields); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	var value Settings
	if err := json.Unmarshal(b, &value); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	// Settings persisted before the Local MCP adapter became a managed
	// runtime component are upgraded to the product defaults rather than
	// silently leaving the adapter unsupervised.
	if strings.TrimSpace(value.LocalMCPBinary) == "" {
		value.LocalMCPBinary = "devbridge-local-mcp"
	}
	if strings.TrimSpace(value.LocalMCPEndpoint) == "" {
		value.LocalMCPEndpoint = "http://127.0.0.1:8090"
	}
	// Migrate PI Agent configuration for settings persisted before PI support
	if strings.TrimSpace(value.PiExecutable) == "" {
		value.PiExecutable = "pi"
	}
	if value.PiAgentConfigVersion == 0 {
		value.PiAgentConfigVersion = piAgentConfigVersion
	}
	migrated, err := migratePersistedRuntimeConfig(rawFields, &value)
	if err != nil {
		return Settings{}, fmt.Errorf("invalid persisted settings: %w", err)
	}
	if err := value.Validate(); err != nil {
		return Settings{}, fmt.Errorf("invalid persisted settings: %w", err)
	}
	if migrated {
		if err := s.Save(value); err != nil {
			return Settings{}, fmt.Errorf("persist migrated settings: %w", err)
		}
	}
	return value, nil
}

// migratePersistedRuntimeConfig performs the one-way v1 global allowlist to
// v2 per-model migration. Existing enabled models retain their enabled state;
// every current candidate is materialized so disabled entries are preserved.
// "medium" is chosen when legacy supported efforts permit it, otherwise the
// first supported legacy effort is used deterministically.
func migratePersistedRuntimeConfig(rawFields map[string]json.RawMessage, value *Settings) (bool, error) {
	if raw, present := rawFields["codingAgentModels"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false, errors.New("coding agent models must not be null")
		}
		if value.CodingAgentConfigVersion != codingAgentConfigVersion {
			return false, fmt.Errorf("unsupported coding agent config version %d", value.CodingAgentConfigVersion)
		}
		_, legacyModels := rawFields["allowedModels"]
		_, legacyEfforts := rawFields["allowedReasoningEfforts"]
		if legacyModels || legacyEfforts {
			value.AllowedModels = nil
			value.AllowedReasoningEfforts = nil
			return true, nil
		}
		return false, nil
	}
	if raw, present := rawFields["allowedModels"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New("allowed models must not be null")
	}
	if raw, present := rawFields["allowedReasoningEfforts"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New("allowed reasoning efforts must not be null")
	}
	legacy := codexruntime.Default()
	legacy.ModelConfigs = nil
	if _, present := rawFields["allowedModels"]; present {
		legacy.Models = value.AllowedModels
	}
	if _, present := rawFields["allowedReasoningEfforts"]; present {
		legacy.ReasoningEfforts = value.AllowedReasoningEfforts
	}
	normalized, err := legacy.Normalize()
	if err != nil {
		return false, err
	}
	value.CodingAgentModels = normalized.ModelConfigs
	value.CodingAgentConfigVersion = codingAgentConfigVersion
	value.AllowedModels = nil
	value.AllowedReasoningEfforts = nil
	return true, nil
}

// Save validates and atomically replaces the settings file. The file is created
// with owner-only permissions because local paths can be sensitive diagnostics.
func (s Store) Save(value Settings) error {
	if s.paths.ConfigFile == "" {
		return errors.New("settings config file path is required")
	}
	// v2 has one persisted authority. Compatibility fields are never written,
	// even when an older in-process caller supplied contradictory values.
	value.AllowedModels = nil
	value.AllowedReasoningEfforts = nil
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.paths.ConfigFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary settings file: %w", err)
	}
	_, err = tmp.Write(data)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmpName, s.paths.ConfigFile); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return os.Chmod(s.paths.ConfigFile, 0600)
}
