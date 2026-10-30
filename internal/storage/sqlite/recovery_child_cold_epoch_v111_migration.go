package sqlite

import (
	"context"
	"database/sql"
)

// Pre-Run adoption has no Run to reference yet. Keep its exact admitted plan
// and epoch transition separately; never rewrite the original admission.
func migrateRecoveryChildColdEpochV111(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TRIGGER recovery_status_adoption_exact BEFORE INSERT ON recovery_status_adoptions
WHEN NEW.previous_epoch=NEW.current_epoch OR NOT EXISTS(
 SELECT 1 FROM recovery_status_records rec JOIN runs child ON child.id=rec.recovery_run_id JOIN runs source ON source.id=rec.source_run_id
 WHERE rec.id=NEW.recovery_id AND child.id=NEW.run_id AND rec.state NOT IN ('RESOLVED','FAILED')
 AND rec.runtime_epoch=NEW.current_epoch AND NEW.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND child.runtime_epoch=NEW.previous_epoch AND child.purpose='RECOVERY_STATUS'
 AND child.project_id=source.project_id AND child.task_id=source.task_id AND child.feature_area_id IS source.feature_area_id
 AND child.operation_id=source.operation_id AND child.operation_kind=source.operation_kind)
BEGIN SELECT RAISE(ABORT,'Recovery adoption must retain exact child epoch and owner'); END;
CREATE TABLE recovery_child_preparation_adoptions (
 recovery_id TEXT NOT NULL REFERENCES recovery_status_records(id),
 child_plan_id TEXT NOT NULL REFERENCES runtime_scope_plans(plan_id),
 previous_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 current_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(recovery_id,current_epoch),
 CHECK(previous_epoch<>current_epoch)
);
CREATE TRIGGER recovery_child_preparation_adoption_exact BEFORE INSERT ON recovery_child_preparation_adoptions
WHEN NOT EXISTS(SELECT 1 FROM recovery_status_records r JOIN recovery_child_admissions a ON a.recovery_id=r.id
 WHERE r.id=NEW.recovery_id AND r.state='PREPARED' AND r.recovery_run_id IS NULL
 AND r.runtime_epoch=NEW.current_epoch AND NEW.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND a.child_plan_id=NEW.child_plan_id
 AND NEW.previous_epoch=COALESCE((SELECT prior.current_epoch FROM recovery_child_preparation_adoptions prior
 WHERE prior.recovery_id=a.recovery_id AND prior.child_plan_id=a.child_plan_id ORDER BY prior.rowid DESC LIMIT 1),a.runtime_epoch))
BEGIN SELECT RAISE(ABORT,'Recovery preparation adoption must retain exact unstarted owner'); END;
CREATE TRIGGER recovery_child_preparation_adoption_immutable BEFORE UPDATE ON recovery_child_preparation_adoptions BEGIN SELECT RAISE(ABORT,'Recovery preparation adoption immutable'); END;
CREATE TRIGGER recovery_child_preparation_adoption_no_delete BEFORE DELETE ON recovery_child_preparation_adoptions BEGIN SELECT RAISE(ABORT,'Recovery preparation adoption durable'); END;
DROP TRIGGER recovery_child_thread_attempt_binding;
CREATE TRIGGER recovery_child_thread_attempt_binding BEFORE INSERT ON recovery_child_thread_attempts
WHEN NOT EXISTS(
 WITH RECURSIVE epochs(epoch) AS (
 SELECT NEW.runtime_epoch
 UNION SELECT a.previous_epoch FROM recovery_status_adoptions a JOIN epochs e ON e.epoch=a.current_epoch WHERE a.recovery_id=NEW.recovery_id AND a.run_id=NEW.child_run_id
 UNION SELECT a.previous_epoch FROM recovery_child_preparation_adoptions a JOIN epochs e ON e.epoch=a.current_epoch JOIN recovery_child_admissions admitted ON admitted.recovery_id=a.recovery_id AND admitted.child_plan_id=a.child_plan_id WHERE a.recovery_id=NEW.recovery_id
 )
 SELECT 1 FROM recovery_child_admissions a JOIN recovery_status_records rec ON rec.id=a.recovery_id
 JOIN runtime_workset_bindings b ON b.plan_id=a.child_plan_id
 JOIN runtime_start_authorities start ON start.authority_id=b.start_authority_id AND start.plan_id=b.plan_id AND start.run_id=b.run_id AND start.workset_content_hash=b.workset_content_hash AND start.runtime_epoch=b.runtime_epoch
 JOIN runs source ON source.id=a.source_run_id JOIN runs child ON child.id=NEW.child_run_id
 WHERE a.recovery_id=NEW.recovery_id AND b.run_id=NEW.child_run_id AND b.workset_content_hash=NEW.child_runtime_workset_hash
 AND rec.recovery_run_id=child.id AND rec.state='RUN_CREATED' AND rec.runtime_epoch=NEW.runtime_epoch AND child.runtime_epoch=rec.runtime_epoch
 AND a.runtime_epoch IN(SELECT epoch FROM epochs) AND b.runtime_epoch IN(SELECT epoch FROM epochs)
 AND source.thread_id=a.source_thread_id AND source.turn_id=a.source_turn_id AND source.runtime_workset_hash=a.source_runtime_workset_hash
 AND source.workspace_id=a.source_workspace_id AND NEW.child_workspace_id<>a.source_workspace_id
 AND NEW.child_workspace_id=json_extract(b.workset_json,'$.workspaceId')
 AND NOT EXISTS(SELECT 1 FROM json_each(b.workset_json,'$.members') child_member JOIN json_each(source.runtime_workset_json,'$.members') source_member
 ON json_extract(child_member.value,'$.canonicalRoot')=json_extract(source_member.value,'$.canonicalRoot')))
BEGIN SELECT RAISE(ABORT,'Recovery child Workset/start authority mismatch'); END;`)
	return err
}
