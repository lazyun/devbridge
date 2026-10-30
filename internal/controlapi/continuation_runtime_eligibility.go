package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func (f *Facade) executionRuntimeEligibility(ctx context.Context, sessionID string, t task.Task) (bool, string, error) {
	if !v2CodingDefinition(t) {
		return true, "", nil
	}
	if f.operatorProject != "" {
		reader, ok := f.deps.WorkflowProgression.(interface {
			ExecutionRuntimeEligibilityForOperator(context.Context, string, string, string, string, string) (bool, string, error)
		})
		if !ok {
			return false, "", errors.New("Operator execution runtime eligibility unavailable")
		}
		return reader.ExecutionRuntimeEligibilityForOperator(ctx, f.operatorProject, t.ID, t.FeatureAreaID, t.CurrentDefinitionRevisionID, t.CurrentExecutionID)
	}
	reader, ok := f.deps.WorkflowProgression.(interface {
		ExecutionRuntimeEligibility(context.Context, string, string, string, string, string) (bool, string, error)
	})
	if !ok {
		return false, "", errors.New("Execution runtime eligibility unavailable")
	}
	return reader.ExecutionRuntimeEligibility(ctx, sessionID, t.ID, t.FeatureAreaID, t.CurrentDefinitionRevisionID, t.CurrentExecutionID)
}

func (f *Facade) enrichExecutionRuntimeEligibility(ctx context.Context, sessionID string, t task.Task, out *p.Projection) error {
	v := out.TaskView
	// A definition-only approval uses a separate explicit adoption confirmation.
	// Its retained historical predecessor is not the candidate being started.
	if !v2CodingDefinition(t) || v == nil || v.CurrentExecution == nil || v.CurrentExecution.ID != t.CurrentExecutionID || v.CurrentExecution.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
		return nil
	}
	available, reason, err := f.executionRuntimeEligibility(ctx, sessionID, t)
	if err != nil || available {
		return err
	}
	for i := range out.Capabilities {
		switch out.Capabilities[i].Action {
		case "START_EXECUTION", "START_VERIFICATION", "RETRY_IMPLEMENTATION", "REQUEST_REVERIFICATION":
			out.Capabilities[i].Available = false
			out.Capabilities[i].Reason = reason
		}
	}
	if a := v.CurrentExecutionAcceptance; a != nil && a.Receipt == nil {
		remaining := make([]string, 0, len(a.AllowedActions))
		for _, action := range a.AllowedActions {
			if action != "RETRY_IMPLEMENTATION" && action != "REQUEST_REVERIFICATION" {
				remaining = append(remaining, action)
				continue
			}
			out.Capabilities = append(out.Capabilities, p.Capability{Action: action, Available: false, Reason: reason, DecisionOption: action, Target: &p.ActionTarget{TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, ExecutionID: t.CurrentExecutionID, SubjectID: t.CurrentExecutionID, DecisionID: wp.TaskAcceptanceDecisionID(t.CurrentExecutionID)}})
		}
		a.AllowedActions = remaining
	}
	return nil
}
