package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex/wire"
	"github.com/lazyun/devbridge/internal/runtimepermission"
)

// decodeInjectedPolicy reads the first executor policy injection request and
// asserts it carries the current turn role for the exact thread.
func decodeInjectedPolicy(t *testing.T, request map[string]json.RawMessage, threadID string) string {
	t.Helper()
	if string(request["method"]) != `"thread/inject_items"` {
		t.Fatalf("current policy must be injected before turn/start: %s", request["method"])
	}
	var params wire.ThreadInjectItemsParams
	if err := json.Unmarshal(request["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params.ThreadID != threadID || len(params.Items) != 1 {
		t.Fatalf("policy injection params=%+v", params)
	}
	var item struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(params.Items[0], &item); err != nil || item.Type != "message" || item.Role != "developer" || len(item.Content) != 1 || item.Content[0].Type != "input_text" {
		t.Fatalf("policy item=%s err=%v", params.Items[0], err)
	}
	return item.Content[0].Text
}

func TestExecutorTurnPolicyReplacesHistoricalRoleWithoutChangingEngineeringTask(t *testing.T) {
	for _, implementation := range []bool{true, false} {
		purpose := "VERIFICATION"
		binding := permissionBinding()
		ceiling := AuthorizationReadOnly
		if implementation {
			purpose = "IMPLEMENTATION"
			binding.TaskPermissions.Purpose = purpose
			binding.TaskPermissions.Copies = nil
			binding.TaskPermissions.Writes = []runtimepermission.WriteGrant{{Root: "/fixture/source", Kind: "PATHS", Paths: []string{"internal/allowed"}}}
			binding.RuntimeWorkspaceRoots = []string{"/fixture/source", "/fixture/scratch"}
			binding.WritableRoots = append([]string(nil), binding.RuntimeWorkspaceRoots...)
			ceiling = AuthorizationWorkspaceWrite
		}
		binding.TaskPermissions.ContentHash = binding.TaskPermissions.Hash()
		t.Run(purpose, func(t *testing.T) {
			c, requests, responses := newClientFixture(t)
			c.executorBoundary = true
			c.taskPermissionHash = binding.TaskPermissions.ContentHash
			c.threadBindings = map[string]RuntimeBinding{"resumed-thread": binding}
			c.markExecutorThread("resumed-thread")
			prompt := "  exact approved engineering task\nDo not change this text.\n"
			done := make(chan error, 1)
			go func() {
				_, err := c.StartTurn(context.Background(), TurnOptions{RunID: "run", ThreadID: "resumed-thread", Prompt: prompt, Model: "frozen-model", ReasoningEffort: "high", AuthorizationCeiling: ceiling, RuntimeBinding: binding})
				done <- err
			}()
			injected := readClientRequest(t, requests)
			injectedPolicy := decodeInjectedPolicy(t, injected, "resumed-thread")
			if !strings.Contains(injectedPolicy, `"purpose":"`+purpose+`"`) || !strings.Contains(injectedPolicy, "supersedes earlier host operation roles") {
				t.Fatal("injected policy does not carry the current turn role")
			}
			if implementation {
				if !strings.Contains(injectedPolicy, `"writes":[{"root":"/fixture/source","kind":"PATHS","paths":["internal/allowed"]}]`) {
					t.Fatal("injected implementation policy lost scoped writes")
				}
			} else if strings.Contains(injectedPolicy, "This is an IMPLEMENTATION turn") {
				t.Fatal("injected verification policy carried implementation role")
			}
			writeServerJSON(t, responses, map[string]any{"id": injected["id"], "result": map[string]any{}})
			req := readClientRequest(t, requests)
			if string(req["method"]) != `"turn/start"` {
				t.Fatalf("unexpected method after injection: %s", req["method"])
			}
			var params wire.TurnStartParams
			if err := json.Unmarshal(req["params"], &params); err != nil {
				t.Fatal(err)
			}
			mode := params.CollaborationMode
			if mode == nil || mode.Mode != "default" || mode.Settings.Model != "frozen-model" || mode.Settings.ReasoningEffort == nil || *mode.Settings.ReasoningEffort != "high" {
				t.Fatalf("frozen selection lost: %+v", mode)
			}
			instructions := mode.Settings.DeveloperInstructions
			if !strings.Contains(instructions, `"purpose":"`+purpose+`"`) || !strings.Contains(instructions, "supersedes earlier host operation roles") {
				t.Fatal("current turn role not installed")
			}
			if implementation {
				if !strings.Contains(instructions, `"writes":[{"root":"/fixture/source","kind":"PATHS","paths":["internal/allowed"]}]`) || strings.Contains(instructions, "Copied source is also read-only") {
					t.Fatal("implementation policy lost scoped writes or retained verification-only policy")
				}
			} else if !strings.Contains(instructions, "Product source worktrees and copied source are read-only") || strings.Contains(instructions, "This is an IMPLEMENTATION turn") {
				t.Fatal("verification inherited implementation role")
			}
			if params.Permissions != binding.TaskPermissions.ProfileID() || params.ApprovalPolicy != "never" || params.SandboxPolicy != nil || len(params.Input) != 1 || params.Input[0].Text != prompt {
				t.Fatal("native authority or approved instruction changed")
			}
			finishBoundTurn(t, responses, req, "resumed-thread", "new-turn")
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Injection failure must reject dispatch, release the reservation, and allow a
// later retry without polluting other threads.
func TestExecutorTurnPolicyInjectionFailureRejectsDispatchAndAllowsRetry(t *testing.T) {
	c, requests, responses := newClientFixture(t)
	c.executorBoundary = true
	binding := permissionBinding()
	c.taskPermissionHash = binding.TaskPermissions.ContentHash
	c.threadBindings = map[string]RuntimeBinding{"resumed-thread": binding, "other-thread": binding}
	c.markExecutorThread("resumed-thread")
	c.markExecutorThread("other-thread")
	result := make(chan error, 1)
	go func() {
		_, err := c.StartTurn(context.Background(), TurnOptions{RunID: "run", ThreadID: "resumed-thread", Prompt: "work", Model: "frozen-model", AuthorizationCeiling: AuthorizationReadOnly, RuntimeBinding: binding})
		result <- err
	}()
	req := readClientRequest(t, requests)
	decodeInjectedPolicy(t, req, "resumed-thread")
	writeServerJSON(t, responses, map[string]any{"id": req["id"], "error": map[string]any{"code": -32000, "message": "injection rejected"}})
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "inject current turn policy") {
			t.Fatalf("injection failure error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("injection failure did not reject the turn")
	}
	// The reservation must be released so the same thread can retry.
	retry := make(chan error, 1)
	go func() {
		_, err := c.StartTurn(context.Background(), TurnOptions{RunID: "run", ThreadID: "resumed-thread", Prompt: "work", Model: "frozen-model", AuthorizationCeiling: AuthorizationReadOnly, RuntimeBinding: binding})
		retry <- err
	}()
	retryInject := readClientRequest(t, requests)
	if decodeInjectedPolicy(t, retryInject, "resumed-thread") == "" {
		t.Fatal("retry injection missing policy")
	}
	writeServerJSON(t, responses, map[string]any{"id": retryInject["id"], "result": map[string]any{}})
	retryTurn := readClientRequest(t, requests)
	if string(retryTurn["method"]) != `"turn/start"` {
		t.Fatalf("retry method = %s", retryTurn["method"])
	}
	finishBoundTurn(t, responses, retryTurn, "resumed-thread", "retry-turn")
	if err := <-retry; err != nil {
		t.Fatal(err)
	}
}

func TestExecutorTurnPolicyMissingFrozenModelStopsBeforeDispatch(t *testing.T) {
	c, _, _ := newClientFixture(t)
	c.executorBoundary = true
	c.markExecutorThread("thread")
	if _, err := c.StartTurn(context.Background(), TurnOptions{ThreadID: "thread", Prompt: "work"}); err == nil || !strings.Contains(err.Error(), "frozen turn model required") {
		t.Fatal(err)
	}
}

func TestDiagnosticTurnPolicyCannotDescribeImplementationWrites(t *testing.T) {
	c := &Client{executorBoundary: true, diagnosticTaskPolicy: permissionBinding().TaskPermissions}
	c.diagnosticTaskPolicy.Purpose = "IMPLEMENTATION"
	mode, err := c.executorTurnMode(TurnOptions{Model: "frozen-model"}, RuntimeBinding{})
	if err != nil {
		t.Fatal(err)
	}
	text := mode.Settings.DeveloperInstructions
	if strings.Contains(text, "This is an IMPLEMENTATION turn") || !strings.Contains(text, "all source and prepared child repositories are read-only") {
		t.Fatal("diagnostic turn expanded authority")
	}
}

func TestExecutorTurnPolicyCancellationReleasesPendingStart(t *testing.T) {
	c, requests, _ := newClientFixture(t)
	c.executorBoundary = true
	binding := permissionBinding()
	c.taskPermissionHash = binding.TaskPermissions.ContentHash
	c.threadBindings = map[string]RuntimeBinding{"thread": binding}
	c.markExecutorThread("thread")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := c.StartTurn(ctx, TurnOptions{RunID: "run", ThreadID: "thread", Prompt: "work", Model: "frozen-model", AuthorizationCeiling: AuthorizationReadOnly, RuntimeBinding: binding})
		result <- err
	}()
	decodeInjectedPolicy(t, readClientRequest(t, requests), "thread")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled injection: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled injection left StartTurn blocked")
	}
	c.mu.Lock()
	pending := c.starting["thread"] != nil
	c.mu.Unlock()
	if pending {
		t.Fatal("cancelled injection retained the thread start reservation")
	}
}
