package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestUsageLedgerAndRunAttributionPersistAtomically(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "epoch-usage")
	root := f.root
	db := f.db
	runValue, _ := beginRecoveryRun(t, f, agentconversation.Execution, "epoch-usage", true)
	threadID, turnID := "thread-epoch-usage", "turn-epoch-usage"
	runContext := runtimeepoch.WithFence(ctx, runValue.RuntimeEpoch, runValue.ID)
	store := NewCodexUsageStore(db)
	ledger := codexusage.CodexUsageLedger{ThreadID: threadID, AccountingEpoch: 2, PreviousRawCumulative: codexusage.Counts{TotalTokens: 10}, LogicalCumulative: codexusage.Counts{TotalTokens: 1010}, Anchored: true, Revision: 1}
	if err := store.Save(runContext, 0, ledger, nil); err != nil {
		t.Fatal(err)
	}
	tracker := codexusage.Begin(ledger, runValue.ID, turnID)
	tracker.Observe(codexusage.Counts{TotalTokens: 40}, nil)
	tracker.Ledger.Revision++
	if err := store.Save(runContext, 1, tracker.Ledger, &tracker.Attribution); err != nil {
		t.Fatal(err)
	}
	// A competing stale writer cannot modify either table.
	stale := tracker.Ledger
	stale.LogicalCumulative.TotalTokens = 9000
	if err := store.Save(ctx, 1, stale, nil); err == nil {
		t.Fatal("accepted stale revision")
	}
	tracker.Finish(true)
	tracker.Ledger.Revision++
	if err := store.Save(runContext, 2, tracker.Ledger, &tracker.Attribution); err != nil {
		t.Fatal(err)
	}
	// Rewriting a completed attribution rolls back the ledger change as well.
	bad := tracker.Ledger
	bad.Revision++
	if err := store.Save(runContext, 3, bad, &tracker.Attribution); err == nil {
		t.Fatal("rewrote completed attribution")
	}
	service := run.NewService(NewRunStore(db), nil, nil)
	for _, status := range []run.Status{run.StatusCollecting} {
		if _, err := service.Transition(runContext, runValue.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	a := tracker.Attribution
	if _, err := service.SucceedWithUsage(runContext, runValue.ID, run.Result{}, &run.TokenUsageObservation{Reliable: a.Reliable, StartCumulative: run.TokenCounts(a.LogicalStart), EndCumulative: run.TokenCounts(a.LogicalEnd)}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	store = NewCodexUsageStore(db)
	got, exists, err := store.Load(ctx, threadID)
	if err != nil || !exists || got.Revision != 3 || got.LogicalCumulative.TotalTokens != 1040 || got.PreviousRawCumulative.TotalTokens != 40 || got.AccountingEpoch != 2 {
		t.Fatalf("ledger=%+v exists=%v err=%v", got, exists, err)
	}
	attribution, err := store.Attribution(ctx, runValue.ID)
	if err != nil || !attribution.Complete || !attribution.Reliable || attribution.LogicalStart.TotalTokens != 1010 || attribution.LogicalEnd.TotalTokens != 1040 {
		t.Fatalf("attribution=%+v err=%v", attribution, err)
	}
	r, err := NewRunStore(db).Get(ctx, runValue.ID)
	if err != nil || r.TokenUsage.Status != run.TokenUsageAvailable || r.TokenUsage.Usage == nil || r.TokenUsage.Usage.TotalTokens != 30 {
		t.Fatalf("run=%+v err=%v", r, err)
	}
}

func TestUsageLedgerRejectsStaleRuntimeAndInactiveRunOwnership(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "usage-runtime-fence")
	runValue, _ := beginRecoveryRun(t, f, agentconversation.Execution, "usage-runtime-fence", true)
	threadID, turnID := "thread-usage-runtime-fence", "turn-usage-runtime-fence"
	current := runtimeepoch.WithFence(ctx, runValue.RuntimeEpoch, runValue.ID)
	store := NewCodexUsageStore(f.db)
	ledger := codexusage.CodexUsageLedger{ThreadID: threadID, AccountingEpoch: 1, Anchored: true, Revision: 1}
	if err := store.Save(current, 0, ledger, nil); err != nil {
		t.Fatal(err)
	}
	tracker := codexusage.Begin(ledger, runValue.ID, turnID)
	tracker.Observe(codexusage.Counts{TotalTokens: 10}, nil)
	tracker.Ledger.Revision++
	if err := store.Save(current, 1, tracker.Ledger, &tracker.Attribution); err != nil {
		t.Fatalf("current runtime write: %v", err)
	}
	withoutRunFence := tracker.Ledger
	withoutRunFence.Revision++
	if err := store.Save(ctx, 2, withoutRunFence, nil); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("Run-owned ledger without Run fence: %v", err)
	}

	next := tracker.Ledger
	next.Revision++
	wrongThread := next
	wrongThread.ThreadID = "another-canonical-thread"
	wrongAttribution := tracker.Attribution
	wrongAttribution.ThreadID = wrongThread.ThreadID
	if err := store.Save(current, 2, wrongThread, &wrongAttribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("wrong canonical Thread write: %v", err)
	}
	stale := runtimeepoch.WithFence(ctx, "runtime_stale_usage", runValue.ID)
	if err := store.Save(stale, 2, next, &tracker.Attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale runtime write: %v", err)
	}
	wrongRun := runtimeepoch.WithFence(ctx, runValue.RuntimeEpoch, "other-run")
	if err := store.Save(wrongRun, 2, next, &tracker.Attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("wrong Run write: %v", err)
	}
	if _, err := f.db.SQL().ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=?`, runValue.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(current, 2, next, &tracker.Attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("released claim write: %v", err)
	}
	loaded, exists, err := store.Load(ctx, ledger.ThreadID)
	if err != nil || !exists || loaded.Revision != 2 || loaded.LogicalCumulative.TotalTokens != 10 {
		t.Fatalf("ledger after fenced writes=%+v exists=%v err=%v", loaded, exists, err)
	}
}

func TestUsageLedgerAllowsStandaloneProbeAndRejectsStaleAuthority(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	store := NewCodexUsageStore(db)
	ledger := codexusage.CodexUsageLedger{ThreadID: "context-probe-thread", AccountingEpoch: 1, Anchored: true, Revision: 1}
	if err := store.Save(ctx, 0, ledger, nil); err != nil {
		t.Fatalf("standalone bootstrap: %v", err)
	}
	tracker := codexusage.Begin(ledger, "", "probe-turn")
	tracker.Observe(codexusage.Counts{TotalTokens: 10}, nil)
	tracker.Finish(true)
	tracker.Ledger.Revision++
	if err := store.Save(ctx, 1, tracker.Ledger, &tracker.Attribution); err != nil {
		t.Fatalf("standalone completion: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO runtime_epochs(id,created_at) VALUES(?,?)`, "runtime_usage_stale_authority", formatTime(testNow)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE runtime_authority SET epoch_id=? WHERE singleton=1`, "runtime_usage_stale_authority"); err != nil {
		t.Fatal(err)
	}
	late := tracker.Ledger
	late.Revision++
	if err := store.Save(ctx, 2, late, &tracker.Attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale authority standalone completion: %v", err)
	}
}

func TestUsageLedgerAllowsLiveDispatchBeforeTurnIsRecorded(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "usage-pre-turn")
	runValue, _ := beginRecoveryRun(t, f, agentconversation.Execution, "usage-pre-turn", false)
	if err := NewAgentConversationStore(f.db).BindThread(ctx, runValue.ID, "thread-usage-pre-turn"); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, runValue.ID, run.StatusRunning, "thread-usage-pre-turn", "")
	store := NewCodexUsageStore(f.db)
	ledger := codexusage.CodexUsageLedger{ThreadID: "thread-usage-pre-turn", AccountingEpoch: 1, Anchored: true, Revision: 1}
	runContext := runtimeepoch.WithFence(ctx, runValue.RuntimeEpoch, runValue.ID)
	if err := store.Save(runContext, 0, ledger, nil); err != nil {
		t.Fatal(err)
	}
	tracker := codexusage.Begin(ledger, runValue.ID, "turn-not-yet-recorded")
	tracker.Ledger.Revision++
	if err := store.Save(runContext, 1, tracker.Ledger, &tracker.Attribution); err != nil {
		t.Fatalf("live pre-turn usage write: %v", err)
	}
}

func TestUsageLedgerRejectsLateTelemetryAfterRunTerminal(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "usage-terminal-fence")
	runValue, _ := beginRecoveryRun(t, f, agentconversation.Execution, "usage-terminal-fence", true)
	threadID, turnID := "thread-usage-terminal-fence", "turn-usage-terminal-fence"
	runContext := runtimeepoch.WithFence(ctx, runValue.RuntimeEpoch, runValue.ID)
	store := NewCodexUsageStore(f.db)
	ledger := codexusage.CodexUsageLedger{ThreadID: threadID, AccountingEpoch: 1, Anchored: true, Revision: 1}
	if err := store.Save(runContext, 0, ledger, nil); err != nil {
		t.Fatal(err)
	}
	tracker := codexusage.Begin(ledger, runValue.ID, turnID)
	tracker.Observe(codexusage.Counts{TotalTokens: 10}, nil)
	tracker.Ledger.Revision++
	if err := store.Save(runContext, 1, tracker.Ledger, &tracker.Attribution); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SQL().ExecContext(ctx, `UPDATE runs SET status='SUCCEEDED' WHERE id=?`, runValue.ID); err != nil {
		t.Fatal(err)
	}
	next := tracker.Ledger
	next.Revision++
	if err := store.Save(runContext, 2, next, &tracker.Attribution); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("late terminal telemetry write: %v", err)
	}
}

func TestUsageDispatchCrashMarkerSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	store := NewCodexUsageStore(db)
	ledger := codexusage.CodexUsageLedger{ThreadID: "canonical", AccountingEpoch: 1, Anchored: true, Revision: 1, InFlightTurnID: "pending", PreviousRawCumulative: codexusage.Counts{TotalTokens: 100}, LogicalCumulative: codexusage.Counts{TotalTokens: 100}}
	if err := store.Save(ctx, 0, ledger, nil); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db = openTestDB(t, root)
	defer db.Close()
	ledger, _, err := NewCodexUsageStore(db).Load(ctx, "canonical")
	if err != nil {
		t.Fatal(err)
	}
	tracker := codexusage.Begin(ledger, "run", "new-turn")
	tracker.Observe(codexusage.Counts{TotalTokens: 150}, nil)
	tracker.Finish(true)
	if tracker.Attribution.Reliable || tracker.Ledger.LogicalCumulative.TotalTokens != 100 || !tracker.Ledger.Anchored {
		t.Fatal(tracker)
	}
}
