// Package structuredschema builds and validates the strict JSON Schema subset
// accepted by Codex structured outputs.
package structuredschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

type Schema map[string]any

func String() Schema  { return Schema{"type": "string"} }
func Integer() Schema { return Schema{"type": "integer"} }
func Boolean() Schema { return Schema{"type": "boolean"} }
func Null() Schema    { return Schema{"type": "null"} }

func Enum(values ...string) Schema {
	items := make([]any, len(values))
	for i := range values {
		items[i] = values[i]
	}
	return Schema{"type": "string", "enum": items}
}

func Array(items any) Schema { return Schema{"type": "array", "items": items} }

// Object always closes the object and requires every declared property. The
// strict output protocol represents optional values with Nullable, not by
// omitting properties from required.
func Object(properties map[string]any) Schema {
	required := make([]string, 0, len(properties))
	for key := range properties {
		required = append(required, key)
	}
	sort.Strings(required)
	return Schema{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func Nullable(schema any) Schema {
	return Schema{"anyOf": []any{schema, Null()}}
}

// NormalizeGenerated converts a conventional generated schema into the strict
// output subset. Properties that were optional in the generated schema become
// required nullable values, preserving their meaning without permitting key
// omission.
func NormalizeGenerated(schema map[string]any) Schema {
	normalized := normalizeGeneratedNode(schema)
	result, ok := normalized.(map[string]any)
	if !ok {
		panic("generated schema root is not an object")
	}
	return Schema(result)
}

func normalizeGeneratedNode(value any) any {
	node, ok := value.(map[string]any)
	if !ok {
		if list, ok := value.([]any); ok {
			out := make([]any, len(list))
			for i := range list {
				out[i] = normalizeGeneratedNode(list[i])
			}
			return out
		}
		return value
	}
	out := make(map[string]any, len(node)+1)
	for key, child := range node {
		out[key] = normalizeGeneratedNode(child)
	}
	if hasType(node["type"], "object") {
		properties, _ := out["properties"].(map[string]any)
		requiredBefore := map[string]bool{}
		if values, ok := node["required"].([]any); ok {
			for _, value := range values {
				if key, ok := value.(string); ok {
					requiredBefore[key] = true
				}
			}
		}
		for key, child := range properties {
			if !requiredBefore[key] {
				properties[key] = Nullable(child)
			}
		}
		closed := Object(properties)
		for key, child := range out {
			if key != "properties" && key != "required" && key != "additionalProperties" {
				closed[key] = child
			}
		}
		return map[string]any(closed)
	}
	return out
}

func Marshal(schema any) json.RawMessage {
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	if err := Validate(raw); err != nil {
		panic(err)
	}
	return raw
}

// Validate checks the final serialized bytes that will be sent on turn/start.
// Every nested object, including objects inside arrays and composition
// branches, must be closed and must require each declared property.
func Validate(raw []byte) error {
	if len(raw) == 0 {
		return errors.New("structured output schema is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("structured output schema is invalid JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("structured output schema contains trailing JSON")
	}
	if err := validateNode(root, "$", true); err != nil {
		return err
	}
	return nil
}

func validateNode(value any, path string, root bool) error {
	node, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("structured output schema %s must be an object", path)
	}
	if root && !hasType(node["type"], "object") {
		return errors.New("structured output root schema must have type object")
	}
	if hasType(node["type"], "object") {
		closed, present := node["additionalProperties"].(bool)
		if !present || closed {
			return fmt.Errorf("structured output object %s requires additionalProperties=false", path)
		}
		properties, ok := node["properties"].(map[string]any)
		if !ok {
			return fmt.Errorf("structured output object %s requires properties", path)
		}
		requiredValues, ok := node["required"].([]any)
		if !ok {
			return fmt.Errorf("structured output object %s requires required", path)
		}
		required := make(map[string]bool, len(requiredValues))
		for _, raw := range requiredValues {
			key, ok := raw.(string)
			if !ok || required[key] {
				return fmt.Errorf("structured output object %s has invalid required entries", path)
			}
			required[key] = true
		}
		for key, child := range properties {
			if !required[key] {
				return fmt.Errorf("structured output object %s must require property %q", path, key)
			}
			if err := validateNode(child, path+".properties."+key, false); err != nil {
				return err
			}
		}
		for key := range required {
			if _, ok := properties[key]; !ok {
				return fmt.Errorf("structured output object %s requires unknown property %q", path, key)
			}
		}
	}
	if items, ok := node["items"]; ok {
		if err := validateNode(items, path+".items", false); err != nil {
			return err
		}
	}
	for _, keyword := range []string{"anyOf", "allOf", "oneOf"} {
		if raw, ok := node[keyword]; ok {
			branches, ok := raw.([]any)
			if !ok || len(branches) == 0 {
				return fmt.Errorf("structured output schema %s.%s requires branches", path, keyword)
			}
			for i, child := range branches {
				if err := validateNode(child, fmt.Sprintf("%s.%s[%d]", path, keyword, i), false); err != nil {
					return err
				}
			}
		}
	}
	for _, keyword := range []string{"$defs", "definitions"} {
		if raw, ok := node[keyword]; ok {
			definitions, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("structured output schema %s.%s must be an object", path, keyword)
			}
			for key, child := range definitions {
				if err := validateNode(child, path+"."+keyword+"."+key, false); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func hasType(value any, want string) bool {
	switch typed := value.(type) {
	case string:
		return typed == want
	case []any:
		for _, item := range typed {
			if item == want {
				return true
			}
		}
	}
	return false
}
