package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// V55 preserves every approval/revision while recording explicit supplemental
// requests and candidate replacement. Only migration source is added here.
func migrateSupplementalReviewV55(ctx context.Context, tx *sql.Tx) error {
	if err := rebuildSupplementalMaterializationsV55(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
CREATE TABLE task_supplemental_review_requests (
 id TEXT PRIMARY KEY, ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),project_id TEXT NOT NULL REFERENCES projects(id),task_id TEXT NOT NULL REFERENCES tasks(id),feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL,payload_sha256 TEXT NOT NULL,purpose TEXT NOT NULL,source_phase TEXT NOT NULL,source_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),source_review_id TEXT NOT NULL REFERENCES reviews(id),source_execution_id TEXT NOT NULL,source_decision_id TEXT NOT NULL,created_at TEXT NOT NULL,UNIQUE(ai_session_id,request_id));
CREATE TABLE task_supplemental_review_current(task_id TEXT PRIMARY KEY REFERENCES tasks(id),request_id TEXT NOT NULL UNIQUE REFERENCES task_supplemental_review_requests(id));
CREATE TABLE task_review_superseded_executions(execution_id TEXT PRIMARY KEY REFERENCES execution_proposals(id),request_id TEXT NOT NULL REFERENCES task_supplemental_review_requests(id));
CREATE TABLE task_supplemental_review_starts(review_id TEXT PRIMARY KEY REFERENCES reviews(id),request_id TEXT NOT NULL REFERENCES task_supplemental_review_requests(id));
CREATE TRIGGER supplemental_request_immutable BEFORE UPDATE ON task_supplemental_review_requests BEGIN SELECT RAISE(ABORT,'Supplemental request is immutable'); END;
CREATE TRIGGER supplemental_request_nodelete BEFORE DELETE ON task_supplemental_review_requests BEGIN SELECT RAISE(ABORT,'Supplemental request is immutable'); END;
CREATE TRIGGER supplemental_superseded_immutable BEFORE UPDATE ON task_review_superseded_executions BEGIN SELECT RAISE(ABORT,'Execution supersession is immutable'); END;
CREATE TRIGGER supplemental_superseded_nodelete BEFORE DELETE ON task_review_superseded_executions BEGIN SELECT RAISE(ABORT,'Execution supersession is immutable'); END;
CREATE TRIGGER supplemental_starts_immutable BEFORE UPDATE ON task_supplemental_review_starts BEGIN SELECT RAISE(ABORT,'Supplemental Review source is immutable'); END;
CREATE TRIGGER supplemental_starts_nodelete BEFORE DELETE ON task_supplemental_review_starts BEGIN SELECT RAISE(ABORT,'Supplemental Review source is immutable'); END;
-- Merge these trigger replacements after the new supplemental relationship tables.
-- Rebuild review_approval_materializations with same fields/PK/initial_revision UNIQUE, remove only task_id UNIQUE.
-- Preserve runtime_config_json TEXT added by V46.

DROP TRIGGER approved_task_source_immutable;
CREATE TRIGGER approved_task_source_immutable BEFORE UPDATE OF review_approval_id ON tasks
 WHEN NEW.review_approval_id IS NOT OLD.review_approval_id AND NOT (OLD.review_approval_id IS NULL AND OLD.phase='REVIEW_APPROVAL_PENDING' AND EXISTS(SELECT 1 FROM review_approval_decisions d JOIN decision_resolution_receipts r ON r.decision_id=d.decision_id WHERE d.decision_id=NEW.review_approval_id AND d.task_id=NEW.id AND d.review_id=OLD.current_review_id AND r.option='APPROVE' AND r.result_id=NEW.id)) AND NOT EXISTS(SELECT 1 FROM task_supplemental_review_current cur
 JOIN task_supplemental_review_requests req ON req.id=cur.request_id AND req.task_id=OLD.id AND req.project_id=OLD.project_id
 JOIN task_supplemental_review_starts started ON started.request_id=req.id AND started.review_id=OLD.current_review_id
 JOIN review_approval_decisions approved ON approved.review_id=started.review_id AND approved.task_id=OLD.id AND approved.project_id=OLD.project_id AND approved.decision_id=NEW.review_approval_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=approved.decision_id AND receipt.option='APPROVE' AND receipt.result_id=OLD.id AND receipt.project_id=OLD.project_id
 WHERE cur.task_id=OLD.id AND req.source_revision_id=OLD.current_definition_revision_id
 AND OLD.phase='REVIEW_APPROVAL_PENDING' AND NEW.phase=OLD.phase AND OLD.status='ACTIVE' AND NEW.status=OLD.status
 AND OLD.current_execution_id IS NULL AND NEW.current_execution_id IS NULL
 AND NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement)
 BEGIN SELECT RAISE(ABORT,'Task approval source is immutable'); END;

DROP TRIGGER approved_task_semantics_immutable;
CREATE TRIGGER approved_task_semantics_immutable BEFORE UPDATE OF project_id,feature_area_id,repository_id,module_id,codex_config_id,model,reasoning_effort,goal,constraints_json,base_commit,problem_statement,work_spec_json,verification_spec_json ON tasks
 WHEN OLD.review_approval_id IS NOT NULL AND (NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.module_id IS NOT OLD.module_id OR NEW.codex_config_id IS NOT OLD.codex_config_id OR NEW.model IS NOT OLD.model OR NEW.reasoning_effort IS NOT OLD.reasoning_effort OR NEW.goal IS NOT OLD.goal OR NEW.constraints_json IS NOT OLD.constraints_json OR NEW.base_commit IS NOT OLD.base_commit OR NEW.problem_statement IS NOT OLD.problem_statement OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.verification_spec_json IS NOT OLD.verification_spec_json)
 AND NOT (NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.model IS OLD.model AND NEW.reasoning_effort IS OLD.reasoning_effort AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement
  AND EXISTS(SELECT 1 FROM decision_resolution_receipts a JOIN execution_proposals predecessor ON predecessor.id=a.subject_id
   JOIN task_definition_revisions revision ON revision.id=NEW.current_definition_revision_id AND revision.task_id=NEW.id
   WHERE a.decision_kind='TASK_ACCEPTANCE' AND a.subject_kind='EXECUTION_PROPOSAL' AND (a.option='REQUEST_CHANGES' OR (a.option='RETRY_IMPLEMENTATION' AND EXISTS(SELECT 1 FROM task_acceptance_continuations c JOIN task_definition_revisions src ON src.id=c.source_revision_id
 WHERE c.decision_id=a.decision_id AND c.source_revision_id=OLD.current_definition_revision_id AND c.revision_id=NEW.current_definition_revision_id
 AND src.task_id=NEW.id AND json_extract(src.payload_json,'$.workSpec.implementationMode')='CODING_AGENT'
 AND json_remove(src.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')=json_remove(revision.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')))) AND a.result_kind='EXECUTION_PROPOSAL'
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
 AND NOT EXISTS(SELECT 1 FROM task_supplemental_review_current cur
 JOIN task_supplemental_review_requests req ON req.id=cur.request_id AND req.task_id=OLD.id AND req.project_id=OLD.project_id
 JOIN task_supplemental_review_starts started ON started.request_id=req.id AND started.review_id=OLD.current_review_id
 JOIN review_approval_decisions approved ON approved.review_id=started.review_id AND approved.task_id=OLD.id AND approved.project_id=OLD.project_id AND approved.decision_id=NEW.review_approval_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=approved.decision_id AND receipt.option='APPROVE' AND receipt.result_id=OLD.id AND receipt.project_id=OLD.project_id
 WHERE cur.task_id=OLD.id AND req.source_revision_id=OLD.current_definition_revision_id
 AND OLD.phase='REVIEW_APPROVAL_PENDING' AND NEW.phase=OLD.phase AND OLD.status='ACTIVE' AND NEW.status=OLD.status
 AND OLD.current_execution_id IS NULL AND NEW.current_execution_id IS NULL
 AND NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement)
 BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;

DROP TRIGGER approved_task_revision_immutable;
CREATE TRIGGER approved_task_revision_immutable BEFORE UPDATE OF current_definition_revision_id ON tasks
 WHEN EXISTS(SELECT 1 FROM review_approval_materializations m WHERE m.task_id=NEW.id AND m.initial_revision_id IS NOT NEW.current_definition_revision_id)
 AND NOT EXISTS(SELECT 1 FROM decision_resolution_receipts a JOIN execution_proposals predecessor ON predecessor.id=a.subject_id
  JOIN task_definition_revisions revision ON revision.id=NEW.current_definition_revision_id AND revision.task_id=NEW.id
  WHERE a.decision_kind='TASK_ACCEPTANCE' AND a.subject_kind='EXECUTION_PROPOSAL' AND (a.option='REQUEST_CHANGES' OR (a.option='RETRY_IMPLEMENTATION' AND EXISTS(SELECT 1 FROM task_acceptance_continuations c JOIN task_definition_revisions src ON src.id=c.source_revision_id
 WHERE c.decision_id=a.decision_id AND c.source_revision_id=OLD.current_definition_revision_id AND c.revision_id=NEW.current_definition_revision_id
 AND src.task_id=NEW.id AND json_extract(src.payload_json,'$.workSpec.implementationMode')='CODING_AGENT'
 AND json_remove(src.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')=json_remove(revision.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')))) AND a.result_kind='EXECUTION_PROPOSAL'
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
 AND NOT EXISTS(SELECT 1 FROM task_supplemental_review_current cur
 JOIN task_supplemental_review_requests req ON req.id=cur.request_id AND req.task_id=OLD.id AND req.project_id=OLD.project_id
 JOIN task_supplemental_review_starts started ON started.request_id=req.id AND started.review_id=OLD.current_review_id
 JOIN review_approval_decisions approved ON approved.review_id=started.review_id AND approved.task_id=OLD.id AND approved.project_id=OLD.project_id AND approved.decision_id=NEW.review_approval_id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=approved.decision_id AND receipt.option='APPROVE' AND receipt.result_id=OLD.id AND receipt.project_id=OLD.project_id
 WHERE cur.task_id=OLD.id AND req.source_revision_id=OLD.current_definition_revision_id
 AND OLD.phase='REVIEW_APPROVAL_PENDING' AND NEW.phase=OLD.phase AND OLD.status='ACTIVE' AND NEW.status=OLD.status
 AND OLD.current_execution_id IS NULL AND NEW.current_execution_id IS NULL
 AND NEW.project_id IS OLD.project_id AND NEW.feature_area_id IS OLD.feature_area_id AND NEW.repository_id IS OLD.repository_id AND NEW.module_id IS OLD.module_id AND NEW.codex_config_id IS OLD.codex_config_id AND NEW.base_commit IS OLD.base_commit AND NEW.problem_statement IS OLD.problem_statement
 AND EXISTS(SELECT 1 FROM task_definition_revisions dst WHERE dst.id=NEW.current_definition_revision_id AND dst.task_id=NEW.id
 AND json_extract(dst.payload_json,'$.reviewApprovalId')=NEW.review_approval_id
 AND json_extract(dst.payload_json,'$.workSpec')=json_remove(json(NEW.work_spec_json),'$.version')
 AND json_extract(dst.payload_json,'$.verificationSpec')=json_remove(json(NEW.verification_spec_json),'$.version')
 AND NEW.goal=json_extract(dst.payload_json,'$.workSpec.goal')
 AND json_extract(NEW.constraints_json,'$.items')=json_extract(dst.payload_json,'$.workSpec.constraints')))
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
  AND a.option IN ('REQUEST_CHANGES','REQUEST_REVERIFICATION','RETRY_IMPLEMENTATION') AND a.project_id=NEW.project_id AND predecessor.task_id=NEW.task_id AND predecessor.outcome IN ('SUCCEEDED','FAILED','CANCELED')
  AND t.current_definition_revision_id=NEW.task_definition_revision_id AND t.confirmed_task_plan_id=NEW.task_plan_id AND p.status='APPLIED'
  AND ((a.option='REQUEST_REVERIFICATION' AND NEW.task_definition_revision_id=predecessor.task_definition_revision_id AND NEW.task_plan_id=predecessor.task_plan_id)
    OR (a.option='REQUEST_CHANGES' AND NEW.task_definition_revision_id<>predecessor.task_definition_revision_id)
    OR (a.option='RETRY_IMPLEMENTATION' AND NEW.task_plan_id=predecessor.task_plan_id AND EXISTS(SELECT 1 FROM task_acceptance_continuations c
     JOIN task_definition_revisions src ON src.id=c.source_revision_id JOIN task_definition_revisions dst ON dst.id=c.revision_id
     WHERE c.decision_id=a.decision_id AND c.source_revision_id=predecessor.task_definition_revision_id AND c.revision_id=NEW.task_definition_revision_id
     AND src.task_id=NEW.task_id AND dst.task_id=NEW.task_id AND json_extract(src.payload_json,'$.workSpec.implementationMode')='CODING_AGENT'
     AND json_remove(src.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction')=json_remove(dst.payload_json,'$.workSpec.codexInstruction','$.verificationSpec.codexInstruction'))))))
)
AND NOT EXISTS(SELECT 1 FROM tasks t
 JOIN task_supplemental_review_current cur ON cur.task_id=t.id
 JOIN task_supplemental_review_requests req ON req.id=cur.request_id AND req.task_id=t.id AND req.project_id=t.project_id
 JOIN task_supplemental_review_starts started ON started.request_id=req.id AND started.review_id=t.current_review_id
 JOIN review_approval_decisions approved ON approved.review_id=started.review_id AND approved.decision_id=t.review_approval_id AND approved.task_id=t.id
 JOIN review_approval_materializations m ON m.decision_id=approved.decision_id AND m.task_id=t.id
 JOIN decision_resolution_receipts receipt ON receipt.decision_id=m.decision_id AND receipt.option='APPROVE' AND receipt.project_id=t.project_id
 JOIN task_plans p ON p.id=NEW.task_plan_id AND p.task_id=t.id AND p.project_id=t.project_id
 WHERE t.id=NEW.task_id AND NEW.project_id=t.project_id AND t.status='ACTIVE' AND t.phase='IMPLEMENTATION_READY' AND t.current_execution_id IS NULL
 AND NEW.status='CONFIRMED' AND NEW.outcome='PENDING' AND NEW.run_id IS NULL AND NEW.verification_run_id IS NULL
 AND NEW.task_definition_revision_id=t.current_definition_revision_id
 AND (NEW.task_definition_revision_id=m.initial_revision_id OR EXISTS(SELECT 1 FROM execution_instruction_sources ins WHERE ins.revision_id=NEW.task_definition_revision_id AND ins.source_revision_id=m.initial_revision_id AND ins.decision_id=m.decision_id AND ins.task_id=t.id AND ins.source_kind='EXECUTION_START'))
 AND t.confirmed_task_plan_id=p.id AND p.status='APPLIED'
 AND NOT EXISTS(SELECT 1 FROM execution_proposals old WHERE old.task_id=t.id AND old.outcome NOT IN ('SUCCEEDED','FAILED','CANCELED') AND NOT EXISTS(SELECT 1 FROM task_review_superseded_executions superseded WHERE superseded.execution_id=old.id)))
BEGIN SELECT RAISE(ABORT,'EXECUTION_PREREQUISITE_NOT_SATISFIED'); END;

`)
	if err != nil {
		return err
	}
	var guard string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='task_current_execution_guard'`).Scan(&guard); err != nil {
		return err
	}
	old := "(OLD.current_execution_id IS NOT NULL AND NEW.current_execution_id IS NULL)"
	replacement := `(OLD.current_execution_id IS NOT NULL AND NEW.current_execution_id IS NULL AND NOT EXISTS(SELECT 1 FROM task_supplemental_review_current c JOIN task_supplemental_review_requests q ON q.id=c.request_id JOIN task_review_superseded_executions x ON x.request_id=q.id AND x.execution_id=OLD.current_execution_id WHERE c.task_id=OLD.id AND q.task_id=OLD.id AND q.project_id=OLD.project_id AND q.feature_area_id=OLD.feature_area_id AND q.source_execution_id=OLD.current_execution_id AND q.source_revision_id=OLD.current_definition_revision_id AND q.source_review_id=OLD.current_review_id AND q.source_phase=OLD.phase AND NEW.phase='REVIEW_REQUIRED' AND NEW.status=OLD.status AND OLD.status='ACTIVE' AND NEW.current_definition_revision_id=OLD.current_definition_revision_id AND NEW.current_review_id=OLD.current_review_id))`
	if !strings.Contains(guard, old) {
		return fmt.Errorf("unexpected current Execution guard")
	}
	guard = strings.Replace(guard, old, replacement, 1)
	guard = strings.ReplaceAll(guard, "r.option IN ('REQUEST_CHANGES','REQUEST_REVERIFICATION')", "r.option IN ('REQUEST_CHANGES','REQUEST_REVERIFICATION','RETRY_IMPLEMENTATION')")
	if _, err = tx.ExecContext(ctx, `DROP TRIGGER task_current_execution_guard`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, guard)
	return err
}

// The existing migration pattern retains every unrelated trigger/index while
// removing only the obsolete one-approval-per-Task uniqueness constraint.
func rebuildSupplementalMaterializationsV55(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `PRAGMA legacy_alter_table=ON`); err != nil {
		return err
	}
	defer tx.ExecContext(context.WithoutCancel(ctx), `PRAGMA legacy_alter_table=OFF`)
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='review_approval_materializations'`).Scan(&ddl); err != nil {
		return err
	}
	old := "task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id)"
	if !strings.Contains(ddl, old) {
		return fmt.Errorf("unexpected approval materialization schema")
	}
	ddl = strings.Replace(ddl, old, "task_id TEXT NOT NULL REFERENCES tasks(id)", 1)
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name='review_approval_materializations' AND sql IS NOT NULL)`)
	if err != nil {
		return err
	}
	var retained []schemaObject
	for rows.Next() {
		var o schemaObject
		if err = rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			rows.Close()
			return err
		}
		retained = append(retained, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range retained {
		if _, err = tx.ExecContext(ctx, "DROP "+strings.ToUpper(o.kind)+" "+sqlIdentifier(o.name)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `ALTER TABLE review_approval_materializations RENAME TO _source54_approval_materializations`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_approval_materializations(decision_id,task_id,initial_revision_id,ai_session_id,created_at,runtime_config_json) SELECT decision_id,task_id,initial_revision_id,ai_session_id,created_at,runtime_config_json FROM _source54_approval_materializations`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE _source54_approval_materializations`); err != nil {
		return err
	}
	for _, o := range retained {
		if _, err = tx.ExecContext(ctx, o.sql); err != nil {
			return err
		}
	}
	return nil
}
