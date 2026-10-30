package runtimepreparation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentPolicyHashPreservesLegacyAndBindsExplicitSelection(t *testing.T) {
	p := Plan{BackendIdentity: "backend", Model: "model", ReasoningEffort: "high"}
	raw, err := json.Marshal(p)
	if err != nil || strings.Contains(string(raw), "agentId") || strings.Contains(string(raw), "executionPolicy") || strings.Contains(string(raw), "agentConfigFingerprint") {
		t.Fatalf("legacy encoding changed: %s %v", raw, err)
	}
	legacyContent, _ := p.ComputeContentHash()
	legacyAuthority, _ := p.ComputeAuthoritySnapshotHash()
	// Reconstruct the historical authority vocabulary exactly; new empty fields
	// must not be inserted into its hash, even as empty strings.
	historical := map[string]any{"backendIdentity": p.BackendIdentity, "backendConfigFingerprint": "", "backendSource": "", "backendKind": "", "model": p.Model, "reasoningEffort": p.ReasoningEffort, "codexFastMode": false, "serviceTier": "", "runtimeAdapterIdentity": "", "runtimeProtocolIdentity": ""}
	b, _ := json.Marshal(historical)
	if legacyAuthority != hashBytesForPolicyTest(b) {
		t.Fatal("legacy authority hash changed")
	}
	p.AgentID = "pi"
	p.ExecutionPolicy = "COOPERATIVE_TRUSTED_V1"
	p.AgentConfigFingerprint = strings.Repeat("c", 64)
	content, _ := p.ComputeContentHash()
	authority, _ := p.ComputeAuthoritySnapshotHash()
	if content == legacyContent || authority == legacyAuthority {
		t.Fatal("explicit policy was not hash-bound")
	}
	p.AgentConfigFingerprint = strings.Repeat("d", 64)
	changedFingerprint, _ := p.ComputeAuthoritySnapshotHash()
	if changedFingerprint == authority {
		t.Fatal("agent launch configuration was not hash-bound")
	}
	p.AgentID = "codex"
	changed, _ := p.ComputeAuthoritySnapshotHash()
	if changed == authority {
		t.Fatal("agent selection was not hash-bound")
	}
	p.ExecutionPolicy = "TASK_SANDBOX_V1"
	changedPolicy, _ := p.ComputeAuthoritySnapshotHash()
	if changedPolicy == changed {
		t.Fatal("policy downgrade was not hash-bound")
	}
}

func hashBytesForPolicyTest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
