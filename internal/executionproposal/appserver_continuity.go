package executionproposal

import (
	"context"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

type appServerPlanRestorer interface {
	RecoverAppServerPlan(context.Context, string, appserver.Binding, task.VerificationSpec, func(context.Context) (run.Run, error), func(context.Context, Outcome, string, string) error) (run.Run, error)
}

// RecoverAppServerExecution restores the original authorized plan's callbacks;
// it cannot consume another authorization or allocate a replacement first Run.
func (s *Service) RecoverAppServerExecution(ctx context.Context, projectID, executionID, runID string, binding appserver.Binding) (run.Run, error) {
	value, err := s.GetForProject(ctx, projectID, executionID)
	if err != nil {
		return run.Run{}, err
	}
	if value.Status != StatusConsumed || value.Outcome != OutcomeRunning || (value.ImplementationRunID != runID && value.VerificationRunID != runID) {
		return run.Run{}, ErrConflict
	}
	restorer, ok := s.starter.(appServerPlanRestorer)
	if !ok {
		return run.Run{}, errors.New("same App Server Execution restoration unavailable")
	}
	reserve := func(c context.Context) (run.Run, error) {
		id, e := s.newRunID()
		if e != nil {
			return run.Run{}, fmt.Errorf("allocate verification Run: %w", e)
		}
		proposal, r, e := s.store.ReserveVerificationRun(c, value.ID, id, s.now().UTC())
		if e == nil {
			logReservedRunCreated(c, proposal, r)
		}
		return r, e
	}
	complete := func(c context.Context, outcome Outcome, code, message string) error {
		completed, e := s.store.Complete(c, value.ID, outcome, code, message, s.now().UTC())
		if e == nil {
			logExecutionOutcome(c, completed)
		}
		return e
	}
	return restorer.RecoverAppServerPlan(ctx, runID, binding, value.VerificationSpec, reserve, complete)
}
