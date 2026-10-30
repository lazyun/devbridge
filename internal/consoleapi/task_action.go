package consoleapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	"reflect"
	"strings"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type TaskActionInput struct {
	CompletionPlan         *delivery.TaskCompletionPlan       `json:"completionPlan,omitempty"`
	Note                   string                             `json:"note,omitempty"`
	ReviewPurpose          string                             `json:"reviewPurpose,omitempty"`
	ReviewFeedback         string                             `json:"reviewFeedback,omitempty"`
	ExecutionInstructions  *task.ExecutionInstructions        `json:"executionInstructions,omitempty"`
	ExecutorKind           review.ExecutorKind                `json:"executorKind,omitempty"`
	NextRunRuntimeConfig   *codexruntime.NextRunRuntimeConfig `json:"nextRunRuntimeConfig,omitempty"`
	BackendSelection       *codexconfig.BackendSelectionInput `json:"backendSelection,omitempty"`
	ReviewDepth            task.ReviewDepth                   `json:"reviewDepth,omitempty"`
	ReviewInstruction      string                             `json:"reviewInstruction,omitempty"`
	ReviewEngineeringGoal  string                             `json:"reviewEngineeringGoal,omitempty"`
	ReviewFocus            *review.Focus                      `json:"reviewFocus,omitempty"`
	Model                  string                             `json:"model,omitempty"`
	ReasoningEffort        string                             `json:"reasoningEffort,omitempty"`
	AgentConfigFingerprint string                             `json:"agentConfigFingerprint,omitempty"`
	AgentID                string                             `json:"agentId,omitempty"`
	ExecutionPolicy        string                             `json:"executionPolicy,omitempty"`
	CodexFastMode          bool                               `json:"codexFastMode,omitempty"`
	NextWorkSpec           *task.WorkSpec                     `json:"nextWorkSpec,omitempty"`
	NextVerificationSpec   *task.VerificationSpec             `json:"nextVerificationSpec,omitempty"`
	Action                 string                             `json:"action"`
	Target                 *workflowpresentation.ActionTarget `json:"target"`
	RequestID              string                             `json:"requestId"`
}

// ReadLocalApproveAndStartReceipt returns evidence for one immutable local
// approve-and-start command. It deliberately has no capability refresh or
// action-resolution path: it re-hashes the exact original browser body and
// passes it only to the Core's pure receipt reader.
func (r *Reader) ReadLocalApproveAndStartReceipt(ctx context.Context, projectID, taskID string, in TaskActionInput) (controlapi.LocalApproveAndStartResult, error) {
	if in.Action != "APPROVE_AND_START" || in.Target == nil || in.RequestID == "" || !consoleTaskTargetShape(in.Action, in.Target) || !consoleTaskInputShape(in) {
		return controlapi.LocalApproveAndStartResult{}, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Exact local approve-and-start receipt identity is required"}
	}
	boundProject, err := localoperator.BindProject(ctx, projectID)
	if err != nil {
		return controlapi.LocalApproveAndStartResult{}, controlapi.MapError(err)
	}
	ctx = boundProject
	if err := r.requireProject(ctx, projectID); err != nil {
		return controlapi.LocalApproveAndStartResult{}, err
	}
	owner, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil || owner.ProjectID != projectID {
		return controlapi.LocalApproveAndStartResult{}, notFound()
	}
	target := in.Target
	if target.TaskID != taskID || target.SubjectID != taskID || target.FeatureAreaID != owner.FeatureAreaID || !strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:") {
		return controlapi.LocalApproveAndStartResult{}, &controlapi.Error{Code: controlapi.CodeInvalidState, Message: "Local approve-and-start receipt does not belong to this Task"}
	}
	return r.control.ReadLocalApproveAndStartForOperator(ctx, projectID, controlapi.LocalApproveAndStartInput{
		TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ReviewID: strings.TrimPrefix(target.DecisionID, "REVIEW_APPROVAL:"), DecisionID: target.DecisionID,
		RequestID: in.RequestID, PayloadSHA256: taskReviewPayloadHash(taskID, in), Target: target, ExecutionInstructions: in.ExecutionInstructions, NextRunRuntimeConfig: in.NextRunRuntimeConfig, BackendSelection: in.BackendSelection,
	})
}

// ResolveTaskAction revalidates the observed capability, then delegates to the
// existing command owner. It does not implement any workflow transitions.
func (r *Reader) ResolveTaskAction(ctx context.Context, projectID, taskID string, in TaskActionInput) (any, error) {
	if in.ExecutorKind != "" && in.ExecutorKind != review.ExecutorCodingAgent && in.ExecutorKind != review.ExecutorPiAgent {
		return nil, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "UPPER_LLM Review must be started from its bound upper LLM tool session"}
	}
	if in.Target == nil || in.RequestID == "" || !consoleTaskActionSupported(in.Action) || !consoleTaskTargetShape(in.Action, in.Target) || !consoleTaskInputShape(in) {
		return nil, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Unsupported or incomplete Task action"}
	}
	boundProject, err := localoperator.BindProject(ctx, projectID)
	if err != nil {
		return nil, controlapi.MapError(err)
	}
	ctx = boundProject
	if err := r.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	owner, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil || owner.ProjectID != projectID {
		return nil, notFound()
	}
	target := in.Target
	if target.TaskID != taskID || target.FeatureAreaID != owner.FeatureAreaID {
		return nil, &controlapi.Error{Code: controlapi.CodeInvalidState, Message: "Task action target does not belong to this Task"}
	}

	if !operatorTaskActionSupportedTarget(in.Action, in.Target) {
		return nil, operatorActionUnavailable(in.Action)
	}
	ctx = taskaction.WithExpectation(ctx, taskaction.Expectation{TaskID: taskID, Generation: target.TaskActionGeneration})
	// This exact retry request resolves its immutable authority before today's
	// capability checks. A replay never dispatches Git or allocates a successor.
	if in.Action == "RETRY_TASK_COMMIT" && target.OperationID != "" {
		return r.control.ReprepareAcceptedTaskCommitForOperator(ctx, projectID, delivery.RetryInput{TaskID: taskID, RepositoryID: target.RepositoryID, PredecessorOperationID: target.OperationID, RequestID: in.RequestID})
	}
	if in.Action == "CONTINUE_TASK" || in.Action == "DEFER_TASK" {
		if target.ExecutionID != owner.CurrentExecutionID || target.TaskDefinitionRevisionID != owner.CurrentDefinitionRevisionID {
			return nil, operatorActionUnavailable(in.Action)
		}
		return r.control.ChangeTaskHoldForOperator(ctx, projectID, task.HoldInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ExecutionID: target.ExecutionID, RevisionID: target.TaskDefinitionRevisionID, Action: in.Action, RequestID: in.RequestID, Note: in.Note})
	}
	if in.Action == "STOP_AND_REVISE" {
		return r.control.StopAndReviseNoRunTaskForOperator(ctx, projectID, wp.StopAndReviseNoRunTask{ProjectID: projectID, TaskID: taskID, PredecessorExecutionID: target.ExecutionID, ExpectedDefinitionRevisionID: target.TaskDefinitionRevisionID})
	}
	if in.Action == "COMPLETE_TASK" {
		p := in.CompletionPlan
		if p == nil || p.TaskID != taskID || p.ProjectID != projectID || p.FeatureAreaID != target.FeatureAreaID || p.ExecutionID != target.ExecutionID || p.RevisionID != target.TaskDefinitionRevisionID {
			return nil, operatorActionUnavailable(in.Action)
		}
		return r.control.CompleteTaskForOperator(ctx, projectID, *p, in.RequestID)
	}
	rawPayloadHash := taskReviewPayloadHash(taskID, in)
	if in.Action != "RETRY_TASK_COMMIT" && in.Action != "APPROVE_AND_START" {
		bound, e := localoperator.BindTask(ctx, projectID, taskID, in.Action)
		if e != nil {
			return nil, controlapi.MapError(e)
		}
		ctx = bound
	}
	receiptInput := in
	if in.Action != "RETRY_TASK_COMMIT" && in.Action != "APPROVE_AND_START" && in.Action != "START_TASK_REVIEW" {
		receiptInput.RequestID, err = localoperator.RequestID(ctx, projectID, taskID, in.Action, in.RequestID)
		if err != nil {
			return nil, controlapi.MapError(err)
		}
	}
	// This is deliberately before capability freshness. A response-loss replay
	// must resume its persisted parent even though APPROVE has already changed
	// the projection and removed the original button.
	if in.Action == "APPROVE_AND_START" {
		return r.control.ApproveAndStartForOperator(ctx, projectID, controlapi.LocalApproveAndStartInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ReviewID: strings.TrimPrefix(target.DecisionID, "REVIEW_APPROVAL:"), DecisionID: target.DecisionID, RequestID: in.RequestID, PayloadSHA256: rawPayloadHash, Target: target, ExecutionInstructions: in.ExecutionInstructions, NextRunRuntimeConfig: in.NextRunRuntimeConfig, BackendSelection: in.BackendSelection})
	}
	if in.Action == "START_EXECUTION" && in.NextRunRuntimeConfig != nil && in.BackendSelection != nil {
		// The dedicated command verifies its immutable selection before current
		// capability/model checks, so lost responses keep their original result.
		input := controlapi.OperatorApprovedTaskStartInput{
			ProjectID: projectID, TaskID: taskID, RequestID: in.RequestID, Expected: taskExecutionExpectation(in),
			RuntimeConfig: *in.NextRunRuntimeConfig, BackendSelection: *in.BackendSelection,
		}
		if prior, found, readErr := r.control.ReadApprovedTaskStartForOperator(ctx, input); readErr != nil || found {
			return prior, readErr
		}
		return nil, &controlapi.Error{Code: "FORMAL_START_CONFIRMATION_REQUIRED", Message: "An operator selection cannot create formal runtime authority. Use START_APPROVED confirmation from the authorized AI Session; a formally prepared candidate can be started with its existing selection."}
	}
	// A local Review start has a typed immutable admission receipt. Read it
	// before freshness checks: a legal response-loss replay returns its exact
	// Review/Run even when current eligibility or model policy later changed.
	// The facade binds and derives the actor-scoped request exactly once from
	// the original request ID, so do not pre-derive it here.
	if in.Action == "START_TASK_REVIEW" {
		admission, found, replayErr := r.control.ReplayTaskReviewStartForOperator(ctx, projectID, owner.ID, target.FeatureAreaID, in.RequestID, rawPayloadHash)
		if replayErr != nil {
			return nil, replayErr
		}
		if found {
			if admission.Review.ID == "" || admission.Run.ID == "" || admission.Review.RunID != admission.Run.ID {
				return nil, &controlapi.Error{Code: controlapi.CodeInvalidState, Message: "Stored Review admission receipt is incomplete"}
			}
			return admission, nil
		}
	}
	replay := false
	if in.Action != "START_TASK_REVIEW" {
		replay, err = r.taskActionReceiptExists(ctx, projectID, "", owner, receiptInput, rawPayloadHash)
		if err != nil {
			return nil, err
		}
	}
	if !replay {
		// Acceptance commands have a complete revision-bound target and their
		// canonical operator command owner revalidates the current gate atomically.
		// An operator has no AI Session selection, so do not make the Session-level
		// product projection an additional authority for these exact commands.
		allowed := strings.HasPrefix(target.DecisionID, "TASK_ACCEPTANCE:")
		if !allowed {
			t, e := r.GetProductTask(ctx, projectID, taskID)
			if e != nil {
				return nil, e
			}
			if t.Presentation != nil && t.Presentation.TaskView != nil && t.Presentation.TaskView.ActionsStatus == "AVAILABLE" {
				for _, c := range t.Presentation.TaskView.Actions {
					if c.Action == in.Action && c.Available && reflect.DeepEqual(c.Target, target) {
						allowed = true
						break
					}
				}
			}
		}
		if !allowed {
			return nil, &controlapi.Error{Code: controlapi.CodeInvalidState, Message: "Task action is stale or unavailable"}
		}
	}
	switch in.Action {
	case "START_TASK_REVIEW":
		return r.control.StartTaskReviewForOperator(ctx, projectID, taskID, in.Action, in.RequestID, rawPayloadHash, target, in.ExecutorKind, in.ReviewDepth, in.ReviewInstruction, in.ReviewEngineeringGoal, *in.ReviewFocus, codexruntime.NextRunRuntimeConfig{Model: in.Model, ReasoningEffort: in.ReasoningEffort, CodexFastMode: in.CodexFastMode, AgentID: in.AgentID, AgentConfigFingerprint: in.AgentConfigFingerprint, ExecutionPolicy: in.ExecutionPolicy}, in.BackendSelection)
	case wp.ActionRequestTaskReview:
		return r.control.RequestTaskReviewForOperator(ctx, projectID, taskID, in.Action, wp.TaskReviewRequestInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, RequestID: in.RequestID, Purpose: in.ReviewPurpose, ExpectedTarget: target.ExpectedReviewRequest})
	case "ACCEPT", "REJECT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION", "RETRY_IMPLEMENTATION":
		if strings.HasPrefix(target.DecisionID, "TASK_ACCEPTANCE:") {
			return r.control.ResolveTaskAcceptanceForOperator(ctx, projectID, taskID, in.Action, wp.TaskAcceptanceInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, TaskDefinitionRevisionID: target.TaskDefinitionRevisionID, ExecutionID: target.ExecutionID, DecisionID: target.DecisionID, Option: in.Action, RequestID: in.RequestID, ExecutionInstructions: in.ExecutionInstructions, NextWorkSpec: in.NextWorkSpec, NextVerificationSpec: in.NextVerificationSpec, NextRunRuntimeConfig: in.NextRunRuntimeConfig})
		}
		if in.Action == "REJECT" && strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:") {
			return r.control.ResolveReviewApprovalForOperator(ctx, projectID, taskID, in.Action, wp.ResolveInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ReviewID: target.DecisionID[len("REVIEW_APPROVAL:"):], DecisionID: target.DecisionID, Option: in.Action, RequestID: in.RequestID}, nil)
		}
		return nil, operatorActionUnavailable(in.Action)
	case "REVISE":
		if !strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:") {
			return nil, operatorActionUnavailable(in.Action)
		}
		return r.control.ResolveReviewApprovalForOperator(ctx, projectID, taskID, in.Action, wp.ResolveInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ReviewID: target.DecisionID[len("REVIEW_APPROVAL:"):], DecisionID: target.DecisionID, Option: in.Action, RequestID: in.RequestID, ReviewFeedback: in.ReviewFeedback}, nil)
	case "REQUEST_RE_REVIEW":
		const prefix = "INVALID_REVIEW_RESULT_RESOLUTION:"
		if !strings.HasPrefix(target.DecisionID, prefix) {
			return nil, operatorActionUnavailable(in.Action)
		}
		return r.control.ResolveReviewApprovalForOperator(ctx, projectID, taskID, in.Action, wp.ResolveInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ReviewID: strings.TrimPrefix(target.DecisionID, prefix), DecisionID: target.DecisionID, Option: "REVISE", RequestID: in.RequestID, ReviewFeedback: in.ReviewFeedback}, nil)
	case "ABANDON_TASK":
		const prefix = "INVALID_REVIEW_RESULT_RESOLUTION:"
		if !strings.HasPrefix(target.DecisionID, prefix) {
			return nil, operatorActionUnavailable(in.Action)
		}
		return r.control.ResolveReviewApprovalForOperator(ctx, projectID, taskID, in.Action, wp.ResolveInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ReviewID: strings.TrimPrefix(target.DecisionID, prefix), DecisionID: target.DecisionID, Option: "REJECT", RequestID: in.RequestID}, nil)
	case "APPROVE":
		if !strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:") {
			return nil, operatorActionUnavailable(in.Action)
		}
		return r.control.ResolveReviewApprovalForOperator(ctx, projectID, taskID, in.Action, wp.ResolveInput{TaskID: taskID, FeatureAreaID: target.FeatureAreaID, ReviewID: target.DecisionID[len("REVIEW_APPROVAL:"):], DecisionID: target.DecisionID, Option: in.Action, RequestID: in.RequestID, NextRunRuntimeConfig: in.NextRunRuntimeConfig}, in.BackendSelection)
	case "START_EXECUTION", "START_VERIFICATION":
		return r.control.StartTaskExecutionForOperator(ctx, projectID, taskID, in.Action, in.RequestID, taskExecutionExpectation(in))
	case "RETRY_TASK_COMMIT":
		return r.control.RetryAcceptedTaskCommitForOperator(ctx, projectID, taskID)
	case "CANCEL_EXECUTION":
		return r.cancelTaskOperation(ctx, projectID, "", owner, target, in.RequestID)
	case "RECOVER":
		if err := r.validateTaskOperationTarget(ctx, projectID, "", owner, target); err != nil {
			return nil, err
		}
		return r.control.RecoverFeatureAreaOperationForOperator(ctx, projectID, taskID, agentconversation.RecoverInput{ExpectedUpperVersion: target.ExpectedUpperVersion, FeatureAreaID: target.FeatureAreaID, RunID: target.RunID, OperationKind: agentconversation.OperationKind(target.OperationKind), OperationID: target.OperationID, AgentConversationID: target.AgentConversationID, RequestID: in.RequestID})
	}
	return nil, operatorActionUnavailable(in.Action)
}

func consoleTaskActionSupported(action string) bool {
	switch action {
	case "CONTINUE_TASK", "COMPLETE_TASK", "DEFER_TASK", "REQUEST_TASK_REVIEW", "REQUEST_RE_REVIEW", "OPEN_PROJECT_REPOSITORIES", "ABANDON_TASK", "START_TASK_REVIEW", "START_EXECUTION", "START_VERIFICATION", "APPROVE", "APPROVE_AND_START", "REVISE", "REJECT", "ACCEPT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION", "RETRY_IMPLEMENTATION", "CANCEL_EXECUTION", "RECOVER", "RETRY_TASK_COMMIT", "STOP_AND_REVISE":
		return true
	}
	return false
}

func taskReviewPayloadHash(taskID string, in TaskActionInput) string {
	payload, _ := json.Marshal(struct {
		TaskID string
		Input  TaskActionInput
	}{taskID, in})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Finding an immutable request only enables the original command owner's
// replay branch. The owner still compares its persisted payload and target.
func (r *Reader) taskActionReceiptExists(ctx context.Context, projectID, sessionID string, owner task.Task, in TaskActionInput, rawPayloadHash string) (bool, error) {
	target := in.Target
	switch in.Action {
	case "START_EXECUTION", "START_VERIFICATION":
		return r.control.TaskExecutionStartReplayForOperator(ctx, projectID, owner.ID, in.Action, in.RequestID, taskExecutionExpectation(in))
	case wp.ActionRequestTaskReview:
		return r.control.TaskReviewRequestReplayForOperator(ctx, projectID, owner.ID, wp.TaskReviewRequestInput{TaskID: owner.ID, FeatureAreaID: target.FeatureAreaID, RequestID: in.RequestID, Purpose: in.ReviewPurpose, ExpectedTarget: target.ExpectedReviewRequest})
	case "CONTINUE_TASK", "COMPLETE_TASK", "DEFER_TASK", "APPROVE", "APPROVE_AND_START", "REVISE", "REJECT", "REQUEST_RE_REVIEW", "ABANDON_TASK", "ACCEPT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION", "RETRY_IMPLEMENTATION":
		var receipt decisionresolution.Receipt
		var err error
		if _, operator := localoperator.FromContext(ctx); operator {
			receipt, err = r.control.GetDecisionResolutionByRequestForOperator(ctx, projectID, in.RequestID)
		} else {
			receipt, err = r.control.GetDecisionResolutionByRequest(ctx, sessionID, in.RequestID)
		}
		if errors.Is(err, decisionresolution.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		wantOption := in.Action
		if wantOption == "REQUEST_RE_REVIEW" {
			wantOption = "REVISE"
		}
		if wantOption == "ABANDON_TASK" {
			wantOption = "REJECT"
		}
		if receipt.DecisionID != target.DecisionID || receipt.Option != wantOption || receipt.SubjectID != target.SubjectID {
			return false, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Decision request payload conflicts with its receipt"}
		}
		return true, nil
	case "CANCEL_EXECUTION":
		if r.plane.ExecutionCancellations == nil {
			return false, nil
		}
		receipts, err := r.plane.ExecutionCancellations.List(ctx, projectID, target.OperationID)
		if err != nil {
			return false, err
		}
		for _, receipt := range receipts {
			if receipt.RequestID == in.RequestID {
				if receipt.OwnerType != target.OperationKind || receipt.OwnerID != target.OperationID || receipt.RunID != target.RunID || receipt.TaskID != owner.ID {
					return false, controlapi.MapError(executioncancel.ErrRequestConflict)
				}
				return true, nil
			}
		}
	case "RECOVER":
		if r.plane.AgentConversations == nil {
			return false, nil
		}
		receipts, err := r.plane.AgentConversations.ListRecoveryReceipts(ctx, owner.FeatureAreaID)
		if err != nil {
			return false, err
		}
		for _, receipt := range receipts {
			if receipt.RequestID == in.RequestID {
				if receipt.OperationKind != agentconversation.OperationKind(target.OperationKind) || receipt.OperationID != target.OperationID || receipt.RunID != target.RunID || receipt.OldConversationID != target.AgentConversationID {
					return false, controlapi.MapError(agentconversation.ErrConflict)
				}
				return true, nil
			}
		}
	}
	return false, nil
}

// Only fields belonging to the original command contract may accompany replay.
// Fresh actions additionally compare the complete target with the shared result.
func consoleTaskTargetShape(action string, target *workflowpresentation.ActionTarget) bool {
	if target == nil || target.TaskID == "" || target.FeatureAreaID == "" {
		return false
	}
	allowed := workflowpresentation.ActionTarget{TaskID: target.TaskID, FeatureAreaID: target.FeatureAreaID, TaskActionGeneration: target.TaskActionGeneration}
	switch action {
	case "STOP_AND_REVISE":
		allowed.ExecutionID, allowed.TaskDefinitionRevisionID = target.ExecutionID, target.TaskDefinitionRevisionID
		if target.ExecutionID == "" || target.TaskDefinitionRevisionID == "" {
			return false
		}
	case "CONTINUE_TASK", "COMPLETE_TASK", "DEFER_TASK":
		allowed.DecisionID, allowed.SubjectID = target.DecisionID, target.SubjectID
		allowed.ExecutionID, allowed.TaskDefinitionRevisionID = target.ExecutionID, target.TaskDefinitionRevisionID
		if target.ExecutionID == "" || target.SubjectID != target.ExecutionID || target.TaskDefinitionRevisionID == "" || target.DecisionID != wp.TaskAcceptanceDecisionID(target.ExecutionID) {
			return false
		}
	case "REQUEST_TASK_REVIEW":
		allowed.SubjectID, allowed.ExpectedReviewRequest = target.SubjectID, target.ExpectedReviewRequest
		if target.SubjectID != target.TaskID || target.ExpectedReviewRequest == nil || target.ExpectedReviewRequest.Phase == "" {
			return false
		}
	case "APPROVE", "APPROVE_AND_START", "REVISE", "REJECT", "REQUEST_RE_REVIEW", "ABANDON_TASK", "ACCEPT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION", "RETRY_IMPLEMENTATION":
		allowed.DecisionID, allowed.SubjectID = target.DecisionID, target.SubjectID
		if strings.HasPrefix(target.DecisionID, "TASK_ACCEPTANCE:") {
			allowed.ExecutionID, allowed.TaskDefinitionRevisionID = target.ExecutionID, target.TaskDefinitionRevisionID
			if target.ExecutionID == "" || target.SubjectID != target.ExecutionID || target.TaskDefinitionRevisionID == "" {
				return false
			}
		} else if !strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:") && !strings.HasPrefix(target.DecisionID, "INVALID_REVIEW_RESULT_RESOLUTION:") || target.SubjectID != target.TaskID {
			return false
		}
	case "START_TASK_REVIEW", "START_EXECUTION", "START_VERIFICATION":
		allowed.SubjectID, allowed.ExecutionID = target.SubjectID, target.ExecutionID
		if target.SubjectID != target.TaskID {
			return false
		}
		if action != "START_TASK_REVIEW" {
			allowed.TaskDefinitionRevisionID = target.TaskDefinitionRevisionID
			allowed.PrimaryMemberID, allowed.ApprovedWorksetHash = target.PrimaryMemberID, target.ApprovedWorksetHash
		}
	case "CANCEL_EXECUTION", "RECOVER":
		allowed.OperationKind, allowed.OperationID = target.OperationKind, target.OperationID
		if target.OperationKind == "REVIEW" {
			allowed.ExpectedUpperVersion = target.ExpectedUpperVersion
		}
		allowed.RunID, allowed.ExecutionID, allowed.AgentConversationID = target.RunID, target.ExecutionID, target.AgentConversationID
		if target.OperationID == "" || target.OperationKind != "TASK" && target.OperationKind != "REVIEW" && target.OperationKind != "EXECUTION" {
			return false
		}
		if target.OperationKind == "TASK" && (action != "CANCEL_EXECUTION" || target.OperationID != target.TaskID || target.RunID != "" || target.ExecutionID != "" || target.AgentConversationID != "") {
			return false
		}
	case "RETRY_TASK_COMMIT":
		allowed.RepositoryID, allowed.OperationID = target.RepositoryID, target.OperationID
		if (target.RepositoryID == "") != (target.OperationID == "") {
			return false
		}
	default:
		return false
	}
	return reflect.DeepEqual(allowed, *target)
}

func taskExecutionExpectation(in TaskActionInput) wp.TaskExecutionExpectation {
	phase := task.PhaseImplementationReady
	if in.Action == "START_VERIFICATION" {
		phase = task.PhaseVerificationReady
	}
	return wp.TaskExecutionExpectation{FeatureAreaID: in.Target.FeatureAreaID, TaskDefinitionRevisionID: in.Target.TaskDefinitionRevisionID, ExecutionID: in.Target.ExecutionID, Phase: phase, PrimaryMemberID: in.Target.PrimaryMemberID, ApprovedWorksetHash: in.Target.ApprovedWorksetHash}
}

// Reject inputs the selected owner would otherwise silently ignore. Core still
// validates every supported field's content and compares its persisted payload.
func consoleTaskInputShape(in TaskActionInput) bool {
	choice := in.Action == "CONTINUE_TASK" || in.Action == "DEFER_TASK" || in.Action == "COMPLETE_TASK"
	if choice {
		if in.Action == "COMPLETE_TASK" && (in.CompletionPlan == nil || !in.CompletionPlan.Valid() || in.Note != "") {
			return false
		}
		if in.Action != "COMPLETE_TASK" && in.CompletionPlan != nil {
			return false
		}
		if in.Action == "DEFER_TASK" && (strings.TrimSpace(in.Note) == "" || len(in.Note) > 8192) {
			return false
		}
		copy := in
		copy.Action = "REJECT"
		copy.CompletionPlan = nil
		copy.Note = ""
		return consoleTaskInputShape(copy)
	}
	if in.CompletionPlan != nil || in.Note != "" {
		return false
	}

	if (in.CodexFastMode || in.AgentID != "" || in.AgentConfigFingerprint != "" || in.ExecutionPolicy != "") && in.Action != "START_TASK_REVIEW" {
		return false
	}
	if in.Action == wp.ActionRequestTaskReview {
		return strings.TrimSpace(in.ReviewPurpose) != "" && in.ReviewFeedback == "" && in.ExecutionInstructions == nil && in.NextRunRuntimeConfig == nil && in.BackendSelection == nil && in.NextWorkSpec == nil && in.NextVerificationSpec == nil && in.ExecutorKind == "" && in.ReviewDepth == "" && in.ReviewInstruction == "" && in.ReviewEngineeringGoal == "" && in.ReviewFocus == nil && in.Model == "" && in.ReasoningEffort == ""
	}
	if in.ReviewPurpose != "" {
		return false
	}
	if in.Action == "REVISE" || in.Action == "REQUEST_RE_REVIEW" {
		return wp.ValidReviewFeedback(in.ReviewFeedback) && in.ExecutionInstructions == nil && in.NextRunRuntimeConfig == nil && in.BackendSelection == nil && in.NextWorkSpec == nil && in.NextVerificationSpec == nil && in.ExecutorKind == "" && in.ReviewDepth == "" && in.ReviewInstruction == "" && in.ReviewEngineeringGoal == "" && in.ReviewFocus == nil && in.Model == "" && in.ReasoningEffort == ""
	}
	if in.ReviewFeedback != "" {
		return false
	}
	if in.ExecutionInstructions != nil && !(in.Action == "RETRY_IMPLEMENTATION" && in.Target != nil && strings.HasPrefix(in.Target.DecisionID, "TASK_ACCEPTANCE:")) && in.Action != "APPROVE_AND_START" {
		return false
	}

	if in.Action == "START_TASK_REVIEW" {
		return in.NextRunRuntimeConfig == nil && in.BackendSelection != nil && in.NextWorkSpec == nil && in.NextVerificationSpec == nil && strings.TrimSpace(in.ReviewInstruction) != "" && strings.TrimSpace(in.ReviewEngineeringGoal) != "" && in.ReviewFocus != nil
	}
	if in.ExecutorKind != "" || in.ReviewDepth != "" || in.ReviewInstruction != "" || in.ReviewEngineeringGoal != "" || in.ReviewFocus != nil || in.Model != "" || in.ReasoningEffort != "" {
		return false
	}
	acceptance := in.Target != nil && strings.HasPrefix(in.Target.DecisionID, "TASK_ACCEPTANCE:")
	if in.NextWorkSpec != nil || in.NextVerificationSpec != nil {
		if !acceptance || in.Action != "REQUEST_CHANGES" {
			return false
		}
	}
	if in.Action == "APPROVE_AND_START" {
		return !acceptance && in.Target != nil && strings.HasPrefix(in.Target.DecisionID, "REVIEW_APPROVAL:") && (in.NextRunRuntimeConfig == nil) == (in.BackendSelection == nil)
	}
	if in.Action == "START_EXECUTION" {
		return in.NextWorkSpec == nil && in.NextVerificationSpec == nil && (in.NextRunRuntimeConfig == nil) == (in.BackendSelection == nil)
	}
	if in.NextRunRuntimeConfig != nil {
		return acceptance && in.BackendSelection == nil && (in.Action == "REQUEST_CHANGES" || in.Action == "RETRY_IMPLEMENTATION") || !acceptance && in.Action == "APPROVE" && in.BackendSelection != nil
	}
	if in.BackendSelection != nil {
		return false
	}
	if acceptance && in.Action == "REQUEST_CHANGES" {
		// An operator-authored successor must choose an actual runtime rather
		// than silently inheriting the Task's mutable display defaults.
		return false
	}
	return true
}

func operatorTaskActionSupported(action string) bool {
	switch action {
	case "CONTINUE_TASK", "COMPLETE_TASK", "DEFER_TASK", wp.ActionRequestTaskReview, "REQUEST_RE_REVIEW", "ABANDON_TASK", "START_TASK_REVIEW", "APPROVE", "APPROVE_AND_START", "ACCEPT", "REJECT", "REVISE", "REQUEST_CHANGES", "REQUEST_REVERIFICATION", "RETRY_IMPLEMENTATION", "START_EXECUTION", "START_VERIFICATION", "CANCEL_EXECUTION", "RECOVER", "RETRY_TASK_COMMIT", "STOP_AND_REVISE":
		return true
	}
	return false
}

func operatorTaskActionSupportedTarget(action string, target *workflowpresentation.ActionTarget) bool {
	if !operatorTaskActionSupported(action) {
		return false
	}
	if action == wp.ActionRequestTaskReview {
		return target != nil && target.SubjectID == target.TaskID && target.ExpectedReviewRequest != nil
	}
	if action == "ACCEPT" || action == "REQUEST_CHANGES" || action == "REQUEST_REVERIFICATION" || action == "RETRY_IMPLEMENTATION" {
		return target != nil && strings.HasPrefix(target.DecisionID, "TASK_ACCEPTANCE:")
	}
	if action == "REJECT" {
		return target != nil && (strings.HasPrefix(target.DecisionID, "TASK_ACCEPTANCE:") || strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:"))
	}
	if action == "REVISE" {
		return target != nil && strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:")
	}
	if action == "REQUEST_RE_REVIEW" || action == "ABANDON_TASK" {
		return target != nil && strings.HasPrefix(target.DecisionID, "INVALID_REVIEW_RESULT_RESOLUTION:")
	}
	if action == "APPROVE" || action == "APPROVE_AND_START" {
		return target != nil && strings.HasPrefix(target.DecisionID, "REVIEW_APPROVAL:")
	}
	return true
}
func operatorActionUnavailable(action string) error {
	return &controlapi.Error{Code: controlapi.ErrorCode("OPERATOR_ACTION_IDENTITY_UNAVAILABLE"), Message: action + " requires an independent persisted operator actor contract; an AI Session will not be borrowed"}
}
