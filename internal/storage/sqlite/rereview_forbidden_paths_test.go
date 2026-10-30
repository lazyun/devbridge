package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReReviewLegacyNullForbiddenPathsPreservesExactDenyList(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.Exec(`CREATE TABLE reviews(id TEXT,task_id TEXT,project_id TEXT,feature_area_id TEXT,executor_kind TEXT,repository_id TEXT,base_commit TEXT,focus_kind TEXT,focus_paths_json TEXT,repository_targets_json TEXT);
 CREATE TABLE review_grants(review_id TEXT,forbidden_paths_json TEXT,protocol_version INTEGER,initial_scope_kind TEXT,initial_scope_paths_json TEXT);
 CREATE TABLE review_re_review_requests(successor_review_id TEXT,previous_review_id TEXT,task_id TEXT);
 CREATE TABLE review_invalid_result_resolutions(successor_review_id TEXT,previous_review_id TEXT,task_id TEXT);
 INSERT INTO reviews VALUES('old','task','project','area','UPPER_LLM','repo','base','REPOSITORY','[]','[]'),('new','task','project','area','UPPER_LLM','repo','base','REPOSITORY','[]','[]');
 INSERT INTO review_grants VALUES('old','["private","secrets.txt"]',1,'REPOSITORY','[]'),('new','null',1,'REPOSITORY','[]');
 INSERT INTO review_re_review_requests VALUES('new','old','task');`)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := reviewForbiddenPathsJSON(ctx, db, "new", "null")
	if e != nil || raw != `["private","secrets.txt"]` {
		t.Fatalf("deny inheritance %q %v", raw, e)
	}
	var persisted string
	db.QueryRow(`SELECT forbidden_paths_json FROM review_grants WHERE review_id='new'`).Scan(&persisted)
	if persisted != "null" {
		t.Fatal("read rewrote historical grant")
	}
	for _, mutation := range []string{`UPDATE reviews SET base_commit='other' WHERE id='new'`, `UPDATE reviews SET focus_paths_json='["other"]' WHERE id='new'`, `DELETE FROM review_re_review_requests`} {
		tx, e := db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(mutation); e != nil {
			t.Fatal(e)
		}
		if _, e = reviewForbiddenPathsJSON(ctx, tx, "new", "null"); e == nil {
			t.Fatalf("mismatched lineage authorized: %s", mutation)
		}
		tx.Rollback()
	}
	if _, e = reviewForbiddenPathsJSON(ctx, db, "unrelated", "null"); e == nil {
		t.Fatal("unrelated null became empty authority")
	}
	if raw, e = reviewForbiddenPathsJSON(ctx, db, "new", "[]"); e != nil || raw != "[]" {
		t.Fatalf("canonical empty list %q %v", raw, e)
	}
}
