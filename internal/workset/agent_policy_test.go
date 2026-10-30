package workset

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/runtimepermission"
)

func TestRuntimeAuthorityEmptyPolicyPreservesExactHistoricalMaterial(t *testing.T) {
	material := struct {
		Z string
		A int
		B []string
	}{"old-layout", 7, nil}
	want, err := json.Marshal(material)
	if err != nil {
		t.Fatal(err)
	}
	got, err := marshalRuntimeAuthority(material, "", "", "")
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("historical material changed: %s != %s (%v)", got, want, err)
	}
	w := runtimeFixture(t)
	raw, err := json.Marshal(w)
	if err != nil || bytes.Contains(raw, []byte(`"agentId"`)) || bytes.Contains(raw, []byte(`"executionPolicy"`)) || bytes.Contains(raw, []byte(`"agentConfigFingerprint"`)) {
		t.Fatalf("legacy JSON gained prospective fields: %s %v", raw, err)
	}
}

func TestRuntimeWorksetPolicyAndAgentAreFrozenAcrossAllHashVersions(t *testing.T) {
	for _, version := range []int{RuntimeProtocolVersion, RuntimeProtocolVersionProjectInspection, RuntimeProtocolVersionRecoveryStatus} {
		w := runtimeFixture(t)
		w.ProtocolVersion = version
		legacy, err := w.ComputeContentHash()
		if err != nil {
			t.Fatal(err)
		}
		w.AgentConfigFingerprint = strings.Repeat("a", 64)
		w.AgentID, w.ExecutionPolicy = "pi", agentpolicy.PolicyCooperativeTrustedV1
		cooperative, err := w.ComputeContentHash()
		if err != nil || cooperative == legacy {
			t.Fatalf("v%d policy omitted from hash: %v", version, err)
		}
		w.AgentConfigFingerprint = strings.Repeat("b", 64)
		changedConfig, err := w.ComputeContentHash()
		if err != nil || changedConfig == cooperative {
			t.Fatalf("v%d launcher fingerprint omitted from hash: %v", version, err)
		}
		w.AgentConfigFingerprint = strings.Repeat("a", 64)
		w.AgentID = "codex"
		codex, err := w.ComputeContentHash()
		if err != nil || codex == cooperative {
			t.Fatalf("v%d agent omitted from hash: %v", version, err)
		}
		w.AgentConfigFingerprint = ""
		w.ExecutionPolicy = agentpolicy.PolicyTaskSandboxV1
		native, err := w.ComputeContentHash()
		if err != nil || native == codex {
			t.Fatalf("v%d policy omitted from hash: %v", version, err)
		}
		w.AgentID, w.ExecutionPolicy = "", ""
		restored, err := w.ComputeContentHash()
		if err != nil || restored != legacy {
			t.Fatalf("v%d historical hash changed: %v", version, err)
		}
	}
}

func TestCooperativeWorksetRequiresExplicitValidAgentAndNoNativePermissions(t *testing.T) {
	for _, selection := range [][2]string{{"", agentpolicy.PolicyCooperativeTrustedV1}, {"pi", ""}, {"pi", agentpolicy.PolicyTaskSandboxV1}, {"codex", "unknown"}} {
		w := runtimeFixture(t)
		w.AgentID, w.ExecutionPolicy = selection[0], selection[1]
		if _, err := w.WithHash(); err == nil {
			t.Fatalf("invalid selection accepted: %v", selection)
		}
	}
	w := runtimeFixture(t)
	w.AgentConfigFingerprint = strings.Repeat("a", 64)
	w.AgentID, w.ExecutionPolicy = "pi", agentpolicy.PolicyCooperativeTrustedV1
	w.RuntimeAdapterIdentity, w.RuntimeProtocolIdentity = agentpolicy.CooperativeAdapterIdentity, agentpolicy.CooperativeProtocolIdentity
	w, err := w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	w.TaskPermissions = &runtimepermission.TaskPermissions{}
	if err := w.Validate(); err == nil {
		t.Fatal("cooperative mode accepted native task permissions")
	}
	w.TaskPermissions = nil
	w.ExecutionPolicy = ""
	if err := w.Validate(); err == nil {
		t.Fatal("stored mode changed without hash refusal")
	}
}
