package controlapi

import (
	"github.com/lazyun/devbridge/internal/codexruntime"
	"testing"
)

func TestOperationModelDefaultAndExplicitOverride(t *testing.T) {
	settings := codexruntime.Default()
	// Use current runtime capability inventory rather than a historical model
	// list: select two entries with different configured defaults.
	settings.ModelConfigs[0].DefaultReasoningEffort = "medium"
	settings.ModelConfigs[1].DefaultReasoningEffort = "high"
	authority, err := codexruntime.NewAuthority(settings)
	if err != nil {
		t.Fatal(err)
	}
	f := &Facade{allowlistProvider: authority}
	a, b := settings.ModelConfigs[0].ModelID, settings.ModelConfigs[1].ModelID
	for _, tc := range []struct{ model, override, want string }{{a, "", "medium"}, {b, "", "high"}, {a, "xhigh", "xhigh"}, {b, "low", "low"}} {
		got, err := f.resolveCurrentExecutionSelection(tc.model, tc.override)
		if err != nil || got != tc.want {
			t.Fatalf("%+v -> %s %v", tc, got, err)
		}
	}
	next := authority.ExecutionAllowlist()
	next.ModelConfigs[0].DefaultReasoningEffort = "high"
	if _, err := authority.Apply(authority.Snapshot().Revision, next); err != nil {
		t.Fatal(err)
	}
	if got, err := f.resolveCurrentExecutionSelection(b, ""); err != nil || got != "high" {
		t.Fatal("model A update changed model B")
	}
	if _, err := f.resolveCurrentExecutionSelection(a, "invalid"); err == nil {
		t.Fatal("invalid effort admitted")
	}
	if _, err := f.resolveCurrentExecutionSelection("unavailable-model", "medium"); err == nil {
		t.Fatal("unknown model admitted")
	}
	if _, err := f.resolveCurrentExecutionSelection("", "medium"); err == nil {
		t.Fatal("effort without a model admitted")
	}
}
