package mcpserver

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"testing"
)

type recoveryProjectionService struct {
	BridgeService
	fresh bool
}

func (s recoveryProjectionService) RecoverFeatureAreaAgentOperation(context.Context, string, agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	return agentconversation.RecoveryResult{FeatureAreaID: "featureArea-a", FreshWorkAllowed: s.fresh, ContextAbandoned: true}, nil
}
func (s recoveryProjectionService) FeatureAreaAgentOperationState(context.Context, string, string) (agentconversation.State, error) {
	return agentconversation.State{}, nil
}
func TestRecoveryProjectionDoesNotOfferFreshWorkForLegacyOwner(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		got, err := AgentOperationRecoverCore(context.Background(), recoveryProjectionService{fresh: fresh}, AgentOperationRecoverInput{})
		if err != nil {
			t.Fatal(err)
		}
		want := "UNAVAILABLE"
		if fresh {
			want = "READY"
		}
		if got.State != want || got.FreshWorkAllowed != fresh {
			t.Fatalf("fresh=%v output=%+v", fresh, got)
		}
	}
}
