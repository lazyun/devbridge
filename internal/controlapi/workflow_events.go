package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/workflowevents"
)

func (f *Facade) WorkflowEvents() *workflowevents.Service { return f.deps.WorkflowEvents }
func (f *Facade) WorkflowEventStatus(ctx context.Context, project, task string) (workflowevents.Status, error) {
	value, err := f.deps.Tasks.Get(ctx, task)
	if err != nil || value.ProjectID != project || f.deps.WorkflowEvents == nil {
		return workflowevents.Status{}, notFound()
	}
	return f.deps.WorkflowEvents.Status(ctx, project, task)
}
