package codexruntime

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"strings"
	"testing"
)

func TestRuntimePolicyLegacyJSONUnchanged(t *testing.T) {
	const legacy = `{"model":"gpt-5.6-luna","reasoningEffort":"medium","codexFastMode":false,"serviceTier":""}`
	var value NextRunRuntimeConfig
	if err := json.Unmarshal([]byte(legacy), &value); err != nil {
		t.Fatal(err)
	}
	normalized, err := value.Normalize(Default())
	if err != nil || normalized != value {
		t.Fatalf("legacy changed: %#v, %v", normalized, err)
	}
	raw, err := json.Marshal(normalized)
	if err != nil || string(raw) != legacy {
		t.Fatalf("legacy bytes changed: %s, %v", raw, err)
	}
}

func TestRuntimePolicyRequiresProspectiveExactSelection(t *testing.T) {
	for _, tc := range []struct {
		agent, policy string
		valid         bool
	}{
		{"", "", true}, {"codex", "", true}, {"", agentpolicy.PolicyTaskSandboxV1, true},
		{"codex", agentpolicy.PolicyCooperativeTrustedV1, true}, {"pi", agentpolicy.PolicyCooperativeTrustedV1, true},
		{"", agentpolicy.PolicyCooperativeTrustedV1, false}, {"pi", "", false}, {"pi", agentpolicy.PolicyTaskSandboxV1, false},
		{"other", agentpolicy.PolicyCooperativeTrustedV1, false}, {"codex", "cooperative", false},
	} {
		value := NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "medium", AgentID: tc.agent, ExecutionPolicy: tc.policy}
		allowlist := Default()
		allowlist.AgentConfigFingerprints = map[string]string{"codex": strings.Repeat("c", 64), "pi": strings.Repeat("a", 64)}
		if tc.policy == agentpolicy.PolicyCooperativeTrustedV1 {
			value.AgentConfigFingerprint = allowlist.AgentConfigFingerprints[tc.agent]
		}
		normalized, err := value.Normalize(allowlist)
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.valid && normalized != value {
			t.Fatalf("normalization changed agent/policy: %#v -> %#v", value, normalized)
		}
		if tc.valid && normalized.ValidateFrozen(allowlist) != nil {
			t.Fatalf("valid frozen value rejected: %#v", normalized)
		}
	}
}

func TestRuntimePolicyRequiresExactCurrentConfiguredFingerprint(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	value := NextRunRuntimeConfig{Model: "gpt-5.6-luna", ReasoningEffort: "medium", AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1, AgentConfigFingerprint: fingerprint}
	inventory := Default()
	if _, err := value.Normalize(inventory); err == nil {
		t.Fatal("unavailable configured agent accepted")
	}
	inventory.AgentConfigFingerprints = map[string]string{"pi": fingerprint}
	if got, err := value.Normalize(inventory); err != nil || got != value {
		t.Fatalf("exact registered agent rejected: %+v %v", got, err)
	}
	changed := value
	changed.AgentConfigFingerprint = strings.Repeat("b", 64)
	if _, err := changed.Normalize(inventory); err == nil {
		t.Fatal("substituted launcher fingerprint accepted")
	}
	inventory.AgentConfigFingerprints["pi"] = strings.Repeat("b", 64)
	if err := value.ValidateFrozen(inventory); err == nil {
		t.Fatal("restart with changed configured launcher accepted")
	}
	value.AgentConfigFingerprint = ""
	if _, err := value.Normalize(inventory); err == nil {
		t.Fatal("missing fingerprint silently inferred")
	}
}

func TestAgentInventoryProviderIsDetachedAndNotModelSettingsAuthority(t *testing.T) {
	initial := Default()
	initial.AgentConfigFingerprints = map[string]string{"pi": strings.Repeat("f", 64)}
	authority, err := NewAuthority(initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(authority.Snapshot().Allowlist.AgentConfigFingerprints) != 0 {
		t.Fatal("settings created registry authority")
	}
	configured := map[string]string{"pi": strings.Repeat("a", 64)}
	authority.SetAgentConfigFingerprintProvider(func() map[string]string { return configured })
	snapshot := authority.Snapshot()
	snapshot.Allowlist.AgentConfigFingerprints["pi"] = strings.Repeat("b", 64)
	if authority.ExecutionAllowlist().AgentConfigFingerprints["pi"] != configured["pi"] {
		t.Fatal("snapshot mutated configured inventory")
	}
	next := Default()
	next.AgentConfigFingerprints = map[string]string{"pi": strings.Repeat("f", 64)}
	applied, err := authority.Apply(snapshot.Revision, next)
	if err != nil || applied.Allowlist.AgentConfigFingerprints["pi"] != configured["pi"] {
		t.Fatalf("settings replaced registry authority: %+v %v", applied, err)
	}
	configured["pi"] = strings.Repeat("c", 64)
	if authority.ExecutionAllowlist().AgentConfigFingerprints["pi"] != configured["pi"] {
		t.Fatal("current configuration drift not observed")
	}
	detached, err := authority.ExecutionAllowlist().Normalize()
	if err != nil {
		t.Fatal(err)
	}
	delete(detached.AgentConfigFingerprints, "pi")
	if len(authority.ExecutionAllowlist().AgentConfigFingerprints) != 1 {
		t.Fatal("normalization aliased registry map")
	}
}
