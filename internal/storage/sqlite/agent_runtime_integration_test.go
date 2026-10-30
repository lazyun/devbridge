package sqlite

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestAgentRuntimeRealTransportSQLiteAcceptance(t *testing.T) {
	for _, scenario := range []string{"command", "file", "permissions", "input", "elicitation", "interrupt", "loss", "steering"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAgentStorageFixture(t, "official-"+scenario)
			r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "official-"+scenario, false)
			ctx, cancel := context.WithTimeout(runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID), 10*time.Second)
			defer cancel()
			conversations := NewAgentConversationStore(f.db)
			runs := run.NewService(NewRunStore(f.db), nil, nil)
			if err := conversations.MarkThreadStartPending(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DEVBRIDGE_AGENT_STDIO_FIXTURE", scenario)
			command := filepath.Join(t.TempDir(), "codex-fixture")
			binary := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
			if err := os.WriteFile(command, []byte("#!/bin/sh\nexec "+binary+" -test.run=^TestAgentRuntimeStdioHelper$ -- \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			store := NewAgentRuntimeStore(f.db)
			service := agentruntime.NewService(store)
			client, err := codex.Start(ctx, codex.Config{CodexPath: command, AgentRuntime: service, UsageStore: NewCodexUsageStore(f.db), ApprovalMode: codex.ApprovalModeInteractive})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			thread, err := client.StartThread(ctx, codex.ThreadOptions{CWD: f.root, Model: "gpt-5.6-terra"})
			if err != nil {
				t.Fatal(err)
			}
			if err = conversations.BindThread(ctx, r.ID, thread.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = runs.AttachThread(ctx, r.ID, thread.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = runs.Transition(ctx, r.ID, run.StatusRunning); err != nil {
				t.Fatal(err)
			}
			if err = conversations.MarkTurnPending(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			type result struct {
				value codex.TurnResult
				err   error
			}
			done := make(chan result, 1)
			go func() {
				v, e := client.StartTurn(ctx, codex.TurnOptions{Model: "gpt-5.6-terra", RunID: r.ID, ThreadID: thread.ID, Prompt: "fixture", OnStarted: func(_, turnID string) error { _, e := runs.AttachTurn(ctx, r.ID, turnID); return e }})
				done <- result{v, e}
			}()
			var state agentruntime.Snapshot
			for {
				state, err = store.Read(ctx, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if (len(state.Requests) > 0 || scenario == "steering") && state.LatestPlan != nil && state.CurrentItem != nil && state.CurrentItem.OutputBytes > 0 && state.Agent.SessionName == "Official renamed fixture" {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("pending request not observed", ctx.Err())
				case result := <-done:
					t.Fatalf("turn ended before request: %+v", result)
				case <-time.After(time.Millisecond * 5):
				}
			}
			if state.LatestPlan == nil || state.LatestPlan.Total != 5 || state.CurrentItem == nil || state.CurrentItem.Type != "commandExecution" || state.CurrentItem.OutputBytes == 0 {
				t.Fatalf("missing telemetry: %+v", state)
			}
			if state.LatestPlan.Completed != 2 || state.Agent.PID <= 0 || state.Agent.ProcessLaunchID == "" || state.Agent.SessionName != "Official renamed fixture" || !strings.Contains(state.CurrentItem.OutputTail, fmt.Sprintf("PID %d", state.Agent.PID)) {
				t.Fatalf("official launch/name/plan %+v", state)
			}
			if scenario == "steering" {
				view, e := service.Steering(ctx, r.ID)
				if e != nil || !view.Capability.Allowed {
					t.Fatalf("transport owner did not expose steering: %+v %v", view, e)
				}
				input := agentruntime.SteeringInput{Identity: *view.Capability.Target, SubmissionRequestID: "transport-steering", Text: "exact clarification", SubmittedBy: "operator:test"}
				receipt, e := service.Steer(ctx, input)
				if e != nil || receipt.State != "ACCEPTED" {
					t.Fatalf("transport steering: %+v %v", receipt, e)
				}
				select {
				case got := <-done:
					if got.err != nil || got.value.TurnID != input.TurnID {
						t.Fatalf("replacement or failed original turn: %+v", got)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if replay, e := service.Steer(ctx, input); e != nil || replay.State != "ACCEPTED" {
					t.Fatalf("terminal replay: %+v %v", replay, e)
				}
				return
			}
			store.Now = func() time.Time { return state.LastActivityAt.Add(43 * time.Minute) }
			waited, e := store.Read(ctx, r.ID)
			if e != nil || waited.AgentHealth != "WAITING" {
				t.Fatalf("waiting precedence %+v %v", waited, e)
			}
			store.Now = time.Now
			durable, err := runs.Get(ctx, r.ID)
			if err != nil || durable.Status != run.StatusRunning {
				t.Fatalf("telemetry changed Run authority: %+v %v", durable, err)
			}
			request := state.Requests[0]
			if scenario == "loss" {
				client.Close()
			} else if scenario == "interrupt" {
				if err = client.InterruptTurn(ctx, thread.ID, state.TurnID); err != nil {
					t.Fatal(err)
				}
			} else {
				response := json.RawMessage(`{"decision":"accept"}`)
				switch scenario {
				case "permissions":
					response = json.RawMessage(`{"permissions":{"fileSystem":{"read":["/tmp/a"]}},"scope":"session"}`)
				case "input":
					response = json.RawMessage(`{"answers":{"q":{"answers":["custom"]}}}`)
				case "elicitation":
					response = json.RawMessage(`{"action":"accept","content":{"name":"Ada"}}`)
				}
				resolution := agentruntime.Resolution{Identity: request.Identity, ItemID: request.ItemID, RequestID: request.RequestID, Response: response}
				if request.Kind == "CODEX_REQUEST" {
					in := agentruntime.UserDecisionInput{Identity: request.Identity, RuntimeRequestID: request.ID, SubmissionRequestID: "explicit-user-" + scenario, ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", SourceSurface: "TEST", SubmittedBy: "user:test"}
					if scenario == "input" {
						in.RawText = "custom"
					} else {
						in.SelectedOptionID = "accept"
						in.RawText = `{"name":"Ada"}`
					}
					in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
					d, e := service.SubmitUserDecision(ctx, in)
					if e != nil {
						t.Fatal(e)
					}
					resolution.Response = nil
					resolution.RuntimeRequestID = request.ID
					resolution.UserDecisionID = d.ID
					resolution.ExpectedState = "USER_DECISION_RECORDED"
					resolution.PayloadSHA256 = d.PayloadSHA256
				}
				_, err = service.Resolve(ctx, resolution)
				if err != nil {
					t.Fatal(err)
				}
			}
			var got result
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			state, err = store.Read(context.Background(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			client.Close()
			exited, e := store.Read(context.Background(), r.ID)
			if e != nil || exited.Agent.Process == nil || exited.Agent.Process.EndedAt == nil || exited.Agent.Process.ExitCode == nil {
				t.Fatalf("process exit not durable %+v %v", exited, e)
			}
			switch scenario {
			case "loss":
				if got.err == nil || state.ConnectionState != "CONNECTION_LOST" || state.Requests[0].State != "CONNECTION_LOST" || len(state.Requests[0].Resolution) != 0 {
					t.Fatalf("fabricated resolution: %+v %v", state, got.err)
				}
			case "interrupt":
				var terminal *codex.TurnError
				if !errors.As(got.err, &terminal) || terminal.Status != "interrupted" || state.WaitingState != "RUNNING" {
					t.Fatalf("interrupt %+v %v", state, got.err)
				}
			default:
				if got.err != nil || state.TurnStatus != "completed" || (state.Requests[0].State != "CONFIRMED_RESOLVED" && state.Requests[0].State != "RESPONDED") || got.value.TokenUsage == nil || !got.value.TokenUsage.Reliable {
					t.Fatalf("protocol did not converge: %+v %+v %v", state, got.value, got.err)
				}
			}
		})
	}
}

func TestAgentRuntimeStdioHelper(t *testing.T) {
	scenario := os.Getenv("DEVBRIDGE_AGENT_STDIO_FIXTURE")
	if scenario == "" {
		return
	}
	out := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	send := func(v any) { _ = out.Encode(v) }
	event := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
	finish := func(status string) {
		event("serverRequest/resolved", map[string]any{"threadId": "fixture-thread", "requestId": "server-request"})
		event("item/completed", map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "item": map[string]any{"id": "cmd", "type": "commandExecution", "status": "completed", "command": "printf fixture", "cwd": "/tmp", "commandActions": []any{}, "exitCode": 0, "durationMs": 1, "aggregatedOutput": "fixture"}})
		event("thread/tokenUsage/updated", map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 8, "cachedInputTokens": 0, "outputTokens": 2, "reasoningOutputTokens": 0, "totalTokens": 10}, "last": map[string]any{"inputTokens": 8, "cachedInputTokens": 0, "outputTokens": 2, "reasoningOutputTokens": 0, "totalTokens": 10}}})
		event("turn/completed", map[string]any{"threadId": "fixture-thread", "turn": map[string]any{"id": "fixture-turn", "status": status, "items": []any{map[string]any{"type": "agentMessage", "id": "message", "text": "done", "phase": "final_answer"}}}})
	}
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(3)
		}
		if string(message.ID) == `"server-request"` {
			if scenario != "interrupt" && scenario != "loss" {
				if len(message.Result) == 0 {
					os.Exit(4)
				}
				finish("completed")
			}
			continue
		}
		if len(message.ID) == 0 {
			continue
		}
		result := any(map[string]any{})
		switch message.Method {
		case "config/read":
			result = map[string]any{"config": map[string]any{"mcp_servers": map[string]any{}}}
		case "mcpServerStatus/list":
			result = map[string]any{"data": []any{}, "nextCursor": nil}
		case "initialize":
			result = map[string]any{"userAgent": "fixture"}
		case "thread/start":
			result = map[string]any{"thread": map[string]any{"id": "fixture-thread", "name": "Official fixture"}}
		case "turn/start":
			event("thread/name/updated", map[string]any{"threadId": "fixture-thread", "threadName": "Official renamed fixture"})
			event("turn/started", map[string]any{"threadId": "fixture-thread", "turn": map[string]any{"id": "fixture-turn", "status": "inProgress", "items": []any{}}})
			plan := []any{}
			for i, step := range []string{"inspect", "implement", "test", "review", "finish"} {
				status := "pending"
				if i < 2 {
					status = "completed"
				} else if i == 2 {
					status = "inProgress"
				}
				plan = append(plan, map[string]any{"step": step, "status": status})
			}
			event("turn/plan/updated", map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "plan": plan})
			event("item/started", map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "item": map[string]any{"id": "cmd", "type": "commandExecution", "status": "inProgress", "command": "printf fixture", "cwd": "/tmp", "commandActions": []any{}}})
			event("item/commandExecution/outputDelta", map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "itemId": "cmd", "delta": fmt.Sprintf("ordered fixture output PID %d", os.Getpid())})
			p := map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "itemId": "cmd", "startedAtMs": 1, "cwd": "/tmp"}
			method := "item/commandExecution/requestApproval"
			switch scenario {
			case "file":
				method = "item/fileChange/requestApproval"
			case "permissions":
				method = "item/permissions/requestApproval"
				p["permissions"] = map[string]any{"fileSystem": map[string]any{"read": []string{"/tmp/a", "/tmp/b"}}}
			case "input":
				method = "item/tool/requestUserInput"
				p["isBlocking"] = true
				p["questions"] = []any{map[string]any{"id": "q", "header": "Question", "question": "Answer?", "isOther": true}}
			case "elicitation":
				method = "mcpServer/elicitation/request"
				p["serverName"] = "fixture"
				p["mode"] = "form"
				p["message"] = "Name"
				p["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}}
			}
			// Nested request BEFORE the outgoing turn/start response proves the reader
			// can make progress while a worker waits for exact durable Turn binding.
			if scenario != "steering" {
				send(map[string]any{"id": "server-request", "method": method, "params": p})
			}
			result = map[string]any{"turn": map[string]any{"id": "fixture-turn", "status": "inProgress", "items": []any{}}}
		case "turn/steer":
			var p struct {
				ThreadID       string `json:"threadId"`
				ExpectedTurnID string `json:"expectedTurnId"`
				Input          []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if scenario != "steering" || json.Unmarshal(message.Params, &p) != nil || p.ThreadID != "fixture-thread" || p.ExpectedTurnID != "fixture-turn" || len(p.Input) != 1 || p.Input[0].Text != "exact clarification" {
				os.Exit(8)
			}
			send(map[string]any{"id": message.ID, "result": map[string]string{"turnId": "fixture-turn"}})
			finish("completed")
			continue
		case "turn/interrupt":
			send(map[string]any{"id": message.ID, "result": result})
			finish("interrupted")
			continue
		}
		send(map[string]any{"id": message.ID, "result": result})
	}
	os.Exit(0)
}

func TestAgentRuntimeRestartAndWriteFences(t *testing.T) {
	f := newAgentStorageFixture(t, "runtime-restart")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "runtime-restart", true)
	ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
	store := NewAgentRuntimeStore(f.db)
	service := agentruntime.NewService(store)
	session := service.NewTurn(ctx, r.ID, "thread-runtime-restart", false)
	if err := session.Bind("turn-runtime-restart"); err != nil {
		t.Fatal(err)
	}
	p := json.RawMessage(`{"threadId":"thread-runtime-restart","turnId":"turn-runtime-restart","itemId":"i","startedAtMs":1}`)
	if err := session.Observe(agentruntime.Event{Method: "item/fileChange/requestApproval", RequestID: json.RawMessage(`9`), Params: p}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Read(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	bad := before
	bad.Sequence++
	bad.TurnID = "stale-turn"
	if err = store.Save(ctx, before.Sequence, bad, false); err == nil {
		t.Fatal("stale Turn write accepted")
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, f.root)
	defer db.Close()
	after, err := NewAgentRuntimeStore(db).Read(context.Background(), r.ID)
	if err != nil || after.Requests[0].State != "ABANDONED_RESTART" || after.Sequence != before.Sequence {
		t.Fatalf("restart replay %+v %v", after, err)
	}
	newService := agentruntime.NewService(NewAgentRuntimeStore(db))
	request := after.Requests[0]
	if _, err = newService.Resolve(context.Background(), agentruntime.Resolution{Identity: request.Identity, ItemID: request.ItemID, RequestID: request.RequestID, Response: json.RawMessage(`{"decision":"accept"}`)}); !errors.Is(err, agentruntime.ErrStale) {
		t.Fatal("old request replayed", err)
	}
	session.Close()
}
