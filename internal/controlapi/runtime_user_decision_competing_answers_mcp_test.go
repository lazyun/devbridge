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

// Two explicitly simulated user choices, real Core/SQLite and Local MCP, and
// the test-only protocol Agent. A request barrier proves overlap at HTTP entry,
// not exhaustive transaction interleavings or native/model qualification.
func TestRuntimeUserDecisionCompetingAnswersLocalMCP(t *testing.T) {
	for _, key := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_RESPONSE_LOSS", "DEVBRIDGE_CODING_V1_MULTI_QUESTION", "DEVBRIDGE_CODING_V1_WITH_APPROVAL", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS"} {
		t.Setenv(key, "")
	}
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
	runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, executionID string) bool {
		f.plane.Retirement.Close()
		pending, live := waitUserRequest(t, f, f.task, "turn-implementation")
		if pending.ExecutionID != executionID || live.ExecutionProposalID != executionID {
			t.Fatal("wrong runtime question")
		}
		service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "competing-answer-test", Ready: true}, f.plane.AIContext, f.facade)
		if err != nil {
			t.Fatal(err)
		}
		adapter, err := localmcp.New(localmcp.Config{RequestTimeout: 30 * time.Second}, service)
		if err != nil {
			t.Fatal(err)
		}
		arrived := make(chan string, 2)
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
							Decision agentruntime.UserDecisionInput `json:"decision"`
						} `json:"arguments"`
					} `json:"params"`
				}
				if json.Unmarshal(raw, &wire) == nil && wire.Method == "tools/call" && wire.Params.Name == "runtime_request_user_decision_submit" && admitted.Add(1) <= 2 {
					arrived <- wire.Params.Arguments.Decision.SubmissionRequestID
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
			c := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "competing-answer", Version: "1"}, nil)
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
		choices := make([]agentruntime.UserDecisionInput, 2)
		for i, answer := range []string{"yes", "no"} {
			draft := agentruntime.UserDecisionInput{Identity: pending.Identity, RuntimeRequestID: pending.ID, SubmissionRequestID: "explicit-competing-" + answer, ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: answer, SelectedOptionID: answer}
			var prepared struct {
				Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
			}
			decode(call(i, "runtime_request_user_decision_prepare_hash", args(draft)), &prepared)
			if prepared.Result.Recorded || prepared.Result.Decision.PayloadSHA256 == "" || prepared.Result.Decision.Identity != pending.Identity || prepared.Result.Decision.RawText != answer {
				t.Fatal("not an exact unrecorded prepared user choice")
			}
			choices[i] = prepared.Result.Decision
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
		for i := range choices {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				out, e := sessions[index].CallTool(ctx, &sdkmcp.CallToolParams{Name: "runtime_request_user_decision_submit", Arguments: args(choices[index])})
				done <- outcome{index, out, e}
			}(i)
		}
		seen := map[string]bool{}
		for range 2 {
			select {
			case id := <-arrived:
				seen[id] = true
			case <-ctx.Done():
				t.Fatal("both submit requests did not reach HTTP gate")
			}
		}
		if len(seen) != 2 || !seen[choices[0].SubmissionRequestID] || !seen[choices[1].SubmissionRequestID] {
			t.Fatalf("barrier did not observe both exact choices: %+v", seen)
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
		winner := -1
		conflict := func(out *sdkmcp.CallToolResult) {
			t.Helper()
			if out == nil || !out.IsError {
				t.Fatalf("loser was not a tool conflict: %+v", out)
			}
			for _, c := range out.Content {
				if text, ok := c.(*sdkmcp.TextContent); ok && strings.HasPrefix(text.Text, "REQUEST_CONFLICT:") {
					return
				}
			}
			t.Fatalf("wrong rejection, not REQUEST_CONFLICT: %+v", out)
		}
		for i, out := range outcomes {
			if out.Err != nil {
				t.Fatalf("competitor %d transport error: %v", i, out.Err)
			}
			if out.Result == nil {
				t.Fatal("missing result")
			}
			if out.Result.IsError {
				conflict(out.Result)
			} else {
				if winner != -1 {
					t.Fatal("both conflicting choices succeeded")
				}
				winner = i
			}
		}
		if winner < 0 {
			t.Fatal("neither valid choice succeeded")
		}
		loser := 1 - winner
		var accepted struct {
			Result controlapi.AgentUserDecisionResult `json:"result"`
		}
		decode(outcomes[winner].Result, &accepted)
		deadline := time.Now().Add(8 * time.Second)
		for {
			state, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, pending.RunID)
			if e != nil {
				t.Fatal(e)
			}
			if len(state.Requests) == 1 && state.Requests[0].State == "RESPONDED" && agentruntime.ResponseDisposition(state.Requests[0]) == "CONSUMED" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("winner not exactly consumed: %+v", state)
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
		if decision.ID != accepted.Result.Decision.ID || decision.SubmissionRequestID != choices[winner].SubmissionRequestID || decision.RawText != choices[winner].RawText || decision.SelectedOptionID != choices[winner].SelectedOptionID || decision.PayloadSHA256 != choices[winner].PayloadSHA256 || decision.Identity != pending.Identity || decision.SubmittedBy != "session:"+f.session {
			t.Fatalf("winner/durable decision mismatch: %+v", decision)
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
			t.Fatalf("dispatch not exact winner: %+v", attempt)
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
		tables := []string{"runtime_user_decisions", "runtime_response_dispatch_attempts", "runtime_response_evidence", "codex_server_requests", "agent_runtime_states", "runs", "tasks", "execution_proposals", "execution_cancellations"}
		before := definitionFormalSnapshot(t, f, tables)
		files := (&continuationGate{t: t, f: f}).snapshot()
		var replay struct {
			Result controlapi.AgentUserDecisionResult `json:"result"`
		}
		decode(call(winner, "runtime_request_user_decision_submit", args(choices[winner])), &replay)
		if !reflect.DeepEqual(replay.Result.Decision, decision) {
			t.Fatal("exact winning replay changed immutable answer")
		}
		conflict(call(loser, "runtime_request_user_decision_submit", args(choices[loser])))
		assertDefinitionFormalSnapshot(t, f, tables, before, "settled winning replay and losing resubmission")
		if !bytes.Equal(wireBefore, wire()) || !reflect.DeepEqual(files, (&continuationGate{t: t, f: f}).snapshot()) {
			t.Fatal("settled replay changed protocol wire or owned files")
		}
		t.Logf("winner=%s submission=%s decision=%s loser=%s conflict=REQUEST_CONFLICT decisionCount=1 dispatchCount=1 actualAgentResponses=1", decision.RawText, decision.SubmissionRequestID, decision.ID, choices[loser].RawText)
		if root := os.Getenv("DEVBRIDGE_COMPETING_ANSWER_EVIDENCE"); root != "" {
			if e := os.MkdirAll(root, 0700); e != nil {
				t.Fatal(e)
			}
			b, _ := json.MarshalIndent(map[string]any{"winner": decision, "loser": choices[loser], "outcomes": outcomes, "dispatch": attempt, "gateArrivals": seen}, "", "  ")
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
