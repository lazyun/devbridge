package controlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two legitimate AI Sessions in one Local caller scope, not separate principals.
// Real MCP/Core/SQLite and an explicitly simulated protocol Agent; no native model.
func TestRuntimeUserDecisionActorIsolationLocalMCP(t *testing.T) {
	for _, k := range []string{"DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "DEVBRIDGE_REAL_CODEX", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CODEX_API_KEY", "DEVBRIDGE_CODING_V1_DROP_RESOLVED", "DEVBRIDGE_CODING_V1_RESPONSE_LOSS", "DEVBRIDGE_CODING_V1_WITH_APPROVAL", "DEVBRIDGE_CODING_V1_TURN_SUFFIX", "DEVBRIDGE_TEST_UNIQUE_TURNS"} {
		t.Setenv(k, "")
	}
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_MULTI_QUESTION", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
	runCodingDeliveryFixtureFrom(t, newUpperLifecycleFixtureScoped(t, "data-instance-local"), func(f *upperLifecycleFixture, executionID string) bool {
		f.plane.Retirement.Close()
		first, _ := waitUserRequest(t, f, f.task, "turn-implementation")
		a, b := newApprovedStartLocalClient(t, f), newApprovedStartLocalClient(t, f)
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		call := func(c *approvedStartLocalClient, name string, args map[string]any) *sdkmcp.CallToolResult {
			t.Helper()
			out, err := c.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatalf("%s transport: %v", name, err)
			}
			return out
		}
		decode := func(out *sdkmcp.CallToolResult, v any) {
			t.Helper()
			if out == nil || out.IsError {
				t.Fatalf("unexpected tool error: %+v", out)
			}
			raw, err := json.Marshal(out.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, v); err != nil {
				t.Fatal(err)
			}
		}
		var init mcpserver.SessionInitializeOutput
		decode(call(b, "session_initialize", map[string]any{"requestId": "actor-isolation-B-initialize"}), &init)
		actorB := init.Result.AISessionID
		if actorB == "" || actorB == f.session {
			t.Fatal("B is not an independent AISession")
		}
		decode(call(b, "project_open", map[string]any{"aiSessionId": actorB, "projectId": f.project, "requestId": "actor-isolation-B-project"}), &map[string]any{})
		read := func(c *approvedStartLocalClient, actor string) []agentruntime.Request {
			t.Helper()
			var out struct {
				Result struct {
					RuntimeRequests []agentruntime.Request `json:"runtimeRequests"`
				} `json:"result"`
			}
			decode(call(c, "operation_get", map[string]any{"aiSessionId": actor, "target": map[string]any{"kind": "EXECUTION", "operationId": executionID, "runId": first.RunID}, "include": []string{"RUNTIME_REQUESTS"}}), &out)
			return out.Result.RuntimeRequests
		}
		var qs []agentruntime.Request
		deadline := time.Now().Add(8 * time.Second)
		for {
			qs = read(a, f.session)
			if len(qs) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("missing two questions")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !reflect.DeepEqual(qs, read(b, actorB)) {
			t.Fatal("B cannot read the same exact questions")
		}
		q1, q2 := qs[0], qs[1]
		for _, q := range qs {
			if q.State != "WAITING_FOR_USER" || q.UserDecision != nil || q.RunID != first.RunID {
				t.Fatalf("not independent waiting request: %+v", q)
			}
		}
		args := func(actor string, in agentruntime.UserDecisionInput) map[string]any {
			return map[string]any{"aiSessionId": actor, "decision": in}
		}
		prepare := func(c *approvedStartLocalClient, actor string, q agentruntime.Request, id, answer string) agentruntime.UserDecisionInput {
			t.Helper()
			draft := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: id, ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "CHATGPT", RawText: answer, SelectedOptionID: answer}
			var out struct {
				Result mcpserver.AgentUserDecisionPreparedHash `json:"result"`
			}
			decode(call(c, "runtime_request_user_decision_prepare_hash", args(actor, draft)), &out)
			if out.Result.Recorded || out.Result.Decision.PayloadSHA256 == "" {
				t.Fatal("prepare recorded authority or omitted hash")
			}
			return out.Result.Decision
		}
		choiceA := prepare(a, f.session, q1, "actor-isolation-Q1", "yes")
		choiceB := prepare(b, actorB, q1, "actor-isolation-Q1", "yes")
		if !reflect.DeepEqual(choiceA, choiceB) {
			t.Fatal("same draft differs between legitimate actors")
		}
		submit := func(c *approvedStartLocalClient, actor string, in agentruntime.UserDecisionInput) controlapi.AgentUserDecisionResult {
			t.Helper()
			var out struct {
				Result controlapi.AgentUserDecisionResult `json:"result"`
			}
			decode(call(c, "runtime_request_user_decision_submit", args(actor, in)), &out)
			return out.Result
		}
		topology := definitionFormalSnapshot(t, f, []string{"runs", "execution_proposals"})
		accepted := submit(a, f.session, choiceA)
		waitConsumed := func(id string) agentruntime.Request {
			t.Helper()
			end := time.Now().Add(8 * time.Second)
			for {
				for _, q := range read(a, f.session) {
					if q.ID == id && q.State == "RESPONDED" && agentruntime.ResponseDisposition(q) == "CONSUMED" {
						return q
					}
				}
				if time.Now().After(end) {
					t.Fatalf("request %s not consumed", id)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		consumed1 := waitConsumed(q1.ID)
		if accepted.Decision.SubmittedBy != "session:"+f.session || accepted.Decision.Identity != q1.Identity {
			t.Fatal("wrong immutable author")
		}
		find := func(id string) agentruntime.Request {
			t.Helper()
			for _, q := range read(a, f.session) {
				if q.ID == id {
					return q
				}
			}
			t.Fatal("missing exact question")
			return agentruntime.Request{}
		}
		if !reflect.DeepEqual(q2, find(q2.ID)) {
			t.Fatal("Q1 answer altered Q2")
		}
		tables := []string{"runtime_user_decisions", "runtime_response_dispatch_attempts", "runtime_response_evidence", "codex_server_requests", "agent_runtime_states", "runs", "tasks", "execution_proposals", "execution_cancellations"}
		wire := func() []byte {
			t.Helper()
			raw, err := os.ReadFile(os.Getenv("DEVBRIDGE_CODING_V1_WIRE_LOG"))
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
		before := definitionFormalSnapshot(t, f, tables)
		files := (&continuationGate{t: t, f: f}).snapshot()
		wireBefore := wire()
		conflict := call(b, "runtime_request_user_decision_submit", args(actorB, choiceB))
		exactConflict := false
		if conflict != nil && conflict.IsError {
			for _, v := range conflict.Content {
				if text, ok := v.(*sdkmcp.TextContent); ok && strings.HasPrefix(text.Text, "REQUEST_CONFLICT:") {
					exactConflict = true
				}
			}
		}
		if !exactConflict {
			t.Fatalf("cross-actor replay was not exact REQUEST_CONFLICT: %+v", conflict)
		}
		replay := submit(a, f.session, choiceA)
		if !reflect.DeepEqual(replay.Decision, accepted.Decision) {
			t.Fatal("original actor replay changed decision")
		}
		if !reflect.DeepEqual(q2, find(q2.ID)) {
			t.Fatal("cross-actor replay altered Q2")
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "cross-actor rejection and original actor replay")
		if !bytes.Equal(wireBefore, wire()) || !reflect.DeepEqual(files, (&continuationGate{t: t, f: f}).snapshot()) {
			t.Fatal("replay altered files/wire")
		}
		// B is authorized to answer its own new question, not globally barred from the Run.
		assertDefinitionFormalSnapshot(t, f, []string{"runs", "execution_proposals"}, topology, "A answer and replay must not create successor")
		choice2 := prepare(b, actorB, q2, "actor-isolation-Q2", "no")
		second := submit(b, actorB, choice2)
		consumed2 := waitConsumed(q2.ID)
		if !reflect.DeepEqual(consumed1, find(q1.ID)) {
			t.Fatal("B answer changed the complete consumed Q1 request")
		}
		if second.Decision.SubmittedBy != "session:"+actorB || second.Decision.ID == accepted.Decision.ID {
			t.Fatal("B positive control did not retain distinct author/decision")
		}
		assertDefinitionFormalSnapshot(t, f, []string{"runs", "execution_proposals"}, topology, "B positive control must not create successor")
		expected := make([][]byte, 2)
		attempts := make([]agentruntime.ResponseDispatchAttempt, 2)
		for i, pair := range []struct {
			q agentruntime.Request
			d agentruntime.UserDecision
		}{{q1, accepted.Decision}, {q2, second.Decision}} {
			var n int
			var raw string
			if err := f.plane.DB.SQL().QueryRow("SELECT count(*),COALESCE(max(decision_json),'') FROM runtime_user_decisions WHERE runtime_request_id=?", pair.q.ID).Scan(&n, &raw); err != nil || n != 1 {
				t.Fatalf("decision count %d: %v", n, err)
			}
			var stored agentruntime.UserDecision
			if err := json.Unmarshal([]byte(raw), &stored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, pair.d) {
				t.Fatal("persisted immutable decision mismatch")
			}
			if err := f.plane.DB.SQL().QueryRow("SELECT count(*),COALESCE(max(attempt_json),'') FROM runtime_response_dispatch_attempts WHERE runtime_request_id=?", pair.q.ID).Scan(&n, &raw); err != nil || n != 1 {
				t.Fatalf("attempt count %d: %v", n, err)
			}
			if err := json.Unmarshal([]byte(raw), &attempts[i]); err != nil {
				t.Fatal(err)
			}
			var err error
			expected[i], err = agentruntime.SerializeUserDecision(pair.q, pair.d)
			if err != nil {
				t.Fatal(err)
			}
			at := attempts[i]
			if at.Identity != pair.q.Identity || at.UserDecisionID != pair.d.ID || at.CommandRequestID != pair.d.SubmissionRequestID || !bytes.Equal(at.Payload, expected[i]) {
				t.Fatal("attempt not exact request/decision/payload")
			}
		}
		// The fixture wire method does NOT contain requestID. Distinct yes/no payloads,
		// exact persisted attempt identities and each CONSUMED request supply correlation.
		counts := []int{0, 0}
		responses := 0
		finalWire := wire()
		for _, line := range bytes.Split(bytes.TrimSpace(finalWire), []byte{'\n'}) {
			var entry struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(line, &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Method != "user-response/turn-implementation" {
				continue
			}
			responses++
			var got any
			if err := json.Unmarshal(entry.Params, &got); err != nil {
				t.Fatal(err)
			}
			for i, payload := range expected {
				var want any
				_ = json.Unmarshal(payload, &want)
				if reflect.DeepEqual(got, want) {
					counts[i]++
				}
			}
		}
		if responses != 2 || counts[0] != 1 || counts[1] != 1 {
			t.Fatalf("wire answer counts %d/%v", responses, counts)
		}
		t.Logf("A=%s B=%s Q1=%s Q2=%s crossActor=REQUEST_CONFLICT decisions=2 exactAttempts=2 wire=yes:1,no:1", f.session, actorB, q1.ID, q2.ID)
		if root := os.Getenv("DEVBRIDGE_ACTOR_ISOLATION_EVIDENCE"); root != "" {
			if err := os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			raw, err := json.MarshalIndent(map[string]any{"actorA": f.session, "actorB": actorB, "preparedA": choiceA, "preparedB": choiceB, "conflict": conflict, "decisionA": accepted.Decision, "decisionB": second.Decision, "consumedQ1": consumed1, "consumedQ2": consumed2, "attempts": attempts, "protocolA": a.session.InitializeResult(), "protocolB": b.session.InitializeResult()}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "result.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "agent-wire.jsonl"), finalWire, 0600); err != nil {
				t.Fatal(err)
			}
		}
		return false
	}, nil)
}
