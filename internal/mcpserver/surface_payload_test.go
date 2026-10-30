package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
)

// Report a reproducible fixture comparison rather than extrapolating real host
// token savings. The full Task source is the same retained Core DTO in both.
func TestSurfaceRepresentativePayloadSizes(t *testing.T) {
	fixture := newSurfaceReadFixture()
	fixture.task.ProblemStatement = fixture.task.WorkSpec.Goal
	compact, err := WorkspaceGetCore(context.Background(), fixture, WorkspaceGetInput{AISessionID: "session", TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	detailed, err := WorkspaceGetCore(context.Background(), fixture, WorkspaceGetInput{AISessionID: "session", TaskID: "task", Include: []string{"TASK"}})
	if err != nil {
		t.Fatal(err)
	}
	oldTask, _ := json.Marshal(taskOutput(fixture.task, true))
	compactJSON, _ := json.Marshal(compact)
	detailedJSON, _ := json.Marshal(detailed)
	if len(compactJSON) >= len(detailedJSON) || len(compactJSON) >= len(oldTask) {
		t.Fatalf("default is not compact: task=%d default=%d optIn=%d", len(oldTask), len(compactJSON), len(detailedJSON))
	}
	t.Logf("Fixture UTF-8 JSON bytes: retained detailed Task DTO=%d; default workspace_get=%d; workspace_get include TASK=%d", len(oldTask), len(compactJSON), len(detailedJSON))
}
