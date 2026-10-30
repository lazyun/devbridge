// Package codexagent adapts the existing Codex client to Core's agent session
// boundary. It neither chooses a backend nor grants authority or starts models.
package codexagent

import (
	"context"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/run"
)

// Client is the protocol seam, implemented by *codex.Client. It also lets tests
// exercise exact translation without launching a process or contacting a model.
type Client interface {
	StartThread(context.Context, codex.ThreadOptions) (codex.Thread, error)
	ResumeThread(context.Context, codex.ThreadResumeOptions) (codex.Thread, error)
	SetThreadName(context.Context, string, string) error
	StartTurn(context.Context, codex.TurnOptions) (codex.TurnResult, error)
	RejoinTurn(context.Context, codex.TurnRejoinOptions) (codex.TurnResult, error)
	Close() error
	InterruptTurn(context.Context, string, string) error
	NextApproval(context.Context) (codex.ApprovalRequest, error)
	ResolveApproval(context.Context, string, codex.ApprovalDecision) error
	Executable() string
}

type Options struct {
	Model                string
	ReasoningEffort      string
	ServiceTier          string
	AuthorizationCeiling codex.AuthorizationCeiling
}

type Session struct {
	client               Client
	usageRunID           string
	dynamicTools         []codex.DynamicToolSpec
	model                string
	reasoningEffort      string
	serviceTier          string
	authorizationCeiling codex.AuthorizationCeiling
	runtimeBinding       codex.RuntimeBinding
}

func NewSession(client Client, options Options) *Session {
	return &Session{client: client, model: options.Model, reasoningEffort: options.ReasoningEffort, serviceTier: options.ServiceTier, authorizationCeiling: options.AuthorizationCeiling}
}

func (s *Session) SetRuntimeBinding(value agent.RuntimeBinding) {
	s.runtimeBinding = codex.RuntimeBinding(value)
}
func (s *Session) SetUsageRunID(id string) { s.usageRunID = id }
func (s *Session) SetDynamicTools(specs []agent.ToolSpec) {
	s.dynamicTools = nil
	for _, spec := range specs {
		s.dynamicTools = append(s.dynamicTools, codex.DynamicToolSpec(spec))
	}
}
func (s Session) Executable() string { return s.client.Executable() }

func (s Session) StartThread(ctx context.Context, cwd string) (string, error) {
	thread, err := s.client.StartThread(ctx, codex.ThreadOptions{CWD: cwd, DynamicTools: s.dynamicTools, Model: s.model, ReasoningEffort: s.reasoningEffort, ServiceTier: s.serviceTier, RuntimeBinding: s.runtimeBinding})
	return thread.ID, err
}
func (s Session) ResumeThread(ctx context.Context, threadID, cwd string, baseline *run.TokenCounts) (string, error) {
	var codexBaseline *codex.TokenCounts
	if baseline != nil {
		codexBaseline = &codex.TokenCounts{InputTokens: baseline.InputTokens, CachedInputTokens: baseline.CachedInputTokens, CacheWriteInputTokens: baseline.CacheWriteInputTokens, OutputTokens: baseline.OutputTokens, ReasoningOutputTokens: baseline.ReasoningOutputTokens, TotalTokens: baseline.TotalTokens}
	}
	thread, err := s.client.ResumeThread(ctx, codex.ThreadResumeOptions{ThreadID: threadID, CWD: cwd, UsageBaseline: codexBaseline, DynamicTools: s.dynamicTools, Model: s.model, ReasoningEffort: s.reasoningEffort, ServiceTier: s.serviceTier, RuntimeBinding: s.runtimeBinding})
	return thread.ID, err
}
func (s Session) SetThreadName(ctx context.Context, threadID, name string) error {
	return s.client.SetThreadName(ctx, threadID, name)
}
func (s Session) StartTurn(ctx context.Context, threadID, prompt string) (agent.TurnResult, error) {
	return s.StartTurnWithStarted(ctx, threadID, prompt, nil)
}
func (s Session) StartTurnWithSchema(ctx context.Context, threadID, prompt string, outputSchema json.RawMessage) (agent.TurnResult, error) {
	return s.StartTurnWithSchemaAndStarted(ctx, threadID, prompt, outputSchema, nil)
}
func (s Session) StartTurnWithSchemaAndStarted(ctx context.Context, threadID, prompt string, outputSchema json.RawMessage, onStarted func(string) error) (agent.TurnResult, error) {
	return s.startTurn(ctx, threadID, prompt, outputSchema, onStarted)
}
func (s Session) StartTurnWithStarted(ctx context.Context, threadID, prompt string, onStarted func(string) error) (agent.TurnResult, error) {
	return s.startTurn(ctx, threadID, prompt, nil, onStarted)
}
func (s Session) startTurn(ctx context.Context, threadID, prompt string, outputSchema json.RawMessage, onStarted func(string) error) (agent.TurnResult, error) {
	var tier *string
	if s.serviceTier != "" {
		tier = &s.serviceTier
	}
	result, err := s.client.StartTurn(ctx, codex.TurnOptions{RunID: s.usageRunID, ThreadID: threadID, Prompt: prompt, OutputSchema: outputSchema, Model: s.model, ReasoningEffort: s.reasoningEffort, ServiceTier: tier, AuthorizationCeiling: s.authorizationCeiling, RuntimeBinding: s.runtimeBinding, OnStarted: func(_ string, turnID string) error {
		if onStarted == nil {
			return nil
		}
		return onStarted(turnID)
	}})
	value := agent.TurnResult{TurnID: result.TurnID, Message: result.FinalMessage, Commands: make([]run.ObservedCommand, 0, len(result.Commands))}
	for _, command := range result.Commands {
		value.Commands = append(value.Commands, run.ObservedCommand{
			ItemID: command.ItemID, Command: command.Command, CWD: command.CWD, Status: command.Status, ExitCode: command.ExitCode,
			Actions: append([]string(nil), command.Actions...), Output: command.Output, OutputTruncated: command.OutputTruncated, DurationMillis: command.DurationMS,
			StartedAt: command.StartedAt, FinishedAt: command.FinishedAt,
		})
	}
	if result.TokenUsage != nil {
		value.TokenUsage = &run.TokenUsageObservation{
			Attribution:        &result.TokenUsage.Attribution,
			StartCumulative:    run.TokenCounts{InputTokens: result.TokenUsage.StartCumulative.InputTokens, CachedInputTokens: result.TokenUsage.StartCumulative.CachedInputTokens, CacheWriteInputTokens: result.TokenUsage.StartCumulative.CacheWriteInputTokens, OutputTokens: result.TokenUsage.StartCumulative.OutputTokens, ReasoningOutputTokens: result.TokenUsage.StartCumulative.ReasoningOutputTokens, TotalTokens: result.TokenUsage.StartCumulative.TotalTokens},
			EndCumulative:      run.TokenCounts{InputTokens: result.TokenUsage.EndCumulative.InputTokens, CachedInputTokens: result.TokenUsage.EndCumulative.CachedInputTokens, CacheWriteInputTokens: result.TokenUsage.EndCumulative.CacheWriteInputTokens, OutputTokens: result.TokenUsage.EndCumulative.OutputTokens, ReasoningOutputTokens: result.TokenUsage.EndCumulative.ReasoningOutputTokens, TotalTokens: result.TokenUsage.EndCumulative.TotalTokens},
			ModelContextWindow: result.TokenUsage.ModelContextWindow,
			Reliable:           result.TokenUsage.Reliable,
		}
	}
	if terminal := TerminalObservation(err); terminal != nil {
		return value, &agent.ExecutionError{Err: terminal}
	}
	return value, err
}
func (s Session) Close() error { return s.client.Close() }
func (s Session) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	return s.client.InterruptTurn(ctx, threadID, turnID)
}
func (s Session) NextApproval(ctx context.Context) (codex.ApprovalRequest, error) {
	return s.client.NextApproval(ctx)
}
func (s Session) ResolveApproval(ctx context.Context, id string, decision codex.ApprovalDecision) error {
	return s.client.ResolveApproval(ctx, id, decision)
}

func (s Session) RejoinTurn(ctx context.Context, threadID, turnID string, runtime *agentruntime.Session) (agent.TurnResult, error) {
	var tier *string
	if s.serviceTier != "" {
		tier = &s.serviceTier
	}
	result, err := s.client.RejoinTurn(ctx, codex.TurnRejoinOptions{TurnOptions: codex.TurnOptions{RunID: s.usageRunID, ThreadID: threadID, Model: s.model, ReasoningEffort: s.reasoningEffort, ServiceTier: tier, AuthorizationCeiling: s.authorizationCeiling, RuntimeBinding: s.runtimeBinding}, TurnID: turnID, RuntimeSession: runtime, DynamicTools: s.dynamicTools})
	value := agent.TurnResult{TurnID: result.TurnID, Message: result.FinalMessage, Commands: make([]run.ObservedCommand, 0, len(result.Commands))}
	for _, command := range result.Commands {
		value.Commands = append(value.Commands, run.ObservedCommand{ItemID: command.ItemID, Command: command.Command, CWD: command.CWD, Status: command.Status, ExitCode: command.ExitCode, Actions: append([]string(nil), command.Actions...), Output: command.Output, OutputTruncated: command.OutputTruncated, DurationMillis: command.DurationMS, StartedAt: command.StartedAt, FinishedAt: command.FinishedAt})
	}
	if terminal := TerminalObservation(err); terminal != nil {
		err = &agent.ExecutionError{Err: terminal}
	}
	return value, err
}

var (
	_ agent.Session               = (*Session)(nil)
	_ agent.ThreadNamer           = (*Session)(nil)
	_ agent.ThreadResumer         = (*Session)(nil)
	_ agent.TurnStarter           = (*Session)(nil)
	_ agent.StructuredSession     = (*Session)(nil)
	_ agent.StructuredTurnStarter = (*Session)(nil)
	_ agent.TurnInterrupter       = (*Session)(nil)
	_ agent.RuntimeBinder         = (*Session)(nil)
	_ agent.UsageRunBinder        = (*Session)(nil)
	_ agent.ToolConfigurer        = (*Session)(nil)
	_ Client                      = (*codex.Client)(nil)
)
