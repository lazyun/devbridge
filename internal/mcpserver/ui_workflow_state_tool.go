package mcpserver

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/observability"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxWorkflowApprovalRequestBytes = 2 << 10

var (
	workflowAbsolutePathPattern = regexp.MustCompile(`(?i)(^|[\s"'=])(?:/[a-z0-9._-]+)+`)
	workflowWindowsPathPattern  = regexp.MustCompile(`(?i)(^|[\s"'=])[a-z]:\\[^\s"']+`)
	workflowSecretPattern       = regexp.MustCompile(`(?i)(password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|cookie|credential|token)(\s*[:=]\s*)[^\s,;]+`)
	workflowBearerPattern       = regexp.MustCompile(`(?i)bearer\s+[^\s,;]+`)
)

type uiWorkflowStateInput struct {
	AISessionID         string `json:"aiSessionId"`
	ReviewID            string `json:"reviewId,omitempty"`
	TaskID              string `json:"taskId,omitempty"`
	TaskPlanID          string `json:"taskPlanId,omitempty"`
	ExecutionProposalID string `json:"executionProposalId,omitempty"`
	RunID               string `json:"runId,omitempty"`
	ScopeExpansionID    string `json:"scopeExpansionId,omitempty"`
}

type UIWorkflowTaskOutput struct {
	Title  string `json:"title"`
	Phase  string `json:"phase"`
	ID     string `json:"id"`
	Status string `json:"status"`
}

type UIWorkflowStageOutput struct {
	Status string `json:"status"`
}

type UIWorkflowRunOutput struct {
	ExecutionID string `json:"executionId,omitempty"`
	ID          string `json:"id"`
	Purpose     string `json:"purpose"`
	Status      string `json:"status"`
}

type UIWorkflowApprovalOutput struct {
	CancelEffect     string          `json:"cancelEffect"`
	Pending          bool            `json:"pending"`
	ID               string          `json:"id,omitempty"`
	RunID            string          `json:"runId,omitempty"`
	Kind             string          `json:"kind,omitempty"`
	Status           string          `json:"status,omitempty"`
	Request          json.RawMessage `json:"request,omitempty"`
	RequestTruncated bool            `json:"requestTruncated"`
	AllowedDecisions []string        `json:"allowedDecisions"`
}

type UIWorkflowFailureOutput struct {
	Code    string `json:"code,omitempty"`
	Summary string `json:"summary"`
}

type UIWorkflowContinuationOutput struct {
	RunID               string `json:"runId,omitempty"`
	RequestID           string `json:"requestId,omitempty"`
	Attempt             bool   `json:"attempt"`
	Kind                string `json:"kind"`
	FeatureAreaID       string `json:"featureAreaId"`
	ReviewID            string `json:"reviewId,omitempty"`
	TaskID              string `json:"taskId,omitempty"`
	Message             string `json:"message"`
	Status              string `json:"status,omitempty"`
	ResultAvailable     bool   `json:"resultAvailable"`
	CompletedTransition bool   `json:"completedTransition"`
}

// UIWorkflowStateOutput is intentionally a small presentation projection. It
// must not grow into the model-facing Run/TaskPlan/Evidence read surface.
type UIWorkflowStateOutput struct {
	ExecutionID                  string                        `json:"executionId,omitempty"`
	AgentExecution               *agentexecution.Projection    `json:"agentExecution,omitempty"`
	Task                         *UIWorkflowTaskOutput         `json:"task,omitempty"`
	ReviewID                     string                        `json:"reviewId,omitempty"`
	TaskPlanID                   string                        `json:"taskPlanId,omitempty"`
	ExecutionProposalID          string                        `json:"executionProposalId,omitempty"`
	Phase                        string                        `json:"phase"`
	Implementation               UIWorkflowStageOutput         `json:"implementation"`
	Verification                 UIWorkflowStageOutput         `json:"verification"`
	CurrentRun                   *UIWorkflowRunOutput          `json:"currentRun,omitempty"`
	Approval                     *UIWorkflowApprovalOutput     `json:"approval,omitempty"`
	ChangedFileCount             int                           `json:"changedFileCount"`
	Terminal                     bool                          `json:"terminal"`
	PlanningContinuationRequired bool                          `json:"planningContinuationRequired"`
	WorkflowPhase                string                        `json:"workflowPhase,omitempty"`
	WorkflowObjective            string                        `json:"workflowObjective,omitempty"`
	Failure                      *UIWorkflowFailureOutput      `json:"failure,omitempty"`
	Continuation                 *UIWorkflowContinuationOutput `json:"continuation,omitempty"`
	TaskPlan                     *TaskPlanOutput               `json:"taskPlan,omitempty"`
	ExecutionProposal            *ExecutionProposalOutput      `json:"executionProposal,omitempty"`
	ScopeExpansion               *ScopeExpansionOutput         `json:"scopeExpansion,omitempty"`
}

const (
	uiWorkflowStateInputSchema   = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"reviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$","maxLength":256},"taskId":{"type":"string","minLength":1,"maxLength":128},"taskPlanId":{"type":"string","maxLength":128},"executionProposalId":{"type":"string","maxLength":128},"runId":{"type":"string","maxLength":128},"scopeExpansionId":{"type":"string","maxLength":128}},"required":["aiSessionId"],"anyOf":[{"required":["reviewId"]},{"required":["taskId"]}],"additionalProperties":false}`
	uiWorkflowStageSchema        = `{"type":"object","properties":{"status":{"type":"string","maxLength":32}},"required":["status"],"additionalProperties":false}`
	uiWorkflowContinuationSchema = `{"type":"object","properties":{"runId":{"type":"string"},"requestId":{"type":"string"},"attempt":{"type":"boolean"},"kind":{"type":"string","enum":["REVIEW_SUCCEEDED","REVIEW_FAILED"]},"featureAreaId":{"type":"string"},"reviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$"},"taskId":{"type":"string"},"message":{"type":"string","maxLength":1024},"status":{"type":"string","maxLength":64},"resultAvailable":{"type":"boolean"},"completedTransition":{"type":"boolean"}},"required":["attempt","kind","featureAreaId","message","resultAvailable","completedTransition"],"additionalProperties":false}`
	uiWorkflowStateOutputSchema  = `{"type":"object","properties":{"executionId":{"type":"string"},"agentExecution":` + agentExecutionProjectionSchema + `,"task":{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"phase":{"type":"string"},"status":{"type":"string","maxLength":32}},"required":["id","title","phase","status"],"additionalProperties":false},"reviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$"},"taskPlanId":{"type":"string"},"executionProposalId":{"type":"string"},"phase":{"type":"string"},"workflowPhase":{"type":"string"},"workflowObjective":{"type":"string"},"implementation":` + uiWorkflowStageSchema + `,"verification":` + uiWorkflowStageSchema + `,"currentRun":{"type":"object","properties":{"executionId":{"type":"string"},"id":{"type":"string"},"purpose":{"type":"string","enum":["SCOPE_REVIEW","IMPLEMENTATION","VERIFICATION"]},"status":{"type":"string","maxLength":32}},"required":["id","purpose","status"],"additionalProperties":false},"approval":{"type":"object","properties":{"cancelEffect":{"type":"string","enum":["DECLINE_ACTION","CANCEL_LEGACY_RUN"]},"pending":{"type":"boolean"},"id":{"type":"string"},"runId":{"type":"string"},"kind":{"type":"string","maxLength":32},"status":{"type":"string","maxLength":32},"request":{},"requestTruncated":{"type":"boolean"},"allowedDecisions":{"type":"array","maxItems":16,"items":{"type":"string","maxLength":64}}},"required":["pending","requestTruncated","allowedDecisions","cancelEffect"],"additionalProperties":false},"changedFileCount":{"type":"integer","minimum":0},"terminal":{"type":"boolean"},"planningContinuationRequired":{"type":"boolean"},"continuation":` + uiWorkflowContinuationSchema + `,"taskPlan":` + taskPlanOutputSchema + `,"executionProposal":` + executionProposalOutputSchema + `,"scopeExpansion":` + scopeExpansionOutputSchema + `,"failure":{"type":"object","properties":{"code":{"type":"string","maxLength":128},"summary":{"type":"string","maxLength":4096}},"required":["summary"],"additionalProperties":false}},"required":["phase","implementation","verification","changedFileCount","terminal","planningContinuationRequired"],"additionalProperties":false}`
)

func registerUIWorkflowStateTool(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:         "ui_workflow_state_get",
		Title:        "Get app workflow state",
		Description:  "Return a compact workflow presentation projection for the embedded Dev Bridge app.",
		InputSchema:  json.RawMessage(uiWorkflowStateInputSchema),
		OutputSchema: json.RawMessage(uiWorkflowStateOutputSchema),
		Annotations:  toolAnnotations("Get app workflow state", true, true),
		Meta: sdkmcp.Meta{"ui": map[string]any{
			"visibility": []string{"app"},
		}},
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input uiWorkflowStateInput) (*sdkmcp.CallToolResult, UIWorkflowStateOutput, error) {
		value, err := uiWorkflowState(ctx, service, input)
		if err != nil {
			return nil, UIWorkflowStateOutput{}, publicToolError(err)
		}
		return nil, value, nil
	})
}

// uiWorkflowState exposes Task authority while retaining child activity details.
func uiWorkflowState(ctx context.Context, service BridgeService, input uiWorkflowStateInput) (UIWorkflowStateOutput, error) {
	taskID := input.TaskID
	if input.ReviewID != "" {
		v, err := service.GetReview(ctx, input.AISessionID, input.ReviewID)
		if err != nil {
			return UIWorkflowStateOutput{}, err
		}
		if taskID != "" && taskID != v.TaskID {
			return UIWorkflowStateOutput{}, task.ErrNotFound
		}
		taskID = v.TaskID
	}
	owner, err := service.GetTask(ctx, input.AISessionID, taskID)
	if err != nil {
		return UIWorkflowStateOutput{}, err
	}
	if taskID == "" || owner.ID != taskID {
		return UIWorkflowStateOutput{}, task.ErrNotFound
	}
	if owner.Type == task.TypeInspection {
		return uiWorkflowActivity(ctx, service, input)
	}
	if input.ReviewID != "" {
		input.TaskID = ""
	}
	result, err := uiWorkflowActivity(ctx, service, input)
	if err != nil {
		return result, err
	}
	result.Task = &UIWorkflowTaskOutput{ID: owner.ID, Title: owner.Title, Status: string(owner.Status), Phase: string(owner.Phase)}
	result.Phase = string(owner.Phase)
	result.WorkflowPhase = string(owner.Phase)
	result.Terminal = owner.Status != task.StatusActive
	result.TaskPlan = nil
	result.ExecutionProposal = nil
	result.PlanningContinuationRequired = false
	if reader, ok := service.(interface {
		WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error)
	}); ok {
		g, e := reader.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: input.AISessionID, TaskID: owner.ID})
		if e != nil {
			return result, e
		}
		result.WorkflowObjective = g.NextAction.Code
	}
	return result, nil
}

func uiWorkflowActivity(ctx context.Context, service BridgeService, input uiWorkflowStateInput) (UIWorkflowStateOutput, error) {
	if input.ReviewID != "" {
		return uiReviewWorkflowState(ctx, service, input)
	}
	if input.TaskID == "" {
		return UIWorkflowStateOutput{}, task.ErrNotFound
	}
	taskValue, err := service.GetTask(ctx, input.AISessionID, input.TaskID)
	if err != nil {
		return UIWorkflowStateOutput{}, err
	}
	if taskValue.ID != input.TaskID {
		return UIWorkflowStateOutput{}, task.ErrNotFound
	}
	historyOnly, err := uiWorkflowHistoryOnly(ctx, service, input.AISessionID, taskValue.ProjectID, taskValue.FeatureAreaID)
	if err != nil {
		return UIWorkflowStateOutput{}, err
	}
	historyOnly = historyOnly || taskValue.LegacyReadOnly
	result := UIWorkflowStateOutput{
		Task:           &UIWorkflowTaskOutput{ID: taskValue.ID, Status: string(taskValue.Status)},
		ReviewID:       input.ReviewID,
		TaskPlanID:     input.TaskPlanID,
		Phase:          string(taskValue.Phase),
		Implementation: UIWorkflowStageOutput{Status: "WAITING"},
		Verification:   UIWorkflowStageOutput{Status: "WAITING"},
	}
	selection, err := resolveUITaskWorkflow(ctx, service, input, taskValue)
	if err != nil {
		return UIWorkflowStateOutput{}, err
	}
	planValue, proposalValue, runValue := selection.plan, selection.execution, selection.run
	if proposalValue != nil && (proposalValue.TaskDefinitionRevisionID == "" || proposalValue.ProvenanceKind == "LEGACY_UNKNOWN") {
		historyOnly = true
	}

	if planValue != nil {
		result.TaskPlanID = planValue.ID
		if planValue.Status == taskplan.StatusPendingConfirmation {
			value := taskPlanOutput(*planValue, true)
			result.TaskPlan = &value
		}
	}
	if proposalValue != nil {
		result.ExecutionProposalID = proposalValue.ID
	}

	if runValue != nil {
		applyRunPresentation(&result, *runValue)
		if err := enrichUIAgentExecution(ctx, service, input.AISessionID, &result); err != nil {
			return result, err
		}
		if runValue.Status == run.StatusWaitingApproval {
			values, listErr := service.ListApprovalsForSession(ctx, input.AISessionID, controlapi.ApprovalFilter{RunID: runValue.ID, PendingOnly: true, ListOptions: controlapi.ListOptions{Limit: 1}})
			if listErr != nil {
				return UIWorkflowStateOutput{}, listErr
			}
			if len(values) > 0 {
				result.Approval = uiWorkflowApproval(values[0])
			}
		}
	}
	if proposalValue != nil && runValue == nil {
		// resolveUITaskWorkflow already validated this exact selector against the
		// session, Project and Task. DEVELOPMENT retains its proposal ID as the
		// universal identity, including after a successor becomes Task-current.
		result.ExecutionID = proposalValue.ID
		if err := enrichUIAgentExecution(ctx, service, input.AISessionID, &result); err != nil {
			return result, err
		}
	}
	if authorizationActionable(proposalValue, result.AgentExecution) {
		value := executionProposalOutput(*proposalValue, true)
		value.AuthorizationActionable = true
		result.ExecutionProposal = &value
	}

	applyCoreExecutionCard(&result)
	if historyOnly {
		applyUnavailableWorkflow(&result)
		return result, nil
	}
	// Only the highest-priority durable decision is projected as a card. This
	// keeps the app read model bounded and prevents obsolete planning lineage
	// from appearing beside a later runtime decision.
	if result.Approval != nil && result.Approval.Pending {
		result.ExecutionProposal = nil
		result.TaskPlan = nil
	} else if runValue != nil {
		result.ExecutionProposal = nil
		result.TaskPlan = nil
	} else if result.ExecutionProposal != nil {
		result.TaskPlan = nil
	}
	decisionKind := ""
	if result.Approval != nil && result.Approval.Pending {
		decisionKind = "RUNTIME_APPROVAL"
	} else if result.ExecutionProposal != nil {
		decisionKind = "EXECUTION_AUTHORIZATION"
	} else if result.TaskPlan != nil {
		decisionKind = "TASK_CONFIRMATION"
	}
	if decisionKind != "" {
		observability.Info(observability.WithContext(ctx, observability.Correlation{AISessionID: input.AISessionID, TaskID: input.TaskID, ReviewID: input.ReviewID}), "decision", "decision.reconstructed", "durable decision card reconstructed", map[string]any{"kind": decisionKind})
	}

	return result, nil
}

type uiTaskWorkflowSelection struct {
	plan      *taskplan.Plan
	execution *executionproposal.Proposal
	run       *run.Run
}

// resolveUITaskWorkflow follows exact persisted references for explicit
// history, and otherwise uses the canonical current operation graph.

// Historical reads retain facts without offering decisions or continuations.
func uiWorkflowHistoryOnly(ctx context.Context, service BridgeService, sessionID, projectID, areaID string) (bool, error) {
	if areaID == "" {
		return true, nil
	}
	area, err := service.GetFeatureArea(ctx, sessionID, areaID)
	if err != nil {
		return false, err
	}
	if area.ID != areaID || area.ProjectID != projectID {
		return false, task.ErrNotFound
	}
	return !featurearea.IsUsable(area), nil
}

func applyUnavailableWorkflow(result *UIWorkflowStateOutput) {
	if result.AgentExecution == nil {
		if !result.Terminal {
			result.Phase = "UNAVAILABLE"
		}
		result.Terminal = true
	}
	result.WorkflowPhase, result.WorkflowObjective = "FEATURE_AREA_UNAVAILABLE", "INSPECT_HISTORY"
	result.TaskPlan, result.ExecutionProposal, result.ScopeExpansion, result.Approval, result.Continuation = nil, nil, nil, nil, nil
	result.PlanningContinuationRequired = false
}

func applyRunPresentation(result *UIWorkflowStateOutput, value run.Run) {
	result.ExecutionID = value.AgentExecutionID
	result.CurrentRun = &UIWorkflowRunOutput{ExecutionID: value.AgentExecutionID, ID: value.ID, Purpose: string(value.Purpose), Status: string(value.Status)}
	switch value.Purpose {
	case run.PurposeScopeReview:

	case run.PurposeImplementation:

		result.Implementation.Status = string(value.Status)
	case run.PurposeVerification:

		result.Implementation.Status = "SUCCEEDED"
		result.Verification.Status = string(value.Status)
	}
	if value.Result != nil {
		result.ChangedFileCount = len(value.Result.GitEvidence.ChangedFiles)
	}
	if value.Failure != nil {
		result.Failure = &UIWorkflowFailureOutput{Code: boundedUTF8(sanitizeWorkflowApprovalText(value.Failure.Code), 128), Summary: "The workflow run failed."}
	}
}

func terminalRunStatus(status run.Status) bool {
	switch status {
	case run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
		return true
	default:
		return false
	}
}

func terminalPhase(status run.Status) string {
	switch status {
	case run.StatusSucceeded:
		return "SUCCEEDED"
	case run.StatusCanceled:
		return "CANCELED"
	default:
		return "FAILED"
	}
}

func uiWorkflowApproval(value approval.Approval) *UIWorkflowApprovalOutput {
	request, truncated := safeApprovalPayload(value.RequestPayload)
	request = sanitizeWorkflowApprovalRequest(request)
	if len(request) > maxWorkflowApprovalRequestBytes {
		request = json.RawMessage(`{"truncated":true}`)
		truncated = true
	}
	allowed := make([]string, 0, len(value.AllowedDecisions))
	for _, decision := range value.AllowedDecisions {
		allowed = append(allowed, string(decision))
	}
	return &UIWorkflowApprovalOutput{CancelEffect: approvalCancelEffect(value), Pending: value.Status == approval.StatusPending, ID: value.ID, RunID: value.RunID, Kind: string(value.Kind), Status: string(value.Status), Request: request, RequestTruncated: truncated, AllowedDecisions: allowed}
}

func sanitizeWorkflowApprovalRequest(request json.RawMessage) json.RawMessage {
	var value any
	if json.Unmarshal(request, &value) != nil {
		return json.RawMessage(`{"unavailable":true}`)
	}
	sanitizeWorkflowApprovalValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"unavailable":true}`)
	}
	return encoded
}

func sanitizeWorkflowApprovalValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if secretApprovalKey(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			if textValue, ok := nested.(string); ok {
				typed[key] = sanitizeWorkflowApprovalText(textValue)
				continue
			}
			sanitizeWorkflowApprovalValue(nested)
		}
	case []any:
		for index, nested := range typed {
			if textValue, ok := nested.(string); ok {
				typed[index] = sanitizeWorkflowApprovalText(textValue)
				continue
			}
			sanitizeWorkflowApprovalValue(nested)
		}
	}
}

func sanitizeWorkflowApprovalText(value string) string {
	value = workflowBearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = workflowSecretPattern.ReplaceAllString(value, "$1$2[REDACTED]")
	value = workflowAbsolutePathPattern.ReplaceAllStringFunc(value, func(match string) string {
		prefix := ""
		if len(match) > 0 && !strings.HasPrefix(match, "/") {
			prefix = match[:1]
		}
		return prefix + "[REDACTED_PATH]"
	})
	value = workflowWindowsPathPattern.ReplaceAllStringFunc(value, func(match string) string {
		prefix := ""
		if len(match) > 1 && match[1] != ':' {
			prefix = match[:1]
		}
		return prefix + "[REDACTED_PATH]"
	})
	return boundedUTF8(value, maxWorkflowApprovalRequestBytes)
}

func uiReviewWorkflowState(ctx context.Context, service BridgeService, input uiWorkflowStateInput) (UIWorkflowStateOutput, error) {
	if input.TaskID != "" || input.TaskPlanID != "" || input.ExecutionProposalID != "" {
		return UIWorkflowStateOutput{}, productreview.ErrInvalidArgument
	}
	value, err := service.GetReview(ctx, input.AISessionID, input.ReviewID)
	if err != nil {
		return UIWorkflowStateOutput{}, err
	}
	historyOnly, err := uiWorkflowHistoryOnly(ctx, service, input.AISessionID, value.ProjectID, value.FeatureAreaID)
	if err != nil {
		return UIWorkflowStateOutput{}, err
	}

	result := UIWorkflowStateOutput{ReviewID: value.ID, Phase: "STARTING", WorkflowPhase: "FEATURE_AREA_REVIEW", WorkflowObjective: "REVIEW_FEATURE_AREA", Implementation: UIWorkflowStageOutput{Status: "NOT_STARTED"}, Verification: UIWorkflowStageOutput{Status: "NOT_STARTED"}}
	// Scope expansion rows are legacy history and never reconstruct a decision.

	if value.RunID == "" {
		if value.StartFailure != nil {
			result.Terminal = true
			result.Phase = "BLOCKED"
			result.Failure = &UIWorkflowFailureOutput{Code: value.StartFailure.Code, Summary: value.StartFailure.Blocker}
		}
		if historyOnly {
			applyUnavailableWorkflow(&result)
		}
		return result, nil
	}
	if input.RunID != "" && input.RunID != value.RunID {
		return result, productreview.ErrNotFound
	}
	runValue, err := service.GetRunForSession(ctx, input.AISessionID, value.RunID)
	if err != nil || runValue.ReviewID != value.ID || runValue.TaskID != value.TaskID {
		return result, productreview.ErrNotFound
	}
	applyRunPresentation(&result, runValue)
	if err := enrichUIAgentExecution(ctx, service, input.AISessionID, &result); err != nil {
		return result, err
	}
	if runValue.Status == run.StatusWaitingApproval {
		values, err := service.ListApprovalsForSession(ctx, input.AISessionID, controlapi.ApprovalFilter{RunID: runValue.ID, PendingOnly: true, ListOptions: controlapi.ListOptions{Limit: 1}})
		if err != nil {
			return result, err
		}
		if len(values) != 0 {
			result.Approval = uiWorkflowApproval(values[0])
			result.Phase, result.WorkflowPhase, result.WorkflowObjective = "WAITING_APPROVAL", "WAITING_APPROVAL", "RESOLVE_USER_DECISION"
		}
	}
	{
		result.Terminal = terminalRunStatus(runValue.Status)
		if result.Terminal {
			result.Phase = terminalPhase(runValue.Status)
		}
	}
	applyCoreExecutionCard(&result)
	if historyOnly {
		applyUnavailableWorkflow(&result)
		return result, nil
	}
	if result.Terminal && (result.AgentExecution == nil || result.AgentExecution.Status == "COMPLETED" || result.AgentExecution.Status == "FAILED" || result.AgentExecution.Status == "CANCELED") {
		claimed, err := service.ClaimReviewContinuation(ctx, input.AISessionID, value.ID)
		if err != nil {
			return result, err
		}
		logCtx := observability.WithContext(ctx, observability.Correlation{AISessionID: input.AISessionID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, ReviewID: value.ID})
		if claimed {
			for _, event := range []string{"continuation.requested", "continuation.emitted", "continuation.delivery_accepted"} {
				observability.Info(logCtx, "review", event, "Review semantic continuation prepared", nil)
			}
			kind, message := "REVIEW_FAILED", "This terminal notification closes the identified Review. Inspect its durable failure result; it does not request or authorize another Review. No implementation has been authorized."
			if runValue.Status == run.StatusSucceeded {
				kind = "REVIEW_SUCCEEDED"
				message = "This terminal notification closes the identified Review and its result is available. Read the existing Task phase and nextAction to decide whether its current review gate is actionable."
			}
			resultAvailable := runValue.Status == run.StatusSucceeded && runValue.Result != nil && runValue.Result.AgentClaim.Message != ""
			identity := "Review " + value.ID + " (Run " + runValue.ID + ", FeatureArea " + value.FeatureAreaID
			if value.StartRequestID != "" {
				identity += ", request " + value.StartRequestID
			}
			result.Continuation = &UIWorkflowContinuationOutput{Attempt: true, RunID: runValue.ID, RequestID: value.StartRequestID, Kind: kind, FeatureAreaID: value.FeatureAreaID, ReviewID: value.ID, TaskID: value.TaskID, Message: boundedUTF8(identity+"). "+message, 1024), Status: string(runValue.Status), ResultAvailable: resultAvailable, CompletedTransition: true}
		} else {
			observability.Info(logCtx, "review", "continuation.skipped_duplicate", "Review semantic continuation already emitted", nil)
		}
	}
	return result, nil
}

// resolveUITaskWorkflow validates each selected object's exact Project/Task and
// Run owner. Historical selectors never borrow a newer execution's authority.
func resolveUITaskWorkflow(ctx context.Context, service BridgeService, input uiWorkflowStateInput, owner task.Task) (uiTaskWorkflowSelection, error) {
	var out uiTaskWorkflowSelection
	if input.RunID != "" {
		v, err := service.GetRunForSession(ctx, input.AISessionID, input.RunID)
		if err != nil {
			return out, err
		}
		if v.TaskID != owner.ID || v.ProjectID != owner.ProjectID || v.ReviewID != "" {
			return out, task.ErrNotFound
		}
		if input.ExecutionProposalID != "" && input.ExecutionProposalID != v.ExecutionProposalID {
			return out, task.ErrNotFound
		}
		input.ExecutionProposalID = v.ExecutionProposalID
		out.run = &v
	}
	if input.ExecutionProposalID != "" {
		v, err := service.GetExecutionProposal(ctx, input.AISessionID, input.ExecutionProposalID)
		if err != nil {
			return out, err
		}
		out.execution = &v
	} else if input.TaskPlanID == "" && input.RunID == "" {
		var current controlapi.CurrentWorkflow
		var err error
		if scoped, ok := service.(interface {
			CurrentTaskWorkflow(context.Context, string, string) (controlapi.CurrentWorkflow, error)
		}); ok {
			current, err = scoped.CurrentTaskWorkflow(ctx, input.AISessionID, owner.ID)
		} else {
			current, err = service.CurrentWorkflow(ctx, input.AISessionID, owner.FeatureAreaID)
		}
		if err != nil {
			return out, err
		}
		if current.Task != nil && current.Task.ID == owner.ID {
			out.execution = current.Execution
			out.plan = current.TaskPlan
		}
	}
	if e := out.execution; e != nil {
		if e.TaskID != owner.ID || e.ProjectID != owner.ProjectID || input.TaskPlanID != "" && input.TaskPlanID != e.TaskPlanID {
			return out, task.ErrNotFound
		}
		input.TaskPlanID = e.TaskPlanID
		if out.run == nil {
			id := e.VerificationRunID
			if id == "" {
				id = e.ImplementationRunID
			}
			if id == "" {
				id = e.RunID
			}
			if id != "" {
				v, err := service.GetRunForSession(ctx, input.AISessionID, id)
				if err != nil {
					return out, err
				}
				out.run = &v
			}
		}
	}
	if input.TaskPlanID != "" {
		v, err := service.GetTaskPlan(ctx, input.AISessionID, input.TaskPlanID)
		if err != nil {
			return out, err
		}
		out.plan = &v
	}
	if p := out.plan; p != nil && (p.TaskID != owner.ID || p.ProjectID != owner.ProjectID || out.execution != nil && out.execution.TaskPlanID != p.ID) {
		return out, task.ErrNotFound
	}
	if r := out.run; r != nil && (r.TaskID != owner.ID || r.ProjectID != owner.ProjectID || r.ReviewID != "" || out.execution == nil || r.ExecutionProposalID != out.execution.ID || r.AgentExecutionID != out.execution.ID) {
		return out, task.ErrNotFound
	}
	return out, nil
}
