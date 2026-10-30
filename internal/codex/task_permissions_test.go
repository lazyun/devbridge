package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

func permissionBinding() RuntimeBinding {
	p := &TaskPermissions{Version: TaskPermissionVersion, TaskID: "task", RevisionID: "revision", ExecutionID: "execution", RunID: "run", WorksetHash: strings.Repeat("a", 64), Purpose: "VERIFICATION", ScratchRoot: "/fixture/scratch", Copies: []VerificationCopy{{SourceRoot: "/fixture/source", Root: "/fixture/copy", SourceDigest: strings.Repeat("b", 64), StartSnapshot: strings.Repeat("c", 40), RootIdentity: "copy-identity", OutputPaths: []string{"dist", "tsconfig.tsbuildinfo"}}}}
	p.ContentHash = p.Hash()
	return RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/source", "/fixture/copy", "/fixture/scratch"}, WritableRoots: []string{"/fixture/copy", "/fixture/scratch"}, TaskPermissions: p}
}

func TestTaskSandboxWirePinsNeverApprovalOnStartResumeAndTurn(t *testing.T) {
	c, requests, responses := newClientFixture(t)
	binding := permissionBinding()
	done := make(chan error, 1)
	go func() {
		_, err := c.StartThread(context.Background(), ThreadOptions{CWD: "/fixture/copy", RuntimeBinding: binding})
		done <- err
	}()
	request := readClientRequest(t, requests)
	var start wire.ThreadStartParams
	if err := json.Unmarshal(request["params"], &start); err != nil {
		t.Fatal(err)
	}
	if start.ApprovalPolicy != "never" || !strings.Contains(start.DeveloperInstructions, "independent copies") || start.Config["shell_environment_policy.set"] == nil {
		t.Fatalf("start policy lost: %+v", start)
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "thread"}, "cwd": "/fixture/copy"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := c.ResumeThread(context.Background(), ThreadResumeOptions{ThreadID: "thread", CWD: "/fixture/copy", RuntimeBinding: binding})
		done <- err
	}()
	request = readClientRequest(t, requests)
	var resume wire.ThreadResumeParams
	json.Unmarshal(request["params"], &resume)
	if resume.ApprovalPolicy != "never" || resume.Config["shell_environment_policy.set"] == nil {
		t.Fatal("resume inherited approval policy")
	}
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "thread"}, "cwd": "/fixture/copy"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := c.StartTurn(context.Background(), TurnOptions{ThreadID: "thread", RunID: "run", Prompt: "exact engineering prompt", AuthorizationCeiling: AuthorizationReadOnly, RuntimeBinding: binding})
		done <- err
	}()
	request = readClientRequest(t, requests)
	var turn wire.TurnStartParams
	json.Unmarshal(request["params"], &turn)
	if turn.ApprovalPolicy != "never" || turn.SandboxPolicy != nil || turn.Permissions != binding.TaskPermissions.ProfileID() {
		t.Fatalf("turn policy unsafe: %+v", turn)
	}
	for _, root := range binding.WritableRoots {
		if root == "/fixture/source" {
			t.Fatal("source writable")
		}
	}
	if turn.Input[0].Text != "exact engineering prompt" {
		t.Fatal("engineering instruction rewritten")
	}
	finishBoundTurn(t, responses, request, "thread", "turn")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartTurn(context.Background(), TurnOptions{ThreadID: "thread", RunID: "foreign-run", Prompt: "wrong owner", AuthorizationCeiling: AuthorizationReadOnly, RuntimeBinding: binding}); err == nil {
		t.Fatal("foreign owner dispatched")
	}
}

func TestTaskSandboxDeniesUnexpectedApprovalWithoutCallingInteractiveBroker(t *testing.T) {
	c := &Client{threadBindings: map[string]RuntimeBinding{"thread": permissionBinding()}}
	called := false
	next := func(context.Context, ServerRequest) (any, error) { called = true; return nil, nil }
	_, err := c.processTaskSandboxRequest(context.Background(), ServerRequest{Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn"}`)}, next)
	if err == nil || called || !strings.Contains(err.Error(), "TASK_SANDBOX_PERMISSION_EXCEEDED") {
		t.Fatal("approval left waiting")
	}
	_, err = c.processTaskSandboxRequest(context.Background(), ServerRequest{Method: "item/tool/requestUserInput", Params: json.RawMessage(`{"threadId":"thread"}`)}, next)
	if err != nil || !called {
		t.Fatal("human question auto-answered")
	}
}

func TestTaskSandboxDiagnosticDoesNotInheritImplementationAuthority(t *testing.T) {
	c, requests, responses := newClientFixture(t)
	p := permissionBinding().TaskPermissions
	c.diagnosticTaskPolicy = p.Clone()
	b := RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/diagnostic-child"}}
	if c.requireTaskProfile(RuntimeBinding{RuntimeWorkspaceRoots: b.RuntimeWorkspaceRoots, WritableRoots: b.RuntimeWorkspaceRoots}) == nil {
		t.Fatal("diagnostic writable binding accepted")
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.StartThread(context.Background(), ThreadOptions{CWD: b.RuntimeWorkspaceRoots[0], RuntimeBinding: b})
		done <- err
	}()
	req := readClientRequest(t, requests)
	var start wire.ThreadStartParams
	json.Unmarshal(req["params"], &start)
	if start.Permissions != p.DiagnosticProfileID() || start.Sandbox != "" || start.ApprovalPolicy != "never" || !strings.Contains(start.DeveloperInstructions, "Network is disabled") {
		t.Fatalf("diagnostic policy: %+v", start)
	}
	writeServerJSON(t, responses, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "diagnostic-thread"}, "cwd": b.RuntimeWorkspaceRoots[0]}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartTurn(context.Background(), TurnOptions{ThreadID: "diagnostic-thread", RunID: "child-run", Prompt: "diagnose", RuntimeBinding: b, AuthorizationCeiling: AuthorizationWorkspaceWrite}); err == nil {
		t.Fatal("diagnostic accepted write Turn")
	}
	go func() {
		_, err := c.StartTurn(context.Background(), TurnOptions{ThreadID: "diagnostic-thread", RunID: "child-run", Prompt: "diagnose", RuntimeBinding: b, AuthorizationCeiling: AuthorizationReadOnly})
		done <- err
	}()
	req = readClientRequest(t, requests)
	var turn wire.TurnStartParams
	json.Unmarshal(req["params"], &turn)
	if turn.Permissions != p.DiagnosticProfileID() || turn.SandboxPolicy != nil || turn.ApprovalPolicy != "never" {
		t.Fatal("diagnostic Turn inherited source profile")
	}
	finishBoundTurn(t, responses, req, "diagnostic-thread", "diagnostic-turn")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTaskSandboxProcessRejectsMissingTaskBinding(t *testing.T) {
	c := &Client{executorBoundary: true, taskPermissionHash: permissionBinding().TaskPermissions.ContentHash}
	if c.requireTaskProfile(RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/source"}}) == nil {
		t.Fatal("task process used default profile without exact binding")
	}
}

func TestTaskDiagnosticObserverCannotAnswerUnownedSourceRequest(t *testing.T) {
	c, _, _ := newClientFixture(t)
	c.detached = true
	c.diagnosticTaskPolicy = permissionBinding().TaskPermissions
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	called := false
	_, err := c.processTaskSandboxRequest(ctx, ServerRequest{Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"source-thread","turnId":"old-turn"}`)}, func(context.Context, ServerRequest) (any, error) { called = true; return nil, nil })
	if called || err == nil {
		t.Fatal("observer answered unowned source request")
	}
}
