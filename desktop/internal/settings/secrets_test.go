package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretStoreSaveLoadDeleteRoundTrip(t *testing.T) {
	store := NewSecretStore(t.TempDir())
	if store.Exists(tunnelSecretTestName) {
		t.Fatal("secret exists before save")
	}
	if err := store.Save(tunnelSecretTestName, "sk-test-secret"); err != nil {
		t.Fatalf("Save(): %v", err)
	}
	if !store.Exists(tunnelSecretTestName) {
		t.Fatal("secret missing after save")
	}
	got, err := store.Load(tunnelSecretTestName)
	if err != nil || got != "sk-test-secret" {
		t.Fatalf("Load() = %q, %v", got, err)
	}
	if err := store.Save(tunnelSecretTestName, "sk-replaced"); err != nil {
		t.Fatalf("replace Save(): %v", err)
	}
	if got, _ := store.Load(tunnelSecretTestName); got != "sk-replaced" {
		t.Fatalf("replaced value = %q", got)
	}
	if err := store.Delete(tunnelSecretTestName); err != nil {
		t.Fatalf("Delete(): %v", err)
	}
	if store.Exists(tunnelSecretTestName) {
		t.Fatal("secret survived Delete")
	}
	if err := store.Delete(tunnelSecretTestName); err != nil {
		t.Fatalf("second Delete(): %v", err)
	}
}

func TestSecretStoreFilePermissionsAreOwnerOnly(t *testing.T) {
	stateDir := t.TempDir()
	store := NewSecretStore(stateDir)
	if err := store.Save(tunnelSecretTestName, "sk-secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(stateDir, "secrets", tunnelSecretTestName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("secret file permissions = %o, want 600", perm)
	}
	dirInfo, err := os.Stat(filepath.Join(stateDir, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Fatalf("secrets directory permissions = %o, want 700", perm)
	}
}

func TestSecretStoreRejectsInvalidInput(t *testing.T) {
	store := NewSecretStore(t.TempDir())
	if err := store.Save(tunnelSecretTestName, "  "); err == nil {
		t.Fatal("empty secret accepted")
	}
	if err := store.Save(tunnelSecretTestName, "bad\x00secret"); err == nil {
		t.Fatal("NUL secret accepted")
	}
	if err := store.Save("../escape", "sk-secret"); err == nil {
		t.Fatal("path traversal secret name accepted")
	}
	if _, err := store.Load(tunnelSecretTestName); err != ErrSecretNotFound {
		t.Fatalf("missing secret error = %v", err)
	}
}

func TestSecretStoreNeverWritesIntoSettingsJSON(t *testing.T) {
	root := t.TempDir()
	configDir, stateDir := filepath.Join(root, "config"), filepath.Join(root, "state")
	store := NewStore(PathsFor(configDir, stateDir))
	value := Defaults()
	value.OpenAITunnelExecutable = "/opt/bin/tunnel-client"
	value.OpenAITunnelID = "tunnel_0123456789abcdef0123456789abcdef"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	secrets := NewSecretStore(stateDir)
	if err := secrets.Save(tunnelSecretTestName, "sk-plaintext-secret"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "devbridge-desktop", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-plaintext-secret") || strings.Contains(string(data), "apiKey") {
		t.Fatalf("settings.json contains secret material: %s", string(data))
	}
}

const tunnelSecretTestName = "test-secret"
