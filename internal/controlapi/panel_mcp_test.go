package controlapi_test

import (
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestSinglePanelActualMCPOpensUnboundSessionWithoutCreatingWork(t *testing.T) {
	s := newCardLifecycleStack(t)
	f := s.fixture
	initialized, err := f.plane.AIContext.InitializeSession(f.ctx, aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local"}, "new-panel-session", "")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: initialized.AISessionID, RequestID: "new-panel-setup", Purpose: c.ProjectSetup, Action: "SELECT_PROJECT", ProjectSetup: &c.ProjectBody{Summary: "Select existing Project"}, Variants: []c.Variant{{ID: "enter", Label: "进入项目", Summary: "Bind only this Session"}}})
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"ai_sessions", "confirmation_intents", "tasks", "reviews", "runs"}
	before := definitionFormalSnapshot(t, f, tables)
	opened, err := s.session.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: "panel_show", Arguments: map[string]any{"aiSessionId": initialized.AISessionID, "presentationRequestId": "one-side-panel"}})
	if err != nil {
		t.Fatal(err)
	}
	if opened.IsError {
		t.Fatalf("panel show: %+v", opened)
	}
	if opened.Meta["singlePanel"] != true || opened.Meta["entryIdentity"] == "" || opened.Meta["presentationMode"] != "PROJECT_SELECTION" {
		t.Fatalf("incorrect panel mount %+v", opened.Meta)
	}
	read, err := s.session.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: "dev_bridge_panel_get", Arguments: map[string]any{"aiSessionId": initialized.AISessionID}})
	if err != nil {
		t.Fatal(err)
	}
	if read.IsError || read.Meta["openai/outputTemplate"] != nil {
		t.Fatalf("feed emitted Card: %+v", read)
	}
	feed := rawEnvelope(read)
	items, _ := feed["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["intentId"] != prepared.IntentID {
		t.Fatalf("feed missing original proposal: %+v", feed)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "MCP panel show/feed")
}

// Exercise the real MCP adapter/Session admission and SQLite projection readers.
// UI focus must never turn navigation into approval or any business write.
func TestPanelFocusActualMCPKeepsExactAreaOverviewAndBusinessRecords(t *testing.T) {
	s := newCardLifecycleStack(t)
	f := s.fixture
	tables := []string{"ai_sessions", "confirmation_intents", "tasks", "reviews", "runs", "feature_areas", "task_definition_revisions"}
	before := definitionFormalSnapshot(t, f, tables)
	call := func(name string, args map[string]any, wantError bool) map[string]any {
		t.Helper()
		r, err := s.session.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			if wantError {
				return nil
			}
			t.Fatal(err)
		}
		if r.IsError != wantError {
			t.Fatalf("%s: %+v", name, r)
		}
		if r.Meta["openai/outputTemplate"] != nil {
			t.Fatalf("silent navigation emitted Card: %+v", r.Meta)
		}
		if wantError {
			return nil
		}
		return rawEnvelope(r)
	}
	focus := func(area, task string) map[string]any {
		args := map[string]any{"aiSessionId": f.session, "projectId": f.project}
		if area != "" {
			args["featureAreaId"] = area
		}
		if task != "" {
			args["taskId"] = task
		}
		return call("panel_focus", args, false)["navigation"].(map[string]any)
	}
	selected := focus("", f.task)
	if selected["taskId"] != f.task || selected["featureAreaId"] != f.area {
		t.Fatal(selected)
	}
	replay := focus(f.area, f.task)
	if replay["version"] != selected["version"] {
		t.Fatal("unchanged focus changed version")
	}
	area := focus(f.area, "")
	if area["taskId"] != "" || area["featureAreaId"] != f.area {
		t.Fatal(area)
	}
	read := call("dev_bridge_card_get", map[string]any{"aiSessionId": f.session, "projectId": f.project, "featureAreaId": f.area, "taskId": "", "selectionMode": "EXACT"}, false)
	if read["selectedTaskId"] != nil && read["selectedTaskId"] != "" {
		t.Fatalf("Area overview auto-selected Task: %+v", read)
	}
	project := focus("", "")
	if project["taskId"] != "" || project["featureAreaId"] != "" {
		t.Fatal(project)
	}
	focus(f.area, f.task)
	for _, args := range []map[string]any{
		{"aiSessionId": f.session, "projectId": "foreign", "taskId": f.task},
		{"aiSessionId": f.session, "projectId": f.project, "featureAreaId": "foreign", "taskId": f.task},
		{"aiSessionId": f.session, "projectId": f.project, "taskId": "missing"},
		{"aiSessionId": "missing", "projectId": f.project, "taskId": f.task},
	} {
		call("panel_focus", args, true)
	}
	feed := call("dev_bridge_panel_get", map[string]any{"aiSessionId": f.session}, false)
	n := feed["navigation"].(map[string]any)
	if n["taskId"] != f.task || n["featureAreaId"] != f.area {
		t.Fatal(feed)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "MCP panel focus and exact overview")
}
