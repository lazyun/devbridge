package mcpserver

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentruntime"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

var projectionRuntimePaths = [][]string{
	{"taskView", "activity", "runtime"},
	{"latestTaskView", "activity", "runtime"},
}

func mustProjectionOutputSchema() *jsonschema.Schema {
	return mustPresentationOutputSchema[presentation.Projection](projectionRuntimePaths)
}

func mustWorkspaceProjectionOutputSchema() *jsonschema.Schema {
	paths := make([][]string, 0, len(projectionRuntimePaths))
	for _, path := range projectionRuntimePaths {
		paths = append(paths, append([]string{"presentation"}, path...))
	}
	return mustPresentationOutputSchema[presentation.WorkspaceProjection](paths)
}

func mustPresentationOutputSchema[T any](runtimePaths [][]string) *jsonschema.Schema {
	schema, err := jsonschema.For[T](&jsonschema.ForOptions{TypeSchemas: actionTargetSchemaTypes()})
	if err != nil {
		panic(fmt.Sprintf("derive presentation output schema: %v", err))
	}
	for _, path := range runtimePaths {
		runtime, err := schemaProperty(schema, path)
		if err != nil {
			panic(fmt.Sprintf("normalize presentation output schema at %v: %v", path, err))
		}
		if err := normalizeRuntimeItemMetadata(runtime); err != nil {
			panic(fmt.Sprintf("normalize presentation output schema at %v: %v", path, err))
		}
	}
	if err := normalizePresentationRawFields(schema, reflect.TypeFor[T]()); err != nil {
		panic(fmt.Sprintf("normalize presentation raw JSON: %v", err))
	}
	return schema
}

func schemaProperty(schema *jsonschema.Schema, path []string) (*jsonschema.Schema, error) {
	current := schema
	for _, name := range path {
		if current == nil || current.Properties == nil || current.Properties[name] == nil {
			return nil, fmt.Errorf("missing property %q", name)
		}
		current = current.Properties[name]
	}
	return current, nil
}

func normalizeRuntimeItemMetadata(runtime *jsonschema.Schema) error {
	for _, name := range []string{"agent", "agentHealth", "currentItem", "items", "requests", "waitingState"} {
		if runtime == nil || runtime.Properties == nil || runtime.Properties[name] == nil {
			return fmt.Errorf("runtime is not an agentruntime.Snapshot: missing %q", name)
		}
	}
	currentItem := runtime.Properties["currentItem"]
	items := runtime.Properties["items"]
	if items.Items == nil {
		return fmt.Errorf("runtime items has no item schema")
	}
	if err := normalizeRuntimeItemSchema(currentItem); err != nil {
		return fmt.Errorf("currentItem: %w", err)
	}
	if err := normalizeRuntimeItemSchema(items.Items); err != nil {
		return fmt.Errorf("items[]: %w", err)
	}
	return nil
}

func normalizeRuntimeItemSchema(item *jsonschema.Schema) error {
	for _, name := range []string{"id", "type", "status", "metadata", "outputBytes"} {
		if item == nil || item.Properties == nil || item.Properties[name] == nil {
			return fmt.Errorf("not an agentruntime.Item: missing %q", name)
		}
	}
	metadata := item.Properties["metadata"]
	if err := requireRawMessageInference(metadata); err != nil {
		return fmt.Errorf("metadata no longer has the expected RawMessage byte-array inference")
	}
	var normalized jsonschema.Schema
	if err := json.Unmarshal([]byte(agentRuntimeItemMetadataOutputSchema), &normalized); err != nil {
		panic(fmt.Sprintf("parse agent runtime item metadata schema: %v", err))
	}
	item.Properties["metadata"] = &normalized
	item.Required = slices.DeleteFunc(item.Required, func(name string) bool { return name == "metadata" })
	if slices.Contains(item.Required, "metadata") {
		return fmt.Errorf("metadata remains required")
	}
	return nil
}

// Normalize only audited RawMessage leaves. The containing DTO schemas and
// required fields remain inferred and closed. Request leaves reuse the same
// contract as Surface/Local MCP, including Project pending-decision requests.
func normalizePresentationRawFields(schema *jsonschema.Schema, typ reflect.Type) error {
	var runtime jsonschema.Schema
	if err := json.Unmarshal([]byte(agentRuntimeOutputSchema), &runtime); err != nil {
		return err
	}
	return normalizePresentationRawFieldsWithRuntime(schema, typ, &runtime)
}

func normalizePresentationRawFieldsWithRuntime(schema *jsonschema.Schema, typ reflect.Type, runtime *jsonschema.Schema) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if schema == nil {
		return fmt.Errorf("missing schema for %v", typ)
	}
	if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		return normalizePresentationRawFieldsWithRuntime(schema.Items, typ.Elem(), runtime)
	}
	if typ.Kind() != reflect.Struct || len(schema.Properties) == 0 {
		return nil
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" && !field.Anonymous {
			name = field.Name
		}
		child := schema.Properties[name]
		if field.Anonymous && name == "" {
			if err := normalizePresentationRawFieldsWithRuntime(schema, field.Type, runtime); err != nil {
				return err
			}
			continue
		}
		if child == nil {
			return fmt.Errorf("missing presentation property %v.%s", typ, name)
		}
		rawType := reflect.TypeFor[json.RawMessage]()
		isRaw := field.Type == rawType
		isRawList := field.Type.Kind() == reflect.Slice && field.Type.Elem() == rawType
		if isRaw || isRawList {
			if typ == reflect.TypeFor[agentruntime.Item]() && name == "metadata" {
				continue
			} // normalized above
			var replacement *jsonschema.Schema
			switch typ {
			case reflect.TypeFor[agentruntime.Request]():
				replacement = runtime.Properties["requests"].Items.Properties[name]
			case reflect.TypeFor[agentruntime.Diagnostic]():
				replacement = runtime.Properties["lastError"].Properties[name]
			case reflect.TypeFor[presentation.Decision]():
				if name == "permissions" {
					replacement = arbitraryPresentationJSONSchema()
				}
			case reflect.TypeFor[presentation.PendingDecision]():
				if name == "runtimeApprovalRequest" {
					replacement = arbitraryPresentationJSONSchema()
				}
			}
			if replacement == nil {
				return fmt.Errorf("unaudited RawMessage %v.%s", typ, name)
			}
			inferred := child
			if isRawList {
				inferred = child.Items
			}
			if err := requireRawMessageInference(inferred); err != nil {
				return fmt.Errorf("%v.%s: %w", typ, name, err)
			}
			// The SDK requires a schema tree: repeated raw leaves must not
			// share schema pointers even when they share one contract.
			raw, err := json.Marshal(replacement)
			if err != nil {
				return err
			}
			var normalized jsonschema.Schema
			if err := json.Unmarshal(raw, &normalized); err != nil {
				return err
			}
			schema.Properties[name] = &normalized
			continue
		}
		if err := normalizePresentationRawFieldsWithRuntime(child, field.Type, runtime); err != nil {
			return fmt.Errorf("%v.%s: %w", typ, name, err)
		}
	}
	return nil
}

func requireRawMessageInference(schema *jsonschema.Schema) error {
	if schema == nil || !slices.Equal(schema.Types, []string{"null", "array"}) || schema.Items == nil || schema.Items.Type != "integer" {
		return fmt.Errorf("no longer has the expected RawMessage byte-array inference")
	}
	return nil
}

// Raw approval presentation values are bounded/redacted upstream; unlike
// runtime Params, the presentation sanitizer preserves every JSON value kind.
func arbitraryPresentationJSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"object", "array", "string", "number", "boolean", "null"}}
}
