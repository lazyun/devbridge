package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/observability"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tools are deliberately app-only. They consolidate a user card click
// into the durable internal transitions it authorizes; they are not choices
// the model should orchestrate.
type uiApprovalDecisionInput struct {
	AISessionID string          `json:"aiSessionId"`
	ApprovalID  string          `json:"approvalId"`
	Decision    string          `json:"decision"`
	Permissions json.RawMessage `json:"permissions,omitempty"`
}

type UIApprovalDecisionOutput struct {
	Guarantee               string               `json:"guarantee"`
	EntityDecision          EntityDecisionOutput `json:"entityDecision"`
	Approval                ApprovalOutput       `json:"approval"`
	Run                     *RunOutput           `json:"run,omitempty"`
	WorkflowRefreshRequired bool                 `json:"workflowRefreshRequired"`
}

const (
	uiApprovalDecisionOutputSchema = `{"type":"object","properties":{"guarantee":{"const":"AUTHORITATIVE_ENTITY_STATE"},"entityDecision":` + EntityDecisionOutputSchema + `,"workflowRefreshRequired":{"type":"boolean"},"approval":` + approvalOutputSchema + `,"run":` + runOutputSchema + `},"required":["guarantee","entityDecision","approval","workflowRefreshRequired"],"additionalProperties":false}`
	uiApprovalDecisionInputSchema  = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"approvalId":{"type":"string","minLength":1,"maxLength":128},"decision":{"type":"string","enum":["APPROVE_ONCE","APPROVE_FOR_SESSION","DECLINE","CANCEL","GRANT_REQUESTED_PERMISSIONS"]},"permissions":{}},"required":["aiSessionId","approvalId","decision"],"additionalProperties":false}`
)

func uiDecisionToolMeta() sdkmcp.Meta {
	return sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}
}

func registerUIDecisionTools(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "ui_approval_get", Title: "Read exact Approval", Description: "App-only exact Approval entity readback. Never decides or redelivers an Approval.", InputSchema: json.RawMessage(approvalRefInputSchema), OutputSchema: json.RawMessage(approvalOutputSchema), Annotations: toolAnnotations("Read exact Approval", true, true), Meta: uiDecisionToolMeta()}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input approvalRefInput) (*sdkmcp.CallToolResult, ApprovalOutput, error) {
		value, err := service.GetApprovalForSession(ctx, input.AISessionID, input.ApprovalID)
		if err != nil {
			return nil, ApprovalOutput{}, publicToolError(err)
		}
		return nil, approvalOutput(value), nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "ui_approval_decide", Title: "Decide runtime approval", Description: "App-only runtime approval decision for the current Execution. CANCEL rejects the current action; it does not safely cancel the Execution. Returns authoritative entity state without an exact request receipt. Retry reads an already-decided Approval without redelivery.", InputSchema: json.RawMessage(uiApprovalDecisionInputSchema), OutputSchema: json.RawMessage(uiApprovalDecisionOutputSchema), Annotations: stateChangingAnnotations("Decide runtime approval", false), Meta: uiDecisionToolMeta()}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input uiApprovalDecisionInput) (*sdkmcp.CallToolResult, UIApprovalDecisionOutput, error) {
		logCtx := observability.WithContext(ctx, observability.Correlation{AISessionID: input.AISessionID, ApprovalID: input.ApprovalID})
		observability.Info(logCtx, "continuation", "continuation.requested", "Approval continuation requested", map[string]any{"kind": "APPROVAL_DECISION", "payload": observability.Fingerprint(input.Decision)})
		value, entity, err := ResolveApprovalDecisionCore(ctx, service, input.AISessionID, input.ApprovalID, approval.Decision{Type: approval.DecisionType(input.Decision), Permissions: input.Permissions})
		if err != nil {
			logContinuationDecisionFailure(logCtx, err)
			return nil, UIApprovalDecisionOutput{}, publicToolError(err)
		}
		out := UIApprovalDecisionOutput{Guarantee: "AUTHORITATIVE_ENTITY_STATE", EntityDecision: entity, Approval: approvalOutput(value)}
		if runValue, readErr := service.GetRunForSession(ctx, input.AISessionID, value.RunID); readErr == nil {
			projected := runOutput(runValue, true)
			out.Run = &projected
		} else {
			out.WorkflowRefreshRequired = true
		}
		if entity.AlreadyResolved {
			observability.Info(logCtx, "continuation", "continuation.skipped_duplicate", "Existing Approval state returned without redelivery", map[string]any{"kind": "APPROVAL_DECISION"})
		}
		observability.Info(logCtx, "continuation", "continuation.emitted", "Approval entity state emitted", map[string]any{"kind": "APPROVAL_DECISION", "approvalStatus": value.Status})
		observability.Info(logCtx, "continuation", "continuation.delivery_accepted", "Continuation accepted for app-tool response serialization", map[string]any{"kind": "APPROVAL_DECISION", "deliveryResult": "HANDLER_ACCEPTED_FOR_APP_RESPONSE", "hostConsumptionProven": false})
		return nil, out, nil
	})
}
