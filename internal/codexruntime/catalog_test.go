package codexruntime

import (
	"fmt"
	"reflect"
	"testing"
)

func discoveredModel() CodingAgentModelConfig {
	tier := "priority"
	return CodingAgentModelConfig{ModelID: "gpt-new-model", SupportedReasoningEfforts: []string{"low", "high", "future"}, DefaultReasoningEffort: "high", FastServiceTier: &tier}
}

func TestMergeCatalogPreservesPreferencesAndEnablesDiscoveredModel(t *testing.T) {
	before := Default()
	before.ModelConfigs[0].Enabled = false
	before.ModelConfigs[1].DefaultReasoningEffort = "high"
	noFast := ""
	updated := before.ModelConfigs[1]
	updated.DefaultReasoningEffort = "low"
	updated.FastServiceTier = &noFast
	newModel := discoveredModel()
	merged, err := before.MergeCatalog([]CodingAgentModelConfig{updated, newModel})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.ModelConfigs) != 5 || merged.ModelConfigs[0].Enabled || merged.ModelConfigs[1].DefaultReasoningEffort != "high" || merged.ModelConfigs[4].Enabled {
		t.Fatalf("merge lost preferences: %+v", merged.ModelConfigs)
	}
	if _, err := merged.ResolveSelection(newModel.ModelID, ""); err == nil {
		t.Fatal("new model auto-enabled")
	}
	merged.ModelConfigs[4].Enabled = true
	merged, err = merged.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	config, err := (NextRunRuntimeConfig{Model: newModel.ModelID, CodexFastMode: true}).Normalize(merged)
	if err != nil || config.ReasoningEffort != "high" || config.ServiceTier != "priority" {
		t.Fatalf("selection=%+v err=%v", config, err)
	}
	if err := config.ValidateFrozen(merged); err != nil {
		t.Fatal(err)
	}
	if !merged.AllowReasoningEffort("future") {
		t.Fatal("new reasoning value was dropped")
	}
	copy, _ := merged.ModelConfig(newModel.ModelID)
	*copy.FastServiceTier = ""
	if merged.ModelConfigs[4].FastTier() != "priority" {
		t.Fatal("ModelConfig aliases the stored tier")
	}
	if _, err := (NextRunRuntimeConfig{Model: updated.ModelID, CodexFastMode: true}).Normalize(merged); err == nil {
		t.Fatal("live catalog Fast unavailability was ignored")
	}
	authority, err := NewAuthority(merged)
	if err != nil {
		t.Fatal(err)
	}
	*newModel.FastServiceTier = ""
	snapshot := authority.Snapshot()
	*snapshot.Allowlist.ModelConfigs[4].FastServiceTier = ""
	if authority.Snapshot().Allowlist.ModelConfigs[4].FastTier() != "priority" {
		t.Fatal("catalog capability mutation leaked")
	}
	if len(before.ModelConfigs) != 4 {
		t.Fatal("merge mutated source")
	}
}

func TestMergeCatalogReplacesUnsupportedDefaultAndRejectsInvalidReads(t *testing.T) {
	before := Default()
	changed := before.ModelConfigs[0]
	changed.SupportedReasoningEfforts = []string{"high"}
	changed.DefaultReasoningEffort = "high"
	merged, err := before.MergeCatalog([]CodingAgentModelConfig{changed})
	if err != nil || merged.ModelConfigs[0].DefaultReasoningEffort != "high" {
		t.Fatalf("merge=%+v err=%v", merged, err)
	}
	for _, models := range [][]CodingAgentModelConfig{nil, {discoveredModel(), discoveredModel()}, {{ModelID: "bad id", SupportedReasoningEfforts: []string{"low"}, DefaultReasoningEffort: "low"}}} {
		if _, err := before.MergeCatalog(models); err == nil {
			t.Fatalf("invalid catalog accepted: %+v", models)
		}
	}
	if !reflect.DeepEqual(before, Default()) {
		t.Fatal("invalid read changed source")
	}
	tooMany := make([]CodingAgentModelConfig, MaxModels+1)
	for i := range tooMany {
		tooMany[i] = discoveredModel()
		tooMany[i].ModelID = fmt.Sprintf("gpt-new-%d", i)
	}
	if _, err := before.MergeCatalog(tooMany); err == nil {
		t.Fatal("catalog limit ignored")
	}
}
