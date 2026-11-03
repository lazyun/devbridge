package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/workflowevents"
)

// A callback can finish after Unsubscribe deleted its delivery and Subscribe
// recreated the same deterministic subscription ID. Its result must not stop
// the replacement subscription or suppress that subscription's new events.
func TestWorkflowEventStaleCompletionCannotStopReplacement(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "current-delivery-stops-subscription"
		if replace {
			name = "removed-delivery-cannot-stop-replacement"
		}
		t.Run(name, func(t *testing.T) {
			f, task := newTaskOwnedFixture(t)
			ctx := context.Background()
			store := NewWorkflowEventStore(f.db)
			now := time.Now().UTC()
			sub := workflowevents.Subscription{
				ID: "stable-subscription", Scope: "owned-caller", ProjectID: f.project,
				Arguments: workflowevents.Arguments{AISessionID: f.session, TaskID: task.ID},
				Delivery:  workflowevents.Delivery{Mode: "webhook", URL: "https://example.com/events", Secret: "test-only-not-transmitted"},
				ExpiresAt: now.Add(time.Hour),
			}
			if err := store.Put(ctx, sub); err != nil {
				t.Fatal(err)
			}
			emit := func() string {
				t.Helper()
				tx, err := f.db.beginWrite(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: f.project, TaskID: task.ID, EntityType: "task", EntityID: task.ID, CreatedAt: now}); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				events, err := NewEventStore(f.db).List(ctx, event.Filter{TaskID: task.ID})
				if err != nil || len(events) == 0 {
					t.Fatalf("persisted event: %v", err)
				}
				return events[len(events)-1].ID
			}
			oldID := emit()
			rows, err := store.Pending(ctx, now.Add(time.Second))
			if err != nil || len(rows) != 1 || rows[0].EventID != oldID || rows[0].Attempts != 0 {
				t.Fatalf("initial delivery: %+v, %v", rows, err)
			}
			inFlight := rows[0]
			var replacementID string
			if replace {
				if err = store.Remove(ctx, sub.Scope, sub.ID); err != nil {
					t.Fatal(err)
				}
				if err = store.Put(ctx, sub); err != nil {
					t.Fatal(err)
				}
				replacementID = emit()
				rows, err = store.Pending(ctx, now.Add(time.Second))
				if err != nil || len(rows) != 1 || rows[0].EventID != replacementID {
					t.Fatalf("replacement delivery: %+v, %v", rows, err)
				}
			}
			// Dispatch maps HTTP 410/413 and non-429 4xx to STOPPED. Only
			// the durable delivery that won the CAS may stop its subscription.
			if err = store.Finish(ctx, inFlight, "STOPPED", now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			status, err := store.Status(ctx, f.project, task.ID, now)
			if err != nil {
				t.Fatal(err)
			}
			if !replace {
				if status.ActiveSubscriptions != 0 || status.StoppedSubscriptions != 1 || status.Pending != 0 {
					t.Fatalf("current callback did not stop its subscription: %+v", status)
				}
				return
			}
			if status.ActiveSubscriptions != 1 || status.StoppedSubscriptions != 0 || status.Pending != 1 {
				t.Fatalf("stale callback stopped replacement: %+v", status)
			}
			rows, err = store.Pending(ctx, now.Add(2*time.Second))
			if err != nil || len(rows) != 1 || rows[0].EventID != replacementID || rows[0].Attempts != 0 {
				t.Fatalf("replacement delivery changed: %+v, %v", rows, err)
			}
			if err = store.Finish(ctx, rows[0], "DELIVERED", now.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			laterID := emit()
			rows, err = store.Pending(ctx, now.Add(3*time.Second))
			if err != nil || len(rows) != 1 || rows[0].EventID != laterID || rows[0].Attempts != 0 {
				t.Fatalf("replacement cannot receive later events: %+v, %v", rows, err)
			}
			status, err = store.Status(ctx, f.project, task.ID, now)
			if err != nil || status.Delivered != 1 || status.Pending != 1 || status.ActiveSubscriptions != 1 {
				t.Fatalf("replacement delivery history: %+v, %v", status, err)
			}
		})
	}
}
