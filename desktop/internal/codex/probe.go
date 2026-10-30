// Package codex provides a deliberately narrow local Codex availability probe.
package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	corecodex "github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexlaunch"
)

const (
	// DefaultProbeTimeout bounds a local --version invocation.
	DefaultProbeTimeout = 3 * time.Second
	maxVersionOutput    = 4096
)

// Status is the observed local Codex availability.
type Status string

const (
	Available Status = "available"
	Missing   Status = "missing"
	Error     Status = "error"
)

// Result is safe to display in local Desktop status. Error is diagnostic text,
// rather than the underlying error, so callers need not serialize error values.
type Result struct {
	Executable string `json:"executable,omitempty"`
	Status     Status `json:"status"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Probe verifies the version and production app-server initialization, without starting a turn.
// An empty executable uses the safe PATH lookup name "codex".
func Probe(ctx context.Context, executable string) (result Result) {
	defer func() { result.Executable = executable }()
	if strings.TrimSpace(executable) == "" {
		executable = "codex"
	}
	executable = codexlaunch.Resolve(executable)
	if err := codexlaunch.Validate(executable); err != nil {
		return Result{Status: Missing, Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, executable, "--version") // #nosec G204 -- configured local executable
	// npm/NVM installations expose codex as a script with `#!/usr/bin/env node`.
	// Finder-launched applications do not inherit an interactive shell PATH, so
	// make the selected launcher's directory available to env as well. This lets
	// an absolute .../bin/codex path find its sibling node executable.
	command.Env = codexlaunch.Environment(os.Environ(), executable)
	var output cappedBuffer
	output.limit = maxVersionOutput
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return Result{Status: Missing, Error: "Codex executable was not found"}
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{Status: Error, Error: "Codex version probe timed out"}
		}
		return Result{Status: Error, Error: fmt.Sprintf("Codex version probe failed: %v", err)}
	}
	// Treat an exactly full buffer as overflow too: a process can be terminated
	// by the OS pipe at that boundary before a subsequent Write exposes bytes.
	if output.truncated || output.Len() == maxVersionOutput {
		return Result{Status: Error, Error: "Codex version output exceeded limit"}
	}
	version := strings.TrimSpace(output.String())
	if version == "" {
		return Result{Status: Error, Error: "Codex version probe returned no version"}
	}
	client, err := corecodex.Start(ctx, corecodex.Config{CodexPath: executable})
	if err != nil {
		return Result{Status: Error, Error: fmt.Sprintf("Codex app-server probe failed (%s): %v", executable, err)}
	}
	defer client.Close()
	if client.Executable() != executable {
		return Result{Status: Error, Error: "CODEX_EXECUTABLE_MISMATCH: probe and runtime launch differ"}
	}
	return Result{Status: Available, Version: version}
}

type cappedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.truncated = true
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *cappedBuffer) Len() int       { return len(b.data) }
func (b *cappedBuffer) String() string { return string(b.data) }
