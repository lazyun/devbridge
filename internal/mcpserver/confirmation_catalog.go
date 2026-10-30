package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ChatGPT has purpose-specific preparation and component confirmation. Local and
// trusted Desktop retain their canonical command catalog. Removing the handlers,
// rather than filtering tools/list, also closes calls using remembered old names.
var chatGPTConfirmationRetiredTools = []string{
	"project_open", "development_branch_select", "feature_area_create", "task_create",
	"task_review_request", "task_review_start", "execution_start", "direct_file_enable",
	"project_selection_show", "decision_respond",
}

func confirmationTool(name string) bool {
	switch name {
	case "project_setup_prepare", "feature_area_prepare", "task_prepare", "review_prepare", "execution_prepare",
		"confirmation_show", "confirmation_get", "confirmation_options_get", "confirmation_commit", "confirmation_reprepare", "confirmation_discard", "confirmation_submission_withdraw", "task_action_conflict_resolve":
		return true
	}
	return false
}
func confirmationComponentTool(name string) bool {
	switch name {
	case "confirmation_get", "confirmation_options_get", "confirmation_commit", "confirmation_discard", "confirmation_submission_withdraw", "task_action_conflict_resolve":
		return true
	}
	return false
}

func registerChatGPTDecisionContinuation(server *sdkmcp.Server, service BridgeService) {
	server.RemoveTools(chatGPTConfirmationRetiredTools...)
	// This endpoint reports the ChatGPT business contract, not the retained
	// Local canonical catalog or app-only presentation readers.
	addExternalTool(server, "bridge_diagnostics", "Bridge diagnostics", externalBridgeDiagnosticsDescription+" Compare the DevBridge 工具版本 ID label in the plugin's saved tool description with this read's serverVersion. Different values mean the imported definition and running service are different releases; a matching release label alone does not prove every schema is current. ChatGPT's asdk_app_v registration ID is a separate host-generated identity, not this service version. businessCatalogSHA256 identifies the ChatGPT business tool-name set after purpose-confirmation migration; it does not fingerprint schemas or descriptions. Presentation-only tools are excluded. Local reports its retained canonical catalog separately.", emptyInputSchema, externalBridgeInfoOutputSchema, toolAnnotations("Bridge diagnostics", true, true), func(ctx context.Context, request *sdkmcp.CallToolRequest, _ bridgeInfoInput) (*sdkmcp.CallToolResult, BridgeInfoOutput, error) {
		out, err := BridgeDiagnosticsCore(ctx, service, request.ProtocolVersion())
		out.BusinessCatalogSHA256 = chatGPTBusinessCatalogSHA256()
		return nil, out, err
	})
	// Review REVISE/REJECT and Task ACCEPT/REJECT/REQUEST_REVERIFICATION are
	// prepared as closed Review/Execution purpose variants. The removed
	// decision_respond name cannot bypass immutable intent, component click,
	// stable subrequest, unique consumption, or receipt readback.
	type verificationInput struct {
		AISessionID    string                      `json:"aiSessionId"`
		TaskID         string                      `json:"taskId"`
		ExpectedTarget wp.TaskExecutionExpectation `json:"expectedTarget"`
	}
	const verificationSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"expectedTarget":{"type":"object","properties":{"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"taskDefinitionRevisionId":{"type":"string","minLength":1,"maxLength":128},"executionId":{"type":"string","minLength":1,"maxLength":128},"phase":{"const":"VERIFICATION_READY"},"primaryMemberId":{"type":"string","minLength":1},"approvedWorksetHash":{"type":"string","pattern":"^[a-f0-9]{64}$"}},"required":["featureAreaId","taskDefinitionRevisionId","executionId","phase"],"additionalProperties":false}},"required":["aiSessionId","taskId","expectedTarget"],"additionalProperties":false}`
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "verification_continue", Title: "Continue frozen Execution verification", Description: "Continue only the exact existing VERIFICATION_READY Execution under its approved verificationSpec, frozen backend binding and runtime configuration. No new configuration, implementation, arbitrary instructions or confirmation is created. Core rechecks lifecycle, reservation, backend fingerprint, authority and current model availability. Unknown historical configuration is blocked, never defaulted.", InputSchema: json.RawMessage(verificationSchema), OutputSchema: json.RawMessage(runtimeDTOSchema[SurfaceExecutionStartOutput]()), Annotations: stateChangingAnnotations("Continue existing verification", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in verificationInput) (*sdkmcp.CallToolResult, SurfaceExecutionStartOutput, error) {
		if in.ExpectedTarget.Phase != "VERIFICATION_READY" || in.ExpectedTarget.ExecutionID == "" || in.ExpectedTarget.FeatureAreaID == "" || in.ExpectedTarget.TaskDefinitionRevisionID == "" {
			return nil, SurfaceExecutionStartOutput{}, errors.New("exact existing verification target required")
		}
		started, err := SurfaceExecutionStartCore(ctx, service, SurfaceExecutionStartInput{AISessionID: in.AISessionID, TaskID: in.TaskID, ExpectedTarget: &in.ExpectedTarget})
		out := SurfaceExecutionStartOutput{StartResult: started, ReservationCommitted: started.Proposal.ID != "" || started.Run.ID != "" || started.Receipt != nil}
		if err != nil && out.ReservationCommitted {
			out.DispatchError = publicToolError(err).Error()
			out.RefreshRequired = true
			return &sdkmcp.CallToolResult{IsError: true}, out, nil
		}
		return nil, out, publicToolError(err)
	})
}

func chatGPTBusinessCatalogSHA256() string {
	retired := make(map[string]bool, len(chatGPTConfirmationRetiredTools))
	for _, name := range chatGPTConfirmationRetiredTools {
		retired[name] = true
	}
	names := make([]string, 0, len(SurfaceBusinessToolNames)+10)
	for _, name := range SurfaceBusinessToolNames {
		if !retired[name] {
			names = append(names, name)
		}
	}
	names = append(names, "verification_continue", "project_setup_prepare", "feature_area_prepare", "task_prepare", "review_prepare", "execution_prepare", "confirmation_get", "confirmation_options_get", "confirmation_commit", "confirmation_reprepare")
	sort.Strings(names)
	raw, _ := json.Marshal(names)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
