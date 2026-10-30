package controlapi_test

import (
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestStaleTaskDeliveryRetryDoesNotWriteFailure(t *testing.T) {
	f, _, _, _ := rejectedDeliveryV118(t)
	oldEpoch := f.plane.DB.RuntimeEpoch()
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	if f.plane.DB.RuntimeEpoch() == oldEpoch {
		t.Fatal("restart did not rotate writer")
	}
	var before, after string
	const snapshot = `SELECT json_object('error',last_error,'completed',completed_at,'outcome',terminal_outcome) FROM task_commit_receipts WHERE task_id=?`
	if err := f.plane.DB.SQL().QueryRow(snapshot, f.task).Scan(&before); err != nil {
		t.Fatal(err)
	}
	stale := runtimeepoch.WithFence(f.ctx, oldEpoch, "")
	if _, err := f.facade.RetryAcceptedTaskCommit(stale, f.session, f.task); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale retry not fenced: %v", err)
	}
	if err := f.plane.DB.SQL().QueryRow(snapshot, f.task).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("fenced callback rewrote delivery receipt: before=%s after=%s", before, after)
	}
}
