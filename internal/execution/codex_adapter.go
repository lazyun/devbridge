package execution

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/agent"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexagent"
	"github.com/lazyun/devbridge/internal/codexconfig"
)

type DefaultCodexFactory struct {
	ContinuityDirectory string
	AppServers          AppServerContinuityStore
	AppServerSpec       func(codexconfig.Config) appserver.Spec

	Config             codex.Config
	DynamicToolHandler codex.DynamicToolHandler
}

// runtimeSelectionAwareCodexFactory carries the durable non-semantic runtime
// snapshot without changing the established model-aware test seam.
type runtimeSelectionAwareCodexFactory interface {
	StartForRuntimeSelection(context.Context, codexconfig.Config, string, string, string, codex.AuthorizationCeiling) (CodexSession, error)
}

func (f DefaultCodexFactory) StartForRuntimeSelection(ctx context.Context, selected codexconfig.Config, model, reasoningEffort, serviceTier string, ceiling codex.AuthorizationCeiling) (CodexSession, error) {
	config := codexLaunchConfig(f.Config, selected)
	config.ApprovalMode = codex.ApprovalModeInteractive
	config.AuthorizationCeiling = ceiling
	config.DynamicToolHandler = f.DynamicToolHandler
	client, err := codex.Start(ctx, config)
	if err != nil {
		return nil, err
	}
	return &codexSession{Session: codexagent.NewSession(client, codexagent.Options{Model: model, ReasoningEffort: reasoningEffort, ServiceTier: serviceTier, AuthorizationCeiling: ceiling})}, nil
}

func (f DefaultCodexFactory) Start(ctx context.Context, selected codexconfig.Config) (CodexSession, error) {
	return f.StartForOperation(ctx, selected, "", "", codex.AuthorizationWorkspaceWrite)
}

// StartForOperation binds the immutable authorization ceiling before the App
// Server begins handling approvals. The same ceiling is rebound on every Turn.
func (f DefaultCodexFactory) StartForOperation(ctx context.Context, selected codexconfig.Config, model, reasoningEffort string, ceiling codex.AuthorizationCeiling) (CodexSession, error) {
	config := codexLaunchConfig(f.Config, selected)
	config.ApprovalMode = codex.ApprovalModeInteractive
	config.AuthorizationCeiling = ceiling
	config.DynamicToolHandler = f.DynamicToolHandler
	client, err := codex.Start(ctx, config)
	if err != nil {
		return nil, err
	}
	return &codexSession{Session: codexagent.NewSession(client, codexagent.Options{Model: model, ReasoningEffort: reasoningEffort, AuthorizationCeiling: ceiling})}, nil
}

// StartWithSelection starts a Codex App Server with this operation's requested
// model and reasoning effort. The selection applies to each Turn, including
// Turns that resume an existing FeatureArea conversation.
func (f DefaultCodexFactory) StartWithSelection(ctx context.Context, selected codexconfig.Config, model, reasoningEffort string) (CodexSession, error) {
	return f.StartForOperation(ctx, selected, model, reasoningEffort, codex.AuthorizationWorkspaceWrite)
}

func codexLaunchConfig(base codex.Config, selected codexconfig.Config) codex.Config {
	config := base
	if selected.CodexPath != "" {
		config.CodexPath = selected.CodexPath
	}
	return config
}

// codexSession preserves the existing App Server ownership/recovery seam. The
// protocol-to-agent session translation lives in codexagent; durable lifecycle
// ownership remains here until the launch/recovery boundary is extracted.
type codexSession struct {
	*codexagent.Session
	idleCleanup       func(context.Context) error
	appServerInstance appserver.Binding
	independent       bool
}

// Neutral adapters supply TerminalError directly. The fallback retains old
// test/custom Codex sessions without requiring Core to interpret native states.
func terminalAgentObservation(err error) *agent.TerminalError {
	var terminal *agent.TerminalError
	if errors.As(err, &terminal) {
		return terminal
	}
	return codexagent.TerminalObservation(err)
}
