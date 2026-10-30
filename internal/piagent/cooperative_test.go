package piagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/runtimepermission"
)

type fixtureLaunch struct {
	Args                   []string
	PolicyFile, PolicyText string
}

func cooperativeRequest(t *testing.T, readOnly bool, factory *Factory) agent.LaunchRequest {
	t.Helper()
	cwd := t.TempDir()
	fingerprint := factory.AgentDescriptor().ConfigFingerprint
	if fingerprint == "" {
		t.Fatal("fixture launcher has no configured identity")
	}
	e := agentpolicy.Envelope{AgentConfigFingerprint: fingerprint, Version: agentpolicy.EnvelopeVersion, Policy: agentpolicy.PolicyCooperativeTrustedV1, AgentID: "pi", RunID: "run-cooperative", TaskID: "task", ExecutionID: "execution", RevisionID: "revision", Purpose: "IMPLEMENTATION", CWD: cwd, ReadOnly: readOnly, ReadableRoots: []string{cwd}, EngineeringScope: json.RawMessage(`{"kind":"REPOSITORY"}`)}
	if !readOnly {
		e.WritableRoots = []string{cwd}
	} else {
		e.Purpose = "VERIFICATION"
	}
	e, err := e.Seal()
	if err != nil {
		t.Fatal(err)
	}
	return agent.LaunchRequest{AgentConfigFingerprint: fingerprint, RunID: e.RunID, CWD: cwd, Policy: &e, Runtime: agent.RuntimeSelection{Model: "fixture/fixture-model", ReasoningEffort: "medium"}, OnPolicyConfigured: func(agentpolicy.LaunchReceipt) error { return nil }}
}

func readFixtureLaunch(t *testing.T, capture string) fixtureLaunch {
	t.Helper()
	data, err := os.ReadFile(capture + ".launch.json")
	if err != nil {
		t.Fatal(err)
	}
	var launch fixtureLaunch
	if err := json.Unmarshal(data, &launch); err != nil {
		t.Fatal(err)
	}
	return launch
}

func TestPiCooperativePolicyUsesSeparateRecordedSystemChannel(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "implementation", true: "verification"}[readOnly], func(t *testing.T) {
			factory, capture := fixtureFactory(t, "success")
			factory.ExtraArgs = []string{"--offline", "--no-extensions", "--tools", "read,grep,find,ls"}
			request := cooperativeRequest(t, readOnly, factory)
			var receipt agentpolicy.LaunchReceipt
			request.OnPolicyConfigured = func(value agentpolicy.LaunchReceipt) error {
				receipt = value
				raw, _ := os.ReadFile(capture)
				if strings.Contains(string(raw), `"type":"prompt"`) {
					t.Fatal("task dispatched before durable launch recording")
				}
				return value.Validate()
			}
			// Use the real production descriptor/Factory registration, not a
			// synthetic capability list: otherwise a missing declaration can
			// leave a tested adapter unreachable through Core's registry.
			var production agent.Factory = factory
			registry, err := agent.NewRegistry(agent.Registration[agent.Factory]{Descriptor: production.AgentDescriptor(), Value: production})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := registry.Select("pi", agent.CapabilityCooperativePolicy, agent.CapabilityTurnStarted, agent.CapabilityInterrupt)
			if err != nil {
				t.Fatal(err)
			}
			s, err := selected.StartAgent(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			launch := readFixtureLaunch(t, capture)
			if launch.PolicyText != request.Policy.SystemText || launch.PolicyFile == "" || strings.HasPrefix(launch.PolicyFile, request.CWD) {
				t.Fatalf("launch=%+v", launch)
			}
			if receipt.AgentConfigFingerprint != request.AgentConfigFingerprint || receipt.RunID != request.RunID || receipt.AgentID != "pi" || receipt.PolicySHA256 != request.Policy.SHA256 || receipt.Status != agentpolicy.LaunchConfigured || receipt.Executable != factory.PiPath || !reflect.DeepEqual(receipt.Args, launch.Args[4:]) {
				t.Fatalf("receipt=%+v launch=%+v", receipt, launch)
			}
			id, err := s.StartThread(context.Background(), request.CWD)
			if err != nil {
				t.Fatal(err)
			}
			prompt := "Frozen engineering instruction.\nNo policy text may be appended here."
			if _, err := s.StartTurn(context.Background(), id, prompt); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(capture)
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var command rpcCommand
				_ = json.Unmarshal([]byte(line), &command)
				if command.Type == "prompt" && command.Message != prompt {
					t.Fatalf("prompt altered: %q", command.Message)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(launch.PolicyFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("policy input survived close: %v", err)
			}
		})
	}
}

func TestPiCooperativeLaunchRejectsMissingOrStrongerGuarantees(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*agent.LaunchRequest)
	}{
		{"missing policy", func(r *agent.LaunchRequest) { r.Policy = nil }},
		{"native policy", func(r *agent.LaunchRequest) { r.Policy.Policy = agentpolicy.PolicyTaskSandboxV1 }},
		{"native permissions", func(r *agent.LaunchRequest) {
			r.RuntimeBinding.TaskPermissions = &runtimepermission.TaskPermissions{Version: runtimepermission.TaskPermissionVersion}
		}},
		{"unrecorded policy", func(r *agent.LaunchRequest) { r.OnPolicyConfigured = nil }},
		{"foreign run", func(r *agent.LaunchRequest) { r.RunID = "foreign" }},
		{"foreign agent", func(r *agent.LaunchRequest) { r.Policy.AgentID = "codex"; p, _ := r.Policy.Seal(); r.Policy = &p }},
		{"tampered text", func(r *agent.LaunchRequest) { r.Policy.SystemText += "not frozen" }},
		{"service tier", func(r *agent.LaunchRequest) { r.Runtime.ServiceTier = "fast" }},
		{"foreign executable", func(r *agent.LaunchRequest) { r.Backend.Executable = "/different/pi" }},
		{"foreign roots", func(r *agent.LaunchRequest) { r.RuntimeBinding.RuntimeWorkspaceRoots = []string{"/different/root"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory, capture := fixtureFactory(t, "success")
			r := cooperativeRequest(t, false, factory)
			tc.mutate(&r)
			s, err := factory.StartAgent(context.Background(), r)
			if err == nil || s != nil {
				t.Fatalf("session=%v err=%v", s, err)
			}
			if _, err := os.Stat(capture); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid launch reached subprocess: %v", err)
			}
		})
	}
}

func TestPiCooperativeRecordingFailureCannotDispatchTask(t *testing.T) {
	factory, capture := fixtureFactory(t, "success")
	r := cooperativeRequest(t, false, factory)
	want := errors.New("receipt persistence failed")
	r.OnPolicyConfigured = func(agentpolicy.LaunchReceipt) error { return want }
	s, err := factory.StartAgent(context.Background(), r)
	if s != nil || !errors.Is(err, want) {
		t.Fatalf("session=%v err=%v", s, err)
	}
	launch := readFixtureLaunch(t, capture)
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), `"type":"prompt"`) {
		t.Fatal("prompt sent after recording failure")
	}
	if _, err := os.Stat(launch.PolicyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("policy input leaked: %v", err)
	}
}

func TestPiCooperativePolicyTamperingBlocksPrompt(t *testing.T) {
	factory, capture := fixtureFactory(t, "success")
	r := cooperativeRequest(t, false, factory)
	s, err := factory.StartAgent(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	launch := readFixtureLaunch(t, capture)
	if err := os.WriteFile(launch.PolicyFile, []byte("changed policy"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartTurn(context.Background(), "fixture-session", "must not dispatch"); err == nil {
		t.Fatal("tampered policy accepted")
	}
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), `"type":"prompt"`) {
		t.Fatalf("task sent with altered policy: %s", raw)
	}
}

func TestPiSafeExtraArgumentsRemainSupported(t *testing.T) {
	for _, args := range [][]string{{"--verbose", "--offline"}, {"--no-extensions", "--no-mcp"}, {"--extension", "/trusted/extension.ts", "--tools", "read,bash"}} {
		if err := validateExtraArgs(args); err != nil {
			t.Fatalf("safe args=%v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"--append-system-prompt", "replacement"}, {"--continue"}, {"--extension"}, {"--mystery", "hidden prompt"}, {"--", "hidden prompt"}} {
		if err := validateExtraArgs(args); err == nil {
			t.Fatalf("conflicting args accepted: %v", args)
		}
	}
}
