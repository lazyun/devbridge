package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type progressionService interface {
	WorkflowGuidance(context.Context, string, string) (wp.Guidance, error)
	ResolveReviewApproval(context.Context, wp.ResolveInput) (wp.Resolution, error)
	StartApprovedExecution(context.Context, string, string) (executionproposal.StartResult, error)
}

func (s *StaticService) WorkflowGuidance(ctx context.Context, session, area string) (wp.Guidance, error) {
	if s.control == nil {
		return wp.Guidance{}, errors.New("workflow unavailable")
	}
	return s.control.WorkflowGuidance(ctx, session, area)
}
func (s *StaticService) ResolveReviewApproval(ctx context.Context, in wp.ResolveInput) (wp.Resolution, error) {
	if s.control == nil {
		return wp.Resolution{}, errors.New("workflow unavailable")
	}
	return s.control.ResolveReviewApproval(ctx, in)
}
func (s *StaticService) StartApprovedExecution(ctx context.Context, session, id string) (executionproposal.StartResult, error) {
	if s.control == nil {
		return executionproposal.StartResult{}, errors.New("workflow unavailable")
	}
	return s.control.StartApprovedExecution(ctx, session, id)
}
func (s *StaticService) ValidateDevelopmentCreation(ctx context.Context, session, area string) error {
	if s.control == nil {
		return nil
	}
	return s.control.ValidateDevelopmentCreation(ctx, session, area)
}

func ReviewApprovalCore(ctx context.Context, service BridgeService, in wp.ResolveInput) (wp.Resolution, error) {
	p, ok := service.(progressionService)
	if !ok {
		return wp.Resolution{}, errors.New("workflow unavailable")
	}
	if in.ReviewID == "" {
		in.ReviewID = strings.TrimPrefix(in.DecisionID, wp.KindReviewApproval+":")
	}
	if in.DecisionID != wp.DecisionID(in.ReviewID) {
		return wp.Resolution{}, &controlapi.Error{Code: controlapi.CodeConflict, Message: "STALE_REVIEW_APPROVAL: Decision and Review mismatch"}
	}
	if in.FeatureAreaID == "" {
		v, err := service.GetReview(ctx, in.AISessionID, in.ReviewID)
		if err != nil {
			return wp.Resolution{}, err
		}
		in.FeatureAreaID = v.FeatureAreaID
	}
	return p.ResolveReviewApproval(ctx, in)
}

const nextRunRuntimeConfigSchema = `{"type":"object","properties":{"model":{"type":"string","minLength":1,"maxLength":128},"reasoningEffort":{"type":"string","minLength":1,"maxLength":64},"codexFastMode":{"type":"boolean"}},"required":["model","codexFastMode"],"additionalProperties":false}`

const reviewDecisionSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string"},"featureAreaId":{"type":"string"},"taskId":{"type":"string"},"decisionId":{"type":"string"},"requestId":{"type":"string","minLength":1,"maxLength":128},"option":{"type":"string","enum":["APPROVE","REJECT","REVISE"]},"reviewFeedback":{"type":"string","minLength":1,"maxLength":8192},"nextRunRuntimeConfig":` + nextRunRuntimeConfigSchema + `,"backendSelection":` + externalReviewBackendSchema + `,"executionInstructions":` + ExecutionInstructionsJSONSchema + `},"required":["aiSessionId","taskId","decisionId","option","requestId"],"allOf":[{"if":{"properties":{"option":{"const":"REVISE"}},"required":["option"]},"then":{"required":["reviewFeedback"],"not":{"anyOf":[{"required":["executionInstructions"]},{"required":["nextRunRuntimeConfig"]},{"required":["backendSelection"]}]}},"else":{"not":{"required":["reviewFeedback"]}}},{"if":{"properties":{"option":{"const":"REJECT"}},"required":["option"]},"then":{"not":{"anyOf":[{"required":["executionInstructions"]},{"required":["nextRunRuntimeConfig"]},{"required":["backendSelection"]}]}},"else":{"if":{"required":["nextRunRuntimeConfig"]},"then":{"required":["backendSelection"]},"else":{"not":{"required":["backendSelection"]}}}}],"additionalProperties":false}`

// WorkflowGuidanceMiddleware only locates the exact resource. Classification
// and capability decisions are read from Core after the command has committed.
func WorkflowGuidanceMiddleware(service BridgeService) sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
			result, err := next(ctx, method, req)
			if method == "tools/list" && err == nil {
				if list, ok := result.(*sdkmcp.ListToolsResult); ok {
					for i, original := range list.Tools {
						if isBootstrapTool(original.Name) || (confirmationTool(original.Name) || original.Name == "verification_continue") {
							continue
						}
						if isSurfaceBusinessTool(original.Name) {
							if !surfaceOwnsEnvelope(original.Name) {
								copyTool := *original
								copyTool.OutputSchema = surfaceEnvelopeOutputSchema(original.OutputSchema)
								list.Tools[i] = &copyTool
							}
							continue
						}
						if strings.HasPrefix(original.Name, "development_branch_") {
							continue
						}
						copyTool := *original
						t := &copyTool
						list.Tools[i] = t
						if t.OutputSchema != nil {
							b, _ := json.Marshal(t.OutputSchema)
							var schema map[string]any
							if json.Unmarshal(b, &schema) == nil {
								if props, ok := schema["properties"].(map[string]any); ok {
									props["workflowGuidance"] = json.RawMessage(wp.GuidanceJSONSchema)
									props["nextAction"] = json.RawMessage(wp.ActionJSONSchema)
									if _, exists := props["currentState"]; !exists {
										props["currentState"] = map[string]any{"type": "string"}
									}
									t.OutputSchema = schema
								}
							}
						}
					}
				}
				return result, err
			}
			if method == "tools/call" {
				if params, ok := req.GetParams().(*sdkmcp.CallToolParamsRaw); ok && (isBootstrapTool(params.Name) || confirmationTool(params.Name) || params.Name == "verification_continue") {
					return result, err
				}
				if params, ok := req.GetParams().(*sdkmcp.CallToolParamsRaw); ok && isSurfaceBusinessTool(params.Name) {
					return surfaceBusinessResult(ctx, service, params, result, err)
				}
			}
			if method != "tools/call" {
				return result, err
			}
			p, ok := service.(progressionService)
			if !ok {
				return result, err
			}
			params, ok := req.GetParams().(*sdkmcp.CallToolParamsRaw)
			if !ok {
				return result, err
			}
			var in map[string]any
			_ = json.Unmarshal(params.Arguments, &in)
			out, ok := result.(*sdkmcp.CallToolResult)
			if !ok || out == nil {
				return result, err
			}
			if out.IsError {
				for _, c := range out.Content {
					if t, ok := c.(*sdkmcp.TextContent); ok {
						var rejection wp.Error
						if json.Unmarshal([]byte(t.Text), &rejection) == nil && rejection.Code != "" && rejection.CurrentState != "" {
							out.StructuredContent = rejection
							return out, nil
						}
					}
				}
			}
			session := stringValue(in, "aiSessionId")
			if session == "" {
				return result, err
			}
			b, _ := json.Marshal(out.StructuredContent)
			var value map[string]any
			if json.Unmarshal(b, &value) != nil || value == nil {
				if !out.IsError {
					return result, err
				}
				value = map[string]any{"status": "REJECTED", "code": "PREREQUISITE_REJECTED"}
				for _, c := range out.Content {
					if t, ok := c.(*sdkmcp.TextContent); ok {
						value["message"] = t.Text
						break
					}
				}
			}
			// Execution Cards already carry their exact Core state and decision.
			// Do not splice current Task guidance into a selected Execution, even
			// when it was selected by Run or is awaiting recovery/finalization.
			if params.Name == "ui_workflow_state_get" && value["agentExecution"] != nil {
				var projection agentexecution.Projection
				raw, _ := json.Marshal(value["agentExecution"])
				if json.Unmarshal(raw, &projection) == nil && projection.ExecutionID == stringValue(value, "executionId") {
					return result, err
				}
			}
			area := stringValue(in, "featureAreaId")
			if v, ok := value["reviewApproval"].(map[string]any); ok && area == "" {
				area = stringValue(v, "featureAreaId")
			}
			if area == "" {
				area = stringValue(value, "featureAreaId")
			}
			if area == "" && params.Name == "feature_area_create" {
				area = stringValue(value, "id")
			}
			if area == "" {
				if v, ok := value["review"].(map[string]any); ok {
					if topic, ok := v["topic"].(map[string]any); ok {
						area = stringValue(topic, "id")
					}
				}
			}
			taskID := stringValue(in, "taskId")
			reviewID := stringValue(in, "reviewId")
			executionID := stringValue(in, "executionId")
			if area == "" && taskID != "" {
				if t, e := service.GetTask(ctx, session, taskID); e == nil {
					area = t.FeatureAreaID
				}
			}
			if area == "" && reviewID != "" {
				if v, e := service.GetReview(ctx, session, reviewID); e == nil {
					area = v.FeatureAreaID
				}
			}
			if area == "" && executionID != "" {
				if e, eErr := service.GetExecutionProposal(ctx, session, executionID); eErr == nil {
					if t, tErr := service.GetTask(ctx, session, e.TaskID); tErr == nil {
						area = t.FeatureAreaID
					}
				}
			}
			scope := wp.ResourceScope{AISessionID: session, FeatureAreaID: area, TaskID: taskID, ReviewID: reviewID, ExecutionID: executionID, RunID: stringValue(in, "runId"), OwnerType: stringValue(in, "ownerType"), OwnerID: stringValue(in, "ownerId")}
			if area == "" && taskID == "" && reviewID == "" && executionID == "" && scope.OwnerID == "" && scope.RunID == "" && params.Name != "task_create" {
				return result, err
			}
			var g wp.Guidance
			var e error
			if stringValue(value, "generation") != "" {
				// Core already returned a complete snapshot. A second read could
				// splice a newer phase into older IDs during an atomic handoff.
				e = json.Unmarshal(b, &g)
			} else if reader, ok := service.(interface {
				WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error)
			}); ok {
				g, e = reader.WorkflowGuidanceForResource(ctx, scope)
			} else {
				g, e = p.WorkflowGuidance(ctx, session, area)
			}
			if e != nil {
				value["workflowGuidance"] = wp.Guidance{RefreshRequired: true}
				out.StructuredContent = value
				return out, err
			}
			if out.IsError {
				g.Status = "REJECTED"
				if params.Name == "task_create" && g.CurrentState == "FEATURE_AREA_REQUIRED" {
					value["code"] = "FEATURE_AREA_REQUIRED"
				}
			}
			value["workflowGuidance"] = g
			value["currentState"] = g.CurrentState
			value["nextAction"] = g.NextAction
			// Existing entity status fields keep their meanings. The mutation envelope
			// status is in workflowGuidance; new Review resolution has it at top level.
			out.StructuredContent = value
			encoded, _ := json.Marshal(g)
			out.Content = append(out.Content, &sdkmcp.TextContent{Text: "Workflow guidance: " + string(encoded)})
			return out, err
		}
	}
}

const ReviewApprovalDescription = "Resolve the exact current Review confirmation on behalf of the user. Configured upper LLMs express user intent; Core enforces completion, ownership, staleness, lifecycle and idempotency. APPROVE freezes the exact definition. REVISE requires bounded reviewFeedback and atomically records CHANGES_REQUESTED, creates one successor Review on the same Task, freezes its inherited authorized inputs, and writes its durable start authority for automatic worker dispatch. It never accepts caller targets, backend, runtime, Review/Run IDs or outcome. REJECT abandons the Task. Historical Review results remain immutable. Reuse requestId and the exact payload after response loss."
const ApprovedExecutionStartDescription = "Start or resume the exact Task current Execution against its approved immutable definition. START_EXECUTION reserves implementation; START_VERIFICATION uses the existing verification reservation for the current candidate through this same execution_start entry. Copy expectedTarget (featureAreaId, taskDefinitionRevisionId, executionId, phase) from the observed current action when submitting from a Task surface; phase is IMPLEMENTATION_READY or VERIFICATION_READY. Core owns candidate and Run idempotency. For the first start after a v2 CODING_AGENT definition-only approval, read workspace_get with exact taskId and include=[TASK,RUNTIME_CONFIG], present its scoped runtimeOptions/backendCandidates, then supply explicit runtimeConfig and backend together with stable requestId and expectedTarget copied verbatim from result.taskView.executionStartTarget, including primaryMemberId and approvedWorksetHash. This selection branch is IMPLEMENTATION_READY only and excludes executionInstructions; never inherit the Review runtime as authorization. Preserve the complete payload on response-loss replay. Existing frozen Executions, legacy starts and Verification keep the branch without new selection. For instruction-only finalization of an eligible initial, not-yet-dispatched CODING_AGENT candidate, include executionInstructions plus stable requestId and complete expectedTarget; Core creates a source-linked immutable revision before reservation. Omit the object when the approved candidates are already executable. It cannot revise goal, scope, mode or runtime config, or reopen dispatched/terminal work. Replay must preserve the exact request and texts. For explicit CODING_AGENT definitions, initial APPROVE freezes the definition into IMPLEMENTATION_READY and this call starts work. DIRECT_FILE approval has its own revision-bound file authority and never uses this Codex start entry. Missing legacy implementationMode must not default to Codex. Verification completion is not Task Acceptance. Follow returned nextAction; WAIT authorizes no mutation or continuous polling."
const WorkflowGuidanceGetDescription = "Read current authoritative workflow state, exact Approval and nextAction. Pure read, including after replay or response loss. WAIT actions mean no advancement mutation is available."

func (s *StaticService) WorkflowGuidanceForResource(ctx context.Context, scope wp.ResourceScope) (wp.Guidance, error) {
	if s.control == nil {
		return wp.Guidance{}, errors.New("workflow unavailable")
	}
	return s.control.WorkflowGuidanceForResource(ctx, scope)
}

func (s *StaticService) StartTaskExecution(ctx context.Context, session, taskID string) (executionproposal.StartResult, error) {
	if s.control == nil {
		return executionproposal.StartResult{}, errors.New("workflow unavailable")
	}
	return s.control.StartTaskExecution(ctx, session, taskID)
}

const TaskAcceptanceDescription = "Compatibility Task Acceptance endpoint. For the current verification workflow use execution_prepare with CONTINUE_TASK, COMPLETE_TASK or DEFER_TASK and the exact shared Task action. COMPLETE_TASK includes the explicit per-repository commit/merge plan and finishes only after integration. Legacy ACCEPT records acceptance only; it does not authorize merge or establish integrated completion. REJECT cancels the Task. REQUEST_CHANGES requires caller-authored complete nextWorkSpec and nextVerificationSpec, including exact Codex instructions, and creates one immutable revision and successor Execution on the same Task. REQUEST_REVERIFICATION accepts no successor specs and creates a verification-only Execution on the same revision. RETRY_IMPLEMENTATION explicitly continues a safely finalized failed/interrupted Coding Agent Execution without a new Review. It accepts no successor specs; omitted executionInstructions reuse the approved revision, while explicit complete executionInstructions materialize only changed canonical instruction fields in an immutable revision. Omitted nextRunRuntimeConfig inherits the predecessor configuration exactly. After a resolved RECOVERED_PARTIAL assessment, supply recoveryStatusId and exact recoveryContinuationInstruction instead of executionInstructions: Core preserves the approved revision and records a separate immutable Run instruction. No scope/configuration changes are allowed on that path. The receipt creates a CONFIRMED successor; call execution_start with its exact target to reserve and dispatch a new Implementation Run. Copy exact taskId, featureAreaId, executionId, taskDefinitionRevisionId and decisionId from the shared Task action. Reuse the same requestId and payload after response loss; stale targets and changed payloads fail closed."

var TaskAcceptanceInputJSONSchema = `{"type":"object","properties":{"recoveryStatusId":{"type":"string","minLength":1},"recoveryContinuationInstruction":{"type":"string","minLength":1,"maxLength":65536},"aiSessionId":{"type":"string","minLength":1},"taskId":{"type":"string","minLength":1},"featureAreaId":{"type":"string","minLength":1},"executionId":{"type":"string","minLength":1},"taskDefinitionRevisionId":{"type":"string","minLength":1},"decisionId":{"type":"string","pattern":"^TASK_ACCEPTANCE:.+"},"requestId":{"type":"string","minLength":1,"maxLength":128},"option":{"enum":["ACCEPT","REJECT","REQUEST_CHANGES","REQUEST_REVERIFICATION","RETRY_IMPLEMENTATION"]},"executionInstructions":` + ExecutionInstructionsJSONSchema + `,"nextWorkSpec":` + WorkSpecInputJSONSchema + `,"nextVerificationSpec":` + VerificationSpecInputJSONSchema + `,"nextRunRuntimeConfig":` + nextRunRuntimeConfigSchema + `},"required":["aiSessionId","taskId","featureAreaId","executionId","taskDefinitionRevisionId","decisionId","requestId","option"],"oneOf":[{"properties":{"option":{"const":"REQUEST_CHANGES"}},"required":["nextWorkSpec","nextVerificationSpec"]},{"properties":{"option":{"enum":["ACCEPT","REJECT","REQUEST_REVERIFICATION","RETRY_IMPLEMENTATION"]}},"not":{"anyOf":[{"required":["nextWorkSpec"]},{"required":["nextVerificationSpec"]}]}}],"allOf":[{"if":{"required":["executionInstructions"]},"then":{"properties":{"option":{"const":"RETRY_IMPLEMENTATION"}}}},{"if":{"required":["nextRunRuntimeConfig"]},"then":{"properties":{"option":{"enum":["REQUEST_CHANGES","REQUEST_REVERIFICATION","RETRY_IMPLEMENTATION"]}}}}],"additionalProperties":false}`

func (s *StaticService) StartTaskExecutionExpected(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation) (executionproposal.StartResult, error) {
	if s.control == nil {
		return executionproposal.StartResult{}, errors.New("workflow unavailable")
	}
	return s.control.StartTaskExecutionExpected(ctx, sessionID, taskID, expected)
}
