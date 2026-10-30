package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed schema_v45.sql
var finalSchemaV45 string

func reviewDepthSchemaObjects() []schemaObject {
	var out []schemaObject
	for _, raw := range strings.Split(finalSchemaV45, "-- OBJECT ")[1:] {
		header, stmt, _ := strings.Cut(raw, "\n")
		kind, name, _ := strings.Cut(header, " ")
		out = append(out, schemaObject{kind, name, stmt})
	}
	return out
}

// migrateReviewDepthV45 preserves historical evidence bytes and all workflow
// pointers. A pre-depth agent already in flight cannot satisfy the new output
// protocol, and an unfinished successful transition has no safe disposition.
func migrateReviewDepthV45(ctx context.Context, tx *sql.Tx) error {
	var ambiguous int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM reviews v LEFT JOIN runs r ON r.id=v.linked_run_id
 WHERE v.start_failure_code IS NULL AND (r.id IS NULL OR r.status NOT IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED'))`).Scan(&ambiguous); err != nil {
		return err
	}
	if ambiguous == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks t LEFT JOIN reviews v ON v.id=t.current_review_id LEFT JOIN runs r ON r.id=v.linked_run_id
 WHERE t.task_type='DEVELOPMENT' AND t.status='ACTIVE' AND
 ((t.phase='REVIEWING' AND (v.id IS NULL OR (v.start_failure_code IS NULL AND (r.status IS NULL OR r.status='SUCCEEDED'))))
 OR (t.phase='REVIEW_APPROVAL_PENDING' AND NOT EXISTS(SELECT 1 FROM review_approval_decisions d WHERE d.review_id=v.id AND d.task_id=t.id AND r.status='SUCCEEDED')))`).Scan(&ambiguous); err != nil {
			return err
		}
	}
	if ambiguous != 0 {
		return fmt.Errorf("schema v44 has ambiguous active/in-flight Review state; explicitly rebuild (recreate) the development database; source data and history are preserved")
	}
	// Rebuild only the two changed tables so review_depth has no implicit
	// default on future inserts. Foreign keys are disabled by ApplyMigrations;
	// legacy_alter_table preserves all cross-table reference names.
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('tasks','reviews') AND sql IS NOT NULL)`)
	if err != nil {
		return err
	}
	var retained []schemaObject
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.sql); err != nil {
			rows.Close()
			return err
		}
		retained = append(retained, object)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, object := range retained {
		if _, err := tx.ExecContext(ctx, "DROP "+strings.ToUpper(object.kind)+" "+sqlIdentifier(object.name)); err != nil {
			return err
		}
	}
	for _, table := range []string{"tasks", "reviews"} {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+sqlIdentifier(table)+" RENAME TO "+sqlIdentifier("_source44_"+table)); err != nil {
			return err
		}
	}
	objects := reviewDepthSchemaObjects()
	for _, object := range objects {
		if object.kind == "table" && (object.name == "tasks" || object.name == "reviews") {
			if _, err := tx.ExecContext(ctx, object.sql); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"tasks", "reviews"} {
		columns, err := schemaColumns(ctx, tx, "_source44_"+table)
		if err != nil {
			return err
		}
		for i := range columns {
			columns[i] = sqlIdentifier(columns[i])
		}
		shared := strings.Join(columns, ",")
		added, values := "required_review_depth", `CASE WHEN task_type='DEVELOPMENT' AND status='ACTIVE' AND phase='REVIEW_REQUIRED' THEN 'FAST' WHEN task_type='DEVELOPMENT' AND status='ACTIVE' AND phase='REVIEWING' THEN 'DEEP' ELSE NULL END`
		if table == "reviews" {
			added, values = "review_depth,review_disposition", `'DEEP',CASE WHEN EXISTS(SELECT 1 FROM review_approval_decisions d WHERE d.review_id=_source44_reviews.id) THEN 'APPROVAL_READY' ELSE NULL END`
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+sqlIdentifier(table)+"("+shared+","+added+") SELECT "+shared+","+values+" FROM "+sqlIdentifier("_source44_"+table)); err != nil {
			return err
		}
	}
	for _, table := range []string{"tasks", "reviews"} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE "+sqlIdentifier("_source44_"+table)); err != nil {
			return err
		}
	}
	for _, object := range retained {
		if _, err := tx.ExecContext(ctx, object.sql); err != nil {
			return err
		}
	}
	for _, object := range objects {
		if object.kind == "trigger" && (object.name == "review_depth_immutable" || object.name == "review_disposition_immutable" || object.name == "review_approval_disposition_guard" || object.name == "task_terminal_review_depth_clear" || object.name == "task_initial_review_depth_guard" || object.name == "review_minimum_depth_guard") {
			if _, err := tx.ExecContext(ctx, object.sql); err != nil {
				return err
			}
		}
	}
	return nil
}
