package mcpserver

import (
	"bytes"
	"encoding/json"
	"testing"

	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestFeatureAreaProductProjectionIsPresentOnEveryReadSurfaceSchema(t *testing.T) {
	for name, raw := range map[string]string{
		"workspace_get": WorkspaceGetOutputSchema,
		"work_list":     WorkListOutputSchema,
		"operation_get": OperationGetOutputSchema,
	} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatalf("%s schema: %v", name, err)
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"domainStatus", "currentTask", "latestTask", "taskPhase", "operationActivity", "requiredAction", "availableActions"} {
			if !bytes.Contains(encoded, []byte(`"`+field+`"`)) {
				t.Fatalf("%s schema does not expose product field %q", name, field)
			}
		}
	}
}

func TestFeatureAreaProductProjectionJSONKeepsUnknownStatusDiagnostic(t *testing.T) {
	product := p.NewFeatureAreaProductProjection("area-1", "Chat", "FUTURE_STATE", false)
	encoded, err := json.Marshal(product)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if _, exists := wire["domainStatus"]; exists {
		t.Fatalf("unknown internal lifecycle must not enter either product state: %s", encoded)
	}
	details, ok := wire["technicalDetails"].(map[string]any)
	if !ok || details["diagnostic"] != "UNKNOWN_FEATURE_AREA_INTERNAL_STATUS" || details["internalStatus"] != "FUTURE_STATE" {
		t.Fatalf("unknown lifecycle diagnostic = %#v", wire["technicalDetails"])
	}
}
