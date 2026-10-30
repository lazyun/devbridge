package execution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/run"
)

type dispositionRuns struct {
	RunService
	mu    sync.Mutex
	value run.Run
}

func (s *dispositionRuns) Get(context.Context, string) (run.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.value
	snapshot := *v.AgentRuntime
	snapshot.Requests = append([]agentruntime.Request(nil), snapshot.Requests...)
	v.AgentRuntime = &snapshot
	return v, nil
}

func TestResponseDispositionGateRequiresExactReceipt(t *testing.T) {
	for _, observation := range []string{"active", "idle", "completed", "structured-result"} {
		t.Run(observation, func(t *testing.T) {
			runs := &dispositionRuns{value: run.Run{ID: "run", Status: run.StatusRunning, AgentRuntime: &agentruntime.Snapshot{TurnStatus: observation, Requests: []agentruntime.Request{{Kind: "CODEX_REQUEST", State: "RESPONDING", Delivery: "UNKNOWN", UserDecision: &agentruntime.UserDecision{UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND"}}}}}}}
			coordinator := &Coordinator{Runs: runs}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { _, err := coordinator.awaitResponseDisposition(ctx, "run"); done <- err }()
			select {
			case err := <-done:
				t.Fatalf("observation released gate: %v", err)
			case <-time.After(70 * time.Millisecond):
			}
			cancel()
			if err := <-done; !errors.Is(err, agentruntime.ErrResponseDispositionUnknown) {
				t.Fatal(err)
			}
			// Only an exact receipt, represented by the authoritative consumed
			// projection, permits the already observed result to continue.
			runs.mu.Lock()
			runs.value.AgentRuntime.Requests[0].Delivery = "CONSUMED"
			runs.value.AgentRuntime.Requests[0].State = "RESPONDED"
			runs.mu.Unlock()
			if canceled, err := coordinator.awaitResponseDisposition(context.Background(), "run"); err != nil || canceled {
				t.Fatalf("resolved=%v %v", canceled, err)
			}
		})
	}
}
