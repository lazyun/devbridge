package simulation

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSimulationChildHelper(t *testing.T) {
	mode := os.Getenv("DEVBRIDGE_SIM_CHILD_HELPER")
	if mode == "" {
		return
	}
	fmt.Println("ready")
	if mode == "exit" {
		os.Exit(0)
	}
	time.Sleep(time.Hour)
	os.Exit(2)
}

func simulationTestChild(t *testing.T, mode string) *child {
	t.Helper()
	p := &child{cmd: exec.Command(os.Args[0], "-test.run=^TestSimulationChildHelper$"), done: make(chan struct{})}
	p.cmd.Env = append(os.Environ(), "DEVBRIDGE_SIM_CHILD_HELPER="+mode)
	var err error
	p.log, err = os.Create(filepath.Join(t.TempDir(), "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		p.log.Close()
		t.Fatal(err)
	}
	p.cmd.Stderr = p.log
	if err := p.cmd.Start(); err != nil {
		p.log.Close()
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() { <-readDone; p.waitErr = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { _ = p.stop(false) })
	ready := make(chan error, 1)
	go func() {
		defer close(readDone)
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("unexpected child readiness %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child readiness timeout")
	}
	return p
}

func TestChildCrashRequiresActualSIGKILL(t *testing.T) {
	p := simulationTestChild(t, "live")
	if err := p.stop(true); err != nil {
		t.Fatal(err)
	}
	if err := verifySIGKILL(p.cmd.ProcessState, p.waitErr); err != nil {
		t.Fatal(err)
	}
	if err := p.stop(true); err == nil || !strings.Contains(err.Error(), "already stopped") {
		t.Fatalf("second crash must not reuse earlier injection: %v", err)
	}
	if err := p.stop(false); err != nil {
		t.Fatalf("normal cleanup must remain idempotent: %v", err)
	}
}

func TestChildCrashRejectsAlreadyExited(t *testing.T) {
	p := simulationTestChild(t, "exit")
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("child exit timeout")
	}
	if err := p.stop(true); err == nil || !strings.Contains(err.Error(), "exited before") {
		t.Fatalf("preexisting exit cannot prove SIGKILL injection: %v", err)
	}
	if err := verifySIGKILL(p.cmd.ProcessState, p.waitErr); err == nil {
		t.Fatal("successful exit cannot prove SIGKILL")
	}
}

func TestChildCrashRejectsOtherSignal(t *testing.T) {
	p := simulationTestChild(t, "live")
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("child signal exit timeout")
	}
	if err := verifySIGKILL(p.cmd.ProcessState, p.waitErr); err == nil {
		t.Fatal("SIGTERM cannot prove SIGKILL")
	}
	if err := p.stop(true); err == nil {
		t.Fatal("already signaled process cannot count as new crash injection")
	}
}

func TestChildNormalCleanupConcurrentAndIdempotent(t *testing.T) {
	p := simulationTestChild(t, "live")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = p.stop(false) }()
	}
	wg.Wait()
	if err := p.stop(true); err == nil {
		t.Fatal("normal cleanup cannot be relabeled crash injection")
	}
	if err := verifySIGKILL(nil, nil); err == nil {
		t.Fatal("missing exit evidence cannot prove SIGKILL")
	}
}
