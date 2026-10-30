package agent

// TurnState is an observed execution state, not analysis, review acceptance, or
// evidence that a user response was consumed. Transport loss is not terminal.
type TurnState string

const (
	TurnCompleted   TurnState = "COMPLETED"
	TurnFailed      TurnState = "FAILED"
	TurnInterrupted TurnState = "INTERRUPTED"
	TurnTerminal    TurnState = "TERMINAL"
)

// TerminalError carries the exact observed interaction alongside its native
// cause. Core must match both identities before treating it as a safe boundary.
type TerminalError struct {
	ThreadID string
	TurnID   string
	State    TurnState
	Err      error
}

func (e *TerminalError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "agent turn ended: " + string(e.State)
}
func (e *TerminalError) Unwrap() error { return e.Err }

// Matches accepts only a known terminal state for the exact nonempty identity.
// It cannot establish response delivery, authorization, or user acceptance.
func (e *TerminalError) Matches(threadID, turnID string) bool {
	if e == nil || threadID == "" || turnID == "" || e.ThreadID != threadID || e.TurnID != turnID {
		return false
	}
	switch e.State {
	case TurnCompleted, TurnFailed, TurnInterrupted, TurnTerminal:
		return true
	default:
		return false
	}
}
