package codexruntime

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestDefaultAllowlistIsComplete(t *testing.T) {
	value := Default()
	if !value.Valid() {
		t.Fatalf("Default().Valid() = false: %+v", value)
	}
	wantModels := []string{"gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"}
	if !reflect.DeepEqual(value.Models, wantModels) || len(value.ReasoningEfforts) != 6 {
		t.Fatalf("Default allowlist = %+v", value)
	}
	candidates := ModelCandidates()
	candidates[0] = "mutated"
	if got := ModelCandidates(); !reflect.DeepEqual(got, wantModels) {
		t.Fatalf("ModelCandidates shared backing storage: %v", got)
	}
}

func TestModelConfigsValidateDefaultsAndPreserveDisabledPreferences(t *testing.T) {
	value := Default()
	value.ModelConfigs[0].Enabled = false
	value.ModelConfigs[0].DefaultReasoningEffort = "high"
	normalized, err := value.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ModelConfigs[0].DefaultReasoningEffort != "high" {
		t.Fatalf("disabled default lost: %+v", normalized.ModelConfigs[0])
	}
	updated := normalized.Clone()
	updated.ModelConfigs[0].SupportedReasoningEfforts = []string{"low", "ultra"}
	if err := updated.Validate(); err == nil {
		t.Fatal("unsupported default accepted")
	}
	updated.ModelConfigs[0].DefaultReasoningEffort = "low"
	if err := updated.Validate(); err != nil {
		t.Fatalf("updated catalog capabilities rejected: %v", err)
	}
	if _, err := normalized.ResolveSelection(normalized.ModelConfigs[0].ModelID, "medium"); err == nil {
		t.Fatal("disabled model selected")
	}
}

func TestRuntimeModelsAreClosedWhileModelIDShapeRemainsGeneric(t *testing.T) {
	value, err := ParseStrict("gpt-5.6-luna,gpt-6-astra", "low,xhigh")
	if err != nil || !value.Valid() || !value.AllowModel("gpt-6-astra") {
		t.Fatalf("fixed inventory = %+v, %v", value, err)
	}
	if err := ValidateModelID("openai/custom.model_v2"); err != nil {
		t.Fatalf("historical model ID shape rejected: %v", err)
	}
	if err := ValidateModels([]string{"openai/custom.model_v2"}); err == nil {
		t.Fatal("unknown runtime model accepted")
	}
	for _, invalid := range []string{"", " leading", "two words", "has,comma", "bad\x00id", strings.Repeat("m", MaxModelIDBytes+1)} {
		if err := ValidateModelID(invalid); err == nil {
			t.Fatalf("ValidateModelID(%q) succeeded", invalid)
		}
	}
	if _, err := ParseStrict("gpt-6-astra", "invalid"); err == nil {
		t.Fatal("invalid explicit reasoning inventory fell back to defaults")
	}
	if _, err := ParseStrict("gpt-6-codex-preview", "low"); err == nil {
		t.Fatal("unknown explicit model fell back to defaults")
	}
	if err := ValidateModels([]string{"gpt-5.6-luna", "gpt-5.6-luna"}); err == nil {
		t.Fatal("duplicate models accepted")
	}
	tooMany := append(ModelCandidates(), "gpt-6-codex-preview")
	if err := ValidateModels(tooMany); err == nil {
		t.Fatalf("more than %d models accepted", MaxModels)
	}
}

func TestAuthoritySnapshotsAreDetachedAndAtomicallyReplaced(t *testing.T) {
	authority, err := NewAuthority(Allowlist{Models: []string{"gpt-5.6-luna"}, ReasoningEfforts: []string{"low"}})
	if err != nil {
		t.Fatal(err)
	}
	first := authority.Snapshot()
	first.Allowlist.Models[0] = "mutated"
	if got := authority.Snapshot().Allowlist.Models[0]; got != "gpt-5.6-luna" {
		t.Fatalf("snapshot shared model backing array: %q", got)
	}
	replacement := Allowlist{Models: []string{"gpt-6-astra"}, ReasoningEfforts: []string{"xhigh"}}
	next, err := authority.Apply(first.Revision, replacement)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Models[0] = "mutated-input"
	next.Allowlist.Models[0] = "mutated-result"
	if got := authority.Snapshot().Allowlist.Models[0]; got != "gpt-6-astra" {
		t.Fatalf("apply shared mutable backing array: %q", got)
	}
	if _, err := authority.Apply(first.Revision, Default()); err != ErrStaleRevision {
		t.Fatalf("stale apply error = %v", err)
	}

	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for index := 0; index < 100; index++ {
			model, effort := "gpt-5.6-terra", "low"
			if index%2 != 0 {
				model, effort = "gpt-5.6-sol", "high"
			}
			snapshot := authority.Snapshot()
			if _, applyErr := authority.Apply(snapshot.Revision, Allowlist{Models: []string{model}, ReasoningEfforts: []string{effort}}); applyErr != nil {
				t.Errorf("concurrent Apply(): %v", applyErr)
				return
			}
		}
	}()
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 100; j++ {
				snapshot := authority.Snapshot()
				if snapshot.Revision < next.Revision || len(snapshot.Allowlist.Models) != 1 {
					t.Errorf("incoherent snapshot: %+v", snapshot)
					return
				}
				model := snapshot.Allowlist.Models[0]
				if !(model == "gpt-6-astra" || model == "gpt-5.6-terra" || model == "gpt-5.6-sol") {
					t.Errorf("mixed inventory generations: %+v", snapshot)
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestParseStrictAcceptsValidSubsets(t *testing.T) {
	value, err := ParseStrict("gpt-5.6-terra,gpt-5.6-sol", "high")
	if err != nil || !value.Valid() || len(value.Models) != 2 {
		t.Fatalf("ParseStrict() = %+v, %v", value, err)
	}
	if !value.AllowModel("gpt-5.6-terra") || value.AllowModel("gpt-5.6-luna") {
		t.Fatalf("ParseStrict() model semantics = %+v", value)
	}
}

func TestParseStrictDefaultsOnlyOmittedDimensions(t *testing.T) {
	empty, err := ParseStrict("", "")
	if err != nil || !reflect.DeepEqual(empty, Default()) {
		t.Fatalf("empty ParseStrict() = %+v, %v", empty, err)
	}
}

func TestDefaultReturnsDeeplyDetachedCapabilities(t *testing.T) {
	first := Default()
	first.ModelConfigs[0].SupportedReasoningEfforts[0] = "mutated"
	second := Default()
	if second.ModelConfigs[0].SupportedReasoningEfforts[0] != "low" {
		t.Fatalf("Default leaked nested capability mutation: %+v", second.ModelConfigs[0])
	}
}

func TestValidateRejectsDisabledValues(t *testing.T) {
	value, err := ParseStrict("gpt-5.6-sol", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	if err := value.ValidateModel("gpt-5.6-luna"); err == nil || !strings.Contains(err.Error(), "MODEL_NOT_ALLOWED") {
		t.Fatalf("ValidateModel() = %v", err)
	}
	if _, err := value.ResolveSelection("gpt-5.6-sol", "low"); err != nil {
		t.Fatalf("ResolveSelection() = %v", err)
	}
	selection, ok := value.ValidateModel("gpt-5.6-luna").(*SelectionError)
	if !ok {
		t.Fatalf("ValidateModel() = %T, want *SelectionError", value.ValidateModel("gpt-5.6-luna"))
	}
	if selection.Value != "gpt-5.6-luna" || len(selection.Candidates) != 1 || selection.Candidates[0] != "gpt-5.6-sol" {
		t.Fatalf("SelectionError candidates = %+v", selection)
	}
}

func TestAuthorityPreservesCanonicalOnlyStartupConfig(t *testing.T) {
	configs := Default().ModelConfigs
	for i := range configs {
		configs[i].Enabled = configs[i].ModelID == "gpt-5.6-terra"
		if configs[i].Enabled {
			configs[i].DefaultReasoningEffort = "xhigh"
		}
	}
	authority, err := NewAuthority(Allowlist{ModelConfigs: configs})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := authority.ExecutionAllowlist()
	if len(snapshot.Models) != 1 || snapshot.Models[0] != "gpt-5.6-terra" {
		t.Fatalf("startup enabled selection overwritten: %+v", snapshot)
	}
	got, err := snapshot.ResolveSelection("gpt-5.6-terra", "")
	if err != nil || got != "xhigh" {
		t.Fatalf("startup default=%q err=%v", got, err)
	}
	configs[1].DefaultReasoningEffort = "low"
	got, _ = snapshot.ResolveSelection("gpt-5.6-terra", "")
	if got != "xhigh" {
		t.Fatal("startup snapshot shares mutable config")
	}
}
