package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentconversation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestRecoveryStatusV81MigrationRollbackRepeatHistory(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "history.db")+"?_pragma=foreign_keys(1)")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var previous []Migration
	for _, m := range DefaultMigrations() {
		if m.Version < 81 {
			previous = append(previous, m)
		}
	}
	if e = ApplyMigrations(ctx, db, previous); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('history','untouched','2026-01-01','2026-01-01')`); e != nil {
		t.Fatal(e)
	}
	var original string
	if e = db.QueryRow(`SELECT json_array(id,name,created_at,updated_at) FROM projects WHERE id='history'`).Scan(&original); e != nil {
		t.Fatal(e)
	}
	broken := append(append([]Migration{}, previous...), Migration{Version: 81, ForeignKeysOff: true, Apply: func(c context.Context, tx *sql.Tx) error {
		if e := migrateRecoveryStatusV81(c, tx); e != nil {
			return e
		}
		return errors.New("after rebuild fault")
	}})
	if e = ApplyMigrations(ctx, db, broken); e == nil {
		t.Fatal("fault unexpectedly committed")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='recovery_status_records'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("rollback leaked recovery table", e)
	}
	for i := 0; i < 2; i++ {
		if e = ApplyMigrations(ctx, db, DefaultMigrations()); e != nil {
			t.Fatal(e)
		}
		var current string
		if e = db.QueryRow(`SELECT json_array(id,name,created_at,updated_at) FROM projects WHERE id='history'`).Scan(&current); e != nil || current != original {
			t.Fatal("history changed", e)
		}
	}
	if e = db.QueryRow(`SELECT count(*) FROM recovery_status_records`).Scan(&n); e != nil || n != 0 {
		t.Fatal("migration fabricated probe", e)
	}
	rows, e := db.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}

// Populate a v80 database with real store-created operation history, including
// a bound Run/conversation, then compare every historical table byte-for-byte.
func TestRecoveryStatusV81PreservesPopulatedOperationHistory(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "migration-status-history")
	beginRecoveryRun(t, f, agentconversation.Execution, "migration-status-history", true)
	root := t.TempDir()
	raw, e := sql.Open("sqlite", "file:"+filepath.Join(root, "v80.db")+"?_pragma=foreign_keys(1)")
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	var previous []Migration
	for _, m := range DefaultMigrations() {
		if m.Version < 81 {
			previous = append(previous, m)
		}
	}
	if e = ApplyMigrations(ctx, raw, previous); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`ATTACH DATABASE ? AS source_history`, filepath.Join(f.root, "devbridge.db")); e != nil {
		t.Fatal(e)
	}
	rows, e := raw.Query(`SELECT name,sql FROM sqlite_master WHERE type='trigger' ORDER BY name`)
	if e != nil {
		t.Fatal(e)
	}
	type trigger struct{ name, sql string }
	var triggers []trigger
	for rows.Next() {
		var d trigger
		if e = rows.Scan(&d.name, &d.sql); e != nil {
			t.Fatal(e)
		}
		triggers = append(triggers, d)
	}
	rows.Close()
	rows, e = raw.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name<>'schema_migrations' ORDER BY name`)
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, name)
	}
	rows.Close()
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	if _, e = raw.Exec(`PRAGMA foreign_keys=OFF`); e != nil {
		t.Fatal(e)
	}
	tx, e := raw.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range triggers {
		if _, e = tx.Exec(`DROP TRIGGER ` + quote(d.name)); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range tables {
		if _, e = tx.Exec(`DELETE FROM ` + quote(table)); e != nil {
			t.Fatal(e)
		}
	}
	for _, table := range tables {
		columns, columnErr := tx.Query(`PRAGMA table_info(` + quote(table) + `)`)
		if columnErr != nil {
			t.Fatal(columnErr)
		}
		var names []string
		for columns.Next() {
			var cid, notNull, primaryKey int
			var name, typ string
			var defaultValue any
			if columnErr = columns.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); columnErr != nil {
				columns.Close()
				t.Fatal(columnErr)
			}
			names = append(names, quote(name))
		}
		if columnErr = columns.Err(); columnErr != nil {
			columns.Close()
			t.Fatal(columnErr)
		}
		if columnErr = columns.Close(); columnErr != nil {
			t.Fatal(columnErr)
		}
		columnList := strings.Join(names, ",")
		if _, e = tx.Exec(`INSERT INTO ` + quote(table) + `(` + columnList + `) SELECT ` + columnList + ` FROM source_history.` + quote(table)); e != nil {
			t.Fatalf("copy historical %s: %v", table, e)
		}
	}
	for _, d := range triggers {
		if _, e = tx.Exec(d.sql); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`PRAGMA foreign_keys=ON`); e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`DETACH DATABASE source_history`); e != nil {
		t.Fatal(e)
	}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, table := range tables {
			rows, e := raw.Query(`SELECT * FROM ` + quote(table))
			if e != nil {
				t.Fatal(e)
			}
			cols, e := rows.Columns()
			if e != nil {
				t.Fatal(e)
			}
			encoded := []string{}
			for rows.Next() {
				values := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range values {
					ptrs[i] = &values[i]
				}
				if e = rows.Scan(ptrs...); e != nil {
					t.Fatal(e)
				}
				b, _ := json.Marshal(values)
				encoded = append(encoded, string(b))
			}
			rows.Close()
			sort.Strings(encoded)
			b, _ := json.Marshal(encoded)
			out[table] = rs.Hash(b)
		}
		return out
	}
	before := snapshot()
	var throughV81 []Migration
	for _, migration := range DefaultMigrations() {
		if migration.Version <= 81 {
			throughV81 = append(throughV81, migration)
		}
	}
	for i := 0; i < 2; i++ {
		if e = ApplyMigrations(ctx, raw, throughV81); e != nil {
			t.Fatal(e)
		}
		after := snapshot()
		if !reflect.DeepEqual(before, after) {
			for table, hash := range before {
				if after[table] != hash {
					t.Errorf("historical table changed: %s", table)
				}
			}
			t.FailNow()
		}
	}
	rows, e = raw.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("populated history FK violation")
	}
	// v82 may add owner receipts but must not rewrite the historical operation
	// identity.  The populated source above includes a FeatureArea conversation
	// and claim created through the real stores.
	var legacyBefore string
	if e = raw.QueryRow(`SELECT json_group_array(json_array(id,project_id,feature_area_id,backend,COALESCE(backend_thread_id,''),COALESCE(current_boundary_turn_id,''),continuation_safe,COALESCE(predecessor_id,''),COALESCE(rotation_reason,''),created_at,ended_at)) FROM (SELECT * FROM agent_conversations ORDER BY id)`).Scan(&legacyBefore); e != nil {
		t.Fatal(e)
	}
	if e = ApplyMigrations(ctx, raw, DefaultMigrations()); e != nil {
		t.Fatal(e)
	}
	var legacyAfter string
	if e = raw.QueryRow(`SELECT json_group_array(json_array(id,project_id,feature_area_id,backend,COALESCE(backend_thread_id,''),COALESCE(current_boundary_turn_id,''),continuation_safe,COALESCE(predecessor_id,''),COALESCE(rotation_reason,''),created_at,ended_at)) FROM (SELECT * FROM agent_conversations ORDER BY id)`).Scan(&legacyAfter); e != nil || legacyAfter != legacyBefore {
		t.Fatalf("v82 rewrote legacy conversation identity: %v", e)
	}
	var badOwners, canonicalFacts int
	if e = raw.QueryRow(`SELECT count(*) FROM agent_conversations WHERE feature_area_id IS NOT NULL AND (owner_kind<>'FEATURE_AREA' OR owner_id<>feature_area_id)`).Scan(&badOwners); e != nil || badOwners != 0 {
		t.Fatalf("legacy owners=%d err=%v", badOwners, e)
	}
	if e = raw.QueryRow(`SELECT count(*) FROM context_probes WHERE canonical_target_receipt_json IS NOT NULL OR runtime_workset_json IS NOT NULL`).Scan(&canonicalFacts); e != nil || canonicalFacts != 0 {
		t.Fatalf("v82 backfilled canonical inspection facts=%d err=%v", canonicalFacts, e)
	}
	rows, e = raw.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v82 populated history FK violation")
	}
}
