package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/run"
)

type independentBlockingSession struct{ *blockingCodexSession }

func (*independentBlockingSession) independentlyOwned() bool { return true }

type independentBlockingFactory struct{ session *independentBlockingSession }

func (f independentBlockingFactory) Start(context.Context, codexconfig.Config) (CodexSession, error) {
	return f.session, nil
}

func TestRuntimeSupervisorIndependentAppServerShutdownPreservesRun(t *testing.T) {
	f := newRuntimeFixture(t, "task-a")
	backend := &independentBlockingSession{newBlockingCodexSession(1)}
	f.coordinator.Codex = independentBlockingFactory{backend}
	started, err := f.supervisor.StartRun(context.Background(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("turn did not begin")
	}
	live, ok := f.supervisor.GetSession(started.ID)
	if !ok {
		t.Fatal("missing live owner")
	}
	if err = f.supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
	result, err := live.Wait(context.Background())
	if !errors.Is(err, ErrAppServerDetached) || result.Status != run.StatusRunning {
		t.Fatalf("detach result=%+v err=%v", result, err)
	}
	persisted, err := f.runs.Get(context.Background(), started.ID)
	if err != nil || persisted.Status != run.StatusRunning || persisted.Failure != nil {
		t.Fatalf("detach terminalized Run=%+v err=%v", persisted, err)
	}
	if backend.closed.Load() != 1 {
		t.Fatalf("client detach count=%d", backend.closed.Load())
	}
}

func TestRuntimeSupervisorIndependentTransportLossPreservesRun(t *testing.T) {
	f := newRuntimeFixture(t, "task-a")
	backend := &independentBlockingSession{newBlockingCodexSession(1)}
	backend.turnErr = errors.New("websocket connection lost before terminal acknowledgement")
	f.coordinator.Codex = independentBlockingFactory{backend}
	started, err := f.supervisor.StartRun(context.Background(), "task-a")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("turn did not begin")
	}
	live, ok := f.supervisor.GetSession(started.ID)
	if !ok {
		t.Fatal("missing live owner")
	}
	close(backend.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := live.Wait(ctx)
	if !errors.Is(err, ErrAppServerDetached) || result.Status != run.StatusRunning {
		t.Fatalf("transport loss result=%+v err=%v", result, err)
	}
	persisted, err := f.runs.Get(context.Background(), started.ID)
	if err != nil || persisted.Status != run.StatusRunning || persisted.Failure != nil {
		t.Fatalf("transport loss terminalized Run=%+v err=%v", persisted, err)
	}
	if err = f.supervisor.Shutdown(); err != nil {
		t.Fatal(err)
	}
}
