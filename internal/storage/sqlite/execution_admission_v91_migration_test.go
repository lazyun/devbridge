package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func migrationsThroughVersion(version int) []Migration {
	migrations := DefaultMigrations()
	for i, migration := range migrations {
		if migration.Version == version {
			return migrations[:i+1]
		}
	}
	panic("missing migration version")
}

func TestExecutionAdmissionV91ColdUpgradeAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "devbridge.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	if err := ApplyMigrations(ctx, db, migrationsThroughVersion(90)); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_proposals'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(before, "'ADMITTED'") {
		t.Fatal("v90 already contains ADMITTED")
	}
	if err := ApplyMigrations(ctx, db, migrationsThroughVersion(91)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	if err := ApplyMigrations(ctx, db, migrationsThroughVersion(91)); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_proposals'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"'ADMITTED'", "status='ADMITTED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NULL AND run_id IS NULL", "status='CONSUMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NOT NULL AND run_id IS NOT NULL"} {
		if !strings.Contains(after, required) {
			t.Fatalf("missing constraint %q", required)
		}
	}
	for _, trigger := range []string{"execution_proposal_admissions_no_update", "execution_proposal_admissions_no_delete", "execution_proposal_admissions_owner"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&count); err != nil || count != 1 {
			t.Fatalf("trigger %s count=%d err=%v", trigger, count, err)
		}
	}
	var transition string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='execution_proposals_status_transition'`).Scan(&transition); err != nil || !strings.Contains(transition, "OLD.status='ADMITTED' AND NEW.status='CONSUMED'") {
		t.Fatalf("missing admitted transition: %v %s", err, transition)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionAdmissionV91PreservesHistoricalConsumedAndConfirmed(t *testing.T) {
	ctx := context.Background()
	// The current production seed uses newer approval tables. Produce its
	// historical states on the current schema, then restore only v90 columns
	// into the actual v90 schema before exercising the migration.
	source := openTestDB(t, t.TempDir())
	defer source.Close()
	consumed := seed(t, source, "v91-consumed", true)
	confirmed := seed(t, source, "v91-confirmed", false)
	db := restoreHistoricalMigrationFixture(t, source, 90)
	path := filepath.Join(db.stateRoot, "devbridge.db")
	var version, admissions int
	var executionDDL string
	if err := db.sql.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 90 {
		t.Fatalf("historical fixture schema version=%d err=%v", version, err)
	}
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='execution_proposal_admissions'`).Scan(&admissions); err != nil || admissions != 0 {
		t.Fatalf("historical fixture already has admission table: count=%d err=%v", admissions, err)
	}
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='execution_proposals'`).Scan(&executionDDL); err != nil || strings.Contains(executionDDL, "'ADMITTED'") {
		t.Fatalf("historical fixture is not the v90 Execution contract: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, status, runID string }{{consumed.run.ExecutionProposalID, "CONSUMED", consumed.run.ID}, {confirmed.run.ExecutionProposalID, "CONFIRMED", ""}} {
		var status string
		var runID sql.NullString
		if err = raw.QueryRowContext(ctx, `SELECT status,run_id FROM execution_proposals WHERE id=?`, tc.id).Scan(&status, &runID); err != nil {
			t.Fatal(err)
		}
		if status != tc.status || runID.String != tc.runID {
			t.Fatalf("history %s status=%s run=%s", tc.id, status, runID.String)
		}
	}
	rows, err := raw.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("historical foreign-key violation")
	}
}

func TestExecutionAdmissionV91RejectsMalformedStateRunPairs(t *testing.T) {
	f := newAgentStorageFixture(t, "v91-malformed")
	ctx := context.Background()
	id := seedExecutionOwner(t, f.db, f.task, f.session, "execution-v91-malformed")
	if _, err := f.db.sql.ExecContext(ctx, `UPDATE execution_proposals SET status='ADMITTED' WHERE id=?`, id); err == nil {
		t.Fatal("ADMITTED accepted without its immutable receipt")
	}
	if _, err := f.db.sql.ExecContext(ctx, `UPDATE execution_proposals SET status='ADMITTED',run_id='invalid-run' WHERE id=?`, id); err == nil {
		t.Fatal("ADMITTED accepted a Run ID")
	}
	if _, err := f.db.sql.ExecContext(ctx, `UPDATE execution_proposals SET status='CONSUMED',consumed_at=?,run_id=NULL WHERE id=?`, formatTime(testNow), id); err == nil {
		t.Fatal("CONSUMED accepted a null Run ID")
	}
	if _, err := f.db.sql.ExecContext(ctx, `UPDATE execution_proposals SET status='REJECTED',rejected_at=?,run_id='invalid-run' WHERE id=?`, formatTime(testNow), id); err == nil {
		t.Fatal("REJECTED accepted a Run ID")
	}
	var status string
	if err := f.db.sql.QueryRowContext(ctx, `SELECT status FROM execution_proposals WHERE id=?`, id).Scan(&status); err != nil || status != "CONFIRMED" {
		t.Fatalf("malformed writes changed proposal: %s %v", status, err)
	}
}
