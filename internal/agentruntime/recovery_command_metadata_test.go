package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandMetadataRetainsBoundedAuthoritativeActions(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		command := "go test ./..."
		if oversized {
			command = strings.Repeat("x", 5000)
		}
		encoded, _ := json.Marshal(map[string]any{"id": "cmd", "type": "commandExecution", "command": "/bin/sh -c command", "commandActions": []any{map[string]any{"type": "unknown", "command": command}}, "aggregatedOutput": "not metadata"})
		var got map[string]json.RawMessage
		if err := json.Unmarshal(itemMetadata(encoded), &got); err != nil {
			t.Fatal(err)
		}
		if (len(got["commandActions"]) > 0) == oversized {
			t.Fatalf("action evidence bounded incorrectly: %s", got["commandActions"])
		}
		if len(got["aggregatedOutput"]) != 0 {
			t.Fatal("output bypassed dedicated bounded output field")
		}
	}
}
