package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex/wire"
)

func TestExecutorBoundaryStartAndResumeDisableInheritedControls(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "resume"}[resume], func(t *testing.T) {
			c, requests, responses := newClientFixture(t)
			c.executorBoundary = true
			done := make(chan error, 1)
			go func() {
				if resume {
					_, err := c.ResumeThread(context.Background(), ThreadResumeOptions{ThreadID: "thread-executor", CWD: "/fixture/app", Model: "gpt-6.1-sol", ReasoningEffort: "low", ServiceTier: "priority"})
					done <- err
				} else {
					_, err := c.StartThread(context.Background(), ThreadOptions{CWD: "/fixture/app", Model: "gpt-6.1-sol", ReasoningEffort: "low", ServiceTier: "priority"})
					done <- err
				}
			}()
			request := readClientRequest(t, requests)
			if string(request["method"]) != `"config/read"` {
				t.Fatalf("must resolve config before thread effects: %s", request["method"])
			}
			var read map[string]any
			_ = json.Unmarshal(request["params"], &read)
			if read["cwd"] != "/fixture/app" || read["includeLayers"] != false {
				t.Fatal(read)
			}
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"devbridge_control": map[string]any{"enabled": true}, "project.local": map[string]any{"enabled": true}}, "features": map[string]any{"apps": true, "plugins": true}}}})
			request = readClientRequest(t, requests)
			var params wire.ThreadStartParams
			_ = json.Unmarshal(request["params"], &params)
			for _, key := range []string{"features.apps", "features.plugins", "features.remote_plugin", "features.skill_mcp_dependency_install"} {
				if params.Config[key] != false {
					t.Fatalf("%s not disabled: %+v", key, params.Config)
				}
			}
			servers := params.Config["mcp_servers"].(map[string]any)
			if len(servers) != 2 {
				t.Fatal(servers)
			}
			for _, entry := range servers {
				if entry.(map[string]any)["enabled"] != false {
					t.Fatal(servers)
				}
			}
			if params.Model != "gpt-6.1-sol" || params.Config["model_reasoning_effort"] != "low" || params.DeveloperInstructions != executorInstructions || params.Sandbox != "workspace-write" {
				t.Fatalf("runtime contract lost: %+v", params)
			}
			raw, _ := json.Marshal(params)
			for _, forbidden := range []string{"aiSessionId", "taskId", "featureAreaId", "reviewId", "executionId"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("workflow field leaked: %s", forbidden)
				}
			}
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "thread-executor"}, "cwd": "/fixture/app"}})
			request = readClientRequest(t, requests)
			if string(request["method"]) != `"mcpServerStatus/list"` {
				t.Fatal(request)
			}
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"data": []any{map[string]any{"name": "devbridge_control", "runtimeStatus": "disabled", "tools": map[string]any{}}}, "nextCursor": "page2"}})
			request = readClientRequest(t, requests)
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"data": []any{}, "nextCursor": nil}})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !c.executorThreads["thread-executor"] {
				t.Fatal("isolated thread not qualified")
			}
		})
	}
}

func TestExecutorBoundaryFailsClosedBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		result   any
		rpcError bool
	}{
		{"config-missing", map[string]any{}, false},
		{"config-malformed", map[string]any{"config": map[string]any{"mcp_servers": []any{"bad"}}}, false},
		{"config-error", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, requests, responses := newClientFixture(t)
			c.executorBoundary = true
			done := make(chan error, 1)
			go func() { _, err := c.StartThread(context.Background(), ThreadOptions{CWD: "/fixture/app"}); done <- err }()
			req := readClientRequest(t, requests)
			if tc.rpcError {
				writeServerJSON(t, responses, map[string]any{"id": req["id"], "error": map[string]any{"code": -32000, "message": "config unavailable"}})
			} else {
				writeServerJSON(t, responses, map[string]any{"id": req["id"], "result": tc.result})
			}
			if err := <-done; err == nil || !strings.Contains(err.Error(), "CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE") {
				t.Fatal(err)
			}
		})
	}
	for _, data := range []any{
		[]any{map[string]any{"name": "controls", "runtimeStatus": "ready", "tools": map[string]any{"workspace_get": map[string]any{}}}},
		[]any{map[string]any{"name": "controls", "runtimeStatus": "starting", "tools": map[string]any{}}},
		nil,
	} {
		c, requests, responses := newClientFixture(t)
		c.executorBoundary = true
		done := make(chan error, 1)
		go func() { done <- c.verifyExecutorTools(context.Background(), "thread") }()
		req := readClientRequest(t, requests)
		writeServerJSON(t, responses, map[string]any{"id": req["id"], "result": map[string]any{"data": data}})
		if err := <-done; err == nil {
			t.Fatal("accepted unproven inventory")
		}
	}
	c, _, _ := newClientFixture(t)
	c.executorBoundary = true
	if _, err := c.StartTurn(context.Background(), TurnOptions{ThreadID: "unbound", Prompt: "work"}); err == nil {
		t.Fatal("unbound cold dispatch accepted")
	}
}

// Opt-in integration: real native protocol, isolated temporary CODEX_HOME, no
// credentials and no model call. Both launch paths use the same isolation policy.
func TestExecutorBoundaryNative(t *testing.T) {
	exe := os.Getenv("DEVBRIDGE_BOUNDARY_NATIVE")
	if exe == "" {
		t.Skip("set DEVBRIDGE_BOUNDARY_NATIVE to a supported native Codex executable")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	config := `features.apps = true
features.plugins = true
[mcp_servers.devbridge_control_fixture]
command = "/does-not-exist/must-not-launch"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, Config{CodexPath: exe, DynamicToolHandler: func(context.Context, DynamicToolCallParams) (DynamicToolCallResponse, error) {
		return DynamicToolCallResponse{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	thread, err := c.StartThread(ctx, ThreadOptions{CWD: home})
	if err != nil {
		t.Fatal(err)
	}
	if !c.executorThreads[thread.ID] {
		t.Fatal("native thread not qualified")
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config+"\n[mcp_servers.new_control]\ncommand=\"/does-not-exist/must-not-launch\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// The first turn is needed before Codex persists a resumable rollout; inspect
	// fresh start again to prove subsequent config names are not cached.
	if _, err := c.StartThread(ctx, ThreadOptions{CWD: home}); err != nil {
		t.Fatal(err)
	}
}

// Opt-in paid smoke test uses the installed user's normal authentication, never
// a production Bridge Task. It exercises native file/command work and resume.
func TestExecutorBoundaryNativeEngineering(t *testing.T) {
	exe, model := os.Getenv("DEVBRIDGE_BOUNDARY_NATIVE"), os.Getenv("DEVBRIDGE_BOUNDARY_MODEL")
	if exe == "" || model == "" {
		t.Skip("set native executable and model for engineering smoke test")
	}
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c, err := Start(ctx, Config{CodexPath: exe, DynamicToolHandler: func(context.Context, DynamicToolCallParams) (DynamicToolCallResponse, error) {
		return DynamicToolCallResponse{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	binding := RuntimeBinding{RuntimeWorkspaceRoots: []string{cwd}, WritableRoots: []string{cwd}}
	thread, err := c.StartThread(ctx, ThreadOptions{CWD: cwd, RuntimeBinding: binding, Model: model, ReasoningEffort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.StartTurn(ctx, TurnOptions{ThreadID: thread.ID, RuntimeBinding: binding, Model: model, ReasoningEffort: "low", AuthorizationCeiling: AuthorizationWorkspaceWrite, Prompt: "Create boundary-proof.txt containing exactly executor-ok followed by a newline in the current workspace. Run a shell command that reads this file and verifies its exact content. Report the actual work and command result briefly. Do not change any other files."})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(cwd, "boundary-proof.txt"))
	if err != nil || string(content) != "executor-ok\n" {
		t.Fatalf("engineering output=%q err=%v report=%s", content, err, result.FinalMessage)
	}
	success := false
	for _, command := range result.Commands {
		if command.ExitCode != nil && *command.ExitCode == 0 {
			success = true
		}
	}
	if !success {
		t.Fatalf("no observed successful command: %+v", result.Commands)
	}
	if _, err := c.ResumeThread(ctx, ThreadResumeOptions{ThreadID: thread.ID, CWD: cwd, RuntimeBinding: binding, Model: model, ReasoningEffort: "low"}); err != nil {
		t.Fatal(err)
	}
	t.Logf("native engineering work, observed command, isolated resume PASS; report: %s", result.FinalMessage)
}

func TestExecutorBoundaryColdActiveThreadCannotBeReconfigured(t *testing.T) {
	c, requests, responses := newClientFixture(t)
	c.executorBoundary = true
	done := make(chan error, 1)
	go func() {
		done <- c.ensureExecutorThread(context.Background(), TurnOptions{ThreadID: "exact-thread"}, RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/app"}, WritableRoots: []string{"/fixture/app"}})
	}()
	req := readClientRequest(t, requests)
	if string(req["method"]) != `"thread/read"` {
		t.Fatal(req)
	}
	writeServerJSON(t, responses, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "exact-thread", "cwd": "/fixture/app", "status": map[string]any{"type": "active"}}}})
	if err := <-done; err == nil {
		t.Fatal("reconfigured an active cold thread")
	}
}

func TestExecutorBoundaryNativeIndependent(t *testing.T) {
	exe := os.Getenv("DEVBRIDGE_BOUNDARY_NATIVE")
	if exe == "" {
		t.Skip("set native executable for independent-server boundary check")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("features.apps=true\nfeatures.plugins=true\n[mcp_servers.control]\ncommand=\"/does-not-exist/must-not-launch\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b, err := appserver.Launch(ctx, appserver.Spec{DataRoot: t.TempDir(), ExecutablePath: exe, CodexHome: home})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := appserver.Stop(context.Background(), b); err != nil {
			t.Error(err)
		}
	}()
	c, err := Start(ctx, Config{AppServerBinding: &b})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.executorBoundary {
		t.Fatal("attached client lacks executor boundary")
	}
	if _, err := c.StartThread(ctx, ThreadOptions{CWD: home}); err != nil {
		t.Fatal(err)
	}
}
