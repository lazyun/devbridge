package review

import (
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
)

func TestReviewPolicyFreezesExplicitAgentWithoutReinterpretingLegacy(t *testing.T) {
	store := &serviceStore{}
	service, err := NewService(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := Review{ReviewDepth: "DEEP", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", CreatedByAISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: FocusRepository}, EngineeringGoal: "goal", Instruction: "exact instruction", Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	legacy, err := service.Prepare(base)
	if err != nil || legacy.RuntimeConfig.AgentID != "" || legacy.RuntimeConfig.ExecutionPolicy != "" {
		t.Fatalf("legacy policy inferred: %+v %v", legacy, err)
	}
	for _, agentID := range []string{"codex", "pi"} {
		value := base
		value.RuntimeConfig = codexruntime.NextRunRuntimeConfig{Model: base.Model, ReasoningEffort: base.ReasoningEffort, AgentID: agentID, AgentConfigFingerprint: strings.Repeat("a", 64), ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1}
		prepared, err := service.Prepare(value)
		if err != nil || prepared.RuntimeConfig != value.RuntimeConfig || prepared.Instruction != value.Instruction {
			t.Fatalf("explicit review policy lost: %+v %v", prepared, err)
		}
	}
}

func TestReviewPolicyRejectsUnknownOrContradictoryExecutor(t *testing.T) {
	service, err := NewService(&serviceStore{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		executor      ExecutorKind
		agent, policy string
	}{
		{ExecutorCodingAgent, "pi", ""}, {ExecutorCodingAgent, "", "COOPERATIVE_TRUSTED_V1"},
		{ExecutorCodingAgent, "codex", "UNKNOWN"}, {ExecutorPiAgent, "codex", "COOPERATIVE_TRUSTED_V1"},
	} {
		value := Review{ReviewDepth: "DEEP", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", CreatedByAISessionID: "session", ExecutorKind: tc.executor, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", AgentID: tc.agent, ExecutionPolicy: tc.policy}}
		if _, err := service.Prepare(value); err == nil {
			t.Fatalf("invalid selection accepted: %+v", tc)
		}
	}
}
