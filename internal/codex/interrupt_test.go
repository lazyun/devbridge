package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

func TestInterruptACKIsNotTerminalAndCompletionUsesExactTurn(t *testing.T) {
	for _, status := range []string{"interrupted", "completed"} {
		t.Run(status, func(t *testing.T) {
			client, requests, responses := newClientFixture(t)
			started := make(chan struct{})
			type result struct {
				value TurnResult
				err   error
			}
			done := make(chan result, 1)
			go func() {
				value, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "exact-thread", Prompt: "controlled work", OnStarted: func(string, string) error { close(started); return nil }})
				done <- result{value, err}
			}()
			start := readClientRequest(t, requests)
			writeServerJSON(t, responses, map[string]any{"id": start["id"], "result": map[string]any{"turn": map[string]any{"id": "exact-turn", "status": "inProgress", "items": []any{}}}})
			<-started
			ack := make(chan error, 1)
			go func() { ack <- client.InterruptTurn(context.Background(), "exact-thread", "exact-turn") }()
			interrupt := readClientRequest(t, requests)
			var method string
			_ = json.Unmarshal(interrupt["method"], &method)
			var params wire.TurnInterruptParams
			_ = json.Unmarshal(interrupt["params"], &params)
			if method != "turn/interrupt" || params.ThreadID != "exact-thread" || params.TurnID != "exact-turn" {
				t.Fatalf("invented or inexact RPC: %s %+v", method, params)
			}
			writeServerJSON(t, responses, map[string]any{"id": interrupt["id"], "result": map[string]any{}})
			if err := <-ack; err != nil {
				t.Fatal(err)
			}
			select {
			case value := <-done:
				t.Fatalf("ACK retired Turn: %+v", value)
			default:
			}
			client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"exact-thread","turn":{"id":"stale-turn","status":"interrupted","items":[]}}`)})
			select {
			case value := <-done:
				t.Fatalf("wrong Turn retired exact target: %+v", value)
			default:
			}
			payload, _ := json.Marshal(map[string]any{"threadId": "exact-thread", "turn": map[string]any{"id": "exact-turn", "status": status, "items": []any{}}})
			client.handleEvent(Event{Method: "turn/completed", Params: payload})
			got := <-done
			if got.value.ThreadID != "exact-thread" || got.value.TurnID != "exact-turn" {
				t.Fatalf("wrong result: %+v", got)
			}
			if status == "completed" {
				if got.err != nil {
					t.Fatal(got.err)
				}
			} else {
				var terminal *TurnError
				if !errors.As(got.err, &terminal) || terminal.Status != "interrupted" {
					t.Fatalf("missing exact terminal error: %v", got.err)
				}
			}
			client.handleEvent(Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"exact-thread","turn":{"id":"exact-turn","status":"completed","items":[]}}`)})
			client.mu.Lock()
			_, revived := client.turns[turnKey{threadID: "exact-thread", turnID: "exact-turn"}]
			client.mu.Unlock()
			if revived {
				t.Fatal("late completion revived retired Turn")
			}
		})
	}
}
