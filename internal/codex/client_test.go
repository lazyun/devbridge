package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex/wire"
	"github.com/lazyun/devbridge/internal/codexusage"
)

func TestStartTurnWaitsForMatchingCompletion(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	client := &Client{turns: make(map[turnKey]*turnState)}
	client.rpc = newRPCTransport(stdoutReader, stdinWriter, client.handleEvent)
	t.Cleanup(func() {
		_ = client.rpc.Close()
		_ = stdinReader.Close()
		_ = stdoutWriter.Close()
	})
	requests := bufio.NewReader(stdinReader)

	type result struct {
		turn TurnResult
		err  error
	}
	aResult := make(chan result, 1)
	bResult := make(chan result, 1)
	go func() {
		turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-A", Prompt: "A"})
		aResult <- result{turn, err}
	}()
	go func() {
		turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-B", Prompt: "B"})
		bResult <- result{turn, err}
	}()

	for i := 0; i < 2; i++ {
		line, err := requests.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Params struct {
				ThreadID string `json:"threadId"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &request); err != nil {
			t.Fatal(err)
		}
		turnID := "turn-A"
		if request.Params.ThreadID == "thread-B" {
			turnID = "turn-B"
		}
		writeServerJSON(t, stdoutWriter, map[string]any{
			"id":     request.ID,
			"result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}},
		})
	}

	writeAgentCompletion(t, stdoutWriter, "thread-B", "turn-B", "message B")
	select {
	case got := <-bResult:
		if got.err != nil || got.turn.FinalMessage != "message B" || got.turn.ThreadID != "thread-B" {
			t.Fatalf("B result = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("B did not complete")
	}
	select {
	case got := <-aResult:
		t.Fatalf("A completed from B's event: %#v", got)
	default:
	}

	writeAgentCompletion(t, stdoutWriter, "thread-A", "turn-A", "message A")
	select {
	case got := <-aResult:
		if got.err != nil || got.turn.FinalMessage != "message A" || got.turn.ThreadID != "thread-A" {
			t.Fatalf("A result = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("A did not complete")
	}
}

func TestStartTurnSendsOutputSchemaOutOfBandWithoutChangingPrompt(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	schema := json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"}},"required":["verdict"],"additionalProperties":false}`)
	prompt := "  Exact authored prompt.  "
	result := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-schema", Prompt: prompt, OutputSchema: schema})
		result <- err
	}()
	request := readClientRequest(t, requests)
	var params wire.TurnStartParams
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "thread-schema" || len(params.Input) != 1 || params.Input[0].Text != prompt || !bytes.Equal(params.OutputSchema, schema) {
		t.Fatalf("turn/start params changed prompt or schema: %+v", params)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-schema", "status": "inProgress", "items": []any{}}}})
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-schema", "turn": map[string]any{"id": "turn-schema", "status": "completed", "items": []any{}}}})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestStartTurnRejectsNonStrictSchemaBeforeWireDispatch(t *testing.T) {
	client, requests, _ := newClientFixture(t)
	invalid := json.RawMessage(`{"type":"object","properties":{"targets":{"type":"array","items":{"type":"object","properties":{},"required":[]}}},"required":["targets"],"additionalProperties":false}`)
	_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-invalid-schema", Prompt: "must not dispatch", OutputSchema: invalid})
	if err == nil || !strings.Contains(err.Error(), "additionalProperties=false") {
		t.Fatalf("StartTurn() error = %v", err)
	}
	dispatched := make(chan struct{}, 1)
	go func() {
		if _, readErr := requests.ReadBytes('\n'); readErr == nil {
			dispatched <- struct{}{}
		}
	}()
	select {
	case <-dispatched:
		t.Fatal("invalid schema reached turn/start")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStartTurnCapturesLatestCumulativeTokenUsage(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	client.mu.Lock()
	client.threadUsage = map[string]threadUsageState{"thread-usage": {ledger: codexusage.CodexUsageLedger{ThreadID: "thread-usage", AccountingEpoch: 1, PreviousRawCumulative: tokenCounts(tokenBreakdown(100, 20, 2, 10, 5, 110)), LogicalCumulative: tokenCounts(tokenBreakdown(100, 20, 2, 10, 5, 110)), Anchored: true}}}
	client.mu.Unlock()
	resultChannel := make(chan struct {
		value TurnResult
		err   error
	}, 1)
	go func() {
		value, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-usage", Prompt: "usage"})
		resultChannel <- struct {
			value TurnResult
			err   error
		}{value, err}
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-usage", "status": "inProgress", "items": []any{}}}})
	window := int64(200000)
	writeTokenUsage(t, responses, "thread-usage", "turn-usage", tokenBreakdown(120, 25, 3, 15, 7, 135), tokenBreakdown(20, 5, 1, 5, 2, 25), &window)
	writeTokenUsage(t, responses, "thread-usage", "turn-usage", tokenBreakdown(140, 30, 4, 25, 9, 165), tokenBreakdown(40, 10, 2, 15, 4, 55), &window)
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-usage", "turn": map[string]any{"id": "turn-usage", "status": "completed", "items": []any{}}}})
	got := <-resultChannel
	if got.err != nil || got.value.TokenUsage == nil || !got.value.TokenUsage.Reliable {
		t.Fatalf("result=%+v err=%v", got.value, got.err)
	}
	if got.value.TokenUsage.StartCumulative.TotalTokens != 110 || got.value.TokenUsage.EndCumulative.TotalTokens != 165 || got.value.TokenUsage.ModelContextWindow == nil || *got.value.TokenUsage.ModelContextWindow != window {
		t.Fatalf("usage=%+v", got.value.TokenUsage)
	}
}

func TestResumeThreadUsesPinnedProtocolAndSeedsNextTurnUsage(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	resumeResult := make(chan struct {
		thread Thread
		err    error
	}, 1)
	go func() {
		thread, err := client.ResumeThread(context.Background(), ThreadResumeOptions{
			ThreadID: "thread-durable", CWD: "/bridge/workspaces/task-one",
			UsageBaseline: &TokenCounts{InputTokens: 80, OutputTokens: 20, TotalTokens: 100},
		})
		resumeResult <- struct {
			thread Thread
			err    error
		}{thread, err}
	}()
	request := readClientRequest(t, requests)
	var rawParams map[string]json.RawMessage
	if err := json.Unmarshal(request["params"], &rawParams); err != nil {
		t.Fatal(err)
	}
	if _, present := rawParams["dynamicTools"]; present {
		t.Fatal("thread/resume params must not send dynamicTools")
	}
	var method string
	if err := json.Unmarshal(request["method"], &method); err != nil || method != "thread/resume" {
		t.Fatalf("resume method=%q err=%v", method, err)
	}
	var params wire.ThreadResumeParams
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "thread-durable" || params.CWD != "/bridge/workspaces/task-one" {
		t.Fatalf("resume params=%+v", params)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{
		"thread": map[string]any{"id": "thread-durable"}, "cwd": "/bridge/workspaces/task-one",
	}})
	if got := <-resumeResult; got.err != nil || got.thread.ID != "thread-durable" {
		t.Fatalf("resume=%+v err=%v", got.thread, got.err)
	}

	turnResult := make(chan struct {
		turn TurnResult
		err  error
	}, 1)
	go func() {
		turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-durable", Prompt: "continue"})
		turnResult <- struct {
			turn TurnResult
			err  error
		}{turn, err}
	}()
	request = readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-two", "status": "inProgress", "items": []any{}}}})
	writeTokenUsage(t, responses, "thread-durable", "turn-two", tokenBreakdown(120, 0, 0, 30, 0, 150), tokenBreakdown(40, 0, 0, 10, 0, 50), nil)
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-durable", "turn": map[string]any{"id": "turn-two", "status": "completed", "items": []any{}}}})
	got := <-turnResult
	if got.err != nil || got.turn.ThreadID != "thread-durable" || got.turn.TurnID != "turn-two" {
		t.Fatalf("turn=%+v err=%v", got.turn, got.err)
	}
	if got.turn.TokenUsage == nil || !got.turn.TokenUsage.Reliable || got.turn.TokenUsage.StartCumulative.TotalTokens != 100 || got.turn.TokenUsage.EndCumulative.TotalTokens != 150 {
		t.Fatalf("usage=%+v", got.turn.TokenUsage)
	}
}

func TestStartThreadForwardsFrozenModelAndReasoningEffort(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	done := make(chan struct {
		thread Thread
		err    error
	}, 1)
	go func() {
		thread, err := client.StartThread(context.Background(), ThreadOptions{
			CWD: "/bridge/workspaces/task-one", Model: "gpt-5.6-terra", ReasoningEffort: "high", ServiceTier: "priority",
		})
		done <- struct {
			thread Thread
			err    error
		}{thread, err}
	}()
	request := readClientRequest(t, requests)
	var params wire.ThreadStartParams
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params.Model != "gpt-5.6-terra" || params.ModelProvider != "openai" {
		t.Fatalf("thread/start params=%+v", params)
	}
	if params.Config == nil || params.Config["model_reasoning_effort"] != "high" || params.Config["service_tier"] != "priority" {
		t.Fatalf("thread/start runtime config=%+v", params.Config)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "thread-selection"}, "cwd": "/bridge/workspaces/task-one"}})
	got := <-done
	if got.err != nil || got.thread.ID != "thread-selection" {
		t.Fatalf("thread=%+v err=%v", got.thread, got.err)
	}
}

func TestStartTurnForwardsFrozenModelAndReasoningEffort(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	done := make(chan struct {
		turn TurnResult
		err  error
	}, 1)
	tier := "priority"
	go func() {
		turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-selection", Prompt: "continue", Model: "gpt-5.6-sol", ReasoningEffort: "xhigh", ServiceTier: &tier})
		done <- struct {
			turn TurnResult
			err  error
		}{turn, err}
	}()
	request := readClientRequest(t, requests)
	var params wire.TurnStartParams
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params.Model != "gpt-5.6-sol" || params.Effort != "xhigh" || params.ServiceTier == nil || *params.ServiceTier != "priority" {
		t.Fatalf("turn/start params=%+v", params)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-one", "status": "inProgress", "items": []any{}}}})
	writeTokenUsage(t, responses, "thread-selection", "turn-one", tokenBreakdown(0, 0, 0, 0, 0, 0), tokenBreakdown(0, 0, 0, 0, 0, 0), nil)
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-selection", "turn": map[string]any{"id": "turn-one", "status": "completed", "items": []any{}}}})
	got := <-done
	if got.err != nil || got.turn.TurnID != "turn-one" {
		t.Fatalf("turn=%+v err=%v", got.turn, got.err)
	}
}

func TestTurnStartServiceTierOffIsExplicitNull(t *testing.T) {
	encoded, err := json.Marshal(wire.TurnStartParams{ThreadID: "resumed-thread", Input: []wire.UserInput{{Type: "text", Text: "continue"}}, ServiceTier: nil})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	value, ok := raw["serviceTier"]
	if !ok || string(value) != "null" {
		t.Fatalf("off tier not explicitly cleared: %s", encoded)
	}
}

func TestStartTurnRebindsOperationSandboxPolicy(t *testing.T) {
	for _, test := range []struct {
		name    string
		ceiling AuthorizationCeiling
		want    string
	}{
		{name: "review read only", ceiling: AuthorizationReadOnly, want: "readOnly"},
		{name: "verification read only", ceiling: AuthorizationReadOnly, want: "readOnly"},
		{name: "execution workspace write", ceiling: AuthorizationWorkspaceWrite, want: "workspaceWrite"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, requests, responses := newClientFixture(t)
			done := make(chan error, 1)
			go func() {
				_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "shared-thread", Prompt: "continue", AuthorizationCeiling: test.ceiling})
				done <- err
			}()
			request := readClientRequest(t, requests)
			var params wire.TurnStartParams
			if err := json.Unmarshal(request["params"], &params); err != nil {
				t.Fatal(err)
			}
			if params.SandboxPolicy == nil || params.SandboxPolicy.Type != test.want || params.SandboxPolicy.NetworkAccess || len(params.SandboxPolicy.WritableRoots) != 0 {
				t.Fatalf("turn/start sandbox policy=%+v", params.SandboxPolicy)
			}
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-one", "status": "inProgress", "items": []any{}}}})
			writeTokenUsage(t, responses, "shared-thread", "turn-one", tokenBreakdown(0, 0, 0, 0, 0, 0), tokenBreakdown(0, 0, 0, 0, 0, 0), nil)
			writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "shared-thread", "turn": map[string]any{"id": "turn-one", "status": "completed", "items": []any{}}}})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSetThreadNameUsesPinnedProtocol(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	done := make(chan error, 1)
	go func() { done <- client.SetThreadName(context.Background(), "thread-durable", "task_exact_id") }()
	request := readClientRequest(t, requests)
	var method string
	if err := json.Unmarshal(request["method"], &method); err != nil || method != "thread/name/set" {
		t.Fatalf("method=%q err=%v", method, err)
	}
	var params wire.ThreadSetNameParams
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != "thread-durable" || params.Name != "task_exact_id" {
		t.Fatalf("params=%+v", params)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestResumeThreadWithoutReliableBaselineMakesTurnUsageUnavailable(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	resumed := make(chan error, 1)
	go func() {
		_, err := client.ResumeThread(context.Background(), ThreadResumeOptions{ThreadID: "thread-no-baseline", CWD: "/bridge/workspace"})
		resumed <- err
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "thread-no-baseline"}, "cwd": "/bridge/workspace"}})
	if err := <-resumed; err != nil {
		t.Fatal(err)
	}

	turnResult := make(chan TurnResult, 1)
	go func() {
		value, _ := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-no-baseline", Prompt: "continue"})
		turnResult <- value
	}()
	request = readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-after-gap", "status": "inProgress", "items": []any{}}}})
	writeTokenUsage(t, responses, "thread-no-baseline", "turn-after-gap", tokenBreakdown(50, 0, 0, 10, 0, 60), tokenBreakdown(50, 0, 0, 10, 0, 60), nil)
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-no-baseline", "turn": map[string]any{"id": "turn-after-gap", "status": "completed", "items": []any{}}}})
	got := <-turnResult
	if got.TokenUsage == nil || got.TokenUsage.Reliable {
		t.Fatalf("usage=%+v", got.TokenUsage)
	}
}

func TestStartTurnMarksDecreasingCumulativeUsageUnreliable(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	client.mu.Lock()
	client.threadUsage = map[string]threadUsageState{"thread-usage": {ledger: codexusage.CodexUsageLedger{ThreadID: "thread-usage", AccountingEpoch: 1, PreviousRawCumulative: tokenCounts(tokenBreakdown(100, 20, 0, 10, 5, 110)), LogicalCumulative: tokenCounts(tokenBreakdown(100, 20, 0, 10, 5, 110)), Anchored: true}}}
	client.mu.Unlock()
	resultChannel := make(chan TurnResult, 1)
	go func() {
		value, _ := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-usage", Prompt: "usage"})
		resultChannel <- value
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-usage", "status": "inProgress", "items": []any{}}}})
	writeTokenUsage(t, responses, "thread-usage", "turn-usage", tokenBreakdown(90, 20, 0, 10, 5, 100), tokenBreakdown(0, 0, 0, 0, 0, 0), nil)
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-usage", "turn": map[string]any{"id": "turn-usage", "status": "completed", "items": []any{}}}})
	got := <-resultChannel
	if got.TokenUsage == nil || got.TokenUsage.Reliable {
		t.Fatalf("usage=%+v", got.TokenUsage)
	}
}

func tokenBreakdown(input, cached, cacheWrite, output, reasoning, total int64) wire.TokenUsageBreakdown {
	return wire.TokenUsageBreakdown{InputTokens: input, CachedInputTokens: cached, CacheWriteInputTokens: cacheWrite, OutputTokens: output, ReasoningOutputTokens: reasoning, TotalTokens: total}
}

func writeTokenUsage(t *testing.T, writer io.Writer, threadID, turnID string, total, last wire.TokenUsageBreakdown, window *int64) {
	t.Helper()
	usage := map[string]any{"total": total, "last": last, "modelContextWindow": window}
	writeServerJSON(t, writer, map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": threadID, "turnId": turnID, "tokenUsage": usage}})
}

func TestStartInitializationContract(t *testing.T) {
	for _, test := range []struct {
		name       string
		mode       string
		wantError  bool
		wantEvents []string
	}{
		{name: "success", mode: "success", wantEvents: []string{"initialize", "initialized", "eof"}},
		{name: "failure", mode: "failure", wantError: true, wantEvents: []string{"initialize", "eof"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "handshake.log")
			t.Setenv("DEVBRIDGE_START_HELPER", "1")
			t.Setenv("DEVBRIDGE_START_HELPER_MODE", test.mode)
			t.Setenv("DEVBRIDGE_START_HELPER_LOG", logPath)
			client, err := Start(context.Background(), Config{CodexPath: startHelperCodexPath(t)})
			if test.wantError {
				if err == nil || client != nil {
					t.Fatalf("Start = client %#v, error %v", client, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			}
			contents, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			events := strings.Fields(string(contents))
			if fmt.Sprint(events) != fmt.Sprint(test.wantEvents) {
				t.Fatalf("handshake events = %v, want %v", events, test.wantEvents)
			}
		})
	}
}

func TestClientRepeatedStartCloseJoinsOwnedLifecycle(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "lifecycle.log")
	t.Setenv("DEVBRIDGE_START_HELPER", "1")
	t.Setenv("DEVBRIDGE_START_HELPER_MODE", "success")
	t.Setenv("DEVBRIDGE_START_HELPER_LOG", logPath)
	codexPath := startHelperCodexPath(t)

	for iteration := 0; iteration < 10; iteration++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		client, err := Start(ctx, Config{CodexPath: codexPath})
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: Start: %v", iteration, err)
		}
		if err := client.Close(); err != nil {
			t.Fatalf("iteration %d: Close: %v", iteration, err)
		}
		for name, done := range map[string]<-chan struct{}{
			"process":         client.process.done,
			"stderr drain":    client.process.stderrDone,
			"RPC read loop":   client.rpc.readDone,
			"response writer": client.rpc.serverWriterDone,
			"process watcher": client.watchDone,
		} {
			select {
			case <-done:
			default:
				t.Fatalf("iteration %d: %s was not joined", iteration, name)
			}
		}
		client.rpc.mu.Lock()
		pending := len(client.rpc.pending)
		client.rpc.mu.Unlock()
		if pending != 0 {
			t.Fatalf("iteration %d: pending RPC operations = %d", iteration, pending)
		}
	}
}

func TestDefaultApprovalPolicyExplicitlyDeclines(t *testing.T) {
	for _, method := range []string{
		"item/commandExecution/requestApproval",
		"item/fileChange/requestApproval",
		"execCommandApproval",
		"applyPatchApproval",
		"mcpServer/elicitation/request",
	} {
		t.Run(method, func(t *testing.T) {
			result, err := builtInServerRequestPolicy(context.Background(), ServerRequest{Method: method})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "decline") && !strings.Contains(string(encoded), "denied") {
				t.Fatalf("approval response does not explicitly decline: %s", encoded)
			}
		})
	}
}

func TestBuiltInPermissionApprovalGrantsNoAdditionalPermissions(t *testing.T) {
	result, err := builtInServerRequestPolicy(context.Background(), ServerRequest{Method: "item/permissions/requestApproval"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"permissions":{}}`; got != want {
		t.Fatalf("permission approval response = %s, want %s", got, want)
	}
}

func TestStartHelperProcess(t *testing.T) {
	if os.Getenv("DEVBRIDGE_START_HELPER") != "1" {
		return
	}
	logPath := os.Getenv("DEVBRIDGE_START_HELPER_LOG")
	mode := os.Getenv("DEVBRIDGE_START_HELPER_MODE")
	initialized := false
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return
		}
		var method string
		_ = json.Unmarshal(message["method"], &method)
		switch method {
		case "initialize":
			appendStartHelperLog(logPath, "initialize")
			if mode == "failure" {
				writeStartHelperJSON(map[string]any{"id": message["id"], "error": map[string]any{"code": -32000, "message": "initialize failed"}})
				continue
			}
			writeStartHelperJSON(map[string]any{"id": message["id"], "result": map[string]any{"userAgent": "test"}})
			initialized = true
		case "initialized":
			if initialized {
				appendStartHelperLog(logPath, "initialized")
			} else {
				appendStartHelperLog(logPath, "initialized-before-success")
			}
		}
	}
	appendStartHelperLog(logPath, "eof")
}

func startHelperCodexPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-test-helper")
	script := fmt.Sprintf("#!/bin/sh\nexec %q -test.run=TestStartHelperProcess -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendStartHelperLog(path, event string) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(file, event)
	_ = file.Close()
}

func writeStartHelperJSON(message any) {
	data, err := json.Marshal(message)
	if err == nil {
		_, _ = fmt.Fprintln(os.Stdout, string(data))
	}
}

func TestStartTurnPreservesEmptyFinalMessage(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	client := &Client{turns: make(map[turnKey]*turnState)}
	client.rpc = newRPCTransport(stdoutReader, stdinWriter, client.handleEvent)
	t.Cleanup(func() {
		_ = client.rpc.Close()
		_ = stdinReader.Close()
		_ = stdoutWriter.Close()
	})
	requests := bufio.NewReader(stdinReader)

	type result struct {
		turn TurnResult
		err  error
	}
	resultChannel := make(chan result, 1)
	go func() {
		turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-empty", Prompt: "empty"})
		resultChannel <- result{turn: turn, err: err}
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, stdoutWriter, map[string]any{
		"id":     request["id"],
		"result": map[string]any{"turn": map[string]any{"id": "turn-empty", "status": "inProgress", "items": []any{}}},
	})
	writeServerJSON(t, stdoutWriter, map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"threadId": "thread-empty",
			"turnId":   "turn-empty",
			"item":     map[string]any{"id": "fallback", "type": "agentMessage", "text": "foo"},
		},
	})
	writeServerJSON(t, stdoutWriter, map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"threadId": "thread-empty",
			"turnId":   "turn-empty",
			"item":     map[string]any{"id": "final", "type": "agentMessage", "text": "", "phase": "final_answer"},
		},
	})
	writeServerJSON(t, stdoutWriter, map[string]any{
		"method": "turn/completed",
		"params": map[string]any{
			"threadId": "thread-empty",
			"turn":     map[string]any{"id": "turn-empty", "status": "completed", "items": []any{}},
		},
	})

	select {
	case got := <-resultChannel:
		if got.err != nil || got.turn.FinalMessage != "" {
			t.Fatalf("turn result = %#v", got)
		}
		assertNoTurnState(t, client, turnKey{threadID: "thread-empty", turnID: "turn-empty"})
	case <-time.After(time.Second):
		t.Fatal("turn did not complete")
	}
}

func TestStartTurnCapturesBoundedAuthoritativeCommandItems(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	resultChannel := make(chan TurnResult, 1)
	go func() {
		result, _ := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-command", Prompt: "verify"})
		resultChannel <- result
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{
		"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-command", "status": "inProgress", "items": []any{}}},
	})
	writeServerJSON(t, responses, map[string]any{"method": "item/started", "params": map[string]any{
		"threadId": "thread-command", "turnId": "turn-command", "startedAtMs": int64(1_700_000_000_000), "item": map[string]any{"id": "cmd-1", "type": "commandExecution", "command": "/bin/zsh -lc 'git diff --check'", "cwd": "/private/worktree", "status": "inProgress", "commandActions": []any{map[string]any{"type": "unknown", "command": "git diff --check"}}},
	}})
	writeServerJSON(t, responses, map[string]any{"method": "item/completed", "params": map[string]any{
		"threadId": "thread-command", "turnId": "turn-command", "completedAtMs": int64(1_700_000_000_012), "item": map[string]any{"id": "cmd-1", "type": "commandExecution", "command": "/bin/zsh -lc 'git diff --check'", "cwd": "/private/worktree", "status": "completed", "commandActions": []any{map[string]any{"type": "unknown", "command": "git diff --check"}}, "exitCode": 0, "durationMs": 12, "aggregatedOutput": strings.Repeat("x", maxCommandOutputBytes+1)},
	}})
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{
		"threadId": "thread-command", "turn": map[string]any{"id": "turn-command", "status": "completed", "items": []any{}},
	}})
	select {
	case result := <-resultChannel:
		if len(result.Commands) != 1 || result.Commands[0].Command != "/bin/zsh -lc 'git diff --check'" || len(result.Commands[0].Actions) != 1 || result.Commands[0].Actions[0] != "git diff --check" || result.Commands[0].ExitCode == nil || *result.Commands[0].ExitCode != 0 || len(result.Commands[0].Output) != maxCommandOutputBytes || !result.Commands[0].OutputTruncated || result.Commands[0].StartedAt == nil || result.Commands[0].StartedAt.UnixMilli() != 1_700_000_000_000 || result.Commands[0].FinishedAt == nil || result.Commands[0].FinishedAt.UnixMilli() != 1_700_000_000_012 {
			t.Fatalf("commands=%+v", result.Commands)
		}
	case <-time.After(time.Second):
		t.Fatal("turn did not complete")
	}
}

func TestStartTurnCompletionWinsAfterCorrelationBeforeExit(t *testing.T) {
	reader, server := io.Pipe()
	writer := newGatedWriteCloser()
	client := &Client{turns: make(map[turnKey]*turnState)}
	client.rpc = newRPCTransport(reader, writer, client.handleEvent)
	resultChannel := make(chan struct {
		turn TurnResult
		err  error
	}, 1)
	go func() {
		turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-race", Prompt: "race"})
		resultChannel <- struct {
			turn TurnResult
			err  error
		}{turn: turn, err: err}
	}()
	<-writer.entered
	if err := client.rpc.dispatch([]byte(`{"id":1,"result":{"turn":{"id":"turn-race","status":"inProgress","items":[]}}}`)); err != nil {
		t.Fatal(err)
	}
	client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-race","turn":{"id":"turn-race","status":"completed","items":[]}}`)})
	client.rpc.fail(ErrAppServerExited)
	writer.releaseWrite()

	got := <-resultChannel
	if got.err != nil || got.turn.TurnID != "turn-race" {
		t.Fatalf("turn result = %#v", got)
	}
	_ = client.rpc.Close()
	_ = server.Close()
}

func TestTransportFailureRejectsLateLifecycleState(t *testing.T) {
	reader, server := io.Pipe()
	requestReader, writer := io.Pipe()
	client := &Client{turns: make(map[turnKey]*turnState)}
	client.rpc = newRPCTransport(reader, writer, client.handleEvent)
	client.rpc.fail(ErrAppServerExited)
	client.rpc.dispatchEvent(Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"late-thread","turn":{"id":"late-turn","status":"completed","items":[]}}`)})
	assertNoTurnState(t, client, turnKey{threadID: "late-thread", turnID: "late-turn"})
	_ = client.rpc.Close()
	_ = server.Close()
	_ = requestReader.Close()
}

func TestStartTurnProtectsCompletionBeforeResponseFromEviction(t *testing.T) {
	reader, server := io.Pipe()
	writer := newGatedWriteCloser()
	client := &Client{turns: make(map[turnKey]*turnState)}
	client.rpc = newRPCTransport(reader, writer, client.handleEvent)
	resultChannel := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "protected-thread", Prompt: "protect"})
		resultChannel <- err
	}()
	<-writer.entered
	client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"protected-thread","turn":{"id":"protected-turn","status":"completed","items":[]}}`)})
	for i := 0; i < maxProvisionalTurnsPerStart-1; i++ {
		params := fmt.Sprintf(`{"threadId":"protected-thread","turn":{"id":"provisional-%d","status":"completed","items":[]}}`, i)
		client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(params)})
	}
	for i := 0; i < maxRememberedTurns+32; i++ {
		params := fmt.Sprintf(`{"threadId":"unrelated-%d","turn":{"id":"turn-%d","status":"completed","items":[]}}`, i, i)
		client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(params)})
	}
	if err := client.rpc.dispatch([]byte(`{"id":1,"result":{"turn":{"id":"protected-turn","status":"inProgress","items":[]}}}`)); err != nil {
		t.Fatal(err)
	}
	writer.releaseWrite()
	if err := <-resultChannel; err != nil {
		t.Fatal(err)
	}
	assertNoTurnState(t, client, turnKey{threadID: "protected-thread", turnID: "protected-turn"})
	client.mu.Lock()
	remembered := len(client.turns)
	client.mu.Unlock()
	if remembered > maxRememberedTurns {
		t.Fatalf("remembered turns = %d, limit = %d", remembered, maxRememberedTurns)
	}
	_ = client.rpc.Close()
	_ = server.Close()
}

func TestStartTurnExitBeforeCompletionCleansWaiter(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	resultChannel := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-exit", Prompt: "exit"})
		resultChannel <- err
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{
		"id":     request["id"],
		"result": map[string]any{"turn": map[string]any{"id": "turn-exit", "status": "inProgress", "items": []any{}}},
	})
	waitForTurnWaiter(t, client, turnKey{threadID: "thread-exit", turnID: "turn-exit"})
	client.rpc.fail(ErrAppServerExited)
	if err := <-resultChannel; !errors.Is(err, ErrAppServerExited) {
		t.Fatalf("turn error = %v", err)
	}
	assertNoTurnState(t, client, turnKey{threadID: "thread-exit", turnID: "turn-exit"})
}

func TestStartTurnCancellationCleansWaiter(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(ctx, TurnOptions{ThreadID: "thread-cancel", Prompt: "cancel"})
		resultChannel <- err
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{
		"id":     request["id"],
		"result": map[string]any{"turn": map[string]any{"id": "turn-cancel", "status": "inProgress", "items": []any{}}},
	})
	waitForTurnWaiter(t, client, turnKey{threadID: "thread-cancel", turnID: "turn-cancel"})
	cancel()
	if err := <-resultChannel; !errors.Is(err, context.Canceled) {
		t.Fatalf("turn error = %v", err)
	}
	key := turnKey{threadID: "thread-cancel", turnID: "turn-cancel"}
	assertNoTurnState(t, client, key)
	client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-cancel","turn":{"id":"turn-cancel","status":"completed","items":[]}}`)})
	assertNoTurnState(t, client, key)
	client.mu.Lock()
	_, retired := client.retired[key]
	client.mu.Unlock()
	if !retired {
		t.Fatal("canceled turn was not retained in the bounded retired set")
	}
}

func TestStartTurnSameThreadLifecycleFloodFailsBoundedly(t *testing.T) {
	client, requests, _ := newClientFixture(t)
	resultChannel := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "flood-thread", Prompt: "flood"})
		resultChannel <- err
	}()
	_ = readClientRequest(t, requests)

	for i := 0; i <= maxProvisionalTurnsPerStart; i++ {
		params := fmt.Sprintf(`{"threadId":"flood-thread","turn":{"id":"candidate-%d","status":"completed","items":[]}}`, i)
		client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(params)})
	}

	select {
	case err := <-resultChannel:
		var overflow *TurnLifecycleOverflowError
		if !errors.As(err, &overflow) {
			t.Fatalf("turn error = %T %v", err, err)
		}
		if overflow.ThreadID != "flood-thread" || overflow.Limit != maxProvisionalTurnsPerStart {
			t.Fatalf("overflow = %#v", overflow)
		}
	case <-time.After(time.Second):
		t.Fatal("turn/start did not fail after lifecycle overflow")
	}

	client.mu.Lock()
	turnCount := len(client.turns)
	retiredCount := len(client.retired)
	start := client.starting["flood-thread"]
	client.mu.Unlock()
	if turnCount > maxRememberedTurns {
		t.Fatalf("live turns = %d, limit = %d", turnCount, maxRememberedTurns)
	}
	if retiredCount > maxRetiredTurns {
		t.Fatalf("retired turns = %d, limit = %d", retiredCount, maxRetiredTurns)
	}
	if start != nil {
		t.Fatal("overflowed turn/start remained registered")
	}
	overflowKey := turnKey{threadID: "flood-thread", turnID: fmt.Sprintf("candidate-%d", maxProvisionalTurnsPerStart)}
	client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":"flood-thread","turn":{"id":"candidate-%d","status":"completed","items":[]}}`,
		maxProvisionalTurnsPerStart,
	))})
	assertNoTurnState(t, client, overflowKey)
	client.mu.Lock()
	_, overflowRetired := client.retired[overflowKey]
	client.mu.Unlock()
	if !overflowRetired {
		t.Fatal("overflow-triggering turn was not retained in the bounded retired set")
	}
	client.rpc.mu.Lock()
	pending := len(client.rpc.pending)
	client.rpc.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending RPC calls = %d", pending)
	}
}

func TestStartTurnLargePostOverflowFloodRemainsBoundedAndReusable(t *testing.T) {
	const postOverflowEvents = 1024
	client, requests, responses := newClientFixture(t)
	resultChannel := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "large-flood-thread", Prompt: "flood"})
		resultChannel <- err
	}()
	_ = readClientRequest(t, requests)

	maximumCandidates, maximumLive, maximumRetired, maximumWaiters := 0, 0, 0, 0
	recordCardinality := func() {
		client.mu.Lock()
		defer client.mu.Unlock()
		if start := client.starting["large-flood-thread"]; start != nil && len(start.candidates) > maximumCandidates {
			maximumCandidates = len(start.candidates)
		}
		if len(client.turns) > maximumLive {
			maximumLive = len(client.turns)
		}
		if len(client.retired) > maximumRetired {
			maximumRetired = len(client.retired)
		}
		waiters := 0
		for _, state := range client.turns {
			if state.waiter != nil {
				waiters++
			}
		}
		if waiters > maximumWaiters {
			maximumWaiters = waiters
		}
	}

	for index := 0; index <= maxProvisionalTurnsPerStart; index++ {
		params := fmt.Sprintf(`{"threadId":"large-flood-thread","turn":{"id":"candidate-%d","status":"completed","items":[]}}`, index)
		client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(params)})
		recordCardinality()
	}
	select {
	case err := <-resultChannel:
		var overflow *TurnLifecycleOverflowError
		if !errors.As(err, &overflow) || overflow.Limit != maxProvisionalTurnsPerStart {
			t.Fatalf("turn error = %T %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("turn/start did not terminate after lifecycle overflow")
	}

	for index := 0; index < postOverflowEvents; index++ {
		params := fmt.Sprintf(`{"threadId":"large-flood-thread","turn":{"id":"late-%d","status":"completed","items":[]}}`, index)
		client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(params)})
		recordCardinality()
	}

	client.mu.Lock()
	start := client.starting["large-flood-thread"]
	retiredCount := len(client.retired)
	for index := 0; index <= maxProvisionalTurnsPerStart; index++ {
		key := turnKey{threadID: "large-flood-thread", turnID: fmt.Sprintf("candidate-%d", index)}
		if client.turns[key] != nil {
			client.mu.Unlock()
			t.Fatalf("overflow-owned candidate recreated live state: %#v", key)
		}
		if _, retired := client.retired[key]; !retired {
			client.mu.Unlock()
			t.Fatalf("overflow-owned candidate was not retired: %#v", key)
		}
	}
	client.mu.Unlock()
	if start != nil {
		t.Fatal("overflowed pending start was resurrected")
	}
	if maximumCandidates != maxProvisionalTurnsPerStart {
		t.Fatalf("maximum provisional candidates = %d, want %d", maximumCandidates, maxProvisionalTurnsPerStart)
	}
	if maximumLive > maxRememberedTurns {
		t.Fatalf("maximum live turns = %d, limit = %d", maximumLive, maxRememberedTurns)
	}
	if maximumRetired > maxRetiredTurns || retiredCount > maxRetiredTurns {
		t.Fatalf("retired turns: maximum=%d final=%d limit=%d", maximumRetired, retiredCount, maxRetiredTurns)
	}
	if maximumWaiters != 0 {
		t.Fatalf("post-overflow waiter count reached %d", maximumWaiters)
	}
	t.Logf(
		"post-overflow cardinalities: candidates max=%d live max=%d retired max=%d retired final=%d waiters max=%d",
		maximumCandidates,
		maximumLive,
		maximumRetired,
		retiredCount,
		maximumWaiters,
	)

	overflowKey := turnKey{threadID: "large-flood-thread", turnID: fmt.Sprintf("candidate-%d", maxProvisionalTurnsPerStart)}
	client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(fmt.Sprintf(
		`{"threadId":"large-flood-thread","turn":{"id":"candidate-%d","status":"completed","items":[]}}`,
		maxProvisionalTurnsPerStart,
	))})
	assertNoTurnState(t, client, overflowKey)
	runSuccessfulTurnAfterLifecycleFailure(t, client, requests, responses, "large-flood-thread", "recovered-after-flood")
}

func TestStartTurnCancellationBeforeCorrelationRetiresProvisionalState(t *testing.T) {
	const provisionalCandidates = 8
	client, requests, responses := newClientFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(ctx, TurnOptions{ThreadID: "provisional-cancel-thread", Prompt: "cancel"})
		resultChannel <- err
	}()
	_ = readClientRequest(t, requests)

	for index := range provisionalCandidates {
		params := fmt.Sprintf(`{"threadId":"provisional-cancel-thread","turn":{"id":"candidate-%d","status":"completed","items":[]}}`, index)
		client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(params)})
	}
	client.mu.Lock()
	pendingStart := client.starting["provisional-cancel-thread"]
	pendingCandidates := 0
	if pendingStart != nil {
		pendingCandidates = len(pendingStart.candidates)
	}
	provisionalLive := len(client.turns)
	client.mu.Unlock()
	if pendingStart == nil || pendingCandidates != provisionalCandidates || provisionalLive != provisionalCandidates {
		t.Fatalf("pre-cancel state: start=%#v candidates=%d live=%d", pendingStart, pendingCandidates, provisionalLive)
	}

	cancel()
	select {
	case err := <-resultChannel:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("turn error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending turn/start did not return cancellation")
	}

	client.mu.Lock()
	if client.starting["provisional-cancel-thread"] != nil {
		client.mu.Unlock()
		t.Fatal("canceled pending start remained registered")
	}
	for index := range provisionalCandidates {
		key := turnKey{threadID: "provisional-cancel-thread", turnID: fmt.Sprintf("candidate-%d", index)}
		if client.turns[key] != nil {
			client.mu.Unlock()
			t.Fatalf("provisional turn remained live after cancellation: %#v", key)
		}
		if _, retired := client.retired[key]; !retired {
			client.mu.Unlock()
			t.Fatalf("provisional turn was not retired after cancellation: %#v", key)
		}
	}
	for key, state := range client.turns {
		if state.waiter != nil {
			client.mu.Unlock()
			t.Fatalf("active waiter remained after cancellation for %#v", key)
		}
	}
	liveCount, retiredCount := len(client.turns), len(client.retired)
	client.mu.Unlock()
	client.rpc.mu.Lock()
	pendingRPCCount := len(client.rpc.pending)
	client.rpc.mu.Unlock()
	if liveCount > maxRememberedTurns || retiredCount > maxRetiredTurns {
		t.Fatalf("post-cancel cardinality: live=%d retired=%d", liveCount, retiredCount)
	}
	if pendingRPCCount != 0 {
		t.Fatalf("pending RPC calls after cancellation = %d", pendingRPCCount)
	}
	t.Logf(
		"post-cancel cardinalities: candidates=%d live=%d retired=%d pending RPC=%d",
		pendingCandidates,
		liveCount,
		retiredCount,
		pendingRPCCount,
	)

	lateKey := turnKey{threadID: "provisional-cancel-thread", turnID: "candidate-0"}
	client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"provisional-cancel-thread","turn":{"id":"candidate-0","status":"completed","items":[]}}`)})
	assertNoTurnState(t, client, lateKey)
	runSuccessfulTurnAfterLifecycleFailure(t, client, requests, responses, "provisional-cancel-thread", "recovered-after-cancel")
}

func TestStartTurnRejectsConcurrentStartForSameThread(t *testing.T) {
	client, requests, _ := newClientFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := client.StartTurn(ctx, TurnOptions{ThreadID: "one-at-a-time", Prompt: "first"})
		firstResult <- err
	}()
	_ = readClientRequest(t, requests)

	_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "one-at-a-time", Prompt: "second"})
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || !strings.Contains(err.Error(), "already pending") {
		t.Fatalf("concurrent turn/start error = %T %v", err, err)
	}
	cancel()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first turn/start error = %v", err)
	}
}

func TestRetiredTurnTrackingIsBounded(t *testing.T) {
	client := &Client{
		turns:   make(map[turnKey]*turnState),
		retired: make(map[turnKey]uint64),
	}
	client.mu.Lock()
	for i := 0; i < maxRetiredTurns+100; i++ {
		client.retireTurnLocked(turnKey{threadID: "retired-thread", turnID: fmt.Sprintf("turn-%d", i)}, nil)
	}
	retiredCount := len(client.retired)
	_, oldestRetained := client.retired[turnKey{threadID: "retired-thread", turnID: "turn-0"}]
	_, newestRetained := client.retired[turnKey{threadID: "retired-thread", turnID: fmt.Sprintf("turn-%d", maxRetiredTurns+99)}]
	client.mu.Unlock()
	if retiredCount != maxRetiredTurns {
		t.Fatalf("retired turns = %d, want %d", retiredCount, maxRetiredTurns)
	}
	if oldestRetained || !newestRetained {
		t.Fatalf("retired LRU eviction: oldest retained=%v, newest retained=%v", oldestRetained, newestRetained)
	}
}

func newClientFixture(t *testing.T) (*Client, *bufio.Reader, *io.PipeWriter) {
	t.Helper()
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	client := &Client{turns: make(map[turnKey]*turnState)}
	client.rpc = newRPCTransport(stdoutReader, stdinWriter, client.handleEvent)
	t.Cleanup(func() {
		_ = client.rpc.Close()
		_ = stdinReader.Close()
		_ = stdoutWriter.Close()
	})
	return client, bufio.NewReader(stdinReader), stdoutWriter
}

func readClientRequest(t *testing.T, reader *bufio.Reader) map[string]json.RawMessage {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(line, &request); err != nil {
		t.Fatal(err)
	}
	return request
}

func waitForTurnWaiter(t *testing.T, client *Client, key turnKey) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		client.mu.Lock()
		ready := client.turns[key] != nil && client.turns[key].waiter != nil
		client.mu.Unlock()
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("turn waiter was not registered")
		}
		time.Sleep(time.Millisecond)
	}
}

func assertNoTurnState(t *testing.T, client *Client, key turnKey) {
	t.Helper()
	client.mu.Lock()
	defer client.mu.Unlock()
	if _, exists := client.turns[key]; exists {
		t.Fatalf("turn state still registered for %#v", key)
	}
}

func runSuccessfulTurnAfterLifecycleFailure(
	t *testing.T,
	client *Client,
	requests *bufio.Reader,
	responses *io.PipeWriter,
	threadID string,
	turnID string,
) {
	t.Helper()
	resultChannel := make(chan struct {
		turn TurnResult
		err  error
	}, 1)
	go func() {
		turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: threadID, Prompt: "recover"})
		resultChannel <- struct {
			turn TurnResult
			err  error
		}{turn: turn, err: err}
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{
		"id":     request["id"],
		"result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}},
	})
	writeServerJSON(t, responses, map[string]any{
		"method": "turn/completed",
		"params": map[string]any{
			"threadId": threadID,
			"turn":     map[string]any{"id": turnID, "status": "completed", "items": []any{}},
		},
	})
	select {
	case result := <-resultChannel:
		if result.err != nil || result.turn.ThreadID != threadID || result.turn.TurnID != turnID {
			t.Fatalf("recovery turn = %#v, error = %v", result.turn, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("independent StartTurn did not complete after lifecycle failure")
	}
}

func writeAgentCompletion(t *testing.T, writer io.Writer, threadID, turnID, text string) {
	t.Helper()
	writeServerJSON(t, writer, map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"threadId": threadID,
			"turnId":   turnID,
			"item":     map[string]any{"id": "message-" + turnID, "type": "agentMessage", "text": text, "phase": "final_answer"},
		},
	})
	writeServerJSON(t, writer, map[string]any{
		"method": "turn/completed",
		"params": map[string]any{
			"threadId": threadID,
			"turn":     map[string]any{"id": turnID, "status": "completed", "items": []any{}},
		},
	})
}

func writeServerJSON(t *testing.T, writer io.Writer, message any) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
}
