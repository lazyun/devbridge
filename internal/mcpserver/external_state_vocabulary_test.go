package mcpserver

import (
	"encoding/json"
	"testing"
)

// The single GPT-facing closed state contract must admit every state the one
// shared Core projection emits; adapters may not drop or rename vocabulary.
func TestExternalWorkflowStateEnumCoversSharedProjectionStates(t *testing.T) {
	var schema struct {
		Properties struct {
			State struct {
				Enum []string `json:"enum"`
			} `json:"state"`
		} `json:"properties"`
		Defs struct {
			States struct {
				Enum []string `json:"enum"`
			} `json:"state"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(externalWorkflowOutputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	enum := append(append([]string{}, schema.Properties.State.Enum...), schema.Defs.States.Enum...)
	if len(schema.Properties.State.Enum) == 0 {
		t.Fatal("top-level state enum missing from the closed output schema")
	}
	for _, want := range []string{"TASK_DELIVERING", "TASK_RECOVERY_REQUIRED", "REVIEW_MATERIALIZING", "FEATURE_AREA_BUSY", "RECOVERY_REQUIRED", "GIT_FINALIZATION_PENDING", "RUNTIME_APPROVAL_REQUIRED"} {
		found := false
		for _, value := range enum {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("shared projection state %q is not admitted by the closed schema enum %v", want, enum)
		}
	}
}
