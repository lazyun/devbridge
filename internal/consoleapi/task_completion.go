package consoleapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/localoperator"
)

func (r *Reader) PrepareTaskCompletion(ctx context.Context, project, id string) (delivery.TaskCompletionPlan, error) {
	ctx, err := localoperator.BindProject(ctx, project)
	if err != nil {
		return delivery.TaskCompletionPlan{}, err
	}
	if err = r.requireProject(ctx, project); err != nil {
		return delivery.TaskCompletionPlan{}, err
	}
	return r.control.PrepareTaskCompletionForOperator(ctx, project, id)
}
