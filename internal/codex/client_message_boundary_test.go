package codex

import (
	"context"
	"testing"
	"time"
)

func TestStartTurnFreezesAgentMessagesAtCompletion(t *testing.T) {
	message := func(id, text, phase string) map[string]any {
		item := map[string]any{"id": id, "type": "agentMessage", "text": text}
		if phase != "" {
			item["phase"] = phase
		}
		return item
	}
	final := message("final", "final before completion", "final_answer")
	fallback := message("fallback", "fallback before completion", "")
	lateFinal := message("late-final", "late final", "final_answer")
	lateFallback := message("late-fallback", "late fallback", "")
	emptyFinal := message("empty-final", "", "final_answer")
	commentary := message("commentary", "not a final answer", "commentary")
	tests := []struct {
		name      string
		before    []any
		snapshot  []any
		after     []any
		duplicate bool
		foreign   bool
		want      string
	}{
		{
			name:   "late_final_cannot_replace_final",
			before: []any{final}, after: []any{lateFinal}, want: "final before completion",
		},
		{
			name:   "late_same_id_cannot_replace_final",
			before: []any{final}, after: []any{message("final", "changed replay", "final_answer")}, want: "final before completion",
		},
		{
			name:   "late_final_cannot_replace_fallback",
			before: []any{fallback}, after: []any{lateFinal}, want: "fallback before completion",
		},
		{
			name:   "late_fallback_cannot_replace_fallback",
			before: []any{fallback}, after: []any{lateFallback}, want: "fallback before completion",
		},
		{
			name:  "late_final_cannot_create_answer",
			after: []any{lateFinal},
		},
		{
			name:  "late_fallback_cannot_create_answer",
			after: []any{lateFallback},
		},
		{
			name:   "late_final_cannot_replace_empty_final",
			before: []any{fallback, emptyFinal}, after: []any{lateFinal},
		},
		{
			name:   "late_empty_final_cannot_erase_final",
			before: []any{final}, after: []any{emptyFinal}, want: "final before completion",
		},
		{
			name:   "late_fallback_cannot_replace_final",
			before: []any{final}, after: []any{lateFallback}, want: "final before completion",
		},
		{
			name:   "last_preterminal_final_wins",
			before: []any{final, lateFinal, fallback, commentary}, want: "late final",
		},
		{
			name:   "last_preterminal_fallback_wins",
			before: []any{fallback, lateFallback, commentary}, want: "late fallback",
		},
		{
			name:   "empty_preterminal_final_beats_fallback",
			before: []any{fallback, emptyFinal, lateFallback},
		},
		{
			name:   "commentary_is_not_fallback",
			before: []any{commentary}, after: []any{commentary},
		},
		{
			name:   "duplicate_messages_preserve_answer",
			before: []any{final, final}, after: []any{final, final}, want: "final before completion",
		},
		{
			name:     "duplicate_completion_cannot_replace_snapshot",
			snapshot: []any{final}, duplicate: true, want: "final before completion",
		},
		{
			name:   "foreign_thread_and_turn_cannot_replace_answer",
			before: []any{final}, foreign: true, want: "final before completion",
		},
		{
			name:   "snapshot_final_overrides_observed_messages",
			before: []any{fallback, lateFinal}, snapshot: []any{final}, after: []any{lateFinal}, want: "final before completion",
		},
		{
			name:   "snapshot_fallback_preserves_existing_precedence",
			before: []any{final}, snapshot: []any{fallback}, after: []any{lateFinal}, want: "fallback before completion",
		},
		{
			name:   "snapshot_empty_final_beats_fallback",
			before: []any{final}, snapshot: []any{emptyFinal, fallback}, after: []any{lateFinal},
		},
		{
			name:     "last_snapshot_final_wins",
			snapshot: []any{final, lateFinal, fallback, commentary}, want: "late final",
		},
		{
			name:     "last_snapshot_fallback_wins",
			snapshot: []any{fallback, lateFallback, commentary}, want: "late fallback",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, requests, responses := newClientFixture(t)
			type result struct {
				turn TurnResult
				err  error
			}
			results := make(chan result, 1)
			go func() {
				turn, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "message-thread", Prompt: "message ordering"})
				results <- result{turn: turn, err: err}
			}()
			request := readClientRequest(t, requests)
			writeMessage := func(threadID, turnID string, item any) {
				writeServerJSON(t, responses, map[string]any{"method": "item/completed", "params": map[string]any{
					"threadId": threadID, "turnId": turnID, "item": item,
				}})
			}
			writeCompletion := func(items []any) {
				if items == nil {
					items = []any{}
				}
				writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{
					"threadId": "message-thread", "turn": map[string]any{"id": "message-turn", "status": "completed", "items": items},
				}})
			}
			for _, item := range tt.before {
				writeMessage("message-thread", "message-turn", item)
			}
			writeCompletion(tt.snapshot)
			for _, item := range tt.after {
				writeMessage("message-thread", "message-turn", item)
			}
			if tt.duplicate {
				writeCompletion([]any{lateFinal})
			}
			if tt.foreign {
				writeMessage("other-thread", "message-turn", lateFinal)
				writeMessage("message-thread", "other-turn", lateFinal)
			}
			// The transport processes all preceding notifications before this
			// response, so the result cannot depend on scheduler timing.
			writeServerJSON(t, responses, map[string]any{
				"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "message-turn", "status": "inProgress", "items": []any{}}},
			})
			select {
			case got := <-results:
				if got.err != nil || got.turn.ThreadID != "message-thread" || got.turn.TurnID != "message-turn" || got.turn.FinalMessage != tt.want {
					t.Fatalf("turn result = %+v, error = %v; want final message %q", got.turn, got.err, tt.want)
				}
			case <-time.After(time.Second):
				t.Fatal("turn did not complete")
			}
		})
	}
}
