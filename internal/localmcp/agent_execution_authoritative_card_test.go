package localmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/executionproposal"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func assertAuthorizationWorkflow(t *testing.T, f *integrationFixture, client *sdkmcp.ClientSession, sid string, e executionproposal.Proposal, status string, actionable bool) map[string]any {
	t.Helper()
	p, err := f.control.GetAgentExecutionForSession(f.ctx, sid, e.ID)
	if err != nil || p.Status != status || p.ExecutionID != e.ID || p.UserAuthorizationRequired {
		t.Fatalf("Core: %+v %v", p, err)
	}
	out := callToolMap(t, f.ctx, client, "workspace_get", map[string]any{"aiSessionId": sid, "featureAreaId": p.FeatureAreaID, "taskId": e.TaskID})
	detail := callToolMap(t, f.ctx, client, "operation_get", map[string]any{"aiSessionId": sid, "target": map[string]any{"kind": "EXECUTION", "operationId": e.ID, "ownerTaskId": e.TaskID}, "include": []string{"RESULT"}})
	core := detail["result"].(map[string]any)["coreOperation"].(map[string]any)
	if core["executionId"] != e.ID || core["status"] != status || core["backendDisposition"] != p.BackendDisposition {
		t.Fatalf("wrong operation: %#v", detail)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, e.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	selected := out["result"].(map[string]any)["selectedTask"].(map[string]any)
	if selected["id"] != owner.ID || selected["phase"] != string(owner.Phase) {
		t.Fatalf("phase not owned by exact Task: %#v", out)
	}
	workflow := out["workflow"].(map[string]any)
	canStart := false
	for _, raw := range workflow["capabilities"].([]any) {
		cap := raw.(map[string]any)
		if (cap["action"] == "START_EXECUTION" || cap["action"] == "START_VERIFICATION") && cap["available"] == true {
			canStart = true
		}
	}
	if canStart != actionable {
		t.Fatalf("start capability %#v", workflow)
	}
	for _, raw := range workflow["pendingDecisions"].([]any) {
		decision := raw.(map[string]any)
		if decision["kind"] == "EXECUTION_AUTHORIZATION" {
			t.Fatalf("second authorization %#v", decision)
		}
	}
	// Read timestamps are observation metadata. The exact durable facts and
	// current capability state must remain equal across a receipt replay.
	return map[string]any{"coreOperation": core, "workflow": workflow, "selectedTask": selected}

}

func TestAuthoritativeWorkflowCancellationDecisionAndReplay(t *testing.T) {
	f, gpt, sid, a := rejectedReadFixture(t)
	local := connectLocalMCP(t, f.libreURL)
	for _, client := range []*sdkmcp.ClientSession{gpt, local} {
		assertAuthorizationWorkflow(t, f, client, sid, a, "CANCELED", false)
	}
	b := nextReadAuthorization(t, f, sid, a)
	for _, client := range []*sdkmcp.ClientSession{gpt, local} {
		assertAuthorizationWorkflow(t, f, client, sid, b, "PENDING", true)
	}
	assertHistoricalRejectedClients(t, f, gpt, sid, a, "TERMINATED", "CANCELED")
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	first := cancelHTTP(t, web.Handler(), f.projectID, b.ID, "", "authoritative-no-run")
	raw, err := f.plane.ExecutionProposals.GetForProject(f.ctx, f.projectID, b.ID)
	if err != nil || raw.Status != "CONFIRMED" || raw.Outcome != "CANCELED" || first.BackendDisposition != "NO_BACKEND" || first.BackendInterruptState != "NOT_SENT" || first.RunID != "" {
		t.Fatalf("canonical no-Run transition: %+v %+v %v", raw, first, err)
	}
	for _, client := range []*sdkmcp.ClientSession{gpt, local} {
		before := assertAuthorizationWorkflow(t, f, client, sid, b, "CANCELED", false)
		replay := cancelHTTP(t, web.Handler(), f.projectID, b.ID, "", "authoritative-no-run")
		after := assertAuthorizationWorkflow(t, f, client, sid, b, "CANCELED", false)
		if !reflect.DeepEqual(first, replay) || !reflect.DeepEqual(before, after) {
			t.Fatal("replay changed workflow or receipt")
		}
	}
	assertHistoricalRejectedClients(t, f, gpt, sid, a, "TERMINATED", "CANCELED")
}

func TestAuthoritativeOrphanCardThroughProductionCancellation(t *testing.T) {
	f, gpt, sid, a := rejectedReadFixture(t)
	// REQUEST_CHANGES leaves its successor awaiting the current explicit
	// confirmation.  This cancellation case needs an independently approved
	// active execution, so build it through the canonical Review-gate fixture.
	current, b := f.admitScalarExecution(t, sid)
	backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: "authoritative-orphan"}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{}), unknown: true}
	f.plane.Coordinator.Codex = backend
	started, err := f.control.StartTaskExecution(f.ctx, sid, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("fake backend did not enter Turn")
	}
	live, ok := f.plane.Supervisor.GetSession(started.Run.ID)
	if !ok {
		t.Fatal("missing live Run")
	}
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	cancelHTTP(t, web.Handler(), f.projectID, b.ID, started.Run.ID, "authoritative-orphan")
	waitCtx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if _, err := live.Wait(waitCtx); errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	receipt := cancelHTTP(t, web.Handler(), f.projectID, b.ID, started.Run.ID, "authoritative-orphan")
	if receipt.BackendDisposition != "UNKNOWN_ORPHANED" || receipt.State != "CANCELED" || backend.calls.Load() != 1 {
		t.Fatal(receipt)
	}
	r, err := f.plane.Runs.Get(f.ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := f.plane.Workspaces.Get(f.ctx, r.WorkspaceID)
	if err != nil || !workspace.RuntimeQuarantined {
		t.Fatalf("orphan workspace: %+v %v", workspace, err)
	}
	boundary, err := f.plane.AgentConversations.Boundary(f.ctx, r.ID)
	if err != nil || boundary.Advance != "UNKNOWN" {
		t.Fatalf("physical boundary was flattened: %+v %v", boundary, err)
	}
	p, err := f.control.GetAgentExecutionForSession(f.ctx, sid, b.ID)
	if err != nil || p.Status != "UNKNOWN_ORPHANED" || p.BackendDisposition != "UNKNOWN_ORPHANED" {
		t.Fatalf("Core: %+v %v", p, err)
	}
	for _, selector := range []map[string]any{{"executionProposalId": b.ID}, {"runId": started.Run.ID}} {
		selector["aiSessionId"], selector["taskId"] = sid, b.TaskID
		card := callToolMap(t, f.ctx, gpt, "ui_workflow_state_get", selector)
		// Unknown cancellation outcome is a frozen recovery entry: the Card
		// projects RECOVERY_REQUIRED while the orphan facts stay unflattened.
		if card["executionId"] != b.ID || card["phase"] != "RECOVERY_REQUIRED" || card["terminal"] != false {
			t.Fatal("Card flattened orphan", card)
		}
		for _, key := range []string{"executionProposal", "approval", "continuation", "workflowGuidance", "nextAction", "currentState"} {
			if card[key] != nil {
				t.Fatal("orphan Card retained action/guidance", card)
			}
		}
		data, _ := json.Marshal(card["agentExecution"])
		var projected map[string]any
		_ = json.Unmarshal(data, &projected)
		if projected["status"] != p.Status || projected["backendDisposition"] != p.BackendDisposition {
			t.Fatal(card)
		}
		if path := os.Getenv("DEVBRIDGE_ORPHAN_CARD_FIXTURE"); path != "" {
			data, _ := json.Marshal(card)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, client := range []*sdkmcp.ClientSession{gpt, connectLocalMCP(t, f.libreURL)} {
		assertAuthorizationWorkflow(t, f, client, sid, b, "UNKNOWN_ORPHANED", false)
	}
	assertHistoricalRejectedClients(t, f, gpt, sid, a, "TERMINATED", "CANCELED")
}
