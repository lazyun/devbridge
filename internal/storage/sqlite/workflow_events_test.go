package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/workflowevents"
)

func TestWorkflowEventOutboxAtomicScopedRestartAndUnsubscribe(t *testing.T) {
	f, task := newTaskOwnedFixture(t)
	ctx := context.Background()
	store := NewWorkflowEventStore(f.db)
	now := time.Now().UTC()
	sub := workflowevents.Subscription{ID: "subscription", Scope: "owned-caller", ProjectID: f.project, Arguments: workflowevents.Arguments{AISessionID: f.session, TaskID: task.ID}, Delivery: workflowevents.Delivery{URL: "https://example.com/events", Secret: "secret-never-in-payload"}, ExpiresAt: now.Add(time.Hour)}
	if e := store.Put(ctx, sub); e != nil {
		t.Fatal(e)
	}
	emit := func(commit bool) {
		t.Helper()
		tx, e := f.db.beginWrite(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		payload, _ := event.Payload(map[string]any{"private": "prompt-secret"})
		if e = appendEvent(ctx, tx, event.Event{Type: event.RunStatusChanged, ProjectID: f.project, TaskID: task.ID, RunID: "", EntityType: "task", EntityID: task.ID, Payload: payload, CreatedAt: now}); e != nil {
			t.Fatal(e)
		}
		if commit {
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
		}
	}
	emit(false)
	rows, e := store.Pending(ctx, now.Add(time.Second))
	if e != nil || len(rows) != 0 {
		t.Fatalf("rolled-back event queued: %v %v", rows, e)
	}
	emit(true)
	rows, e = store.Pending(ctx, now.Add(time.Second))
	if e != nil || len(rows) != 1 {
		t.Fatalf("event missing: %v %v", rows, e)
	}
	first := rows[0]
	if strings.Contains(string(first.Body), "prompt-secret") || strings.Contains(string(first.Body), "secret-never-in-payload") {
		t.Fatal("private payload leaked")
	}
	var body map[string]any
	if json.Unmarshal(first.Body, &body) != nil || body["name"] != workflowevents.Name {
		t.Fatal(string(first.Body))
	}
	if e = store.Finish(ctx, first, "RETRY", now.Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	root := f.db.stateRoot
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	f.db = openTestDB(t, root)
	t.Cleanup(func() { f.db.Close() })
	store = NewWorkflowEventStore(f.db)
	rows, e = store.Pending(ctx, now.Add(2*time.Second))
	if e != nil || len(rows) != 1 || rows[0].EventID != first.EventID || rows[0].Attempts != 1 {
		t.Fatalf("restart lost exact event: %v %v", rows, e)
	}
	if e = store.Remove(ctx, "foreign-scope", sub.ID); e != nil {
		t.Fatal(e)
	}
	if active, _ := store.Active(ctx, sub.ID, now); !active {
		t.Fatal("foreign owner removed subscription")
	}
	if e = store.Finish(ctx, rows[0], "DELIVERED", now); e != nil {
		t.Fatal(e)
	}
	if rows, e = store.Pending(ctx, now.Add(time.Second)); e != nil || len(rows) != 0 {
		t.Fatal("acknowledged delivery repeated")
	}
	emit(true)
	if e = store.Remove(ctx, sub.Scope, sub.ID); e != nil {
		t.Fatal(e)
	}
	if rows, e = store.Pending(ctx, now.Add(time.Second)); e != nil || len(rows) != 0 {
		t.Fatal("unsubscribed event delivered")
	}
}

func TestWorkflowEventRuntimeQuestionIsDurableAndNotDuplicated(t *testing.T) {
	f, task := newTaskOwnedFixture(t)
	a := f.start(t)
	ctx := context.Background()
	store := NewWorkflowEventStore(f.db)
	now := time.Now().UTC()
	sub := workflowevents.Subscription{ID: "question-sub", Scope: "owned-caller", ProjectID: f.project, Arguments: workflowevents.Arguments{AISessionID: f.session, TaskID: task.ID}, Delivery: workflowevents.Delivery{URL: "https://example.com/events", Secret: "private"}, ExpiresAt: now.Add(time.Hour)}
	if e := store.Put(ctx, sub); e != nil {
		t.Fatal(e)
	}
	requestBytes, _ := json.Marshal(agentruntime.Request{ID: "question", State: "WAITING_FOR_USER", CreatedAt: now, Payload: json.RawMessage(`{"prompt":"PRIVATE QUESTION"}`)})
	request := string(requestBytes)
	for range 2 {
		if _, e := f.db.execWrite(ctx, `INSERT INTO codex_server_requests(connection_id,request_id,run_id,request_json) VALUES('connection','"rpc-question"',?,?) ON CONFLICT(connection_id,request_id) DO UPDATE SET request_json=excluded.request_json`, a.Run.ID, request); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := store.Pending(ctx, now.Add(time.Second))
	if e != nil || len(rows) != 1 || strings.Contains(string(rows[0].Body), "PRIVATE QUESTION") || !strings.Contains(string(rows[0].Body), "runtime.input_required") {
		t.Fatalf("question notification=%v err=%v", rows, e)
	}
	status, e := store.Status(ctx, f.project, task.ID, now)
	if e != nil || status.ActiveSubscriptions != 1 || status.Pending != 1 {
		t.Fatalf("status=%+v err=%v", status, e)
	}
	if e = store.Finish(ctx, rows[0], "STOPPED", now); e != nil {
		t.Fatal(e)
	}
	status, e = store.Status(ctx, f.project, task.ID, now)
	if e != nil || status.StoppedSubscriptions != 1 || status.ActiveSubscriptions != 0 {
		t.Fatalf("stopped status=%+v err=%v", status, e)
	}
}

func TestWorkflowEventsExpiredAndForeignScopeCannotDeliver(t *testing.T) {
	f, task := newTaskOwnedFixture(t)
	ctx := context.Background()
	store := NewWorkflowEventStore(f.db)
	now := time.Now().UTC()
	for _, test := range []struct {
		id, scope string
		expiry    time.Time
	}{{"expired", "owned-caller", now.Add(-time.Second)}, {"foreign", "different-caller", now.Add(time.Hour)}} {
		sub := workflowevents.Subscription{ID: test.id, Scope: test.scope, ProjectID: f.project, Arguments: workflowevents.Arguments{AISessionID: f.session, TaskID: task.ID}, ExpiresAt: test.expiry}
		if e := store.Put(ctx, sub); e != nil {
			t.Fatal(e)
		}
		if active, e := store.Active(ctx, sub.ID, now); e != nil || active {
			t.Fatalf("invalid subscription is active: %s %v", sub.ID, e)
		}
	}
	tx, e := f.db.beginWrite(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: f.project, TaskID: task.ID, EntityType: "task", EntityID: task.ID, CreatedAt: now}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if rows, e := store.Pending(ctx, now.Add(time.Second)); e != nil || len(rows) != 0 {
		t.Fatal("expired/foreign subscription delivered", e)
	}
}
