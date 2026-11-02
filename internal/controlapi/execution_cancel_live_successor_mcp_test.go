package controlapi_test

// Real Core/SQLite/Git and public Local MCP after typed fixture bootstrap.
// The existing explicitly TEST_ONLY protocol platform launches only this test
// binary, never a native executable or model. No business row is manufactured.
import (
	"context"
	"encoding/json"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestExecutionCancellationReplayWithLiveSuccessorMCP(t *testing.T) {
	// Never allow ambient fixture switches to select a real runtime.
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS"} {
		t.Setenv(key, "")
	}
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, predecessor string) bool {
		f.plane.Retirement.Close()
		oldRequest, oldRun := waitUserRequest(t, f, f.task, "turn-implementation")
		if oldRun.ExecutionProposalID != predecessor || oldRun.Status != run.StatusRunning {
			t.Fatalf("predecessor not actual running execution: %+v", oldRun)
		}
		client := newApprovedStartLocalClient(t, f)
		calls := []map[string]any{}
		defer func() {
			if root := os.Getenv("DEVBRIDGE_CANCEL_SUCCESSOR_EVIDENCE"); root != "" {
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Error(err)
					return
				}
				raw, _ := json.MarshalIndent(calls, "", "  ")
				if err := os.WriteFile(filepath.Join(root, "mcp-calls.json"), raw, 0600); err != nil {
					t.Error(err)
				}
			}
		}()
		call := func(name string, args map[string]any, wantError bool) []byte {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			result, err := client.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatalf("%s transport: %v", name, err)
			}
			raw, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			full, _ := json.Marshal(result)
			calls = append(calls, map[string]any{"tool": name, "arguments": args, "wantError": wantError, "response": json.RawMessage(raw), "fullResponse": json.RawMessage(full)})
			if result.IsError != wantError {
				t.Fatalf("%s unexpected error=%v response=%s", name, result.IsError, full)
			}
			return raw
		}
		type envelope struct {
			Result          executioncancel.Receipt `json:"result"`
			Context         map[string]any          `json:"context"`
			Workflow        map[string]any          `json:"workflow"`
			RefreshRequired bool                    `json:"refreshRequired"`
		}
		command := map[string]any{"aiSessionId": f.session, "ownerType": "EXECUTION", "ownerId": predecessor, "runId": oldRun.ID, "requestId": "cancel-real-predecessor", "reason": "Explicit fixture user cancels exact original Run"}
		read := func() envelope {
			var out envelope
			if err := json.Unmarshal(call("operation_cancel", command, false), &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		original := read()
		if (original.Result.State != "CANCELED" && original.Result.State != "CANCEL_REQUESTED") || original.Result.OwnerID != predecessor || original.Result.RunID != oldRun.ID || original.Result.BackendDisposition == "NO_BACKEND" {
			t.Fatalf("actual Run cancellation missing: %+v", original.Result)
		}
		if session, ok := f.plane.Supervisor.GetSession(oldRun.ID); ok {
			select {
			case <-session.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("canceled original pipeline did not stop")
			}
		}

		// ACK is not terminal: join the original pipeline and observe the exact
		// canonical ledger before treating its receipt as settled history.
		deadline := time.Now().Add(10 * time.Second)
		for {
			receipts, e := f.facade.ListExecutionCancellationsForSession(f.ctx, f.session, predecessor)
			if e != nil {
				t.Fatal(e)
			}
			settled := false
			for _, receipt := range receipts {
				if receipt.RequestID == command["requestId"] && receipt.State == "CANCELED" {
					original.Result = receipt
					settled = true
				}
			}
			if settled {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("original cancellation not settled after pipeline exit: %+v", receipts)
			}
			time.Sleep(10 * time.Millisecond)
		}
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		var retry struct {
			Result mcpserver.SurfaceDecisionRespondOutput `json:"result"`
		}
		args := map[string]any{"aiSessionId": f.session, "kind": "TASK_ACCEPTANCE", "taskId": f.task, "featureAreaId": f.area, "taskDefinitionRevisionId": current.CurrentDefinitionRevisionID, "executionId": predecessor, "decisionId": wp.TaskAcceptanceDecisionID(predecessor), "option": "RETRY_IMPLEMENTATION", "requestId": "explicit-unchanged-successor"}
		if err = json.Unmarshal(call("decision_respond", args, false), &retry); err != nil {
			t.Fatal(err)
		}
		if retry.Result.TaskAcceptance == nil || retry.Result.TaskAcceptance.SuccessorExecutionID == "" || retry.Result.TaskAcceptance.SuccessorExecutionID == predecessor {
			t.Fatalf("missing independently authorized successor: %+v", retry)
		}
		successor := retry.Result.TaskAcceptance.SuccessorExecutionID
		var workspace struct {
			Result struct {
				TaskView struct {
					ExecutionStartTarget *wp.TaskExecutionExpectation `json:"executionStartTarget"`
				} `json:"taskView"`
			} `json:"result"`
		}
		if err = json.Unmarshal(call("workspace_get", map[string]any{"aiSessionId": f.session, "taskId": f.task, "include": []string{"TASK"}}, false), &workspace); err != nil {
			t.Fatal(err)
		}
		target := workspace.Result.TaskView.ExecutionStartTarget
		if target == nil || target.ExecutionID != successor || target.ApprovedWorksetHash == "" {
			t.Fatalf("successor has no exact public start target: %+v", workspace)
		}
		// Distinct protocol turn IDs are mandatory across real Runs.
		t.Setenv("DEVBRIDGE_CODING_V1_TURN_SUFFIX", "-successor")
		t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation-successor")
		started := decodeApprovedLocalStart(t, call("execution_start", map[string]any{"aiSessionId": f.session, "taskId": f.task, "expectedTarget": target}, false))
		if started.Proposal.ID != successor || started.Run.ID == oldRun.ID {
			t.Fatalf("wrong successor dispatch: %+v", started)
		}
		newRequest, newRun := waitUserRequest(t, f, f.task, "turn-implementation-successor")
		if newRun.ID != started.Run.ID || newRun.ExecutionProposalID != successor || newRun.Status != run.StatusRunning || newRequest.ID == oldRequest.ID {
			t.Fatalf("successor lacks independent live request: %+v / %+v", newRun, newRequest)
		}
		snapshotter := &continuationGate{t: t, f: f}
		tables := []string{"tasks", "task_definition_revisions", "execution_proposals", "execution_cancellations", "decision_resolution_receipts", "runtime_selection_sources", "runtime_selection_bindings", "runtime_scope_plans", "runtime_workset_bindings", "runtime_start_authorities", "run_repository_bindings", "runtime_user_decisions", "runtime_response_dispatch_attempts"}
		assertReplay := func(cold bool) {
			t.Helper()
			before := definitionFormalSnapshot(t, f, tables)
			files := snapshotter.snapshot()
			successorBefore, e := f.plane.Runs.Get(f.ctx, newRun.ID)
			if e != nil {
				t.Fatal(e)
			}
			originalBefore, e := f.plane.Runs.Get(f.ctx, oldRun.ID)
			if e != nil {
				t.Fatal(e)
			}
			var countBefore int
			if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&countBefore); e != nil {
				t.Fatal(e)
			}
			for range 2 {
				replay := read()
				if !reflect.DeepEqual(replay.Result, original.Result) {
					t.Fatalf("cold=%v historical cancellation changed: %+v", cold, replay.Result)
				}
				if replay.Context["executionId"] != predecessor || replay.Context["runId"] != oldRun.ID || replay.Context["decisionId"] != nil || replay.Context["taskId"] != f.task {
					t.Fatalf("cold=%v original context borrowed successor: %+v", cold, replay.Context)
				}
				if !replay.RefreshRequired || replay.Workflow["stale"] != true || replay.Workflow["nextAction"] != nil || replay.Workflow["pendingDecisions"] != nil {
					t.Fatalf("cold=%v original envelope acquired successor actions: %+v", cold, replay)
				}
			}
			changed := map[string]any{}
			for k, v := range command {
				changed[k] = v
			}
			changed["reason"] = "changed old payload"
			call("operation_cancel", changed, true)
			assertDefinitionFormalSnapshot(t, f, tables, before, "old cancellation replay with real successor")
			if !reflect.DeepEqual(files, snapshotter.snapshot()) {
				t.Fatal("old replay changed owned files or HEADs")
			}
			after, e := f.plane.Runs.Get(f.ctx, newRun.ID)
			if e != nil || !reflect.DeepEqual(after, successorBefore) {
				t.Fatalf("cold=%v old replay changed successor Run: %v", cold, e)
			}
			prior, e := f.plane.Runs.Get(f.ctx, oldRun.ID)
			if e != nil || !reflect.DeepEqual(prior, originalBefore) {
				t.Fatalf("cold=%v old replay changed original Run: %v", cold, e)
			}
			var countAfter int
			if e = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&countAfter); e != nil || countAfter != countBefore {
				t.Fatalf("replay created Run: %d -> %d %v", countBefore, countAfter, e)
			}
		}
		assertReplay(false)
		// Quiesce only the separately authorized successor before reopening. Cold
		// readback qualifies durable history, not native live-session reattachment.
		call("operation_cancel", map[string]any{"aiSessionId": f.session, "ownerType": "EXECUTION", "ownerId": successor, "runId": newRun.ID, "requestId": "fixture-stop-successor", "reason": "Explicit fixture user cleanup before cold historical replay"}, false)
		if session, ok := f.plane.Supervisor.GetSession(newRun.ID); ok {
			select {
			case <-session.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("successor pipeline did not stop")
			}
		}
		client.close()
		reopenDirectDeliveryFixture(t, f)
		f.plane.Retirement.Close()
		client = newApprovedStartLocalClient(t, f)
		assertReplay(true)
		t.Logf("real predecessor %s/%s and successor %s/%s; warm live and cold terminal historical envelopes remained isolated", predecessor, oldRun.ID, successor, newRun.ID)
		return false
	}, nil)
}
