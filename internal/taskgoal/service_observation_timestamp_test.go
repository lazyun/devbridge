package taskgoal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/task"
)

type observationTimestampUpdate struct {
	executionID, goalID string
	status              Status
	at                  time.Time
}

// This fake only observes Report's storage boundary; it does not model a real
// store's operation authorization or execute any workflow initialization.
type observationTimestampStore struct {
	value     Progress
	updates   []observationTimestampUpdate
	updateErr error
}

func (*observationTimestampStore) InitializeGoalProgress(context.Context, string, string, string, time.Time) error {
	panic("Report must not initialize progress")
}

func (s *observationTimestampStore) GoalProgress(context.Context, string) ([]Progress, error) {
	return []Progress{s.value}, nil
}

func (s *observationTimestampStore) UpdateGoalProgress(_ context.Context, executionID, goalID string, status Status, at time.Time) error {
	s.updates = append(s.updates, observationTimestampUpdate{executionID, goalID, status, at})
	if s.updateErr != nil {
		return s.updateErr
	}
	s.value.Status, s.value.UpdatedAt = status, at
	return nil
}

type observationTimestampUnusedLookups struct{}

func (observationTimestampUnusedLookups) Get(context.Context, string) (Execution, error) {
	panic("Report must not look up executions")
}

func (observationTimestampUnusedLookups) Goals(context.Context, string) ([]task.Goal, error) {
	panic("Report must not look up task goals")
}

func observationTimestampFixture(from Status) Progress {
	return Progress{
		ExecutionID: "synthetic-execution", GoalID: "synthetic-goal", ProjectID: "synthetic-project",
		Position: 2, Title: "Synthetic timestamp observation", Status: from,
		UpdatedAt: time.Date(2026, 11, 3, 7, 8, 9, 123456789, time.FixedZone("stored", -7*60*60)),
	}
}

func observationTimestampService(store *observationTimestampStore, now func() time.Time) *Service {
	unused := observationTimestampUnusedLookups{}
	return NewService(store, unused, unused, now)
}

func checkObservationTimestampUpdate(t *testing.T, store *observationTimestampStore, before Progress, status Status, at time.Time) {
	t.Helper()
	want := observationTimestampUpdate{before.ExecutionID, before.GoalID, status, at}
	if len(store.updates) != 1 || store.updates[0] != want {
		t.Fatalf("store calls = %+v, want exactly one %+v", store.updates, want)
	}
}

func TestReportObservationTimestampMatchesStore(t *testing.T) {
	transitions := []struct{ from, to Status }{
		{StatusPending, StatusRunning},
		{StatusPending, StatusCompleted},
		{StatusPending, StatusFailed},
		{StatusPending, StatusSkipped},
		{StatusRunning, StatusCompleted},
		{StatusRunning, StatusFailed},
		{StatusRunning, StatusSkipped},
	}
	for _, transition := range transitions {
		t.Run(string(transition.from)+"_to_"+string(transition.to), func(t *testing.T) {
			before := observationTimestampFixture(transition.from)
			store := &observationTimestampStore{value: before}
			first := time.Date(2026, 11, 4, 10, 11, 12, 987654321, time.FixedZone("clock", 330*60))
			clockCalls := 0
			service := observationTimestampService(store, func() time.Time {
				at := first.Add(time.Duration(clockCalls) * time.Second)
				clockCalls++
				return at
			})

			got, err := service.Report(context.Background(), before.ExecutionID, before.GoalID, transition.to)
			if err != nil {
				t.Fatal(err)
			}
			checkObservationTimestampUpdate(t, store, before, transition.to, first.UTC())
			if got.UpdatedAt != store.updates[0].at {
				t.Errorf("returned UpdatedAt %s differs from timestamp passed to store %s", got.UpdatedAt.Format(time.RFC3339Nano), store.updates[0].at.Format(time.RFC3339Nano))
			}
			want := before
			want.Status, want.UpdatedAt = transition.to, first.UTC()
			if got != want || store.value != want {
				t.Errorf("returned = %+v, captured persisted value = %+v, want %+v", got, store.value, want)
			}
			if clockCalls != 1 {
				t.Errorf("clock calls = %d, want 1", clockCalls)
			}
		})
	}
}

func TestReportObservationTimestampSameStatus(t *testing.T) {
	for _, status := range []Status{StatusRunning, StatusCompleted, StatusFailed, StatusSkipped} {
		t.Run(string(status), func(t *testing.T) {
			before := observationTimestampFixture(status)
			store := &observationTimestampStore{value: before}
			service := observationTimestampService(store, func() time.Time {
				t.Fatal("same-status observation must preserve the stored timestamp without reading the clock")
				return time.Time{}
			})

			got, err := service.Report(context.Background(), before.ExecutionID, before.GoalID, status)
			if err != nil {
				t.Fatal(err)
			}
			checkObservationTimestampUpdate(t, store, before, status, before.UpdatedAt)
			if got != before || store.value != before {
				t.Fatalf("same-status result = %+v, captured persisted value = %+v, want unchanged %+v", got, store.value, before)
			}
		})
	}
}

func TestReportObservationTimestampStoreError(t *testing.T) {
	for _, status := range []Status{StatusRunning, StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			before := observationTimestampFixture(StatusRunning)
			storeErr := errors.New("synthetic update failure")
			store := &observationTimestampStore{value: before, updateErr: storeErr}
			first := time.Date(2026, 11, 4, 10, 11, 12, 987654321, time.FixedZone("clock", 330*60))
			clockCalls := 0
			service := observationTimestampService(store, func() time.Time {
				clockCalls++
				return first
			})

			got, err := service.Report(context.Background(), before.ExecutionID, before.GoalID, status)
			if err != storeErr || got != (Progress{}) {
				t.Fatalf("result = %+v, error = %v; want zero Progress and original store error", got, err)
			}
			wantTime, wantClockCalls := first.UTC(), 1
			if status == before.Status {
				wantTime, wantClockCalls = before.UpdatedAt, 0
			}
			checkObservationTimestampUpdate(t, store, before, status, wantTime)
			if store.value != before || clockCalls != wantClockCalls {
				t.Fatalf("captured persisted value = %+v, clock calls = %d; want unchanged %+v and %d calls", store.value, clockCalls, before, wantClockCalls)
			}
		})
	}
}

func TestReportObservationTimestampRejectsInvalidTransition(t *testing.T) {
	for _, from := range []Status{StatusCompleted, StatusFailed, StatusSkipped} {
		t.Run(string(from), func(t *testing.T) {
			before := observationTimestampFixture(from)
			store := &observationTimestampStore{value: before}
			service := observationTimestampService(store, func() time.Time {
				t.Fatal("invalid transition must not read the clock")
				return time.Time{}
			})

			got, err := service.Report(context.Background(), before.ExecutionID, before.GoalID, StatusRunning)
			var invalid *ErrInvalidTransition
			if !errors.As(err, &invalid) || invalid.From != from || invalid.To != StatusRunning || got != (Progress{}) {
				t.Fatalf("result = %+v, error = %v; want zero Progress and unchanged invalid-transition behavior", got, err)
			}
			if len(store.updates) != 0 || store.value != before {
				t.Fatalf("invalid transition changed store: %+v", store)
			}
		})
	}
}
