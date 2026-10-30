package sqlite

// Legacy incomplete merges have no dispatch proof. They are deliberately not
// imported as fresh operations: their missing outcome must remain UNKNOWN.
const mergeDeliveryV119SQL = `
CREATE TABLE merge_delivery_operations (
 operation_id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 merge_request_id TEXT NOT NULL, repository_id TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id), execution_id TEXT NOT NULL CHECK(execution_id<>''),
 member_id TEXT NOT NULL, workset_id TEXT NOT NULL, development_context_id TEXT NOT NULL,
 request_id TEXT NOT NULL, predecessor_operation_id TEXT UNIQUE REFERENCES merge_delivery_operations(operation_id),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)), payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 stage TEXT NOT NULL DEFAULT 'MERGE' CHECK(stage='MERGE'), created_at TEXT NOT NULL,
 UNIQUE(project_id,request_id), FOREIGN KEY(merge_request_id,repository_id) REFERENCES feature_area_commit_members(request_id,repository_id));
CREATE TABLE merge_delivery_current_operations (
 merge_request_id TEXT NOT NULL,repository_id TEXT NOT NULL,operation_id TEXT NOT NULL UNIQUE REFERENCES merge_delivery_operations(operation_id),
 PRIMARY KEY(merge_request_id,repository_id), FOREIGN KEY(merge_request_id,repository_id) REFERENCES feature_area_commit_members(request_id,repository_id));
CREATE TABLE merge_delivery_dispatches (
 operation_id TEXT PRIMARY KEY REFERENCES merge_delivery_operations(operation_id), runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),created_at TEXT NOT NULL);
CREATE TABLE merge_delivery_outcomes (
 operation_id TEXT PRIMARY KEY REFERENCES merge_delivery_dispatches(operation_id), outcome TEXT NOT NULL CHECK(outcome IN ('APPLIED','REJECTED_NOT_CONSUMED')),
 before_external_effect INTEGER NOT NULL,commit_oid TEXT NOT NULL CHECK(commit_oid<>''),merge_oid TEXT NOT NULL,error TEXT NOT NULL,created_at TEXT NOT NULL,
 CHECK((outcome='APPLIED' AND before_external_effect=0 AND merge_oid<>'' AND error='') OR (outcome='REJECTED_NOT_CONSUMED' AND before_external_effect=1 AND merge_oid='' AND error<>'')));
CREATE TRIGGER merge_operation_initial_exact BEFORE INSERT ON merge_delivery_operations WHEN NEW.predecessor_operation_id IS NULL AND NOT EXISTS(
 SELECT 1 FROM feature_area_commit_members m JOIN feature_area_commit_receipts p ON p.request_id=m.request_id JOIN tasks t ON t.id=NEW.task_id JOIN task_commit_receipts q ON q.task_id=t.id JOIN task_commit_members tm ON tm.task_id=t.id AND tm.repository_id=m.repository_id
 WHERE m.request_id=NEW.merge_request_id AND m.repository_id=NEW.repository_id AND p.feature_area_id=NEW.feature_area_id AND p.completed_at IS NULL
 AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.status='SUCCEEDED' AND q.terminal_outcome='APPLIED' AND q.completed_at IS NOT NULL
 AND tm.state='COMPLETED' AND EXISTS(SELECT 1 FROM json_each(m.task_commits_json) j WHERE j.value=tm.resulting_commit)
 AND NEW.execution_id=COALESCE(NULLIF(t.current_execution_id,''),'direct/'||q.result_id)
 AND m.member_id=NEW.member_id AND q.workspace_id=NEW.workset_id AND m.development_cycle_id=NEW.development_context_id
 AND json_extract(NEW.payload_json,'$.PlanSHA256')=p.plan_sha256 AND json_extract(NEW.payload_json,'$.ProjectID')=NEW.project_id AND json_extract(NEW.payload_json,'$.FeatureAreaID')=p.feature_area_id
 AND json_extract(NEW.payload_json,'$.WorkspaceID')=NEW.workset_id AND json_extract(NEW.payload_json,'$.Member.repositoryId')=m.repository_id
 AND json_extract(NEW.payload_json,'$.Member.memberId')=m.member_id AND json_extract(NEW.payload_json,'$.Member.memberVersion')=m.member_version
 AND json_extract(NEW.payload_json,'$.Member.sourceHead')=m.source_head AND json_extract(NEW.payload_json,'$.Member.expectedDevHead')=m.expected_dev_head
 AND json_extract(NEW.payload_json,'$.Member.developmentCycleId')=m.development_cycle_id)
BEGIN SELECT RAISE(ABORT,'Merge operation requires exact accepted member'); END;
CREATE TRIGGER merge_operation_retry_exact BEFORE INSERT ON merge_delivery_operations WHEN NEW.predecessor_operation_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM merge_delivery_operations p JOIN merge_delivery_current_operations c ON c.operation_id=p.operation_id JOIN merge_delivery_outcomes r ON r.operation_id=p.operation_id
 WHERE p.operation_id=NEW.predecessor_operation_id AND r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1
 AND p.project_id=NEW.project_id AND p.feature_area_id=NEW.feature_area_id AND p.merge_request_id=NEW.merge_request_id AND p.repository_id=NEW.repository_id
 AND p.task_id=NEW.task_id AND p.execution_id=NEW.execution_id AND p.member_id=NEW.member_id AND p.workset_id=NEW.workset_id AND p.development_context_id=NEW.development_context_id
 AND p.payload_json=NEW.payload_json AND p.payload_sha256=NEW.payload_sha256 AND p.stage=NEW.stage AND p.operation_id<>NEW.operation_id AND p.request_id<>NEW.request_id)
BEGIN SELECT RAISE(ABORT,'Merge retry requires exact rejected predecessor'); END;
CREATE TRIGGER merge_current_insert_exact BEFORE INSERT ON merge_delivery_current_operations WHEN NOT EXISTS(SELECT 1 FROM merge_delivery_operations o WHERE o.operation_id=NEW.operation_id AND o.merge_request_id=NEW.merge_request_id AND o.repository_id=NEW.repository_id AND o.predecessor_operation_id IS NULL)
BEGIN SELECT RAISE(ABORT,'Merge current requires initial identity'); END;
CREATE TRIGGER merge_current_update_exact BEFORE UPDATE ON merge_delivery_current_operations WHEN NEW.merge_request_id<>OLD.merge_request_id OR NEW.repository_id<>OLD.repository_id OR NOT EXISTS(SELECT 1 FROM merge_delivery_operations o WHERE o.operation_id=NEW.operation_id AND o.merge_request_id=OLD.merge_request_id AND o.repository_id=OLD.repository_id AND o.predecessor_operation_id=OLD.operation_id)
BEGIN SELECT RAISE(ABORT,'Merge current requires exact successor'); END;
CREATE TRIGGER merge_dispatch_exact BEFORE INSERT ON merge_delivery_dispatches WHEN NOT EXISTS(SELECT 1 FROM merge_delivery_operations o JOIN merge_delivery_current_operations c ON c.operation_id=o.operation_id JOIN feature_area_commit_receipts p ON p.request_id=o.merge_request_id WHERE o.operation_id=NEW.operation_id AND p.completed_at IS NULL AND NEW.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))
BEGIN SELECT RAISE(ABORT,'Merge dispatch requires current operation'); END;
CREATE TRIGGER merge_outcome_exact BEFORE INSERT ON merge_delivery_outcomes WHEN NOT EXISTS(SELECT 1 FROM merge_delivery_operations o JOIN merge_delivery_current_operations c ON c.operation_id=o.operation_id JOIN merge_delivery_dispatches d ON d.operation_id=o.operation_id WHERE o.operation_id=NEW.operation_id AND NEW.commit_oid=json_extract(o.payload_json,'$.Member.sourceHead') AND d.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))
BEGIN SELECT RAISE(ABORT,'Merge outcome requires exact dispatch'); END;
CREATE TRIGGER merge_parent_pending BEFORE INSERT ON feature_area_commit_receipts WHEN NEW.protocol_version=2 AND EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE feature_area_id=NEW.feature_area_id AND protocol_version=2 AND completed_at IS NULL)
BEGIN SELECT RAISE(ABORT,'Unresolved merge requires original operation'); END;
CREATE TRIGGER merge_member_result_exact BEFORE UPDATE OF state,resulting_dev_head ON feature_area_commit_members WHEN NEW.state IN ('COMPLETED','NO_CHANGE') AND EXISTS(SELECT 1 FROM merge_delivery_operations o WHERE o.merge_request_id=NEW.request_id AND o.repository_id=NEW.repository_id) AND NOT EXISTS(SELECT 1 FROM merge_delivery_current_operations c JOIN merge_delivery_outcomes r ON r.operation_id=c.operation_id WHERE c.merge_request_id=NEW.request_id AND c.repository_id=NEW.repository_id AND r.outcome='APPLIED' AND r.merge_oid=NEW.resulting_dev_head)
BEGIN SELECT RAISE(ABORT,'Merge projection requires exact applied outcome'); END;
CREATE TRIGGER merge_operation_immutable BEFORE UPDATE ON merge_delivery_operations BEGIN SELECT RAISE(ABORT,'Merge operation immutable'); END;
CREATE TRIGGER merge_operation_durable BEFORE DELETE ON merge_delivery_operations BEGIN SELECT RAISE(ABORT,'Merge operation durable'); END;
CREATE TRIGGER merge_dispatch_immutable BEFORE UPDATE ON merge_delivery_dispatches BEGIN SELECT RAISE(ABORT,'Merge dispatch immutable'); END;
CREATE TRIGGER merge_dispatch_durable BEFORE DELETE ON merge_delivery_dispatches BEGIN SELECT RAISE(ABORT,'Merge dispatch durable'); END;
CREATE TRIGGER merge_outcome_immutable BEFORE UPDATE ON merge_delivery_outcomes BEGIN SELECT RAISE(ABORT,'Merge outcome immutable'); END;
CREATE TRIGGER merge_outcome_durable BEFORE DELETE ON merge_delivery_outcomes BEGIN SELECT RAISE(ABORT,'Merge outcome durable'); END;
CREATE TRIGGER merge_current_durable BEFORE DELETE ON merge_delivery_current_operations BEGIN SELECT RAISE(ABORT,'Merge current durable'); END;
`
