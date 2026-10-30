package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// StartTaskReviewForOperator is the local-author entry to the original Review
// admission. It derives an actor-scoped receipt once and never borrows a
// Session. The store repeats the supplemental-request relation in its writer
// transaction before a Review, Run, claim, or handoff can exist.
func (f *Facade) StartTaskReviewForOperator(ctx context.Context, projectID, taskID, action, requestID, payloadHash string, target *p.ActionTarget, executor review.ExecutorKind, depth task.ReviewDepth, instruction, engineeringGoal string, focus review.Focus, config codexruntime.NextRunRuntimeConfig, backendInput *codexconfig.BackendSelectionInput) (review.StartAdmission, error) {
	if action != "START_TASK_REVIEW" || target == nil || target.TaskID != taskID || target.SubjectID != taskID || target.FeatureAreaID == "" || (executor != review.ExecutorCodingAgent && executor != review.ExecutorPiAgent) || !depth.Valid() || instruction == "" || engineeringGoal == "" || payloadHash == "" {
		return review.StartAdmission{}, invalid("Exact local Coding Review start is required")
	}
	if focus.Kind == review.FocusRepository && (len(focus.Paths) != 0 || len(focus.Modules) != 0) || focus.Kind == review.FocusFocused && (len(focus.Paths) == 0 || len(focus.Modules) == 0) {
		return review.StartAdmission{}, invalid("Complete explicit Review focus is required")
	}
	if focus.Kind == review.FocusFocused {
		primary := false
		for _, selected := range focus.Modules {
			primary = primary || selected.Role == "PRIMARY"
		}
		if !primary {
			return review.StartAdmission{}, invalid("Focused Review requires an explicit PRIMARY module")
		}
	}
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, action)
	if err != nil {
		return review.StartAdmission{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return review.StartAdmission{}, err
	}
	requestID, err = localoperator.RequestID(ctx, projectID, taskID, action, requestID)
	if err != nil {
		return review.StartAdmission{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	owner, err := reader.deps.Tasks.Get(ctx, taskID)
	if err != nil || owner.ProjectID != projectID || owner.FeatureAreaID != target.FeatureAreaID || owner.Status != task.StatusActive || owner.Type != task.TypeDevelopment || owner.LegacyReadOnly {
		return review.StartAdmission{}, notFound()
	}
	if err := reader.validateCurrentExecutionSelection(config.Model, config.ReasoningEffort); err != nil {
		return review.StartAdmission{}, err
	}
	config, err = config.Normalize(reader.ExecutionAllowlist())
	if err != nil {
		return review.StartAdmission{}, MapError(err)
	}
	if config.AgentID == "pi" {
		current := reader.conversationModelDefault(ctx, owner.FeatureAreaID)
		if current == nil || current.State != "THREADLESS" {
			return review.StartAdmission{}, confirmationSelectionFailure("AGENT_CAPABILITY_UNAVAILABLE", "Pi requires a fresh single run and cannot resume an existing canonical conversation.")
		}
	}
	var selected codexconfig.BackendSelection
	if executor == review.ExecutorCodingAgent {
		var err error
		selected, err = reader.resolveBackendSelection(ctx, "", projectID, owner.FeatureAreaID, backendInput)
		if err != nil {
			return review.StartAdmission{}, invalid(err.Error())
		}
	}
	var supplementalID string
	if progression, ok := reader.deps.WorkflowProgression.(interface {
		TaskReviewRequestEligibilityForOperator(context.Context, string, string) (wp.TaskReviewRequestEligibility, error)
	}); ok {
		facts, e := progression.TaskReviewRequestEligibilityForOperator(ctx, projectID, taskID)
		if e != nil {
			return review.StartAdmission{}, MapError(e)
		}
		if facts.CurrentRequest != nil {
			supplementalID = facts.CurrentRequest.ID
		}
	} else {
		return review.StartAdmission{}, errors.New("Task Review request relation unavailable")
	}
	candidate := review.Review{TaskID: taskID, ProjectID: projectID, FeatureAreaID: owner.FeatureAreaID, AuthorKind: string(review.AuthorLocalOperator), AuthorID: actor.PrincipalID(), SupplementalRequestID: supplementalID, RepositoryID: owner.RepositoryID, CodexConfigID: selected.ConfigID, BackendSelection: &selected, ExecutorKind: executor, ReviewDepth: depth, Instruction: instruction, EngineeringGoal: engineeringGoal, Focus: focus, RuntimeConfig: config, Model: config.Model, ReasoningEffort: config.ReasoningEffort}
	if executor == review.ExecutorPiAgent {
		// PI Agent runs outside the Codex backend inventory; a selection would
		// violate the executor mismatch invariants.
		candidate.CodexConfigID = ""
		candidate.BackendSelection = nil
	}
	return reader.startAdmittedReview(ctx, scopereview.AdmittedStartInput{OperatorPrincipalID: actor.PrincipalID(), RequestID: requestID, PayloadSHA256: payloadHash, Review: candidate}, false)
}

func (f *Facade) ReplayTaskReviewStartForOperator(ctx context.Context, projectID, taskID, areaID, requestID, payloadHash string) (review.StartAdmission, bool, error) {
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, "START_TASK_REVIEW")
	if err != nil {
		return review.StartAdmission{}, false, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return review.StartAdmission{}, false, err
	}
	// Keep the immutable receipt identity identical to fresh admission. The
	// caller supplies the raw request ID and payload hash; this is the single
	// actor-scoped derivation point for a replay read.
	requestID, err = localoperator.RequestID(ctx, projectID, taskID, "START_TASK_REVIEW", requestID)
	if err != nil {
		return review.StartAdmission{}, false, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	store, ok := reader.deps.Reviews.(interface {
		ReplayAdmittedStartForOperator(context.Context, string, string, string, string, string, string) (review.StartAdmission, bool, error)
	})
	if !ok {
		return review.StartAdmission{}, false, errors.New("Review admission replay unavailable")
	}
	result, found, err := store.ReplayAdmittedStartForOperator(ctx, projectID, taskID, areaID, requestID, payloadHash, actor.PrincipalID())
	if err != nil {
		// A failed or contradictory receipt read is not a cache miss. The
		// Console caller must return it rather than attempting fresh admission.
		return review.StartAdmission{}, found, MapError(err)
	}
	if found && (result.Review.ID == "" || result.Run.ID == "" || result.Review.RunID != result.Run.ID) {
		return review.StartAdmission{}, true, &Error{Code: CodeInvalidState, Message: "Stored Review admission receipt is incomplete"}
	}
	return result, found, nil
}
