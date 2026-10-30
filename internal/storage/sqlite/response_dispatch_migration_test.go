package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestResponseDispatchV80MigrationRollbackRepeatAndHistory(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "history.db")+"?_pragma=foreign_keys(1)")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var previous []Migration
	for _, m := range DefaultMigrations() {
		if m.Version < 80 {
			previous = append(previous, m)
		}
	}
	if e = ApplyMigrations(ctx, db, previous); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('historical','unchanged','2026-01-01','2026-01-01')`); e != nil {
		t.Fatal(e)
	}
	history := func() [32]byte {
		var b string
		if e := db.QueryRow(`SELECT json_array(id,name,created_at,updated_at) FROM projects WHERE id='historical'`).Scan(&b); e != nil {
			t.Fatal(e)
		}
		return sha256.Sum256([]byte(b))
	}
	before := history()
	broken := append(append([]Migration{}, previous...), Migration{Version: 80, SQL: responseDispatchV80SQL + `INSERT INTO no_such_table VALUES(1);`})
	if e = ApplyMigrations(ctx, db, broken); e == nil {
		t.Fatal("fault migration succeeded")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='runtime_response_dispatch_attempts'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial migration survived rollback", e)
	}
	for i := 0; i < 2; i++ {
		if e = ApplyMigrations(ctx, db, DefaultMigrations()); e != nil {
			t.Fatal(e)
		}
		if history() != before {
			t.Fatal("historical bytes changed")
		}
	}
	for _, table := range []string{"runtime_user_decisions", "app_server_run_bindings", "app_server_reattachments", "runtime_response_dispatch_attempts", "runtime_response_evidence"} {
		if e = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); e != nil || n != 0 {
			t.Fatal("migration fabricated history", table, e)
		}
	}
	if _, e = db.Exec(`INSERT INTO runtime_response_evidence(id,run_id,runtime_request_id,connection_id,backend_request_id,kind,evidence_json,observed_at) VALUES('e','absent','q','c','1','EXACT_PENDING_REPLAY','{}','now')`); e == nil {
		t.Fatal("FK bypass")
	}
	rows, e := db.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("history FK corruption")
	}
}
