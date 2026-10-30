package mcpserver

import (
	"strings"
	"testing"
)

func TestConfirmationRuntimePolicySchemaIsExplicitAndClosed(t *testing.T) {
	schema := resolveSchema(t, confirmationRuntimeSchema())
	base := func() map[string]any {
		return map[string]any{"model": "gpt-5.6-luna", "reasoningEffort": "medium", "codexFastMode": false}
	}
	for _, tc := range []struct {
		agent, policy string
		valid         bool
	}{
		{"", "", true}, {"codex", "", true}, {"", "TASK_SANDBOX_V1", true},
		{"codex", "COOPERATIVE_TRUSTED_V1", true}, {"pi", "COOPERATIVE_TRUSTED_V1", true},
		{"", "COOPERATIVE_TRUSTED_V1", false}, {"pi", "", false}, {"pi", "TASK_SANDBOX_V1", false},
		{"unknown", "COOPERATIVE_TRUSTED_V1", false}, {"codex", "trusted", false},
	} {
		value := base()
		if tc.agent != "" {
			value["agentId"] = tc.agent
		}
		if tc.policy != "" {
			value["executionPolicy"] = tc.policy
		}
		if tc.policy == "COOPERATIVE_TRUSTED_V1" {
			value["agentConfigFingerprint"] = strings.Repeat("a", 64)
		}
		if err := schema.Validate(value); (err == nil) != tc.valid {
			t.Fatalf("%#v: %v", value, err)
		}
	}
	forged := base()
	forged["policyEnvelope"] = map[string]any{"contentHash": "forged"}
	if schema.Validate(forged) == nil {
		t.Fatal("Core-derived policy envelope accepted")
	}
}

func TestRuntimePolicyProjectionDoesNotDefaultMalformedEvidence(t *testing.T) {
	for _, value := range []map[string]any{
		{"agentId": false}, {"executionPolicy": nil}, {"executionPolicy": ""}, {"agentId": "pi"},
		{"executionPolicy": "COOPERATIVE_TRUSTED_V1"}, {"agentId": "pi", "executionPolicy": "TASK_SANDBOX_V1"},
	} {
		if validRuntimePolicyShape(value) {
			t.Fatalf("malformed runtime policy accepted: %#v", value)
		}
	}
	if !validRuntimePolicyShape(map[string]any{}) || !validRuntimePolicyShape(map[string]any{"agentId": "pi", "executionPolicy": "COOPERATIVE_TRUSTED_V1", "agentConfigFingerprint": strings.Repeat("a", 64)}) {
		t.Fatal("valid policy projection refused")
	}
}
