package codexagent_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexagent"
)

func TestTerminalObservationNormalizesStatesWithoutLosingNativeCause(t *testing.T) {
	for _, tc := range []struct {
		status string
		state  agent.TurnState
	}{{"completed", agent.TurnCompleted}, {"failed", agent.TurnFailed}, {"interrupted", agent.TurnInterrupted}, {"other-terminal", agent.TurnTerminal}} {
		t.Run(tc.status, func(t *testing.T) {
			native := &codex.TurnError{ThreadID: "exact-thread", TurnID: "exact-turn", Status: tc.status, Message: "native details"}
			wrapped := fmt.Errorf("protocol context: %w", native)
			observed := codexagent.TerminalObservation(wrapped)
			if observed == nil || observed.State != tc.state || observed.ThreadID != native.ThreadID || observed.TurnID != native.TurnID || observed.Err != wrapped || observed.Error() != wrapped.Error() || !errors.Is(observed, wrapped) || !errors.Is(observed, native) {
				t.Fatalf("observation=%+v", observed)
			}
			var found *codex.TurnError
			if !errors.As(observed, &found) || found != native {
				t.Fatal("original native error lost")
			}
		})
	}
}

func TestTerminalObservationNeverInfersTerminalFromTransportFailure(t *testing.T) {
	for _, err := range []error{nil, context.Canceled, context.DeadlineExceeded, codex.ErrClientClosed, codex.ErrAppServerExited, &codex.ProtocolError{Message: "terminal-looking text: completed"}, fmt.Errorf("wrapped: %w", codex.ErrAppServerExited), &agent.ExecutionError{Err: errors.New("failed")}} {
		if observed := codexagent.TerminalObservation(err); observed != nil {
			t.Fatalf("transport error %v became terminal: %+v", err, observed)
		}
	}
}

func TestSessionNormalizesTerminalErrorsForStartAndRejoin(t *testing.T) {
	for _, operation := range []string{"start", "rejoin"} {
		t.Run(operation, func(t *testing.T) {
			native := &codex.TurnError{ThreadID: "exact-thread", TurnID: "exact-turn", Status: "interrupted"}
			wrapped := fmt.Errorf("wire context: %w", native)
			f := &fakeClient{result: codex.TurnResult{TurnID: "exact-turn"}, err: wrapped}
			s := codexagent.NewSession(f, codexagent.Options{})
			var err error
			if operation == "start" {
				_, err = s.StartTurn(context.Background(), "exact-thread", "prompt")
			} else {
				_, err = s.RejoinTurn(context.Background(), "exact-thread", "exact-turn", nil)
			}
			var execution *agent.ExecutionError
			var terminal *agent.TerminalError
			var original *codex.TurnError
			if !errors.As(err, &execution) || !errors.As(err, &terminal) || !errors.As(err, &original) || !errors.Is(err, wrapped) || original != native || terminal.ThreadID != "exact-thread" || terminal.TurnID != "exact-turn" || terminal.State != agent.TurnInterrupted {
				t.Fatalf("terminal chain changed: %v; %+v", err, terminal)
			}
		})
	}
}
