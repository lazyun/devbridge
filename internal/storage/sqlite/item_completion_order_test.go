package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

// The normal runtime observer and its independent history writer must agree
// that a replay of an old completed item does not replace a newer active item.
// Setup uses the public tracked Review fixture and regular service/store APIs;
// no disconnect, recovery, approval, write fault or process failure is involved.
func TestRuntimeCompletedItemReplayPreservesSQLiteObservation(t *testing.T) {
	f := newAgentStorageFixture(t, "item-completion-order")
	op := prepareAgentOperation(t, f, agentconversation.Review, "item-completion-order")
	value, err := op.make(context.Background(), "run-item-completion-order")
	if err != nil {
		t.Fatal(err)
	}
	ctx := runtimeepoch.WithFence(context.Background(), value.RuntimeEpoch, value.ID)
	runs := run.NewService(NewRunStore(f.db), nil, nil)
	conversations := NewAgentConversationStore(f.db)
	if _, err = runs.Transition(ctx, value.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err = conversations.BeginRun(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	if err = conversations.MarkThreadStartPending(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	if err = conversations.BindThread(ctx, value.ID, "thread-item-order"); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.AttachThread(ctx, value.ID, "thread-item-order"); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Transition(ctx, value.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err = conversations.MarkTurnPending(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.AttachTurn(ctx, value.ID, "turn-item-order"); err != nil {
		t.Fatal(err)
	}
	authorityBefore, err := runs.Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	store := NewAgentRuntimeStore(f.db)
	service := agentruntime.NewService(store)
	session := service.NewTurn(ctx, value.ID, "thread-item-order", true)
	t.Cleanup(session.Close)
	if err = session.Bind("turn-item-order"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	observe := func(method string, item map[string]any) {
		t.Helper()
		at = at.Add(time.Second)
		params, err := json.Marshal(map[string]any{"threadId": "thread-item-order", "turnId": "turn-item-order", "item": item})
		if err != nil {
			t.Fatal(err)
		}
		if err = session.Observe(agentruntime.Event{Method: method, Params: params, ReceivedAt: at}); err != nil {
			t.Fatal(err)
		}
		flushCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err = session.Flush(flushCtx); err != nil {
			t.Fatal(err)
		}
	}
	command := map[string]any{"id": "command-a", "type": "commandExecution", "status": "inProgress", "command": "printf first", "cwd": f.root, "commandActions": []any{}}
	observe("item/started", command)
	command["status"], command["exitCode"], command["durationMs"], command["aggregatedOutput"] = "completed", 0, 1, "first"
	observe("item/completed", command)
	firstCompletion := at
	observe("item/started", map[string]any{"id": "message-b", "type": "agentMessage", "text": "next item", "phase": "commentary"})
	before, err := service.Read(ctx, value.ID)
	if err != nil || before.CurrentItem == nil || before.CurrentItem.ID != "message-b" || len(before.Items) != 2 {
		t.Fatalf("initial SQLite observation=%+v err=%v", before, err)
	}
	observe("item/completed", command)
	// A separately constructed reader establishes this is durable state, rather
	// than only a mutation in the observer's in-memory snapshot.
	after, err := agentruntime.NewService(NewAgentRuntimeStore(f.db)).Read(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentItem == nil {
		t.Error("duplicate completed item erased durable current item")
	} else if !reflect.DeepEqual(after.CurrentItem, before.CurrentItem) {
		t.Errorf("duplicate completed item replaced durable current item: before=%s after=%s", before.CurrentItem.ID, after.CurrentItem.ID)
	}
	if len(after.Items) != len(before.Items) {
		t.Errorf("duplicate completed item changed durable item count: before=%d after=%d", len(before.Items), len(after.Items))
	} else if !reflect.DeepEqual(after.Items, before.Items) {
		t.Errorf("duplicate completed item rewrote durable completion: before=%s after=%s", before.Items[0].CompletedAt, after.Items[0].CompletedAt)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := store.ReadSessionHistory(ctx, value.ID, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if page.MaterializedSequence >= after.Sequence {
			if len(page.Items) != 2 {
				t.Fatalf("history item count=%d, want 2", len(page.Items))
			}
			found := false
			for _, record := range page.Items {
				if record.Item.ID == "command-a" {
					found = true
					if record.Item.CompletedAt == nil || !record.Item.CompletedAt.Equal(firstCompletion) {
						t.Errorf("duplicate completed item rewrote materialized completion: got=%s want=%s", record.Item.CompletedAt, firstCompletion)
					}
				}
			}
			if !found {
				t.Fatal("completed command missing from history")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("history did not materialize sequence %d: %+v", after.Sequence, page)
		}
		time.Sleep(time.Millisecond)
	}
	authorityAfter, err := runs.Get(ctx, value.ID)
	if err != nil || !reflect.DeepEqual(authorityAfter, authorityBefore) {
		t.Fatalf("display observation changed authoritative Run: before=%+v after=%+v err=%v", authorityBefore, authorityAfter, err)
	}
}
