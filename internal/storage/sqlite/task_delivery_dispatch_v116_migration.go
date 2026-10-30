package sqlite

const taskDeliveryDispatchV116SQL = `
CREATE TABLE task_delivery_dispatch_intents (
 task_id TEXT NOT NULL REFERENCES tasks(id), repository_id TEXT NOT NULL REFERENCES repositories(id),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id), created_at TEXT NOT NULL,
 PRIMARY KEY(task_id,repository_id));
CREATE TABLE task_delivery_dispatch_results (
 task_id TEXT NOT NULL, repository_id TEXT NOT NULL, payload_sha256 TEXT NOT NULL,
 commit_oid TEXT NOT NULL, tree_oid TEXT NOT NULL, changed INTEGER NOT NULL CHECK(changed IN (0,1)), created_at TEXT NOT NULL,
 PRIMARY KEY(task_id,repository_id), FOREIGN KEY(task_id,repository_id) REFERENCES task_delivery_dispatch_intents(task_id,repository_id));
CREATE TRIGGER task_delivery_dispatch_exact BEFORE INSERT ON task_delivery_dispatch_intents
WHEN NOT EXISTS(SELECT 1 FROM tasks t JOIN task_commit_receipts q ON q.task_id=t.id
 WHERE t.id=NEW.task_id AND ((t.status='ACTIVE' AND t.phase='DELIVERING') OR (q.protocol_version=1 AND t.status='SUCCEEDED')) AND q.completed_at IS NULL
 AND q.revision_id=t.current_definition_revision_id AND NEW.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND ((q.protocol_version=1 AND t.repository_id=NEW.repository_id) OR EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=t.id AND m.repository_id=NEW.repository_id AND m.state IN ('PENDING','UNKNOWN','FAILED')))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id AND (c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') OR c.interrupt_state='OUTCOME_UNKNOWN' OR c.backend_disposition='UNKNOWN_ORPHANED' OR (c.owner_type='TASK' AND c.state='CANCELED'))))
BEGIN SELECT RAISE(ABORT,'Task delivery dispatch requires current uncanceled admission'); END;
CREATE TRIGGER task_delivery_result_exact BEFORE INSERT ON task_delivery_dispatch_results
WHEN NOT EXISTS(SELECT 1 FROM task_delivery_dispatch_intents i WHERE i.task_id=NEW.task_id AND i.repository_id=NEW.repository_id AND i.payload_sha256=NEW.payload_sha256)
 OR (NEW.changed=1 AND (NEW.commit_oid='' OR NEW.tree_oid=''))
BEGIN SELECT RAISE(ABORT,'Task delivery result requires exact dispatch'); END;
CREATE TRIGGER task_delivery_dispatch_immutable BEFORE UPDATE ON task_delivery_dispatch_intents BEGIN SELECT RAISE(ABORT,'Task delivery dispatch immutable'); END;
CREATE TRIGGER task_delivery_dispatch_no_delete BEFORE DELETE ON task_delivery_dispatch_intents BEGIN SELECT RAISE(ABORT,'Task delivery dispatch durable'); END;
CREATE TRIGGER task_delivery_result_immutable BEFORE UPDATE ON task_delivery_dispatch_results BEGIN SELECT RAISE(ABORT,'Task delivery result immutable'); END;
CREATE TRIGGER task_delivery_result_no_delete BEFORE DELETE ON task_delivery_dispatch_results BEGIN SELECT RAISE(ABORT,'Task delivery result durable'); END;
`
