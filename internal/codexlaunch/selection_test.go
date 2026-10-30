package codexlaunch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeSelectionPrecedenceAndFailure(t *testing.T) {
	dir := t.TempDir()
	selected, override := filepath.Join(dir, "selected"), filepath.Join(dir, "override")
	for _, file := range []string{selected, override} {
		if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(dir, "selection.json")
	t.Setenv(SelectionEnv, file)
	t.Setenv("PATH", "/usr/bin:/bin")
	persist := func(detail string) {
		t.Helper()
		if err := Write(file, Selection{Configured: "codex", Executable: selected, Error: detail}); err != nil {
			t.Fatal(err)
		}
	}
	persist("")
	for _, configured := range []string{"", "codex", override} {
		result, err := Runtime(configured)
		want := selected
		if configured == override {
			want = override
		}
		if err != nil || result.Executable != want {
			t.Fatalf("Runtime(%q) = %+v, %v", configured, result, err)
		}
	}
	persist("version succeeded but initialize failed")
	if _, err := Runtime(""); err == nil || !strings.Contains(err.Error(), "initialize failed") {
		t.Fatalf("probe failure: %v", err)
	}
	if _, err := Runtime(override); err != nil {
		t.Fatalf("explicit override lost: %v", err)
	}
	persist("")
	if err := os.Chmod(selected, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Runtime(""); err == nil || !strings.Contains(err.Error(), "NOT_EXECUTABLE") {
		t.Fatalf("non-executable: %v", err)
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if _, err := Runtime(""); err == nil || !strings.Contains(err.Error(), selected) {
		t.Fatalf("missing selection: %v", err)
	}
	if _, err := Runtime(selected); err == nil {
		t.Fatal("explicit missing path fell back")
	}
	if err := os.WriteFile(file, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Runtime(""); err == nil || !strings.Contains(err.Error(), "SELECTION_UNAVAILABLE") {
		t.Fatalf("corruption: %v", err)
	}
}
