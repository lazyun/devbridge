package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/observability"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxApprovalRequestBytes = approval.MaxPresentationPayloadBytes

type approvalRefInput struct {
	AISessionID string `json:"aiSessionId"`
	ApprovalID  string `json:"approvalId"`
}

type approvalListInput struct {
	AISessionID string `json:"aiSessionId"`
	RunID       string `json:"runId,omitempty"`
	PendingOnly bool   `json:"pendingOnly,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Offset      int    `json:"offset,omitempty"`
}

type approvalDecideInput struct {
	AISessionID string          `json:"aiSessionId"`
	ApprovalID  string          `json:"approvalId"`
	Decision    string          `json:"decision"`
	Permissions json.RawMessage `json:"permissions,omitempty"`
}

type ApprovalDecisionOutput struct {
	Type        string          `json:"type"`
	Permissions json.RawMessage `json:"permissions,omitempty"`
}

type ApprovalOutput struct {
	CancelEffect     string                  `json:"cancelEffect"`
	ID               string                  `json:"id"`
	RunID            string                  `json:"runId"`
	Kind             string                  `json:"kind"`
	Status           string                  `json:"status"`
	Request          json.RawMessage         `json:"request"`
	RequestTruncated bool                    `json:"requestTruncated"`
	AllowedDecisions []string                `json:"allowedDecisions"`
	Decision         *ApprovalDecisionOutput `json:"decision,omitempty"`
	CreatedAt        string                  `json:"createdAt"`
	DecidedAt        *string                 `json:"decidedAt,omitempty"`
	DeliveredAt      *string                 `json:"deliveredAt,omitempty"`
}

type ApprovalListOutput struct {
	Approvals []ApprovalOutput `json:"approvals"`
}

type ApprovalContinuationOutput struct {
	Approval ApprovalOutput `json:"approval"`
	Run      RunOutput      `json:"run"`
}

const (
	approvalRefInputSchema           = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"approvalId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","approvalId"],"additionalProperties":false}`
	approvalListInputSchema          = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","maxLength":128},"pendingOnly":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0,"maximum":1000000}},"required":["aiSessionId"],"additionalProperties":false}`
	approvalDecideInputSchema        = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"approvalId":{"type":"string","minLength":1,"maxLength":128},"decision":{"type":"string","enum":["APPROVE_ONCE","APPROVE_FOR_SESSION","DECLINE","CANCEL","GRANT_REQUESTED_PERMISSIONS"]},"permissions":{}},"required":["aiSessionId","approvalId","decision"],"additionalProperties":false}`
	approvalOutputSchema             = `{"type":"object","properties":{"cancelEffect":{"type":"string","enum":["DECLINE_ACTION","CANCEL_LEGACY_RUN"]},"id":{"type":"string"},"runId":{"type":"string"},"kind":{"type":"string"},"status":{"type":"string"},"request":{},"requestTruncated":{"type":"boolean"},"allowedDecisions":{"type":"array","maxItems":16,"items":{"type":"string"}},"decision":{"type":"object","properties":{"type":{"type":"string"},"permissions":{}},"required":["type"],"additionalProperties":false},"createdAt":{"type":"string"},"decidedAt":{"type":"string"},"deliveredAt":{"type":"string"}},"required":["id","runId","kind","status","request","requestTruncated","allowedDecisions","createdAt","cancelEffect"],"additionalProperties":false}`
	approvalListOutputSchema         = `{"type":"object","properties":{"approvals":{"type":"array","maxItems":100,"items":` + approvalOutputSchema + `}},"required":["approvals"],"additionalProperties":false}`
	approvalContinuationOutputSchema = `{"type":"object","properties":{"approval":` + approvalOutputSchema + `,"run":` + runOutputSchema + `},"required":["approval","run"],"additionalProperties":false}`
)

func registerApprovalTools(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "approval_list", Title: "List runtime approvals", Description: "List bounded redacted runtime approvals in the AI session's Project. Present each pending enumerable decision as an interactive card; do not ask the user to type allow or decline.",
		InputSchema: json.RawMessage(approvalListInputSchema), OutputSchema: json.RawMessage(approvalListOutputSchema), Annotations: toolAnnotations("List runtime approvals", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input approvalListInput) (*sdkmcp.CallToolResult, ApprovalListOutput, error) {
		values, err := service.ListApprovalsForSession(ctx, input.AISessionID, controlapi.ApprovalFilter{RunID: input.RunID, PendingOnly: input.PendingOnly, ListOptions: controlapi.ListOptions{Limit: input.Limit, Offset: input.Offset}})
		if err != nil {
			return nil, ApprovalListOutput{}, publicToolError(err)
		}
		for _, value := range values {
			logApprovalPresented(ctx, input.AISessionID, value)
		}
		return nil, ApprovalListOutput{Approvals: approvalOutputs(values)}, nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "approval_get", Title: "Get runtime approval", Description: "Get one bounded redacted runtime approval after validating AI session Project ownership. Refresh the same current Project Card Pending Decisions.",
		InputSchema: json.RawMessage(approvalRefInputSchema), OutputSchema: json.RawMessage(approvalOutputSchema), Annotations: toolAnnotations("Get runtime approval", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input approvalRefInput) (*sdkmcp.CallToolResult, ApprovalOutput, error) {
		value, err := service.GetApprovalForSession(ctx, input.AISessionID, input.ApprovalID)
		if err != nil {
			return nil, ApprovalOutput{}, publicToolError(err)
		}
		logApprovalPresented(ctx, input.AISessionID, value)
		return nil, approvalOutput(value), nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "approval_decide", Title: "Decide runtime approval", Description: "Handle one runtime-approval click in the same current Project Card by durably recording and delivering that exact legal human decision to the correct live Run session. A stale or repeated decision fails safely. The host write-permission popup is separate and is not proof of this domain decision.",
		InputSchema: json.RawMessage(approvalDecideInputSchema), OutputSchema: json.RawMessage(approvalContinuationOutputSchema), Annotations: stateChangingAnnotations("Decide runtime approval", false), Meta: decisionToolMeta(),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input approvalDecideInput) (*sdkmcp.CallToolResult, ApprovalContinuationOutput, error) {
		logCtx := observability.WithContext(ctx, observability.Correlation{AISessionID: input.AISessionID, ApprovalID: input.ApprovalID})
		observability.Info(logCtx, "continuation", "continuation.requested", "Approval continuation requested", map[string]any{"kind": "APPROVAL_DECISION", "payload": observability.Fingerprint(input.Decision)})
		value, err := service.DecideApprovalForSession(ctx, input.AISessionID, input.ApprovalID, approval.Decision{Type: approval.DecisionType(input.Decision), Permissions: input.Permissions})
		if err != nil {
			logContinuationDecisionFailure(logCtx, err)
			return nil, ApprovalContinuationOutput{}, publicToolError(err)
		}
		runValue, err := service.GetRunForSession(ctx, input.AISessionID, value.RunID)
		if err != nil {
			observability.Error(logCtx, "continuation", "continuation.preparation_failed", "Approval continuation response preparation failed", map[string]any{"kind": "APPROVAL_DECISION", "errorCode": "CONTINUATION_RUN_PROJECTION_FAILED", "errorSummary": err.Error()})
			return nil, ApprovalContinuationOutput{}, publicToolError(err)
		}
		logCtx = observability.WithContext(logCtx, observability.Correlation{RunID: value.RunID})
		observability.Info(logCtx, "continuation", "continuation.emitted", "Approval continuation result emitted", map[string]any{"kind": "APPROVAL_DECISION", "terminalState": runValue.Status, "payload": observability.Fingerprint(string(value.Status))})
		observability.Info(logCtx, "continuation", "continuation.delivery_accepted", "Continuation accepted for MCP response serialization", map[string]any{"kind": "APPROVAL_DECISION", "deliveryResult": "HANDLER_ACCEPTED_FOR_MCP_RESPONSE", "hostConsumptionProven": false})
		return nil, ApprovalContinuationOutput{Approval: approvalOutput(value), Run: runOutput(runValue, true)}, nil
	})
}

func logApprovalPresented(ctx context.Context, aiSessionID string, value approval.Approval) {
	if value.Status != approval.StatusPending {
		return
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{AISessionID: aiSessionID, RunID: value.RunID, ApprovalID: value.ID})
	observability.Info(logCtx, "approval", "approval.presented", "pending Approval projected for presentation", map[string]any{"kind": value.Kind, "action": observability.Fingerprint(string(value.RequestPayload))})
}

func logContinuationDecisionFailure(ctx context.Context, err error) {
	if errors.Is(err, approval.ErrAlreadyResolved) {
		observability.Info(ctx, "continuation", "continuation.skipped_duplicate", "duplicate Approval continuation skipped", map[string]any{"kind": "APPROVAL_DECISION", "reasonCode": "APPROVAL_ALREADY_RESOLVED"})
		return
	}
	var delivery *approval.DeliveryError
	if errors.As(err, &delivery) {
		observability.Error(ctx, "continuation", "continuation.delivery_failed", "Approval continuation runtime delivery failed", map[string]any{"kind": "APPROVAL_DECISION", "errorCode": "APPROVAL_CONTINUATION_DELIVERY_FAILED", "errorSummary": err.Error()})
		return
	}
	observability.Error(ctx, "continuation", "continuation.rejected", "Approval continuation request rejected", map[string]any{"kind": "APPROVAL_DECISION", "errorCode": "APPROVAL_CONTINUATION_REJECTED", "errorSummary": err.Error()})
}

func approvalOutputs(values []approval.Approval) []ApprovalOutput {
	result := make([]ApprovalOutput, 0, len(values))
	for _, value := range values {
		result = append(result, approvalOutput(value))
	}
	return result
}

func approvalOutput(value approval.Approval) ApprovalOutput {
	request, truncated := safeApprovalPayload(value.RequestPayload)
	allowed := make([]string, 0, len(value.AllowedDecisions))
	for _, decision := range value.AllowedDecisions {
		allowed = append(allowed, string(decision))
	}
	result := ApprovalOutput{CancelEffect: approvalCancelEffect(value), ID: value.ID, RunID: value.RunID, Kind: string(value.Kind), Status: string(value.Status), Request: request, RequestTruncated: truncated, AllowedDecisions: allowed, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), DecidedAt: formattedTime(value.DecidedAt), DeliveredAt: formattedTime(value.DeliveredAt)}
	if value.Decision != nil {
		var permissions json.RawMessage
		if len(value.Decision.Permissions) > 0 {
			permissions, _ = safeApprovalPayload(value.Decision.Permissions)
		}
		result.Decision = &ApprovalDecisionOutput{Type: string(value.Decision.Type), Permissions: permissions}
	}
	return result
}

func safeApprovalPayload(raw json.RawMessage) (json.RawMessage, bool) {
	return approval.SafePresentationPayload(raw)
}

func secretApprovalKey(value string) bool {
	return approval.SecretPayloadKey(value)
}

func approvalCancelEffect(value approval.Approval) string {
	if value.WorkflowOwnedRun {
		return "DECLINE_ACTION"
	}
	return "CANCEL_LEGACY_RUN"
}
