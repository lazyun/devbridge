package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

type delayedInterruptSession struct {
	CodexSession
	entered, release chan struct{}
}

func (c delayedInterruptSession) InterruptTurn(context.Context, string, string) error {
	close(c.entered)
	<-c.release
	return errors.New("late lost response")
}

type fencingCancellationStore struct {
	executioncancel.Store
	target executioncancel.Target
}

func (s fencingCancellationStore) ClaimInterrupt(context.Context, string, string) (executioncancel.Target, bool, error) {
	return s.target, true, nil
}
func (s fencingCancellationStore) RecordInterrupt(context.Context, string, string, string) error {
	return nil
}

func TestLateCancellationOutcomeNeverCancelsSuccessorRun(t *testing.T) {
	root, cancelRoot := context.WithCancelCause(context.Background())
	defer cancelRoot(nil)
	ctx, cancel := context.WithCancelCause(root)
	defer cancel(nil)
	ctx = runtimeepoch.WithFence(ctx, "epoch-exact", "implementation")
	ready, terminal := make(chan struct{}), make(chan struct{})
	close(ready)
	client := delayedInterruptSession{entered: make(chan struct{}), release: make(chan struct{})}
	session := &RunSession{RunID: "implementation", ctx: ctx, cancel: cancel, done: make(chan struct{}), turnReady: ready, turnTerminal: terminal, client: client}
	supervisor := &RuntimeSupervisor{rootContext: root, coordinator: &Coordinator{Cancellations: fencingCancellationStore{target: executioncancel.Target{RunID: "implementation", RuntimeEpoch: "epoch-exact", ThreadID: "thread", TurnID: "turn-implementation"}}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervisor.interruptExecution(session, executioncancel.Input{RunID: "implementation"}, ctx, ready, terminal)
	}()
	select {
	case <-client.entered:
	case <-time.After(time.Second):
		t.Fatal("interrupt did not start")
	}
	// Normal exact completion is observed before the serial supervisor transfers
	// its in-memory ownership. The old RPC deliberately remains outstanding.
	session.turnFinished("implementation")
	session.setRuntimeFence("epoch-exact", "verification")
	close(client.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old cancellation did not join")
	}
	if session.context().Err() != nil {
		t.Fatal("late Implementation cancellation canceled successor Verification")
	}
	fence, _ := runtimeepoch.FromContext(session.context())
	if fence.RunID != "verification" {
		t.Fatalf("successor fence changed: %+v", fence)
	}
}

func TestLateCancellationOutcomeCannotCancelObservedNormalTerminal(t *testing.T) {
	root, cancelRoot := context.WithCancelCause(context.Background())
	defer cancelRoot(nil)
	ctx, cancel := context.WithCancelCause(root)
	defer cancel(nil)
	ready, terminal := make(chan struct{}), make(chan struct{})
	close(ready)
	client := delayedInterruptSession{entered: make(chan struct{}), release: make(chan struct{})}
	session := &RunSession{RunID: "implementation", ctx: ctx, cancel: cancel, done: make(chan struct{}), turnReady: ready, turnTerminal: terminal, client: client}
	supervisor := &RuntimeSupervisor{rootContext: root, coordinator: &Coordinator{Cancellations: fencingCancellationStore{target: executioncancel.Target{RunID: "implementation", ThreadID: "thread", TurnID: "turn"}}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		supervisor.interruptExecution(session, executioncancel.Input{RunID: "implementation"}, ctx, ready, terminal)
	}()
	<-client.entered
	session.turnFinished("implementation")
	close(client.release)
	<-done
	if session.context().Err() != nil {
		t.Fatal("late interrupt error poisoned normal terminal accounting")
	}
}
