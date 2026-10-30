package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/desktop/internal/settings"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/codexruntime"
)

func TestReadCodexModelsKeepsSettingsUntilSavedAndSurvivesReload(t *testing.T) {
	root := t.TempDir()
	paths := settings.PathsFor(filepath.Join(root, "config"), filepath.Join(root, "state"))
	app := newApp(paths, nil)
	app.settings.DataRoot = filepath.Join(root, "data")
	app.settings.CodingAgentModels[0].Enabled = false
	before := app.GetSettings()
	pinned := filepath.Join(root, "codex-pinned")
	if err := codexlaunch.Write(app.codexSelectionFile(), codexlaunch.Selection{Configured: "codex", Executable: pinned}); err != nil {
		t.Fatal(err)
	}
	app.modelCatalogReader = func(ctx context.Context, executable string) ([]codexruntime.CodingAgentModelConfig, error) {
		if executable != pinned {
			t.Fatalf("read used %q instead of pinned %q", executable, pinned)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("model read has no deadline")
		}
		tier := "priority"
		return []codexruntime.CodingAgentModelConfig{{ModelID: "gpt-new-model", SupportedReasoningEfforts: []string{"high", "future"}, DefaultReasoningEffort: "high", FastServiceTier: &tier}}, nil
	}
	models, err := app.ReadCodexModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 5 || models[4].Enabled || models[0].Enabled {
		t.Fatalf("models=%+v", models)
	}
	if !reflect.DeepEqual(app.GetSettings(), before) {
		t.Fatal("reading changed saved settings")
	}
	models[4].Enabled = true
	value := before.Settings
	value.CodingAgentModels = models
	store := settings.NewStore(paths)
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || !reflect.DeepEqual(loaded.CodingAgentModels, models) {
		t.Fatalf("reload=%+v err=%v", loaded.CodingAgentModels, err)
	}
	app.settings = loaded
	projection := app.GetSettings()
	if len(projection.CodexModelCandidates) != 5 || projection.CodexModelCandidates[4] != "gpt-new-model" {
		t.Fatal("saved model missing from candidates")
	}
	*projection.CodingAgentModels[4].FastServiceTier = ""
	if app.GetSettings().CodingAgentModels[4].FastTier() != "priority" {
		t.Fatal("settings projection aliases stored capabilities")
	}
}

func TestReadCodexModelsFailurePreservesConfiguration(t *testing.T) {
	root := t.TempDir()
	app := newApp(settings.PathsFor(filepath.Join(root, "config"), filepath.Join(root, "state")), nil)
	before := app.GetSettings()
	want := errors.New("catalog offline")
	app.modelCatalogReader = func(context.Context, string) ([]codexruntime.CodingAgentModelConfig, error) { return nil, want }
	if _, err := app.ReadCodexModels(); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(before, app.GetSettings()) {
		t.Fatal("failed model read changed settings")
	}
}
