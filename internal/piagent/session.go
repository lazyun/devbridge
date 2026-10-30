package piagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
)

// Session adapts a PI Agent Client to the execution.CodexSession interface.
// Each Session owns one Client subprocess. PI Agent does not have
// explicit threads; each session is effectively one thread.
//
// PI's working directory is fixed at process start (the RPC protocol has no
// cwd command), and the coordinator only learns the run workspace after
// factory creation via StartThread. The subprocess is therefore started
// lazily on the first StartThread/StartTurn call, when cwd is known.
type Session struct {
	factory *Factory
	client  *Client
	cwd     string

	mu       sync.Mutex
	threadID string
	closed   bool
}

// NewSession creates a CodexSession-compatible wrapper around a PI Agent client.
func NewSession(client *Client, cwd string) *Session {
	return &Session{
		client: client,
		cwd:    cwd,
	}
}

// clientLocked returns the running client, starting it lazily with the given
// working directory. Callers must hold s.mu.
func (s *Session) clientLocked(ctx context.Context, cwd string) (*Client, error) {
	if s.closed {
		return nil, fmt.Errorf("pi agent session closed")
	}
	if s.client != nil {
		return s.client, nil
	}
	if cwd == "" {
		cwd = s.cwd
	}
	client, err := Start(ctx, Config{
		PiPath:        s.factory.PiPath,
		CWD:           cwd,
		Model:         s.factory.Model,
		ThinkingLevel: s.factory.ThinkingLevel,
		SessionDir:    s.factory.SessionDir,
		ExtraArgs:     s.factory.ExtraArgs,
	})
	if err != nil {
		return nil, err
	}
	s.client = client
	s.cwd = cwd
	return client, nil
}

// StartThread binds the session to the run workspace (worktree) and starts
// the PI subprocess there. It returns a synthetic thread ID derived from the
// working directory.
func (s *Session) StartThread(ctx context.Context, cwd string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threadID != "" && s.client != nil {
		return s.threadID, nil
	}
	if cwd == "" {
		cwd = s.cwd
	}
	if _, err := s.clientLocked(ctx, cwd); err != nil {
		return "", fmt.Errorf("pi agent start: %w", err)
	}
	// PI Agent doesn't have explicit thread IDs. Use CWD as a stable identifier.
	s.threadID = fmt.Sprintf("pi-%s", cwd)
	return s.threadID, nil
}

// StartTurn sends the prompt to PI Agent and waits for completion.
// It returns an execution.CodexTurnResult with the collected output.
func (s *Session) StartTurn(ctx context.Context, _ string, prompt string) (execution.CodexTurnResult, error) {
	s.mu.Lock()
	client, err := s.clientLocked(ctx, "")
	s.mu.Unlock()
	if err != nil {
		return execution.CodexTurnResult{}, err
	}
	output, err := client.Prompt(ctx, prompt)
	if err != nil {
		return execution.CodexTurnResult{}, fmt.Errorf("pi agent turn: %w", err)
	}
	return execution.CodexTurnResult{
		TurnID:  fmt.Sprintf("turn-%d", client.reqID.Load()),
		Message: output,
	}, nil
}

// StartTurnWithSchema sends a prompt with an output schema requirement.
// PI Agent doesn't natively support JSON schema enforcement, so the schema
// is appended to the prompt as an instruction.
func (s *Session) StartTurnWithSchema(ctx context.Context, threadID, prompt string, schema json.RawMessage) (execution.CodexTurnResult, error) {
	if len(schema) > 0 {
		// Append schema instruction to the prompt.
		prompt = fmt.Sprintf("%s\n\nRespond with JSON matching this schema:\n%s", prompt, string(schema))
	}
	return s.StartTurn(ctx, threadID, prompt)
}

// StartTurnWithSchemaAndStarted combines schema enforcement with a started callback.
func (s *Session) StartTurnWithSchemaAndStarted(ctx context.Context, threadID, prompt string, schema json.RawMessage, onStarted func(string) error) (execution.CodexTurnResult, error) {
	if onStarted != nil {
		_ = onStarted(s.threadID)
	}
	return s.StartTurnWithSchema(ctx, threadID, prompt, schema)
}

// SetThreadName is a no-op for PI Agent (threads are implicit).
func (s *Session) SetThreadName(_ context.Context, _ string, _ string) error {
	return nil
}

// ResumeThread is a no-op for PI Agent (sessions are not resumable across processes).
func (s *Session) ResumeThread(_ context.Context, _ string, _ string, _ *run.TokenCounts) (string, error) {
	return s.threadID, nil
}

// Close shuts down the PI Agent subprocess.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.client == nil {
		return nil
	}
	return s.client.Close()
}

// Factory creates PI Agent sessions that implement execution.CodexFactory.
type Factory struct {
	// PiPath is the resolved path to the pi executable.
	// If empty, Resolve("pi") is used.
	PiPath string
	// Model selects the AI model (empty uses PI's default).
	Model string
	// ThinkingLevel controls reasoning depth.
	ThinkingLevel string
	// SessionDir overrides the default session storage location.
	SessionDir string
	// ExtraArgs are additional CLI arguments.
	ExtraArgs []string
}

// Start creates a new PI Agent session for the given backend config.
// The subprocess starts lazily once the run workspace cwd is known.
func (f *Factory) Start(ctx context.Context, config codexconfig.Config) (execution.CodexSession, error) {
	return f.newSession(), nil
}

// StartWithSelection creates a session with model/reasoning selection.
func (f *Factory) StartWithSelection(ctx context.Context, _ codexconfig.Config, model, reasoningEffort string) (execution.CodexSession, error) {
	factory := *f
	if model != "" {
		factory.Model = model
	}
	if reasoningEffort != "" {
		factory.ThinkingLevel = reasoningEffort
	}
	return factory.newSession(), nil
}

// StartForOperation creates a session bound to a specific operation.
func (f *Factory) StartForOperation(ctx context.Context, _ codexconfig.Config, model, reasoningEffort string, _ codex.AuthorizationCeiling) (execution.CodexSession, error) {
	return f.StartWithSelection(ctx, codexconfig.Config{}, model, reasoningEffort)
}

func (f *Factory) newSession() *Session {
	return &Session{factory: f}
}

// Compile-time interface checks.
var _ execution.CodexSession = (*Session)(nil)
var _ execution.CodexFactory = (*Factory)(nil)
