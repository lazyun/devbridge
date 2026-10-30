package controlapi_test

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
)

func TestTaskPanelFeedKeepsExactUnsubmittedIntentAndReadOnlyHistory(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "task-panel-feed")
	submitLifecycleApprovalReady(t, f, "task-panel-approved")
	pending, _ := prepareLifecycleReviewDecision(t, f, "REVISE", "panel-revise")

	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	for range 2 {
		workspace, e := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.session, f.area, f.task)
		if e != nil {
			t.Fatal(e)
		}
		feed := workspace.ConfirmationFeed
		if feed == nil || feed.Availability != "AVAILABLE" || feed.TaskID != f.task || feed.AISessionID != f.session {
			t.Fatalf("feed %+v", feed)
		}
		found := false
		for _, item := range feed.Items {
			if item.IntentID == pending.IntentID {
				found = true
				if item.Version != pending.Version || item.ContentHash != pending.ContentHash || item.State != "PENDING" {
					t.Fatalf("wrong intent identity %+v", item)
				}
			}
		}
		if !found {
			t.Fatalf("unsubmitted proposal omitted: %+v", feed)
		}
	}
	reader := sqlite.NewConfirmationStore(f.plane.DB)
	if _, err := reader.ListTaskConfirmations(f.ctx, f.session, "foreign-project", f.task); err == nil {
		t.Fatal("foreign project feed was accepted")
	}
	if _, err := reader.ListTaskConfirmations(f.ctx, f.session, pending.ProjectID, "foreign-task"); err == nil {
		t.Fatal("foreign task feed was accepted")
	}
	if _, err := reader.ListTaskConfirmations(f.ctx, "foreign-session", pending.ProjectID, f.task); err == nil {
		t.Fatal("foreign session feed was accepted")
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "panel feed reads")
}

func TestTaskPanelFeedCrossesRealMCPWireWithoutPreparingAnotherCard(t *testing.T) {
	stack := newCardLifecycleStack(t)
	pending := stack.prepareReviewDecision("panel-wire-prepare", "")
	f := stack.fixture
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	result, err := stack.session.CallTool(stack.ctx, &sdkmcp.CallToolParams{Name: "dev_bridge_card_get", Arguments: map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "selectionMode": "EXACT"}})
	if err != nil || result.IsError {
		t.Fatalf("wire feed rejected %+v %v", result, err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var workspace p.WorkspaceProjection
	if err = json.Unmarshal(raw, &workspace); err != nil {
		t.Fatal(err)
	}
	feed := workspace.ConfirmationFeed
	if feed == nil || feed.Availability != "AVAILABLE" {
		t.Fatalf("wire omitted feed %s", raw)
	}
	found := false
	for _, item := range feed.Items {
		if item.IntentID == pending.IntentID {
			found = true
			if item.ContentHash != pending.ContentHash || item.State != "PENDING" {
				t.Fatalf("wire identity %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("wire omitted prepared confirmation")
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "real MCP panel read")
}
