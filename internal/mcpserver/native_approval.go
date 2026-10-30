package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
)

// Native App Server approvals are RuntimeRequests, not legacy Approval rows or
// semantic UserDecisions. Validate their complete frozen identity before using
// the existing Core resolver; the runtime still validates the actual response.
func resolveNativeApproval(ctx context.Context, service BridgeService, session string, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	p, ok := service.(agentRuntimeService)
	if !ok {
		return agentruntime.Snapshot{}, errors.New("agent runtime unavailable")
	}
	snapshot, err := p.AgentRuntimeStateForSession(ctx, session, in.RunID)
	if err != nil {
		return snapshot, err
	}
	if in.Identity != snapshot.Identity || in.RuntimeRequestID == "" || in.ExpectedState != "PENDING" || in.UserDecisionID != "" || len(in.Response) == 0 || in.PayloadSHA256 == "" {
		return snapshot, agentruntime.ErrStale
	}
	for _, q := range snapshot.Requests {
		if q.ID != in.RuntimeRequestID {
			continue
		}
		if q.Kind != "RUNTIME_APPROVAL" || q.State != "PENDING" || q.Identity != in.Identity || q.ItemID != in.ItemID || !bytes.Equal(bytes.TrimSpace(q.RequestID), bytes.TrimSpace(in.RequestID)) || q.PayloadSHA256 != in.PayloadSHA256 {
			return snapshot, agentruntime.ErrStale
		}
		if q.Method != "item/commandExecution/requestApproval" && q.Method != "item/fileChange/requestApproval" && q.Method != "item/permissions/requestApproval" {
			return snapshot, agentruntime.ErrUnsupported
		}
		return p.ResolveAgentRequestForSession(ctx, session, in)
	}
	return snapshot, agentruntime.ErrStale
}

func runtimeApprovalInputSchema() string {
	// Legacy entity and native request forms are disjoint. An approvalId can
	// never be inferred from a runtimeRequestId or itemId.
	var root map[string]any
	_ = json.Unmarshal([]byte(runtimeDTOSchema[struct {
		AISessionID string                  `json:"aiSessionId"`
		Resolution  agentruntime.Resolution `json:"resolution"`
	}]()), &root)
	props := root["properties"].(map[string]any)
	r := props["resolution"].(map[string]any)
	rp := r["properties"].(map[string]any)
	delete(rp, "userDecisionId")
	rp["requestId"] = map[string]any{"type": []string{"string", "integer"}}
	rp["response"] = map[string]any{"type": "object", "description": "Exact user-selected native availableResponses option; validated by the original runtime"}
	rp["expectedState"] = map[string]any{"const": "PENDING"}
	r["required"] = []string{"projectId", "featureAreaId", "taskId", "runId", "threadId", "turnId", "connectionId", "runtimeEpoch", "operationKind", "operationId", "taskDefinitionRevisionId", "runtimeRequestId", "requestId", "itemId", "expectedState", "payloadSHA256", "response"}
	props["approvalId"] = map[string]any{"type": "string", "minLength": 1}
	props["decision"] = map[string]any{"enum": []string{"APPROVE_ONCE", "APPROVE_FOR_SESSION", "DECLINE", "CANCEL", "GRANT_REQUESTED_PERMISSIONS"}}
	props["permissions"] = map[string]any{"type": "object"}
	root["required"] = []string{"aiSessionId"}
	root["oneOf"] = []any{
		map[string]any{"required": []string{"resolution"}, "not": map[string]any{"anyOf": []any{map[string]any{"required": []string{"approvalId"}}, map[string]any{"required": []string{"decision"}}, map[string]any{"required": []string{"permissions"}}}}},
		map[string]any{"required": []string{"approvalId", "decision"}, "not": map[string]any{"required": []string{"resolution"}}, "oneOf": []any{map[string]any{"properties": map[string]any{"decision": map[string]any{"const": "GRANT_REQUESTED_PERMISSIONS"}}, "required": []string{"permissions"}}, map[string]any{"properties": map[string]any{"decision": map[string]any{"enum": []string{"APPROVE_ONCE", "APPROVE_FOR_SESSION", "DECLINE", "CANCEL"}}}, "not": map[string]any{"required": []string{"permissions"}}}}},
	}
	b, _ := json.Marshal(root)
	return string(b)
}
