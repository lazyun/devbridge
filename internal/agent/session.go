// Package agent defines Core's backend-independent execution session boundary.
// It describes observations and explicit capabilities, not a sandbox or proof of
// agent trust. Adapters must honor the already frozen execution inputs; they may
// not infer new authority from conversation text or a successful transport call.
package agent

import (
	"context"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepermission"
)

// Session is the minimum transport contract. Optional capabilities below are
// checked separately: implementing Session does not imply resume, structured
// output, interruption, runtime binding, or recovery support.
type Session interface {
	StartThread(context.Context, string) (string, error)
	StartTurn(context.Context, string, string) (TurnResult, error)
	Close() error
}

// TurnResult keeps the agent's claim separate from observed command evidence.
// Missing usage is unknown, not zero. Durable history and receipts remain owned
// by Core; a result cannot prove that an earlier user response was consumed.
type TurnResult struct {
	TurnID     string
	Message    string
	Commands   []run.ObservedCommand
	TokenUsage *run.TokenUsageObservation
}

type ThreadNamer interface {
	SetThreadName(context.Context, string, string) error
}

type ThreadResumer interface {
	ResumeThread(context.Context, string, string, *run.TokenCounts) (string, error)
}

type TurnStarter interface {
	StartTurnWithStarted(context.Context, string, string, func(string) error) (TurnResult, error)
}

type StructuredTurnStarter interface {
	StartTurnWithSchemaAndStarted(context.Context, string, string, json.RawMessage, func(string) error) (TurnResult, error)
}

type StructuredSession interface {
	StartTurnWithSchema(context.Context, string, string, json.RawMessage) (TurnResult, error)
}

type TurnInterrupter interface {
	InterruptTurn(context.Context, string, string) error
}

// RuntimeBinding is the explicit, frozen filesystem context of this operation.
// Passing it is not an authorization grant or a claim of host-level isolation.
type RuntimeBinding struct {
	RuntimeWorkspaceRoots []string
	WritableRoots         []string
	TaskPermissions       *runtimepermission.TaskPermissions `json:",omitempty"`
}

type RuntimeBinder interface {
	SetRuntimeBinding(RuntimeBinding)
}

type UsageRunBinder interface {
	SetUsageRunID(string)
}

// ToolSpec describes a Core-owned tool exposed to the agent. Transport-specific
// tool calls and approval delivery remain adapter concerns.
type ToolSpec struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	DeferLoading bool            `json:"deferLoading,omitempty"`
}

type ToolConfigurer interface {
	SetDynamicTools([]ToolSpec)
}

// ExecutionError marks a terminal executor outcome, not infrastructure failure.
// The wrapped protocol error is retained for exact classification and recovery.
type ExecutionError struct{ Err error }

func (e *ExecutionError) Error() string { return "task execution failed: " + e.Err.Error() }
func (e *ExecutionError) Unwrap() error { return e.Err }
