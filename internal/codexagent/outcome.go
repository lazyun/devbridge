package codexagent

import (
	"errors"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/codex"
)

// TerminalObservation preserves the original protocol identities and cause.
// It also supports legacy injected Codex sessions while Core transitions to the
// neutral error contract. A transport error never yields a terminal observation.
func TerminalObservation(err error) *agent.TerminalError {
	var terminal *codex.TurnError
	if !errors.As(err, &terminal) {
		return nil
	}
	state := agent.TurnTerminal
	switch terminal.Status {
	case "completed":
		state = agent.TurnCompleted
	case "failed":
		state = agent.TurnFailed
	case "interrupted":
		state = agent.TurnInterrupted
	}
	return &agent.TerminalError{ThreadID: terminal.ThreadID, TurnID: terminal.TurnID, State: state, Err: err}
}
