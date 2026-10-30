package main

import (
	"path/filepath"
	"testing"
)

func TestSafeWorkingDirectoryUsesRequestedDirectory(t *testing.T) {
	root := t.TempDir()
	got, cleanup, err := safeWorkingDirectory(root, "unused-")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	want, _ := filepath.Abs(root)
	if got != want {
		t.Fatalf("working directory = %q, want %q", got, want)
	}
}
