// Package piagent provides a deliberately narrow local PI Agent availability probe.
package piagent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/piagent"
)

const (
	// DefaultProbeTimeout bounds a local --version invocation.
	DefaultProbeTimeout = 3 * time.Second
	maxVersionOutput    = 4096
)

// Status is the observed local PI Agent availability.
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

// Probe verifies the version and basic functionality, without starting a session.
// An empty executable uses the safe PATH lookup name "pi".
func Probe(ctx context.Context, executable string) (result Result) {
	defer func() { result.Executable = executable }()
	if strings.TrimSpace(executable) == "" {
		executable = "pi"
	}
	executable = piagent.Resolve(executable)

	ctx, cancel := context.WithTimeout(ctx, DefaultProbeTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, executable, "--version") // #nosec G204 -- configured local executable
	var output cappedBuffer
	output.limit = maxVersionOutput
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	if errors.Is(err, exec.ErrNotFound) {
		return Result{Status: Missing, Error: "PI Agent executable was not found"}
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{Status: Error, Error: "PI Agent version probe timed out"}
		}
		return Result{Status: Error, Error: fmt.Sprintf("PI Agent version probe failed: %v", err)}
	}
	// Treat an exactly full buffer as overflow too
	if output.truncated || output.Len() == maxVersionOutput {
		return Result{Status: Error, Error: "PI Agent version output exceeded limit"}
	}
	version := strings.TrimSpace(output.String())
	if version == "" {
		return Result{Status: Error, Error: "PI Agent version probe returned no version"}
	}
	return Result{Status: Available, Version: version}
}

// Model represents a PI Agent model configuration.
type Model struct {
	Provider                  string   `json:"provider"`
	ModelID                   string   `json:"modelId"`
	ContextWindow             string   `json:"contextWindow,omitempty"`
	MaxOutput                 string   `json:"maxOutput,omitempty"`
	SupportsThinking          bool     `json:"supportsThinking"`
	SupportsImages            bool     `json:"supportsImages"`
	Enabled                   bool     `json:"enabled"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts,omitempty"`
	DefaultReasoningEffort    string   `json:"defaultReasoningEffort,omitempty"`
}

// ListModels reads available models from PI Agent.
func ListModels(ctx context.Context, executable string) ([]Model, error) {
	if strings.TrimSpace(executable) == "" {
		executable = "pi"
	}
	executable = piagent.Resolve(executable)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, executable, "--list-models") // #nosec G204
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list PI Agent models: %w", err)
	}

	return parseModels(string(output))
}

// parseModels parses the tabular output from `pi --list-models`.
func parseModels(output string) ([]Model, error) {
	var models []Model
	scanner := bufio.NewScanner(strings.NewReader(output))

	// Skip header line
	if !scanner.Scan() {
		return nil, errors.New("empty model list")
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Parse tab-separated values
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}

		provider := fields[0]
		modelID := fields[1]
		contextWindow := fields[2]
		maxOutput := fields[3]
		supportsThinking := strings.ToLower(fields[4]) == "yes"
		supportsImages := strings.ToLower(fields[5]) == "yes"

		// Build full model ID
		fullModelID := modelID
		if provider != "" && !strings.Contains(modelID, "/") {
			fullModelID = provider + "/" + modelID
		}

		// Determine supported reasoning efforts
		supportedEfforts := []string{"low", "medium", "high"}
		if supportsThinking {
			supportedEfforts = append(supportedEfforts, "xhigh")
		}

		models = append(models, Model{
			Provider:                  provider,
			ModelID:                   fullModelID,
			ContextWindow:             contextWindow,
			MaxOutput:                 maxOutput,
			SupportsThinking:          supportsThinking,
			SupportsImages:            supportsImages,
			Enabled:                   true, // Enable all models by default
			SupportedReasoningEfforts: supportedEfforts,
			DefaultReasoningEffort:    "medium",
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to parse model list: %w", err)
	}

	return models, nil
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
