package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex/wire"
)

type HistoryItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}
type HistoryPage struct {
	ThreadID   string        `json:"threadId"`
	TurnID     string        `json:"turnId"`
	Items      []HistoryItem `json:"items"`
	NextCursor *string       `json:"nextCursor"`
	Source     string        `json:"source"`
}
type historyRPC interface {
	Call(context.Context, string, any, any) error
}

// ReadPersistedHistory connects to the exact original instance without loading
// or resuming a thread. It cannot own approvals, receive a recovered Turn, or
// write observations into Core. No backend is selected from current defaults.
func ReadPersistedHistory(ctx context.Context, b appserver.Binding, dataRoot, thread, turn, cursor string, limit int) (HistoryPage, error) {
	if err := appserver.VerifyOwner(b, dataRoot); err != nil {
		return HistoryPage{}, err
	}
	if err := appserver.Verify(ctx, b); err != nil {
		return HistoryPage{}, err
	}
	socket, err := dialUnixWebSocket(ctx, b.SocketPath)
	if err != nil {
		return HistoryPage{}, err
	}
	rpc := newRPCTransportWithProcessor(socket, socket, func(context.Context, ServerRequest) (any, error) {
		return nil, errors.New("history connection cannot answer runtime requests")
	}, func(Event) {})
	defer rpc.Close()
	var initialized wire.InitializeResponse
	if err = rpc.Call(ctx, "initialize", wire.InitializeParams{ClientInfo: wire.ClientInfo{Name: "devbridge-history", Title: "Dev Bridge history", Version: "0.1.0"}}, &initialized); err != nil {
		return HistoryPage{}, err
	}
	if err = rpc.Notify("initialized", struct{}{}); err != nil {
		return HistoryPage{}, err
	}
	return readHistoryPage(ctx, rpc, thread, turn, cursor, limit)
}
func readHistoryPage(ctx context.Context, rpc historyRPC, thread, turn, cursor string, limit int) (HistoryPage, error) {
	out := HistoryPage{ThreadID: thread, TurnID: turn, Items: []HistoryItem{}, Source: "CODEX_PERSISTED_HISTORY"}
	if thread == "" || turn == "" || len(cursor) > 4096 || limit < 1 || limit > 50 {
		return out, errors.New("exact history target and page required")
	}
	var meta struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := rpc.Call(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": false}, &meta); err != nil {
		return out, err
	}
	if meta.Thread.ID != thread {
		return out, errors.New("history thread identity mismatch")
	}
	params := map[string]any{"threadId": thread, "turnId": turn, "limit": limit, "sortDirection": "asc"}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var page struct {
		Data []struct {
			TurnID string          `json:"turnId"`
			Item   json.RawMessage `json:"item"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := rpc.Call(ctx, "thread/items/list", params, &page); err != nil {
		return out, err
	}
	if len(page.Data) > limit || page.NextCursor != nil && (len(*page.NextCursor) > 4096 || *page.NextCursor == "" || *page.NextCursor == cursor) {
		return out, errors.New("invalid history pagination")
	}
	for _, row := range page.Data {
		if row.TurnID != turn {
			return HistoryPage{}, errors.New("history turn identity mismatch")
		}
		item, err := historyItem(row.Item)
		if err != nil {
			return HistoryPage{}, err
		}
		out.Items = append(out.Items, item)
	}
	out.NextCursor = page.NextCursor
	return out, nil
}
func historyItem(raw json.RawMessage) (HistoryItem, error) {
	var v struct {
		ID, Type, Status, Text, Command, AggregatedOutput string
		Content                                           json.RawMessage
		Changes                                           []struct{ Path, Diff string }
		Summary                                           []string
	}
	if json.Unmarshal(raw, &v) != nil || v.ID == "" || len(v.ID) > 1024 {
		return HistoryItem{}, errors.New("invalid persisted history item")
	}
	item := HistoryItem{ID: v.ID, Type: v.Type, Status: v.Status}
	switch v.Type {
	case "agentMessage", "plan":
		item.Text = v.Text
	case "userMessage":
		var content []struct{ Type, Text string }
		if json.Unmarshal(v.Content, &content) != nil {
			return HistoryItem{}, errors.New("invalid user history content")
		}
		for _, part := range content {
			if part.Type == "text" {
				item.Text += part.Text + "\n"
			}
		}
	case "commandExecution":
		item.Text = v.Command + "\n" + v.AggregatedOutput
	case "fileChange":
		for _, change := range v.Changes {
			item.Text += change.Path + "\n" + change.Diff + "\n"
		}
	case "reasoning":
		item.Text = strings.Join(v.Summary, "\n") // Never expose raw reasoning blocks.
	default:
		item.Text = "此类型仅展示记录标识。"
	}
	if len(item.Text) > 64<<10 {
		end := 64 << 10
		for end > 0 && !utf8.RuneStart(item.Text[end]) {
			end--
		}
		item.Text = item.Text[:end]
		item.Truncated = true
	}
	return item, nil
}
