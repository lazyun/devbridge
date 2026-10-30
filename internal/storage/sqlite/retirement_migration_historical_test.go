// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateRetirement(ctx context.Context, tx *sql.Tx) error {
	// Preserve every trigger while rebuilding the lifecycle CHECK contract.
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name='feature_areas' AND sql IS NOT NULL) ORDER BY type,name`)
	if err != nil {
		return err
	}
	type object struct{ kind, name, ddl string }
	var objects []object
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.kind, &o.name, &o.ddl); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, `DROP `+strings.ToUpper(o.kind)+` "`+o.name+`"`); err != nil {
			return err
		}
	}
	var ddl string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='feature_areas'`).Scan(&ddl); err != nil {
		return err
	}
	body := ddl[strings.Index(ddl, "("):]
	body = strings.ReplaceAll(body, "'RETIRED'", "'RETIRED','RETIRING'")
	if _, err = tx.ExecContext(ctx, `CREATE TABLE feature_areas_v37 `+body+`; INSERT INTO feature_areas_v37 SELECT * FROM feature_areas; DROP TABLE feature_areas; ALTER TABLE feature_areas_v37 RENAME TO feature_areas;`); err != nil {
		return err
	}
	for _, o := range objects {
		if _, err = tx.ExecContext(ctx, o.ddl); err != nil {
			return fmt.Errorf("restore %s: %w", o.name, err)
		}
	}
	_, err = tx.ExecContext(ctx, retirementSchema)
	return err
}

const retirementSchema = `
CREATE TABLE feature_area_retirements (
 feature_area_id TEXT PRIMARY KEY REFERENCES feature_areas(id),
 request_id TEXT NOT NULL,
 requested_by_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 requested_at TEXT NOT NULL,
 completed_at TEXT,
 last_error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE TRIGGER retirement_identity_immutable BEFORE UPDATE OF feature_area_id,request_id,requested_by_ai_session_id,requested_at ON feature_area_retirements
BEGIN SELECT RAISE(ABORT,'retirement identity is immutable'); END;
CREATE TRIGGER retirement_no_delete BEFORE DELETE ON feature_area_retirements BEGIN SELECT RAISE(ABORT,'retirement is permanent history'); END;
DROP TRIGGER feature_areas_lifecycle_transition;
CREATE TRIGGER feature_areas_lifecycle_transition BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status<>OLD.status AND NOT (OLD.legacy_read_only=0 AND (
 (OLD.status='DISCOVERING' AND NEW.status IN ('ACTIVE','DECOMPOSED','RETIRING')) OR
 (OLD.status='ACTIVE' AND NEW.status='RETIRING') OR (OLD.status='RETIRING' AND NEW.status='RETIRED')))
BEGIN SELECT RAISE(ABORT,'invalid FeatureArea lifecycle transition'); END;
DROP TRIGGER feature_area_agent_busy_lifecycle;
CREATE TRIGGER feature_area_agent_busy_lifecycle BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status<>OLD.status AND NEW.status<>'RETIRING' AND EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_BUSY'); END;
CREATE TRIGGER retirement_intent_required BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status='RETIRING' AND NOT EXISTS(SELECT 1 FROM feature_area_retirements WHERE feature_area_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'retirement intent required'); END;
CREATE TRIGGER retirement_completion_guard BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status='RETIRED' AND (
 NOT EXISTS(SELECT 1 FROM feature_area_retirements WHERE feature_area_id=NEW.id) OR
 EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=NEW.id AND legacy_read_only=0 AND status NOT IN ('SUCCEEDED','FAILED','CANCELED')) OR
 EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=NEW.id) OR
 EXISTS(SELECT 1 FROM task_preparations WHERE feature_area_id=NEW.id AND status IN ('DRAFT','PREPARING')) OR
 EXISTS(SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id WHERE t.feature_area_id=NEW.id AND e.outcome IN ('PENDING','RUNNING') AND e.status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED')))
BEGIN SELECT RAISE(ABORT,'retirement convergence incomplete'); END;
CREATE TRIGGER retirement_task_insert BEFORE INSERT ON tasks WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_review_insert BEFORE INSERT ON reviews WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_run_insert BEFORE INSERT ON runs WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_run_start BEFORE UPDATE OF status,thread_id,turn_id ON runs
WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) AND
 ((NEW.status IN ('PREPARING','RUNNING') AND NEW.status IS NOT OLD.status) OR NEW.thread_id IS NOT OLD.thread_id OR NEW.turn_id IS NOT OLD.turn_id)
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_task_advance BEFORE UPDATE ON tasks WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) AND
 (NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.verification_spec_json IS NOT OLD.verification_spec_json OR NEW.model IS NOT OLD.model OR NEW.reasoning_effort IS NOT OLD.reasoning_effort OR NEW.confirmed_task_plan_id IS NOT OLD.confirmed_task_plan_id OR (NEW.status IN ('READY','RUNNING') AND NEW.status IS NOT OLD.status))
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_revision_insert BEFORE INSERT ON task_definition_revisions WHEN EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_preparation_insert BEFORE INSERT ON task_preparations WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_preparation_start BEFORE UPDATE OF status ON task_preparations WHEN NEW.status IN ('PREPARING','READY') AND NEW.status<>OLD.status AND EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_execution_insert BEFORE INSERT ON execution_proposals WHEN EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_execution_advance BEFORE UPDATE ON execution_proposals WHEN (NEW.status IN ('CONFIRMED','CONSUMED') AND NEW.status<>OLD.status OR NEW.verification_run_id IS NOT OLD.verification_run_id) AND EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_plan_insert BEFORE INSERT ON task_plans WHEN EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_plan_decision BEFORE UPDATE OF status ON task_plans WHEN NEW.status IN ('CONFIRMED','APPLIED') AND EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_approval_decision BEFORE UPDATE OF status ON approvals WHEN NEW.status='DECIDED' AND EXISTS(SELECT 1 FROM runs r JOIN feature_areas f ON f.id=r.feature_area_id WHERE r.id=NEW.run_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
`
