package codex

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Diagnostic only: demonstrate precisely what the production Close certifies.
// The descendant is a bounded fixture, never a real Codex process.
func TestAuditOwnedCloseDoesNotJoinDescendantWriter(t *testing.T) {
	dir := t.TempDir()
	p, err := startOwnedProcess(os.Args[0], []string{"-test.run=^TestAuditTerminationHelper$"}, []string{"DEVBRIDGE_TERMINATION_AUDIT=parent", "DEVBRIDGE_TERMINATION_DIR=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	line, err := bufio.NewReader(p.stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	child, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Kill(); _ = child.Release() })
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	default:
		t.Fatal("direct process not joined")
	}
	if err := os.WriteFile(filepath.Join(dir, "trigger"), []byte("after Close returned"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(dir, "proof")); err == nil {
			t.Logf("PROVEN: production Close returned success and joined app-server; descendant subsequently wrote %q", data)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture descendant did not demonstrate the expected write")
}

func TestAuditTerminationHelper(t *testing.T) {
	mode := os.Getenv("DEVBRIDGE_TERMINATION_AUDIT")
	if mode == "" {
		return
	}
	dir := os.Getenv("DEVBRIDGE_TERMINATION_DIR")
	if mode == "parent" {
		child := exec.Command(os.Args[0], "-test.run=^TestAuditTerminationHelper$")
		child.Env = append(os.Environ(), "DEVBRIDGE_TERMINATION_AUDIT=child")
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, child.Process.Pid)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "trigger")); err == nil {
			if err := os.WriteFile(filepath.Join(dir, "proof"), []byte("descendant write after direct process joined"), 0600); err != nil {
				os.Exit(3)
			}
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(4)
}
