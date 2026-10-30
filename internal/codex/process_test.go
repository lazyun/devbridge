package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"testing"
	"time"
)

func TestOwnedProcessStderrIsolationAndClose(t *testing.T) {
	process, err := startOwnedProcess(
		os.Args[0],
		[]string{"-test.run=TestAppServerHelperProcess", "--", "stdio"},
		[]string{"DEVBRIDGE_HELPER_PROCESS=1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	transport := newRPCTransport(process.stdout, process.stdin, nil)
	var response struct {
		OK bool `json:"ok"`
	}
	if err := transport.Call(context.Background(), "test", struct{}{}, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatal("unexpected helper response")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	case <-time.After(time.Second):
		t.Fatal("owned process still running after Close")
	}
	if stderr := process.stderrText(); !strings.Contains(stderr, "stderr-only-not-json") {
		t.Fatalf("stderr capture = %q", stderr)
	}
	select {
	case diagnostic := <-transport.Diagnostics():
		if diagnostic != nil {
			t.Fatalf("stderr contaminated JSON-RPC diagnostics: %v", diagnostic)
		}
	default:
	}
}

func TestOwnedProcessRepeatedLifecycleJoinsStderrDrain(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		process, err := startOwnedProcess(
			os.Args[0],
			[]string{"-test.run=TestAppServerHelperProcess", "--", "stdio"},
			[]string{"DEVBRIDGE_HELPER_PROCESS=1"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Close(); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		select {
		case <-process.done:
		default:
			t.Fatalf("iteration %d: child process was not reaped", iteration)
		}
		select {
		case <-process.stderrDone:
		default:
			t.Fatalf("iteration %d: stderr drain was not joined", iteration)
		}
	}
}

func TestOwnedProcessStderrCaptureIsBounded(t *testing.T) {
	process, err := startOwnedProcess(
		os.Args[0],
		[]string{"-test.run=TestAppServerHelperProcess", "--", "stderr-flood"},
		[]string{"DEVBRIDGE_HELPER_PROCESS=1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(process.stderrText()); got != stderrCaptureLimit {
		t.Fatalf("stderr capture size = %d, want %d", got, stderrCaptureLimit)
	}
}

func TestOwnedProcessForcedTerminationIsReaped(t *testing.T) {
	process, err := startOwnedProcess(
		os.Args[0],
		[]string{"-test.run=TestForcedTerminationHelperProcess"},
		[]string{"DEVBRIDGE_FORCE_TERMINATION_HELPER=1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.done:
	default:
		t.Fatal("forcibly terminated child process was not reaped")
	}
}

func TestForcedTerminationHelperProcess(t *testing.T) {
	if os.Getenv("DEVBRIDGE_FORCE_TERMINATION_HELPER") != "1" {
		return
	}
	signal.Ignore(os.Interrupt)
	for {
		time.Sleep(time.Hour)
	}
}

func TestAppServerHelperProcess(t *testing.T) {
	if os.Getenv("DEVBRIDGE_HELPER_PROCESS") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		os.Exit(2)
	}
	mode := os.Args[separator+1]
	if mode == "stderr-flood" {
		fmt.Fprint(os.Stderr, strings.Repeat("x", stderrCaptureLimit*2))
	} else if mode != "stdio" {
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "stderr-only-not-json")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]json.RawMessage
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		response, _ := json.Marshal(map[string]any{
			"id":     request["id"],
			"result": map[string]bool{"ok": true},
		})
		fmt.Fprintln(os.Stdout, string(response))
	}
	os.Exit(0)
}
