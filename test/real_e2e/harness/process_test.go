package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChildProcessCanBeKilledNonGracefullyAndCapturesLogs(t *testing.T) {
	if os.Getenv("DEVBRIDGE_E2E_HELPER") == "1" {
		println("helper stdout")
		select {}
	}
	artifacts := t.TempDir()
	child, err := StartChild(CommandSpec{Path: os.Args[0], Args: []string{"-test.run=TestChildProcessCanBeKilledNonGracefullyAndCapturesLogs"}, Env: []string{"DEVBRIDGE_E2E_HELPER=1"}}, artifacts, "child")
	if err != nil {
		t.Fatal(err)
	}
	if child.PID() == 0 {
		t.Fatal("child has no pid")
	}
	if err := child.KillNonGracefully(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("non-gracefully killed child unexpectedly exited cleanly")
	}
	for _, path := range []string{child.StdoutPath, child.StderrPath} {
		if filepath.Dir(path) != artifacts {
			t.Fatalf("log escaped artifact directory: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing log %s: %v", path, err)
		}
	}
}
