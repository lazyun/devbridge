package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/project"
)

// Verifies the v135 migration (including the append-only history triggers)
// applies cleanly on the v134 schema and the authorization store round-trips
// through the migrated tables with a real writer lock and runtime epoch.
func TestProjectAuthorizationMigrationV135(t *testing.T) {
	ctx := context.Background()
	db := openSelectionMigrationDB(t, migrationsThroughVersion(134))
	defer db.Close()
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO projects(id,name,created_at,updated_at) VALUES ('p1','Project One','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.sql, DefaultMigrations()); err != nil {
		t.Fatalf("apply v135: %v", err)
	}
	store := NewAuthorizationStore(db)
	if _, err := store.Get(ctx, "p1"); err != project.ErrAuthorizationNotFound {
		t.Fatalf("default state must be not-found, got %v", err)
	}
	first, err := store.Apply(ctx, project.ExecutionAuthorization{ProjectID: "p1", TrustedExecution: true, NetworkAccess: false, HeadRevision: 1, AuthorizedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !first.TrustedExecution || first.NetworkAccess || first.HeadRevision != 1 {
		t.Fatalf("unexpected applied value %+v", first)
	}
	revoked, err := store.Apply(ctx, project.ExecutionAuthorization{ProjectID: "p1", TrustedExecution: false, NetworkAccess: false, HeadRevision: 2, UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}, 1)
	if err != nil || revoked.TrustedExecution {
		t.Fatalf("revoke failed: %+v %v", revoked, err)
	}
	history, err := store.GetRevision(ctx, "p1", 1)
	if err != nil || !history.TrustedExecution {
		t.Fatalf("append-only history lost revision 1: %+v %v", history, err)
	}
	// Read back through parseTime: timestamps survive the round trip and the
	// initial AuthorizedAt is preserved by later revisions.
	current, err := store.Get(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if current.HeadRevision != 2 || current.TrustedExecution {
		t.Fatalf("head must reflect revision 2: %+v", current)
	}
	if !current.AuthorizedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("AuthorizedAt must stay frozen at the first grant: %v", current.AuthorizedAt)
	}
	if !current.UpdatedAt.Equal(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("UpdatedAt must advance: %v", current.UpdatedAt)
	}
	// History is append-only at the SQL level after the real migration.
	if _, err := db.sql.ExecContext(ctx, `UPDATE project_execution_authorization_revisions SET network_access=1 WHERE project_id='p1'`); err == nil {
		t.Fatal("history UPDATE must abort after real migration")
	}
	// Stale CAS is rejected without state change.
	if _, err := store.Apply(ctx, project.ExecutionAuthorization{ProjectID: "p1", TrustedExecution: true, NetworkAccess: true, HeadRevision: 2, UpdatedAt: time.Now().UTC()}, 1); err != project.ErrConflict {
		t.Fatalf("stale CAS must conflict, got %v", err)
	}
	// Re-applying all migrations must be a no-op.
	if _, err := db.sql.ExecContext(ctx, `UPDATE project_execution_authorizations SET authorized_at='invalid' WHERE project_id='p1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(ctx, project.ExecutionAuthorization{ProjectID: "p1", TrustedExecution: true, NetworkAccess: true, HeadRevision: 3, UpdatedAt: time.Now().UTC()}, 2); err == nil {
		t.Fatal("corrupt original authorization timestamp was silently replaced")
	}
	var revisions int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM project_execution_authorization_revisions WHERE project_id='p1'`).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("failed update wrote an authorization revision: %d %v", revisions, err)
	}
	if err := ApplyMigrations(ctx, db.sql, DefaultMigrations()); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}
}
