package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRetirementV36ForwardMigrationPreservesLifecycleHistory(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v36.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:36]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	if _, err = raw.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','History',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"DISCOVERING", "ACTIVE", "RETIRED"} {
		initial := state
		if initial == "RETIRED" {
			initial = "ACTIVE"
		}
		if _, err = raw.Exec(`INSERT INTO feature_areas(id,project_id,title,problem_statement,status,created_at,updated_at) VALUES(?,'p',?,'Original',?,?,?)`, state, state, initial, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if state == "RETIRED" {
			if _, err = raw.Exec(`UPDATE feature_areas SET status='RETIRED' WHERE id='RETIRED'`); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot := func() []string {
		rows, err := raw.Query(`SELECT id||'|'||status||'|'||created_at||'|'||updated_at FROM feature_areas ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var values []string
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
		return values
	}
	before := snapshot()
	for i := 0; i < 2; i++ {
		if err = ApplyMigrations(ctx, raw, historicalMigrations()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("migration reinterpreted lifecycle history")
	}
	var count int
	if err = raw.QueryRow(`SELECT count(*) FROM feature_area_retirements`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invented historical intent: %d %v", count, err)
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if _, err = raw.Exec(`UPDATE feature_areas SET status='RETIRING' WHERE id='ACTIVE'`); err == nil {
		t.Fatal("RETIRING without intent")
	}
}
