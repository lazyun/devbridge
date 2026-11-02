package controlapi_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/mcpserver"
)

// Test-only explicit user choices, real Core and public MCP; never a native/model runtime.
func TestRuntimeUserDecisionLateConfirmationWithLiveSuccessorMCP(t *testing.T) {
	var prepared agentruntime.UserDecisionInput
	runCancellationLiveSuccessorFixture(t, func(f *upperLifecycleFixture, call func(string, map[string]any, bool) []byte, old agentruntime.Request, successor *agentruntime.Request) bool {
		read := func(request agentruntime.Request) agentruntime.Request {
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
			for _, r := range out.Result.Requests {
				if r.ID == request.ID {
					if r.Identity != request.Identity {
						t.Fatalf("public request identity changed: %+v", r)
					}
					return r
				}
			}
			t.Fatalf("public operation did not expose exact request: %s", raw)
			return agentruntime.Request{}
		}
		args := func(in agentruntime.UserDecisionInput) map[string]any {
			return map[string]any{"aiSessionId": f.session, "decision": in}
		}
		if successor == nil {
			request := read(old)
			if request.State != "WAITING_FOR_USER" || request.UserDecision != nil {
				t.Fatalf("old question not pending: %+v", request)
			}
			draft := agentruntime.UserDecisionInput{Identity: request.Identity, RuntimeRequestID: request.ID, SubmissionRequestID: "explicit-test-user-delayed-confirmation", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
			var envelope struct {
				Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
			}
			raw := call("runtime_request_user_decision_prepare_hash", args(draft), false)
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			out := envelope.Result
			if out.Recorded || out.Decision.PayloadSHA256 == "" || out.Decision.Identity != request.Identity || out.Decision.RuntimeRequestID != request.ID {
				t.Fatalf("not exact unrecorded preparation: %s", raw)
			}
			prepared = out.Decision
			return true
		}
		canceled := read(old)
		if canceled.State != "CANCELED" || canceled.UserDecision != nil {
			t.Fatalf("old request not canceled without decision: %+v", canceled)
		}
		t.Logf("old request terminal state=%s", canceled.State)
		wire := func() []byte {
			b, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
			if e != nil {
				t.Fatal(e)
			}
			return b
		}
		wireBefore := wire()
		current := read(*successor)
		if current.State != "WAITING_FOR_USER" || current.UserDecision != nil {
			t.Fatalf("successor question not pending: %+v", current)
		}
		// Preserve the entire durable authority and runtime request/dispatch ledger.
		tables := []string{"tasks", "task_definition_revisions", "execution_proposals", "execution_cancellations", "decision_resolution_receipts", "runtime_selection_sources", "runtime_selection_bindings", "runtime_scope_plans", "runtime_workset_bindings", "runtime_start_authorities", "run_repository_bindings", "runtime_user_decisions", "runtime_response_dispatch_attempts", "runs", "agent_runtime_states"}
		before := definitionFormalSnapshot(t, f, tables)
		snapshots := &continuationGate{t: t, f: f}
		files := snapshots.snapshot()
		variants := []struct {
			name string
			in   agentruntime.UserDecisionInput
		}{
			{"exact-old-prepared-confirmation", prepared},
			{"old-request-new-run", prepared},
			{"new-request-old-run", prepared},
			{"new-run-and-request-old-operation", prepared},
			{"new-identity-old-request", prepared},
		}
		variants[1].in.RunID = current.RunID
		variants[2].in.RuntimeRequestID = current.ID
		variants[3].in.RunID = current.RunID
		variants[3].in.RuntimeRequestID = current.ID
		variants[4].in.Identity = current.Identity
		for _, tc := range variants {
			t.Run(tc.name, func(t *testing.T) {
				// Do not forge a new checksum locally: mixed identities must fail public
				// scoped preparation, and cannot turn the earlier exact checksum into consent.
				draft := tc.in
				draft.PayloadSHA256 = ""
				call("runtime_request_user_decision_prepare_hash", args(draft), true)
				call("runtime_request_user_decision_submit", args(tc.in), true)
				assertDefinitionFormalSnapshot(t, f, tables, before, "rejected late runtime user confirmation")
				if !reflect.DeepEqual(files, snapshots.snapshot()) {
					t.Fatal("late confirmation changed owned files/HEADs")
				}
				if !bytes.Equal(wireBefore, wire()) {
					t.Fatal("rejected confirmation wrote protocol response")
				}
				after := read(*successor)
				if !reflect.DeepEqual(after, current) {
					t.Fatalf("late confirmation consumed or changed successor request: before=%+v after=%+v", current, after)
				}
			})
		}
		// Positive control: a new explicit simulated user answer for the exact
		// successor is independent authority, not reuse of the old prepared input.
		draft := agentruntime.UserDecisionInput{Identity: current.Identity, RuntimeRequestID: current.ID, SubmissionRequestID: "explicit-test-user-current-confirmation", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
		var positive struct {
			Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
		}
		raw := call("runtime_request_user_decision_prepare_hash", args(draft), false)
		if err := json.Unmarshal(raw, &positive); err != nil {
			t.Fatal(err)
		}
		if positive.Result.Recorded || positive.Result.Decision.PayloadSHA256 == "" || positive.Result.Decision.Identity != current.Identity {
			t.Fatalf("positive hash not exact: %s", raw)
		}
		call("runtime_request_user_decision_submit", args(positive.Result.Decision), false)
		deadline := time.Now().Add(10 * time.Second)
		for {
			state := read(*successor)
			if state.UserDecision != nil && state.UserDecision.RuntimeRequestID == current.ID && agentruntime.ResponseDisposition(state) == "CONSUMED" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("current answer not consumed: %+v", state)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !reflect.DeepEqual(canceled, read(old)) {
			t.Fatal("positive current answer changed canceled old request")
		}
		data := wire()
		if bytes.Count(data, []byte(`"method":"user-response/turn-implementation"`)) != 0 || bytes.Count(data, []byte(`"method":"user-response/turn-implementation-successor"`)) != 1 {
			t.Fatalf("response was not successor-only once: %s", data)
		}
		return false // Positive control can now progress; fixture owns shutdown.
	})
}
