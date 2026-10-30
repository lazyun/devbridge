package approval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSafePresentationPayloadRedactsAndBoundsApprovalContext(t *testing.T) {
	value, truncated := SafePresentationPayload(json.RawMessage(`{"cwd":"/private/repository","authorization":"Bearer secret","environment":[{"name":"TOKEN","value":"secret"}],"command":"go test ./..."}`))
	if truncated {
		t.Fatal("small payload was truncated")
	}
	text := string(value)
	for _, forbidden := range []string{"/private/repository", "Bearer secret", `"value":"secret"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("presentation leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "go test ./...") || !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "[REDACTED_PATH]") {
		t.Fatalf("safe context lost useful redacted facts: %s", text)
	}

	value, truncated = SafePresentationPayload(json.RawMessage(`{"data":"` + strings.Repeat("x", MaxPresentationPayloadBytes) + `"}`))
	if !truncated || len(value) > MaxPresentationPayloadBytes || !strings.Contains(string(value), `"truncated":true`) {
		t.Fatalf("payload was not bounded: %s", value)
	}
}
