package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentruntime"
)

func TestRuntimeDispatchPayloadMatchesJSONBytes(t *testing.T) {
	var runtime jsonschema.Schema
	if err := json.Unmarshal([]byte(agentRuntimeOutputSchema), &runtime); err != nil {
		t.Fatal(err)
	}
	workspace := mustWorkspaceProjectionOutputSchema()
	projected, err := schemaProperty(workspace, []string{"presentation", "taskView", "activity", "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{
		"runtime":      runtime.Properties["requests"].Items.Properties["dispatchAttempts"].Items,
		"presentation": projected.Properties["requests"].Items.Properties["dispatchAttempts"].Items,
	}
	var direct jsonschema.Schema
	if err := json.Unmarshal([]byte(runtimeDTOSchema[agentruntime.ResponseDispatchAttempt]()), &direct); err != nil {
		t.Fatal(err)
	}
	schemas["direct"] = &direct
	for name, schema := range schemas {
		t.Run(name, func(t *testing.T) {
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, payload := range [][]byte{nil, {}, []byte("{\"answers\":{\"adaptation\":{\"answers\":[\"yes\"]}}}"), {0, 255, 128}} {
				attempt := agentruntime.ResponseDispatchAttempt{Payload: payload}
				value := jsonValue(t, attempt)
				var expected any
				if payload != nil {
					expected = base64.StdEncoding.EncodeToString(payload)
				}
				if !reflect.DeepEqual(value["payload"], expected) {
					t.Fatalf("wire payload changed: %#v", value["payload"])
				}
				if err := resolved.Validate(value); err != nil {
					t.Fatalf("valid JSON []byte payload rejected: %v", err)
				}
				encoded, _ := json.Marshal(value)
				var roundtrip agentruntime.ResponseDispatchAttempt
				if err := json.Unmarshal(encoded, &roundtrip); err != nil || !reflect.DeepEqual(roundtrip.Payload, payload) {
					t.Fatalf("bytes did not roundtrip: %v", err)
				}
			}
			for _, invalid := range []any{[]any{1, 2}, map[string]any{"answer": "yes"}, true, 42} {
				value := jsonValue(t, agentruntime.ResponseDispatchAttempt{})
				value["payload"] = invalid
				if err := resolved.Validate(value); err == nil {
					t.Fatalf("non-wire payload admitted: %#v", invalid)
				}
			}
		})
	}
}
