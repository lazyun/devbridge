package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/codex/wire"
)

var (
	ErrAppServerExited = errors.New("codex app server exited")
	ErrClientClosed    = errors.New("codex client closed")
)

type RPCError struct {
	Code    int64
	Message string
	Data    json.RawMessage
}

func (e *RPCError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("JSON-RPC error %d", e.Code)
	}
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

type ProtocolError struct {
	Message string
	Input   string
}

func (e *ProtocolError) Error() string {
	if e.Input == "" {
		return "app-server protocol error: " + e.Message
	}
	return fmt.Sprintf("app-server protocol error: %s: %q", e.Message, e.Input)
}

type TurnError struct {
	Failure  *wire.TurnFailure
	ThreadID string
	TurnID   string
	Status   string
	Message  string
}

type TurnLifecycleOverflowError struct {
	ThreadID string
	Limit    int
}

func (e *TurnLifecycleOverflowError) Error() string {
	return fmt.Sprintf("turn/start for thread %s exceeded the pre-response lifecycle limit of %d turns", e.ThreadID, e.Limit)
}

func (e *TurnError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("codex turn %s (%s) %s: %s", e.TurnID, e.ThreadID, e.Status, e.Message)
	}
	return fmt.Sprintf("codex turn %s (%s) ended with status %s", e.TurnID, e.ThreadID, e.Status)
}
