package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
)

func TestProjectInspectionToolSchemasAndProductionFacade(t *testing.T) {
	var _ m5CanonicalInspectionService = (*controlapi.Facade)(nil)
	for name, raw := range map[string]string{
		"prepare": projectInspectionPrepareSchema,
		"plan": projectInspectionPlanSchema,
		"confirm": projectInspectionConfirmSchema,
		"start": projectInspectionStartSchema,
		"probe": probeOutputSchema,
	} {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil { t.Fatalf("%s schema: %v", name, err) }
		if decoded["type"] != "object" || decoded["additionalProperties"] != false { t.Fatalf("%s schema is not closed: %v", name, decoded) }
	}
}
