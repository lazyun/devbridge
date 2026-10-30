package sqlite

// v118 retains v116 dispatch history and imports only already durable outcomes.
// Missing v116 results remain dispatched operations with an unknown outcome.
const deliveryRecoveryV118SQL = `
ALTER TABLE task_commit_receipts ADD COLUMN terminal_outcome TEXT NOT NULL DEFAULT 'APPLIED' CHECK(terminal_outcome IN ('APPLIED','ABANDONED'));
DROP TRIGGER task_commit_v2_complete;
CREATE TRIGGER task_commit_v2_complete BEFORE UPDATE OF completed_at ON task_commit_receipts
WHEN NEW.protocol_version=2 AND NEW.completed_at IS NOT NULL AND NEW.terminal_outcome='APPLIED' AND (NOT EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=NEW.task_id) OR EXISTS(SELECT 1 FROM task_commit_members WHERE task_id=NEW.task_id AND state NOT IN ('COMPLETED','NO_CHANGE')) OR (SELECT count(*) FROM task_commit_members WHERE task_id=NEW.task_id) <> CASE WHEN NEW.evidence_kind='DIRECT_FILE' THEN (SELECT json_array_length(result_json,'$.members') FROM direct_file_implementation_results WHERE id=NEW.result_id) ELSE (SELECT count(*) FROM run_repository_bindings WHERE run_id=NEW.implementation_run_id) END)
BEGIN SELECT RAISE(ABORT,'All Task delivery members must complete'); END;
CREATE TABLE task_delivery_operations (
 operation_id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id), request_id TEXT NOT NULL, predecessor_operation_id TEXT REFERENCES task_delivery_operations(operation_id),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), payload_sha256 TEXT NOT NULL,
 member_id TEXT NOT NULL, execution_id TEXT NOT NULL, workset_id TEXT NOT NULL, development_context_id TEXT NOT NULL,
 stage TEXT NOT NULL DEFAULT 'COMMIT' CHECK(stage='COMMIT'), created_at TEXT NOT NULL,
 UNIQUE(project_id,request_id), UNIQUE(predecessor_operation_id));
CREATE TABLE task_delivery_operation_dispatches (
 operation_id TEXT PRIMARY KEY REFERENCES task_delivery_operations(operation_id), runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),created_at TEXT NOT NULL);
CREATE TABLE task_delivery_operation_outcomes (
 operation_id TEXT PRIMARY KEY REFERENCES task_delivery_operation_dispatches(operation_id),
 outcome TEXT NOT NULL CHECK(outcome IN ('APPLIED','REJECTED_NOT_CONSUMED')),
 before_external_effect INTEGER NOT NULL CHECK(before_external_effect IN (0,1)),
 commit_oid TEXT NOT NULL, tree_oid TEXT NOT NULL, changed INTEGER NOT NULL CHECK(changed IN (0,1)),error TEXT NOT NULL,created_at TEXT NOT NULL,
 CHECK((outcome='APPLIED' AND before_external_effect=0 AND error='' AND (changed=0 OR (commit_oid<>'' AND tree_oid<>''))) OR (outcome='REJECTED_NOT_CONSUMED' AND before_external_effect=1 AND commit_oid='' AND tree_oid='' AND changed=0 AND error<>'')));
CREATE TABLE task_delivery_current_operations (
 task_id TEXT NOT NULL REFERENCES tasks(id),repository_id TEXT NOT NULL REFERENCES repositories(id),
 operation_id TEXT NOT NULL UNIQUE REFERENCES task_delivery_operations(operation_id),PRIMARY KEY(task_id,repository_id));
CREATE TABLE task_delivery_abandonments (
 task_id TEXT PRIMARY KEY REFERENCES task_commit_receipts(task_id), project_id TEXT NOT NULL,request_id TEXT NOT NULL,
 members_json TEXT NOT NULL CHECK(json_valid(members_json)),operations_json TEXT NOT NULL CHECK(json_valid(operations_json)),created_at TEXT NOT NULL,
 FOREIGN KEY(project_id,request_id) REFERENCES execution_cancellations(project_id,request_id));

INSERT INTO task_delivery_operations(operation_id,project_id,task_id,repository_id,request_id,payload_json,payload_sha256,member_id,execution_id,workset_id,development_context_id,created_at)
 SELECT 'initial/'||hex(i.task_id)||'/'||hex(i.repository_id),t.project_id,i.task_id,i.repository_id,'initial/'||hex(i.task_id)||'/'||hex(i.repository_id),i.payload_json,i.payload_sha256,
 COALESCE(m.member_id,i.repository_id),'legacy-v116/'||hex(i.task_id),q.workspace_id,
 'legacy-v116/'||hex(i.task_id)||'/'||hex(i.repository_id),i.created_at
 FROM task_delivery_dispatch_intents i JOIN tasks t ON t.id=i.task_id JOIN task_commit_receipts q ON q.task_id=i.task_id LEFT JOIN task_commit_members m ON m.task_id=i.task_id AND m.repository_id=i.repository_id;
INSERT INTO task_delivery_current_operations SELECT task_id,repository_id,operation_id FROM task_delivery_operations;
INSERT INTO task_delivery_operation_dispatches SELECT o.operation_id,i.runtime_epoch,i.created_at FROM task_delivery_operations o JOIN task_delivery_dispatch_intents i ON i.task_id=o.task_id AND i.repository_id=o.repository_id;
INSERT INTO task_delivery_operation_outcomes SELECT o.operation_id,'APPLIED',0,r.commit_oid,r.tree_oid,r.changed,'',r.created_at FROM task_delivery_operations o JOIN task_delivery_dispatch_results r ON r.task_id=o.task_id AND r.repository_id=o.repository_id;

CREATE TRIGGER delivery_operation_initial_exact BEFORE INSERT ON task_delivery_operations WHEN NEW.predecessor_operation_id IS NULL AND NOT EXISTS(
 SELECT 1 FROM task_delivery_dispatch_intents i JOIN tasks t ON t.id=i.task_id JOIN task_commit_receipts q ON q.task_id=t.id WHERE i.task_id=NEW.task_id AND i.repository_id=NEW.repository_id AND t.project_id=NEW.project_id
 AND i.payload_json=NEW.payload_json AND i.payload_sha256=NEW.payload_sha256 AND NEW.operation_id LIKE 'initial/%' AND NEW.request_id=NEW.operation_id
 AND json_extract(NEW.payload_json,'$.TaskID')=i.task_id AND json_extract(NEW.payload_json,'$.RepositoryID')=i.repository_id AND json_extract(NEW.payload_json,'$.RevisionID')=q.revision_id AND NEW.workset_id=q.workspace_id)
BEGIN SELECT RAISE(ABORT,'Initial delivery operation requires exact frozen dispatch'); END;
CREATE TRIGGER delivery_operation_retry_exact BEFORE INSERT ON task_delivery_operations WHEN NEW.predecessor_operation_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM task_delivery_operations p JOIN task_delivery_operation_outcomes r ON r.operation_id=p.operation_id JOIN task_delivery_current_operations c ON c.operation_id=p.operation_id
 JOIN tasks t ON t.id=p.task_id JOIN task_commit_receipts q ON q.task_id=t.id
 WHERE p.operation_id=NEW.predecessor_operation_id AND r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1
 AND p.project_id=NEW.project_id AND p.task_id=NEW.task_id AND p.repository_id=NEW.repository_id AND p.payload_json=NEW.payload_json AND p.payload_sha256=NEW.payload_sha256
 AND p.member_id=NEW.member_id AND p.execution_id=NEW.execution_id AND p.workset_id=NEW.workset_id AND p.development_context_id=NEW.development_context_id AND p.stage=NEW.stage
 AND p.request_id<>NEW.request_id AND p.operation_id<>NEW.operation_id AND t.status='ACTIVE' AND t.phase IN ('DELIVERING','RECOVERY_REQUIRED') AND q.completed_at IS NULL)
BEGIN SELECT RAISE(ABORT,'Delivery retry requires exact current rejected operation'); END;
CREATE TRIGGER delivery_current_insert_exact BEFORE INSERT ON task_delivery_current_operations WHEN NOT EXISTS(SELECT 1 FROM task_delivery_operations o WHERE o.operation_id=NEW.operation_id AND o.task_id=NEW.task_id AND o.repository_id=NEW.repository_id AND o.predecessor_operation_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Delivery current operation requires initial identity'); END;
CREATE TRIGGER delivery_current_update_exact BEFORE UPDATE ON task_delivery_current_operations WHEN NEW.task_id<>OLD.task_id OR NEW.repository_id<>OLD.repository_id OR NOT EXISTS(SELECT 1 FROM task_delivery_operations o WHERE o.operation_id=NEW.operation_id AND o.task_id=OLD.task_id AND o.repository_id=OLD.repository_id AND o.predecessor_operation_id=OLD.operation_id)
BEGIN SELECT RAISE(ABORT,'Delivery current operation requires exact successor'); END;
CREATE TRIGGER delivery_operation_dispatch_exact BEFORE INSERT ON task_delivery_operation_dispatches WHEN NOT EXISTS(
 SELECT 1 FROM task_delivery_operations o JOIN task_delivery_current_operations c ON c.operation_id=o.operation_id JOIN tasks t ON t.id=o.task_id JOIN task_commit_receipts q ON q.task_id=t.id JOIN feature_areas a ON a.id=t.feature_area_id
 WHERE o.operation_id=NEW.operation_id AND ((t.status='ACTIVE' AND t.phase='DELIVERING') OR (q.protocol_version=1 AND t.status='SUCCEEDED')) AND q.completed_at IS NULL AND q.revision_id=t.current_definition_revision_id
 AND a.status='ACTIVE' AND NEW.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations x WHERE x.task_id=t.id AND (x.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') OR x.interrupt_state='OUTCOME_UNKNOWN' OR x.backend_disposition='UNKNOWN_ORPHANED' OR (x.owner_type='TASK' AND x.state='CANCELED'))))
BEGIN SELECT RAISE(ABORT,'Delivery dispatch requires current uncanceled operation'); END;
CREATE TRIGGER delivery_outcome_exact BEFORE INSERT ON task_delivery_operation_outcomes WHEN NOT EXISTS(
 SELECT 1 FROM task_delivery_operation_dispatches d JOIN task_delivery_operations o ON o.operation_id=d.operation_id JOIN task_delivery_current_operations c ON c.operation_id=o.operation_id
 WHERE o.operation_id=NEW.operation_id AND NOT EXISTS(SELECT 1 FROM task_delivery_abandonments a WHERE a.task_id=o.task_id)
 AND (NEW.outcome='APPLIED' OR d.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)))
BEGIN SELECT RAISE(ABORT,'Delivery outcome requires current exact dispatch'); END;
CREATE TRIGGER delivery_abandonment_exact BEFORE INSERT ON task_delivery_abandonments WHEN NOT EXISTS(
 SELECT 1 FROM execution_cancellations c JOIN tasks t ON t.id=c.task_id JOIN task_commit_receipts q ON q.task_id=t.id
 WHERE c.project_id=NEW.project_id AND c.request_id=NEW.request_id AND c.task_id=NEW.task_id AND c.owner_type='TASK' AND c.owner_id=t.id AND c.state='CANCELED' AND c.resolved_at IS NOT NULL AND c.backend_disposition='NO_BACKEND' AND q.completed_at IS NULL)
 OR EXISTS(SELECT 1 FROM task_delivery_operations o JOIN task_delivery_operation_dispatches d ON d.operation_id=o.operation_id LEFT JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id WHERE o.task_id=NEW.task_id AND r.operation_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Delivery abandonment requires settled exact Task cancellation'); END;
CREATE TRIGGER delivery_abandoned_parent_exact BEFORE UPDATE OF terminal_outcome ON task_commit_receipts WHEN NEW.terminal_outcome<>OLD.terminal_outcome AND (NEW.terminal_outcome<>'ABANDONED' OR OLD.completed_at IS NOT NULL OR NOT EXISTS(SELECT 1 FROM task_delivery_abandonments a WHERE a.task_id=NEW.task_id))
BEGIN SELECT RAISE(ABORT,'Delivery abandonment requires immutable receipt'); END;
CREATE TRIGGER delivery_abandoned_parent_immutable BEFORE UPDATE ON task_commit_receipts WHEN OLD.terminal_outcome='ABANDONED'
BEGIN SELECT RAISE(ABORT,'Abandoned delivery is immutable'); END;
CREATE TRIGGER delivery_abandoned_member_immutable BEFORE UPDATE ON task_commit_members WHEN EXISTS(SELECT 1 FROM task_delivery_abandonments a WHERE a.task_id=OLD.task_id)
BEGIN SELECT RAISE(ABORT,'Abandoned delivery member history is immutable'); END;
CREATE TRIGGER delivery_operation_immutable BEFORE UPDATE ON task_delivery_operations BEGIN SELECT RAISE(ABORT,'Delivery operation immutable'); END;
CREATE TRIGGER delivery_operation_durable BEFORE DELETE ON task_delivery_operations BEGIN SELECT RAISE(ABORT,'Delivery operation durable'); END;
CREATE TRIGGER delivery_dispatch_immutable BEFORE UPDATE ON task_delivery_operation_dispatches BEGIN SELECT RAISE(ABORT,'Delivery dispatch immutable'); END;
CREATE TRIGGER delivery_dispatch_durable BEFORE DELETE ON task_delivery_operation_dispatches BEGIN SELECT RAISE(ABORT,'Delivery dispatch durable'); END;
CREATE TRIGGER delivery_outcome_immutable BEFORE UPDATE ON task_delivery_operation_outcomes BEGIN SELECT RAISE(ABORT,'Delivery outcome immutable'); END;
CREATE TRIGGER delivery_outcome_durable BEFORE DELETE ON task_delivery_operation_outcomes BEGIN SELECT RAISE(ABORT,'Delivery outcome durable'); END;
CREATE TRIGGER delivery_current_durable BEFORE DELETE ON task_delivery_current_operations BEGIN SELECT RAISE(ABORT,'Delivery current operation durable'); END;
CREATE TRIGGER delivery_abandonment_immutable BEFORE UPDATE ON task_delivery_abandonments BEGIN SELECT RAISE(ABORT,'Delivery abandonment immutable'); END;
CREATE TRIGGER delivery_abandonment_durable BEFORE DELETE ON task_delivery_abandonments BEGIN SELECT RAISE(ABORT,'Delivery abandonment durable'); END;
`
