package codex

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func (c *Client) installUsageLedger(ctx context.Context, threadID string, fresh bool, legacy *TokenCounts) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.threadUsage == nil {
		c.threadUsage = make(map[string]threadUsageState)
	}
	// A warm resume must retain raw and logical coordinates independently.
	if _, exists := c.threadUsage[threadID]; exists {
		return
	}
	state := threadUsageState{ledger: codexusage.CodexUsageLedger{ThreadID: threadID, AccountingEpoch: 1, Anchored: fresh}}
	if c.usageStore != nil {
		ledger, exists, err := c.usageStore.Load(ctx, threadID)
		if err != nil {
			state.persistFailed = true
			state.ledger.Anchored = false
			c.rpc.diagnostic(err)
		} else if exists {
			state.ledger = ledger
			c.threadUsage[threadID] = state
			return
		}
	}
	// Only a caller's exact legacy conversation head may seed migration. Never
	// seed from workspace totals or infer zero from a missing observation.
	if !fresh && !state.persistFailed && legacy != nil && codexusage.Valid(*legacy) {
		state.ledger.PreviousRawCumulative = *legacy
		state.ledger.LogicalCumulative = *legacy
		state.ledger.Anchored = true
	}
	// A newly-created Thread is not durably bound to its Run until the
	// coordinator receives the thread/start response. Its first durable write
	// is the pre-dispatch marker in beginTurnStart, after that binding exists.
	if fence, ok := runtimeepoch.FromContext(ctx); ok && fence.RunID != "" {
		c.threadUsage[state.ledger.ThreadID] = state
		return
	}
	c.saveUsageLocked(ctx, &state, nil)
}

func (c *Client) saveUsageLocked(ctx context.Context, state *threadUsageState, attribution *codexusage.RunUsageAttribution) {
	if c.threadUsage == nil {
		c.threadUsage = make(map[string]threadUsageState)
	}
	if c.usageStore != nil && !state.persistFailed {
		if ctx == nil {
			ctx = context.Background()
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		next := state.ledger
		next.Revision++
		err := c.usageStore.Save(ctx, state.ledger.Revision, next, attribution)
		cancel()
		if err != nil {
			state.persistFailed = true
			c.rpc.diagnostic(err)
		} else {
			state.ledger = next
		}
	}
	c.threadUsage[state.ledger.ThreadID] = *state
}

func (c *Client) persistTurnUsageLocked(state *turnState) {
	tracker := state.usage
	current := c.threadUsage[tracker.Ledger.ThreadID]
	tracker.Ledger.Revision = current.ledger.Revision
	updated := threadUsageState{ledger: tracker.Ledger, persistFailed: current.persistFailed}
	if updated.persistFailed {
		tracker.Unavailable("PERSISTENCE_FAILED")
	}
	c.saveUsageLocked(state.usageContext, &updated, &tracker.Attribution)
	tracker.Ledger = updated.ledger
	if updated.persistFailed {
		tracker.Unavailable("PERSISTENCE_FAILED")
	}
}

// Preserve presence: a missing/null total (or counter) is not observed zero.
// cacheWriteInputTokens is an optional extension of the pinned breakdown.
func decodeRawCumulative(raw json.RawMessage) (TokenCounts, error) {
	var value struct {
		InputTokens           *int64
		CachedInputTokens     *int64
		CacheWriteInputTokens int64
		OutputTokens          *int64
		ReasoningOutputTokens *int64
		TotalTokens           *int64
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return TokenCounts{}, err
	}
	if value.InputTokens == nil || value.CachedInputTokens == nil || value.OutputTokens == nil || value.ReasoningOutputTokens == nil || value.TotalTokens == nil {
		return TokenCounts{}, errors.New("cumulative usage is missing required counters")
	}
	return TokenCounts{InputTokens: *value.InputTokens, CachedInputTokens: *value.CachedInputTokens, CacheWriteInputTokens: value.CacheWriteInputTokens, OutputTokens: *value.OutputTokens, ReasoningOutputTokens: *value.ReasoningOutputTokens, TotalTokens: *value.TotalTokens}, nil
}
