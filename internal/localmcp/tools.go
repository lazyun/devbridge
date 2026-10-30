package localmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/review"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"strings"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpserver "github.com/lazyun/devbridge/internal/mcpserver"
)

// The Local MCP adapter's own tool presentation helpers.
func readOnlyAnnotations(title string, readOnly, idempotent bool) *sdkmcp.ToolAnnotations {
	nonDestructive, closedWorld := false, false
	return &sdkmcp.ToolAnnotations{Title: title, ReadOnlyHint: readOnly, DestructiveHint: &nonDestructive, IdempotentHint: idempotent, OpenWorldHint: &closedWorld}
}

func stateChangingAnnotations(title string, destructive bool) *sdkmcp.ToolAnnotations {
	closedWorld := false
	return &sdkmcp.ToolAnnotations{Title: title, ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false, OpenWorldHint: &closedWorld}
}

// publicToolError converts adapter-internal failures into bounded, stable
// tool errors. Control-plane errors keep their durable codes so an LLM can
// distinguish "already resolved" from transient failures.
func publicToolError(err error) error {
	var depthErr *review.DepthTooShallowError
	if errors.As(err, &depthErr) {
		raw, _ := json.Marshal(struct {
			Code string `json:"code"`
			*review.DepthTooShallowError
		}{"REVIEW_DEPTH_TOO_SHALLOW", depthErr})
		return errors.New(string(raw))
	}
	var progressionErr *wp.Error
	if errors.As(err, &progressionErr) {
		raw, _ := json.Marshal(progressionErr)
		return errors.New(string(raw))
	}
	if err == nil {
		return nil
	}
	var apiErr *controlapi.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%s: %s", apiErr.Code, apiErr.Message)
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 512 {
		message = message[:512]
	}
	return fmt.Errorf("Bridge request failed: %s", message)
}

// registerLocalMCPTools installs the Local MCP adapter's own registration:
// the twelve product capabilities over the shared cores plus the single
// generic structured decision capability. The ChatGPT app-private ui_* tools,
// the decision card resource, and ChatGPT continuation do not exist here.
// registerLocalMCPTools delegates the complete business schema to the shared adapter.
func registerLocalMCPTools(server *sdkmcp.Server, service mcpserver.BridgeService) {
	mcpserver.RegisterSurfaceBusinessTools(server, service)
}

// WorkflowOutput is the local MCP workspace_get projection: the canonical
// closed product state with the durable decisionId added to the projected
// pending decision. Internal Run/Thread/Turn identifiers stay hidden.
type WorkflowOutput struct {
	mcpserver.ExternalWorkflowStateOutput
}

func buildWorkflowOutput(ctx context.Context, service mcpserver.BridgeService, aiSessionID string, featureAreaIDs ...string) (WorkflowOutput, error) {
	featureAreaID := ""
	if len(featureAreaIDs) != 0 {
		featureAreaID = featureAreaIDs[0]
	}
	state, err := mcpserver.BuildWorkflowState(ctx, service, aiSessionID, featureAreaID)
	if err != nil {
		return WorkflowOutput{}, publicToolError(err)
	}
	out := WorkflowOutput{ExternalWorkflowStateOutput: state.Output}
	mcpserver.SetDecisionInteractionAvailability(&out.ExternalWorkflowStateOutput, true)
	logCtx := observability.WithContext(ctx, observability.Correlation{AISessionID: aiSessionID})
	observability.Info(logCtx, "localmcp", "localmcp.workflow.read", "Local MCP workflow snapshot read", map[string]any{"state": out.State, "pendingDecision": out.PendingDecision != nil})
	return out, nil
}

// DecisionRespondInput is the generic structured decision contract.
type DecisionRespondInput struct {
	ExecutionID              string                 `json:"executionId,omitempty"`
	TaskDefinitionRevisionID string                 `json:"taskDefinitionRevisionId,omitempty"`
	FeatureAreaID            string                 `json:"featureAreaId,omitempty"`
	TaskID                   string                 `json:"taskId,omitempty"`
	AISessionID              string                 `json:"aiSessionId"`
	DecisionID               string                 `json:"decisionId"`
	Option                   string                 `json:"option"`
	RequestID                string                 `json:"requestId,omitempty"`
	NextWorkSpec             *task.WorkSpec         `json:"nextWorkSpec,omitempty"`
	NextVerificationSpec     *task.VerificationSpec `json:"nextVerificationSpec,omitempty"`
}

// DecisionRespondOutput reports the resolution plus the fresh canonical
// workflow state summary so the conversation can continue from durable state.
// The full projection stays available through workspace_get.
type DecisionRespondOutput struct {
	ReviewApproval *wp.Resolution `json:"reviewApproval,omitempty"`
	*mcpserver.DecisionResolveOutput
	Guarantee      string                          `json:"guarantee"`
	EntityDecision *mcpserver.EntityDecisionOutput `json:"entityDecision,omitempty"`
	Kind           string                          `json:"kind"`
	Option         string                          `json:"option"`
}

type decisionHandle struct {
	kind        string
	subjectID   string
	expansionID string
	approvalID  string
}

func parseDecisionID(raw string) (decisionHandle, error) {
	kind, subject, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || strings.TrimSpace(subject) == "" {
		return decisionHandle{}, errors.New("decisionId must be <KIND>:<subject> as projected by workspace_get")
	}
	switch kind {
	case "TASK_ACCEPTANCE":
		return decisionHandle{kind: kind, subjectID: subject}, nil
	case "SCOPE_EXPANSION":
		return decisionHandle{kind: kind, expansionID: subject}, nil
	case "RUNTIME_APPROVAL":
		return decisionHandle{kind: kind, approvalID: subject}, nil
	default:
		return decisionHandle{}, fmt.Errorf("unknown decision kind %q", kind)
	}
}

// validOptions lists the options each durable decision kind accepts. They are
// exactly the options the workflow projection advertises.
func (h decisionHandle) validOptions() []string {
	switch h.kind {
	case "TASK_ACCEPTANCE":
		return []string{"ACCEPT", "REJECT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION"}
	case "SCOPE_EXPANSION":
		return []string{"ALLOW", "KEEP_CURRENT"}
	case "RUNTIME_APPROVAL":
		return []string{"APPROVE_ONCE", "APPROVE_FOR_SESSION", "DECLINE", "CANCEL", "GRANT_REQUESTED_PERMISSIONS"}
	default:
		return nil
	}
}

func containsOption(options []string, option string) bool {
	for _, candidate := range options {
		if candidate == option {
			return true
		}
	}
	return false
}

// DecisionRespondCore resolves one durable pending decision. Bridge
// structurally validates that the decision exists, is still pending, that the
// supplied session is authorized for the same Project, and that the option is
// valid. Task Decisions return exact durable request receipts; other active
// Decisions return subject state and resolve through their existing CAS cores. Cross-adapter resolution is intentional:
// any authorized session of the same Project may resolve a durable decision.
func DecisionRespondCore(ctx context.Context, service mcpserver.BridgeService, in DecisionRespondInput) (DecisionRespondOutput, error) {
	if strings.HasPrefix(in.DecisionID, wp.KindReviewApproval+":") {
		out, err := mcpserver.ReviewApprovalCore(ctx, service, wp.ResolveInput{AISessionID: in.AISessionID, TaskID: in.TaskID, DecisionID: in.DecisionID, Option: in.Option, RequestID: in.RequestID})
		return DecisionRespondOutput{ReviewApproval: &out, Kind: wp.KindReviewApproval, Option: in.Option, Guarantee: "DURABLE_REQUEST_RECEIPT"}, publicToolError(err)
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{AISessionID: in.AISessionID})
	observability.Info(logCtx, "localmcp", "localmcp.decision.requested", "Local MCP structured decision requested", map[string]any{"decisionId": in.DecisionID, "option": in.Option, "requestId": in.RequestID})
	handle, parseErr := parseDecisionID(in.DecisionID)
	if parseErr != nil {
		return DecisionRespondOutput{}, publicToolError(parseErr)
	}
	option := strings.TrimSpace(in.Option)
	if !containsOption(handle.validOptions(), option) {
		return DecisionRespondOutput{}, publicToolError(fmt.Errorf("option %q is not valid for %s; use one of the options projected by workspace_get", option, handle.kind))
	}
	if handle.kind == "TASK_ACCEPTANCE" {
		out, err := mcpserver.ResolveDecisionCore(ctx, service, mcpserver.DecisionResolveInput{TaskID: in.TaskID, ExecutionID: in.ExecutionID, FeatureAreaID: in.FeatureAreaID, TaskDefinitionRevisionID: in.TaskDefinitionRevisionID, AISessionID: in.AISessionID, DecisionID: in.DecisionID, Option: in.Option, RequestID: in.RequestID, NextWorkSpec: in.NextWorkSpec, NextVerificationSpec: in.NextVerificationSpec})
		if err != nil {
			return DecisionRespondOutput{}, publicToolError(err)
		}
		return DecisionRespondOutput{DecisionResolveOutput: &out, Guarantee: "DURABLE_REQUEST_RECEIPT", Kind: handle.kind, Option: option}, nil
	}
	if in.RequestID != "" {
		return DecisionRespondOutput{}, publicToolError(fmt.Errorf("requestId is supported only for Review Approval and Task Acceptance; this Decision has authoritative entity state but no durable request receipt"))
	}
	var entity mcpserver.EntityDecisionOutput
	var err error
	switch handle.kind {
	case "RUNTIME_APPROVAL":
		_, entity, err = mcpserver.ResolveApprovalDecisionCore(ctx, service, in.AISessionID, handle.approvalID, approval.Decision{Type: approval.DecisionType(option)})
	case "SCOPE_EXPANSION":
		err = &controlapi.Error{Code: controlapi.CodeConflict, Message: "SCOPE_EXPANSION_LEGACY_ONLY: engineering scope decisions belong to the Upper AI"}
	}
	if err != nil {
		return DecisionRespondOutput{}, publicToolError(err)
	}
	return DecisionRespondOutput{Guarantee: "AUTHORITATIVE_ENTITY_STATE", EntityDecision: &entity, Kind: handle.kind, Option: option}, nil
}
