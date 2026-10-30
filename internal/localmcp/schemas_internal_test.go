package localmcp

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/mcpserver"
)

func TestLocalMCPSchemasAreClosedAndCarryDecisionHandle(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(localMCPWorkflowOutputSchema), &schema); err != nil {
		t.Fatalf("workflow output schema is not valid JSON: %v", err)
	}
	pendingAny, ok := schema["properties"].(map[string]any)["pendingDecision"]
	if !ok {
		t.Fatalf("workflow output schema lost pendingDecision: %#v", schema["properties"])
	}
	pending := map[string]any{"additionalProperties": "missing", "required": []any{}, "properties": map[string]any{}}
	if wrapper, isMap := pendingAny.(map[string]any); isMap && wrapper["type"] == "object" {
		pending = wrapper
	} else if wrapper, isMap := pendingAny.(map[string]any); isMap {
		if anyOf, isAnyOf := wrapper["anyOf"].([]any); isAnyOf {
			for _, branch := range anyOf {
				if object, isObject := branch.(map[string]any); isObject && object["type"] == "object" {
					pending = object
				}
			}
		}
	}
	if pending["additionalProperties"] != false {
		t.Fatalf("pendingDecision schema is open: %#v", pending)
	}
	properties := pending["properties"].(map[string]any)
	decisionID, ok := properties["decisionId"].(map[string]any)
	if !ok {
		t.Fatalf("pendingDecision schema has no decisionId: %#v", properties)
	}
	if decisionID["minLength"] != float64(1) {
		t.Fatalf("decisionId must be non-empty: %#v", decisionID)
	}
	required := pending["required"].([]any)
	found := false
	for _, name := range required {
		if name == "decisionId" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pendingDecision.required = %v lacks decisionId", required)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("workflow output schema is open: %#v", schema["additionalProperties"])
	}
	var decisionInput map[string]any
	if err := json.Unmarshal([]byte(localMCPDecisionRespondInputSchema), &decisionInput); err != nil {
		t.Fatal(err)
	}
	if decisionInput["additionalProperties"] != false {
		t.Fatal("decision_respond input schema is open")
	}
}

func TestLocalMCPAndGPTShareAuthoritativeDecisionIdentity(t *testing.T) {
	shared := mcpserver.ExternalWorkflowStateOutputSchema
	if localMCPWorkflowOutputSchema != shared {
		t.Fatal("Decision identity diverged across adapters")
	}
}
