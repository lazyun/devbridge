package tunnel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBuildCommandUsesOnlyAuthoritativeFlags(t *testing.T) {
	dir := t.TempDir()
	command, err := BuildCommand(LaunchConfig{
		Executable:      "/opt/bin/tunnel-client",
		MCPEndpoint:     "http://127.0.0.1:8080",
		TunnelID:        "tunnel_0123456789abcdef0123456789abcdef",
		APIKeyReference: "env:CONTROL_PLANE_API_KEY",
		RuntimeFiles:    RuntimeFilesIn(dir),
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Path != "/opt/bin/tunnel-client" || command.Args[0] != "run" {
		t.Fatalf("command = %+v", command)
	}
	args := command.Args
	expect := map[string]string{
		"--mcp.server-url":          "http://127.0.0.1:8080/mcp",
		"--control-plane.tunnel-id": "tunnel_0123456789abcdef0123456789abcdef",
		"--control-plane.api-key":   "env:CONTROL_PLANE_API_KEY",
		"--health.listen-addr":      "127.0.0.1:0",
		"--health.url-file":         filepath.Join(dir, HealthURLFileName),
		"--pid.file":                filepath.Join(dir, PIDFileName),
	}
	for i := 0; i+1 < len(args); i++ {
		if want, ok := expect[args[i]]; ok {
			if args[i+1] != want {
				t.Fatalf("%s = %q, want %q", args[i], args[i+1], want)
			}
			delete(expect, args[i])
		}
	}
	for flag := range expect {
		t.Fatalf("missing flag %s in %v", flag, args)
	}
}

func TestBuildCommandRejectsIncompleteConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		config LaunchConfig
	}{
		{"no executable", LaunchConfig{TunnelID: "tunnel_0123456789abcdef0123456789abcdef", MCPEndpoint: "http://127.0.0.1:8080", RuntimeFiles: RuntimeFilesIn(t.TempDir())}},
		{"relative executable", LaunchConfig{Executable: "tunnel-client", TunnelID: "tunnel_0123456789abcdef0123456789abcdef", MCPEndpoint: "http://127.0.0.1:8080", RuntimeFiles: RuntimeFilesIn(t.TempDir())}},
		{"no tunnel id", LaunchConfig{Executable: "/opt/bin/tunnel-client", MCPEndpoint: "http://127.0.0.1:8080", RuntimeFiles: RuntimeFilesIn(t.TempDir())}},
		{"no mcp endpoint", LaunchConfig{Executable: "/opt/bin/tunnel-client", TunnelID: "tunnel_0123456789abcdef0123456789abcdef", RuntimeFiles: RuntimeFilesIn(t.TempDir())}},
		{"no runtime files", LaunchConfig{Executable: "/opt/bin/tunnel-client", TunnelID: "tunnel_0123456789abcdef0123456789abcdef", MCPEndpoint: "http://127.0.0.1:8080"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildCommand(tc.config); err == nil {
				t.Fatal("BuildCommand succeeded")
			}
		})
	}
}

func TestValidateExecutablePathRejectsDirectoriesAndNonExecutables(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateExecutablePath(dir); err == nil {
		t.Fatal("accepted a directory")
	}
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutablePath(plain); err == nil {
		t.Fatal("accepted a non-executable file")
	}
	missing := filepath.Join(dir, "missing")
	if err := ValidateExecutablePath(missing); err == nil {
		t.Fatal("accepted a missing file")
	}
}

func TestVersionProbeRunsRealExecutable(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tunnel-client")
	content := "#!/bin/sh\nif [ \"$1\" != \"--version\" ]; then exit 9; fi\necho \"0.0.14+abc (git sha: abc)\"\n"
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	version, err := Version(context.Background(), script)
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.0.14+abc" {
		t.Fatalf("version = %q", version)
	}
}

func TestVersionProbeRejectsBrokenExecutable(t *testing.T) {
	script := filepath.Join(t.TempDir(), "tunnel-client")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Version(context.Background(), script); err == nil {
		t.Fatal("version probe succeeded for a broken executable")
	}
}

func TestAttachCheckExternalConflictAndStale(t *testing.T) {
	original := pidAlive
	defer func() { pidAlive = original }()

	t.Run("live compatible daemon is external", func(t *testing.T) {
		pidAlive = func(int) bool { return true }
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(healthzBody))
		}))
		defer server.Close()
		dir := t.TempDir()
		files := RuntimeFilesIn(dir)
		if err := os.WriteFile(files.HealthURLFile, []byte(server.URL), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.PIDFile, []byte("123"), 0600); err != nil {
			t.Fatal(err)
		}
		result := AttachCheck(files)
		if !result.External || result.Conflict || result.PID != 123 {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("occupied surface is a conflict", func(t *testing.T) {
		pidAlive = func(int) bool { return false }
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("nginx"))
		}))
		defer server.Close()
		dir := t.TempDir()
		files := RuntimeFilesIn(dir)
		if err := os.WriteFile(files.HealthURLFile, []byte(server.URL), 0600); err != nil {
			t.Fatal(err)
		}
		result := AttachCheck(files)
		if result.External || !result.Conflict {
			t.Fatalf("result = %+v", result)
		}
	})
	t.Run("stale files from an exited daemon are removed", func(t *testing.T) {
		pidAlive = func(int) bool { return false }
		dir := t.TempDir()
		files := RuntimeFilesIn(dir)
		if err := os.WriteFile(files.HealthURLFile, []byte("http://127.0.0.1:1"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.PIDFile, []byte("123"), 0600); err != nil {
			t.Fatal(err)
		}
		result := AttachCheck(files)
		if result.External || result.Conflict {
			t.Fatalf("result = %+v", result)
		}
		if _, err := os.Stat(files.HealthURLFile); !os.IsNotExist(err) {
			t.Fatalf("stale health url file survived: %v", err)
		}
		if _, err := os.Stat(files.PIDFile); !os.IsNotExist(err) {
			t.Fatalf("stale pid file survived: %v", err)
		}
	})
	t.Run("non-loopback health url is ignored", func(t *testing.T) {
		pidAlive = func(int) bool { return false }
		dir := t.TempDir()
		files := RuntimeFilesIn(dir)
		if err := os.WriteFile(files.HealthURLFile, []byte("http://example.com"), 0600); err != nil {
			t.Fatal(err)
		}
		if result := AttachCheck(files); result.External || result.Conflict {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestReadinessReportsTheDaemonOwnVerdict(t *testing.T) {
	var body atomic.Value
	body.Store(readyzBody)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer server.Close()
	dir := t.TempDir()
	files := RuntimeFilesIn(dir)
	if err := os.WriteFile(files.HealthURLFile, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	readiness := Readiness(files)
	ready, detail, err := readiness(context.Background())
	if err != nil || !ready || detail != readyzBody {
		t.Fatalf("ready=%v detail=%q err=%v", ready, detail, err)
	}
	body.Store("oauth discovery failed: connection refused")
	ready, detail, err = readiness(context.Background())
	if err != nil || ready || !strings.Contains(detail, "oauth discovery failed") {
		t.Fatalf("ready=%v detail=%q err=%v", ready, detail, err)
	}
	if err := os.Remove(files.HealthURLFile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readiness(context.Background()); err == nil {
		t.Fatal("missing health url file must report an error, not readiness")
	}
}
