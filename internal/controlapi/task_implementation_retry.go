package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// enrichImplementationRetry consumes the same pure predicate used inside the
// acceptance write transaction. Neither a failed report nor a stale phase grants
// continuation authority. The command always rechecks the full target.
func (f *Facade) enrichImplementationRetry(ctx context.Context, sessionID string, t task.Task, out *p.Projection) error {
	v := out.TaskView
	if v == nil || v.CurrentExecution == nil || t.CurrentExecutionID == "" || t.Status != task.StatusActive || v.CurrentExecutionAcceptance == nil || v.CurrentExecutionAcceptance.Receipt != nil {
		return nil
	}
	reader, ok := f.deps.WorkflowProgression.(interface {
		ImplementationRetryEligibility(context.Context, string, string, string, string, string) (bool, string, error)
	})
	if !ok {
		return errors.New("Implementation retry eligibility unavailable")
	}
	var available bool
	var reason string
	var err error
	if f.operatorProject != "" {
		operator, ok := f.deps.WorkflowProgression.(interface {
			ImplementationRetryEligibilityForOperator(context.Context, string, string, string, string, string) (bool, string, error)
		})
		if !ok {
			return errors.New("Operator Implementation retry eligibility unavailable")
		}
		available, reason, err = operator.ImplementationRetryEligibilityForOperator(ctx, f.operatorProject, t.ID, t.FeatureAreaID, t.CurrentDefinitionRevisionID, t.CurrentExecutionID)
	} else {
		available, reason, err = reader.ImplementationRetryEligibility(ctx, sessionID, t.ID, t.FeatureAreaID, t.CurrentDefinitionRevisionID, t.CurrentExecutionID)
	}
	if err != nil {
		return err
	}
	target := &p.ActionTarget{TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, ExecutionID: t.CurrentExecutionID, SubjectID: t.CurrentExecutionID, DecisionID: wp.TaskAcceptanceDecisionID(t.CurrentExecutionID)}
	capability := p.Capability{Action: wp.AcceptanceRetryImplementation, Available: available, Reason: reason, DecisionOption: wp.AcceptanceRetryImplementation, Target: target}
	if available && reason == "EXPLICIT_RUNTIME_CONFIG_REQUIRED" {
		capability.RequiredInputs = []string{"nextRunRuntimeConfig"}
	}
	out.Capabilities = append(out.Capabilities, capability)
	if available {
		if out.PendingDecision != nil && (out.PendingDecision.Kind != wp.KindTaskAcceptance || out.PendingDecision.ID != target.DecisionID) {
			return errors.New("Implementation retry decision authority changed; refresh required")
		}
		if out.PendingDecision == nil {
			out.PendingDecision = &p.Decision{Kind: wp.KindTaskAcceptance, ID: target.DecisionID, SubjectID: t.CurrentExecutionID, Summary: "Execution 已安全收口；可明确选择继续实施。", Guarantee: "DURABLE_REQUEST_RECEIPT"}
		}
		v.CurrentExecutionAcceptance.AllowedActions = append(v.CurrentExecutionAcceptance.AllowedActions, wp.AcceptanceRetryImplementation)
	}
	return nil
}
