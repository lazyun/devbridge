package controlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two identical explicitly simulated user submissions, real Core/SQLite and Local MCP, and
// the test-only protocol Agent. A request barrier proves overlap at HTTP entry,
// not exhaustive transaction interleavings or native/model qualification.
// It does not claim a second BeforeDispatch invocation; the public queue fence
// may reject or coalesce replay before that internal boundary.
func TestRuntimeUserDecisionExactConcurrentReplayLocalMCP(t *testing.T) {
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_RESPONSE_LOSS", "DEVBRIDGE_CODING_V1_MULTI_QUESTION", "DEVBRIDGE_CODING_V1_WITH_APPROVAL", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS"} {
		t.Setenv(key, "")
	}
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED_ID", "")
	t.Setenv("DEVBRIDGE_CODING_V1_LATE_RESOLVED_FILE", "")
	t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
	runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, executionID string) bool {
		f.plane.Retirement.Close()
		pending, live := waitUserRequest(t, f, f.task, "turn-implementation")
		if pending.ExecutionID != executionID || live.ExecutionProposalID != executionID {
			t.Fatal("wrong runtime question")
		}
		service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "exact-replay-test", Ready: true}, f.plane.AIContext, f.facade)
		if err != nil {
			t.Fatal(err)
		}
		adapter, err := localmcp.New(localmcp.Config{RequestTimeout: 30 * time.Second}, service)
		if err != nil {
			t.Fatal(err)
		}
		type arrival struct {
			Session  string
			Decision agentruntime.UserDecisionInput
		}
		arrived := make(chan arrival, 2)
		release := make(chan struct{})
		var releaseOnce sync.Once
		openGate := func() { releaseOnce.Do(func() { close(release) }) }
		defer openGate()
		var admitted atomic.Int32
		handler := adapter.Handler()
		host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.Body != nil {
				raw, e := io.ReadAll(r.Body)
				if e != nil {
					http.Error(w, e.Error(), http.StatusBadRequest)
					return
				}
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(raw))
				var wire struct {
					Method string `json:"method"`
					Params struct {
						Name      string `json:"name"`
						Arguments struct {
							Decision    agentruntime.UserDecisionInput `json:"decision"`
							AISessionID string                         `json:"aiSessionId"`
						} `json:"arguments"`
					} `json:"params"`
				}
				if json.Unmarshal(raw, &wire) == nil && wire.Method == "tools/call" && wire.Params.Name == "runtime_request_user_decision_submit" && admitted.Add(1) <= 2 {
					arrived <- arrival{wire.Params.Arguments.AISessionID, wire.Params.Arguments.Decision}
					timer := time.NewTimer(15 * time.Second)
					defer timer.Stop()
					select {
					case <-release:
					case <-r.Context().Done():
						return
					case <-timer.C:
						http.Error(w, "test-only submit barrier timeout", http.StatusGatewayTimeout)
						return
					}
				}
			}
			handler.ServeHTTP(w, r)
		}))
		defer host.Close()
		sessions := make([]*sdkmcp.ClientSession, 0, 2)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		var wg sync.WaitGroup
		defer func() {
			cancel()
			openGate()
			for _, s := range sessions {
				_ = s.Close()
			}
			wg.Wait()
		}()
		for range 2 {
			// Separate clients, sessions and HTTP transports avoid a single connection
			// accidentally serializing the supposedly competing requests.
			transport := http.DefaultTransport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			c := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "exact-replay", Version: "1"}, nil)
			s, e := c.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + localmcp.MCPPath, HTTPClient: &http.Client{Transport: transport, Timeout: 35 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
			if e != nil {
				t.Fatal(e)
			}
			sessions = append(sessions, s)
		}
		args := func(in agentruntime.UserDecisionInput) map[string]any {
			return map[string]any{"aiSessionId": f.session, "decision": in}
		}
		call := func(index int, name string, input map[string]any) *sdkmcp.CallToolResult {
			t.Helper()
			out, e := sessions[index].CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: input})
			if e != nil {
				t.Fatalf("%s transport: %v", name, e)
			}
			return out
		}
		decode := func(out *sdkmcp.CallToolResult, target any) {
			t.Helper()
			if out == nil || out.IsError {
				t.Fatalf("unexpected tool error: %+v", out)
			}
			b, e := json.Marshal(out.StructuredContent)
			if e != nil {
				t.Fatal(e)
			}
			if e = json.Unmarshal(b, target); e != nil {
				t.Fatal(e)
			}
		}
		draft := agentruntime.UserDecisionInput{Identity: pending.Identity, RuntimeRequestID: pending.ID, SubmissionRequestID: "explicit-exact-replay", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: "yes", SelectedOptionID: "yes"}
		var prepared struct {
			Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
		}
		decode(call(0, "runtime_request_user_decision_prepare_hash", args(draft)), &prepared)
		choice := prepared.Result.Decision
		if prepared.Result.Recorded || choice.PayloadSHA256 == "" || choice.Identity != pending.Identity || choice.RawText != "yes" || choice.SubmittedBy != "" {
			t.Fatal("not an exact unrecorded prepared user choice")
		}
		var initialCount int
		if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_user_decisions WHERE runtime_request_id=?`, pending.ID).Scan(&initialCount); e != nil || initialCount != 0 {
			t.Fatalf("prepare recorded authority: %d %v", initialCount, e)
		}
		type outcome struct {
			Index  int
			Result *sdkmcp.CallToolResult
			Err    error
		}
		done := make(chan outcome, 2)
		for i := range 2 {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				out, e := sessions[index].CallTool(ctx, &sdkmcp.CallToolParams{Name: "runtime_request_user_decision_submit", Arguments: args(choice)})
				done <- outcome{index, out, e}
			}(i)
		}
		seen := make([]arrival, 0, 2)
		for range 2 {
			select {
			case got := <-arrived:
				if got.Session != f.session || !reflect.DeepEqual(got.Decision, choice) {
					t.Fatalf("gate saw changed exact submission/scope: %+v", got)
				}
				seen = append(seen, got)
			case <-ctx.Done():
				t.Fatal("both exact submit calls did not reach HTTP gate")
			}
		}
		if len(seen) != 2 || admitted.Load() != 2 {
			t.Fatal("gate did not observe two distinct HTTP invocations")
		}
		openGate()
		outcomes := make([]outcome, 2)
		for range 2 {
			select {
			case out := <-done:
				outcomes[out.Index] = out
			case <-ctx.Done():
				t.Fatal("competing submit calls did not finish")
			}
		}
		wg.Wait()
		unknown := func(out *sdkmcp.CallToolResult) {
			t.Helper()
			if out == nil || !out.IsError {
				t.Fatalf("expected UNKNOWN tool error: %+v", out)
			}
			for _, c := range out.Content {
				if text, ok := c.(*sdkmcp.TextContent); ok && strings.HasPrefix(text.Text, "RESPONSE_DISPOSITION_UNKNOWN:") {
					return
				}
			}
			t.Fatalf("wrong rejection, not RESPONSE_DISPOSITION_UNKNOWN: %+v", out)
		}
		var successes []agentruntime.UserDecision
		for i, out := range outcomes {
			if out.Err != nil {
				t.Fatalf("exact invocation %d transport error: %v", i, out.Err)
			}
			if out.Result == nil {
				t.Fatal("missing result")
			}
			if out.Result.IsError {
				unknown(out.Result)
			} else {
				var accepted struct {
					Result controlapi.AgentUserDecisionResult `json:"result"`
				}
				decode(out.Result, &accepted)
				successes = append(successes, accepted.Result.Decision)
			}
		}
		deadline := time.Now().Add(8 * time.Second)
		for {
			state, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, pending.RunID)
			if e != nil {
				t.Fatal(e)
			}
			if len(state.Requests) == 1 && state.Requests[0].State == "RESPONDING" && agentruntime.ResponseDisposition(state.Requests[0]) == "UNKNOWN" {
				q := state.Requests[0]
				if q.ID != pending.ID || q.UserDecision == nil || q.Projection == nil || q.Projection.ResendAllowed || !reflect.DeepEqual(q.Projection.AvailableActions, []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}) {
					t.Fatalf("UNKNOWN manufactured resend authority or changed exact question: %+v", q)
				}
				observed, readErr := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				// Core transport completion can precede the separate Agent's
				// read/log. Wait for observable receipt, not an arbitrary sleep.
				if bytes.Count(observed, []byte("user-response/turn-implementation")) >= 1 && bytes.HasSuffix(observed, []byte{'\n'}) {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("answer did not settle UNKNOWN: %+v", state)
			}
			time.Sleep(10 * time.Millisecond)
		}
		var count int
		var decisionJSON, attemptJSON string
		if e := f.plane.DB.SQL().QueryRow(`SELECT count(*),COALESCE(max(decision_json),'') FROM runtime_user_decisions WHERE runtime_request_id=?`, pending.ID).Scan(&count, &decisionJSON); e != nil || count != 1 {
			t.Fatalf("not exactly one immutable decision: %d %v", count, e)
		}
		var decision agentruntime.UserDecision
		if e := json.Unmarshal([]byte(decisionJSON), &decision); e != nil {
			t.Fatal(e)
		}
		if decision.SubmissionRequestID != choice.SubmissionRequestID || decision.RawText != choice.RawText || decision.SelectedOptionID != choice.SelectedOptionID || decision.PayloadSHA256 != choice.PayloadSHA256 || decision.Identity != pending.Identity || decision.SubmittedBy != "session:"+f.session {
			t.Fatalf("exact/durable decision mismatch: %+v", decision)
		}
		for _, got := range successes {
			if !reflect.DeepEqual(got, decision) {
				t.Fatalf("successful replay changed immutable decision: %+v", got)
			}
		}
		current, e := f.plane.Runs.Get(f.ctx, pending.RunID)
		if e != nil || string(current.Status) != "RUNNING" || current.RuntimeEpoch != pending.RuntimeEpoch || current.OperationID != pending.OperationID {
			t.Fatalf("owner/run no longer live: %+v %v", current, e)
		}
		var falseProofs int
		if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_response_evidence WHERE run_id=? AND kind='PRE_EFFECT_REJECTED'`, pending.RunID).Scan(&falseProofs); e != nil || falseProofs != 0 {
			t.Fatalf("manufactured retry authority: %d %v", falseProofs, e)
		}
		if e := f.plane.DB.SQL().QueryRow(`SELECT count(*),COALESCE(max(attempt_json),'') FROM runtime_response_dispatch_attempts WHERE runtime_request_id=?`, pending.ID).Scan(&count, &attemptJSON); e != nil || count != 1 {
			t.Fatalf("not exactly one dispatch: %d %v", count, e)
		}
		var attempt agentruntime.ResponseDispatchAttempt
		if e := json.Unmarshal([]byte(attemptJSON), &attempt); e != nil {
			t.Fatal(e)
		}
		expected, e := agentruntime.SerializeUserDecision(pending, decision)
		if e != nil {
			t.Fatal(e)
		}
		if attempt.UserDecisionID != decision.ID || attempt.CommandRequestID != decision.SubmissionRequestID || attempt.Identity != pending.Identity || !bytes.Equal(attempt.Payload, expected) {
			t.Fatalf("dispatch not exact answer: %+v", attempt)
		}
		wire := func() []byte {
			t.Helper()
			b, e := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
			if e != nil {
				t.Fatal(e)
			}
			return b
		}
		wireBefore := wire()
		responses := 0
		for _, line := range bytes.Split(bytes.TrimSpace(wireBefore), []byte{'\n'}) {
			var entry struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if e := json.Unmarshal(line, &entry); e != nil {
				t.Fatal(e)
			}
			if entry.Method == "user-response/turn-implementation" {
				responses++
				var got, want any
				_ = json.Unmarshal(entry.Params, &got)
				_ = json.Unmarshal(expected, &want)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("Agent observed losing/altered answer: %s want %s", entry.Params, expected)
				}
			}
		}
		if responses != 1 {
			t.Fatalf("Agent received %d answers", responses)
		}
		publicUnknown := func() agentruntime.Request {
			t.Helper()
			var out struct {
				Result struct {
					Requests []agentruntime.Request `json:"runtimeRequests"`
				} `json:"result"`
			}
			decode(call(0, "operation_get", map[string]any{"aiSessionId": f.session, "target": map[string]any{"kind": "EXECUTION", "operationId": pending.ExecutionID, "runId": pending.RunID}, "include": []string{"RUNTIME_REQUESTS"}}), &out)
			if len(out.Result.Requests) != 1 {
				t.Fatalf("wrong public runtime requests: %+v", out)
			}
			q := out.Result.Requests[0]
			if q.ID != pending.ID || q.Identity != pending.Identity || q.UserDecision == nil || !reflect.DeepEqual(*q.UserDecision, decision) || q.State != "RESPONDING" || q.Projection == nil || q.Projection.ResponseDisposition != "UNKNOWN" || q.Projection.ResendAllowed || !reflect.DeepEqual(q.Projection.AvailableActions, []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}) {
				t.Fatalf("public UNKNOWN projection granted resend or changed identity: %+v", q)
			}
			return q
		}
		publicBefore := publicUnknown()
		tables := []string{"runtime_user_decisions", "runtime_response_dispatch_attempts", "runtime_response_evidence", "codex_server_requests", "agent_runtime_states", "runs", "tasks", "execution_proposals", "execution_cancellations"}
		before := definitionFormalSnapshot(t, f, tables)
		files := (&continuationGate{t: t, f: f}).snapshot()
		unknown(call(0, "runtime_request_user_decision_submit", args(choice)))
		unknown(call(1, "runtime_request_user_decision_submit", args(choice)))
		if after := publicUnknown(); !reflect.DeepEqual(publicBefore, after) {
			t.Fatal("settled replay changed public UNKNOWN request")
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "settled exact UNKNOWN replays")
		if !bytes.Equal(wireBefore, wire()) || !reflect.DeepEqual(files, (&continuationGate{t: t, f: f}).snapshot()) {
			t.Fatal("settled replay changed protocol wire or owned files")
		}
		t.Logf("exactCalls=2 successfulResults=%d decision=%s decisionCount=1 dispatchCount=1 actualAgentResponses=1 disposition=UNKNOWN", len(successes), decision.ID)
		if root := os.Getenv("DEVBRIDGE_EXACT_REPLAY_EVIDENCE"); root != "" {
			if e := os.MkdirAll(root, 0700); e != nil {
				t.Fatal(e)
			}
			b, _ := json.MarshalIndent(map[string]any{"decision": decision, "outcomes": outcomes, "dispatch": attempt, "gateArrivals": seen, "publicUnknown": publicBefore}, "", "  ")
			if e := os.WriteFile(filepath.Join(root, "result.json"), b, 0600); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(root, "agent-wire.jsonl"), wireBefore, 0600); e != nil {
				t.Fatal(e)
			}
		}
		return false
	}, nil)
}
