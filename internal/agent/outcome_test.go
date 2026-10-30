package agent_test

import (
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agent"
)

func TestTerminalErrorMatchesOnlyExactCompleteTerminalIdentity(t *testing.T) {
	for _, state := range []agent.TurnState{agent.TurnCompleted, agent.TurnFailed, agent.TurnInterrupted, agent.TurnTerminal} {
		terminal := &agent.TerminalError{ThreadID: "thread", TurnID: "turn", State: state}
		if !terminal.Matches("thread", "turn") {
			t.Fatalf("valid terminal state %q did not match", state)
		}
		for _, target := range [][2]string{{"other-thread", "turn"}, {"thread", "other-turn"}, {"", "turn"}, {"thread", ""}, {"", ""}, {" thread", "turn"}, {"thread", "turn "}} {
			if terminal.Matches(target[0], target[1]) {
				t.Fatalf("state %q accepted wrong identity %+v", state, target)
			}
		}
	}
	for _, terminal := range []*agent.TerminalError{nil, {}, {ThreadID: "thread", State: agent.TurnCompleted}, {TurnID: "turn", State: agent.TurnCompleted}, {ThreadID: "thread", TurnID: "turn"}, {ThreadID: "thread", TurnID: "turn", State: "RUNNING"}, {ThreadID: "thread", TurnID: "turn", State: "UNKNOWN"}} {
		if terminal.Matches("thread", "turn") {
			t.Fatalf("incomplete/nonterminal observation matched: %+v", terminal)
		}
	}
}

func TestTerminalErrorPreservesCauseAndHasCauseFreeDescription(t *testing.T) {
	cause := errors.New("native failure")
	terminal := &agent.TerminalError{ThreadID: "thread", TurnID: "turn", State: agent.TurnFailed, Err: cause}
	if terminal.Error() != cause.Error() || terminal.Unwrap() != cause || !errors.Is(terminal, cause) {
		t.Fatalf("native cause changed: %v", terminal)
	}
	terminal.Err = nil
	if terminal.Error() != "agent turn ended: FAILED" || terminal.Unwrap() != nil {
		t.Fatalf("cause-free terminal=%v", terminal)
	}
}
