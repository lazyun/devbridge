package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRecoveryStatusSameThreadExactPromptAndPreEffectFence(t *testing.T) {
	for _, fenceFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-turn", true: "durable-fence-rejected"}[fenceFails], func(t *testing.T) {
			var c *Client
			f := newRPCFixture(t, nil, func(e Event) { c.handleEvent(e) })
			c = &Client{detached: true, rpc: f.transport, turns: map[turnKey]*turnState{}, retired: map[turnKey]uint64{}, starting: map[string]*turnStartState{}}
			instruction := "  调查状态\nverbatim\t\"bytes\"  "
			schema := json.RawMessage(`{"type":"object","properties":{"version":{"type":"integer"}},"required":["version"],"additionalProperties":false}`)
			fenced := false
			done := make(chan error, 1)
			go func() {
				_, e := c.StartRecoveryStatusTurn(context.Background(), RecoveryStatusTurnOptions{TurnOptions: TurnOptions{RunID: "new-run", ThreadID: "canonical-thread", Prompt: instruction, OutputSchema: schema, Model: "frozen-model", RuntimeBinding: RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/app", "/fixture/sdk"}, WritableRoots: []string{"/fixture/app", "/fixture/sdk"}}}, SourceTurnID: "source-turn", ExpectedCWD: "/fixture/app", BeforeDispatch: func() error {
					fenced = true
					if fenceFails {
						return errors.New("durable intent unavailable")
					}
					return nil
				}})
				done <- e
			}()
			req := f.readRequest(t)
			if string(req["method"]) != `"thread/loaded/list"` {
				t.Fatal(req)
			}
			f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []string{"canonical-thread"}}})
			req = f.readRequest(t)
			if string(req["method"]) != `"thread/read"` {
				t.Fatal(req)
			}
			f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "canonical-thread", "cwd": "/fixture/app", "status": map[string]string{"type": "idle"}, "turns": []any{map[string]any{"id": "source-turn", "status": "completed"}}}}})
			req = f.readRequest(t)
			if string(req["method"]) != `"thread/turns/list"` {
				t.Fatal(req)
			}
			f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []any{map[string]any{"id": "source-turn", "status": "completed"}}}})
			req = f.readRequest(t)
			if string(req["method"]) != `"thread/resume"` || string(req["params"]) != `{"excludeTurns":true,"threadId":"canonical-thread"}` {
				t.Fatalf("resume changed identity/config: %s %s", req["method"], req["params"])
			}
			f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "canonical-thread"}}})
			if !fenceFails {
				req = f.readRequest(t)
				if string(req["method"]) != `"turn/start"` || !fenced {
					t.Fatal(req)
				}
				var p struct {
					ThreadID string `json:"threadId"`
					Input    []struct {
						Text string `json:"text"`
					} `json:"input"`
					OutputSchema  json.RawMessage `json:"outputSchema"`
					SandboxPolicy struct {
						Type string `json:"type"`
					} `json:"sandboxPolicy"`
					Model string   `json:"model"`
					Roots []string `json:"runtimeWorkspaceRoots"`
				}
				if e := json.Unmarshal(req["params"], &p); e != nil {
					t.Fatal(e)
				}
				if p.ThreadID != "canonical-thread" || len(p.Input) != 1 || p.Input[0].Text != instruction || string(p.OutputSchema) != string(schema) || p.SandboxPolicy.Type != "readOnly" || p.Model != "frozen-model" || len(p.Roots) != 2 {
					t.Fatalf("changed wire input: %s", req["params"])
				}
				f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"turn": map[string]any{"id": "new-turn", "status": "inProgress"}}})
				f.write(t, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical-thread", "turn": map[string]any{"id": "new-turn", "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "text": `{"version":1}`}}}}})
			}
			select {
			case e := <-done:
				if (e != nil) != fenceFails {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				t.Fatal("recovery turn blocked")
			}
		})
	}
}

func TestRecoveryChildThreadDiffersFromSourceThread(t *testing.T) {
	var c *Client
	f := newRPCFixture(t, nil, func(e Event) { c.handleEvent(e) })
	c = &Client{detached: true, rpc: f.transport, turns: map[turnKey]*turnState{}, retired: map[turnKey]uint64{}, starting: map[string]*turnStartState{}}
	binding := RuntimeBinding{RuntimeWorkspaceRoots: []string{"/managed/child"}, WritableRoots: []string{}}
	stage := []string{}
	done := make(chan error, 1)
	go func() {
		_, err := c.StartRecoveryStatusChildTurn(context.Background(), RecoveryStatusChildTurnOptions{
			SourceThreadID: "source-thread", SourceTurnID: "source-turn", SourceCWD: "/managed/source",
			ChildThread:        ThreadOptions{CWD: "/managed/child", RuntimeBinding: binding, Model: "frozen-model"},
			ChildTurn:          TurnOptions{RunID: "child-run", Prompt: "Diagnose source provenance in natural language", RuntimeBinding: binding},
			BeforeThreadCreate: func() error { stage = append(stage, "claim-thread"); return nil },
			OnThreadCreated: func(id string) error {
				if id != "child-thread" {
					return errors.New("wrong child thread")
				}
				stage = append(stage, "receipt-thread")
				return nil
			},
			BeforeTurnDispatch: func(id string) error {
				if id != "child-thread" {
					return errors.New("wrong turn thread")
				}
				stage = append(stage, "claim-turn")
				return nil
			},
		})
		done <- err
	}()
	req := f.readRequest(t)
	if string(req["method"]) != `"thread/loaded/list"` {
		t.Fatal(req)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []string{"source-thread"}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/read"` {
		t.Fatal(req)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "source-thread", "cwd": "/managed/source", "status": map[string]string{"type": "idle"}, "turns": []any{map[string]any{"id": "source-turn", "status": "completed"}}}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/turns/list"` {
		t.Fatal(req)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []any{map[string]any{"id": "source-turn", "status": "completed"}}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/start"` {
		t.Fatal(req)
	}
	var threadParams struct {
		CWD   string   `json:"cwd"`
		Roots []string `json:"runtimeWorkspaceRoots"`
	}
	if err := json.Unmarshal(req["params"], &threadParams); err != nil || threadParams.CWD != "/managed/child" || len(threadParams.Roots) != 1 || threadParams.Roots[0] != "/managed/child" {
		t.Fatalf("child thread root: %s err=%v", req["params"], err)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "child-thread"}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"turn/start"` {
		t.Fatal(req)
	}
	var rawTurn map[string]json.RawMessage
	if err := json.Unmarshal(req["params"], &rawTurn); err != nil {
		t.Fatal(err)
	}
	if _, present := rawTurn["outputSchema"]; present {
		t.Fatal("natural recovery must omit outputSchema")
	}
	var turnParams struct {
		ThreadID string   `json:"threadId"`
		Roots    []string `json:"runtimeWorkspaceRoots"`
	}
	if err := json.Unmarshal(req["params"], &turnParams); err != nil || turnParams.ThreadID != "child-thread" || len(turnParams.Roots) != 1 || turnParams.Roots[0] != "/managed/child" {
		t.Fatalf("child turn root: %s err=%v", req["params"], err)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"turn": map[string]any{"id": "child-turn", "status": "inProgress"}}})
	f.write(t, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "child-thread", "turn": map[string]any{"id": "child-turn", "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "text": "{}"}}}}})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("child turn blocked")
	}
	if len(stage) != 3 || stage[0] != "claim-thread" || stage[1] != "receipt-thread" || stage[2] != "claim-turn" {
		t.Fatalf("authority stages: %v", stage)
	}
}

func TestRecoveryStatusRejectsUnprovenThreadBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, threadStatus, turnStatus, cwd string
		loaded                              bool
	}{
		{"active-thread", "active", "completed", "/fixture/app", true},
		{"nonterminal-turn", "idle", "inProgress", "/fixture/app", true},
		{"unavailable-thread", "idle", "completed", "/fixture/app", false},
		{"root-mismatch", "idle", "completed", "/other/root", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRPCFixture(t, nil, nil)
			c := &Client{detached: true, rpc: f.transport}
			dispatched := false
			done := make(chan error, 1)
			go func() {
				_, e := c.StartRecoveryStatusTurn(context.Background(), RecoveryStatusTurnOptions{TurnOptions: TurnOptions{RunID: "probe", ThreadID: "thread", Prompt: "caller", OutputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)}, SourceTurnID: "source", ExpectedCWD: "/fixture/app", BeforeDispatch: func() error { dispatched = true; return nil }})
				done <- e
			}()
			req := f.readRequest(t)
			ids := []string{}
			if tc.loaded {
				ids = []string{"thread"}
			}
			f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": ids}})
			if tc.loaded {
				req = f.readRequest(t)
				f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "thread", "cwd": tc.cwd, "status": map[string]string{"type": tc.threadStatus}, "turns": []any{map[string]any{"id": "source", "status": tc.turnStatus}}}}})
				req = f.readRequest(t)
				if string(req["method"]) != `"thread/turns/list"` {
					t.Fatal(req)
				}
				f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []any{map[string]any{"id": "source", "status": tc.turnStatus}}}})
			}
			select {
			case e := <-done:
				if e == nil || dispatched {
					t.Fatalf("admitted %v, dispatch=%v", e, dispatched)
				}
			case <-time.After(time.Second):
				t.Fatal("blocked")
			}
		})
	}
}

func TestRecoveryStatusLostStartResponseLeavesTurnUnproven(t *testing.T) {
	f := newRPCFixture(t, nil, nil)
	c := &Client{detached: true, rpc: f.transport, turns: map[turnKey]*turnState{}, retired: map[turnKey]uint64{}, starting: map[string]*turnStartState{}}
	calls := 0
	done := make(chan TurnResult, 1)
	errs := make(chan error, 1)
	go func() {
		r, e := c.StartRecoveryStatusTurn(context.Background(), RecoveryStatusTurnOptions{TurnOptions: TurnOptions{RunID: "probe", ThreadID: "thread", Prompt: "exact caller", OutputSchema: json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`), RuntimeBinding: RuntimeBinding{RuntimeWorkspaceRoots: []string{"/fixture/app"}}}, SourceTurnID: "source", ExpectedCWD: "/fixture/app", BeforeDispatch: func() error { calls++; return nil }})
		done <- r
		errs <- e
	}()
	req := f.readRequest(t)
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []string{"thread"}}})
	req = f.readRequest(t)
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "thread", "cwd": "/fixture/app", "status": map[string]string{"type": "idle"}, "turns": []any{map[string]any{"id": "source", "status": "completed"}}}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/turns/list"` {
		t.Fatal(req)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []any{map[string]any{"id": "source", "status": "completed"}}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/resume"` {
		t.Fatal(req)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "thread"}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"turn/start"` {
		t.Fatal(req)
	}
	// The backend may have accepted this exact call. Connection loss is not
	// proof that starting a replacement Turn would be safe.
	if e := f.transport.Detach(); e != nil {
		t.Fatal(e)
	}
	select {
	case r := <-done:
		if r.TurnID != "" || calls != 1 {
			t.Fatalf("invented turn or retry: %+v calls=%d", r, calls)
		}
		if e := <-errs; e == nil {
			t.Fatal("lost response accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("response loss blocked")
	}
}
