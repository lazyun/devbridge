package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func liveUsageFixture(t *testing.T, suffix string) (*CodexUsageStore, context.Context, codexusage.CodexUsageLedger, codexusage.RunUsageAttribution, *DB) {
	t.Helper()
	ctx := context.Background()
	f := newAgentStorageFixture(t, suffix)
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, suffix, true)
	thread, turn := "thread-"+suffix, "turn-"+suffix
	ledger := codexusage.CodexUsageLedger{ThreadID: thread, AccountingEpoch: 1, Anchored: true, Revision: 1}
	store := NewCodexUsageStore(f.db)
	runCtx := runtimeepoch.WithFence(ctx, r.RuntimeEpoch, r.ID)
	if err := store.Save(runCtx, 0, ledger, nil); err != nil {
		t.Fatal(err)
	}
	tracker := codexusage.Begin(ledger, r.ID, turn)
	tracker.Observe(codexusage.Counts{TotalTokens: 10}, nil)
	tracker.Ledger.Revision++
	return store, runCtx, tracker.Ledger, tracker.Attribution, f.db
}

func TestCodexUsageFenceCASRace(t *testing.T) {
	store, ctx, ledger, attribution, _ := liveUsageFixture(t, "usage-fence-race")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- store.Save(ctx, 1, ledger, &attribution) }()
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatalf("race err=%v", err)
		}
	}
	if success != 1 {
		t.Fatalf("CAS successes=%d", success)
	}
}

func TestCodexUsageFenceRejectsAuthorityClaimAndAttributionMismatches(t *testing.T) {
	store, ctx, ledger, attribution, db := liveUsageFixture(t, "usage-fence-mismatch")
	stale := runtimeepoch.WithFence(context.Background(), "runtime-stale", attribution.RunID)
	if err := store.Save(stale, 1, ledger, &attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale=%v", err)
	}
	wrong := attribution
	wrong.RunID = "wrong-run"
	if err := store.Save(ctx, 1, ledger, &wrong); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("wrong attribution=%v", err)
	}
	empty := attribution
	empty.RunID = ""
	if err := store.Save(ctx, 1, ledger, &empty); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("empty attribution=%v", err)
	}
	if _, err := db.SQL().Exec(`DELETE FROM feature_area_agent_claims WHERE run_id=?`, attribution.RunID); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, 1, ledger, &attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("claim mismatch=%v", err)
	}
}

func TestCodexUsageFenceClosesBeforeTerminalAndPreservesLedger(t *testing.T) {
	for _, boundary := range []string{"finalization intent", "canceled write"} {
		t.Run(boundary, func(t *testing.T) {
			store, ctx, ledger, attribution, db := liveUsageFixture(t, "usage-closed-"+boundary)
			want := runtimeepoch.ErrFenced
			switch boundary {
			case "finalization intent":
				if err := NewAgentConversationStore(db).RecordFinalization(ctx, attribution.RunID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: attribution.TurnID, Quiesced: true}); err != nil {
					t.Fatal(err)
				}
			case "canceled write":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			if err := store.Save(ctx, 1, ledger, &attribution); !errors.Is(err, want) {
				t.Fatalf("write after %s: %v", boundary, err)
			}
			stored, exists, err := store.Load(context.Background(), ledger.ThreadID)
			if err != nil || !exists || stored.Revision != 1 || stored.LogicalCumulative.TotalTokens != 0 {
				t.Fatalf("rejected write mutated ledger: %+v %v", stored, err)
			}
		})
	}
}

func TestCodexUsageClaimMismatchCannotReplaceLiveAuthority(t *testing.T) {
	store, ctx, ledger, attribution, db := liveUsageFixture(t, "usage-invalid-claim")
	// The shared ownership trigger rejects a mismatched claim before any
	// callback can observe it; the legitimate owner must still be able to save.
	if _, err := db.SQL().Exec(`UPDATE feature_area_agent_claims SET operation_id='another-operation' WHERE run_id=?`, attribution.RunID); err == nil {
		t.Fatal("mismatched claim replaced authoritative Run ownership")
	}
	if err := store.Save(ctx, 1, ledger, &attribution); err != nil {
		t.Fatalf("rejected claim mutation poisoned current authority: %v", err)
	}
}

func TestCodexUsageFenceRejectsOldRuntimeAfterRestartWithMatchingRevision(t *testing.T) {
	oldStore, oldCtx, ledger, attribution, oldDB := liveUsageFixture(t, "usage-restart-fence")
	if err := oldDB.Close(); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, oldDB.stateRoot)
	defer db.Close()
	currentStore := NewCodexUsageStore(db)
	for _, attempt := range []struct {
		store *CodexUsageStore
		ctx   context.Context
	}{
		{oldStore, oldCtx},
		{currentStore, oldCtx},
		{currentStore, runtimeepoch.WithFence(context.Background(), db.epoch, attribution.RunID)},
	} {
		if err := attempt.store.Save(attempt.ctx, 1, ledger, &attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
			t.Fatalf("abandoned Run regained authority: %v", err)
		}
	}
	stored, exists, err := currentStore.Load(context.Background(), ledger.ThreadID)
	if err != nil || !exists || stored.Revision != 1 || stored.AccountingEpoch != ledger.AccountingEpoch {
		t.Fatalf("restart fence altered token coordinates: %+v %v", stored, err)
	}
}
