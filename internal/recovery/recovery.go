package recovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/worktree"
)

const InterruptedCode = "bridge_restart_interrupted"

type Store interface {
	ListRuns(context.Context) ([]run.Run, error)
	InterruptRun(context.Context, string, run.Status, time.Time) (bool, error)
	ListWorktrees(context.Context) ([]worktree.Worktree, error)
	MarkWorktreeMissing(context.Context, string, time.Time) (bool, error)
}

type executionPlanStore interface {
	InterruptOrphanedExecutionPlans(context.Context, time.Time) error
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

func (s *Service) Recover(ctx context.Context) (returnErr error) {
	started := time.Now()
	interruptedRuns, missingWorktrees := 0, 0
	observability.Info(ctx, "recovery", "recovery.started", "Bridge recovery scan started", nil)
	defer func() {
		data := map[string]any{"durationMillis": time.Since(started).Milliseconds(), "interruptedRunCount": interruptedRuns, "missingWorktreeCount": missingWorktrees}
		if returnErr != nil {
			data["errorCode"] = "RECOVERY_FAILED"
			data["errorSummary"] = returnErr.Error()
			observability.Error(ctx, "recovery", "recovery.failed", "Bridge recovery scan failed", data)
			return
		}
		observability.Info(ctx, "recovery", "recovery.completed", "Bridge recovery scan completed", data)
	}()
	if s == nil || s.store == nil {
		return errors.New("recovery store is required")
	}
	if agents, ok := s.store.(interface {
		RecoverAgentOperations(context.Context, time.Time) error
	}); ok {
		if err := agents.RecoverAgentOperations(ctx, s.now().UTC()); err != nil {
			return fmt.Errorf("recover Feature Area Agent operations: %w", err)
		}
	}
	runs, err := s.store.ListRuns(ctx)
	if err != nil {
		return fmt.Errorf("list runs for recovery: %w", err)
	}
	for _, value := range runs {
		switch value.Status {
		case run.StatusPreparing, run.StatusRunning, run.StatusWaitingApproval, run.StatusCollecting:
			changed, err := s.store.InterruptRun(ctx, value.ID, value.Status, s.now().UTC())
			if err != nil {
				return fmt.Errorf("interrupt run %s: %w", value.ID, err)
			}
			if changed {
				interruptedRuns++
				logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, TaskID: value.TaskID, ReviewID: value.ReviewID, ExecutionID: value.AgentExecutionID, RunID: value.ID})
				observability.Info(logCtx, "recovery", "recovery.run.interrupted", "orphaned active Run marked interrupted", map[string]any{"previousState": value.Status, "newState": run.StatusInfrastructureFailed, "errorCode": InterruptedCode})
				observability.Info(logCtx, "run", "run.infrastructure_failed", "Run marked infrastructure-failed during recovery", map[string]any{"purpose": value.Purpose, "previousState": value.Status, "newState": run.StatusInfrastructureFailed, "errorCode": InterruptedCode})
			}
		}
	}
	if plans, ok := s.store.(executionPlanStore); ok {
		if err := plans.InterruptOrphanedExecutionPlans(ctx, s.now().UTC()); err != nil {
			return fmt.Errorf("interrupt orphaned execution plans: %w", err)
		}
	}
	if tasks, ok := s.store.(interface {
		RecoverTaskLifecycles(context.Context, time.Time) error
	}); ok {
		if err := tasks.RecoverTaskLifecycles(ctx, s.now().UTC()); err != nil {
			return fmt.Errorf("reconcile Task lifecycles: %w", err)
		}
	}

	worktrees, err := s.store.ListWorktrees(ctx)
	if err != nil {
		return fmt.Errorf("list worktrees for recovery: %w", err)
	}
	for _, value := range worktrees {
		if value.Status == worktree.StatusRemoved || value.Status == worktree.StatusMissing {
			continue
		}
		if _, err := os.Stat(value.Path); errors.Is(err, os.ErrNotExist) {
			changed, err := s.store.MarkWorktreeMissing(ctx, value.ID, s.now().UTC())
			if err != nil {
				return fmt.Errorf("mark worktree %s missing: %w", value.ID, err)
			}
			if changed {
				missingWorktrees++
				observability.Info(ctx, "recovery", "recovery.worktree.missing", "persisted worktree path is missing", map[string]any{"worktreeId": value.ID, "runId": value.RunID})
			}
		} else if err != nil {
			return fmt.Errorf("inspect worktree %s: %w", value.ID, err)
		}
	}
	return nil
}
