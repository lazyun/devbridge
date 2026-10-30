package codex

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"testing"
)

func TestSteeringUsesExactTurnAndNeverStartsReplacement(t *testing.T) {
	for _, scenario := range []string{"accepted", "mismatch", "rejected", "lost", "completed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRPCFixture(t, nil, nil)
			state := &turnState{}
			c := &Client{rpc: f.transport, turns: map[turnKey]*turnState{{threadID: "thread", turnID: "turn"}: state}}
			if scenario == "completed" {
				delete(c.turns, turnKey{threadID: "thread", turnID: "turn"})
			}
			done := make(chan string, 1)
			go func() {
				done <- c.steerCurrentTurn(context.Background(), agentruntime.SteeringInput{Identity: agentruntime.Identity{ThreadID: "thread", TurnID: "turn"}, Text: "  exact\nclarification  "})
			}()
			if scenario == "completed" {
				if got := <-done; got != "REJECTED" {
					t.Fatal(got)
				}
				return
			}
			req := f.readRequest(t)
			if string(req["method"]) != `"turn/steer"` || string(req["params"]) != `{"threadId":"thread","expectedTurnId":"turn","input":[{"type":"text","text":"  exact\nclarification  "}]}` {
				t.Fatalf("changed target/input or permissions: %s", req)
			}
			want := "UNKNOWN"
			switch scenario {
			case "accepted":
				want = "ACCEPTED"
				f.write(t, map[string]any{"id": req["id"], "result": map[string]string{"turnId": "turn"}})
			case "mismatch":
				f.write(t, map[string]any{"id": req["id"], "result": map[string]string{"turnId": "other"}})
			case "rejected":
				want = "REJECTED"
				f.write(t, map[string]any{"id": req["id"], "error": map[string]any{"code": -32602, "message": "expected turn mismatch"}})
			case "lost":
				_ = f.transport.Detach()
			}
			if got := <-done; got != want {
				t.Fatalf("got=%s want=%s", got, want)
			}
		})
	}
}
