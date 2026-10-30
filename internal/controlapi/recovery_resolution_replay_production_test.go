package controlapi_test

import (
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/recoverystatus"
)

// The subprocess speaks the App Server protocol; platform qualification is
// explicitly substituted by the fixture, not asserted as native compatibility.
func TestRecoveryResolutionReplyLossAndColdReplay(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVBRIDGE_RECOVERY_FAKE_SERVER", "1")
	t.Setenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY", executable)
	runRecoveryStatusOffline(t, "complete-old-unknown", func(f *upperLifecycleFixture, cfg controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
		original := f.plane.RecoveryStatus.Store
		fault := &recoveryWindowStore{Store: original, recoveryChildWindowStore: original.(recoveryChildWindowStore), window: "resolution-reply-lost"}
		f.plane.RecoveryStatus.Store = fault
		if _, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err != nil {
			t.Fatal(err)
		}
		assessed := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "ASSESSED" || r.State == "FAILED" })
		if assessed.State != "ASSESSED" {
			t.Fatalf("assessment failed: %+v", assessed)
		}
		if _, err := resolveRecoveryFixture(t, f, cmd, assessed); err == nil || !fault.hit.Load() {
			t.Fatal("resolution response-loss fault did not fire")
		}
		committed, err := original.Read(f.ctx, cmd.ID)
		if err != nil || committed.State != "RESOLVED" {
			t.Fatalf("missing committed receipt: %v", err)
		}
		for _, cold := range []bool{false, true} {
			if cold {
				reopenRecoveryFixture(t, f, cfg)
			}
			if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER IF NOT EXISTS reject_resolution_replay_update BEFORE UPDATE ON recovery_status_records BEGIN SELECT RAISE(ABORT,'resolution replay attempted write'); END`); err != nil {
				t.Fatal(err)
			}
			before := definitionFormalSnapshot(t, f, definitionFormalEveryTable(t, f))
			replay, err := resolveRecoveryFixture(t, f, cmd, assessed)
			if err != nil || !reflect.DeepEqual(replay.Record, committed) {
				t.Fatalf("replay cold=%v differs from committed receipt: %v", cold, err)
			}
			after := definitionFormalSnapshot(t, f, definitionFormalEveryTable(t, f))
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("replay cold=%v mutated durable state", cold)
			}
			if calls.Load() != 4 {
				t.Fatalf("replay dispatched model: calls=%d", calls.Load())
			}
		}
		return false
	})
}
