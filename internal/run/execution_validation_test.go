package run

import (
	"context"
	"errors"
	"testing"
)

type validatingRunStore struct {
	*MemoryStore
	value Run
	err   error
}

func (s *validatingRunStore) ValidateRunExecution(_ context.Context, value Run) error {
	s.value = value
	return s.err
}

func TestExecutionValidationRequiresDurableCapability(t *testing.T) {
	if err := NewService(NewMemoryStore(), nil, nil).ValidateRunExecution(context.Background(), Run{ID: "run", AgentExecutionID: "execution"}); err == nil {
		t.Fatal("memory Run association alone authorized backend work")
	}
	var service *Service
	if err := service.ValidateRunExecution(context.Background(), Run{}); err == nil {
		t.Fatal("nil service authorized work")
	}
	if err := NewService(NewMemoryStore(), nil, nil).ValidateRunExecutionDispatch(context.Background(), Run{ID: "run", AgentExecutionID: "execution"}); err == nil {
		t.Fatal("memory association alone authorized backend dispatch")
	}
	if err := service.ValidateRunExecutionDispatch(context.Background(), Run{}); err == nil {
		t.Fatal("nil service authorized dispatch")
	}
}

func TestExecutionValidationPreservesExactRunAndFailure(t *testing.T) {
	want := errors.New("stale durable ownership")
	store := &validatingRunStore{MemoryStore: NewMemoryStore(), err: want}
	value := Run{ID: "run", TaskID: "task", AgentExecutionID: "execution", RuntimeEpoch: "epoch"}
	if err := NewService(store, nil, nil).ValidateRunExecution(context.Background(), value); !errors.Is(err, want) || !store.value.SameOwner(value) {
		t.Fatalf("validation lost exact owner or failure: %+v %v", store.value, err)
	}
}
