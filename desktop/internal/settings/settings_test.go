package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultsAreValid(t *testing.T) {
	value := Defaults()
	if err := value.Validate(); err != nil {
		t.Fatalf("Defaults().Validate(): %v", err)
	}
	if len(value.CodingAgentModels) == 0 {
		t.Fatal("default coding agent models are missing")
	}
}

func TestValidateRejectsCredentialsAndInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Settings)
	}{
		{"blank binary", func(s *Settings) { s.CoreBinary = " " }},
		{"blank data root", func(s *Settings) { s.DataRoot = " " }},
		{"NUL data root", func(s *Settings) { s.DataRoot = "data\x00evil" }},
		{"relative data root", func(s *Settings) { s.DataRoot = "relative/data" }},
		{"NUL executable", func(s *Settings) { s.CodexExecutable = "codex\x00evil" }},
		{"credentials in endpoint", func(s *Settings) { s.CoreEndpoint = "http://secret@127.0.0.1:8081" }},
		{"non-http endpoint", func(s *Settings) { s.MCPEndpoint = "https://127.0.0.1:8080" }},
		{"endpoint path", func(s *Settings) { s.MCPEndpoint = "http://127.0.0.1:8080/v1" }},
		{"missing port", func(s *Settings) { s.MCPEndpoint = "http://127.0.0.1" }},
		{"non-loopback endpoint", func(s *Settings) { s.CoreEndpoint = "http://192.0.2.1:8081" }},
		{"no enabled models", func(s *Settings) {
			for i := range s.CodingAgentModels {
				s.CodingAgentModels[i].Enabled = false
			}
		}},
		{"unsupported effort", func(s *Settings) { s.CodingAgentModels[0].SupportedReasoningEfforts = []string{"ultra"} }},
		{"default not supported", func(s *Settings) { s.CodingAgentModels[0].DefaultReasoningEffort = "ultra" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := Defaults()
			tc.set(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}

func TestModelConfigPersistsIndependentDefaults(t *testing.T) {
	root := t.TempDir()
	store := NewStore(PathsFor(filepath.Join(root, "config"), filepath.Join(root, "state")))
	value := Defaults()
	value.DataRoot = filepath.Join(root, "bridge-data")
	value.CodingAgentModels[0].Enabled = false
	value.CodingAgentModels[1].DefaultReasoningEffort = "high"
	if err := store.Save(value); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.CodingAgentModels[0].Enabled || got.CodingAgentModels[1].DefaultReasoningEffort != "high" {
		t.Fatalf("model configs = %#v", got.CodingAgentModels)
	}
}

func TestStoreMigratesPersistedModelInventories(t *testing.T) {
	for _, test := range []struct {
		name        string
		modelsJSON  string
		wantEnabled []string
	}{
		{name: "missing", wantEnabled: []string{"gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"}},
		{name: "old known subset", modelsJSON: `["gpt-5.6-luna","gpt-5.6-sol"]`, wantEnabled: []string{"gpt-5.6-luna", "gpt-5.6-sol"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store := NewStore(PathsFor(root, root))
			value := DefaultsFor(store.Paths().DefaultDataRoot)
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "codingAgentModels")
			if test.modelsJSON == "" {
				delete(fields, "allowedModels")
			} else {
				fields["allowedModels"] = json.RawMessage(test.modelsJSON)
			}
			writePersistedSettings(t, store, fields)
			got, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			var enabled []string
			for _, config := range got.CodingAgentModels {
				if config.Enabled {
					enabled = append(enabled, config.ModelID)
				}
			}
			if !reflect.DeepEqual(enabled, test.wantEnabled) {
				t.Fatalf("enabled=%v want=%v", enabled, test.wantEnabled)
			}
			persistedBytes, err := os.ReadFile(store.Paths().ConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			var persisted Settings
			if err := json.Unmarshal(persistedBytes, &persisted); err != nil {
				t.Fatal(err)
			}
			if len(persisted.CodingAgentModels) == 0 {
				t.Fatal("persisted model configs missing")
			}
			reloaded, err := store.Load()
			if err != nil || !reflect.DeepEqual(reloaded.CodingAgentModels, got.CodingAgentModels) {
				t.Fatalf("persisted migration=%v err=%v", reloaded.CodingAgentModels, err)
			}
		})
	}
}

func TestStoreRejectsExplicitNullModelConfigs(t *testing.T) {
	for _, modelsJSON := range []string{"null"} {
		t.Run(modelsJSON, func(t *testing.T) {
			root := t.TempDir()
			store := NewStore(PathsFor(root, root))
			value := DefaultsFor(store.Paths().DefaultDataRoot)
			encoded, _ := json.Marshal(value)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(encoded, &fields)
			fields["codingAgentModels"] = json.RawMessage(modelsJSON)
			writePersistedSettings(t, store, fields)
			if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "invalid persisted settings") {
				t.Fatalf("Load() error=%v", err)
			}
		})
	}
}

func TestV2SettingsDropContradictoryLegacyRuntimeFields(t *testing.T) {
	root := t.TempDir()
	store := NewStore(PathsFor(root, root))
	value := DefaultsFor(store.Paths().DefaultDataRoot)
	value.AllowedModels = []string{"gpt-6-astra"}
	value.AllowedReasoningEfforts = []string{"low"}
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["allowedModels"]; ok {
		t.Fatal("Save persisted legacy allowedModels")
	}
	if _, ok := fields["allowedReasoningEfforts"]; ok {
		t.Fatal("Save persisted legacy allowedReasoningEfforts")
	}

	fields["allowedModels"] = json.RawMessage(`["gpt-6-astra"]`)
	fields["allowedReasoningEfforts"] = json.RawMessage(`["low"]`)
	writePersistedSettings(t, store, fields)
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.CodingAgentModels, value.CodingAgentModels) {
		t.Fatalf("v2 configuration was changed by legacy fields: %+v", loaded.CodingAgentModels)
	}
	data, err = os.ReadFile(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "allowedModels") || strings.Contains(string(data), "allowedReasoningEfforts") {
		t.Fatalf("Load did not rewrite legacy fields: %s", data)
	}
}

func writePersistedSettings(t *testing.T, store Store, fields map[string]json.RawMessage) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(store.Paths().ConfigFile), 0700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Paths().ConfigFile, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreLoadDefaultsAndRoundTrip(t *testing.T) {
	root := t.TempDir()
	store := NewStore(PathsFor(filepath.Join(root, "config"), filepath.Join(root, "state")))
	got, err := store.Load()
	if err != nil || !reflect.DeepEqual(got, DefaultsFor(store.Paths().DefaultDataRoot)) {
		t.Fatalf("initial Load() = %#v, %v", got, err)
	}
	want := Defaults()
	want.DataRoot = filepath.Join(root, "bridge-data")
	want.CodexExecutable = "/opt/bin/codex"
	want.CoreEndpoint = "http://localhost:9081"
	if err := store.Save(want); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	got, err = store.Load()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, %v; want %#v", got, err, want)
	}
	info, err := os.Stat(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
}

func TestStoreRejectsInvalidPersistedJSON(t *testing.T) {
	root := t.TempDir()
	store := NewStore(PathsFor(root, root))
	if err := os.MkdirAll(filepath.Dir(store.Paths().ConfigFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Paths().ConfigFile, []byte(`{"coreBinary":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil || !strings.Contains(err.Error(), "invalid persisted settings") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestPathsForDoesNotUseRepository(t *testing.T) {
	paths := PathsFor("/config-base", "/state-base")
	if paths.ConfigFile != "/config-base/devbridge-desktop/settings.json" || paths.LogDir != "/state-base/devbridge-desktop/logs" || paths.DefaultDataRoot != "/state-base/DevBridge/data" {
		t.Fatalf("PathsFor() = %#v", paths)
	}
}

func TestTunnelConfigurationValidation(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Settings)
	}{
		{"relative tunnel executable", func(s *Settings) { s.OpenAITunnelExecutable = "tunnel-client" }},
		{"NUL tunnel executable", func(s *Settings) { s.OpenAITunnelExecutable = "/opt/tun\x00nel" }},
		{"invalid tunnel id", func(s *Settings) { s.OpenAITunnelID = "tunnel_test" }},
		{"uppercase tunnel id", func(s *Settings) { s.OpenAITunnelID = "tunnel_0123456789ABCDEF0123456789ABCDEF" }},
		{"short tunnel id", func(s *Settings) { s.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcde" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := Defaults()
			tc.set(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
	valid := []struct {
		name string
		set  func(*Settings)
	}{
		{"unconfigured", func(*Settings) {}},
		{"configured", func(s *Settings) {
			s.OpenAITunnelExecutable = "/opt/bin/tunnel-client"
			s.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
		}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			value := Defaults()
			tc.set(&value)
			if err := value.Validate(); err != nil {
				t.Fatalf("Validate(): %v", err)
			}
		})
	}
}

func TestTunnelConfigurationPersistsInMachineLocalSettings(t *testing.T) {
	root := t.TempDir()
	store := NewStore(PathsFor(filepath.Join(root, "config"), filepath.Join(root, "state")))
	value := Defaults()
	value.DataRoot = filepath.Join(root, "bridge-data")
	value.OpenAITunnelExecutable = "/opt/bin/tunnel-client"
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := store.Save(value); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenAITunnelExecutable != value.OpenAITunnelExecutable || got.OpenAITunnelID != value.OpenAITunnelID {
		t.Fatalf("tunnel configuration not persisted: %#v", got)
	}
	// The tunnel runtime directory is machine-local Desktop state, separate
	// from the DataRoot: switching DataRoot cannot clear tunnel settings.
	if store.Paths().TunnelDir == "" || strings.Contains(store.Paths().TunnelDir, value.DataRoot) {
		t.Fatalf("tunnel dir %q must be independent of DataRoot %q", store.Paths().TunnelDir, value.DataRoot)
	}
}

func TestPiDefaultMustBelongToEnabledSelection(t *testing.T) {
	for _, tc := range []struct {
		name, model        string
		enabled, wantError bool
	}{
		{"selected", "provider/selected", true, false},
		{"disabled", "provider/selected", false, true},
		{"outside selection", "provider/other", true, true},
		{"not configured", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := Defaults()
			value.PiAgentModels = []PiAgentModelConfig{{ModelID: "provider/selected", Enabled: tc.enabled}}
			value.PiModel = tc.model
			err := value.Validate()
			if (err != nil) != tc.wantError {
				t.Fatalf("Validate=%v", err)
			}
		})
	}
}
