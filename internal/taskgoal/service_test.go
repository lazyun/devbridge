package taskgoal

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/task"
)

type memoryStore struct {
	progress map[string][]Progress
}

func newMemoryStore() *memoryStore {
	return &memoryStore{progress: make(map[string][]Progress)}
}

func (s *memoryStore) InitializeGoalProgress(_ context.Context, executionID, _ string, _ string, now time.Time) error {
	if _, ok := s.progress[executionID]; ok {
		return nil
	}
	s.progress[executionID] = []Progress{
		{ExecutionID: executionID, GoalID: "goal_1", Position: 0, Title: "first", Status: StatusPending, UpdatedAt: now},
		{ExecutionID: executionID, GoalID: "goal_2", Position: 1, Title: "second", Status: StatusPending, UpdatedAt: now},
	}
	return nil
}

func (s *memoryStore) GoalProgress(_ context.Context, executionID string) ([]Progress, error) {
	values, ok := s.progress[executionID]
	if !ok {
		return []Progress{}, nil
	}
	return append([]Progress(nil), values...), nil
}

func (s *memoryStore) UpdateGoalProgress(_ context.Context, executionID, goalID string, status Status, now time.Time) error {
	values, ok := s.progress[executionID]
	if !ok {
		return ErrNotFound
	}
	for index := range values {
		if values[index].GoalID == goalID {
			values[index].Status = status
			values[index].UpdatedAt = now
			return nil
		}
	}
	return ErrNotFound
}

type memoryExecutions struct {
	values map[string]Execution
}

func (s *memoryExecutions) Get(_ context.Context, id string) (Execution, error) {
	value, ok := s.values[id]
	if !ok {
		return Execution{}, errors.New("execution not found")
	}
	return value, nil
}

type memoryTasks struct {
	goals map[string][]task.Goal
}

func (s *memoryTasks) Goals(_ context.Context, taskID string) ([]task.Goal, error) {
	return append([]task.Goal(nil), s.goals[taskID]...), nil
}

func testService(store *memoryStore) *Service {
	executions := &memoryExecutions{values: map[string]Execution{
		"exec-1": {ID: "exec-1", TaskID: "task-1", ProjectID: "project", Status: "CONFIRMED"},
	}}
	tasks := &memoryTasks{goals: map[string][]task.Goal{
		"task-1": {
			{ID: "goal_1", TaskID: "task-1", Position: 0, Title: "first"},
			{ID: "goal_2", TaskID: "task-1", Position: 1, Title: "second"},
		},
	}}
	return NewService(store, executions, tasks, func() time.Time { return time.Unix(10, 0).UTC() })
}

func TestInitializeIsIdempotentAndRequiresGoals(t *testing.T) {
	store := newMemoryStore()
	service := testService(store)
	ctx := context.Background()
	created, err := service.Initialize(ctx, "exec-1")
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	again, err := service.Initialize(ctx, "exec-1")
	if err != nil || again {
		t.Fatalf("second created=%v err=%v", again, err)
	}
	created, err = service.Initialize(ctx, "exec-missing")
	if err == nil {
		t.Fatal("expected missing execution error")
	}
	_ = created
}

func TestInitializeWithoutGoalsCreatesNoProgress(t *testing.T) {
	store := newMemoryStore()
	service := testService(store)
	service.executions = &memoryExecutions{values: map[string]Execution{"exec-2": {ID: "exec-2", TaskID: "task-2", ProjectID: "project"}}}
	created, err := service.Initialize(context.Background(), "exec-2")
	if err != nil || created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	values, err := service.ListForExecution(context.Background(), "exec-2")
	if err != nil || len(values) != 0 {
		t.Fatalf("values=%v err=%v", values, err)
	}
}

func TestReportFollowsClosedTransitions(t *testing.T) {
	store := newMemoryStore()
	service := testService(store)
	ctx := context.Background()
	if _, err := service.Initialize(ctx, "exec-1"); err != nil {
		t.Fatal(err)
	}
	running, err := service.Report(ctx, "exec-1", "goal_1", StatusRunning)
	if err != nil || running.Status != StatusRunning {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	completed, err := service.Report(ctx, "exec-1", "goal_1", StatusCompleted)
	if err != nil || completed.Status != StatusCompleted {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	if _, err := service.Report(ctx, "exec-1", "goal_1", StatusRunning); !errors.As(err, new(*ErrInvalidTransition)) {
		t.Fatalf("terminal re-report err=%v", err)
	}
	if _, err := service.Report(ctx, "exec-1", "goal_1", StatusCompleted); err != nil {
		t.Fatalf("same-status report err=%v", err)
	}
	if _, err := service.Report(ctx, "exec-1", "goal_2", StatusCompleted); err != nil {
		t.Fatalf("pending to completed err=%v", err)
	}
	if _, err := service.Report(ctx, "exec-1", "goal_unknown", StatusRunning); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown goal err=%v", err)
	}
	if _, err := service.Report(ctx, "exec-1", "goal_2", Status("BOGUS")); err == nil {
		t.Fatal("expected invalid status error")
	}
}

func TestListForExecutionOrdersByPosition(t *testing.T) {
	store := newMemoryStore()
	service := testService(store)
	ctx := context.Background()
	if _, err := service.Initialize(ctx, "exec-1"); err != nil {
		t.Fatal(err)
	}
	values, err := service.ListForExecution(ctx, "exec-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Progress{
		{ExecutionID: "exec-1", GoalID: "goal_1", Position: 0, Title: "first", Status: StatusPending, UpdatedAt: time.Unix(10, 0).UTC()},
		{ExecutionID: "exec-1", GoalID: "goal_2", Position: 1, Title: "second", Status: StatusPending, UpdatedAt: time.Unix(10, 0).UTC()},
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values=%+v", values)
	}
}
