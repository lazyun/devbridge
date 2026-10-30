package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

func migrateTaskActionNodeV133(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE task_action_heads (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id),
 generation INTEGER NOT NULL CHECK(generation>0),
 owner_intent_id TEXT REFERENCES confirmation_intents(intent_id),
 conflict INTEGER NOT NULL DEFAULT 0 CHECK(conflict IN (0,1))
);
CREATE TABLE task_action_confirmation_bindings (
 intent_id TEXT PRIMARY KEY REFERENCES confirmation_intents(intent_id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 generation INTEGER NOT NULL CHECK(generation>0)
);
CREATE INDEX task_action_binding_task ON task_action_confirmation_bindings(task_id);
CREATE TABLE task_action_conflict_resolutions (
 task_id TEXT NOT NULL REFERENCES tasks(id),generation INTEGER NOT NULL,
 intent_id TEXT NOT NULL REFERENCES confirmation_intents(intent_id),
 PRIMARY KEY(task_id,generation)
);
CREATE TABLE task_action_releases (
 intent_id TEXT PRIMARY KEY REFERENCES confirmation_intents(intent_id),
 task_id TEXT NOT NULL REFERENCES tasks(id), generation INTEGER NOT NULL, reason TEXT NOT NULL
);
CREATE TRIGGER task_action_release_immutable BEFORE UPDATE ON task_action_releases BEGIN SELECT RAISE(ABORT,'Task action release immutable'); END;
CREATE TRIGGER task_action_release_durable BEFORE DELETE ON task_action_releases BEGIN SELECT RAISE(ABORT,'Task action release durable'); END;
CREATE TRIGGER task_action_resolution_immutable BEFORE UPDATE ON task_action_conflict_resolutions BEGIN SELECT RAISE(ABORT,'Task conflict resolution immutable'); END;
CREATE TRIGGER task_action_resolution_durable BEFORE DELETE ON task_action_conflict_resolutions BEGIN SELECT RAISE(ABORT,'Task conflict resolution durable'); END;
CREATE TRIGGER task_action_binding_immutable BEFORE UPDATE ON task_action_confirmation_bindings BEGIN SELECT RAISE(ABORT,'Task action binding immutable'); END;
CREATE TRIGGER task_action_binding_durable BEFORE DELETE ON task_action_confirmation_bindings BEGIN SELECT RAISE(ABORT,'Task action binding durable'); END;
INSERT INTO task_action_heads(task_id,generation) SELECT id,1 FROM tasks;
CREATE TRIGGER task_action_created AFTER INSERT ON tasks BEGIN
 INSERT INTO task_action_heads(task_id,generation) VALUES(NEW.id,1);
END;
CREATE TRIGGER task_action_lifecycle AFTER UPDATE OF phase,status,current_review_id,current_execution_id,current_definition_revision_id ON tasks
WHEN NEW.phase IS NOT OLD.phase OR NEW.status IS NOT OLD.status OR NEW.current_review_id IS NOT OLD.current_review_id OR NEW.current_execution_id IS NOT OLD.current_execution_id OR NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id
BEGIN UPDATE task_action_heads SET generation=generation+1 WHERE task_id=NEW.id; END;
CREATE TRIGGER task_action_run_result AFTER UPDATE OF status ON runs
WHEN NEW.status IS NOT OLD.status AND EXISTS(SELECT 1 FROM tasks t WHERE t.id=NEW.task_id AND (t.current_review_id=NEW.review_id OR t.current_execution_id=NEW.execution_proposal_id))
BEGIN UPDATE task_action_heads SET generation=generation+1 WHERE task_id=NEW.task_id; END;
CREATE TRIGGER task_action_confirmation_settled AFTER UPDATE OF intent_json ON confirmation_intents
WHEN json_extract(NEW.intent_json,'$.state') IN ('COMPLETED','REJECTED','SUPERSEDED') AND json_extract(NEW.intent_json,'$.state') IS NOT json_extract(OLD.intent_json,'$.state')
BEGIN UPDATE task_action_heads SET owner_intent_id=NULL,generation=generation+1 WHERE owner_intent_id=NEW.intent_id; END;
-- Adopt only exact current unresolved legacy facts. More than one is a
-- conflict, never a newest-timestamp selection. Existing payloads stay intact.
INSERT INTO task_action_confirmation_bindings(intent_id,task_id,generation)
SELECT c.intent_id,t.id,1 FROM confirmation_intents c JOIN tasks t ON t.id=COALESCE(json_extract(c.intent_json,'$.content.execution.taskId'),json_extract(c.intent_json,'$.content.review.taskId'),json_extract(c.intent_json,'$.content.featureArea.completionTaskId'),json_extract(c.intent_json,'$.content.recoveryPurpose.proposal.taskId'),(SELECT json_extract(j.value,'$.taskId') FROM json_each(json_extract(c.intent_json,'$.steps')) j WHERE COALESCE(json_extract(j.value,'$.taskId'),'')<>'' LIMIT 1))
WHERE json_extract(c.intent_json,'$.state') IN ('PENDING','CONFIRMING','UNKNOWN','PARTIAL')
AND (json_extract(c.intent_json,'$.commit') IS NOT NULL OR (
 t.status='ACTIVE' AND julianday(json_extract(c.intent_json,'$.expiresAt'))>julianday('now')
 AND COALESCE(json_extract(c.intent_json,'$.content.execution.taskDefinitionRevisionId'),t.current_definition_revision_id,'')=COALESCE(t.current_definition_revision_id,'')
 AND COALESCE(NULLIF(json_extract(c.intent_json,'$.content.execution.executionId'),''),t.current_execution_id,'')=COALESCE(t.current_execution_id,'')
 AND COALESCE(NULLIF(json_extract(c.intent_json,'$.content.review.reviewId'),''),NULLIF(json_extract(c.intent_json,'$.content.execution.reviewId'),''),t.current_review_id,'')=COALESCE(t.current_review_id,'')
));
UPDATE task_action_heads SET conflict=(SELECT count(*)>1 FROM task_action_confirmation_bindings b WHERE b.task_id=task_action_heads.task_id),
 owner_intent_id=(SELECT CASE WHEN count(*)=1 THEN min(intent_id) END FROM task_action_confirmation_bindings b WHERE b.task_id=task_action_heads.task_id);
`)
	if err != nil {
		return err
	}
	// These are workflow facts, not UI reads or runtime heartbeat updates.
	for _, v := range []struct{ name, table, key string }{
		{"choice", "task_hold_receipts", "NEW.task_id"},
		{"delivery", "task_delivery_operations", "NEW.task_id"},
		{"analysis", "run_analyses", "(SELECT task_id FROM runs WHERE id=NEW.run_id)"},
		{"decision", "decision_resolution_receipts", "(SELECT id FROM tasks WHERE id=NEW.subject_id UNION ALL SELECT task_id FROM reviews WHERE id=NEW.subject_id UNION ALL SELECT task_id FROM execution_proposals WHERE id=NEW.subject_id LIMIT 1)"},
	} {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER task_action_%s AFTER INSERT ON %s BEGIN UPDATE task_action_heads SET generation=generation+1 WHERE task_id=%s; END;`, v.name, v.table, v.key)); err != nil {
			return err
		}
	}
	return nil
}
