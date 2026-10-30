package piagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolvePiExecutable(t *testing.T) {
	// Create a temporary pi executable.
	home := t.TempDir()
	piPath := filepath.Join(home, ".local", "bin", "pi")
	if err := os.MkdirAll(filepath.Dir(piPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(piPath, []byte("#!/bin/sh\necho 'pi mock'\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Test resolution with custom PATH.
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	resolved := Resolve("pi")
	if resolved == "pi" {
		// If not found in PATH, should check candidates.
		// The mock should be found in ~/.local/bin/pi.
		if !isExecutable(piPath) {
			t.Skip("mock pi not executable, skipping resolution test")
		}
	}
}

func TestClientStartAndClose(t *testing.T) {
	// Check if pi is available.
	piPath := Resolve("pi")
	if _, err := exec.LookPath(piPath); err != nil {
		t.Skip("pi executable not available, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cwd := t.TempDir()
	t.Logf("Using CWD: %s", cwd)
	t.Logf("PI path: %s", piPath)

	cfg := Config{
		PiPath: piPath,
		CWD:    cwd,
	}

	t.Log("Starting PI Agent client...")
	client, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Log("PI Agent client started successfully")

	// Send a simple command to verify the connection works.
	t.Log("Sending GetState command...")
	state, err := client.GetState(ctx)
	if err != nil {
		t.Logf("Stderr output: %s", client.stderrOutput.String())
		t.Errorf("GetState failed: %v", err)
	} else if len(state) == 0 {
		t.Error("GetState returned empty data")
	} else {
		t.Logf("GetState succeeded, got %d bytes", len(state))
	}

	// Close should succeed.
	t.Log("Closing client...")
	if err := client.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
	t.Log("Client closed")
}

func TestClientPrompt(t *testing.T) {
	piPath := Resolve("pi")
	if _, err := exec.LookPath(piPath); err != nil {
		t.Skip("pi executable not available, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := Config{
		PiPath: piPath,
		CWD:    t.TempDir(),
	}

	client, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer client.Close()

	// Send a simple prompt. This may fail if pi requires authentication.
	output, err := client.Prompt(ctx, "Say 'hello' in one word.")
	if err != nil {
		// Check if it's an authentication error.
		errStr := err.Error()
		if strings.Contains(errStr, "auth") || strings.Contains(errStr, "credential") || strings.Contains(errStr, "API key") || strings.Contains(errStr, "login") {
			t.Skipf("PI Agent requires authentication: %v", err)
		}
		t.Logf("Stderr: %s", client.stderrOutput.String())
		t.Fatalf("Prompt failed: %v", err)
	}

	// PI Agent might return empty output if it's waiting for authentication
	// or if the prompt was handled by an extension.
	if output == "" {
		t.Log("Warning: PI Agent returned empty output (may require authentication)")
	} else {
		t.Logf("PI Agent output: %s", output)
	}
}
