package controlapi_test

// Real Core/SQLite/Git and public Local MCP after typed fixture bootstrap.
// The existing explicitly TEST_ONLY protocol platform launches only this test
// binary, never a native executable or model. No business row is manufactured.
import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestRuntimeRequestRecoveryWithLiveSuccessorMCP(t *testing.T) {
	// Never allow ambient fixture switches to select a real runtime.
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS"} {
		t.Setenv(key, "")
	}
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
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
		readRequest := func(request agentruntime.Request) agentruntime.Request {
			t.Helper()
			var out struct {
				Result struct {
					Requests []agentruntime.Request `json:"runtimeRequests"`
				} `json:"result"`
			}
			raw := call("operation_get", map[string]any{"aiSessionId": f.session, "target": map[string]any{"kind": "EXECUTION", "operationId": request.ExecutionID, "runId": request.RunID}, "include": []string{"RUNTIME_REQUESTS"}}, false)
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			for _, q := range out.Result.Requests {
				if q.ID == request.ID {
					if q.Identity != request.Identity {
						t.Fatal("public request identity changed")
					}
					return q
				}
			}
			t.Fatalf("exact public request absent: %s", raw)
			return agentruntime.Request{}
		}
		answer := func(request agentruntime.Request, command string) {
			t.Helper()
			q := readRequest(request)
			draft := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: command, ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
			var out struct {
				Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
			}
			args := map[string]any{"aiSessionId": f.session, "decision": draft}
			raw := call("runtime_request_user_decision_prepare_hash", args, false)
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if out.Result.Recorded || out.Result.Decision.PayloadSHA256 == "" || out.Result.Decision.Identity != q.Identity || out.Result.Decision.RuntimeRequestID != q.ID {
				t.Fatalf("not exact unrecorded hash: %s", raw)
			}
			args["decision"] = out.Result.Decision
			call("runtime_request_user_decision_submit", args, false)
		}
		answer(oldRequest, "explicit-test-user-original-unknown")
		waitUnknownRequest(t, f, oldRequest, false)
		unknown := readRequest(oldRequest)
		if unknown.UserDecision == nil || len(unknown.DispatchAttempts) != 1 || agentruntime.ResponseDisposition(unknown) != "UNKNOWN" {
			t.Fatalf("missing real immutable unknown dispatch: %+v", unknown)
		}
		command := controlapi.AgentRequestRecoveryInput{Identity: unknown.Identity, RuntimeRequestID: unknown.ID, CommandRequestID: "cancel-real-predecessor", Action: "CANCEL_OPERATION"}
		successorRunID := ""
		recover := func(in controlapi.AgentRequestRecoveryInput, wantError bool) controlapi.AgentRequestRecoveryResult {
			t.Helper()
			raw := call("runtime_request_recovery", map[string]any{"aiSessionId": f.session, "recovery": in}, wantError)
			if wantError {
				return controlapi.AgentRequestRecoveryResult{}
			}
			var out struct {
				Result   controlapi.AgentRequestRecoveryResult `json:"result"`
				Context  map[string]any                        `json:"context"`
				Workflow map[string]any                        `json:"workflow"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if successorRunID != "" {
				if out.Context["runId"] == successorRunID || out.Workflow["nextAction"] != nil || out.Workflow["pendingDecisions"] != nil {
					t.Fatalf("historical runtime recovery borrowed successor guidance: %s", raw)
				}
				if id := out.Context["runId"]; id != nil && id != in.RunID {
					t.Fatalf("historical recovery outer Run identity changed: %s", raw)
				}
				if id := out.Context["executionId"]; id != nil && id != in.OperationID {
					t.Fatalf("historical recovery outer owner identity changed: %s", raw)
				}
			}
			return out.Result
		}
		initial := recover(command, false)
		if initial.Cancellation == nil || initial.Cancellation.OwnerID != predecessor || initial.Cancellation.RunID != oldRun.ID || initial.Cancellation.BackendDisposition == "NO_BACKEND" {
			t.Fatalf("not actual original cancellation: %+v", initial)
		}
		if session, ok := f.plane.Supervisor.GetSession(oldRun.ID); ok {
			select {
			case <-session.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("canceled pipeline did not join")
			}
		}
		var canonical executioncancel.Receipt
		deadline := time.Now().Add(10 * time.Second)
		for {
			receipts, e := f.facade.ListExecutionCancellationsForSession(f.ctx, f.session, predecessor)
			if e != nil {
				t.Fatal(e)
			}
			for _, r := range receipts {
				if r.RequestID == command.CommandRequestID && r.State == "CANCELED" {
					canonical = r
				}
			}
			if canonical.RequestID != "" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("cancellation receipt did not settle")
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
		t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "")
		t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "")
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
		successorRunID = newRun.ID
		canceled := readRequest(oldRequest)
		currentRequest := readRequest(newRequest)
		if canceled.State != "CANCELED" || agentruntime.ResponseDisposition(canceled) != "UNKNOWN" || !reflect.DeepEqual(canceled.UserDecision, unknown.UserDecision) || !reflect.DeepEqual(canceled.DispatchAttempts, unknown.DispatchAttempts) {
			t.Fatalf("cancel changed immutable unknown answer/dispatch: %+v", canceled)
		}
		if currentRequest.State != "WAITING_FOR_USER" || currentRequest.UserDecision != nil {
			t.Fatal("successor is not independently waiting")
		}
		tables := []string{"tasks", "task_definition_revisions", "execution_proposals", "execution_cancellations", "decision_resolution_receipts", "runtime_selection_sources", "runtime_selection_bindings", "runtime_scope_plans", "runtime_workset_bindings", "runtime_start_authorities", "run_repository_bindings", "runtime_user_decisions", "runtime_response_dispatch_attempts", "runs", "agent_runtime_states", "runtime_preparation_authorities"}
		before := definitionFormalSnapshot(t, f, tables)
		snapshots := &continuationGate{t: t, f: f}
		files := snapshots.snapshot()
		wire := func() []byte {
			b, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
			if e != nil {
				t.Fatal(e)
			}
			return b
		}
		wireBefore := wire()
		if bytes.Count(wireBefore, []byte(`"method":"user-response/turn-implementation"`)) != 1 {
			t.Fatal("original dispatch was not exactly once")
		}
		unchanged := func() {
			t.Helper()
			assertDefinitionFormalSnapshot(t, f, tables, before, "historical runtime recovery")
			if !reflect.DeepEqual(files, snapshots.snapshot()) || !bytes.Equal(wireBefore, wire()) {
				t.Fatal("historical recovery changed files or wire")
			}
			if !reflect.DeepEqual(canceled, readRequest(oldRequest)) || !reflect.DeepEqual(currentRequest, readRequest(newRequest)) {
				t.Fatal("historical recovery changed old/new request")
			}
		}
		checkOriginal := func(result controlapi.AgentRequestRecoveryResult) {
			t.Helper()
			if result.Runtime.Identity.RunID != oldRun.ID || len(result.Runtime.Requests) != 1 || !reflect.DeepEqual(result.Runtime.Requests[0], canceled) {
				t.Fatalf("recovery borrowed successor or changed original: %+v", result)
			}
		}
		for range 2 {
			result := recover(command, false)
			if result.Cancellation == nil || result.SuccessorRequiresConfirmation || !reflect.DeepEqual(*result.Cancellation, canonical) {
				t.Fatalf("original cancellation receipt changed: %+v", result.Cancellation)
			}
			checkOriginal(result)
			unchanged()
			waiting := command
			waiting.Action = "CONTINUE_WAITING"
			waiting.CommandRequestID = "original-scoped-read"
			result = recover(waiting, false)
			if result.Cancellation != nil || result.SuccessorRequiresConfirmation {
				t.Fatalf("pure wait created authority: %+v", result)
			}
			checkOriginal(result)
			unchanged()
		}
		variants := []controlapi.AgentRequestRecoveryInput{command, command, command, command}
		variants[0].RunID = newRequest.RunID
		variants[1].RuntimeRequestID = newRequest.ID
		variants[2].RunID = newRequest.RunID
		variants[2].RuntimeRequestID = newRequest.ID
		variants[3].Identity = newRequest.Identity
		for _, in := range variants {
			for _, action := range []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"} {
				in.Action = action
				recover(in, true)
				unchanged()
			}
		}
		// A new exact simulated user answer is separate authority, not redispatch.
		answer(newRequest, "explicit-test-user-successor")
		deadline = time.Now().Add(10 * time.Second)
		for {
			q := readRequest(newRequest)
			if q.UserDecision != nil && q.UserDecision.RuntimeRequestID == newRequest.ID && agentruntime.ResponseDisposition(q) == "CONSUMED" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("legitimate successor answer not consumed: %+v", q)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !reflect.DeepEqual(canceled, readRequest(oldRequest)) {
			t.Fatal("current positive answer changed old request")
		}
		if bytes.Count(wire(), []byte(`"method":"user-response/turn-implementation"`)) != 1 || bytes.Count(wire(), []byte(`"method":"user-response/turn-implementation-successor"`)) != 1 {
			t.Fatal("answer was redispatched or current response duplicated")
		}
		return false
	}, nil)
}
