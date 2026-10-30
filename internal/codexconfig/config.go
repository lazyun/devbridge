package codexconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

const DefaultConfigID = "codex_config_default"

// Config is a Bridge-managed Codex launch choice. CodexPath is the existing
// gateway's stable reference to the concrete Codex launcher executable.
type Config struct {
	ID        string
	Name      string
	CodexPath string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BackendSelection is the explicit backend identity selected for one
// confirmation. It is a transport fact, not an authorization grant.
type BackendSelection struct {
	ConfigID          string `json:"configId"`
	ConfigFingerprint string `json:"configFingerprint"`
	ConfigName        string `json:"configName"`
	Source            string `json:"source"`
}

// BackendSelectionInput is the closed client-supplied identity of one backend.
// Display metadata and authority provenance are resolved and frozen by Core.
type BackendSelectionInput struct {
	ConfigID          string `json:"configId"`
	ConfigFingerprint string `json:"configFingerprint"`
}

func (s BackendSelection) Input() BackendSelectionInput {
	return BackendSelectionInput{ConfigID: s.ConfigID, ConfigFingerprint: s.ConfigFingerprint}
}

// Fingerprint is stable across inventory timestamps and changes only when
// the concrete configured backend identity changes.
func (c Config) Fingerprint() string {
	h := sha256.Sum256([]byte(c.ID + "\x00" + c.Name + "\x00" + c.CodexPath))
	return hex.EncodeToString(h[:])
}

func (c Config) Selection(source string) BackendSelection {
	return BackendSelection{ConfigID: c.ID, ConfigFingerprint: c.Fingerprint(), ConfigName: c.Name, Source: source}
}
