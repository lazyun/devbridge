package piagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentpolicy"
)

func TestPiConfiguredIdentityUsesLauncherBytesAndEveryFactorySetting(t *testing.T) {
	f, _ := fixtureFactory(t, "success")
	original := f.AgentDescriptor().ConfigFingerprint
	if len(original) != 64 || f.AgentDescriptor().ConfigFingerprint != original {
		t.Fatalf("fingerprint=%q", original)
	}
	for _, mutate := range []func(*Factory){
		func(f *Factory) { f.Model = "new-default-model" }, func(f *Factory) { f.ThinkingLevel = "high" },
		func(f *Factory) { f.SessionDir = "/different/session/storage" }, func(f *Factory) { f.ExtraArgs = []string{"--offline"} },
	} {
		snapshot := *f
		mutate(&snapshot)
		if changed := snapshot.AgentDescriptor().ConfigFingerprint; changed == "" || changed == original {
			t.Fatalf("configuration mutation not measured: %+v %q", snapshot, changed)
		}
	}
	data, err := os.ReadFile(f.PiPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.PiPath, append(data, []byte("\n# changed executable bytes\n")...), 0700); err != nil {
		t.Fatal(err)
	}
	if changed := f.AgentDescriptor().ConfigFingerprint; changed == "" || changed == original {
		t.Fatal("executable bytes absent from fingerprint")
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if fp := (&Factory{PiPath: path}).AgentDescriptor().ConfigFingerprint; fp != "" {
			t.Fatalf("unavailable launcher fingerprint=%q", fp)
		}
	}
	if fp := (&Factory{PiPath: f.PiPath, ExtraArgs: []string{"--model", "override"}}).AgentDescriptor().ConfigFingerprint; fp != "" {
		t.Fatalf("invalid launch configuration fingerprint=%q", fp)
	}
}

func TestPiApprovedConfigurationMutationRejectsBeforeLaunch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Factory)
	}{
		{"arguments", func(f *Factory) { f.ExtraArgs = []string{"--offline"} }},
		{"default model", func(f *Factory) { f.Model = "changed" }},
		{"thinking", func(f *Factory) { f.ThinkingLevel = "high" }},
		{"session directory", func(f *Factory) { f.SessionDir = "/changed" }},
		{"launcher bytes", func(f *Factory) {
			data, _ := os.ReadFile(f.PiPath)
			_ = os.WriteFile(f.PiPath, append(data, []byte("\n# changed\n")...), 0700)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, capture := fixtureFactory(t, "success")
			r := cooperativeRequest(t, false, f)
			tc.mutate(f)
			s, err := f.StartAgent(context.Background(), r)
			if s != nil || err == nil {
				t.Fatalf("session=%v error=%v", s, err)
			}
			if _, err := os.Stat(capture); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("changed configuration launched: %v", err)
			}
		})
	}
}

func TestPiFrozenFingerprintRequiredAcrossRequestEnvelopeAndReceipt(t *testing.T) {
	for _, kind := range []string{"missing request", "changed request", "changed envelope"} {
		t.Run(kind, func(t *testing.T) {
			f, capture := fixtureFactory(t, "success")
			r := cooperativeRequest(t, false, f)
			switch kind {
			case "missing request":
				r.AgentConfigFingerprint = ""
			case "changed request":
				r.AgentConfigFingerprint = strings.Repeat("f", 64)
			case "changed envelope":
				r.Policy.AgentConfigFingerprint = strings.Repeat("f", 64)
				sealed, err := r.Policy.Seal()
				if err != nil {
					t.Fatal(err)
				}
				r.Policy = &sealed
			}
			s, err := f.StartAgent(context.Background(), r)
			if s != nil || err == nil {
				t.Fatalf("session=%v error=%v", s, err)
			}
			if _, err := os.Stat(capture); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("mismatched fingerprint launched")
			}
		})
	}
}

func TestPiLauncherMutationDuringStartupCannotRecordOrDispatch(t *testing.T) {
	f, capture := fixtureFactory(t, "mutate-launcher")
	t.Setenv("DEVBRIDGE_PI_MUTATE_LAUNCHER", f.PiPath)
	r := cooperativeRequest(t, false, f)
	recorded := false
	r.OnPolicyConfigured = func(agentpolicy.LaunchReceipt) error { recorded = true; return nil }
	s, err := f.StartAgent(context.Background(), r)
	if s != nil || err == nil || recorded {
		t.Fatalf("session=%v error=%v recorded=%v", s, err, recorded)
	}
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), `"type":"prompt"`) {
		t.Fatal("prompt sent after startup identity changed")
	}
}

func TestPiConfigMutationInRecordingCallbackFailsBeforePrompt(t *testing.T) {
	f, capture := fixtureFactory(t, "success")
	r := cooperativeRequest(t, false, f)
	r.OnPolicyConfigured = func(agentpolicy.LaunchReceipt) error { f.ExtraArgs = []string{"--offline"}; return nil }
	s, err := f.StartAgent(context.Background(), r)
	if s != nil || err == nil {
		t.Fatalf("session=%v error=%v", s, err)
	}
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), `"type":"prompt"`) {
		t.Fatal("prompt sent after config changed")
	}
}

func TestPiLauncherMutationAfterStartupBlocksFirstPrompt(t *testing.T) {
	f, capture := fixtureFactory(t, "success")
	r := cooperativeRequest(t, false, f)
	s, err := f.StartAgent(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data, _ := os.ReadFile(f.PiPath)
	if err := os.WriteFile(f.PiPath, append(data, []byte("\n# changed before prompt\n")...), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartTurn(context.Background(), "fixture-session", "must not execute"); err == nil {
		t.Fatal("changed binary accepted for task prompt")
	}
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), `"type":"prompt"`) {
		t.Fatal("prompt sent after launcher changed")
	}
}
