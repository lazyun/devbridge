package agent_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agent"
)

func TestRegistrySelectsExactIdentityWithoutFallback(t *testing.T) {
	registry, err := agent.NewRegistry(
		agent.Registration[string]{Descriptor: agent.Descriptor{ID: "codex", Capabilities: []agent.Capability{agent.CapabilityThreadResume, agent.CapabilitySameTurnRecovery}}, Value: "codex handle"},
		agent.Registration[string]{Descriptor: agent.Descriptor{ID: "pi"}, Value: "pi handle"},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex", "pi"} {
		got, err := registry.Select(id)
		if err != nil || got != id+" handle" {
			t.Fatalf("Select(%q)=%q, %v", id, got, err)
		}
	}
	for _, id := range []string{"", "Codex", " codex", "codex ", "missing"} {
		got, err := registry.Select(id)
		if got != "" || !errors.Is(err, agent.ErrAgentUnavailable) {
			t.Fatalf("unknown identity %q fell back: %q, %v", id, got, err)
		}
	}
	got, err := registry.Select("pi", agent.CapabilitySameTurnRecovery, agent.CapabilityThreadResume, agent.CapabilitySameTurnRecovery)
	var missing *agent.CapabilityError
	if got != "" || !errors.Is(err, agent.ErrCapabilityUnsupported) || !errors.As(err, &missing) || missing.AgentID != "pi" || !reflect.DeepEqual(missing.Missing, []agent.Capability{agent.CapabilitySameTurnRecovery, agent.CapabilityThreadResume}) {
		t.Fatalf("missing capability selection=%q, %v (%+v)", got, err, missing)
	}
	if got, err = registry.Select("codex", agent.CapabilitySameTurnRecovery); err != nil || got != "codex handle" {
		t.Fatalf("supported selection=%q, %v", got, err)
	}
}

func TestRegistryFreezesDescriptorCapabilities(t *testing.T) {
	capabilities := []agent.Capability{agent.CapabilityThreadResume}
	descriptor := agent.Descriptor{ID: "original", Capabilities: capabilities}
	registration := agent.Registration[int]{Descriptor: descriptor, Value: 17}
	registry, err := agent.NewRegistry(registration)
	if err != nil {
		t.Fatal(err)
	}
	capabilities[0] = agent.CapabilitySameTurnRecovery
	registration.Descriptor.ID = "replacement"
	registration.Value = 99
	if got, err := registry.Select("original", agent.CapabilityThreadResume); err != nil || got != 17 {
		t.Fatalf("registered handle mutated: %d, %v", got, err)
	}
	if got, err := registry.Select("original", agent.CapabilitySameTurnRecovery); got != 0 || !errors.Is(err, agent.ErrCapabilityUnsupported) {
		t.Fatalf("later capability leaked: %d, %v", got, err)
	}
	if _, err := registry.Select("replacement"); !errors.Is(err, agent.ErrAgentUnavailable) {
		t.Fatalf("later identity leaked: %v", err)
	}
}

func TestRegistryRejectsInvalidOrDuplicateIdentity(t *testing.T) {
	for _, id := range []string{"", " ", " leading", "trailing ", "\tbad"} {
		if registry, err := agent.NewRegistry(agent.Registration[int]{Descriptor: agent.Descriptor{ID: id}, Value: 1}); err == nil || registry != nil {
			t.Fatalf("invalid identity %q accepted: %v", id, registry)
		}
	}
	if registry, err := agent.NewRegistry(agent.Registration[int]{Descriptor: agent.Descriptor{ID: "same"}, Value: 1}, agent.Registration[int]{Descriptor: agent.Descriptor{ID: "same"}, Value: 2}); err == nil || registry != nil {
		t.Fatalf("duplicate identity accepted: %v", registry)
	}
}

func TestRegistryNilAndEmptyFailClosed(t *testing.T) {
	empty, err := agent.NewRegistry[string]()
	if err != nil {
		t.Fatal(err)
	}
	for _, registry := range []*agent.Registry[string]{nil, {}, empty} {
		got, err := registry.Select("codex", agent.CapabilityThreadResume)
		if got != "" || !errors.Is(err, agent.ErrAgentUnavailable) {
			t.Fatalf("empty registry selected %q, %v", got, err)
		}
	}
}
