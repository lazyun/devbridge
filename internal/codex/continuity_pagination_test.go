package codex

import (
	"context"
	"encoding/json"
	"testing"
)

func TestObserveExactTurnPagesFullHistoryAndRejectsCursorCycles(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact second page", true: "cyclic cursor"}[cycle], func(t *testing.T) {
			f := newRPCFixture(t, nil, nil)
			c := &Client{detached: true, rpc: f.transport}
			done := make(chan error, 1)
			go func() {
				v, err := c.ObserveLoadedTurn(context.Background(), "thread", "original")
				if err == nil && (v.Turn.ID != "original" || len(v.Turn.Items) != 1) {
					t.Errorf("incomplete terminal evidence: %+v", v)
				}
				done <- err
			}()
			req := f.readRequest(t)
			f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []string{"thread"}}})
			req = f.readRequest(t)
			if string(req["method"]) != `"thread/read"` || string(req["params"]) != `{"includeTurns":false,"threadId":"thread"}` {
				t.Fatal(req)
			}
			f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"thread": map[string]any{"id": "thread", "status": map[string]string{"type": "idle"}}}})
			for page := 0; page < 2; page++ {
				req = f.readRequest(t)
				var p map[string]any
				_ = json.Unmarshal(req["params"], &p)
				if string(req["method"]) != `"thread/turns/list"` || p["threadId"] != "thread" || p["itemsView"] != "full" || p["sortDirection"] != "desc" || page == 1 && p["cursor"] != "next" {
					t.Fatal(req)
				}
				if page == 0 || cycle {
					f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []any{map[string]string{"id": "another", "status": "completed"}}, "nextCursor": "next"}})
				} else {
					f.write(t, map[string]any{"id": req["id"], "result": map[string]any{"data": []any{map[string]any{"id": "original", "status": "completed", "items": []any{map[string]string{"type": "agentMessage", "text": "original result"}}}}}})
				}
			}
			if err := <-done; (err != nil) != cycle {
				t.Fatalf("cycle=%v err=%v", cycle, err)
			}
		})
	}
}
