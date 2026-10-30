package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SurfaceBusinessToolNames is the shared canonical product catalog. Local MCP
// retains it; ChatGPT replaces direct creation/start with purpose confirmation
// and separately registers presentation tools.
// Catalog membership includes bootstrap identity and does not imply envelope wrapping.
var SurfaceBusinessToolNames = []string{
	"bridge_diagnostics", "session_initialize", "project_list", "session_binding_get", "project_open", "workspace_get", "work_list", "operation_get", "project_inspection_recovery_continue",
	"development_branch_create", "development_branch_select", "feature_area_create", "feature_area_lifecycle",
	"task_create", "task_review_request", "task_review_start", "review_result_submit", "review_result_get", "run_analysis_get", "run_analysis_submit", "review_access_get", "review_access_prepare", "review_evidence_get", "decision_respond", "execution_start", "operation_cancel", "successor_execution_prepare", "runtime_request_respond", "runtime_request_user_decision_submit", "runtime_request_user_decision_prepare_hash", "runtime_request_recovery", "recovery_status_prepare", "recovery_status_start", "recovery_status_read", "recovery_status_resolve", "recovery_purpose_repair",
	"agent_context_reset", "agent_operation_recover", "feature_area_submit", "task_commit_retry", "task_commit_reprepare", "task_commit_retry_candidates", "feature_area_merge_reprepare", "feature_area_merge_retry_candidates",
	"task_stop_and_revise",
	"repository_unlink_prepare", "repository_unlink_read", "repository_unlink_commit",
	"file_implementation_result_get", "file_implementation_result_submit", "file_implementation_accept", "direct_file_enable", "direct_file_disable", "direct_file_reconcile_task_authorization", "file_list", "file_read", "file_write", "document_create", "repository_search", "repository_file_read",
}

func RegisterSurfaceBusinessTools(server *sdkmcp.Server, service BridgeService) {
	addExternalTool(server, "task_stop_and_revise", "Revise canceled Task", "Explicitly return a NO_RUN_SUCCESSOR_REQUIRED Task to Review after its exact admitted Execution cancellation is settled. Supply the current predecessor and definition revision from workspace_get. This changes only the Task phase, preserves the canceled owner, and creates no revision, receipt or runtime. A repeat reads the same REVIEW_REQUIRED identity; after Review advances, refresh instead. Prepare and confirm a new Review separately.", noRunRevisionInputSchema, runtimeDTOSchema[task.Task](), stateChangingAnnotations("Revise canceled Task", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in noRunRevisionInput) (*sdkmcp.CallToolResult, task.Task, error) {
		out, err := noRunRevisionCore(ctx, service, in)
		return nil, out, err
	})
	server.AddReceivingMiddleware(SessionScopeMiddleware(service))
	registerExternalWorkflowTools(server, service, reviewStartInputSchema())
	registerCanonicalInspectionTools(server, service)
	registerSessionInitialize(server, service)
	registerSessionBindingGet(server, service)
	registerRepositoryUnlinkTools(server, service)
	registerRepositoryFileReadTool(server, service)
	registerTaskReviewRequest(server, service)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "workspace_get", Description: "SILENT authoritative workspace read for internal analysis and component refresh; it never inserts a Card or changes workflow state. Read compact Project workspace and exact selected Task, current phase, real pending decisions, blockers and nextAction. The exact Task exposes implementationMode and current directFile workflow, revision, workspace, repository, allowed operations, approved scope and readiness; reads never create or enable authorization. Explicit Task and FeatureArea assertions must match. Extra partitions require closed include values; partitions retain independent generations and errors, not an atomic snapshot.", InputSchema: json.RawMessage(WorkspaceGetInputSchema), OutputSchema: json.RawMessage(WorkspaceGetOutputSchema), Annotations: toolAnnotations("Read workspace", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in WorkspaceGetInput) (*sdkmcp.CallToolResult, WorkspaceGetOutput, error) {
		out, err := WorkspaceGetCore(ctx, service, in)
		return nil, out, err
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "work_list", Description: "SILENT bounded list for internal analysis and component refresh; it never inserts a Card or changes workflow state. List accurately scoped FeatureAreas (kind FEATURE_AREA), semantic Tasks (kind TASK) or existing Review/Development/Inspection operations (kind OPERATION). Review is never a pseudo-Task.", InputSchema: json.RawMessage(WorkListInputSchema), OutputSchema: json.RawMessage(WorkListOutputSchema), Annotations: toolAnnotations("List work", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in WorkListInput) (*sdkmcp.CallToolResult, WorkListOutput, error) {
		out, err := WorkListCore(ctx, service, in)
		return nil, out, err
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "operation_get", Description: "SILENT exact operation read for internal analysis and component refresh; it never inserts a Card or changes workflow state. Copy target identity from workspace_get, work_list, or an existing workspace_show result; do not mechanically call workspace_get first. Request SOURCE, RESULT, EVIDENCE, ACTIVITY or RUNTIME_REQUESTS as needed; Review proposal, Verification evidence, AgentClaim and telemetry remain distinct sources. Never invent IDs.", InputSchema: json.RawMessage(OperationGetInputSchema), OutputSchema: json.RawMessage(OperationGetOutputSchema), Annotations: toolAnnotations("Read operation", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in OperationGetInput) (*sdkmcp.CallToolResult, OperationGetOutput, error) {
		out, err := OperationGetCore(ctx, service, in)
		return nil, out, err
	})
	registerDevelopmentBranchCreate(server, service)
	addExternalTool(server, "development_branch_select", "Select Development Branch", DevelopmentBranchSelectDescription, DevelopmentBranchSelectInputSchema, DevelopmentCycleOutputSchema, lifecycleAnnotations("Select Development Branch", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in DevelopmentBranchSelectInput) (*sdkmcp.CallToolResult, developmentcycle.Cycle, error) {
		out, err := DevelopmentBranchSelectCore(ctx, service, in)
		return nil, out, err
	})
	registerFeatureAreaCommit(server, service)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "feature_area_lifecycle", Description: "Apply ACTIVATE, DECOMPOSE or explicitly confirmed RETIRE through canonical Core lifecycle methods and durable request receipts. DECOMPOSE requires children and excludes confirmation. RETIRE requires confirmed=true and records durable stop intent, then converges through canonical cancellation; it is destructive and cannot reopen closed work.", InputSchema: json.RawMessage(SurfaceFeatureAreaLifecycleInputSchema), Annotations: lifecycleAnnotations("Change FeatureArea lifecycle", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in SurfaceFeatureAreaLifecycleInput) (*sdkmcp.CallToolResult, SurfaceFeatureAreaLifecycleOutput, error) {
		out, err := SurfaceFeatureAreaLifecycleCore(ctx, service, in)
		return nil, out, publicToolError(err)
	})
	addExternalTool(server, "agent_context_reset", "Reset Agent Context", "Rotate the canonical context of an idle FeatureArea. Prior context stays historical. This does not recover unsafe operations; use agent_operation_recover for that separate Core protocol.", FeatureAreaLifecycleInputSchema, AgentContextResetOutputSchema, lifecycleAnnotations("Reset Agent Context", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in FeatureAreaLifecycleInput) (*sdkmcp.CallToolResult, AgentContextResetOutput, error) {
		err := service.ResetFeatureAreaAgentContext(ctx, in.AISessionID, in.FeatureAreaID, in.RequestID)
		return nil, AgentContextResetOutput{FeatureAreaID: in.FeatureAreaID, RequestID: in.RequestID}, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "decision_respond", Description: ReviewApprovalDescription + " " + TaskAcceptanceDescription + " Required kind separates REVIEW_APPROVAL from TASK_ACCEPTANCE; runtime permission uses runtime_request_respond. Copy the exact projected target and wait for an explicit user choice. No response or display implies approval. DIRECT_FILE APPROVE returns its original receipt and revision-bound directFile authority in the model-visible structured reviewApproval result; historical approval success and current file-access state are separate facts.", InputSchema: json.RawMessage(SurfaceDecisionRespondInputSchema), Annotations: stateChangingAnnotations("Respond to business decision", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in SurfaceDecisionRespondInput) (*sdkmcp.CallToolResult, SurfaceDecisionRespondOutput, error) {
		out, err := SurfaceDecisionRespondCore(ctx, service, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "execution_start", Description: ApprovedExecutionStartDescription, InputSchema: json.RawMessage(surfaceExecutionStartInputSchema), OutputSchema: json.RawMessage(runtimeDTOSchema[SurfaceExecutionStartOutput]()), Annotations: stateChangingAnnotations("Start Task execution or verification", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in SurfaceExecutionStartInput) (*sdkmcp.CallToolResult, SurfaceExecutionStartOutput, error) {
		started, err := SurfaceExecutionStartCore(ctx, service, in)
		out := SurfaceExecutionStartOutput{StartResult: started, ReservationCommitted: started.Proposal.ID != "" || started.Run.ID != "" || started.Receipt != nil}
		if err != nil && out.ReservationCommitted {
			out.DispatchError = publicToolError(err).Error()
			out.RefreshRequired = true
			return &sdkmcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, publicToolError(err)
	})
	addExternalTool(server, "operation_cancel", "Cancel exact operation", OperationCancelDescription+" Copy the exact cancellation capability. Use either ownerType/ownerId or executionId, never both. ACK is not terminal; Inspection cancellation uses its exact probe owner and durable cancellation ledger.", SurfaceOperationCancelInputSchema, ExecutionCancelReceiptSchema, lifecycleAnnotations("Cancel exact operation", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in executioncancel.Input) (*sdkmcp.CallToolResult, executioncancel.Receipt, error) {
		out, err := ExecutionCancelCore(ctx, service, in)
		return nil, out, err
	})
	addExternalTool(server, "successor_execution_prepare", "Prepare successor execution", "Submit the exact Core successor command for a canceled admitted Execution with no Run. Use the projected Task state and predecessor identity; replay the same requestId and payload after response loss.", successorPrepareInputSchema, runtimeDTOSchema[wp.SuccessorCommandReceipt](), stateChangingAnnotations("Prepare successor execution", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in successorPrepareInput) (*sdkmcp.CallToolResult, wp.SuccessorCommandReceipt, error) {
		out, err := successorPrepareCore(ctx, service, in)
		return nil, out, err
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "runtime_request_respond", Description: CodexRequestResolveDescription + " CODEX_REQUEST requires an existing durable UserDecision reference, never raw response JSON. RUNTIME_APPROVAL is a separate permission decision: use exact resolution identity, expectedState=PENDING, request payload SHA256 and the user-selected native response for a native runtimeRequest; use approvalId/decision only for a real legacy Approval entity. Never mix them, infer approval, or route permission as a semantic UserDecision.", InputSchema: json.RawMessage(SurfaceRuntimeRequestInputSchema), OutputSchema: json.RawMessage(SurfaceRuntimeRequestOutputSchema), Annotations: stateChangingAnnotations("Respond to runtime request", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in SurfaceRuntimeRequestInput) (*sdkmcp.CallToolResult, SurfaceRuntimeRequestOutput, error) {
		out, err := SurfaceRuntimeRequestCore(ctx, service, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "runtime_request_user_decision_submit", Description: ExplicitUserDecisionDescription + " Submit the immutable explicit user choice for the exact CODEX_REQUEST. RESPOND continues only the current approved scope. STOP_AND_REVISE and CANCEL_OPERATION invoke canonical cancellation; no successor is started. Stable submissionRequestId and payloadSHA256 identify exact replay. Use read-only runtime_request_user_decision_prepare_hash to obtain the canonical hash without recording or dispatching anything. Hash excludes the server-injected submittedBy field; never supply an actor. Read operation_get RUNTIME_REQUESTS first.", InputSchema: json.RawMessage(AgentUserDecisionInputSchema), OutputSchema: json.RawMessage(AgentUserDecisionOutputSchema), Annotations: stateChangingAnnotations("Submit explicit user runtime decision", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in AgentUserDecisionInput) (*sdkmcp.CallToolResult, controlapi.AgentUserDecisionResult, error) {
		out, err := AgentUserDecisionCore(ctx, service, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "runtime_request_user_decision_prepare_hash", Description: "Pure read: compute the canonical payloadSHA256 for an exact user decision input after scoped runtime-request lookup. Never records a UserDecision or dispatches any response. Use the returned exact decision input unchanged for submission only after an explicit current user answer. A prepared hash is not consent; do not infer or author the user's semantic answer.", InputSchema: json.RawMessage(AgentUserDecisionPrepareHashInputSchema), OutputSchema: json.RawMessage(runtimeDTOSchema[AgentUserDecisionPreparedHash]()), Annotations: toolAnnotations("Prepare exact user decision checksum", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in AgentUserDecisionInput) (*sdkmcp.CallToolResult, AgentUserDecisionPreparedHash, error) {
		out, err := AgentUserDecisionPrepareHashCore(ctx, service, in)
		return nil, out, publicToolError(err)
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "runtime_request_recovery", Description: "Handle RESPONSE_DISPOSITION_UNKNOWN without another answer: CONTINUE_WAITING is pure read; CANCEL_OPERATION and STOP_AND_REVISE use original canonical cancellation and wait for quiescence. STOP_AND_REVISE never starts a successor; use existing purpose-scoped confirmation only after authority releases. Never submit an answer again, overwrite UserDecision, force resend or infer consumption from terminal status.", InputSchema: json.RawMessage(AgentRequestRecoveryInputSchema), OutputSchema: json.RawMessage(runtimeDTOSchema[controlapi.AgentRequestRecoveryResult]()), Annotations: stateChangingAnnotations("Handle unknown answer disposition", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in AgentRequestRecoveryInput) (*sdkmcp.CallToolResult, controlapi.AgentRequestRecoveryResult, error) {
		out, err := AgentRequestRecoveryCore(ctx, service, in)
		return nil, out, publicToolError(err)
	})
	RegisterRecoveryStatusTools(server, service)
	RegisterDirectFileTools(server, service)
	RegisterUpperReviewTools(server, service)
	registerRunAnalysisTools(server, service)
	registerFileImplementationResults(server, service)
}

// Tagged objects remain typed here and are closed/disjoint in the wire schema.
// There is no arbitrary action payload or parallel business state machine.
type SurfaceFeatureAreaLifecycleInput struct {
	AISessionID   string                        `json:"aiSessionId"`
	FeatureAreaID string                        `json:"featureAreaId"`
	RequestID     string                        `json:"requestId"`
	Action        string                        `json:"action"`
	Children      []featurearea.ChildDefinition `json:"children,omitempty"`
	Confirmed     *bool                         `json:"confirmed,omitempty"`
}
type SurfaceFeatureAreaLifecycleOutput struct {
	Action        string                      `json:"action"`
	FeatureArea   *ExternalTopicOutput        `json:"featureArea,omitempty"`
	Decomposition *FeatureAreaDecomposeOutput `json:"decomposition,omitempty"`
}

func SurfaceFeatureAreaLifecycleCore(ctx context.Context, service BridgeService, in SurfaceFeatureAreaLifecycleInput) (SurfaceFeatureAreaLifecycleOutput, error) {
	out := SurfaceFeatureAreaLifecycleOutput{Action: in.Action}
	provider, ok := service.(featureAreaLifecycleBridge)
	if !ok {
		return out, errors.New("FeatureArea lifecycle unavailable")
	}
	if strings.TrimSpace(in.AISessionID) == "" || strings.TrimSpace(in.FeatureAreaID) == "" || strings.TrimSpace(in.RequestID) == "" {
		return out, errors.New("exact Session, FeatureArea and requestId required")
	}
	if in.Action != "DECOMPOSE" && in.Children != nil || in.Action != "RETIRE" && in.Confirmed != nil {
		return out, errors.New("lifecycle fields do not match action")
	}
	if in.Action == "DECOMPOSE" {
		if len(in.Children) == 0 {
			return out, errors.New("DECOMPOSE requires children")
		}
		v, e := provider.DecomposeFeatureArea(ctx, featurearea.DecomposeInput{AISessionID: in.AISessionID, FeatureAreaID: in.FeatureAreaID, RequestID: in.RequestID, Children: in.Children})
		if e != nil {
			return out, e
		}
		d := FeatureAreaDecomposeOutput{Parent: externalTopicOutputFor(v.Parent), Children: []ExternalTopicOutput{}}
		for _, c := range v.Children {
			d.Children = append(d.Children, externalTopicOutputFor(c))
		}
		out.Decomposition = &d
		return out, nil
	}
	var v featurearea.FeatureArea
	var e error
	switch in.Action {
	case "ACTIVATE":
		v, e = provider.ActivateFeatureArea(ctx, in.AISessionID, in.FeatureAreaID, in.RequestID)
	case "RETIRE":
		if in.Confirmed == nil || !*in.Confirmed {
			return out, errors.New("RETIRE requires explicit confirmed=true")
		}
		v, e = provider.RetireFeatureArea(ctx, in.AISessionID, in.FeatureAreaID, in.RequestID)
	default:
		return out, errors.New("unsupported lifecycle action")
	}
	if e == nil {
		p := externalTopicOutputFor(v)
		out.FeatureArea = &p
	}
	return out, e
}

type SurfaceDecisionRespondInput struct {
	RecoveryStatusID                string                             `json:"recoveryStatusId,omitempty"`
	RecoveryContinuationInstruction string                             `json:"recoveryContinuationInstruction,omitempty"`
	ExecutionInstructions           *task.ExecutionInstructions        `json:"executionInstructions,omitempty"`
	Kind                            string                             `json:"kind"`
	AISessionID                     string                             `json:"aiSessionId"`
	TaskID                          string                             `json:"taskId"`
	FeatureAreaID                   string                             `json:"featureAreaId,omitempty"`
	ReviewID                        string                             `json:"reviewId,omitempty"`
	ExecutionID                     string                             `json:"executionId,omitempty"`
	TaskDefinitionRevisionID        string                             `json:"taskDefinitionRevisionId,omitempty"`
	DecisionID                      string                             `json:"decisionId"`
	RequestID                       string                             `json:"requestId"`
	Option                          string                             `json:"option"`
	NextWorkSpec                    *task.WorkSpec                     `json:"nextWorkSpec,omitempty"`
	NextVerificationSpec            *task.VerificationSpec             `json:"nextVerificationSpec,omitempty"`
	NextRunRuntimeConfig            *codexruntime.NextRunRuntimeConfig `json:"nextRunRuntimeConfig,omitempty"`
	BackendSelection                *codexconfig.BackendSelectionInput `json:"backendSelection,omitempty"`
}
type SurfaceDecisionRespondOutput struct {
	Kind           string                       `json:"kind"`
	Guarantee      string                       `json:"guarantee"`
	ReviewApproval *wp.Resolution               `json:"reviewApproval,omitempty"`
	TaskAcceptance *wp.TaskAcceptanceResolution `json:"taskAcceptance,omitempty"`
}

func SurfaceDecisionRespondCore(ctx context.Context, service BridgeService, in SurfaceDecisionRespondInput) (SurfaceDecisionRespondOutput, error) {
	out := SurfaceDecisionRespondOutput{Kind: in.Kind, Guarantee: "DURABLE_REQUEST_RECEIPT"}
	if in.ExecutionInstructions != nil && !(in.Kind == "REVIEW_APPROVAL" && in.Option == "APPROVE" || in.Kind == "TASK_ACCEPTANCE" && in.Option == "RETRY_IMPLEMENTATION") {
		return out, errors.New("executionInstructions require REVIEW_APPROVAL APPROVE or TASK_ACCEPTANCE RETRY_IMPLEMENTATION")
	}
	if strings.TrimSpace(in.TaskID) == "" || strings.TrimSpace(in.AISessionID) == "" || strings.TrimSpace(in.RequestID) == "" {
		return out, errors.New("exact Session, Task and requestId required")
	}
	if !strings.HasPrefix(in.DecisionID, in.Kind+":") {
		return out, errors.New("decision kind and exact decisionId mismatch")
	}
	switch in.Kind {
	case "REVIEW_APPROVAL":
		if in.RecoveryStatusID != "" || in.RecoveryContinuationInstruction != "" || in.NextWorkSpec != nil || in.NextVerificationSpec != nil || in.ExecutionID != "" || in.TaskDefinitionRevisionID != "" {
			return out, errors.New("Review Approval rejects acceptance fields")
		}
		if in.Option != "APPROVE" && in.Option != "REVISE" && in.Option != "REJECT" {
			return out, errors.New("invalid Review Approval option")
		}
		var backend *codexconfig.BackendSelection
		if in.BackendSelection != nil {
			configs, err := service.ListCodexConfigs(ctx, in.AISessionID, aicontext.ListOptions{Limit: 100})
			if err != nil {
				return out, err
			}
			for _, config := range configs {
				if config.Enabled && config.ID == in.BackendSelection.ConfigID && config.Fingerprint() == in.BackendSelection.ConfigFingerprint {
					selection := config.Selection("local-mcp-decision")
					backend = &selection
					break
				}
			}
			if backend == nil {
				return out, errors.New("invalid backend selection")
			}
		}
		v, e := ReviewApprovalCore(ctx, service, wp.ResolveInput{ExecutionInstructions: in.ExecutionInstructions, AISessionID: in.AISessionID, TaskID: in.TaskID, FeatureAreaID: in.FeatureAreaID, ReviewID: in.ReviewID, DecisionID: in.DecisionID, Option: in.Option, RequestID: in.RequestID, NextRunRuntimeConfig: in.NextRunRuntimeConfig, BackendSelection: backend})
		if e == nil {
			out.ReviewApproval = &v
		}
		return out, e
	case "TASK_ACCEPTANCE":
		if in.ReviewID != "" || in.FeatureAreaID == "" || in.ExecutionID == "" || in.TaskDefinitionRevisionID == "" {
			return out, errors.New("Task Acceptance requires exact FeatureArea, Execution and revision; rejects Review identity")
		}
		if in.Option == "REQUEST_CHANGES" {
			if in.NextWorkSpec == nil || in.NextVerificationSpec == nil {
				return out, errors.New("REQUEST_CHANGES requires complete nextWorkSpec and nextVerificationSpec")
			}
		} else if in.NextWorkSpec != nil || in.NextVerificationSpec != nil {
			return out, errors.New("successor specs are permitted only for REQUEST_CHANGES")
		}
		if in.Option != "ACCEPT" && in.Option != "REJECT" && in.Option != "REQUEST_CHANGES" && in.Option != "REQUEST_REVERIFICATION" && in.Option != "RETRY_IMPLEMENTATION" {
			return out, errors.New("invalid Task Acceptance option")
		}
		p, ok := service.(wp.TaskAcceptanceStore)
		if !ok {
			return out, errors.New("Task acceptance unavailable")
		}
		v, e := p.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{RecoveryStatusID: in.RecoveryStatusID, RecoveryContinuationInstruction: in.RecoveryContinuationInstruction, ExecutionInstructions: in.ExecutionInstructions, AISessionID: in.AISessionID, TaskID: in.TaskID, FeatureAreaID: in.FeatureAreaID, ExecutionID: in.ExecutionID, TaskDefinitionRevisionID: in.TaskDefinitionRevisionID, DecisionID: in.DecisionID, Option: in.Option, RequestID: in.RequestID, NextWorkSpec: in.NextWorkSpec, NextVerificationSpec: in.NextVerificationSpec, NextRunRuntimeConfig: in.NextRunRuntimeConfig})
		if e == nil {
			out.TaskAcceptance = &v
		}
		return out, e
	default:
		return out, errors.New("unsupported business decision kind")
	}
}

// Dispatch may fail after Core committed a candidate/Run reservation. Keep the
// exact reservation visible while preserving the failed delivery fact.
type SurfaceExecutionStartOutput struct {
	executionproposal.StartResult
	ReservationCommitted bool   `json:"reservationCommitted"`
	DispatchError        string `json:"dispatchError,omitempty"`
	RefreshRequired      bool   `json:"refreshRequired,omitempty"`
}

type SurfaceExecutionStartInput struct {
	ExecutionInstructions *task.ExecutionInstructions        `json:"executionInstructions,omitempty"`
	RuntimeConfig         *codexruntime.NextRunRuntimeConfig `json:"runtimeConfig,omitempty"`
	Backend               *codexconfig.BackendSelectionInput `json:"backend,omitempty"`
	RequestID             string                             `json:"requestId,omitempty"`
	ExpectedTarget        *wp.TaskExecutionExpectation       `json:"expectedTarget,omitempty"`
	AISessionID           string                             `json:"aiSessionId"`
	TaskID                string                             `json:"taskId"`
}

func SurfaceExecutionStartCore(ctx context.Context, service BridgeService, in SurfaceExecutionStartInput) (executionproposal.StartResult, error) {
	if in.RuntimeConfig != nil || in.Backend != nil {
		if in.RuntimeConfig == nil || in.Backend == nil || in.ExpectedTarget == nil || strings.TrimSpace(in.RequestID) == "" || in.ExecutionInstructions != nil {
			return executionproposal.StartResult{}, errors.New("explicit approved start requires runtimeConfig, backend, exact expectedTarget and requestId, without executionInstructions")
		}
		p, ok := service.(interface {
			StartApprovedTaskExecution(context.Context, controlapi.ApprovedTaskStartInput) (executionproposal.StartResult, error)
		})
		if !ok {
			return executionproposal.StartResult{}, errors.New("explicit approved Task start unavailable")
		}
		return p.StartApprovedTaskExecution(ctx, controlapi.ApprovedTaskStartInput{AISessionID: in.AISessionID, TaskID: in.TaskID, RequestID: in.RequestID, ExpectedTarget: *in.ExpectedTarget, RuntimeConfig: *in.RuntimeConfig, Backend: *in.Backend})
	}
	if in.ExecutionInstructions != nil {
		if in.ExpectedTarget == nil || strings.TrimSpace(in.RequestID) == "" {
			return executionproposal.StartResult{}, errors.New("instruction submission requires exact expectedTarget and requestId")
		}
		p, ok := service.(interface {
			StartTaskExecutionInstructions(context.Context, string, string, wp.TaskExecutionExpectation, string, task.ExecutionInstructions) (executionproposal.StartResult, error)
		})
		if !ok {
			return executionproposal.StartResult{}, errors.New("execution instruction submission unavailable")
		}
		return p.StartTaskExecutionInstructions(ctx, in.AISessionID, in.TaskID, *in.ExpectedTarget, in.RequestID, *in.ExecutionInstructions)
	}
	if in.RequestID != "" {
		return executionproposal.StartResult{}, errors.New("requestId on execution_start requires complete explicit runtime selection or executionInstructions; unchanged starts use the original reservation contract")
	}

	if in.ExpectedTarget != nil {
		p, ok := service.(interface {
			StartTaskExecutionExpected(context.Context, string, string, wp.TaskExecutionExpectation) (executionproposal.StartResult, error)
		})
		if !ok {
			return executionproposal.StartResult{}, errors.New("Exact Task progression unavailable")
		}
		return p.StartTaskExecutionExpected(ctx, in.AISessionID, in.TaskID, *in.ExpectedTarget)
	}
	p, ok := service.(interface {
		StartTaskExecution(context.Context, string, string) (executionproposal.StartResult, error)
	})
	if !ok {
		return executionproposal.StartResult{}, errors.New("workflow unavailable")
	}
	// Core ReserveTaskExecution owns both initial implementation and the existing
	// START_VERIFICATION reservation of the exact current candidate.
	return p.StartTaskExecution(ctx, in.AISessionID, in.TaskID)
}

type SurfaceRuntimeRequestInput struct {
	Kind        string                   `json:"kind"`
	AISessionID string                   `json:"aiSessionId"`
	Resolution  *agentruntime.Resolution `json:"resolution,omitempty"`
	ApprovalID  string                   `json:"approvalId,omitempty"`
	Decision    string                   `json:"decision,omitempty"`
	Permissions json.RawMessage          `json:"permissions,omitempty"`
}
type SurfaceRuntimeRequestOutput struct {
	Kind           string                 `json:"kind"`
	Guarantee      string                 `json:"guarantee"`
	Runtime        *agentruntime.Snapshot `json:"runtime,omitempty"`
	Approval       *ApprovalOutput        `json:"approval,omitempty"`
	EntityDecision *EntityDecisionOutput  `json:"entityDecision,omitempty"`
}

func SurfaceRuntimeRequestCore(ctx context.Context, service BridgeService, in SurfaceRuntimeRequestInput) (SurfaceRuntimeRequestOutput, error) {
	out := SurfaceRuntimeRequestOutput{Kind: in.Kind}
	switch in.Kind {
	case "CODEX_REQUEST":
		if in.Resolution == nil || in.Resolution.UserDecisionID == "" || in.Resolution.RuntimeRequestID == "" || len(in.Resolution.Response) != 0 || in.ApprovalID != "" || in.Decision != "" || in.Permissions != nil {
			return out, errors.New("CODEX_REQUEST requires its exact identity and durable UserDecision reference; raw response is prohibited")
		}
		p, ok := service.(agentRuntimeService)
		if !ok {
			return out, errors.New("agent runtime unavailable")
		}
		v, e := p.ResolveAgentRequestForSession(ctx, in.AISessionID, *in.Resolution)
		if e == nil {
			out.Runtime = &v
			out.Guarantee = "EXACT_RUNTIME_REQUEST_STATE"
		}
		return out, e
	case "RUNTIME_APPROVAL":
		if in.Resolution != nil {
			if in.ApprovalID != "" || in.Decision != "" || in.Permissions != nil {
				return out, errors.New("native approval cannot mix legacy Approval fields")
			}
			v, e := resolveNativeApproval(ctx, service, in.AISessionID, *in.Resolution)
			if e == nil {
				out.Runtime = &v
				out.Guarantee = "EXACT_RUNTIME_REQUEST_STATE"
			}
			return out, e
		}
		if in.ApprovalID == "" {
			return out, errors.New("RUNTIME_APPROVAL requires its exact Approval entity")
		}
		if in.Decision != "GRANT_REQUESTED_PERMISSIONS" && in.Permissions != nil {
			return out, errors.New("permissions require GRANT_REQUESTED_PERMISSIONS")
		}
		v, entity, e := ResolveApprovalDecisionCore(ctx, service, in.AISessionID, in.ApprovalID, approval.Decision{Type: approval.DecisionType(in.Decision), Permissions: in.Permissions})
		if e == nil {
			p := approvalOutput(v)
			out.Approval = &p
			out.EntityDecision = &entity
			out.Guarantee = "AUTHORITATIVE_ENTITY_STATE"
		}
		return out, e
	default:
		return out, errors.New("unsupported runtime request kind")
	}
}

// unionSchema merges named typed schemas without weakening their individual
// additionalProperties/required constraints. The branches are discriminated.
func surfaceTaggedUnion(tag string, branches map[string]string) string {
	properties := map[string]any{}
	variants := []any{}
	keys := make([]string, 0, len(branches))
	for k := range branches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, kind := range keys {
		raw := branches[kind]
		var branch map[string]any
		if err := json.Unmarshal([]byte(raw), &branch); err != nil {
			panic(err)
		}
		p := branch["properties"].(map[string]any)
		p[tag] = map[string]any{"const": kind}
		r, _ := branch["required"].([]any)
		branch["required"] = append(r, tag)
		branch["additionalProperties"] = false
		variants = append(variants, branch)
		for k, v := range p {
			if old, ok := properties[k]; ok {
				properties[k] = map[string]any{"anyOf": []any{old, v}}
			} else {
				properties[k] = v
			}
		}
	}
	properties[tag] = map[string]any{"type": "string"}
	out, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "oneOf": variants, "additionalProperties": false})
	return string(out)
}
func surfaceSchemaWith(raw string, props map[string]any, required ...string) string {
	var s map[string]any
	if e := json.Unmarshal([]byte(raw), &s); e != nil {
		panic(e)
	}
	p := s["properties"].(map[string]any)
	for k, v := range props {
		p[k] = v
	}
	r, _ := s["required"].([]any)
	for _, k := range required {
		r = append(r, k)
	}
	s["required"] = r
	b, _ := json.Marshal(s)
	return string(b)
}

var SurfaceFeatureAreaLifecycleInputSchema = surfaceTaggedUnion("action", map[string]string{"ACTIVATE": FeatureAreaLifecycleInputSchema, "DECOMPOSE": FeatureAreaDecomposeInputSchema, "RETIRE": surfaceSchemaWith(FeatureAreaLifecycleInputSchema, map[string]any{"confirmed": map[string]any{"const": true}}, "confirmed")})
var SurfaceDecisionRespondInputSchema = surfaceTaggedUnion("kind", map[string]string{"REVIEW_APPROVAL": surfaceSchemaWith(reviewDecisionSchema, map[string]any{"reviewId": map[string]any{"type": "string", "minLength": 1}, "decisionId": map[string]any{"type": "string", "pattern": "^REVIEW_APPROVAL:.+"}}), "TASK_ACCEPTANCE": TaskAcceptanceInputJSONSchema})
var SurfaceRuntimeRequestInputSchema = surfaceTaggedUnion("kind", map[string]string{"CODEX_REQUEST": agentRequestInputSchema, "RUNTIME_APPROVAL": runtimeApprovalInputSchema()})
var SurfaceOperationCancelInputSchema = func() string {
	var a, b map[string]any
	_ = json.Unmarshal([]byte(OperationCancelInputSchema), &a)
	_ = json.Unmarshal([]byte(ExecutionCancelInputSchema), &b)
	p := map[string]any{}
	for _, s := range []map[string]any{a, b} {
		for k, v := range s["properties"].(map[string]any) {
			p[k] = v
		}
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "oneOf": []any{a, b}, "additionalProperties": false})
	return string(raw)
}()

var SurfaceRuntimeRequestOutputSchema = `{"type":"object","properties":{"kind":{"type":"string","enum":["CODEX_REQUEST","RUNTIME_APPROVAL"]},"guarantee":{"type":"string","enum":["EXACT_RUNTIME_REQUEST_STATE","AUTHORITATIVE_ENTITY_STATE"]},"runtime":` + agentRuntimeOutputSchema + `,"approval":` + approvalOutputSchema + `,"entityDecision":` + EntityDecisionOutputSchema + `},"required":["kind","guarantee"],"additionalProperties":false}`
