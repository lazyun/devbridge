package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/workflowevents"
)

func TestWorkflowEventKeyRotationFencesOldCallback(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		name := "same-key-refresh"
		if rotate {
			name = "rotated-key-refresh"
		}
		for _, outcome := range []string{"STOPPED", "RETRY", "EXHAUSTED", "DELIVERED"} {
			t.Run(name+"/"+outcome, func(t *testing.T) {
				f, task := newTaskOwnedFixture(t)
				ctx := context.Background()
				store := NewWorkflowEventStore(f.db)
				now := time.Now().UTC()
				sub := workflowevents.Subscription{
					ID: "rotating-subscription", Scope: "owned-caller", ProjectID: f.project,
					Arguments: workflowevents.Arguments{AISessionID: f.session, TaskID: task.ID},
					Delivery:  workflowevents.Delivery{Mode: "webhook", URL: "https://example.com/events", Secret: "old-test-key"},
					ExpiresAt: now.Add(time.Hour),
				}
				if err := store.Put(ctx, sub); err != nil {
					t.Fatal(err)
				}
				emit := func() {
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
				}
				emit()
				rows, err := store.Pending(ctx, now.Add(time.Second))
				if err != nil || len(rows) != 1 {
					t.Fatalf("initial delivery=%+v err=%v", rows, err)
				}
				inFlight := rows[0]
				// Subscribe refreshes an existing deterministic ID through Put after
				// verifying the new key. The pending delivery remains the same row.
				if rotate {
					sub.Delivery.Secret = "new-test-key"
				}
				sub.ExpiresAt = now.Add(2 * time.Hour)
				if err = store.Put(ctx, sub); err != nil {
					t.Fatal(err)
				}
				rows, err = store.Pending(ctx, now.Add(time.Second))
				if err != nil || len(rows) != 1 || rows[0].Subscription.Delivery.Secret != sub.Delivery.Secret || rows[0].EventID != inFlight.EventID || string(rows[0].Body) != string(inFlight.Body) {
					t.Fatalf("key refresh changed delivery identity or did not rotate key: %+v err=%v", rows, err)
				}
				current := rows[0]
				// For example, the receiver rejects the already in-flight old-key
				// request with 401 after it has verified the refreshed subscription.
				if err = store.Finish(ctx, inFlight, outcome, now.Add(time.Minute)); err != nil {
					t.Fatal(err)
				}
				status, err := store.Status(ctx, f.project, task.ID, now)
				if !rotate {
					want := workflowevents.Status{ActiveSubscriptions: 1}
					switch outcome {
					case "STOPPED":
						want = workflowevents.Status{StoppedSubscriptions: 1}
					case "RETRY":
						want.Pending = 1
					case "EXHAUSTED":
						want.Exhausted = 1
					case "DELIVERED":
						want.Delivered = 1
					}
					if err != nil || status != want {
						t.Fatalf("same-key refresh suppressed callback: %+v want=%+v err=%v", status, want, err)
					}
					rows, err = store.Pending(ctx, now.Add(2*time.Minute))
					if err != nil || len(rows) != want.Pending || (len(rows) == 1 && rows[0].Attempts != 1) {
						t.Fatalf("same-key retry not preserved: %+v err=%v", rows, err)
					}
					return
				}
				if err != nil || status.ActiveSubscriptions != 1 || status.StoppedSubscriptions != 0 || status.Pending != 1 || status.Delivered != 0 || status.Exhausted != 0 {
					t.Fatalf("old-key callback changed refreshed subscription/delivery: %+v err=%v", status, err)
				}
				rows, err = store.Pending(ctx, now.Add(time.Second))
				if err != nil || len(rows) != 1 || rows[0].Attempts != 0 || rows[0].EventID != current.EventID || rows[0].Subscription.Delivery.Secret != sub.Delivery.Secret {
					t.Fatalf("old-key callback consumed or deferred current delivery: %+v err=%v", rows, err)
				}
				if err = store.Finish(ctx, current, "DELIVERED", now); err != nil {
					t.Fatal(err)
				}
				emit()
				rows, err = store.Pending(ctx, now.Add(time.Second))
				if err != nil || len(rows) != 1 || rows[0].EventID == current.EventID || rows[0].Subscription.Delivery.Secret != sub.Delivery.Secret {
					t.Fatalf("refreshed subscription cannot receive later event: %+v err=%v", rows, err)
				}
				// A current-key permanent rejection must still stop the subscription.
				if err = store.Finish(ctx, rows[0], "STOPPED", now); err != nil {
					t.Fatal(err)
				}
				status, err = store.Status(ctx, f.project, task.ID, now)
				if err != nil || status.ActiveSubscriptions != 0 || status.StoppedSubscriptions != 1 || status.Delivered != 1 || status.Pending != 0 {
					t.Fatalf("current-key callback was ignored: %+v err=%v", status, err)
				}
			})
		}
	}
}
