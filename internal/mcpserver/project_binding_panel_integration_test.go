package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFirstPanelProjectSelectionUsesOriginalSessionAndFormalClick(t *testing.T) {
	ctx := context.Background()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	if _, err := plane.Projects.Create(ctx, "Alpha"); err != nil {
		t.Fatal(err)
	}
	chosen, err := plane.Projects.Create(ctx, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "project-binding-test", ApplicationVersion: "test", Ready: true}, plane.AIContext, facade)
	if err != nil {
		t.Fatal(err)
	}
	_, _, base := startTestServer(t, service)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "project-panel", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: base + mcpserver.MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		if res.IsError {
			t.Fatalf("%s: %s %+v", name, raw, res.Content)
		}
		var out map[string]any
		if e = json.Unmarshal(raw, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	initial := call("session_initialize", map[string]any{"requestId": "initialize-first-panel"})["result"].(map[string]any)
	id := initial["aiSessionId"].(string)
	if initial["state"] != "PROJECT_SELECTION_REQUIRED" || initial["boundProjectId"] != nil {
		t.Fatal(initial)
	}
	panelOutput := call("panel_show", map[string]any{"aiSessionId": id, "presentationRequestId": "first-panel"})
	feed := call("dev_bridge_panel_get", map[string]any{"aiSessionId": id})
	if len(feed["items"].([]any)) != 0 {
		t.Fatalf("display prepared a mutation: %+v", feed)
	}
	args := map[string]any{"aiSessionId": id, "requestId": "project-user-click", "purpose": "PROJECT_SETUP", "action": "SELECT_PROJECT", "projectSetup": map[string]any{"summary": "选择当前会话使用的项目。仅绑定项目，不配置分支或启动任务。"}, "variants": []any{map[string]any{"id": "select-project", "label": "绑定所选项目", "summary": "将当前会话绑定到你在确认表单中明确选择的项目。"}}}
	prepared := call("project_setup_prepare", args)
	intent := prepared["intent"].(map[string]any)
	replay := call("project_setup_prepare", args)["intent"].(map[string]any)
	if replay["intentId"] != intent["intentId"] {
		t.Fatal("prepare retry duplicated intent")
	}
	read := map[string]any{"aiSessionId": id, "intentId": intent["intentId"], "purpose": "PROJECT_SETUP", "version": intent["version"], "contentHash": intent["contentHash"]}
	formalRead := call("confirmation_get", read)
	optionsOutput := call("confirmation_options_get", read)
	options := optionsOutput["options"].(map[string]any)
	if len(options["projects"].([]any)) != 2 {
		t.Fatal(options)
	}
	selectedQuery := map[string]any{}
	for k, v := range read {
		selectedQuery[k] = v
	}
	selectedQuery["projectId"] = chosen.ID
	selectedOptions := call("confirmation_options_get", selectedQuery)
	unbound := call("session_initialize", map[string]any{"aiSessionId": id})["result"].(map[string]any)
	if unbound["boundProjectId"] != nil {
		t.Fatal("prepare or options bound project")
	}
	commit := map[string]any{}
	for k, v := range read {
		commit[k] = v
	}
	commit["action"] = "SELECT_PROJECT"
	commit["requestId"] = "formal-user-confirm"
	commit["variantId"] = "select-project"
	commit["selection"] = map[string]any{"projectId": chosen.ID}
	completedOutput := call("confirmation_commit", commit)
	completed := completedOutput["intent"].(map[string]any)
	if completed["state"] != "COMPLETED" {
		t.Fatal(completed)
	}
	call("confirmation_commit", commit)
	bound := call("session_initialize", map[string]any{"aiSessionId": id})["result"].(map[string]any)
	if bound["aiSessionId"] != id || bound["boundProjectId"] != chosen.ID || bound["state"] != "PROJECT_BOUND" {
		t.Fatal(bound)
	}
	if file := os.Getenv("DEVBRIDGE_PROJECT_PANEL_FIXTURE"); file != "" {
		workspace := call("dev_bridge_card_get", map[string]any{"aiSessionId": id, "projectId": chosen.ID, "selectionMode": "EXACT"})
		raw, _ := json.MarshalIndent(map[string]any{"initial": initial, "panel": panelOutput, "prepared": prepared, "confirmation": formalRead, "options": optionsOutput, "selectedOptions": selectedOptions, "completed": completedOutput, "bound": bound, "workspace": workspace}, "", "  ")
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"development_cycles", "tasks", "reviews", "runs", "workspaces"} {
		var count int
		if err := plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("binding created %s: %d %v", table, count, err)
		}
	}
}
