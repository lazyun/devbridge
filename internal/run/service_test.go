package run

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/evidence"
)

func TestAttemptsAreSequentialAndConcurrent(t *testing.T) {
	store := NewMemoryStore()
	var mu sync.Mutex
	next := 0
	service := NewService(store, func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		next++
		return fmt.Sprintf("run-%03d", next), nil
	}, nil)
	const count = 50
	attempts := make(chan int, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := service.Create(context.Background(), "task")
			if err != nil {
				errs <- err
				return
			}
			attempts <- value.Attempt
		}()
	}
	wg.Wait()
	close(attempts)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	values := make([]int, 0, count)
	for attempt := range attempts {
		values = append(values, attempt)
	}
	sort.Ints(values)
	for index, attempt := range values {
		if attempt != index+1 {
			t.Fatalf("attempts=%v", values)
		}
	}
	other, err := service.Create(context.Background(), "other")
	if err != nil || other.Attempt != 1 {
		t.Fatalf("other=%+v err=%v", other, err)
	}
}

func TestFocusedScopeReviewMemoryStorePreservesImmutableInitialScope(t *testing.T) {
	service := NewService(NewMemoryStore(), func() (string, error) { return "focused-run", nil }, nil)
	initial := ReviewScope{Kind: ReviewScopePaths, Paths: []string{"internal/primary"}}
	current := ReviewScope{Kind: ReviewScopePaths, Paths: []string{"internal/dependency", "internal/primary"}}
	plan := ReviewPlan{Mode: ReviewModeFocused, InitialScope: initial, SelectedModules: []ReviewModuleSelection{{Key: "primary", Role: ReviewModulePrimary}}}
	value, err := service.CreateFocusedScopeReview(context.Background(), "task", "Review the focused production boundary.", current, plan)
	if err != nil {
		t.Fatal(err)
	}
	if value.ReviewPlan.InitialScope.Kind != initial.Kind || len(value.ReviewPlan.InitialScope.Paths) != 1 || value.ReviewPlan.InitialScope.Paths[0] != initial.Paths[0] {
		t.Fatalf("focused Review initial scope = %+v, want %+v", value.ReviewPlan.InitialScope, initial)
	}
}

func TestStrictProgressionAndSuccess(t *testing.T) {
	now := time.Unix(0, 0)
	service := NewService(NewMemoryStore(), func() (string, error) { return "run", nil }, func() time.Time { now = now.Add(time.Second); return now })
	value, err := service.Create(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	value, err = service.Transition(context.Background(), value.ID, StatusPreparing)
	if err != nil || value.StartedAt != nil || value.FinishedAt != nil {
		t.Fatalf("prepare=%+v err=%v", value, err)
	}
	if _, err = service.AttachWorktree(context.Background(), value.ID, "wt"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.AttachThread(context.Background(), value.ID, "thread"); err != nil {
		t.Fatal(err)
	}
	value, err = service.Transition(context.Background(), value.ID, StatusRunning)
	if err != nil || value.StartedAt == nil || value.FinishedAt != nil {
		t.Fatal(err)
	}
	value, err = service.Transition(context.Background(), value.ID, StatusCollecting)
	if err != nil || value.StartedAt == nil || value.FinishedAt != nil {
		t.Fatal(err)
	}
	result := Result{AgentClaim: evidence.AgentClaim{Message: "done"}}
	value, err = service.Succeed(context.Background(), value.ID, result)
	if err != nil || value.Status != StatusSucceeded || value.FinishedAt == nil || value.Result == nil || value.TokenUsage.Status != TokenUsageUnavailable {
		t.Fatalf("success=%+v err=%v", value, err)
	}
	_, err = service.Fail(context.Background(), value.ID, Failure{Kind: FailureTask})
	var transition *TransitionError
	if !errors.As(err, &transition) {
		t.Fatalf("terminal mutation=%v", err)
	}
}

func TestWaitingApprovalIsNonTerminalAndCanResumeOrFail(t *testing.T) {
	for _, test := range []struct {
		name       string
		failure    *Failure
		wantStatus Status
	}{
		{name: "resume", wantStatus: StatusRunning},
		{name: "infrastructure failure", failure: &Failure{Kind: FailureInfrastructure, Code: "approval_delivery_failed"}, wantStatus: StatusInfrastructureFailed},
		{name: "cancel", failure: &Failure{Kind: FailureCanceled, Code: "human_approval_canceled"}, wantStatus: StatusCanceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(NewMemoryStore(), func() (string, error) { return "waiting-" + test.name, nil }, nil)
			value, _ := service.Create(context.Background(), "task")
			value, _ = service.Transition(context.Background(), value.ID, StatusPreparing)
			value, _ = service.Transition(context.Background(), value.ID, StatusRunning)
			started := value.StartedAt
			value, err := service.Transition(context.Background(), value.ID, StatusWaitingApproval)
			if err != nil || value.FinishedAt != nil || value.TokenUsage.Status != TokenUsagePending || value.StartedAt == nil || !value.StartedAt.Equal(*started) {
				t.Fatalf("waiting=%+v err=%v", value, err)
			}
			if test.failure == nil {
				value, err = service.Transition(context.Background(), value.ID, StatusRunning)
			} else {
				value, err = service.Fail(context.Background(), value.ID, *test.failure)
			}
			if err != nil || value.Status != test.wantStatus {
				t.Fatalf("final=%+v err=%v", value, err)
			}
			if test.failure == nil && value.FinishedAt != nil {
				t.Fatalf("resumed run was finalized: %+v", value)
			}
			if test.failure != nil && value.FinishedAt == nil {
				t.Fatalf("terminal run has no FinishedAt: %+v", value)
			}
		})
	}
}

func TestTokenUsageUsesCumulativeDeltaAndRejectsDecreases(t *testing.T) {
	service := NewService(NewMemoryStore(), func() (string, error) { return "run", nil }, nil)
	value, _ := service.Create(context.Background(), "task")
	value, _ = service.Transition(context.Background(), value.ID, StatusPreparing)
	value, _ = service.Transition(context.Background(), value.ID, StatusRunning)
	value, _ = service.Transition(context.Background(), value.ID, StatusCollecting)
	window := int64(200000)
	observation := &TokenUsageObservation{Reliable: true, StartCumulative: TokenCounts{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 10, ReasoningOutputTokens: 5, TotalTokens: 110}, EndCumulative: TokenCounts{InputTokens: 140, CachedInputTokens: 30, OutputTokens: 25, ReasoningOutputTokens: 9, TotalTokens: 165}, ModelContextWindow: &window}
	value, err := service.SucceedWithUsage(context.Background(), value.ID, Result{}, observation)
	if err != nil || value.TokenUsage.Status != TokenUsageAvailable || value.TokenUsage.Usage == nil {
		t.Fatalf("usage=%+v err=%v", value.TokenUsage, err)
	}
	if value.TokenUsage.Usage.TotalTokens != 55 || value.TokenUsage.Usage.InputTokens != 40 || value.TokenUsage.ModelContextWindow == nil || value.TokenUsage.Usage.TotalTokens == window {
		t.Fatalf("usage delta=%+v", value.TokenUsage)
	}

	service = NewService(NewMemoryStore(), func() (string, error) { return "decreasing", nil }, nil)
	value, _ = service.Create(context.Background(), "task")
	decreasing := &TokenUsageObservation{Reliable: true, StartCumulative: TokenCounts{TotalTokens: 10}, EndCumulative: TokenCounts{TotalTokens: 9}}
	value, err = service.FailWithUsage(context.Background(), value.ID, Failure{Kind: FailureInfrastructure}, decreasing)
	if err != nil || value.TokenUsage.Status != TokenUsageUnavailable || value.TokenUsage.Usage != nil {
		t.Fatalf("decreasing usage=%+v err=%v", value.TokenUsage, err)
	}
}

func TestInvalidTransitionsAndFailureKinds(t *testing.T) {
	kinds := []struct {
		kind   FailureKind
		status Status
	}{{FailureTask, StatusFailed}, {FailureInfrastructure, StatusInfrastructureFailed}, {FailureCanceled, StatusCanceled}}
	for i, test := range kinds {
		service := NewService(NewMemoryStore(), func() (string, error) { return fmt.Sprintf("run-%d", i), nil }, nil)
		value, _ := service.Create(context.Background(), "task")
		if _, err := service.Transition(context.Background(), value.ID, StatusRunning); err == nil {
			t.Fatal("skipped PREPARING")
		}
		value, err := service.Fail(context.Background(), value.ID, Failure{Kind: test.kind, Stage: "test", Message: "failure"})
		if err != nil || value.Status != test.status || value.Failure == nil || value.FinishedAt == nil || value.TokenUsage.Status != TokenUsageUnavailable {
			t.Fatalf("failure=%+v err=%v", value, err)
		}
	}
}
