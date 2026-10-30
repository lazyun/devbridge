package localmcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSemanticTaskBlocksDecompositionAndEmptyAreaReplaysAcrossAdapters(t *testing.T) {
	f := newIntegrationFixture(t)
	f.plane.Coordinator.Codex = &stubCodexFactory{}
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sessionID := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	parentID := callToolMap(t, f.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": sessionID, "title": "Separate startup and shutdown", "problemStatement": "Determine capability boundaries before creating Tasks."})["id"].(string)
	result := callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sessionID, parentID))
	reviewID := result["review"].(map[string]any)["id"].(string)
	waitForReviewSucceeded(t, f, reviewID)
	if tasks, err := f.plane.Tasks.List(f.ctx); err != nil || len(tasks) != 1 {
		t.Fatalf("Review created Tasks: %+v %v", tasks, err)
	}
	request := map[string]any{"aiSessionId": sessionID, "featureAreaId": parentID, "requestId": "decompose-reviewed-area", "children": []map[string]any{{"title": "Startup", "problemStatement": "Own startup readiness."}, {"title": "Shutdown", "problemStatement": "Own graceful shutdown."}}}
	callToolErrorText(t, f.ctx, local, "featureArea_decompose", request)
	parentID = closureArea(t, f, gpt, sessionID, "Empty boundary to decompose")
	request["featureAreaId"] = parentID
	decomposed := callToolMap(t, f.ctx, local, "featureArea_decompose", request)
	if decomposed["parent"].(map[string]any)["status"] != "DECOMPOSED" {
		t.Fatalf("decompose=%+v", decomposed)
	}
	replay := callToolMap(t, f.ctx, gpt, "featureArea_decompose", request)
	a, _ := json.Marshal(decomposed)
	b, _ := json.Marshal(replay)
	if string(a) != string(b) {
		t.Fatalf("cross-adapter replay changed children: %s %s", a, b)
	}
	for _, raw := range decomposed["children"].([]any) {
		child := raw.(map[string]any)
		id := child["id"].(string)
		if child["status"] != "DISCOVERING" {
			t.Fatalf("child=%+v", child)
		}
		if _, err := f.plane.AgentConversations.Current(f.ctx, id); err == nil {
			t.Fatal("child inherited parent context")
		}
		if reviews, err := f.plane.Reviews.ListByFeatureArea(f.ctx, id); err != nil || len(reviews) != 0 {
			t.Fatalf("child Review inheritance: %+v %v", reviews, err)
		}
	}
	history, err := f.plane.AgentConversations.ListByFeatureArea(f.ctx, parentID)
	if err != nil || len(history) != 0 {
		t.Fatalf("parent context=%+v %v", history, err)
	}
	read := callToolMap(t, f.ctx, local, "operation_get", map[string]any{"aiSessionId": sessionID, "target": map[string]any{"kind": "REVIEW", "operationId": reviewID}, "include": []string{"RESULT"}})
	if read["result"].(map[string]any)["operation"].(map[string]any)["status"] != "COMPLETED" || read["result"].(map[string]any)["runResults"].([]any)[0].(map[string]any)["status"] != "SUCCEEDED" {
		t.Fatalf("terminal history=%+v", read)
	}
	request["children"] = []map[string]any{{"title": "Different", "problemStatement": "Different semantics."}}
	if text := callToolErrorText(t, f.ctx, gpt, "featureArea_decompose", request); !strings.Contains(text, "CONFLICT") {
		t.Fatalf("changed replay=%s", text)
	}
}

func TestSharedTaskCreationLifecycleAndExplicitResetContract(t *testing.T) {
	f := newIntegrationFixture(t)
	gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
	sessionID := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	areas := map[string]string{}
	for _, status := range []string{"DISCOVERING", "ACTIVE", "DECOMPOSED", "RETIRED"} {
		id := callToolMap(t, f.ctx, gpt, "featureArea_create", map[string]any{"aiSessionId": sessionID, "title": status, "problemStatement": "Check exact lifecycle gates."})["id"].(string)
		areas[status] = id
		args := map[string]any{"aiSessionId": sessionID, "featureAreaId": id, "requestId": "activate-" + status}
		if status == "ACTIVE" || status == "RETIRED" {
			callToolMap(t, f.ctx, local, "featureArea_activate", args)
			callToolMap(t, f.ctx, gpt, "featureArea_activate", args)
		}
		if status == "RETIRED" {
			args["requestId"] = "retire"
			callToolMap(t, f.ctx, gpt, "featureArea_retire", args)
		}
		if status == "DECOMPOSED" {
			args["requestId"] = "decompose"
			args["children"] = []map[string]any{{"title": "Child", "problemStatement": "Independent child."}}
			callToolMap(t, f.ctx, local, "featureArea_decompose", args)
		}
	}
	for name, transport := range map[string]*sdkmcp.ClientSession{"gpt": gpt, "local": local} {
		for _, status := range []string{"", "DISCOVERING", "DECOMPOSED", "RETIRED"} {
			args := validOwnershipTaskArgs(sessionID, areas[status])
			args["repositoryId"] = f.repositoryID
			if status == "" {
				delete(args, "featureAreaId")
			}
			text := callToolErrorText(t, f.ctx, transport, "task_create", args)
			if text == "" {
				t.Fatalf("%s accepted %s Task", name, status)
			}
		}
		for _, status := range []string{"DECOMPOSED", "RETIRED"} {
			callToolErrorText(t, f.ctx, transport, "agent_context_reset", map[string]any{"aiSessionId": sessionID, "featureAreaId": areas[status], "requestId": "reset-terminal"})
		}
		// Local keeps the typed create command and rejects an obsolete multiplex
		// action. GPT has no direct feature-area creation command; its separate
		// purpose-confirmation catalog is asserted elsewhere.
		if name == "local" {
			callToolErrorText(t, f.ctx, transport, "featureArea_create", map[string]any{"aiSessionId": sessionID, "title": "Invalid multiplex", "problemStatement": "Invalid", "action": "RESET_AGENT_CONTEXT", "featureAreaId": areas["ACTIVE"], "requestId": "old-reset"})
		}
	}
	if tasks, err := f.plane.Tasks.List(f.ctx); err != nil || len(tasks) != 0 {
		t.Fatalf("rejected Task writes materialized rows: %+v %v", tasks, err)
	}
	for _, transport := range []*sdkmcp.ClientSession{gpt, local} {
		args := validOwnershipTaskArgs(sessionID, areas["ACTIVE"])
		args["repositoryId"] = f.repositoryID
		text := callToolErrorText(t, f.ctx, transport, "task_create", args)
		if text == "" {
			t.Fatalf("ACTIVE without Review Approval: %s", text)
		}
	}

}

func validOwnershipTaskArgs(sessionID, areaID string) map[string]any {
	return map[string]any{"aiSessionId": sessionID, "featureAreaId": areaID, "title": "Explicit intent", "goal": "Clarify startup", "constraints": []string{}, "executionScope": map[string]any{"kind": "REPOSITORY", "paths": []string{}}, "implementationInstruction": "Clarify startup.", "verificationSpec": map[string]any{"mode": "NONE", "waiver": map[string]any{"source": "USER_WAIVED", "reason": "Synthetic test."}}, "model": "gpt-5.6-terra", "reasoningEffort": "high"}
}
