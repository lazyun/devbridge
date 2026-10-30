// Package codexusage owns Bridge's accounting coordinates. Codex totals are
// cumulative only within a ledger epoch; conversation identity is independent.
package codexusage

import (
	"context"
	"math"
)

type Counts struct {
	InputTokens           int64
	CachedInputTokens     int64
	CacheWriteInputTokens int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	TotalTokens           int64
}

type CodexUsageLedger struct {
	ThreadID              string
	AccountingEpoch       int64
	PreviousRawCumulative Counts
	LogicalCumulative     Counts
	Anchored              bool
	LastTurnID            string
	InFlightTurnID        string
	Revision              int64
}

type RunUsageAttribution struct {
	RunID              string
	ThreadID           string
	TurnID             string
	StartEpoch         int64
	EndEpoch           int64
	LogicalStart       Counts
	LogicalEnd         Counts
	Observed           bool
	Reliable           bool
	Complete           bool
	Reason             string
	ModelContextWindow *int64
}

// Save atomically advances the ledger revision and its exact Turn attribution.
// A stale writer must fail; it must never replace newer accounting evidence.
type Store interface {
	Load(context.Context, string) (CodexUsageLedger, bool, error)
	Save(context.Context, int64, CodexUsageLedger, *RunUsageAttribution) error
}

type Tracker struct {
	Ledger       CodexUsageLedger
	Attribution  RunUsageAttribution
	seen         map[Counts]struct{}
	uncertainEnd bool
}

func Begin(ledger CodexUsageLedger, runID, turnID string) *Tracker {
	if ledger.AccountingEpoch == 0 {
		ledger.AccountingEpoch = 1
	}
	if ledger.InFlightTurnID != "" {
		ledger.Anchored = false // A process exit cannot prove the previous end.
	}
	a := RunUsageAttribution{RunID: runID, ThreadID: ledger.ThreadID, TurnID: turnID,
		StartEpoch: ledger.AccountingEpoch, EndEpoch: ledger.AccountingEpoch,
		LogicalStart: ledger.LogicalCumulative, LogicalEnd: ledger.LogicalCumulative,
		Reliable: ledger.Anchored}
	if !a.Reliable {
		a.Reason = "BASELINE_MISSING"
	}
	ledger.InFlightTurnID = turnID
	return &Tracker{Ledger: ledger, Attribution: a, seen: make(map[Counts]struct{})}
}

func (t *Tracker) Unavailable(reason string) {
	t.Attribution.Reliable = false
	t.Attribution.Reason = reason
}

func (t *Tracker) InvalidTelemetry() {
	t.Unavailable("INVALID_TELEMETRY")
	t.uncertainEnd = true
}

// Observe is called only for the exact dispatched Turn, in transport order.
// last is deliberately absent: an append sample is not a Turn total.
func (t *Tracker) Observe(raw Counts, window *int64) {
	a, l := &t.Attribution, &t.Ledger
	if a.Complete {
		return
	}
	if !Valid(raw) || (window != nil && *window < 0) {
		t.InvalidTelemetry()
		return
	}
	if _, replay := t.seen[raw]; replay {
		return
	}
	first := !a.Observed
	a.Observed = true
	var delta Counts
	if !l.Anchored {
		// Re-establish the raw coordinate without claiming an unknown prefix.
		l.PreviousRawCumulative, l.Anchored = raw, true
	} else if d, ok := Subtract(raw, l.PreviousRawCumulative); ok {
		delta = d
	} else if first && l.LastTurnID != a.TurnID {
		// Old-Turn telemetry has already been filtered. This is a new epoch,
		// but the reset's precise position relative to the Run is unknown.
		// Preserve a logical coordinate and recover the NEXT Run's baseline.
		if l.AccountingEpoch == math.MaxInt64 {
			t.Unavailable("COUNTER_OVERFLOW")
			return
		}
		l.AccountingEpoch++
		delta = raw
		t.Unavailable("EPOCH_BOUNDARY_UNCERTAIN")
	} else {
		// No ordering marker distinguishes an unseen lower sample from a
		// mid-Turn reset. Do not open an epoch or overwrite the high-water mark.
		t.Unavailable("AMBIGUOUS_REGRESSION")
		t.uncertainEnd = true
		return
	}
	logical, ok := Add(l.LogicalCumulative, delta)
	if !ok {
		t.Unavailable("COUNTER_OVERFLOW")
		return
	}
	t.uncertainEnd = false
	l.PreviousRawCumulative, l.LogicalCumulative, l.LastTurnID = raw, logical, a.TurnID
	a.LogicalEnd, a.EndEpoch = logical, l.AccountingEpoch
	if window != nil {
		v := *window
		a.ModelContextWindow = &v
	}
	// A bounded replay cache is enough for exact common replays. Evicted
	// lower snapshots degrade conservatively instead of creating an epoch.
	if len(t.seen) < 256 {
		t.seen[raw] = struct{}{}
	}
}

func (t *Tracker) Finish(complete bool) {
	t.Attribution.Complete = complete
	if !complete || !t.Attribution.Observed {
		t.Unavailable("INCOMPLETE_TELEMETRY")
		t.Ledger.Anchored = false
	}
	if t.uncertainEnd {
		t.Ledger.Anchored = false
	}
	if complete {
		t.Ledger.InFlightTurnID = ""
	}
}

func Valid(v Counts) bool {
	return v.InputTokens >= 0 && v.CachedInputTokens >= 0 && v.CacheWriteInputTokens >= 0 && v.OutputTokens >= 0 && v.ReasoningOutputTokens >= 0 && v.TotalTokens >= 0
}

func AtLeast(v, b Counts) bool {
	return v.InputTokens >= b.InputTokens && v.CachedInputTokens >= b.CachedInputTokens && v.CacheWriteInputTokens >= b.CacheWriteInputTokens && v.OutputTokens >= b.OutputTokens && v.ReasoningOutputTokens >= b.ReasoningOutputTokens && v.TotalTokens >= b.TotalTokens
}

func Subtract(v, b Counts) (Counts, bool) {
	if !Valid(v) || !Valid(b) || !AtLeast(v, b) {
		return Counts{}, false
	}
	return Counts{v.InputTokens - b.InputTokens, v.CachedInputTokens - b.CachedInputTokens, v.CacheWriteInputTokens - b.CacheWriteInputTokens, v.OutputTokens - b.OutputTokens, v.ReasoningOutputTokens - b.ReasoningOutputTokens, v.TotalTokens - b.TotalTokens}, true
}

func Add(a, b Counts) (Counts, bool) {
	if !Valid(a) || !Valid(b) {
		return Counts{}, false
	}
	v := Counts{a.InputTokens + b.InputTokens, a.CachedInputTokens + b.CachedInputTokens, a.CacheWriteInputTokens + b.CacheWriteInputTokens, a.OutputTokens + b.OutputTokens, a.ReasoningOutputTokens + b.ReasoningOutputTokens, a.TotalTokens + b.TotalTokens}
	return v, Valid(v)
}
