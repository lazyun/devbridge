package codex

import (
	"context"
	"encoding/json"
	"testing"
)

// The envelope identity remains usable even when the usage body cannot be
// decoded. Such an exact current-Turn event cannot be silently discarded: its
// missing final cumulative would otherwise be charged to the next Run.
func TestMalformedUsageEnvelopeInvalidatesExactTurnAndNextBaseline(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"string window", `{"last":{"inputTokens":10,"cachedInputTokens":5,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":10},"total":{"inputTokens":20,"cachedInputTokens":10,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":20},"modelContextWindow":"4096"}`},
		{"fractional window", `{"last":{"inputTokens":10,"cachedInputTokens":5,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":10},"total":{"inputTokens":20,"cachedInputTokens":10,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":20},"modelContextWindow":4096.5}`},
		{"overflowing window", `{"last":{"inputTokens":10,"cachedInputTokens":5,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":10},"total":{"inputTokens":20,"cachedInputTokens":10,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":20},"modelContextWindow":9223372036854775808}`},
		{"scalar usage body", `42`},
		{"array usage body", `[]`},
		{"null usage body", `null`},
		{"missing usage body", ""},
	} {
		for _, beforeResponse := range []bool{false, true} {
			ordering := "after response"
			if beforeResponse {
				ordering = "before response"
			}
			t.Run(tc.name+"/"+ordering, func(t *testing.T) {
				client, requests, responses := newClientFixture(t)
				store := &serializedUsageStore{}
				client.usageStore = store
				client.installUsageLedger(context.Background(), "canonical", true, nil)
				done := make(chan TurnResult, 1)
				started := make(chan struct{})
				go func() {
					value, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "canonical", RunID: "run-current", Prompt: "continue", OnStarted: func(string, string) error { close(started); return nil }})
					if err != nil {
						t.Errorf("turn: %v", err)
					}
					done <- value
				}()
				request := readClientRequest(t, requests)
				respond := func() {
					writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-current", "status": "inProgress"}}})
				}
				if !beforeResponse {
					respond()
					<-started
				}
				writeTokenUsage(t, responses, "canonical", "turn-current", tokenBreakdown(10, 5, 0, 0, 0, 10), tokenBreakdown(10, 5, 0, 0, 0, 10), nil)
				params := map[string]any{"threadId": "canonical", "turnId": "turn-current"}
				if tc.body != "" {
					params["tokenUsage"] = json.RawMessage(tc.body)
				}
				writeServerJSON(t, responses, map[string]any{"method": "thread/tokenUsage/updated", "params": params})
				writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical", "turn": map[string]any{"id": "turn-current", "status": "completed"}}})
				if beforeResponse {
					respond()
				}
				got := <-done
				if got.TokenUsage == nil || got.TokenUsage.Reliable || got.TokenUsage.Attribution.Reason != "INVALID_TELEMETRY" {
					t.Errorf("malformed exact-Turn usage remained exact: %+v", got.TokenUsage)
				}
				client.mu.Lock()
				ledger := client.threadUsage["canonical"].ledger
				client.mu.Unlock()
				if ledger.Anchored {
					t.Errorf("malformed final usage left a trusted baseline: %+v", ledger)
				}
				persisted, exists, err := store.Load(context.Background(), "canonical")
				if err != nil || !exists || persisted.Anchored || persisted.InFlightTurnID != "" {
					t.Errorf("malformed final usage retained a durable trusted baseline: %+v, exists=%v, err=%v", persisted, exists, err)
				}
				next := usageTurn(t, client, requests, responses, "turn-reanchor", false, 30)
				if next.TokenUsage == nil || next.TokenUsage.Reliable || next.TokenUsage.EndCumulative.TotalTokens != 10 {
					t.Errorf("next Run charged unknown prior spending: %+v", next.TokenUsage)
				}
				recovered := usageTurn(t, client, requests, responses, "turn-recovered", false, 40)
				if recovered.TokenUsage == nil || !recovered.TokenUsage.Reliable || recovered.TokenUsage.EndCumulative.TotalTokens-recovered.TokenUsage.StartCumulative.TotalTokens != 10 {
					t.Errorf("trusted next baseline did not recover: %+v", recovered.TokenUsage)
				}
			})
		}
	}
}

func TestMalformedUsageEnvelopeForOtherTurnDoesNotDegradeCurrent(t *testing.T) {
	for _, body := range []string{`42`, `{"modelContextWindow":"4096"}`} {
		t.Run(body, func(t *testing.T) {
			client, requests, responses := newClientFixture(t)
			client.installUsageLedger(context.Background(), "canonical", true, nil)
			usageTurn(t, client, requests, responses, "turn-prior", false, 10)
			done := make(chan TurnResult, 1)
			started := make(chan struct{})
			go func() {
				value, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "canonical", RunID: "run-current", Prompt: "continue", OnStarted: func(string, string) error { close(started); return nil }})
				if err != nil {
					t.Errorf("turn: %v", err)
				}
				done <- value
			}()
			request := readClientRequest(t, requests)
			// These pre-response events must not contaminate the candidate later
			// selected by the exact turn/start response.
			writeUnrelated := func() {
				for _, identity := range []struct{ thread, turn any }{
					{"foreign", "turn-current"},
					{"canonical", "turn-other"},
					{"canonical", "turn-prior"},
					{"", "turn-current"},
					{"canonical", ""},
					{42, "turn-current"},
					{"canonical", 42},
				} {
					writeServerJSON(t, responses, map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": identity.thread, "turnId": identity.turn, "tokenUsage": json.RawMessage(body)}})
				}
			}
			writeUnrelated()
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-current", "status": "inProgress"}}})
			<-started
			window := int64(4096)
			writeTokenUsage(t, responses, "canonical", "turn-current", tokenBreakdown(30, 15, 0, 0, 0, 30), tokenBreakdown(20, 10, 0, 0, 0, 20), &window)
			writeUnrelated()
			writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical", "turn": map[string]any{"id": "turn-current", "status": "completed"}}})
			got := <-done
			if got.TokenUsage == nil || !got.TokenUsage.Reliable || got.TokenUsage.EndCumulative.TotalTokens-got.TokenUsage.StartCumulative.TotalTokens != 20 || got.TokenUsage.ModelContextWindow == nil || *got.TokenUsage.ModelContextWindow != window {
				t.Fatalf("unrelated malformed usage changed current-Turn accounting: %+v", got.TokenUsage)
			}
		})
	}
}

func TestUsageEnvelopePreservesValidCountersAndOptionalWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window json.RawMessage
		want   int64
	}{
		{name: "absent"},
		{name: "null", window: json.RawMessage(`null`)},
		{name: "zero", window: json.RawMessage(`0`)},
		{name: "positive", window: json.RawMessage(`4096`), want: 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, requests, responses := newClientFixture(t)
			client.installUsageLedger(context.Background(), "canonical", true, nil)
			done := make(chan TurnResult, 1)
			go func() {
				value, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "canonical", Prompt: "continue"})
				if err != nil {
					t.Errorf("turn: %v", err)
				}
				done <- value
			}()
			request := readClientRequest(t, requests)
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-current", "status": "inProgress"}}})
			counts := tokenBreakdown(20, 10, 2, 10, 5, 30)
			body := map[string]any{"last": counts, "total": counts}
			if tc.window != nil {
				body["modelContextWindow"] = tc.window
			}
			writeServerJSON(t, responses, map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "canonical", "turnId": "turn-current", "tokenUsage": body}})
			writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical", "turn": map[string]any{"id": "turn-current", "status": "completed"}}})
			got := <-done
			if got.TokenUsage == nil || !got.TokenUsage.Reliable || got.TokenUsage.EndCumulative != tokenCounts(counts) {
				t.Fatalf("valid usage changed: %+v", got.TokenUsage)
			}
			window := got.TokenUsage.ModelContextWindow
			if tc.name == "absent" || tc.name == "null" {
				if window != nil {
					t.Fatalf("absent window became %d", *window)
				}
			} else if window == nil || *window != tc.want {
				t.Fatalf("window=%v, want %d", window, tc.want)
			}
		})
	}
}

func TestMalformedUsageEnvelopeLaterValidSampleRecoversNextBaseline(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	client.installUsageLedger(context.Background(), "canonical", true, nil)
	done := make(chan TurnResult, 1)
	started := make(chan struct{})
	go func() {
		value, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "canonical", Prompt: "continue", OnStarted: func(string, string) error { close(started); return nil }})
		if err != nil {
			t.Errorf("turn: %v", err)
		}
		done <- value
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-current", "status": "inProgress"}}})
	<-started
	writeTokenUsage(t, responses, "canonical", "turn-current", tokenBreakdown(10, 5, 0, 0, 0, 10), tokenBreakdown(10, 5, 0, 0, 0, 10), nil)
	writeServerJSON(t, responses, map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "canonical", "turnId": "turn-current", "tokenUsage": 42}})
	writeTokenUsage(t, responses, "canonical", "turn-current", tokenBreakdown(30, 15, 0, 0, 0, 30), tokenBreakdown(20, 10, 0, 0, 0, 20), nil)
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical", "turn": map[string]any{"id": "turn-current", "status": "completed"}}})
	got := <-done
	if got.TokenUsage == nil || got.TokenUsage.Reliable || got.TokenUsage.Attribution.Reason != "INVALID_TELEMETRY" || got.TokenUsage.EndCumulative.TotalTokens != 30 {
		t.Fatalf("current Run lost its uncertainty or valid end: %+v", got.TokenUsage)
	}
	client.mu.Lock()
	ledger := client.threadUsage["canonical"].ledger
	client.mu.Unlock()
	if !ledger.Anchored || ledger.PreviousRawCumulative.TotalTokens != 30 {
		t.Fatalf("later valid end did not restore baseline: %+v", ledger)
	}
	next := usageTurn(t, client, requests, responses, "turn-next", false, 40)
	if next.TokenUsage == nil || !next.TokenUsage.Reliable || next.TokenUsage.EndCumulative.TotalTokens-next.TokenUsage.StartCumulative.TotalTokens != 10 {
		t.Fatalf("next Run failed exact accounting after recovered end: %+v", next.TokenUsage)
	}
}
