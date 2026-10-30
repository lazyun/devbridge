package codexusage

import (
	"math"
	"testing"
)

func counts(n int64) Counts {
	return Counts{InputTokens: n * 4, CachedInputTokens: n, CacheWriteInputTokens: n, OutputTokens: n * 2, ReasoningOutputTokens: n, TotalTokens: n * 6}
}

func TestEpochResetRecoversNextRunAcrossAllCounters(t *testing.T) {
	ledger := CodexUsageLedger{ThreadID: "canonical", AccountingEpoch: 1, Anchored: true}
	for i, tc := range []struct {
		raw   int64
		exact bool
		delta int64
		epoch int64
	}{
		{100, true, 100, 1}, {120, true, 20, 1},
		{10, false, 0, 2}, {15, true, 5, 2},
		{15, true, 0, 2}, {2, false, 0, 3}, {8, true, 6, 3},
	} {
		tracker := Begin(ledger, "run", string(rune('a'+i)))
		tracker.Observe(counts(tc.raw), nil)
		tracker.Observe(counts(tc.raw), nil) // duplicate / rate-limit rebroadcast
		tracker.Finish(true)
		if tracker.Attribution.Reliable != tc.exact || tracker.Ledger.AccountingEpoch != tc.epoch || tracker.Ledger.ThreadID != "canonical" {
			t.Fatalf("step %d: %+v", i, tracker)
		}
		if tc.exact {
			delta, ok := Subtract(tracker.Attribution.LogicalEnd, tracker.Attribution.LogicalStart)
			if !ok || delta != counts(tc.delta) {
				t.Fatalf("step %d: delta=%+v", i, delta)
			}
		}
		ledger = tracker.Ledger
	}
}

func TestReplayAndAmbiguousRegressionNeverOpenMidTurnEpoch(t *testing.T) {
	tracker := Begin(CodexUsageLedger{ThreadID: "t", AccountingEpoch: 1, Anchored: true}, "r", "turn")
	tracker.Observe(counts(10), nil)
	tracker.Observe(counts(20), nil)
	tracker.Observe(counts(10), nil) // known stale sample
	if !tracker.Attribution.Reliable || tracker.Ledger.LogicalCumulative != counts(20) {
		t.Fatal(tracker)
	}
	tracker.Observe(counts(5), nil) // reset vs out-of-order cannot be proved
	if tracker.Attribution.Reliable || tracker.Ledger.AccountingEpoch != 1 || tracker.Ledger.PreviousRawCumulative != counts(20) {
		t.Fatal(tracker)
	}
	tracker.Observe(counts(25), nil)
	tracker.Finish(true)
	next := Begin(tracker.Ledger, "next", "next-turn")
	next.Observe(counts(30), nil)
	next.Finish(true)
	delta, _ := Subtract(next.Attribution.LogicalEnd, next.Attribution.LogicalStart)
	if !next.Attribution.Reliable || delta != counts(5) {
		t.Fatal(next)
	}
}

func TestMissingAndInterruptedBaselineReanchorsWithoutChargingUnknownPrefix(t *testing.T) {
	for _, ledger := range []CodexUsageLedger{
		{ThreadID: "t", AccountingEpoch: 1},
		{ThreadID: "t", AccountingEpoch: 2, Anchored: true, InFlightTurnID: "interrupted", PreviousRawCumulative: counts(100), LogicalCumulative: counts(200)},
	} {
		tracker := Begin(ledger, "r", "new")
		tracker.Observe(counts(150), nil)
		tracker.Finish(true)
		if tracker.Attribution.Reliable || tracker.Ledger.LogicalCumulative != ledger.LogicalCumulative {
			t.Fatal(tracker)
		}
		next := Begin(tracker.Ledger, "r2", "next")
		next.Observe(counts(160), nil)
		next.Finish(true)
		delta, _ := Subtract(next.Attribution.LogicalEnd, next.Attribution.LogicalStart)
		if !next.Attribution.Reliable || delta != counts(10) {
			t.Fatal(next)
		}
	}
}

func TestNoTelemetryIsNotZeroAndDoesNotLeakIntoNextRun(t *testing.T) {
	tracker := Begin(CodexUsageLedger{ThreadID: "t", AccountingEpoch: 1, Anchored: true}, "r", "turn")
	tracker.Finish(true)
	if tracker.Attribution.Reliable || tracker.Ledger.Anchored {
		t.Fatal(tracker)
	}
	next := Begin(tracker.Ledger, "r2", "turn2")
	next.Observe(counts(50), nil)
	next.Finish(true)
	if next.Attribution.Reliable || next.Ledger.LogicalCumulative != (Counts{}) {
		t.Fatal(next)
	}
}

func TestInvalidCountsAndOverflowCannotProduceExactUsage(t *testing.T) {
	for _, raw := range []Counts{{TotalTokens: -1}, {CacheWriteInputTokens: -1}} {
		tracker := Begin(CodexUsageLedger{ThreadID: "t", Anchored: true}, "r", "turn")
		tracker.Observe(raw, nil)
		tracker.Finish(true)
		if tracker.Attribution.Reliable || tracker.Ledger.LogicalCumulative != (Counts{}) {
			t.Fatal(tracker)
		}
	}
	tracker := Begin(CodexUsageLedger{ThreadID: "t", Anchored: true, LogicalCumulative: Counts{TotalTokens: math.MaxInt64}}, "r", "turn")
	tracker.Observe(Counts{TotalTokens: 1}, nil)
	if tracker.Attribution.Reliable || tracker.Ledger.LogicalCumulative.TotalTokens != math.MaxInt64 {
		t.Fatal(tracker)
	}
}

func TestMixedCounterResetDoesNotStrandThread(t *testing.T) {
	previous := Counts{InputTokens: 100, CachedInputTokens: 80, TotalTokens: 100}
	tracker := Begin(CodexUsageLedger{ThreadID: "t", AccountingEpoch: 1, Anchored: true, PreviousRawCumulative: previous, LogicalCumulative: previous, LastTurnID: "old"}, "reset", "new")
	// The new ledger can have more output while other counters have regressed.
	tracker.Observe(Counts{InputTokens: 10, OutputTokens: 150, TotalTokens: 160}, nil)
	tracker.Finish(true)
	if tracker.Attribution.Reliable || tracker.Ledger.AccountingEpoch != 2 {
		t.Fatal(tracker)
	}
	next := Begin(tracker.Ledger, "next", "next")
	next.Observe(Counts{InputTokens: 20, OutputTokens: 160, TotalTokens: 180}, nil)
	next.Finish(true)
	delta, _ := Subtract(next.Attribution.LogicalEnd, next.Attribution.LogicalStart)
	if !next.Attribution.Reliable || delta.TotalTokens != 20 {
		t.Fatal(next)
	}
}
