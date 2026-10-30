package codex

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex/wire"
	"io"
	"sync"
	"testing"
	"time"
)

func TestRPCDetachNeverAnswersPendingServerQuestion(t *testing.T) {
	entered := make(chan struct{})
	f := newRPCFixture(t, func(ctx context.Context, r ServerRequest) (any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	f.write(t, map[string]any{"id": "question", "method": "item/tool/requestUserInput", "params": map[string]any{}})
	<-entered
	ended := make(chan error, 1)
	go func() { ended <- f.transport.Detach() }()
	select {
	case e := <-ended:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("detach blocked")
	}
	b, e := f.requests.ReadBytes('\n')
	if e != io.EOF || len(b) != 0 {
		t.Fatalf("detach wrote server answer: %s %v", b, e)
	}
}
func TestRejoinLoadedTurnUsesOriginalTurnWithoutStart(t *testing.T) {
	var c *Client
	f := newRPCFixture(t, nil, func(e Event) { c.handleEvent(e) })
	c = &Client{detached: true, rpc: f.transport, turns: map[turnKey]*turnState{}, retired: map[turnKey]uint64{}, starting: map[string]*turnStartState{}}
	done := make(chan error, 1)
	go func() {
		r, e := c.RejoinTurn(context.Background(), TurnRejoinOptions{TurnOptions: TurnOptions{ThreadID: "thread"}, TurnID: "turn"})
		if e == nil && (r.TurnID != "turn" || r.FinalMessage != "original result") {
			e = io.ErrUnexpectedEOF
		}
		done <- e
	}()
	req := f.readRequest(t)
	if string(req["method"]) != `"thread/loaded/list"` {
		t.Fatal(string(req["method"]))
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []string{"thread"}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/read"` {
		t.Fatal(string(req["method"]))
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "thread", "status": map[string]string{"type": "active"}, "turns": []any{map[string]any{"id": "turn", "status": "inProgress"}}}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/turns/list"` {
		t.Fatal(string(req["method"]))
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []any{map[string]any{"id": "turn", "status": "inProgress"}}}})
	req = f.readRequest(t)
	if string(req["method"]) != `"thread/resume"` {
		t.Fatal(string(req["method"]))
	}
	var p map[string]any
	json.Unmarshal(req["params"], &p)
	if len(p) != 2 || p["threadId"] != "thread" || p["excludeTurns"] != true {
		t.Fatalf("resume mutated config: %v", p)
	}
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]string{"id": "thread"}}})
	f.write(t, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "text": "original result"}}}}})
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("rejoin did not complete")
	}
}
func TestRejoinRejectsUnloadedThreadWithoutColdResume(t *testing.T) {
	f := newRPCFixture(t, nil, nil)
	c := &Client{detached: true, rpc: f.transport}
	done := make(chan error, 1)
	go func() { _, e := c.ObserveLoadedTurn(context.Background(), "missing", "turn"); done <- e }()
	req := f.readRequest(t)
	f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []string{}}})
	if e := <-done; e == nil {
		t.Fatal("accepted unloaded thread")
	}
}

func TestAppServerQuiescenceRequiresEveryLoadedThreadIdle(t *testing.T) {
	for _, status := range []string{"idle", "active", "notLoaded"} {
		t.Run(status, func(t *testing.T) {
			f := newRPCFixture(t, nil, nil)
			c := &Client{detached: true, rpc: f.transport}
			done := make(chan bool, 1)
			go func() { quiet, err := c.AppServerQuiescent(context.Background()); done <- quiet && err == nil }()
			request := f.readRequest(t)
			f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"data": []string{"one", "two"}}})
			for _, thread := range []string{"one", "two"} {
				request = f.readRequest(t)
				value := "idle"
				if thread == "two" {
					value = status
				}
				f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": thread, "status": map[string]string{"type": value}}}})
			}
			if got := <-done; got != (status == "idle") {
				t.Fatalf("quiescent=%v status=%s", got, status)
			}
		})
	}
}

type detachRuntimeStore struct {
	mu       sync.Mutex
	snapshot agentruntime.Snapshot
}

func (s *detachRuntimeStore) Open(_ context.Context, id agentruntime.Identity) (agentruntime.Identity, error) {
	id.ProjectID = "project"
	id.FeatureAreaID = "area"
	id.TaskID = "task"
	return id, nil
}
func (s *detachRuntimeStore) Save(_ context.Context, _ uint64, snapshot agentruntime.Snapshot, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(snapshot)
	return json.Unmarshal(raw, &s.snapshot)
}
func (s *detachRuntimeStore) Read(_ context.Context, _ string) (agentruntime.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var copy agentruntime.Snapshot
	raw, _ := json.Marshal(s.snapshot)
	err := json.Unmarshal(raw, &copy)
	return copy, err
}
func TestDetachedStartTurnCancellationNeverAnswersQuestion(t *testing.T) {
	store := &detachRuntimeStore{}
	runtime := agentruntime.NewService(store)
	var c *Client
	f := newRPCFixture(t, func(ctx context.Context, r ServerRequest) (any, error) {
		return c.processRuntimeRequest(ctx, r, builtInServerRequestPolicy)
	}, func(e Event) { c.handleEvent(e) })
	c = &Client{detached: true, rpc: f.transport, agentRuntime: runtime, runtimeSessions: map[string]*agentruntime.Session{}, threadNames: map[string]string{}, turns: map[turnKey]*turnState{}, retired: map[turnKey]uint64{}, starting: map[string]*turnStartState{}, threadUsage: map[string]threadUsageState{}}
	f.transport.requestObserver = func(r ServerRequest) { c.observeRuntime(Event{Method: r.Method, Params: r.Params, RequestID: r.ID}) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.StartTurn(ctx, TurnOptions{RunID: "run", ThreadID: "thread", Prompt: "fixture"})
		done <- err
	}()
	request := f.readRequest(t)
	f.write(t, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]string{"id": "turn", "status": "inProgress"}}})
	f.write(t, map[string]any{"id": "question", "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item", "isBlocking": true, "questions": []any{map[string]any{"id": "q", "header": "Header", "question": "Choose?"}}}})
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, _ := runtime.Read(context.Background(), "run")
		if len(snapshot.Requests) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("question not durable")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled turn succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	bytes, err := f.requests.ReadBytes('\n')
	if len(bytes) != 0 || err != io.EOF {
		t.Fatalf("detach manufactured question response: %s %v", bytes, err)
	}
}

func TestRecoveredTerminalReadPersistsRuntimeTerminalEvidence(t *testing.T) {
	store := &detachRuntimeStore{}
	service := agentruntime.NewService(store)
	session := service.NewTurn(context.Background(), "run", "thread", false)
	if err := session.Bind("turn"); err != nil {
		t.Fatal(err)
	}
	observed := wire.Turn{ID: "turn", Status: "completed", Items: []json.RawMessage{json.RawMessage(`{"type":"agentMessage","id":"message","text":"original terminal result","phase":null}`)}}
	if err := observeRecoveredTerminal(session, "thread", observed); err != nil {
		t.Fatal(err)
	}
	session.Close()
	snapshot, err := service.Read(context.Background(), "run")
	if err != nil || snapshot.TurnStatus != "completed" || snapshot.ConnectionState != "CLOSED" {
		t.Fatalf("lost terminal read evidence: %+v %v", snapshot, err)
	}
}
