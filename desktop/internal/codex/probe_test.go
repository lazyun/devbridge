package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeAvailable(t *testing.T) {
	t.Setenv("GO_WANT_CODEX_HELPER", "available")
	result := Probe(context.Background(), helperExecutable(t))
	if result.Status != Available || result.Version != "codex-cli 0.149.0" || result.Error != "" {
		t.Fatalf("Probe() = %#v", result)
	}
}

func TestProbeMissing(t *testing.T) {
	result := Probe(context.Background(), "definitely-not-a-codex-binary-for-devbridge-test")
	if result.Status != Missing || result.Version != "" || result.Error == "" {
		t.Fatalf("Probe() = %#v", result)
	}
}

func TestProbeAbsoluteNVMStyleLauncherFindsSiblingNode(t *testing.T) {
	dir := t.TempDir()
	node := filepath.Join(dir, "node")
	script := "#!/bin/sh\nif [ \"$2\" = --version ]; then echo 'codex-cli sibling-node'; exit; fi\nexec '" + os.Args[0] + "' app-server\n"
	if err := os.WriteFile(node, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	launcher := filepath.Join(dir, "codex")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("GO_WANT_CODEX_HELPER", "available")
	result := Probe(context.Background(), launcher)
	if result.Status != Available || result.Version != "codex-cli sibling-node" {
		t.Fatalf("Probe() = %#v", result)
	}
}

func TestEnvironmentWithExecutableDirPreservesEnvironment(t *testing.T) {
	env := codexlaunch.Environment([]string{"LANG=en_US.UTF-8", "PATH=/usr/bin"}, "/opt/nvm/bin/codex")
	if got := strings.Join(env, "|"); got != "LANG=en_US.UTF-8|PATH=/opt/nvm/bin:/usr/bin" {
		t.Fatalf("environment = %q", got)
	}
}

func TestProbeFailureAndBoundedOutput(t *testing.T) {
	for _, mode := range []string{"fail", "large"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GO_WANT_CODEX_HELPER", mode)
			result := Probe(context.Background(), helperExecutable(t))
			if result.Status != Error || result.Version != "" || result.Error == "" {
				t.Fatalf("Probe() = %#v", result)
			}
			if strings.Contains(result.Error, strings.Repeat("x", 32)) {
				t.Fatalf("unbounded output leaked into error: %q", result.Error)
			}
		})
	}
}

func helperExecutable(t *testing.T) string {
	t.Helper()
	return os.Args[0]
}

func TestMain(m *testing.M) {
	// A probe runs this test binary with --version. Handle that child mode before
	// running tests so it cannot recursively invoke the suite.
	switch os.Getenv("GO_WANT_CODEX_HELPER") {
	case "available":
		if len(os.Args) > 1 && os.Args[1] == "app-server" {
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				_ = json.Unmarshal(scanner.Bytes(), &req)
				if req.Method == "initialize" {
					fmt.Printf("{\"id\":%s,\"result\":{\"userAgent\":\"test\"}}\n", req.ID)
				}
			}
			os.Exit(0)
		}
		_, _ = os.Stdout.WriteString("codex-cli 0.149.0\n")
		os.Exit(0)
	case "fail":
		_, _ = os.Stderr.WriteString("no version available")
		os.Exit(2)
	case "large":
		for remaining := maxVersionOutput + 1; remaining > 0; {
			chunk := 1024
			if remaining < chunk {
				chunk = remaining
			}
			written, _ := os.Stdout.WriteString(strings.Repeat("x", chunk))
			remaining -= written
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
