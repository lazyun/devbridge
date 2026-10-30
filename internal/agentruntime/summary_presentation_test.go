package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryIncludesCurrentItemKindWithoutDiagnosticPayload(t *testing.T) {
	s := Snapshot{CurrentItem: &Item{Type: "commandExecution", Metadata: json.RawMessage(`{"command":"private command"}`), OutputTail: "private output"}, LatestPlan: &Plan{Total: 5, Completed: 2, Steps: []PlanStep{{Step: "Update integration", Status: "inProgress"}}}}
	v := s.Summary()
	if v.CurrentItemType != "commandExecution" || v.Step != "Update integration" || v.PlanTotal != 5 || v.PlanCompleted != 2 {
		t.Fatal(v)
	}
	encoded, _ := json.Marshal(v)
	for _, secret := range []string{"private command", "private output", "metadata", "pid", "launchId"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("summary leaked %s", secret)
		}
	}
	s.CurrentItem = nil
	if s.Summary().CurrentItemType != "" {
		t.Fatal("stale item")
	}
}
