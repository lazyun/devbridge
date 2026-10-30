package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/run"
)

type unreadableRuntimeRuns struct {
	RunService
	failure error
}

func (s *unreadableRuntimeRuns) Get(context.Context, string) (run.Run, error) {
	// Match run.Service.Get's intentionally tolerant display fallback.
	return run.Run{ID: "run", AgentRuntime: &agentruntime.Snapshot{Availability: "UNAVAILABLE", CaptureCoverage: agentruntime.Coverage{State: "UNAVAILABLE", Reasons: []string{"RUNTIME_READ_FAILED"}}}}, nil
}
func (s *unreadableRuntimeRuns) GetForRuntimeAuthority(context.Context, string) (run.Run, error) {
	return run.Run{}, s.failure
}
func TestResponseDispositionGateDoesNotUseUnavailableDisplay(t *testing.T) {
	failure := errors.New("request ledger unreadable")
	c := &Coordinator{Runs: &unreadableRuntimeRuns{failure: failure}}
	if canceled, err := c.awaitResponseDisposition(context.Background(), "run"); canceled || !errors.Is(err, failure) {
		t.Fatalf("unreadable requests released disposition gate: canceled=%v err=%v", canceled, err)
	}
}

type displayOnlyRuntimeRuns struct{ RunService }

func (*displayOnlyRuntimeRuns) Get(ctx context.Context, id string) (run.Run, error) {
	return (&unreadableRuntimeRuns{}).Get(ctx, id)
}

func TestResponseDispositionGateRejectsFailedLegacyDisplay(t *testing.T) {
	c := &Coordinator{Runs: &displayOnlyRuntimeRuns{}}
	if canceled, err := c.awaitResponseDisposition(context.Background(), "run"); canceled || err == nil {
		t.Fatalf("failed legacy display released gate: canceled=%v err=%v", canceled, err)
	}
}
