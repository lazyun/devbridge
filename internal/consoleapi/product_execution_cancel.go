package consoleapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
)

func (r *Reader) CancelProductExecution(ctx context.Context, projectID, executionID, runID, requestID string, observed ...*agentconversation.UpperVersion) (executioncancel.Receipt, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return executioncancel.Receipt{}, err
	}

	if r.plane.AgentExecutions == nil {
		return executioncancel.Receipt{}, controlapi.MapError(executioncancel.ErrRunExecutionMismatch)
	}
	e, err := r.plane.AgentExecutions.Get(ctx, executionID)
	if err != nil {
		return executioncancel.Receipt{}, controlapi.MapError(err)
	}
	if e.ProjectID != projectID || string(e.ProvenanceKind) == "LEGACY_UNKNOWN" {
		return executioncancel.Receipt{}, controlapi.MapError(executioncancel.ErrRunExecutionMismatch)
	}
	if runID != "" {
		runValue, err := r.plane.Runs.Get(ctx, runID)
		if err != nil || runValue.AgentExecutionID != e.ID || runValue.TaskID != e.TaskID || runValue.ProjectID != e.ProjectID || runValue.FeatureAreaID != e.FeatureAreaID || runValue.OperationID != e.SemanticOwnerID || string(runValue.OperationKind) != e.SemanticOwnerKind {
			return executioncancel.Receipt{}, controlapi.MapError(executioncancel.ErrRunExecutionMismatch)
		}
	} else if string(e.Kind) != "DEVELOPMENT" {
		return executioncancel.Receipt{}, controlapi.MapError(executioncancel.ErrRunExecutionMismatch)
	}
	// The canonical owner protocol proves that an omitted Run really means
	// no backend/claim exists; the envelope itself grants no cancellation power.
	var version *agentconversation.UpperVersion
	if len(observed) > 1 {
		return executioncancel.Receipt{}, controlapi.MapError(executioncancel.ErrInvalidArgument)
	}
	if len(observed) == 1 {
		version = observed[0]
	}
	input := executioncancel.Input{ExpectedUpperVersion: version, ProjectID: projectID, OwnerType: e.SemanticOwnerKind, OwnerID: e.SemanticOwnerID, RunID: runID, RequestID: requestID}
	if input.OwnerType == "INSPECTION" {
		return executioncancel.Receipt{}, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Inspection cancellation is unavailable"}
	}
	// The envelope has already been resolved above; do not feed its semantic
	// proposal ID back through the facade's envelope resolver.
	receipt, err := r.control.CancelExecutionForOperator(ctx, projectID, e.TaskID, input)
	if err == nil {
		receipt.ExecutionID = e.ID
	}
	return receipt, err
}

// cancelTaskOperation accepts the semantic owner carried by a fresh Task action.
// ExecutionID may describe an envelope in other surfaces; it is never used as
// the routing identity here. The canonical protocol still checks Run/claim/epoch.
func (r *Reader) cancelTaskOperation(ctx context.Context, projectID, sessionID string, owner task.Task, target *workflowpresentation.ActionTarget, requestID string) (executioncancel.Receipt, error) {
	if err := r.validateTaskOperationTarget(ctx, projectID, sessionID, owner, target); err != nil {
		return executioncancel.Receipt{}, err
	}
	// Leave ExecutionID empty intentionally: explicit owner routing bypasses
	// the envelope-ID adapter, then Input.Normalized supplies the semantic ID.
	return r.control.CancelExecutionForOperator(ctx, projectID, owner.ID, executioncancel.Input{ExpectedUpperVersion: target.ExpectedUpperVersion, ProjectID: projectID, OwnerType: target.OperationKind, OwnerID: target.OperationID, RunID: target.RunID, RequestID: requestID})
}

func (r *Reader) validateTaskOperationTarget(ctx context.Context, projectID, sessionID string, owner task.Task, target *workflowpresentation.ActionTarget) error {
	mismatch := func() error { return controlapi.MapError(executioncancel.ErrRunExecutionMismatch) }
	if target.TaskID != owner.ID || target.FeatureAreaID != owner.FeatureAreaID || target.OperationID == "" {
		return mismatch()
	}
	switch target.OperationKind {
	case "TASK":
		if target.OperationID != owner.ID || target.ExecutionID != "" || target.RunID != "" {
			return mismatch()
		}
	case "REVIEW":
		value, err := r.plane.Reviews.Get(ctx, target.OperationID)
		if err != nil || value.TaskID != owner.ID || value.ProjectID != projectID || value.FeatureAreaID != owner.FeatureAreaID || value.RunID != target.RunID {
			return mismatch()
		}
	case "EXECUTION":
		value, err := r.plane.ExecutionProposals.GetForProject(ctx, projectID, target.OperationID)
		if err != nil || value.TaskID != owner.ID || value.ProjectID != projectID || target.TaskDefinitionRevisionID != "" && value.TaskDefinitionRevisionID != target.TaskDefinitionRevisionID {
			return mismatch()
		}
	default:
		return mismatch()
	}
	if target.ExecutionID != "" && target.ExecutionID != target.OperationID {
		if r.plane.AgentExecutions == nil {
			return mismatch()
		}
		envelope, err := r.plane.AgentExecutions.Get(ctx, target.ExecutionID)
		if err != nil || envelope.ProjectID != projectID || envelope.TaskID != owner.ID || envelope.FeatureAreaID != owner.FeatureAreaID || envelope.SemanticOwnerKind != target.OperationKind || envelope.SemanticOwnerID != target.OperationID {
			return mismatch()
		}
	}
	if target.RunID != "" {
		value, err := r.plane.Runs.Get(ctx, target.RunID)
		if err != nil || value.ProjectID != projectID || value.TaskID != owner.ID || value.FeatureAreaID != owner.FeatureAreaID || string(value.OperationKind) != target.OperationKind || value.OperationID != target.OperationID {
			return mismatch()
		}
		if r.plane.AgentConversations == nil {
			return controlapi.MapError(agentconversation.ErrUnavailable)
		}
		boundary, err := r.plane.AgentConversations.Boundary(ctx, target.RunID)
		if err != nil && !errors.Is(err, agentconversation.ErrNotFound) {
			return err
		}
		if boundary.AgentConversationID != target.AgentConversationID {
			return mismatch()
		}
	}
	return nil
}
