package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/artifact"
)

type policyArtifactMetadata struct {
	values []artifact.Artifact
	err    error
}

func (s *policyArtifactMetadata) Create(_ context.Context, value artifact.Artifact) error {
	if s.err != nil {
		return s.err
	}
	s.values = append(s.values, value)
	return nil
}

func TestDurableCooperativePolicyArtifactsPreserveExactBytesAndLaunchFacts(t *testing.T) {
	root := t.TempDir()
	sequence := 0
	fs, err := artifact.NewFilesystem(t.TempDir(), func() (string, error) { sequence++; return fmt.Sprintf("policy-%d", sequence), nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &policyArtifactMetadata{}
	recorder := &DurableResultRecorder{Filesystem: fs, Artifacts: store}
	policy, err := (agentpolicy.Envelope{AgentConfigFingerprint: strings.Repeat("a", 64), Version: agentpolicy.EnvelopeVersion, Policy: agentpolicy.PolicyCooperativeTrustedV1, AgentID: "pi", RunID: "run-policy", Purpose: "VERIFICATION", CWD: root, ReadOnly: true, ReadableRoots: []string{root}, ScratchRoots: []string{t.TempDir()}, EngineeringScope: json.RawMessage(`{"kind":"NONE"}`)}).Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err = recorder.RecordAgentPolicy(context.Background(), policy.RunID, policy); err != nil {
		t.Fatal(err)
	}
	receipt := agentpolicy.LaunchReceipt{AgentConfigFingerprint: strings.Repeat("a", 64), RunID: policy.RunID, AgentID: policy.AgentID, PolicySHA256: policy.SHA256, Status: agentpolicy.LaunchConfigured, Mechanism: "PI_APPEND_SYSTEM_PROMPT", Executable: "/configured/pi", Args: []string{"--mode", "rpc", "--no-session", "--append-system-prompt", "/transport-owned/policy.txt"}}
	if err = recorder.RecordAgentPolicyReceipt(context.Background(), policy.RunID, receipt); err != nil {
		t.Fatal(err)
	}
	if len(store.values) != 2 {
		t.Fatalf("metadata=%+v", store.values)
	}
	for i, value := range store.values {
		raw, err := os.ReadFile(value.Path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if value.SHA256 != hex.EncodeToString(sum[:]) || value.Kind != artifact.KindDiagnostic || value.RunID != policy.RunID {
			t.Fatalf("artifact identity changed: %+v", value)
		}
		var record struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			var saved agentpolicy.Envelope
			if err = json.Unmarshal(record.Value, &saved); err != nil {
				t.Fatal(err)
			}
			if record.Type != "AGENT_POLICY_ENVELOPE" || saved.Validate() != nil || saved.SystemText != policy.SystemText || saved.SHA256 != policy.SHA256 {
				t.Fatal("frozen policy bytes changed")
			}
		} else {
			var saved agentpolicy.LaunchReceipt
			if err = json.Unmarshal(record.Value, &saved); err != nil {
				t.Fatal(err)
			}
			if record.Type != "AGENT_POLICY_LAUNCH_RECEIPT" || saved.Validate() != nil || saved.Executable != receipt.Executable || saved.Status != agentpolicy.LaunchConfigured {
				t.Fatal("launch fact became delivery/qualification claim")
			}
		}
	}
	store.err = errors.New("metadata unavailable")
	if err = recorder.RecordAgentPolicy(context.Background(), policy.RunID, policy); !errors.Is(err, store.err) {
		t.Fatalf("metadata loss hidden: %v", err)
	}
	if err = recorder.RecordAgentPolicyReceipt(context.Background(), "foreign-run", receipt); err == nil {
		t.Fatal("foreign receipt accepted")
	}
}
