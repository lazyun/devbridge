package codex

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartThreadSerializesAndRegistersDynamicTools(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	client.dynamicTools = newDynamicToolBroker(func(context.Context, DynamicToolCallParams) (DynamicToolCallResponse, error) {
		return DynamicToolCallResponse{}, nil
	})
	spec := DynamicToolSpec{
		Type: "function", Name: "review_context", Description: "Read trusted review context",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`), DeferLoading: true,
	}
	done := make(chan struct {
		thread Thread
		err    error
	}, 1)
	go func() {
		thread, err := client.StartThread(context.Background(), ThreadOptions{CWD: "/bridge/workspace", DynamicTools: []DynamicToolSpec{spec}})
		done <- struct {
			thread Thread
			err    error
		}{thread, err}
	}()
	request := readClientRequest(t, requests)
	var params struct {
		CWD          string            `json:"cwd"`
		DynamicTools []DynamicToolSpec `json:"dynamicTools"`
	}
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params.CWD != "/bridge/workspace" || len(params.DynamicTools) != 1 || params.DynamicTools[0].Type != "function" || params.DynamicTools[0].Name != "review_context" || !params.DynamicTools[0].DeferLoading || string(params.DynamicTools[0].InputSchema) != string(spec.InputSchema) {
		t.Fatalf("thread/start params = %#v", params)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "thread-dynamic"}, "cwd": "/bridge/workspace"}})
	if got := <-done; got.err != nil || got.thread.ID != "thread-dynamic" {
		t.Fatalf("start=%+v err=%v", got.thread, got.err)
	}
	if !client.dynamicTools.registered(DynamicToolCallParams{ThreadID: "thread-dynamic", Tool: "review_context"}) {
		t.Fatal("successful thread start did not register dynamic tool")
	}
}

func TestDynamicToolsOptIntoPinnedExperimentalAPI(t *testing.T) {
	withoutTools := initializeParams(Config{})
	if withoutTools.Capabilities != nil {
		t.Fatalf("ordinary initialize unexpectedly opts into capabilities: %+v", withoutTools.Capabilities)
	}
	withTools := initializeParams(Config{DynamicToolHandler: func(context.Context, DynamicToolCallParams) (DynamicToolCallResponse, error) {
		return DynamicToolCallResponse{}, nil
	}})
	if withTools.Capabilities == nil || !withTools.Capabilities.ExperimentalAPI {
		t.Fatalf("dynamic-tool initialize capabilities=%+v", withTools.Capabilities)
	}
	encoded, err := json.Marshal(withTools)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"clientInfo":{"name":"devbridge","title":"Dev Bridge","version":"0.1.0"},"capabilities":{"experimentalApi":true}}` {
		t.Fatalf("initialize params=%s", encoded)
	}
}

func TestDynamicToolCallDeliversTrustedParamsAndExactResponse(t *testing.T) {
	called := make(chan DynamicToolCallParams, 1)
	broker := newDynamicToolBroker(func(_ context.Context, params DynamicToolCallParams) (DynamicToolCallResponse, error) {
		called <- params
		return DynamicToolCallResponse{Success: true, ContentItems: []DynamicToolCallOutputContentItem{{Type: "inputText", Text: "trusted result"}}}, nil
	})
	broker.registerThread("thread-1", []DynamicToolSpec{{Type: "function", Name: "review_context", InputSchema: json.RawMessage(`true`)}})
	fixture := newRPCFixture(t, func(ctx context.Context, request ServerRequest) (any, error) {
		return broker.process(ctx, request, builtInServerRequestPolicy)
	}, nil)
	fixture.write(t, map[string]any{"id": 7, "method": "item/tool/call", "params": map[string]any{
		"callId": "call-1", "threadId": "thread-1", "turnId": "turn-1", "tool": "review_context", "arguments": map[string]any{"focus": "security"},
	}})
	select {
	case got := <-called:
		if got.CallID != "call-1" || got.ThreadID != "thread-1" || got.TurnID != "turn-1" || got.Tool != "review_context" || string(got.Arguments) != `{"focus":"security"}` || got.Namespace != nil {
			t.Fatalf("handler params=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("dynamic tool handler was not called")
	}
	response := fixture.readRequest(t)
	if got := compactJSON(t, response["result"]); got != `{"contentItems":[{"text":"trusted result","type":"inputText"}],"success":true}` {
		t.Fatalf("response=%s", got)
	}
}

func TestResumeThreadLocallyRegistersDynamicToolsWithoutSerializingThem(t *testing.T) {
	called := make(chan DynamicToolCallParams, 1)
	broker := newDynamicToolBroker(func(_ context.Context, params DynamicToolCallParams) (DynamicToolCallResponse, error) {
		called <- params
		return DynamicToolCallResponse{Success: true, ContentItems: []DynamicToolCallOutputContentItem{{Type: "inputText", Text: "resumed trusted result"}}}, nil
	})
	fixture := newRPCFixture(t, func(ctx context.Context, request ServerRequest) (any, error) {
		return broker.process(ctx, request, builtInServerRequestPolicy)
	}, nil)
	client := &Client{rpc: fixture.transport, turns: make(map[turnKey]*turnState), dynamicTools: broker}
	spec := DynamicToolSpec{Type: "function", Name: "review_context", Description: "Read trusted review context", InputSchema: json.RawMessage(`true`)}
	resumed := make(chan error, 1)
	go func() {
		_, err := client.ResumeThread(context.Background(), ThreadResumeOptions{ThreadID: "thread-resumed", CWD: "/bridge/workspace", DynamicTools: []DynamicToolSpec{spec}})
		resumed <- err
	}()
	request := fixture.readRequest(t)
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if _, present := params["dynamicTools"]; present {
		t.Fatal("thread/resume params must not send dynamicTools")
	}
	fixture.write(t, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "thread-resumed"}, "cwd": "/bridge/workspace"}})
	if err := <-resumed; err != nil {
		t.Fatal(err)
	}

	fixture.write(t, map[string]any{"id": "dynamic-after-resume", "method": "item/tool/call", "params": map[string]any{
		"callId": "call-resumed", "threadId": "thread-resumed", "turnId": "turn-resumed", "tool": "review_context", "arguments": map[string]any{"focus": "correctness"},
	}})
	select {
	case got := <-called:
		if got.ThreadID != "thread-resumed" || got.TurnID != "turn-resumed" || got.Tool != "review_context" {
			t.Fatalf("handler params=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("resumed thread dynamic tool handler was not called")
	}
	response := fixture.readRequest(t)
	if got := compactJSON(t, response["result"]); got != `{"contentItems":[{"text":"resumed trusted result","type":"inputText"}],"success":true}` {
		t.Fatalf("response=%s", got)
	}
}

func TestDynamicToolCallRejectsMalformedUnknownAndNamespacedCalls(t *testing.T) {
	var calls atomic.Int32
	broker := newDynamicToolBroker(func(context.Context, DynamicToolCallParams) (DynamicToolCallResponse, error) {
		calls.Add(1)
		return DynamicToolCallResponse{Success: true}, nil
	})
	broker.registerThread("thread-1", []DynamicToolSpec{{Type: "function", Name: "registered", InputSchema: json.RawMessage(`true`)}})
	fixture := newRPCFixture(t, func(ctx context.Context, request ServerRequest) (any, error) {
		return broker.process(ctx, request, builtInServerRequestPolicy)
	}, nil)
	for id, params := range map[string]any{
		"malformed":      map[string]any{"callId": "call", "threadId": "thread-1", "turnId": "turn", "tool": "registered"},
		"unknown-thread": map[string]any{"callId": "call", "threadId": "other", "turnId": "turn", "tool": "registered", "arguments": map[string]any{}},
		"unknown-tool":   map[string]any{"callId": "call", "threadId": "thread-1", "turnId": "turn", "tool": "other", "arguments": map[string]any{}},
		"namespaced":     map[string]any{"callId": "call", "threadId": "thread-1", "turnId": "turn", "tool": "registered", "namespace": "other", "arguments": map[string]any{}},
	} {
		fixture.write(t, map[string]any{"id": id, "method": "item/tool/call", "params": params})
		response := fixture.readRequest(t)
		if got := compactJSON(t, response["result"]); got != `{"contentItems":[{"text":"dynamic tool call was not accepted","type":"inputText"}],"success":false}` {
			t.Fatalf("%s response=%s", id, got)
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("handler calls=%d, want 0", got)
	}
}

func TestDynamicToolProcessorComposesWithInteractiveApproval(t *testing.T) {
	approvals := newApprovalBroker()
	broker := newDynamicToolBroker(func(context.Context, DynamicToolCallParams) (DynamicToolCallResponse, error) {
		return DynamicToolCallResponse{Success: true}, nil
	})
	fixture := newRPCFixture(t, func(ctx context.Context, request ServerRequest) (any, error) {
		return broker.process(ctx, request, approvals.process)
	}, nil)
	fixture.write(t, map[string]any{"id": "approval", "method": "item/fileChange/requestApproval", "params": modernApprovalParams()})
	approval, err := approvals.next(context.Background())
	if err != nil || approval.Kind != ApprovalKindFileChange {
		t.Fatalf("approval=%+v err=%v", approval, err)
	}
	resolved := make(chan error, 1)
	go func() {
		resolved <- approvals.resolve(context.Background(), approval.ID, ApprovalDecision{Type: DecisionDecline})
	}()
	response := fixture.readRequest(t)
	if got := compactJSON(t, response["result"]); got != `{"decision":"decline"}` {
		t.Fatalf("approval response=%s", got)
	}
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
}
