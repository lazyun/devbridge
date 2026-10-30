package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestUserDecisionRuntimeOutputClosedSchema(t *testing.T) {
	request := runtimePayloadRequest()
	request.ID = "runtime-request"
	request.Kind = "CODEX_REQUEST"
	request.OperationKind = "EXECUTION"
	request.OperationID = "execution"
	request.RuntimeEpoch = "epoch"
	request.State = "UNKNOWN"
	request.ResponseSchema = json.RawMessage(`{"type":"object"}`)
	request.UserDecision = &agentruntime.UserDecision{ID: "decision", UserDecisionInput: agentruntime.UserDecisionInput{Identity: request.Identity, RuntimeRequestID: request.ID, SubmissionRequestID: "submission", Action: "RESPOND", RawText: "不要改写 <是>", SourceSurface: "CHATGPT", SubmittedBy: "session:session"}}
	snapshot := agentruntime.Snapshot{Identity: request.Identity, Requests: []agentruntime.Request{request}}
	if err := resolveSchema(t, json.RawMessage(agentRuntimeOutputSchema)).Validate(jsonValue(t, snapshot)); err != nil {
		t.Fatal(err)
	}
	value := jsonValue(t, snapshot)
	value["unrecognizedAuthority"] = "not allowed"
	if err := resolveSchema(t, json.RawMessage(agentRuntimeOutputSchema)).Validate(value); err == nil {
		t.Fatal("schema accepted invented authority")
	}
}
func TestUserDecisionMCPRejectsRawResponseAndActor(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(agentRequestInputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolution := schema["properties"].(map[string]any)["resolution"].(map[string]any)
	if _, ok := resolution["properties"].(map[string]any)["response"]; ok {
		t.Fatal("raw answer remains writable")
	}
	if err := json.Unmarshal([]byte(AgentUserDecisionInputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	decision := schema["properties"].(map[string]any)["decision"].(map[string]any)
	if _, ok := decision["properties"].(map[string]any)["submittedBy"]; ok {
		t.Fatal("caller can impersonate actor")
	}
	for _, phrase := range []string{"explicit user answer", "Do not infer", "Preserve exact user response", "do not call", "latest pending request is never authority"} {
		if !strings.Contains(ExplicitUserDecisionDescription, phrase) {
			t.Fatal(phrase)
		}
	}
}

type userDecisionWakeFixture struct {
	*surfaceReadFixture
	state agentruntime.Snapshot
	reads int
}

func (f *userDecisionWakeFixture) AgentRuntimeStateForSession(_ context.Context, session, runID string) (agentruntime.Snapshot, error) {
	f.reads++
	if session != "session" || runID != f.state.RunID {
		return agentruntime.Snapshot{}, errors.New("scope mismatch")
	}
	return f.state, nil
}
func (f *userDecisionWakeFixture) ResolveAgentRequestForSession(context.Context, string, agentruntime.Resolution) (agentruntime.Snapshot, error) {
	panic("read notification must never respond")
}
func TestUserDecisionWakeupReadsExactPersistedQuestion(t *testing.T) {
	request := runtimePayloadRequest()
	request.ID = "question"
	request.Kind = "CODEX_REQUEST"
	request.RuntimeEpoch = "epoch"
	request.State = "WAITING_FOR_USER"
	f := &userDecisionWakeFixture{surfaceReadFixture: newSurfaceReadFixture(), state: agentruntime.Snapshot{Identity: request.Identity, Requests: []agentruntime.Request{request}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "question-wakeup", Version: "1"}, nil)
	registerRuntimeRequestWakeupClaim(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	call := func(id string) (runtimeRequestWakeupResult, bool) {
		result, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "dev_bridge_runtime_request_wakeup_claim", Arguments: runtimeRequestWakeupIdentity{AISessionID: "session", RunID: request.RunID, RuntimeRequestID: id, RuntimeEpoch: "epoch"}})
		if err != nil {
			t.Fatal(err)
		}
		var out runtimeRequestWakeupResult
		if !result.IsError {
			b, _ := json.Marshal(result.StructuredContent)
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
		}
		return out, result.IsError
	}
	if _, failed := call("latest"); !failed {
		t.Fatal("guessed request accepted")
	}
	first, failed := call(request.ID)
	if failed || first.Status != "CLAIMED" || !strings.Contains(first.Prompt, request.ID) || first.DeliveryGuarantee != "UNCONFIRMED" {
		t.Fatalf("%+v failed=%v", first, failed)
	}
	second, failed := call(request.ID)
	if failed || second.Status != "ALREADY_ATTEMPTED" {
		t.Fatalf("%+v", second)
	}
	f.state.Requests[0].State = "UNKNOWN"
	if _, failed := call(request.ID); !failed {
		t.Fatal("answered unknown question shown as unanswered")
	}
	if f.reads != 4 {
		t.Fatalf("reads=%d", f.reads)
	}
}

func TestUserDecisionPrepareHashIsExactPureRead(t *testing.T) {
	request := runtimePayloadRequest()
	request.ID = "question"
	request.Kind = "CODEX_REQUEST"
	request.RuntimeEpoch = "epoch"
	request.State = "WAITING_FOR_USER"
	f := &userDecisionWakeFixture{surfaceReadFixture: newSurfaceReadFixture(), state: agentruntime.Snapshot{Identity: request.Identity, Requests: []agentruntime.Request{request}}}
	in := AgentUserDecisionInput{AISessionID: "session", Decision: agentruntime.UserDecisionInput{Identity: request.Identity, RuntimeRequestID: request.ID, SubmissionRequestID: "submission", ExpectedState: request.State, Action: "RESPOND", RawText: "  否 <>&\u2028\n", SourceSurface: "CHATGPT"}}
	out, err := AgentUserDecisionPrepareHashCore(context.Background(), f, in)
	if err != nil || out.Recorded || out.Decision.RawText != in.Decision.RawText || out.Decision.PayloadSHA256 != agentruntime.DecisionPayloadSHA256(in.Decision) {
		t.Fatalf("%+v %v", out, err)
	}
	if f.state.Requests[0].UserDecision != nil || f.state.Requests[0].State != "WAITING_FOR_USER" {
		t.Fatal("hash read changed request")
	}
	in.Decision.RuntimeRequestID = "latest"
	if _, err = AgentUserDecisionPrepareHashCore(context.Background(), f, in); err == nil {
		t.Fatal("guessed request prepared")
	}
}

func TestUserDecisionPresentationHintOnlyForExactUnansweredQuestion(t *testing.T) {
	request := runtimePayloadRequest()
	request.ID = "question"
	request.Kind = "CODEX_REQUEST"
	request.RuntimeEpoch = "epoch"
	request.OperationKind = "EXECUTION"
	request.OperationID = "execution"
	request.AgentConversationID = "conversation"
	request.ExecutionID = "execution"
	request.TaskDefinitionRevisionID = "revision"
	request.State = "WAITING_FOR_USER"
	ctx := SurfaceContext{AISessionID: "session", ProjectID: request.ProjectID, FeatureAreaID: request.FeatureAreaID, TaskID: request.TaskID, Target: &p.OperationTarget{Kind: "EXECUTION", OperationID: "execution", RunID: request.RunID}}
	pending := p.PendingDecision{Kind: "CODEX_REQUEST", TaskID: request.TaskID, FeatureAreaID: request.FeatureAreaID, RuntimeRequest: &request, Options: []p.Capability{{Available: true, Target: &p.ActionTarget{RunID: request.RunID, SubjectID: request.ID}}}}
	hint := surfaceHintFor(ctx, SurfaceWorkflow{PendingDecisions: []p.PendingDecision{pending}}, false)
	if hint.Required || hint.Reason != "PENDING_RUNTIME_REQUEST" || hint.Arguments == nil || hint.Arguments.ActionTarget.SubjectID != request.ID {
		t.Fatalf("exact optional hint %+v", hint)
	}
	for _, state := range []string{"USER_DECISION_RECORDED", "RESPONDING", "UNKNOWN", "RESPONDED", "CANCELED", "STALE"} {
		request.State = state
		hint = surfaceHintFor(ctx, SurfaceWorkflow{PendingDecisions: []p.PendingDecision{pending}}, false)
		if hint.Required || hint.Reason != "NO_PENDING_INTERACTION" {
			t.Fatalf("%s prompted answered question %+v", state, hint)
		}
	}
	request.State = "WAITING_FOR_USER"
	request.ID = ""
	if got := surfaceHintFor(ctx, SurfaceWorkflow{PendingDecisions: []p.PendingDecision{pending}}, false); got.Reason != "NO_PENDING_INTERACTION" {
		t.Fatal("legacy incomplete request became actionable")
	}
}

func TestUnknownDispositionRecoveryClosedContract(t *testing.T) {
	request := runtimePayloadRequest()
	request.ID = "request"
	request.Kind = "CODEX_REQUEST"
	request.State = "RESPONDING"
	request.Delivery = "UNKNOWN"
	request.UserDecision = &agentruntime.UserDecision{ID: "decision", UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND", RawText: "不可改写的答案"}}
	snapshot := agentruntime.Snapshot{Identity: request.Identity, Requests: []agentruntime.Request{request}}
	agentruntime.ApplyRequestProjections(&snapshot)
	if err := resolveSchema(t, json.RawMessage(agentRuntimeOutputSchema)).Validate(jsonValue(t, snapshot)); err != nil {
		t.Fatal(err)
	}
	projection := snapshot.Requests[0].Projection
	if projection.ResponseDisposition != "UNKNOWN" || projection.ResendAllowed || len(projection.AvailableActions) != 3 || projection.UserDecisionSummary != "不可改写的答案" {
		t.Fatalf("projection=%+v", projection)
	}
	input := map[string]any{"aiSessionId": "session", "recovery": map[string]any{"projectId": "project", "featureAreaId": "area", "taskId": "task", "runId": "run", "threadId": "thread", "turnId": "turn", "connectionId": "connection", "runtimeRequestId": "request", "commandRequestId": "command", "action": "CONTINUE_WAITING"}}
	schema := resolveSchema(t, json.RawMessage(AgentRequestRecoveryInputSchema))
	if err := schema.Validate(input); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"force", "retry", "assumeNotConsumed"} {
		input["recovery"].(map[string]any)[flag] = true
		if err := schema.Validate(input); err == nil {
			t.Fatalf("accepted %s", flag)
		}
		delete(input["recovery"].(map[string]any), flag)
	}
	input["recovery"].(map[string]any)["action"] = "RESEND"
	if err := schema.Validate(input); err == nil {
		t.Fatal("accepted resend")
	}
	if !strings.Contains(RuntimeUserDecisionInstructions, "你的回答已经记录。Codex 可能已经收到该回答，但当前协议无法确认。Dev Bridge 不会自动重发。") {
		t.Fatal("mandatory explanation missing")
	}
}
