package execution

import (
	"context"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
)

func (s *RunSession) turnStarted(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunID != runID || s.turnReady == nil {
		return
	}
	select {
	case <-s.turnReady:
	default:
		close(s.turnReady)
	}
}

func (s *RunSession) turnFinished(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunID != runID || s.turnTerminal == nil {
		return
	}
	select {
	case <-s.turnTerminal:
	default:
		close(s.turnTerminal)
	}
}

// CancelExecution durably admits an exact request before scheduling its one
// owned backend signal. Disconnecting this caller never cancels that workflow.
func (s *RuntimeSupervisor) CancelExecution(ctx context.Context, in executioncancel.Input) (executioncancel.Receipt, error) {
	in = in.Normalized()
	if s == nil || s.coordinator == nil || s.coordinator.Cancellations == nil {
		return executioncancel.Receipt{}, executioncancel.ErrExecutionNotRunning
	}
	s.mu.Lock()
	session := s.sessions[in.RunID]
	value, dispatch, err := s.coordinator.Cancellations.Begin(ctx, in, s.accepting && session != nil && session.currentRunID() == in.RunID)
	if err != nil || !dispatch {
		s.mu.Unlock()
		return value, err
	}
	s.cancelWG.Add(1)
	// Capture the Run fence and channels before this shared session can advance
	// to Verification. A delayed goroutine must not adopt its successor identity.
	session.mu.RLock()
	ownedCtx, ready, terminal := session.ctx, session.turnReady, session.turnTerminal
	session.mu.RUnlock()
	go func() { defer s.cancelWG.Done(); s.interruptExecution(session, in, ownedCtx, ready, terminal) }()
	s.mu.Unlock()
	return value, nil
}

func (s *RuntimeSupervisor) interruptExecution(session *RunSession, in executioncancel.Input, ownedCtx context.Context, ready, terminal <-chan struct{}) {
	store := s.coordinator.Cancellations
	ctx := context.WithoutCancel(ownedCtx)
	unknown := func() {
		_ = store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN")
		// Close/join the local runtime through its existing failure path. This
		// explicitly does not assert that an orphan backend stopped writing.
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.RunID != in.RunID {
			return
		}
		select {
		case <-terminal:
			return
		default:
		}
		session.cancel(executioncancel.ErrOutcomeUnknown)
	}
	waitCtx, cancelWait := context.WithTimeout(s.rootContext, 30*time.Second)
	defer cancelWait()
	select {
	case <-session.done:
		return
	case <-terminal:
		// The original Turn already has an exact terminal observation. Bind
		// cancellation to that Turn without issuing a redundant interrupt.
		_, _, _ = store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID)
		return
	case <-ready:
	case <-waitCtx.Done():
		unknown()
		return
	}
	session.mu.RLock()
	client, runID := session.client, session.RunID
	session.mu.RUnlock()
	if runID != in.RunID {
		return
	} // never follow a mutable session to verification
	target, dispatch, err := store.ClaimInterrupt(ctx, in.ProjectID, in.RequestID)
	if err != nil {
		// Terminal observation can race this exact dispatch check. Let the
		// existing finalizer finish before classifying a genuinely stranded path.
		if errors.Is(err, executioncancel.ErrRunNotActive) {
			select {
			case <-session.done:
				return
			case <-terminal:
				return
			case <-waitCtx.Done():
			}
		}
		unknown()
		return
	}
	if !dispatch {
		return
	}
	interrupt, ok := client.(interface {
		InterruptTurn(context.Context, string, string) error
	})
	if !ok {
		unknown()
		return
	}
	signalCtx, cancelSignal := context.WithTimeout(s.rootContext, 5*time.Second)
	err = interrupt.InterruptTurn(signalCtx, target.ThreadID, target.TurnID)
	cancelSignal()
	if err != nil {
		// A lost response is never retried. Existing close/join preserves UNKNOWN
		// unless an exact terminal event already won the race.
		unknown()
		return
	} else if err = store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, "ACKNOWLEDGED"); err != nil {
		unknown()
		return
	}
	select {
	case <-session.done:
	case <-terminal:
	case <-waitCtx.Done():
		unknown()
	}
}
