package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/codexusage"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

// Missing canonical member observations can be materialized from independent
// recovery facts. Existing terminal observations are never rewritten.
func materializeRecoveredMembersTx(ctx context.Context, tx *sql.Tx, source run.Run, facts rs.FactualObservation) error {
	if source.RuntimeWorkset == nil || len(facts.After) != len(source.RuntimeWorkset.Members) {
		return rs.ErrIneligible
	}
	observations := map[string]rs.WorktreeObservation{}
	for _, o := range facts.After {
		observations[o.RepositoryID] = o
	}
	for _, m := range source.RuntimeWorkset.Members {
		o, ok := observations[m.RepositoryID]
		if !ok || o.MemberID != m.MemberID || o.WorktreeID != m.WorktreeID || o.RootIdentity != m.RuntimeRootIdentity || o.State != "OBSERVED" || o.HEAD != m.StartHEAD {
			return rs.ErrIneligible
		}
		state := "OBSERVED"
		if o.Snapshot == m.StartSnapshot {
			state = "NO_CHANGE"
		}
		if source.Purpose == run.PurposeVerification && state != "NO_CHANGE" {
			return rs.ErrIneligible
		}
		paths, _ := json.Marshal(o.ChangedPaths)
		var oldState, head, snapshot, root string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(observation_state,''),COALESCE(end_head,''),COALESCE(end_snapshot,''),COALESCE(end_root_identity,'') FROM run_repository_bindings WHERE run_id=? AND repository_id=? AND member_id=? AND worktree_id=? AND runtime_root_identity=?`, source.ID, m.RepositoryID, m.MemberID, m.WorktreeID, m.RuntimeRootIdentity).Scan(&oldState, &head, &snapshot, &root)
		if err != nil {
			return err
		}
		if oldState != "" && oldState != "PENDING" {
			if (oldState != "OBSERVED" && oldState != "NO_CHANGE") || head != o.HEAD || snapshot != o.Snapshot || root != o.RootIdentity {
				return rs.ErrIneligible
			}
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE run_repository_bindings SET end_head=?,end_snapshot=?,end_root_identity=?,changed_paths_json=?,diff_digest=?,observation_state=?,observed_at=? WHERE run_id=? AND repository_id=? AND (observation_state IS NULL OR observation_state='PENDING')`, o.HEAD, o.Snapshot, o.RootIdentity, string(paths), o.DiffDigest, state, formatTime(facts.ObservedAt), source.ID, m.RepositoryID); err != nil {
			return err
		}
		if m.MemberID == source.RuntimeWorkset.PrimaryMemberID {
			if _, err = tx.ExecContext(ctx, `UPDATE runs SET end_snapshot=COALESCE(end_snapshot,?) WHERE id=?`, o.Snapshot, source.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// Recovery usage is attributed exclusively to its own new Turn. Missing or
// uncertain telemetry remains UNAVAILABLE instead of borrowing source totals.
func finalizeRecoveryUsageTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	child, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, id))
	if err != nil {
		return err
	}
	if child.TokenUsage.Status != run.TokenUsagePending {
		return nil
	}
	child.TokenUsage.Status = run.TokenUsageUnavailable
	child.UpdatedAt = now
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT attribution_json FROM run_usage_attributions WHERE run_id=? AND thread_id=? AND turn_id=?`, id, child.ThreadID, child.TurnID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		var a codexusage.RunUsageAttribution
		if json.Unmarshal(raw, &a) != nil {
			return rs.ErrConflict
		}
		child.TokenUsage.ModelContextWindow = a.ModelContextWindow
		if a.RunID == id && a.ThreadID == child.ThreadID && a.TurnID == child.TurnID && a.Complete && a.Observed && a.Reliable && codexusage.Valid(a.LogicalStart) && codexusage.Valid(a.LogicalEnd) {
			start, end := run.TokenCounts(a.LogicalStart), run.TokenCounts(a.LogicalEnd)
			delta := run.TokenCounts{InputTokens: end.InputTokens - start.InputTokens, CachedInputTokens: end.CachedInputTokens - start.CachedInputTokens, CacheWriteInputTokens: end.CacheWriteInputTokens - start.CacheWriteInputTokens, OutputTokens: end.OutputTokens - start.OutputTokens, ReasoningOutputTokens: end.ReasoningOutputTokens - start.ReasoningOutputTokens, TotalTokens: end.TotalTokens - start.TotalTokens}
			if codexusage.Valid(codexusage.Counts(delta)) {
				child.TokenUsage.Status = run.TokenUsageAvailable
				child.TokenUsage.StartCumulative = start
				child.TokenUsage.EndCumulative = &end
				child.TokenUsage.Usage = &delta
			}
		}
	}
	return updateRunTokenUsage(ctx, tx, child)
}
