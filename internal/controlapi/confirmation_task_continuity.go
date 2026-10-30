package controlapi

import (
	"context"
	"sort"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
)

func (f *Facade) confirmationTaskContinuity(ctx context.Context, intent c.Intent) *c.TaskContinuity {
	if intent.Purpose != c.Task || intent.Content.Task == nil {
		return nil
	}
	out := &c.TaskContinuity{Availability: "UNAVAILABLE", ProjectID: intent.ProjectID, FeatureAreaID: intent.Content.Task.FeatureAreaID, Tasks: []c.ContinuationTask{}, ReadFailureCode: "TASK_INVENTORY_UNAVAILABLE"}
	if f.deps.Tasks == nil || f.deps.FeatureAreas == nil {
		return out
	}
	area, err := f.deps.FeatureAreas.GetForSession(ctx, intent.AISessionID, out.FeatureAreaID)
	if err != nil || area.ID != out.FeatureAreaID || area.ProjectID != out.ProjectID {
		return out
	}
	values, err := f.deps.Tasks.List(ctx)
	if err != nil {
		return out
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].UpdatedAt.After(values[j].UpdatedAt)
	})
	for _, value := range values {
		if value.ProjectID != out.ProjectID || value.FeatureAreaID != out.FeatureAreaID || value.Type != task.TypeDevelopment || value.LegacyReadOnly {
			continue
		}
		switch value.Status {
		case task.StatusActive, task.StatusPlanning, task.StatusReady, task.StatusRunning:
		default:
			continue
		}
		out.Total++
		if len(out.Tasks) >= 20 {
			continue
		}
		item := c.ContinuationTask{TaskID: value.ID, Title: value.Title, Status: string(value.Status), Phase: string(value.Phase), Stage: "当前阶段待核对", Summary: "请读取原任务的当前状态和下一步。", TaskDefinitionRevisionID: value.CurrentDefinitionRevisionID}
		// Reuse the same result-aware projection as task details. In particular,
		// RECOVERY_REQUIRED alone must not imply a missing Codex result.
		if f.deps.Runs != nil {
			if view, e := f.taskView(ctx, value); e == nil {
				item.Stage, item.Summary = view.PhaseDescription, view.Activity.Summary
				if view.NextStep != nil {
					item.Stage, item.Summary = view.NextStep.Label, view.NextStep.Summary
				}
			}
		}
		out.Tasks = append(out.Tasks, item)
	}
	out.Availability, out.ReadFailureCode = "AVAILABLE", ""
	return out
}
