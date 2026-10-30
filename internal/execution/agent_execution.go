package execution

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/run"
)

// validateRunExecution is deliberately mandatory even for custom RunService
// implementations. A prepared snapshot alone never authorizes a backend call.
func (c *Coordinator) validateRunExecution(ctx context.Context, value run.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	validator, ok := c.Runs.(interface {
		ValidateRunExecution(context.Context, run.Run) error
	})
	if !ok {
		return errors.New("durable AgentExecution validation is unavailable")
	}
	return validator.ValidateRunExecution(ctx, value)
}

func (c *Coordinator) validateRunExecutionDispatch(ctx context.Context, value run.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	validator, ok := c.Runs.(interface {
		ValidateRunExecutionDispatch(context.Context, run.Run) error
	})
	if !ok {
		return errors.New("durable AgentExecution dispatch validation is unavailable")
	}
	return validator.ValidateRunExecutionDispatch(ctx, value)
}
