package approval

import (
	"encoding/json"
	"strings"
)

// MaxPresentationPayloadBytes bounds approval context that may be returned to
// any client. It is deliberately shared by all presentation adapters.
const MaxPresentationPayloadBytes = 16 << 10

// SafePresentationPayload returns a bounded, redacted copy of an approval
// payload. It is presentation-only and must never be used as resolver input.
func SafePresentationPayload(raw json.RawMessage) (json.RawMessage, bool) {
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if len(raw) == 0 || decoder.Decode(&value) != nil {
		return json.RawMessage(`{"unavailable":true}`), false
	}
	redactPresentationValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"unavailable":true}`), false
	}
	if len(encoded) <= MaxPresentationPayloadBytes {
		return encoded, false
	}
	summary, _ := json.Marshal(struct {
		Truncated bool `json:"truncated"`
		ByteCount int  `json:"byteCount"`
	}{Truncated: true, ByteCount: len(encoded)})
	return summary, true
}

func redactPresentationValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		secretEnvironmentValue := false
		if name, ok := typed["name"].(string); ok && SecretPayloadKey(name) {
			secretEnvironmentValue = true
		}
		for key, nested := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
			if SecretPayloadKey(normalized) || (secretEnvironmentValue && normalized == "value") {
				typed[key] = "[REDACTED]"
				continue
			}
			if text, ok := nested.(string); ok && strings.HasPrefix(text, "/") && (strings.Contains(normalized, "path") || normalized == "cwd") {
				typed[key] = "[REDACTED_PATH]"
				continue
			}
			redactPresentationValue(nested)
		}
	case []any:
		for _, nested := range typed {
			redactPresentationValue(nested)
		}
	}
}

// SecretPayloadKey identifies payload field names that cannot be exposed by a
// client-neutral presentation. It is also used by stricter client adapters.
func SecretPayloadKey(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_"))
	for _, fragment := range []string{"secret", "password", "passwd", "api_key", "apikey", "access_token", "refresh_token", "authorization", "cookie", "credential", "token"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
