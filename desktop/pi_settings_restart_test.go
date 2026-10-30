package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/desktop/internal/settings"
)

func selectedPiSettings(value settings.Settings) settings.Settings {
	value.PiExecutable = "pi"
	value.PiAgentModels = []settings.PiAgentModelConfig{{ModelID: "openai/test", Enabled: true}}
	value.PiModel = "openai/test"
	return value
}

func TestPiSettingsSaveWhileRunningAndApplyOnExplicitRestart(t *testing.T) {
	for _, stopStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "restart", true: "stop-start"}[stopStart], func(t *testing.T) {
			app, factory, _, _ := tunnelTestApp(t)
			if err := app.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Stop() }()
			original := app.GetSettings().Settings
			changed := selectedPiSettings(original)
			if err := app.SaveSettings(changed); err != nil {
				t.Fatal(err)
			}
			// A later default edit must compare with the actual launch, not the previous save.
			changed.PiThinkingLevel = "high"
			if err := app.SaveSettings(changed); err != nil {
				t.Fatal(err)
			}
			if !app.Status().PiRestartRequired {
				t.Fatal("saved Pi changes not projected as pending")
			}
			persisted, err := app.store.Load()
			if err != nil || persisted.PiModel != changed.PiModel {
				t.Fatalf("persisted Pi model: %q, %v", persisted.PiModel, err)
			}
			factory.mu.Lock()
			count := len(factory.commands)
			factory.mu.Unlock()
			if count != 3 {
				t.Fatalf("save restarted children: %d", count)
			}
			if err := app.Start(); err != nil {
				t.Fatal(err)
			}
			if !app.Status().PiRestartRequired {
				t.Fatal("Start while running consumed pending change")
			}
			if stopStart {
				if err := app.Stop(); err != nil {
					t.Fatal(err)
				}
				if err := app.Start(); err != nil {
					t.Fatal(err)
				}
			} else if err := app.Restart(); err != nil {
				t.Fatal(err)
			}
			if app.Status().PiRestartRequired {
				t.Fatal("Pi settings still pending after restart")
			}
			factory.mu.Lock()
			command := factory.commands[3]
			factory.mu.Unlock()
			for _, arg := range []string{"-pi-agent", "-pi-agent-model", "openai/test", "-pi-agent-thinking", "high"} {
				if !contains(command.Args, arg) {
					t.Fatalf("new Core command missing %q: %v", arg, command.Args)
				}
			}
			// Clearing the last selected model is also saved without stopping live children.
			changed.PiAgentModels, changed.PiModel = nil, ""
			if err := app.SaveSettings(changed); err != nil {
				t.Fatal(err)
			}
			if !app.Status().PiRestartRequired {
				t.Fatal("disabling Pi did not require restart")
			}
			if err := app.Restart(); err != nil {
				t.Fatal(err)
			}
			factory.mu.Lock()
			command = factory.commands[6]
			factory.mu.Unlock()
			if contains(command.Args, "-pi-agent") || app.Status().PiRestartRequired {
				t.Fatal("Pi stayed enabled after clearing and applying")
			}
		})
	}
}

func TestPiPendingChangesCanBeRevertedAndFailedSavePreservesState(t *testing.T) {
	app, factory, _, _ := tunnelTestApp(t)
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Stop() }()
	original := app.GetSettings().Settings
	if err := app.SaveSettings(selectedPiSettings(original)); err != nil {
		t.Fatal(err)
	}
	if err := app.SaveSettings(original); err != nil {
		t.Fatal(err)
	}
	if app.Status().PiRestartRequired {
		t.Fatal("reverted changes still pending")
	}
	app.store = &appSettingsStore{value: original, err: errors.New("disk full")}
	if err := app.SaveSettings(selectedPiSettings(original)); err == nil {
		t.Fatal("failed persistence accepted")
	}
	if !reflect.DeepEqual(app.GetSettings().Settings, original) || app.Status().PiRestartRequired {
		t.Fatal("failed save changed settings or pending projection")
	}
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if len(factory.commands) != 3 {
		t.Fatal("saving/reverting settings restarted live children")
	}
}

func TestPiChangesDoNotTakeOwnershipOfExternalCore(t *testing.T) {
	app := newApp(settings.PathsFor(t.TempDir(), t.TempDir()), nil)
	app.supervisor, _ = appTestSupervisor(t, true)
	if err := app.supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.supervisor.Stop(context.Background()) }()
	if err := app.SaveSettings(selectedPiSettings(app.GetSettings().Settings)); err == nil {
		t.Fatal("external Pi runtime change accepted")
	}
}
