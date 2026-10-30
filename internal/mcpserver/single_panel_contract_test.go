package mcpserver_test

import (
	"context"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

func TestSinglePanelDiscoveryKeepsSecondaryCardsAppOnly(t *testing.T) {
	_, _, base := startTestServer(t, readyService(t))
	ctx := context.Background()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "one-panel", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: base + mcpserver.MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		ui, _ := tool.Meta["ui"].(map[string]any)
		visibility := strings.Join(testToolVisibility(ui), ",")
		switch tool.Name {
		case "panel_show":
			found[tool.Name] = true
			if visibility != "model" || ui["resourceUri"] != "ui://devbridge/card/task-panel-v47" {
				t.Fatalf("wrong panel entry: %+v", tool.Meta)
			}
		case "panel_focus":
			found[tool.Name] = true
			if visibility != "model,app" || ui["resourceUri"] != nil {
				t.Fatalf("navigation must be silent and host callable: %+v", tool.Meta)
			}
		case "confirmation_show", "workspace_show", "details_show", "dev_bridge_panel_get":
			found[tool.Name] = true
			if visibility != "app" {
				t.Fatalf("secondary model Card: %s %v", tool.Name, tool.Meta)
			}
			if ui["resourceUri"] != nil || tool.Meta["openai/outputTemplate"] != nil {
				t.Fatalf("private helper cannot advertise a standalone widget: %s %v", tool.Name, tool.Meta)
			}
		}
	}
	if len(found) != 6 {
		t.Fatalf("incomplete single-panel registration: %v", found)
	}
	if !strings.Contains(session.InitializeResult().Instructions, "call panel_show ONCE") || !strings.Contains(session.InitializeResult().Instructions, "never a new conversation Card") {
		t.Fatal("model routing can still duplicate Cards")
	}
}

func TestPanelClickDispatcherToolsAreHostCallable(t *testing.T) {
	_, _, base := startTestServer(t, readyService(t))
	ctx := context.Background()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "panel-host", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: base + mcpserver.MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"agent_operation_recover": "model,app", "runtime_request_user_decision_prepare_hash": "model,app",
		"runtime_request_user_decision_submit": "model,app", "runtime_request_recovery": "model,app",
		"project_setup_prepare": "model,app", "run_analysis_get": "model,app", "ui_approval_get": "app", "ui_approval_decide": "app",
		"file_write": "model", "review_access_prepare": "model", "review_result_submit": "model", "run_analysis_submit": "model",
	}
	for _, tool := range listed.Tools {
		expected, ok := want[tool.Name]
		if !ok {
			continue
		}
		ui, _ := tool.Meta["ui"].(map[string]any)
		if got := strings.Join(testToolVisibility(ui), ","); got != expected {
			t.Errorf("%s: visibility=%s, want %s", tool.Name, got, expected)
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatalf("panel host cannot discover required tools: %v", want)
	}
}
