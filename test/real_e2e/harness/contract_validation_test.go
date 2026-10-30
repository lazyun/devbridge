package harness

import (
	"strings"
	"testing"
)

func TestValidateDiscoveredInputSchemaDetectsHarnessDrift(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"requestId":       map[string]any{"type": "string", "minLength": 1},
			"model":           map[string]any{"type": "string"},
			"reasoningEffort": map[string]any{"type": "string"},
		},
		"required":             []string{"requestId", "model", "reasoningEffort"},
		"additionalProperties": false,
	}
	valid := map[string]any{"requestId": "request-1", "model": "gpt-test", "reasoningEffort": "high"}
	if err := validateDiscoveredInputSchema("task_create", schema, valid); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"requestId", "model", "reasoningEffort"} {
		payload := map[string]any{}
		for key, value := range valid {
			if key != missing {
				payload[key] = value
			}
		}
		err := validateDiscoveredInputSchema("task_create", schema, payload)
		if err == nil || !strings.Contains(err.Error(), "HARNESS_CONTRACT_DRIFT") || !strings.Contains(err.Error(), missing) {
			t.Fatalf("missing %s error=%v", missing, err)
		}
	}
}
