package localmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
)

const localMCPProductTools = 27

func TestLocalMCPDiscoveryContract(t *testing.T) {
	session := connectLocalMCP(t, startLocalMCPTestServer(t, readyService(t)))
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Name == "task_review_start" {
			got, marshalErr := json.Marshal(tool.InputSchema)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var canonical any
			if unmarshalErr := json.Unmarshal([]byte(mcpserver.ExternalReviewStartInputSchema), &canonical); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			want, _ := json.Marshal(canonical)
			if !bytes.Equal(got, want) {
				t.Fatalf("Local MCP Review schema diverged from canonical contract:\ngot %s\nwant %s", got, want)
			}
		}
	}
	want := mcpserver.SurfaceBusinessToolNames
	if len(names) != len(want) {
		t.Fatalf("Local MCP discovery = %v, want exactly %d tools", names, len(want))
	}
	byName := map[string]bool{}
	for _, name := range names {
		byName[name] = true
	}
	for _, name := range want {
		if !byName[name] {
			t.Fatalf("local MCP tool missing: %s (have %v)", name, names)
		}
	}
	// No ChatGPT app-private decision tools, no ChatGPT continuation surface.
	for _, forbidden := range []string{"ui_task_decide", "ui_execution_decide", "ui_scope_expansion_decide", "ui_approval_decide", "ui_workflow_state_get"} {
		if byName[forbidden] {
			t.Fatalf("ChatGPT app-private tool leaked into the Local MCP adapter: %s", forbidden)
		}
	}
	// No ChatGPT decision-card resource.
	resources, err := session.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Resources) != 0 {
		t.Fatalf("The Local MCP adapter must not expose ChatGPT decision resources: %+v", resources.Resources)
	}
	if got := localmcp.LocalMCPServerInstructions; !strings.Contains(got, "decision_respond") || !strings.Contains(got, "aiSessionId") || strings.Contains(got, "ui_task_decide") {
		t.Fatalf("Local MCP instructions contract drifted: %q", got)
	}
	for _, want := range []string{
		"feature_area_create\ndefaults to CREATE_ONLY",
		"CREATE_TASK guidance is not automatic authority",
		"CREATE_AND_REVIEW with a stable requestId",
		"task_create then task_review_start",
		"Review Approval and Task Acceptance remain exact human decision\nboundaries",
		"nextAction is guidance rather than client auto-execution",
	} {
		if !strings.Contains(localmcp.LocalMCPServerInstructions, want) {
			t.Fatalf("Local MCP composition instruction omits %q", want)
		}
	}
}
