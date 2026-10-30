package controlapi_test

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

// Completed command readback must work even when every write transaction is
// refused. Stale callbacks may observe history but cannot acquire new effects.
func assertDeliveryReplayReadOnly(t *testing.T, f *upperLifecycleFixture, check func(context.Context)) {
	t.Helper()
	f.plane.Retirement.Close()
	if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER reject_delivery_replay_write BEFORE UPDATE ON runtime_authority BEGIN SELECT RAISE(ABORT,'replay attempted a write transaction'); END`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER reject_delivery_replay_write`); err != nil {
			t.Error(err)
		}
	}()
	check(f.ctx)
	check(runtimeepoch.WithFence(f.ctx, "obsolete-delivery-observer", ""))
}
