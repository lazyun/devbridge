package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSafeApprovalPayloadRedactsSecretsAndAbsolutePaths(t *testing.T) {
	raw := json.RawMessage(`{"command":"go test ./...","cwd":"/Users/example/private/repo","authorization":"Bearer secret","nested":{"apiKey":"secret-value","relativePath":"safe/file.go"},"environment":[{"name":"TOKEN","value":"secret"}]}`)
	value, truncated := safeApprovalPayload(raw)
	if truncated {
		t.Fatal("small payload was truncated")
	}
	text := string(value)
	for _, forbidden := range []string{"/Users/example/private/repo", "Bearer secret", "secret-value", `"value":"secret"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("secret or absolute path leaked: %s", text)
		}
	}
	for _, expected := range []string{"go test ./...", "[REDACTED_PATH]", "[REDACTED]", "safe/file.go"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in %s", expected, text)
		}
	}
}

func TestSafeApprovalPayloadBoundsOutput(t *testing.T) {
	value, truncated := safeApprovalPayload(json.RawMessage(`{"data":"` + strings.Repeat("x", maxApprovalRequestBytes) + `"}`))
	if !truncated || len(value) > maxApprovalRequestBytes || !strings.Contains(string(value), `"truncated":true`) {
		t.Fatalf("value=%s truncated=%v", value, truncated)
	}
}
