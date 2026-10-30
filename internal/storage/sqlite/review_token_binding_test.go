package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"
	"testing"
)

func TestReviewWorkTokenBindsExactLinkedRun(t *testing.T) {
	f, r, _ := unifiedOwnerFixture(t, agentconversation.Review)
	ctx := context.Background()
	// Seed an unrelated newer Review in the same FeatureArea. It cannot
	// replace this Review's exact linked Run.
	// Release only the fixture claim to admit a separate legitimate owner.
	if _, err := f.db.SQL().ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE feature_area_id=?`, f.area.ID); err != nil {
		t.Fatal(err)
	}
	op := prepareAgentOperation(t, f, agentconversation.Review, "unrelated")
	other, err := op.make(ctx, "unrelated-newer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.SQL().ExecContext(ctx, `UPDATE runs SET created_at='2099-01-01T00:00:00Z' WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.SQL().ExecContext(ctx, `UPDATE run_token_usage SET status='UNAVAILABLE' WHERE run_id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.TokenUsageStatus{run.TokenUsagePending, run.TokenUsageAvailable, run.TokenUsageUnavailable} {
		if _, err := f.db.SQL().ExecContext(ctx, `UPDATE run_token_usage SET status=? WHERE run_id=?`, status, r.ID); err != nil {
			t.Fatal(err)
		}
		value, err := NewTaskStore(f.db).Get(ctx, r.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if value.Work.RunID != r.ID || value.Work.TokenAvailability != string(status) {
			t.Fatalf("exact binding: %+v", value.Work)
		}
	}
	if _, err := f.db.SQL().ExecContext(ctx, `DELETE FROM run_token_usage WHERE run_id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	value, err := NewTaskStore(f.db).Get(ctx, r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Work.TokenAvailability != "UNAVAILABLE" {
		t.Fatal(value.Work)
	}
}
