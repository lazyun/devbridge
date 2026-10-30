package execution

import (
	"github.com/lazyun/devbridge/internal/executioncancel"
	"testing"
)

type availabilityStore struct{ executioncancel.Store }

func TestCancellationAvailabilityRequiresExactLiveOwnedRun(t *testing.T) {
	s := &RuntimeSupervisor{accepting: true, coordinator: &Coordinator{Cancellations: availabilityStore{}}, sessions: map[string]*RunSession{"implementation": {RunID: "implementation"}}}
	if !s.ExecutionCancellationAvailable("implementation") || s.ExecutionCancellationAvailable("verification") {
		t.Fatal("incorrect live run eligibility")
	}
	s.sessions["implementation"].RunID = "verification"
	if s.ExecutionCancellationAvailable("implementation") {
		t.Fatal("old lookup adopted successor run")
	}
	s.sessions["implementation"].RunID = "implementation"
	s.accepting = false
	if s.ExecutionCancellationAvailable("implementation") {
		t.Fatal("stopped runtime admitted cancellation")
	}
	if (*RuntimeSupervisor)(nil).ExecutionCancellationAvailable("implementation") {
		t.Fatal("missing runtime admitted cancellation")
	}
}
