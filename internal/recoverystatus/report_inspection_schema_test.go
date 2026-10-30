package recoverystatus

import (
	"encoding/json"
	"testing"
)

func TestInspectionReportSchemaForSourceEmbedsCanonicalResultIdentity(t *testing.T) {
	source := Source{OwnerKind: "PROJECT_INSPECTION", OwnerID: "inspection", ProjectID: "project", TaskID: "task", OperationKind: "INSPECTION", OperationID: "inspection", RunID: "source-run", TurnID: "source-turn", RuntimeWorksetHash: "workset-hash", WorkspaceID: "workspace"}
	var schema map[string]any
	if err := json.Unmarshal(ReportSchemaForSource(source), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	version := properties["version"].(map[string]any)
	if version["const"] != InspectionReportVersion {
		t.Fatalf("inspection recovery schema version=%v", version["const"])
	}
	result := properties["inspectionResult"].(map[string]any)
	resultProperties := result["properties"].(map[string]any)
	sourceSchema := resultProperties["source"].(map[string]any)["properties"].(map[string]any)
	for field, want := range map[string]string{"projectId": "project", "inspectionOwnerTaskId": "task", "inspectionOperationId": "inspection", "runId": "source-run", "runtimeWorksetHash": "workset-hash"} {
		if got := sourceSchema[field].(map[string]any)["const"]; got != want {
			t.Fatalf("%s const=%v want=%s", field, got, want)
		}
	}
}

func TestLegacyReportSchemaForSourceBytesRemainV1(t *testing.T) {
	source := Source{OperationKind: "EXECUTION", OperationID: "execution", RunID: "run", TurnID: "turn"}
	var schema map[string]any
	if err := json.Unmarshal(ReportSchemaForSource(source), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	version := properties["version"].(map[string]any)
	enum, ok := version["enum"].([]any)
	if !ok || len(enum) != 1 || enum[0] != ReportVersion {
		t.Fatalf("legacy report version changed: %#v", version)
	}
	if _, ok := properties["inspectionResult"]; ok {
		t.Fatal("legacy v1 schema gained inspectionResult")
	}
}
