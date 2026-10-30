// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

const schemaV38 = `
-- Existing reviews and receipt meanings remain unchanged. Only admissions made
-- under this contract receive a workflow source; no historical approval is inferred.
CREATE TABLE review_workflow_sources (
 review_id TEXT PRIMARY KEY REFERENCES reviews(id),
 development_cycle_id TEXT REFERENCES development_cycles(id),
 development_ref TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE review_approval_decisions (
 decision_id TEXT PRIMARY KEY,
 review_id TEXT NOT NULL UNIQUE REFERENCES review_workflow_sources(review_id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 review_task_id TEXT NOT NULL REFERENCES tasks(id),
 start_request_id TEXT NOT NULL,
 provenance_json TEXT NOT NULL CHECK(json_valid(provenance_json)),
 provenance_sha256 TEXT NOT NULL CHECK(length(provenance_sha256)=64),
 result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64),
 result_text TEXT NOT NULL,
 created_at TEXT NOT NULL
);
ALTER TABLE tasks ADD COLUMN review_approval_id TEXT REFERENCES review_approval_decisions(decision_id);
CREATE UNIQUE INDEX tasks_review_approval ON tasks(review_approval_id) WHERE review_approval_id IS NOT NULL;
CREATE TABLE review_approval_receipt_context (
 decision_id TEXT PRIMARY KEY REFERENCES decision_resolution_receipts(decision_id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 created_at TEXT NOT NULL
);
CREATE TRIGGER review_receipt_context_immutable BEFORE UPDATE ON review_approval_receipt_context BEGIN SELECT RAISE(ABORT,'Review receipt context is immutable'); END;
CREATE TRIGGER review_receipt_context_no_delete BEFORE DELETE ON review_approval_receipt_context BEGIN SELECT RAISE(ABORT,'Review receipt context is durable'); END;
CREATE TABLE review_approval_materializations (
 decision_id TEXT PRIMARY KEY REFERENCES decision_resolution_receipts(decision_id),
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
 initial_revision_id TEXT NOT NULL UNIQUE REFERENCES task_definition_revisions(id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 created_at TEXT NOT NULL
);
CREATE TRIGGER review_workflow_source_immutable BEFORE UPDATE ON review_workflow_sources BEGIN SELECT RAISE(ABORT,'Review workflow source is immutable'); END;
CREATE TRIGGER review_workflow_source_no_delete BEFORE DELETE ON review_workflow_sources BEGIN SELECT RAISE(ABORT,'Review workflow source is durable'); END;
CREATE TRIGGER review_approval_immutable BEFORE UPDATE ON review_approval_decisions BEGIN SELECT RAISE(ABORT,'Review Approval is immutable'); END;
CREATE TRIGGER review_approval_no_delete BEFORE DELETE ON review_approval_decisions BEGIN SELECT RAISE(ABORT,'Review Approval is durable'); END;
CREATE TRIGGER review_materialization_immutable BEFORE UPDATE ON review_approval_materializations BEGIN SELECT RAISE(ABORT,'Review materialization is immutable'); END;
CREATE TRIGGER review_materialization_no_delete BEFORE DELETE ON review_approval_materializations BEGIN SELECT RAISE(ABORT,'Review materialization is durable'); END;
CREATE TRIGGER approved_review_result_immutable BEFORE UPDATE OF result_text ON runs
 WHEN NEW.result_text IS NOT OLD.result_text AND EXISTS(SELECT 1 FROM review_approval_decisions WHERE review_id=OLD.review_id)
 BEGIN SELECT RAISE(ABORT,'Review Approval result is immutable'); END;
CREATE TRIGGER approved_task_source_immutable BEFORE UPDATE OF review_approval_id ON tasks
 WHEN NEW.review_approval_id IS NOT OLD.review_approval_id BEGIN SELECT RAISE(ABORT,'Task approval source is immutable'); END;
CREATE TRIGGER approved_task_insert BEFORE INSERT ON tasks WHEN NEW.review_approval_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM review_approval_decisions d JOIN decision_resolution_receipts r ON r.decision_id=d.decision_id
 WHERE d.decision_id=NEW.review_approval_id AND d.project_id=NEW.project_id AND d.feature_area_id=NEW.feature_area_id
 AND r.option='APPROVE' AND r.result_id=NEW.id AND r.result_kind='DEVELOPMENT_TASK')
 BEGIN SELECT RAISE(ABORT,'REVIEW_APPROVAL_REQUIRED'); END;
CREATE TRIGGER approved_task_semantics_immutable BEFORE UPDATE OF project_id,feature_area_id,repository_id,module_id,codex_config_id,model,reasoning_effort,goal,constraints_json,base_commit,problem_statement,work_spec_json,verification_spec_json ON tasks
 WHEN OLD.review_approval_id IS NOT NULL AND (NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.module_id IS NOT OLD.module_id OR NEW.codex_config_id IS NOT OLD.codex_config_id OR NEW.model IS NOT OLD.model OR NEW.reasoning_effort IS NOT OLD.reasoning_effort OR NEW.goal IS NOT OLD.goal OR NEW.constraints_json IS NOT OLD.constraints_json OR NEW.base_commit IS NOT OLD.base_commit OR NEW.problem_statement IS NOT OLD.problem_statement OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.verification_spec_json IS NOT OLD.verification_spec_json)
 BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_revision_immutable BEFORE UPDATE OF current_definition_revision_id ON tasks
 WHEN EXISTS(SELECT 1 FROM review_approval_materializations m WHERE m.task_id=NEW.id AND m.initial_revision_id IS NOT NEW.current_definition_revision_id)
 BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_goals_insert BEFORE INSERT ON task_goals WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL) BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_goals_update BEFORE UPDATE ON task_goals WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL) BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_goals_delete BEFORE DELETE ON task_goals WHEN EXISTS(SELECT 1 FROM tasks WHERE id=OLD.task_id AND review_approval_id IS NOT NULL) BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;

CREATE TRIGGER approved_preparation_binding BEFORE INSERT ON task_preparations
 WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM tasks t JOIN review_approval_materializations m ON m.task_id=t.id JOIN review_approval_decisions d ON d.decision_id=m.decision_id WHERE t.id=NEW.task_id AND NEW.task_definition_revision_id=m.initial_revision_id AND NEW.source_review_id=d.review_id AND t.current_definition_revision_id=m.initial_revision_id)
 BEGIN SELECT RAISE(ABORT,'REVIEW_APPROVAL_REQUIRED'); END;
CREATE TRIGGER approved_execution_binding BEFORE INSERT ON execution_proposals
 WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL)
 AND (NEW.status<>'CONFIRMED' OR NOT EXISTS(
 SELECT 1 FROM tasks t JOIN review_approval_materializations m ON m.task_id=t.id JOIN task_preparations p ON p.task_id=t.id JOIN task_plans plan ON plan.id=p.task_plan_id
 WHERE t.id=NEW.task_id AND p.id=NEW.task_preparation_id AND p.status='READY' AND plan.status='APPLIED' AND NEW.task_plan_id=p.task_plan_id AND NEW.task_definition_revision_id=m.initial_revision_id AND t.current_definition_revision_id=m.initial_revision_id)
 OR EXISTS(SELECT 1 FROM execution_proposals WHERE task_preparation_id=NEW.task_preparation_id))
 BEGIN SELECT RAISE(ABORT,'EXECUTION_PREREQUISITE_NOT_SATISFIED'); END;
`
