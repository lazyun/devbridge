package mcpserver

import (
	"context"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentconversation"
)

type AgentOperationRecoverInput struct {
	ExpectedUpperVersion *agentconversation.UpperVersion `json:"expectedUpperVersion,omitempty"`
	AISessionID          string                          `json:"aiSessionId"`
	FeatureAreaID        string                          `json:"featureAreaId"`
	OperationKind        agentconversation.OperationKind `json:"operationKind"`
	OperationID          string                          `json:"operationId"`
	RunID                string                          `json:"runId"`
	AgentConversationID  string                          `json:"agentConversationId"`
	RequestID            string                          `json:"requestId"`
}
type AgentOperationRecoveryOutput struct {
	ExpectedUpperVersion   *agentconversation.UpperVersion `json:"expectedUpperVersion,omitempty"`
	AuthorizedUpperVersion *agentconversation.UpperVersion `json:"authorizedUpperVersion,omitempty"`
	GrantState             string                          `json:"grantState,omitempty"`
	BackendDisposition     string                          `json:"backendDisposition,omitempty"`
	State                  string                          `json:"state"`
	FeatureAreaID          string                          `json:"featureAreaId"`
	OperationKind          agentconversation.OperationKind `json:"operationKind"`
	OperationID            string                          `json:"operationId"`
	RunID                  string                          `json:"runId"`
	OldAgentConversationID string                          `json:"oldAgentConversationId"`
	NewAgentConversationID string                          `json:"newAgentConversationId"`
	Boundary               agentconversation.Advance       `json:"boundary"`
	ContextAbandoned       bool                            `json:"contextAbandoned"`
	FreshWorkAllowed       bool                            `json:"freshWorkAllowed"`
}
type agentOperationRecoveryService interface {
	RecoverFeatureAreaAgentOperation(context.Context, string, agentconversation.RecoverInput) (agentconversation.RecoveryResult, error)
	FeatureAreaAgentOperationState(context.Context, string, string) (agentconversation.State, error)
}

func AgentOperationRecoverCore(ctx context.Context, service BridgeService, in AgentOperationRecoverInput) (AgentOperationRecoveryOutput, error) {
	recovery, ok := service.(agentOperationRecoveryService)
	if !ok {
		return AgentOperationRecoveryOutput{}, publicToolError(fmt.Errorf("agent operation recovery is unavailable"))
	}
	result, err := recovery.RecoverFeatureAreaAgentOperation(ctx, in.AISessionID, agentconversation.RecoverInput{ExpectedUpperVersion: in.ExpectedUpperVersion, FeatureAreaID: in.FeatureAreaID, OperationKind: in.OperationKind, OperationID: in.OperationID, RunID: in.RunID, AgentConversationID: in.AgentConversationID, RequestID: in.RequestID})
	if err != nil {
		return AgentOperationRecoveryOutput{}, publicToolError(err)
	}
	state := "UNAVAILABLE"
	if result.BackendDisposition == "UPPER_REAUTHORIZED" {
		state = "REAUTHORIZED"
	}
	if result.FreshWorkAllowed {
		state = "READY"
	}
	return AgentOperationRecoveryOutput{ExpectedUpperVersion: result.ExpectedUpperVersion, AuthorizedUpperVersion: result.AuthorizedUpperVersion, GrantState: result.GrantState, BackendDisposition: result.BackendDisposition, State: state, FeatureAreaID: result.FeatureAreaID, OperationKind: result.OperationKind, OperationID: result.OperationID, RunID: result.RunID, OldAgentConversationID: result.OldConversationID, NewAgentConversationID: result.NewConversationID, Boundary: result.Boundary, ContextAbandoned: result.ContextAbandoned, FreshWorkAllowed: result.FreshWorkAllowed}, nil
}
