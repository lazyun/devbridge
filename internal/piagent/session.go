package piagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
)

// Session owns one ephemeral Pi process. Its observed session identity is not a
// durable Core conversation or a filesystem authorization boundary.
type Session struct {
	factory                 *Factory
	client                  *Client
	cwd                     string
	mu                      sync.Mutex
	turnMu                  sync.Mutex
	threadID                string
	activeTurnID            string
	closed                  bool
	policy                  *agentpolicy.Envelope
	launchConfig            *Factory
	launchConfigFingerprint string
}

func NewSession(client *Client, cwd string) *Session { return &Session{client: client, cwd: cwd} }

func (s *Session) clientLocked(ctx context.Context, cwd string) (*Client, error) {
	if s.closed {
		return nil, errors.New("pi session closed")
	}
	if s.client != nil {
		return s.client, nil
	}
	if s.factory == nil {
		return nil, errors.New("pi session has no client or factory")
	}
	client, err := Start(ctx, Config{PiPath: s.factory.PiPath, CWD: cwd, Model: s.factory.Model,
		ThinkingLevel: s.factory.ThinkingLevel, SessionDir: s.factory.SessionDir, ExtraArgs: s.factory.ExtraArgs, policy: s.policy})
	if err != nil {
		return nil, err
	}
	s.client, s.cwd = client, cwd
	return client, nil
}

// StartThread verifies Pi's actual session ID without making a model call. A
// different cwd cannot silently reuse a process started in the old workspace.
func (s *Session) StartThread(ctx context.Context, cwd string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("pi session closed")
	}
	if cwd == "" {
		cwd = s.cwd
	}
	if cwd == "" {
		return "", errors.New("pi working directory is required")
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	if s.client != nil && s.cwd != "" {
		bound, err := filepath.Abs(s.cwd)
		if err != nil || bound != cwd {
			return "", errors.New("pi working directory is immutable")
		}
	}
	if s.client != nil && s.client.cwd != cwd {
		return "", errors.New("pi client working directory differs from requested workspace")
	}
	if s.threadID != "" {
		return s.threadID, nil
	}
	client, err := s.clientLocked(ctx, cwd)
	if err != nil {
		return "", fmt.Errorf("pi start: %w", err)
	}
	identity, err := s.verifyState(ctx, client)
	if err != nil {
		return "", err
	}
	s.cwd, s.threadID = cwd, identity
	return s.threadID, nil
}

func (s *Session) verifyState(ctx context.Context, client *Client) (string, error) {
	if err := s.verifyLaunchConfiguration(); err != nil {
		return "", err
	}
	if err := client.policyInput.verify(); err != nil {
		return "", err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := client.GetState(handshakeCtx)
	if err != nil {
		return "", err
	}
	var state struct {
		SessionID string `json:"sessionId"`
		Model     *struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
		ThinkingLevel       string `json:"thinkingLevel"`
		IsStreaming         bool   `json:"isStreaming"`
		IsCompacting        bool   `json:"isCompacting"`
		PendingMessageCount int    `json:"pendingMessageCount"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || strings.TrimSpace(state.SessionID) == "" || strings.TrimSpace(state.SessionID) != state.SessionID {
		return "", fmt.Errorf("%w: missing exact session identity", ErrProtocol)
	}
	if state.IsStreaming || state.IsCompacting || state.PendingMessageCount != 0 {
		return "", errors.New("pi session is not idle")
	}
	if s.factory != nil {
		if selected := s.factory.Model; selected != "" && (state.Model == nil || selected != state.Model.ID && selected != state.Model.Provider+"/"+state.Model.ID) {
			return "", errors.New("pi selected model differs from frozen selection")
		}
		if selected := s.factory.ThinkingLevel; selected != "" && selected != state.ThinkingLevel {
			return "", errors.New("pi thinking level differs from frozen selection")
		}
	}
	return state.SessionID, nil
}

func (s *Session) StartTurn(ctx context.Context, threadID, prompt string) (agent.TurnResult, error) {
	return s.StartTurnWithStarted(ctx, threadID, prompt, nil)
}

// StartTurnWithStarted reports the accepted RPC prompt identity, not a thread
// ID. Callback failure remains a failure and does not trigger another prompt.
func (s *Session) StartTurnWithStarted(ctx context.Context, threadID, prompt string, onStarted func(string) error) (agent.TurnResult, error) {
	if !s.turnMu.TryLock() {
		return agent.TurnResult{}, errors.New("pi turn already active")
	}
	defer s.turnMu.Unlock()
	s.mu.Lock()
	if s.closed || s.client == nil || s.threadID == "" || threadID != s.threadID {
		s.mu.Unlock()
		return agent.TurnResult{}, errors.New("pi turn requires the exact active session")
	}
	client := s.client
	s.mu.Unlock()
	identity, err := s.verifyState(ctx, client)
	if err != nil {
		return agent.TurnResult{}, err
	}
	if identity != threadID {
		return agent.TurnResult{}, errors.New("pi runtime session identity changed")
	}
	defer func() { s.mu.Lock(); s.activeTurnID = ""; s.mu.Unlock() }()
	started := func(id string) error {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return errors.New("pi session closed during prompt admission")
		}
		s.activeTurnID = id
		s.mu.Unlock()
		if onStarted != nil {
			return onStarted(id)
		}
		return nil
	}
	observed, err := client.prompt(ctx, prompt, started)
	result := agent.TurnResult{TurnID: observed.TurnID, Message: observed.Message}
	if err != nil && observed.State != "" {
		return result, &agent.ExecutionError{Err: &agent.TerminalError{ThreadID: threadID, TurnID: result.TurnID, State: observed.State, Err: err}}
	}
	return result, err
}

// Pi currently has no native schema contract, durable resume, Core host-tool
// dispatcher, authorization ceiling, reliable usage accounting, or same-turn
// recovery here. Those optional interfaces are intentionally not implemented.
func (s *Session) AgentDescriptor() agent.Descriptor {
	d := descriptor()
	if s.launchConfig != nil {
		d.ConfigFingerprint = s.launchConfig.configurationFingerprint()
	} else if s.factory != nil {
		d.ConfigFingerprint = s.factory.configurationFingerprint()
	}
	return d
}

// InterruptTurn acknowledges only delivery of an interruption request. The
// active prompt must separately observe agent_settled before Core can claim a
// terminal outcome. Clearing queued work prevents abort from starting a followup.
func (s *Session) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	s.mu.Lock()
	if s.closed || threadID == "" || threadID != s.threadID || turnID == "" || turnID != s.activeTurnID || s.client == nil {
		s.mu.Unlock()
		return errors.New("pi interrupt requires the exact active turn")
	}
	client := s.client
	// Keep the exact active identity locked until both requests are delivered:
	// otherwise a settled prompt could yield to a new one between clear/abort.
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := client.send(ctx, rpcCommand{Type: "clear_queue"})
	if err != nil {
		return err
	}
	if !response.Success {
		return fmt.Errorf("pi clear queue: %s", response.Error)
	}
	return client.Abort(ctx)
}

func (s *Session) Close() error {
	s.mu.Lock()
	s.closed = true
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Close()
}

type Factory struct {
	PiPath        string
	Model         string
	ThinkingLevel string
	SessionDir    string
	ExtraArgs     []string
}

func descriptor() agent.Descriptor {
	return agent.Descriptor{ID: "pi", Capabilities: []agent.Capability{agent.CapabilityCooperativePolicy, agent.CapabilityTurnStarted, agent.CapabilityInterrupt}}
}
func (f *Factory) AgentDescriptor() agent.Descriptor {
	d := descriptor()
	d.ConfigFingerprint = f.configurationFingerprint()
	return d
}
func (f *Factory) Start(_ context.Context, _ codexconfig.Config) (execution.CodexSession, error) {
	return f.newSession(), nil
}
func (f *Factory) StartWithSelection(_ context.Context, _ codexconfig.Config, model, reasoningEffort string) (execution.CodexSession, error) {
	factory := *f
	if model != "" {
		factory.Model = model
	}
	if reasoningEffort != "" {
		factory.ThinkingLevel = reasoningEffort
	}
	return factory.newSession(), nil
}

// CWD is not a sandbox. Do not discard a read-only or workspace-write ceiling
// simply because the Pi subprocess can be launched.
func (f *Factory) StartForOperation(ctx context.Context, config codexconfig.Config, model, reasoningEffort string, ceiling codex.AuthorizationCeiling) (execution.CodexSession, error) {
	if ceiling != "" {
		return nil, &agent.CapabilityError{AgentID: "pi", Missing: []agent.Capability{agent.CapabilityAuthorizationCeiling}}
	}
	return f.StartWithSelection(ctx, config, model, reasoningEffort)
}
func (f *Factory) newSession() *Session {
	factory := *f
	factory.ExtraArgs = append([]string(nil), f.ExtraArgs...)
	return &Session{factory: &factory}
}

var (
	_ agent.Session          = (*Session)(nil)
	_ agent.TurnStarter      = (*Session)(nil)
	_ agent.TurnInterrupter  = (*Session)(nil)
	_ agent.Described        = (*Session)(nil)
	_ agent.Described        = (*Factory)(nil)
	_ execution.CodexFactory = (*Factory)(nil)
)
