package structuredschema

import (
	"encoding/json"
	"reflect"
	"testing"
)

// This finite table uses literal input/output fixtures. It intentionally checks
// decoded serialized output before consulting Validate, so the package's own
// validator is not the oracle. Inputs use ordinary JSON maps and []any required
// values, and absent properties are distinguished from explicitly empty maps.
func TestEmptyObjectBoundary(t *testing.T) {
	cases := []struct {
		name  string
		input string
		build func() Schema
		want  string
	}{
		{
			name:  "builder_nil_properties",
			build: func() Schema { return Object(nil) },
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		},
		{
			name:  "builder_empty_properties_control",
			build: func() Schema { return Object(map[string]any{}) },
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		},
		{
			name:  "root_absent_properties",
			input: `{"type":"object"}`,
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		},
		{
			name:  "root_empty_properties_control",
			input: `{"type":"object","properties":{}}`,
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		},
		{
			name:  "required_nested_absent_properties",
			input: `{"type":"object","properties":{"empty":{"type":"object"}},"required":["empty"]}`,
			want:  `{"type":"object","properties":{"empty":{"type":"object","properties":{},"required":[],"additionalProperties":false}},"required":["empty"],"additionalProperties":false}`,
		},
		{
			name:  "required_nested_empty_properties_control",
			input: `{"type":"object","properties":{"empty":{"type":"object","properties":{}}},"required":["empty"]}`,
			want:  `{"type":"object","properties":{"empty":{"type":"object","properties":{},"required":[],"additionalProperties":false}},"required":["empty"],"additionalProperties":false}`,
		},
		{
			name:  "optional_nested_absent_properties",
			input: `{"type":"object","properties":{"empty":{"type":"object"}}}`,
			want:  `{"type":"object","properties":{"empty":{"anyOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false},{"type":"null"}]}},"required":["empty"],"additionalProperties":false}`,
		},
		{
			name:  "optional_nested_empty_properties_control",
			input: `{"type":"object","properties":{"empty":{"type":"object","properties":{}}}}`,
			want:  `{"type":"object","properties":{"empty":{"anyOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false},{"type":"null"}]}},"required":["empty"],"additionalProperties":false}`,
		},
		{
			name:  "array_item_absent_properties",
			input: `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object"}}},"required":["items"]}`,
			want:  `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{},"required":[],"additionalProperties":false}}},"required":["items"],"additionalProperties":false}`,
		},
		{
			name:  "array_item_empty_properties_control",
			input: `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{}}}},"required":["items"]}`,
			want:  `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{},"required":[],"additionalProperties":false}}},"required":["items"],"additionalProperties":false}`,
		},
		{
			name:  "anyof_branch_absent_properties",
			input: `{"type":"object","properties":{"choice":{"anyOf":[{"type":"object"},{"type":"string"}]}},"required":["choice"]}`,
			want:  `{"type":"object","properties":{"choice":{"anyOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false},{"type":"string"}]}},"required":["choice"],"additionalProperties":false}`,
		},
		{
			name:  "anyof_branch_empty_properties_control",
			input: `{"type":"object","properties":{"choice":{"anyOf":[{"type":"object","properties":{}},{"type":"string"}]}},"required":["choice"]}`,
			want:  `{"type":"object","properties":{"choice":{"anyOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false},{"type":"string"}]}},"required":["choice"],"additionalProperties":false}`,
		},
		{
			name:  "allof_branch_absent_properties",
			input: `{"type":"object","properties":{"choice":{"allOf":[{"type":"object"}]}},"required":["choice"]}`,
			want:  `{"type":"object","properties":{"choice":{"allOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
		},
		{
			name:  "allof_branch_empty_properties_control",
			input: `{"type":"object","properties":{"choice":{"allOf":[{"type":"object","properties":{}}]}},"required":["choice"]}`,
			want:  `{"type":"object","properties":{"choice":{"allOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false}]}},"required":["choice"],"additionalProperties":false}`,
		},
		{
			name:  "oneof_branch_absent_properties",
			input: `{"type":"object","properties":{"choice":{"oneOf":[{"type":"object"},{"type":"null"}]}},"required":["choice"]}`,
			want:  `{"type":"object","properties":{"choice":{"oneOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false},{"type":"null"}]}},"required":["choice"],"additionalProperties":false}`,
		},
		{
			name:  "oneof_branch_empty_properties_control",
			input: `{"type":"object","properties":{"choice":{"oneOf":[{"type":"object","properties":{}},{"type":"null"}]}},"required":["choice"]}`,
			want:  `{"type":"object","properties":{"choice":{"oneOf":[{"type":"object","properties":{},"required":[],"additionalProperties":false},{"type":"null"}]}},"required":["choice"],"additionalProperties":false}`,
		},
		{
			name:  "defs_absent_properties",
			input: `{"type":"object","properties":{},"$defs":{"Empty":{"type":"object"}}}`,
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false,"$defs":{"Empty":{"type":"object","properties":{},"required":[],"additionalProperties":false}}}`,
		},
		{
			name:  "defs_empty_properties_control",
			input: `{"type":"object","properties":{},"$defs":{"Empty":{"type":"object","properties":{}}}}`,
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false,"$defs":{"Empty":{"type":"object","properties":{},"required":[],"additionalProperties":false}}}`,
		},
		{
			name:  "definitions_absent_properties",
			input: `{"type":"object","properties":{},"definitions":{"Empty":{"type":"object"}}}`,
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false,"definitions":{"Empty":{"type":"object","properties":{},"required":[],"additionalProperties":false}}}`,
		},
		{
			name:  "definitions_empty_properties_control",
			input: `{"type":"object","properties":{},"definitions":{"Empty":{"type":"object","properties":{}}}}`,
			want:  `{"type":"object","properties":{},"required":[],"additionalProperties":false,"definitions":{"Empty":{"type":"object","properties":{},"required":[],"additionalProperties":false}}}`,
		},
		{
			name:  "required_names_sorted_optional_nullable_control",
			input: `{"type":"object","properties":{"zeta":{"type":"integer"},"alpha":{"type":"string"},"middle":{"type":"boolean"}},"required":["zeta","middle"]}`,
			want:  `{"type":"object","properties":{"zeta":{"type":"integer"},"alpha":{"anyOf":[{"type":"string"},{"type":"null"}]},"middle":{"type":"boolean"}},"required":["alpha","middle","zeta"],"additionalProperties":false}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var output Schema
			if tc.build != nil {
				output = tc.build()
			} else {
				var input map[string]any
				if err := json.Unmarshal([]byte(tc.input), &input); err != nil {
					t.Fatalf("invalid literal input: %v", err)
				}
				output = NormalizeGenerated(input)
			}
			// Use encoding/json directly: Marshal intentionally panics when
			// Validate rejects a schema, obscuring the structural comparison.
			raw, err := json.Marshal(output)
			if err != nil {
				t.Fatalf("cannot serialize output: %v", err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("cannot decode output: %v", err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatalf("invalid literal expectation: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("literal oracle mismatch\n got: %s\nwant: %s", raw, tc.want)
			}
			// These checks are secondary evidence, not the output oracle.
			if err := Validate([]byte(tc.want)); err != nil {
				t.Fatalf("literal expectation fails local consistency check: %v", err)
			}
			t.Logf("secondary Validate(actual): %v", Validate(raw))
		})
	}
}
