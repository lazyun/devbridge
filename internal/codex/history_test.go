package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type historyStub struct {
	calls                                 []string
	foreignThread, foreignTurn, badCursor bool
}

func (s *historyStub) Call(_ context.Context, method string, params any, out any) error {
	s.calls = append(s.calls, method)
	p := params.(map[string]any)
	var response any
	switch method {
	case "thread/read":
		if p["includeTurns"] != false {
			panic("history loaded full thread")
		}
		id := p["threadId"]
		if s.foreignThread {
			id = "other"
		}
		response = map[string]any{"thread": map[string]any{"id": id}}
	case "thread/items/list":
		if p["threadId"] != "thread" || p["turnId"] != "turn" {
			panic("unbound history")
		}
		turn := "turn"
		if s.foreignTurn {
			turn = "other"
		}
		next := "next"
		if s.badCursor {
			next = "cursor"
		}
		response = map[string]any{"data": []any{map[string]any{"turnId": turn, "item": map[string]any{"id": "item", "type": "agentMessage", "text": "review report", "status": "completed"}}}, "nextCursor": next}
	default:
		panic("history issued a mutating RPC: " + method)
	}
	raw, _ := json.Marshal(response)
	return json.Unmarshal(raw, out)
}
func TestPersistedHistoryReadsOnlyExactTurnAndPaginates(t *testing.T) {
	s := &historyStub{}
	page, e := readHistoryPage(context.Background(), s, "thread", "turn", "cursor", 20)
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Items) != 1 || page.Items[0].Text != "review report" || page.NextCursor == nil || *page.NextCursor != "next" || len(s.calls) != 2 {
		t.Fatalf("page=%+v calls=%v", page, s.calls)
	}
}
func TestPersistedHistoryRejectsForeignTargetsAndStalledCursor(t *testing.T) {
	for _, s := range []*historyStub{{foreignThread: true}, {foreignTurn: true}, {badCursor: true}} {
		if _, e := readHistoryPage(context.Background(), s, "thread", "turn", "cursor", 20); e == nil {
			t.Fatal("accepted unbound history")
		}
	}
	s := &historyStub{}
	if _, e := readHistoryPage(context.Background(), s, "thread", "turn", "", 100); e == nil || len(s.calls) != 0 {
		t.Fatal("invalid page reached backend")
	}
}
func TestPersistedHistoryOmitsToolSecretsAndRawReasoning(t *testing.T) {
	for _, raw := range []string{`{"id":"x","type":"mcpToolCall","arguments":{"token":"secret"}}`, `{"id":"x","type":"reasoning","summary":["public summary"],"content":["secret"]}`} {
		v, e := historyItem(json.RawMessage(raw))
		if e != nil || strings.Contains(v.Text, "secret") {
			t.Fatal(v, e)
		}
	}
	raw, _ := json.Marshal(map[string]any{"id": "x", "type": "agentMessage", "text": strings.Repeat("汉", 30000)})
	v, e := historyItem(raw)
	if e != nil || !v.Truncated || len(v.Text) > 64<<10 {
		t.Fatal("history display cap missing")
	}
}
