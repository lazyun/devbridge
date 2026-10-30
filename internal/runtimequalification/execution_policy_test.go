package runtimequalification

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

func TestNativeLaunchAuthorityCannotIgnoreDifferentAgentOrPolicy(t *testing.T) {
	for _, field := range []string{"agent", "policy"} {
		execution, authority, config := launchAuthorityFixture()
		if field == "agent" {
			execution.RuntimeConfig.AgentID = "pi"
		} else {
			execution.RuntimeConfig.ExecutionPolicy = "COOPERATIVE_TRUSTED_V1"
		}
		configs := &launchConfigurations{config: config}
		service := Service{Selections: &launchSelectionStore{authority: authority}, Configurations: configs}
		if _, err := service.VerifiedExecutionLaunch(context.Background(), execution); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) || configs.reads != 0 {
			t.Fatalf("%s policy mismatch reached launch config: %v reads=%d", field, err, configs.reads)
		}
	}
}
