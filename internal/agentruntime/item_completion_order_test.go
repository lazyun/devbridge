package agentruntime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// Replayed lifecycle observations for an already completed item must not move
// the current-item projection away from a subsequently started item, or rewrite
// the first item's recorded completion. This is display data, not Run authority.
func TestCompletedItemReplayPreservesNewerCurrentItem(t *testing.T) {
	for _, name := range []string{"late-start", "late-output", "exact-completion", "conflicting-completion"} {
		t.Run(name, func(t *testing.T) {
			service, session, _ := newSession(t)
			at := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
			observe := func(method string, params any) {
				t.Helper()
				at = at.Add(time.Second)
				encoded, err := json.Marshal(params)
				if err != nil {
					t.Fatal(err)
				}
				if err := session.Observe(Event{Method: method, Params: encoded, ReceivedAt: at}); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := session.Flush(ctx); err != nil {
					t.Fatal(err)
				}
			}
			started := map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{
				"id": "command-a", "type": "commandExecution", "status": "inProgress", "command": "printf first", "cwd": "/tmp", "commandActions": []any{},
			}}
			completed := map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{
				"id": "command-a", "type": "commandExecution", "status": "completed", "command": "printf first", "cwd": "/tmp", "commandActions": []any{}, "exitCode": 0, "durationMs": 1, "aggregatedOutput": "first",
			}}
			observe("item/started", started)
			observe("item/completed", completed)
			observe("item/started", map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{
				"id": "message-b", "type": "agentMessage", "text": "next item", "phase": "commentary",
			}})
			before, err := service.Read(context.Background(), "run")
			if err != nil || len(before.Items) != 2 || before.CurrentItem == nil || before.CurrentItem.ID != "message-b" || before.Items[0].CompletedAt == nil || before.Items[0].Status != "completed" || before.Items[0].OutputTail != "first" || before.Items[0].OutputBytes != 5 {
				t.Fatalf("initial observations: %+v, %v", before, err)
			}
			switch name {
			case "late-start":
				observe("item/started", started)
			case "late-output":
				observe("item/commandExecution/outputDelta", map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "command-a", "delta": "late output"})
			case "exact-completion":
				observe("item/completed", completed)
			case "conflicting-completion":
				item := completed["item"].(map[string]any)
				item["status"], item["exitCode"], item["aggregatedOutput"] = "failed", 1, "conflicting result"
				observe("item/completed", completed)
			}
			after, err := service.Read(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if after.CurrentItem == nil {
				t.Errorf("replayed %s erased current item", name)
			} else if !reflect.DeepEqual(after.CurrentItem, before.CurrentItem) {
				t.Errorf("replayed %s changed current item: before=%s after=%s", name, before.CurrentItem.ID, after.CurrentItem.ID)
			}
			if len(after.Items) != len(before.Items) {
				t.Errorf("replayed %s changed item count: before=%d after=%d", name, len(before.Items), len(after.Items))
			} else if !reflect.DeepEqual(after.Items, before.Items) {
				t.Errorf("replayed %s changed completed item evidence: before=%s/%s/%q after=%s/%s/%q", name, before.Items[0].Status, before.Items[0].CompletedAt, before.Items[0].OutputTail, after.Items[0].Status, after.Items[0].CompletedAt, after.Items[0].OutputTail)
			}
		})
	}
}
