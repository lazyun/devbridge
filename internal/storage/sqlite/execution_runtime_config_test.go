package sqlite

import (
	"testing"

	"github.com/lazyun/devbridge/internal/codexruntime"
)

func TestExecutionRuntimeConfigReplayMatchesOnlyOriginalSelection(t *testing.T) {
	persisted := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: true, ServiceTier: "priority"}
	if !matchesReplayRuntimeConfig(codexruntime.NextRunRuntimeConfig{Model: persisted.Model, ReasoningEffort: persisted.ReasoningEffort, CodexFastMode: true}, persisted) {
		t.Fatal("exact runtime retry rejected")
	}
	for _, changed := range []codexruntime.NextRunRuntimeConfig{
		{Model: "gpt-5.6-sol", ReasoningEffort: "high", CodexFastMode: true},
		{Model: persisted.Model, ReasoningEffort: "xhigh", CodexFastMode: true},
		{Model: persisted.Model, ReasoningEffort: persisted.ReasoningEffort, CodexFastMode: false},
	} {
		if matchesReplayRuntimeConfig(changed, persisted) {
			t.Fatalf("changed retry accepted: %+v", changed)
		}
	}
}

func TestFreezeExecutionRuntimeConfigDerivesServiceTier(t *testing.T) {
	fast, err := freezeExecutionRuntimeConfig(codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: true})
	if err != nil || fast.ServiceTier != "priority" {
		t.Fatalf("fast=%+v err=%v", fast, err)
	}
	off, err := freezeExecutionRuntimeConfig(codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: false, ServiceTier: "priority"})
	if err != nil || off.ServiceTier != "" {
		t.Fatalf("off=%+v err=%v", off, err)
	}
}
