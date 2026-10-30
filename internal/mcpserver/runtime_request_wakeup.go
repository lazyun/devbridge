package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type runtimeRequestWakeupIdentity struct {
	AISessionID      string `json:"aiSessionId"`
	RunID            string `json:"runId"`
	RuntimeRequestID string `json:"runtimeRequestId"`
	RuntimeEpoch     string `json:"runtimeEpoch"`
}
type runtimeRequestWakeupResult struct {
	Status            string `json:"status"`
	RuntimeRequestID  string `json:"runtimeRequestId"`
	Prompt            string `json:"prompt,omitempty"`
	DeliveryGuarantee string `json:"deliveryGuarantee"`
}

func registerRuntimeRequestWakeupClaim(server *sdkmcp.Server, service BridgeService) {
	var mu sync.Mutex
	attempted := map[runtimeRequestWakeupIdentity]bool{}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "dev_bridge_runtime_request_wakeup_claim", Description: "App-only notification preflight. Read the exact durable CODEX_REQUEST before reserving one process-local follow-up attempt. CLAIMED and ALREADY_ATTEMPTED never prove delivery or user authorization. Notification failure leaves the request waiting for the user. Never submits an answer.", InputSchema: json.RawMessage(runtimeDTOSchema[runtimeRequestWakeupIdentity]()), Annotations: toolAnnotations("Reserve exact runtime question notification", true, true), Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runtimeRequestWakeupIdentity) (*sdkmcp.CallToolResult, runtimeRequestWakeupResult, error) {
		out := runtimeRequestWakeupResult{RuntimeRequestID: in.RuntimeRequestID, DeliveryGuarantee: "UNCONFIRMED"}
		reader, ok := service.(agentRuntimeService)
		if !ok {
			return nil, out, errors.New("agent runtime unavailable")
		}
		snapshot, err := reader.AgentRuntimeStateForSession(ctx, in.AISessionID, in.RunID)
		if err != nil {
			return nil, out, publicToolError(err)
		}
		eligible := false
		for _, r := range snapshot.Requests {
			if r.ID == in.RuntimeRequestID && r.Kind == "CODEX_REQUEST" && r.RuntimeEpoch == in.RuntimeEpoch && r.State == "WAITING_FOR_USER" && r.UserDecision == nil {
				eligible = true
			}
		}
		if !eligible {
			return nil, out, errors.New("exact persisted request is not waiting for user")
		}
		mu.Lock()
		defer mu.Unlock()
		if attempted[in] {
			out.Status = "ALREADY_ATTEMPTED"
			return nil, out, nil
		}
		if len(attempted) >= 10000 {
			return nil, out, errors.New("notification attempt capacity reached")
		}
		attempted[in] = true
		out.Status = "CLAIMED"
		out.Prompt = "Read operation_get(include=RUNTIME_REQUESTS) for exact runtimeRequestId=" + in.RuntimeRequestID + ", runId=" + in.RunID + " before presenting the original question and options. " + ExplicitUserDecisionDescription + " This notification is not a user answer."
		return nil, out, nil
	})
}
