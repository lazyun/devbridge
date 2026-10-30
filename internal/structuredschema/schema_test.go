package structuredschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestObjectBuilderClosesAndRequiresEveryNestedObject(t *testing.T) {
	raw := Marshal(Object(map[string]any{
		"targets": Array(Object(map[string]any{
			"scope": Object(map[string]any{"paths": Array(String())}),
		})),
		"result": Nullable(Object(map[string]any{"summary": String()})),
	}))
	if err := Validate(raw); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeGeneratedClosesObjectsAndMakesOptionalPropertyNullable(t *testing.T) {
	generated := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"requiredValue": map[string]any{"type": "string"},
			"optionalValue": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
		},
		"required": []any{"requiredValue"},
	}
	raw := Marshal(NormalizeGenerated(generated))
	var normalized map[string]any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	properties := normalized["properties"].(map[string]any)
	if _, ok := properties["optionalValue"].(map[string]any)["anyOf"]; !ok {
		t.Fatalf("optional generated property was not made nullable: %s", raw)
	}
}

func TestValidateRejectsEveryOpenOrPartiallyRequiredObject(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"open root", `{"type":"object","properties":{},"required":[]}`, "additionalProperties=false"},
		{"open array item", `{"type":"object","additionalProperties":false,"properties":{"items":{"type":"array","items":{"type":"object","properties":{},"required":[]}}},"required":["items"]}`, "$.properties.items.items"},
		{"missing required", `{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string"}},"required":[]}`, `require property "value"`},
		{"open composition", `{"type":"object","additionalProperties":false,"properties":{"value":{"anyOf":[{"type":"object","properties":{},"required":[]},{"type":"null"}]}},"required":["value"]}`, "$.properties.value.anyOf[0]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}
