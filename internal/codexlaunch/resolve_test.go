package codexlaunch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCodexExecutableFindsNVMInstallWithoutShellPath(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, ".nvm", "versions", "node", "v23.6.0", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(launcher), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	if got := resolveCodexExecutableFrom("codex", func(string) (string, error) { return "", os.ErrNotExist }, []string{launcher}); got != launcher {
		t.Fatalf("resolved path = %q, want %q", got, launcher)
	}
}
