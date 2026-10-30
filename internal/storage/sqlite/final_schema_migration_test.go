package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalV42RejectsDisposableEntitiesWithoutChangingSource(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "source.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:41]); err != nil {
		t.Fatal(err)
	}
	// An unsupported development entity is rejected, never mapped into a native owner.
	if _, err = raw.Exec(`CREATE TABLE discarded_development_entity(id TEXT); INSERT INTO discarded_development_entity VALUES('retain-until-explicit-recreation')`); err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, DefaultMigrations()); err == nil || !strings.Contains(err.Error(), "recreat") {
		t.Fatalf("disposable source accepted: %v", err)
	}
	var version, count int
	if err = raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 41 {
		t.Fatalf("source version mutated: %d %v", version, err)
	}
	if err = raw.QueryRow(`SELECT count(*) FROM discarded_development_entity`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source data mutated: %d %v", count, err)
	}
}

func TestFinalV42OpenRejectsObsoleteSameVersionBeforeRuntimeEpoch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err = db.SQL().QueryRow(`SELECT count(*) FROM runtime_epochs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL().Exec(`DROP TRIGGER approved_execution_binding; CREATE TRIGGER approved_execution_binding BEFORE INSERT ON execution_proposals BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(ctx, root); err == nil || !strings.Contains(err.Error(), "recreat") {
		if opened != nil {
			opened.Close()
		}
		t.Fatalf("obsolete same-version schema accepted: %v", err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var after int
	if err = raw.QueryRow(`SELECT count(*) FROM runtime_epochs`).Scan(&after); err != nil || before != after {
		t.Fatalf("rejected schema allocated epoch: %d %d %v", before, after, err)
	}
}
