package sqlite

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestRuntimePlatformWritePreservesEpochAndRunFences(t *testing.T) {
	for _, kind := range []string{"stale-epoch", "missing-run", "canceled-context"} {
		t.Run(kind, func(t *testing.T) {
			f := newPlatformStoreFixture(t)
			ctx := f.ctx
			want := runtimeepoch.ErrFenced
			switch kind {
			case "stale-epoch":
				ctx = runtimeepoch.WithFence(ctx, "retired-epoch", "")
			case "missing-run":
				ctx = runtimeepoch.WithFence(ctx, f.db.epoch, "missing-run")
			case "canceled-context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			platformRejectWithoutWrites(t, f, want, func() error {
				_, err := f.store.RegisterDescriptor(ctx, f.descriptor)
				return err
			})
			// A failed fence must release its pinned connection and transaction.
			if _, err := f.store.RegisterDescriptor(f.ctx, f.descriptor); err != nil {
				t.Fatalf("valid write after rejected fence: %v", err)
			}
		})
	}
}
