package codexlaunch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const SelectionEnv = "DEVBRIDGE_CODEX_SELECTION_FILE"

// Selection is an atomic, durable Desktop probe result. Core reads it anew for
// each launch, so a live Settings change only affects subsequent runtimes.
type Selection struct {
	Configured string `json:"configured"`
	Executable string `json:"executable"`
	Error      string `json:"error,omitempty"`
}

func Read(file string) (Selection, error) {
	var value Selection
	data, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	if err != nil {
		return value, fmt.Errorf("CODEX_SELECTION_UNAVAILABLE: %q: %w", file, err)
	}
	return value, nil
}

func Write(file string, value Selection) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".codex-selection-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}

// Runtime preserves per-config overrides. Only the default empty/bare codex
// choice inherits Desktop's pinned selection. A selection failure never falls
// through to PATH, even if another Codex installation becomes available.
func Runtime(configured string) (Selection, error) {
	configured = strings.TrimSpace(configured)
	value := Selection{Configured: configured}
	if file := os.Getenv(SelectionEnv); file != "" && (configured == "" || configured == "codex") {
		var err error
		value, err = Read(file)
		if err != nil {
			return value, err
		}
		if value.Error != "" {
			return value, fmt.Errorf("CODEX_PROBE_FAILED: %s", value.Error)
		}
	} else {
		value.Executable = Resolve(configured)
	}
	return value, Validate(value.Executable)
}
