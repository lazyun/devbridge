package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestRuntimeEpochPersistsAcrossReopenAndChangesAuthority(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	first := db.RuntimeEpoch()
	if first == "" {
		t.Fatal("first runtime epoch is empty")
	}
	assertRuntimeAuthority(t, db, first, 1)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = openTestDB(t, root)
	t.Cleanup(func() { _ = db.Close() })
	second := db.RuntimeEpoch()
	if second == "" || second == first {
		t.Fatalf("runtime epochs first=%q second=%q", first, second)
	}
	assertRuntimeAuthority(t, db, second, 2)
}

func TestRuntimeEpochWriteFenceRejectsStaleContextAndAuthority(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { _ = db.Close() })
	store := NewProjectStore(db)
	now := time.Date(2026, 9, 7, 1, 2, 3, 0, time.UTC)

	staleContext := runtimeepoch.WithFence(ctx, "runtime_stale_callback", "")
	err := store.Create(staleContext, project.Project{ID: "project-stale-context", Name: "stale", CreatedAt: now, UpdatedAt: now})
	if !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale callback error=%v, want %v", err, runtimeepoch.ErrFenced)
	}
	assertProjectAbsent(t, db, "project-stale-context")

	successor := "runtime_test_successor"
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO runtime_epochs(id,created_at) VALUES(?,?)`, successor, formatTime(now.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE runtime_authority SET epoch_id=? WHERE singleton=1`, successor); err != nil {
		t.Fatal(err)
	}
	err = store.Create(ctx, project.Project{ID: "project-stale-db", Name: "stale DB", CreatedAt: now, UpdatedAt: now})
	if !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale DB authority error=%v, want %v", err, runtimeepoch.ErrFenced)
	}
	assertProjectAbsent(t, db, "project-stale-db")

	if _, err := db.SQL().ExecContext(ctx, `UPDATE runtime_authority SET epoch_id=? WHERE singleton=1`, db.RuntimeEpoch()); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, project.Project{ID: "project-current", Name: "current", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("current runtime write: %v", err)
	}
}

func TestRunAndClaimPersistAllocatingRuntimeEpoch(t *testing.T) {
	f := newAgentStorageFixture(t, "runtime-epoch")
	created, err := prepareAgentOperation(t, f, agentconversation.Execution, "runtime-epoch").make(context.Background(), "run-runtime-epoch")
	if err != nil {
		t.Fatal(err)
	}
	if created.RuntimeEpoch != f.db.RuntimeEpoch() {
		t.Fatalf("Run runtime epoch=%q, want %q", created.RuntimeEpoch, f.db.RuntimeEpoch())
	}
	claim, err := NewAgentConversationStore(f.db).GetClaim(context.Background(), f.area.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claim.RunID != created.ID || claim.RuntimeEpoch != created.RuntimeEpoch || claim.Kind != agentconversation.Execution {
		t.Fatalf("claim=%+v Run=%+v", claim, created)
	}
}

func assertRuntimeAuthority(t *testing.T, db *DB, want string, wantHistory int) {
	t.Helper()
	var got string
	if err := db.SQL().QueryRow(`SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("runtime authority=%q, want %q", got, want)
	}
	var history int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM runtime_epochs`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != wantHistory {
		t.Fatalf("runtime epoch history=%d, want %d", history, wantHistory)
	}
}

func assertProjectAbsent(t *testing.T, db *DB, id string) {
	t.Helper()
	var count int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM projects WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("project %q was written through runtime fence", id)
	}
}
