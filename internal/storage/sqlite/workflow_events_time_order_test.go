package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/workflowevents"
)

// RFC3339Nano omits trailing fractional zeroes. Persisted timestamps must be
// ordered by their instant, not by the position of the trailing UTC marker.
func TestWorkflowEventExpiryFractionalOrdering(t *testing.T) {
	for _, fraction := range []time.Duration{0, 100 * time.Millisecond, 120 * time.Millisecond, 123456789 * time.Nanosecond} {
		t.Run(fraction.String(), func(t *testing.T) {
			f, task := newTaskOwnedFixture(t)
			ctx := context.Background()
			expiry := time.Now().UTC().Truncate(time.Second).Add(time.Hour + fraction)
			store := NewWorkflowEventStore(f.db)
			sub := workflowevents.Subscription{ID: "time-order-sub", Scope: "owned-caller", ProjectID: f.project, Arguments: workflowevents.Arguments{AISessionID: f.session, TaskID: task.ID}, ExpiresAt: expiry}
			if err := store.Put(ctx, sub); err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.beginWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: f.project, TaskID: task.ID, EntityType: "task", EntityID: task.ID, CreatedAt: expiry.Add(-time.Second)}); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for _, cold := range []bool{false, true} {
				if cold {
					root := f.db.stateRoot
					if err := f.db.Close(); err != nil {
						t.Fatal(err)
					}
					f.db = openTestDB(t, root)
					t.Cleanup(func() { f.db.Close() })
					store = NewWorkflowEventStore(f.db)
				}
				for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
					now := expiry.Add(offset)
					want := offset < 0
					active, err := store.Active(ctx, sub.ID, now)
					if err != nil || active != want {
						t.Errorf("cold=%v expiry=%s now=%s Active=%v want=%v err=%v", cold, formatTime(expiry), formatTime(now), active, want, err)
					}
					status, err := store.Status(ctx, f.project, task.ID, now)
					wantCount := 0
					if want {
						wantCount = 1
					}
					if err != nil || status.ActiveSubscriptions != wantCount || status.Pending != wantCount {
						t.Errorf("cold=%v offset=%s Status=%+v want active=%d err=%v", cold, offset, status, wantCount, err)
					}
					rows, err := store.Pending(ctx, now)
					if err != nil || len(rows) != wantCount {
						t.Errorf("cold=%v offset=%s pending=%d want=%d err=%v", cold, offset, len(rows), wantCount, err)
					}
				}
			}
		})
	}
}

func TestWorkflowEventRetryFractionalOrdering(t *testing.T) {
	f, task := newTaskOwnedFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	store := NewWorkflowEventStore(f.db)
	sub := workflowevents.Subscription{ID: "retry-time-sub", Scope: "owned-caller", ProjectID: f.project, Arguments: workflowevents.Arguments{AISessionID: f.session, TaskID: task.ID}, ExpiresAt: base.Add(time.Hour)}
	if err := store.Put(ctx, sub); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: f.project, TaskID: task.ID, EntityType: "task", EntityID: task.ID, CreatedAt: base.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Pending(ctx, base)
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial delivery=%+v err=%v", rows, err)
	}
	current := rows[0]
	for attempt, fraction := range []time.Duration{0, 100 * time.Millisecond, 120 * time.Millisecond, 123456789 * time.Nanosecond} {
		due := base.Add(time.Duration(attempt+1)*time.Second + fraction)
		if err = store.Finish(ctx, current, "RETRY", due); err != nil {
			t.Fatal(err)
		}
		for _, cold := range []bool{false, true} {
			if cold {
				root := f.db.stateRoot
				if err = f.db.Close(); err != nil {
					t.Fatal(err)
				}
				f.db = openTestDB(t, root)
				t.Cleanup(func() { f.db.Close() })
				store = NewWorkflowEventStore(f.db)
			}
			for _, now := range []time.Time{due.Truncate(time.Second).Add(-time.Nanosecond), due.Truncate(time.Second), due.Add(-time.Nanosecond), due, due.Add(time.Nanosecond)} {
				rows, err = store.Pending(ctx, now)
				want := 0
				if !now.Before(due) {
					want = 1
				}
				if err != nil || len(rows) != want {
					t.Errorf("cold=%v due=%s now=%s pending=%d want=%d err=%v", cold, formatTime(due), formatTime(now), len(rows), want, err)
				}
				if len(rows) == 1 && (rows[0].EventID != current.EventID || rows[0].Attempts != attempt+1) {
					t.Fatalf("read changed event identity/attempt: %+v", rows[0])
				}
			}
		}
		rows, err = store.Pending(ctx, due.Add(time.Second))
		if err != nil || len(rows) != 1 {
			t.Fatalf("positive control=%+v err=%v", rows, err)
		}
		current = rows[0]
	}
	if err = store.Finish(ctx, current, "DELIVERED", base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if rows, err = store.Pending(ctx, base.Add(time.Minute)); err != nil || len(rows) != 0 {
		t.Fatalf("delivered event retried: %+v err=%v", rows, err)
	}
}
