package codex

import (
	"context"
	"testing"
	"time"
)

func TestRecoveryChildReadLoadedThreadRejectsForeignOrBusyThread(t *testing.T) {
	for _, scenario := range []string{"exact", "not-loaded", "wrong-id", "wrong-cwd", "busy"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRPCFixture(t, nil, nil)
			c := &Client{detached: true, rpc: f.transport}
			binding := RuntimeBinding{RuntimeWorkspaceRoots: []string{"/managed/child"}}
			done := make(chan error, 1)
			go func() {
				_, err := c.readLoadedRecoveryChildThread(context.Background(), "child", "/managed/child", binding)
				done <- err
			}()
			request := f.readRequest(t)
			if string(request["method"]) != `"thread/loaded/list"` {
				t.Fatalf("unexpected effect %s", request["method"])
			}
			loaded := []string{"child"}
			if scenario == "not-loaded" {
				loaded = []string{"source"}
			}
			f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"data": loaded}})
			if scenario != "not-loaded" {
				request = f.readRequest(t)
				if string(request["method"]) != `"thread/read"` {
					t.Fatalf("unexpected effect %s", request["method"])
				}
				id, cwd, status := "child", "/managed/child", "idle"
				switch scenario {
				case "wrong-id":
					id = "source"
				case "wrong-cwd":
					cwd = "/managed/source"
				case "busy":
					status = "active"
				}
				f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": id, "cwd": cwd, "status": map[string]string{"type": status}}}})
			}
			select {
			case err := <-done:
				if (err == nil) != (scenario == "exact") {
					t.Fatalf("scenario=%s err=%v", scenario, err)
				}
				if scenario == "exact" {
					if !sameRuntimeBinding(c.threadBindings["child"], binding) {
						t.Fatal("exact frozen binding not installed")
					}
				} else if len(c.threadBindings) != 0 {
					t.Fatal("failed observation installed binding")
				}
			case <-time.After(time.Second):
				t.Fatal("readback failed to finish; must not fall back to start/resume")
			}
		})
	}
}

func TestRecoveryChildSubscriptionReconcilesExactTerminalRead(t *testing.T) {
	for _, scenario := range []string{"completed-before-subscription", "rollout-metadata-flush", "wrong-root", "wrong-turn"} {
		t.Run(scenario, func(t *testing.T) {
			var c *Client
			f := newRPCFixture(t, nil, func(event Event) { c.handleEvent(event) })
			key := turnKey{threadID: "child", turnID: "accepted"}
			state := &turnState{waiter: make(chan struct{}), commands: make(map[string]CommandExecution)}
			c = &Client{detached: true, rpc: f.transport, turns: map[turnKey]*turnState{key: state}, retired: map[turnKey]uint64{}, starting: map[string]*turnStartState{}}
			done := make(chan error, 1)
			go func() {
				done <- c.subscribeRecoveryChildTurn(context.Background(), "child", "accepted", "/managed/child")
			}()
			request := f.readRequest(t)
			if string(request["method"]) != `"thread/resume"` || string(request["params"]) != `{"excludeTurns":true,"threadId":"child"}` {
				t.Fatalf("subscription changed authority: %s %s", request["method"], request["params"])
			}
			if scenario == "rollout-metadata-flush" {
				f.write(t, map[string]any{"id": request["id"], "error": map[string]any{"code": -32603, "message": "rollout for child is empty"}})
				request = f.readRequest(t)
				if string(request["method"]) != `"thread/resume"` || string(request["params"]) != `{"excludeTurns":true,"threadId":"child"}` {
					t.Fatalf("retry changed authority: %s %s", request["method"], request["params"])
				}
			}
			f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]string{"id": "child"}}})
			request = f.readRequest(t)
			if string(request["method"]) != `"thread/loaded/list"` {
				t.Fatal(request)
			}
			f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"data": []string{"child"}}})
			request = f.readRequest(t)
			if string(request["method"]) != `"thread/read"` {
				t.Fatal(request)
			}
			cwd, turn := "/managed/child", "accepted"
			if scenario == "wrong-root" {
				cwd = "/foreign"
			}
			if scenario == "wrong-turn" {
				turn = "foreign"
			}
			f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "child", "cwd": cwd, "status": map[string]string{"type": "idle"}, "turns": []any{map[string]any{"id": turn, "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "text": "exact result"}}}}}}})
			request = f.readRequest(t)
			if string(request["method"]) != `"thread/turns/list"` {
				t.Fatal(request)
			}
			f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"data": []any{map[string]any{"id": turn, "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "text": "exact result"}}}}}})
			select {
			case err := <-done:
				if scenario == "completed-before-subscription" || scenario == "rollout-metadata-flush" {
					if err != nil || state.completion == nil || state.completion.Turn.ID != "accepted" {
						t.Fatalf("terminal read not reconciled: %+v %v", state, err)
					}
				} else if err == nil || state.completion != nil {
					t.Fatalf("foreign terminal accepted: %+v %v", state, err)
				}
			case <-time.After(time.Second):
				t.Fatal("subscription blocked")
			}
		})
	}
}
