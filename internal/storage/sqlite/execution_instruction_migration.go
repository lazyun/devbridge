package sqlite

import (
	"context"
	"database/sql"
)

// V53 only adds immutable provenance and exact start receipts. It does not
// rewrite any historical proposal, definition, approval, Execution or Run.
func migrateExecutionInstructionsV53(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
CREATE TABLE execution_instruction_sources (
 revision_id TEXT PRIMARY KEY REFERENCES task_definition_revisions(id),
 source_revision_id TEXT REFERENCES task_definition_revisions(id),
 decision_id TEXT NOT NULL REFERENCES review_approval_decisions(decision_id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 request_id TEXT NOT NULL,
 source_kind TEXT NOT NULL CHECK(source_kind IN ('REVIEW_APPROVAL','EXECUTION_START')),
 instructions_sha256 TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE review_approval_instruction_payloads (
 decision_id TEXT PRIMARY KEY REFERENCES review_approval_decisions(decision_id),
 instructions_sha256 TEXT NOT NULL
);
CREATE TABLE task_execution_instruction_receipts (
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 request_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(id),
 payload_sha256 TEXT NOT NULL,
 source_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(ai_session_id,request_id)
);
CREATE TRIGGER execution_instruction_sources_immutable BEFORE UPDATE ON execution_instruction_sources BEGIN SELECT RAISE(ABORT,'Execution instruction source is immutable'); END;
CREATE TRIGGER execution_instruction_sources_nodelete BEFORE DELETE ON execution_instruction_sources BEGIN SELECT RAISE(ABORT,'Execution instruction source is immutable'); END;
CREATE TRIGGER review_approval_instruction_payloads_immutable BEFORE UPDATE ON review_approval_instruction_payloads BEGIN SELECT RAISE(ABORT,'Approval instruction payload is immutable'); END;
CREATE TRIGGER review_approval_instruction_payloads_nodelete BEFORE DELETE ON review_approval_instruction_payloads BEGIN SELECT RAISE(ABORT,'Approval instruction payload is immutable'); END;
CREATE TRIGGER task_execution_instruction_receipts_immutable BEFORE UPDATE ON task_execution_instruction_receipts BEGIN SELECT RAISE(ABORT,'Execution instruction receipt is immutable'); END;
CREATE TRIGGER task_execution_instruction_receipts_nodelete BEFORE DELETE ON task_execution_instruction_receipts BEGIN SELECT RAISE(ABORT,'Execution instruction receipt is immutable'); END;
DROP TRIGGER approved_task_revision_immutable;
CREATE TRIGGER approved_task_revision_immutable BEFORE UPDATE OF current_definition_revision_id ON tasks
 WHEN EXISTS(SELECT 1 FROM review_approval_materializations m WHERE m.task_id=NEW.id AND m.initial_revision_id IS NOT NEW.current_definition_revision_id)
 AND NOT EXISTS(SELECT 1 FROM decision_resolution_receipts a JOIN execution_proposals predecessor ON predecessor.id=a.subject_id
  JOIN task_definition_revisions revision ON revision.id=NEW.current_definition_revision_id AND revision.task_id=NEW.id
  WHERE a.decision_kind='TASK_ACCEPTANCE' AND a.subject_kind='EXECUTION_PROPOSAL' AND a.option='REQUEST_CHANGES' AND a.result_kind='EXECUTION_PROPOSAL'
  AND a.project_id=NEW.project_id AND predecessor.task_id=NEW.id AND predecessor.task_definition_revision_id=OLD.current_definition_revision_id
  AND NOT EXISTS(SELECT 1 FROM execution_proposals successor WHERE successor.id=a.result_id)
  AND OLD.current_execution_id=predecessor.id
  AND json_extract(revision.payload_json,'$.workSpec')=json_remove(json(NEW.work_spec_json),'$.version')
  AND json_extract(revision.payload_json,'$.verificationSpec')=json_remove(json(NEW.verification_spec_json),'$.version')
  AND NEW.goal=json_extract(revision.payload_json,'$.workSpec.goal')
  AND json_extract(NEW.constraints_json,'$.items')=json_extract(revision.payload_json,'$.workSpec.constraints'))
 AND NOT EXISTS(SELECT 1 FROM execution_instruction_sources ins
 JOIN task_definition_revisions src ON src.id=ins.source_revision_id AND src.task_id=OLD.id
 JOIN task_definition_revisions dst ON dst.id=ins.revision_id AND dst.task_id=OLD.id
 JOIN review_approval_materializations m ON m.initial_revision_id=src.id AND m.task_id=OLD.id AND m.decision_id=ins.decision_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=m.decision_id AND receipt.option='APPROVE' AND receipt.project_id=OLD.project_id
 JOIN review_approval_decisions approved ON approved.decision_id=m.decision_id AND approved.review_id=OLD.current_review_id
 WHERE ins.source_kind='EXECUTION_START' AND ins.task_id=OLD.id AND ins.decision_id=OLD.review_approval_id
 AND src.id=OLD.current_definition_revision_id AND dst.id=NEW.current_definition_revision_id
 AND OLD.current_execution_id IS NULL AND NEW.current_execution_id IS NULL AND OLD.status='ACTIVE' AND NEW.status=OLD.status AND OLD.phase='IMPLEMENTATION_READY' AND NEW.phase=OLD.phase
 AND json_extract(src.payload_json,'$.workSpec.implementationMode')='CODING_AGENT'
 AND json_remove(src.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')=json_remove(dst.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')
 AND json_extract(dst.payload_json,'$.workSpec')=json_remove(json(NEW.work_spec_json),'$.version')
 AND json_extract(dst.payload_json,'$.verificationSpec')=json_remove(json(NEW.verification_spec_json),'$.version')
 AND NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.model IS OLD.model AND NEW.reasoning_effort IS OLD.reasoning_effort AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement AND NEW.goal IS OLD.goal AND NEW.constraints_json IS OLD.constraints_json)
 BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
DROP TRIGGER approved_task_semantics_immutable;
CREATE TRIGGER approved_task_semantics_immutable BEFORE UPDATE OF project_id,feature_area_id,repository_id,module_id,codex_config_id,model,reasoning_effort,goal,constraints_json,base_commit,problem_statement,work_spec_json,verification_spec_json ON tasks
 WHEN OLD.review_approval_id IS NOT NULL AND (NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.module_id IS NOT OLD.module_id OR NEW.codex_config_id IS NOT OLD.codex_config_id OR NEW.model IS NOT OLD.model OR NEW.reasoning_effort IS NOT OLD.reasoning_effort OR NEW.goal IS NOT OLD.goal OR NEW.constraints_json IS NOT OLD.constraints_json OR NEW.base_commit IS NOT OLD.base_commit OR NEW.problem_statement IS NOT OLD.problem_statement OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.verification_spec_json IS NOT OLD.verification_spec_json)
 AND NOT (NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.model IS OLD.model AND NEW.reasoning_effort IS OLD.reasoning_effort AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement
  AND EXISTS(SELECT 1 FROM decision_resolution_receipts a JOIN execution_proposals predecessor ON predecessor.id=a.subject_id
   JOIN task_definition_revisions revision ON revision.id=NEW.current_definition_revision_id AND revision.task_id=NEW.id
   WHERE a.decision_kind='TASK_ACCEPTANCE' AND a.subject_kind='EXECUTION_PROPOSAL' AND a.option='REQUEST_CHANGES' AND a.result_kind='EXECUTION_PROPOSAL'
   AND a.project_id=NEW.project_id AND predecessor.task_id=NEW.id AND predecessor.task_definition_revision_id=OLD.current_definition_revision_id
   AND NOT EXISTS(SELECT 1 FROM execution_proposals successor WHERE successor.id=a.result_id)
   AND OLD.current_execution_id=predecessor.id
   AND json_extract(revision.payload_json,'$.workSpec')=json_remove(json(NEW.work_spec_json),'$.version')
   AND json_extract(revision.payload_json,'$.verificationSpec')=json_remove(json(NEW.verification_spec_json),'$.version')
   AND NEW.goal=json_extract(revision.payload_json,'$.workSpec.goal')
   AND json_extract(NEW.constraints_json,'$.items')=json_extract(revision.payload_json,'$.workSpec.constraints')))
 AND NOT EXISTS(SELECT 1 FROM execution_instruction_sources ins
 JOIN task_definition_revisions src ON src.id=ins.source_revision_id AND src.task_id=OLD.id
 JOIN task_definition_revisions dst ON dst.id=ins.revision_id AND dst.task_id=OLD.id
 JOIN review_approval_materializations m ON m.initial_revision_id=src.id AND m.task_id=OLD.id AND m.decision_id=ins.decision_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=m.decision_id AND receipt.option='APPROVE' AND receipt.project_id=OLD.project_id
 JOIN review_approval_decisions approved ON approved.decision_id=m.decision_id AND approved.review_id=OLD.current_review_id
 WHERE ins.source_kind='EXECUTION_START' AND ins.task_id=OLD.id AND ins.decision_id=OLD.review_approval_id
 AND src.id=OLD.current_definition_revision_id AND dst.id=NEW.current_definition_revision_id
 AND OLD.current_execution_id IS NULL AND NEW.current_execution_id IS NULL AND OLD.status='ACTIVE' AND NEW.status=OLD.status AND OLD.phase='IMPLEMENTATION_READY' AND NEW.phase=OLD.phase
 AND json_extract(src.payload_json,'$.workSpec.implementationMode')='CODING_AGENT'
 AND json_remove(src.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')=json_remove(dst.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')
 AND json_extract(dst.payload_json,'$.workSpec')=json_remove(json(NEW.work_spec_json),'$.version')
 AND json_extract(dst.payload_json,'$.verificationSpec')=json_remove(json(NEW.verification_spec_json),'$.version')
 AND NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.model IS OLD.model AND NEW.reasoning_effort IS OLD.reasoning_effort AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement AND NEW.goal IS OLD.goal AND NEW.constraints_json IS OLD.constraints_json)
 BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
DROP TRIGGER approved_execution_binding;
CREATE TRIGGER approved_execution_binding BEFORE INSERT ON execution_proposals
WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL)
AND (NEW.status<>'CONFIRMED'
 OR (
 NOT EXISTS(SELECT 1 FROM tasks t JOIN review_approval_materializations m ON m.task_id=t.id
  JOIN decision_resolution_receipts a ON a.decision_id=m.decision_id JOIN task_plans p ON p.id=NEW.task_plan_id AND p.task_id=t.id
  WHERE t.id=NEW.task_id AND a.option='APPROVE' AND a.project_id=t.project_id AND t.review_approval_id=a.decision_id AND p.status='APPLIED'
  AND t.current_definition_revision_id=NEW.task_definition_revision_id
  AND (NEW.task_definition_revision_id=m.initial_revision_id OR EXISTS(SELECT 1 FROM execution_instruction_sources ins JOIN task_definition_revisions src ON src.id=ins.source_revision_id JOIN task_definition_revisions dst ON dst.id=ins.revision_id JOIN review_approval_decisions approved ON approved.decision_id=ins.decision_id WHERE ins.revision_id=NEW.task_definition_revision_id AND ins.source_revision_id=m.initial_revision_id AND ins.task_id=t.id AND ins.decision_id=m.decision_id AND ins.source_kind='EXECUTION_START' AND approved.review_id=t.current_review_id AND src.task_id=t.id AND dst.task_id=t.id AND json_remove(src.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')=json_remove(dst.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')))
  AND NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.task_id=NEW.task_id))
 AND NOT EXISTS(SELECT 1 FROM decision_resolution_receipts a JOIN execution_proposals predecessor ON predecessor.id=a.subject_id JOIN tasks t ON t.id=NEW.task_id JOIN task_plans p ON p.id=NEW.task_plan_id
  WHERE a.decision_kind='TASK_ACCEPTANCE' AND a.subject_kind='EXECUTION_PROPOSAL' AND a.result_kind='EXECUTION_PROPOSAL' AND a.result_id=NEW.id
  AND a.option IN ('REQUEST_CHANGES','REQUEST_REVERIFICATION') AND a.project_id=NEW.project_id AND predecessor.task_id=NEW.task_id AND predecessor.outcome IN ('SUCCEEDED','FAILED','CANCELED')
  AND t.current_definition_revision_id=NEW.task_definition_revision_id AND t.confirmed_task_plan_id=NEW.task_plan_id AND p.status='APPLIED'
  AND ((a.option='REQUEST_REVERIFICATION' AND NEW.task_definition_revision_id=predecessor.task_definition_revision_id AND NEW.task_plan_id=predecessor.task_plan_id)
    OR (a.option='REQUEST_CHANGES' AND NEW.task_definition_revision_id<>predecessor.task_definition_revision_id)))))
BEGIN SELECT RAISE(ABORT,'EXECUTION_PREREQUISITE_NOT_SATISFIED'); END;
`)
	return err
}
