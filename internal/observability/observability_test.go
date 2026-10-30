package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSchemaContextAndSanitization(t *testing.T) {
	var output bytes.Buffer
	l := New(Options{Process: "devbridge-mcp", Build: "0.1.0-dev+gabcdef0", Writer: &output})
	ctx := WithFields(context.Background(), Fields{RequestID: "r1", FeatureAreaID: "feature_area-1", RunID: "run-1"})
	l.Info(ctx, "review", "feature_area.review.start.accepted", "accepted\nrecord", map[string]any{"authorization": "Bearer no", "endpoint": "https://u:p@example.test/a?token=no", "nested": []any{"x"}})
	var got map[string]any
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schemaVersion", "timestamp", "level", "process", "component", "event", "build", "requestId", "featureAreaId", "runId", "data"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing %s: %#v", key, got)
		}
	}
	if got["level"] != "INFO" || got["message"] != "accepted record" {
		t.Fatalf("unexpected common values: %#v", got)
	}
	data := got["data"].(map[string]any)
	if data["authorization"] != "[redacted]" || data["endpoint"] != "https://example.test/a" {
		t.Fatalf("data = %#v", data)
	}
}

func TestBoundsAndFingerprint(t *testing.T) {
	var output bytes.Buffer
	l := New(Options{Writer: &output, MaxRecord: 500})
	l.Info(context.Background(), strings.Repeat("component", 30), "event", strings.Repeat("message", 1000), map[string]any{"value": strings.Repeat("value", 10000)})
	if output.Len() > 500 {
		t.Fatalf("record %d exceeds bound", output.Len())
	}
	var got map[string]any
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	f := Fingerprint("private prompt")
	if f["bytes"] != 14 || len(f["sha256"].(string)) != 64 {
		t.Fatalf("fingerprint = %#v", f)
	}
}

func TestRotationRetentionAndConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	l := New(Options{Process: "core", Directory: dir, Writer: io.Discard, MaxBytes: 180, Generations: 2})
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Info(context.Background(), "test", "event", "record", map[string]any{"x": strings.Repeat("x", 80)})
		}()
	}
	wg.Wait()
	l.Close()
	for _, name := range []string{"core.jsonl", "core.jsonl.1", "core.jsonl.2"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "core.jsonl.3")); !os.IsNotExist(err) {
		t.Fatalf("unexpected old generation: %v", err)
	}
}

func TestSinkFailureDoesNotSurface(t *testing.T) {
	var output bytes.Buffer
	l := New(Options{Directory: filepath.Join(t.TempDir(), "blocked"), Filename: "x.jsonl", Writer: &output})
	// Make the directory unavailable after setup; logging remains best effort.
	l.sink.mu.Lock()
	if l.sink.file != nil {
		_ = l.sink.file.Close()
		l.sink.file = nil
	}
	l.sink.path = "/dev/null/log.jsonl"
	l.sink.mu.Unlock()
	l.Error(context.Background(), "storage", "run.failed", "write failure must not escape", nil)
	if output.Len() == 0 {
		t.Fatal("stderr fallback did not receive record")
	}
	if !strings.Contains(output.String(), `"event":"log.sink.failed"`) || !strings.Contains(output.String(), `"errorCode":"LOG_FILE_OPEN_FAILED"`) {
		t.Fatalf("file creation failure was not diagnosable: %s", output.String())
	}
}

func TestRotationFailureFallsBackToStderr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rotate.jsonl")
	if err := os.Mkdir(path+".1", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path+".1", "block"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	l := New(Options{Directory: dir, Filename: filepath.Base(path), Writer: &output, MaxBytes: 1, Generations: 1})
	l.Info(context.Background(), "test", "rotation.probe", "force rotation", nil)
	l.Close()
	if !strings.Contains(output.String(), `"errorCode":"LOG_FILE_ROTATE_FAILED"`) || !strings.Contains(output.String(), `"event":"rotation.probe"`) {
		t.Fatalf("rotation failure did not preserve diagnostic event on stderr: %s", output.String())
	}
}

func TestWriteFailureFallsBackToStderr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "write.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	l := New(Options{Writer: &output})
	l.sink.path, l.sink.file = path, readOnly
	l.Info(context.Background(), "test", "write.probe", "force write failure", nil)
	l.Close()
	if !strings.Contains(output.String(), `"errorCode":"LOG_FILE_WRITE_FAILED"`) || !strings.Contains(output.String(), `"event":"write.probe"`) {
		t.Fatalf("write failure did not preserve diagnostic event on stderr: %s", output.String())
	}
}

func TestSlogAdapter(t *testing.T) {
	var output bytes.Buffer
	l := New(Options{Writer: &output})
	l.Slog().LogAttrs(context.Background(), slog.LevelWarn, "warning", slog.String("component", "mcp"), slog.String("event", "mcp.tool.failed"), slog.String("token", "secret"))
	if !strings.Contains(output.String(), `"token":"[redacted]"`) {
		t.Fatalf("slog event was not sanitized: %s", output.String())
	}
}

func TestSanitizesStructuredValues(t *testing.T) {
	type payload struct {
		Password string `json:"password"`
		Useful   string `json:"useful"`
	}
	var output bytes.Buffer
	New(Options{Writer: &output}).Info(context.Background(), "mcp", "mcp.tool.requested", "safe", map[string]any{"payload": payload{Password: "do-not-log", Useful: "handle"}})
	if strings.Contains(output.String(), "do-not-log") || !strings.Contains(output.String(), `"useful":"handle"`) {
		t.Fatalf("structured value escaped sanitizer: %s", output.String())
	}
}

func TestSanitizesCredentialShapedCommandWithoutLosingDiagnosticShape(t *testing.T) {
	var output bytes.Buffer
	New(Options{Writer: &output}).Info(context.Background(), "review", "codex.command.observed", "safe", map[string]any{"command": "API_TOKEN=top-secret PASSWORD='also-secret' curl -H 'Authorization: Bearer hidden' https://url-user:url-pass@example.test/path?token=query-secret"})
	text := output.String()
	for _, secret := range []string{"top-secret", "also-secret", "hidden", "url-user", "url-pass", "query-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("credential %q leaked: %s", secret, text)
		}
	}
	if !strings.Contains(text, "curl") || !strings.Contains(text, "[redacted]") {
		t.Fatalf("diagnostic command shape was not preserved: %s", text)
	}
}

func TestHostileCorrelationValueIsFingerprintRedacted(t *testing.T) {
	var output bytes.Buffer
	ctx := WithFields(context.Background(), Fields{RequestID: "Bearer header-secret", ProjectID: "project-safe"})
	New(Options{Writer: &output}).Info(ctx, "mcp", "mcp.tool.requested", "safe", nil)
	if strings.Contains(output.String(), "header-secret") || !strings.Contains(output.String(), `"requestId":"redacted_`) || !strings.Contains(output.String(), `"projectId":"project-safe"`) {
		t.Fatalf("hostile correlation was not safely represented: %s", output.String())
	}
	for _, value := range []string{"req_safe-1", "a:b.c_2"} {
		if !ValidRequestID(value) {
			t.Fatalf("safe request ID rejected: %q", value)
		}
	}
	for _, value := range []string{"", "Bearer secret", "token=value", strings.Repeat("x", 129)} {
		if ValidRequestID(value) {
			t.Fatalf("unsafe request ID accepted: %q", value)
		}
	}
}

func TestUsageTokenCountersAreNotMistakenForCredentials(t *testing.T) {
	var output bytes.Buffer
	New(Options{Writer: &output}).Info(context.Background(), "codex", "codex.usage.snapshot", "usage", map[string]any{"inputTokens": int64(10), "cachedInputTokens": int64(3), "totalTokens": int64(13), "accessToken": "secret"})
	if !strings.Contains(output.String(), `"inputTokens":10`) || !strings.Contains(output.String(), `"cachedInputTokens":3`) || !strings.Contains(output.String(), `"accessToken":"[redacted]"`) || strings.Contains(output.String(), `"accessToken":"secret"`) {
		t.Fatalf("usage/credential sanitization=%s", output.String())
	}
}
