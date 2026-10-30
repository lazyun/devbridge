package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

func TestClientBindsExactRuntimeRootsAcrossThreadResumeAndTurns(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	binding := RuntimeBinding{
		RuntimeWorkspaceRoots: []string{"/fixture/app", "/fixture/sdk"},
		WritableRoots:         []string{"/fixture/app"},
	}

	started := make(chan error, 1)
	go func() {
		_, err := client.StartThread(context.Background(), ThreadOptions{
			CWD: "/fixture/app", RuntimeBinding: binding, Model: "gpt-5.6-terra", ReasoningEffort: "xhigh", ServiceTier: "priority",
		})
		started <- err
	}()
	request := readClientRequest(t, requests)
	assertBoundThreadRequest(t, request, "thread/start", "/fixture/app", binding, "workspace-write")
	var start wire.ThreadStartParams
	if err := json.Unmarshal(request["params"], &start); err != nil {
		t.Fatal(err)
	}
	if start.Model != "gpt-5.6-terra" || start.ModelProvider != "openai" || start.Config["model_reasoning_effort"] != "xhigh" || start.Config["service_tier"] != "priority" {
		t.Fatalf("thread/start selection changed: %+v", start)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "bound-thread"}, "cwd": "/fixture/app"}})
	if err := <-started; err != nil {
		t.Fatal(err)
	}

	resumed := make(chan error, 1)
	go func() {
		_, err := client.ResumeThread(context.Background(), ThreadResumeOptions{
			ThreadID: "bound-thread", CWD: "/fixture/app", RuntimeBinding: binding, Model: "gpt-5.6-terra", ReasoningEffort: "xhigh", ServiceTier: "priority",
		})
		resumed <- err
	}()
	request = readClientRequest(t, requests)
	assertBoundThreadRequest(t, request, "thread/resume", "/fixture/app", binding, "workspace-write")
	var resume wire.ThreadResumeParams
	if err := json.Unmarshal(request["params"], &resume); err != nil {
		t.Fatal(err)
	}
	if resume.Model != "gpt-5.6-terra" || resume.ModelProvider != "openai" || resume.Config["model_reasoning_effort"] != "xhigh" || resume.ServiceTier != "priority" {
		t.Fatalf("thread/resume selection changed: %+v", resume)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "bound-thread"}, "cwd": "/fixture/app"}})
	if err := <-resumed; err != nil {
		t.Fatal(err)
	}

	schema := json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`)
	prompt := "  exact prompt bytes\n"
	tier := "priority"
	turn := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "bound-thread", Prompt: prompt, OutputSchema: schema, Model: "gpt-5.6-terra", ReasoningEffort: "xhigh", ServiceTier: &tier, AuthorizationCeiling: AuthorizationWorkspaceWrite, RuntimeBinding: binding})
		turn <- err
	}()
	request = readClientRequest(t, requests)
	assertBoundTurnRequest(t, request, binding, "workspaceWrite", []string{"/fixture/app"}, prompt, schema)
	finishBoundTurn(t, responses, request, "bound-thread", "write-turn")
	if err := <-turn; err != nil {
		t.Fatal(err)
	}

	readOnly := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "bound-thread", Prompt: "review", AuthorizationCeiling: AuthorizationReadOnly, RuntimeBinding: binding})
		readOnly <- err
	}()
	request = readClientRequest(t, requests)
	assertBoundTurnRequest(t, request, binding, "readOnly", nil, "review", nil)
	finishBoundTurn(t, responses, request, "bound-thread", "read-turn")
	if err := <-readOnly; err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeBindingRejectsRootDriftBeforeWireDispatch(t *testing.T) {
	client, _, _ := newClientFixture(t)
	binding := RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/app", "/fixture/sdk"}, WritableRoots: []string{"/fixture/app"}}
	if _, err := client.StartThread(context.Background(), ThreadOptions{CWD: "/fixture/other", RuntimeBinding: binding}); err == nil {
		t.Fatal("accepted cwd outside frozen roots")
	}
	if _, err := client.StartThread(context.Background(), ThreadOptions{CWD: "/fixture/app", RuntimeBinding: RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/app"}, WritableRoots: []string{"/fixture/other"}}}); err == nil {
		t.Fatal("accepted writable root outside frozen roots")
	}
	client.runtimeMu.Lock()
	client.threadBindings = map[string]RuntimeBinding{"bound-thread": cloneRuntimeBinding(binding)}
	client.runtimeMu.Unlock()
	if _, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "bound-thread", Prompt: "continue", RuntimeBinding: RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/app", "/fixture/third"}, WritableRoots: []string{"/fixture/app"}}}); err == nil {
		t.Fatal("accepted turn roots that differ from frozen thread binding")
	}
}

func assertBoundThreadRequest(t *testing.T, request map[string]json.RawMessage, method, cwd string, binding RuntimeBinding, sandbox string) {
	t.Helper()
	var gotMethod string
	if err := json.Unmarshal(request["method"], &gotMethod); err != nil || gotMethod != method {
		t.Fatalf("method=%q err=%v", gotMethod, err)
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	assertNoThirdRoot(t, params, binding.RuntimeWorkspaceRoots)
	if string(params["cwd"]) != `"`+cwd+`"` || string(params["sandbox"]) != `"`+sandbox+`"` {
		t.Fatalf("thread binding params=%s", request["params"])
	}
	var roots []string
	if err := json.Unmarshal(params["runtimeWorkspaceRoots"], &roots); err != nil || !sameRoots(roots, binding.RuntimeWorkspaceRoots) {
		t.Fatalf("runtime roots=%v err=%v", roots, err)
	}
}

func assertBoundTurnRequest(t *testing.T, request map[string]json.RawMessage, binding RuntimeBinding, policy string, writable []string, prompt string, schema json.RawMessage) {
	t.Helper()
	var params wire.TurnStartParams
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if len(params.Input) != 1 || params.Input[0].Text != prompt || !bytes.Equal(params.OutputSchema, schema) || !sameRoots(params.RuntimeWorkspaceRoots, binding.RuntimeWorkspaceRoots) {
		t.Fatalf("turn input/binding changed: %+v", params)
	}
	if params.SandboxPolicy == nil || params.SandboxPolicy.Type != policy || !sameRoots(params.SandboxPolicy.WritableRoots, writable) {
		t.Fatalf("turn policy=%+v", params.SandboxPolicy)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &raw); err != nil {
		t.Fatal(err)
	}
	assertNoThirdRoot(t, raw, binding.RuntimeWorkspaceRoots)
}

func assertNoThirdRoot(t *testing.T, params map[string]json.RawMessage, roots []string) {
	t.Helper()
	if _, exists := params["permissions"]; exists || bytes.Contains(params["runtimeWorkspaceRoots"], []byte("third")) || bytes.Contains(params["sandboxPolicy"], []byte("third")) {
		t.Fatalf("unexpected authority field: %s", mustJSON(t, params))
	}
	for _, root := range roots {
		if !bytes.Contains(params["runtimeWorkspaceRoots"], []byte(root)) {
			t.Fatalf("missing root %q in %s", root, params["runtimeWorkspaceRoots"])
		}
	}
}

func finishBoundTurn(t *testing.T, responses interface{ Write([]byte) (int, error) }, request map[string]json.RawMessage, threadID, turnID string) {
	t.Helper()
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}})
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": "completed", "items": []any{}}}})
}

func sameRoots(left, right []string) bool {
	return sameRuntimeBinding(RuntimeBinding{RuntimeWorkspaceRoots: left}, RuntimeBinding{RuntimeWorkspaceRoots: right})
}
