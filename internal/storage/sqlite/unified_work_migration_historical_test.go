// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Rebuild only the CHECK/nullability contracts that change. Preserve the actual
// column order, data, indexes and triggers of every supported predecessor.
// All DDL, backfill and foreign-key validation share the migration transaction.
func migrateUnifiedWork(ctx context.Context, tx *sql.Tx) error {
	type object struct{ kind, name, ddl string }
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE type='trigger' OR (type='index' AND tbl_name IN ('tasks','runs','task_preparations','execution_cancellations') AND sql IS NOT NULL) ORDER BY type,name`)
	if err != nil {
		return err
	}
	var objects []object
	for rows.Next() {
		var v object
		if err = rows.Scan(&v.kind, &v.name, &v.ddl); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, v)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, v := range objects {
		if _, err = tx.ExecContext(ctx, `DROP `+strings.ToUpper(v.kind)+` "`+v.name+`"`); err != nil {
			return err
		}
	}
	for _, table := range []string{"tasks", "runs", "task_preparations", "execution_cancellations"} {
		var ddl string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			return err
		}
		body := ddl[strings.Index(ddl, "("):]
		switch table {
		case "tasks":
			body = strings.ReplaceAll(body, "module_id TEXT NOT NULL", "module_id TEXT")
			body = strings.ReplaceAll(body, "repository_id TEXT NOT NULL", "repository_id TEXT")
			body = strings.ReplaceAll(body, "codex_config_id TEXT NOT NULL", "codex_config_id TEXT")
		case "runs":
			body = strings.ReplaceAll(body, "'SCOPE_REVIEW','IMPLEMENTATION','VERIFICATION'", "'SCOPE_REVIEW','IMPLEMENTATION','VERIFICATION','INSPECTION'")
			body = strings.ReplaceAll(body, "'REVIEW','PREPARATION','EXECUTION'", "'REVIEW','PREPARATION','EXECUTION','INSPECTION'")
		case "task_preparations":
			body = strings.ReplaceAll(body, "'DRAFT','PREPARING','READY','CONFLICT','FAILED'", "'DRAFT','PREPARING','READY','CONFLICT','FAILED','CANCELED'")
		case "execution_cancellations":
			body = strings.ReplaceAll(body, "execution_id TEXT NOT NULL", "execution_id TEXT")
			body = strings.ReplaceAll(body, "run_id TEXT NOT NULL", "run_id TEXT")
		}
		if _, err = tx.ExecContext(ctx, `CREATE TABLE "`+table+`_v36" `+body+`; INSERT INTO "`+table+`_v36" SELECT * FROM "`+table+`"; DROP TABLE "`+table+`"; ALTER TABLE "`+table+`_v36" RENAME TO "`+table+`";`); err != nil {
			return fmt.Errorf("rebuild %s: %w", table, err)
		}
	}
	for _, v := range objects {
		if _, err = tx.ExecContext(ctx, v.ddl); err != nil {
			return fmt.Errorf("restore %s: %w", v.name, err)
		}
	}
	_, err = tx.ExecContext(ctx, unifiedWorkSchema)
	return err
}

const unifiedWorkSchema = `
ALTER TABLE tasks ADD COLUMN task_type TEXT NOT NULL DEFAULT 'DEVELOPMENT' CHECK(task_type IN ('DEVELOPMENT','REVIEW','INSPECTION'));
ALTER TABLE tasks ADD COLUMN task_scope TEXT NOT NULL DEFAULT 'FEATURE_AREA' CHECK(task_scope IN ('FEATURE_AREA','PROJECT'));
ALTER TABLE tasks ADD COLUMN work_owner_id TEXT;
ALTER TABLE tasks ADD COLUMN work_legacy_envelope INTEGER NOT NULL DEFAULT 0 CHECK(work_legacy_envelope IN (0,1));
ALTER TABLE reviews ADD COLUMN task_id TEXT REFERENCES tasks(id);
ALTER TABLE context_probes ADD COLUMN task_id TEXT REFERENCES tasks(id);
ALTER TABLE context_probes ADD COLUMN run_id TEXT REFERENCES runs(id);
ALTER TABLE context_probes ADD COLUMN runtime_epoch TEXT REFERENCES runtime_epochs(id);
ALTER TABLE runs ADD COLUMN context_probe_id TEXT REFERENCES context_probes(id);
ALTER TABLE execution_cancellations ADD COLUMN owner_type TEXT NOT NULL DEFAULT 'EXECUTION' CHECK(owner_type IN ('REVIEW','PREPARATION','EXECUTION','TASK'));
ALTER TABLE execution_cancellations ADD COLUMN owner_id TEXT;
ALTER TABLE execution_cancellations ADD COLUMN task_id TEXT REFERENCES tasks(id);
ALTER TABLE execution_cancellations ADD COLUMN backend_disposition TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(backend_disposition IN ('ACTIVE','TERMINATED','NO_BACKEND','UNKNOWN_ORPHANED'));
UPDATE execution_cancellations SET owner_id=execution_id,task_id=(SELECT task_id FROM execution_proposals WHERE id=execution_id);
CREATE UNIQUE INDEX tasks_work_owner ON tasks(task_type,work_owner_id) WHERE work_owner_id IS NOT NULL;
CREATE UNIQUE INDEX reviews_work_task ON reviews(task_id) WHERE task_id IS NOT NULL;
CREATE UNIQUE INDEX probes_work_task ON context_probes(task_id) WHERE task_id IS NOT NULL;
CREATE UNIQUE INDEX runs_context_probe ON runs(context_probe_id) WHERE context_probe_id IS NOT NULL;
CREATE UNIQUE INDEX cancellations_active_owner ON execution_cancellations(owner_type,owner_id) WHERE state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED');

CREATE TABLE agent_orphan_dispositions (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 owner_type TEXT NOT NULL, owner_id TEXT NOT NULL,
 conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 original_runtime_epoch TEXT, observed_runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 thread_id TEXT, turn_id TEXT, workspace_id TEXT REFERENCES workspaces(id),
 request_id TEXT NOT NULL, source TEXT NOT NULL CHECK(source IN ('CANCELLATION','RECOVERY')),
 disposition TEXT NOT NULL DEFAULT 'UNKNOWN_ORPHANED' CHECK(disposition='UNKNOWN_ORPHANED'),
 created_at TEXT NOT NULL
);
CREATE TRIGGER orphan_disposition_immutable BEFORE UPDATE ON agent_orphan_dispositions BEGIN SELECT RAISE(ABORT,'orphan disposition is permanent history'); END;
CREATE TRIGGER orphan_disposition_no_delete BEFORE DELETE ON agent_orphan_dispositions BEGIN SELECT RAISE(ABORT,'orphan disposition is permanent history'); END;

-- Deterministic envelopes. No Development revisions, plans or receipts.
DROP TRIGGER tasks_current_insert;
DROP TRIGGER runs_operation_owner_immutable;
INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,problem_statement,feature_area_id,task_type,task_scope,work_owner_id,model,reasoning_effort)
 SELECT 'work_'||v.id,v.project_id,(SELECT id FROM modules WHERE project_id=v.project_id ORDER BY system_owned DESC,id LIMIT 1),v.repository_id,v.codex_config_id,v.engineering_goal,'{"version":1,"items":[]}',COALESCE(v.base_commit,''),
 CASE WHEN r.status='SUCCEEDED' THEN 'SUCCEEDED' WHEN r.status='CANCELED' THEN 'CANCELED' WHEN r.status IN ('FAILED','INFRASTRUCTURE_FAILED') OR v.start_failure_code IS NOT NULL THEN 'FAILED' WHEN r.id IS NOT NULL THEN 'RUNNING' ELSE 'PLANNING' END,
 v.created_at,COALESCE(r.updated_at,v.created_at),v.engineering_goal,v.feature_area_id,'REVIEW','FEATURE_AREA',v.id,v.model,v.reasoning_effort
 FROM reviews v LEFT JOIN runs r ON r.id=v.linked_run_id;
UPDATE reviews SET task_id='work_'||id WHERE EXISTS(SELECT 1 FROM tasks WHERE id='work_'||reviews.id);
UPDATE runs SET task_id=(SELECT task_id FROM reviews WHERE id=review_id) WHERE task_id IS NULL AND review_id IS NOT NULL AND EXISTS(SELECT 1 FROM reviews WHERE id=review_id AND task_id IS NOT NULL);
INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,problem_statement,task_type,task_scope,work_owner_id)
 SELECT 'work_'||id,project_id,(SELECT id FROM modules WHERE project_id=context_probes.project_id ORDER BY system_owned DESC,id LIMIT 1),repository_id,codex_config_id,codex_instruction,'{"version":1,"items":[]}','',
 CASE status WHEN 'CREATED' THEN 'PLANNING' WHEN 'NO_GUIDANCE' THEN 'SUCCEEDED' ELSE status END,created_at,updated_at,codex_instruction,'INSPECTION','PROJECT',id FROM context_probes;
UPDATE context_probes SET task_id='work_'||id;
UPDATE tasks SET work_legacy_envelope=1 WHERE task_type IN ('REVIEW','INSPECTION');
INSERT INTO agent_orphan_dispositions(run_id,task_id,feature_area_id,owner_type,owner_id,conversation_id,original_runtime_epoch,observed_runtime_epoch,thread_id,turn_id,workspace_id,request_id,source,created_at)
 SELECT r.id,r.task_id,r.feature_area_id,r.operation_kind,r.operation_id,k.old_conversation_id,r.runtime_epoch,k.authorized_epoch,r.thread_id,r.turn_id,r.workspace_id,k.request_id,'RECOVERY',k.created_at
 FROM agent_operation_recoveries k JOIN runs r ON r.id=k.run_id JOIN tasks t ON t.id=r.task_id JOIN agent_operation_boundaries b ON b.run_id=r.id JOIN agent_conversations a ON a.id=k.old_conversation_id
 WHERE b.advance_state='UNKNOWN' AND a.ended_at IS NOT NULL AND t.status IN ('FAILED','CANCELED','SUCCEEDED') AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE run_id=r.id)
 AND (r.workspace_id IS NULL OR EXISTS(SELECT 1 FROM workspaces w WHERE w.id=r.workspace_id AND w.runtime_quarantined=1));

UPDATE execution_cancellations SET backend_disposition='UNKNOWN_ORPHANED' WHERE EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=execution_cancellations.run_id);
UPDATE execution_cancellations SET backend_disposition='TERMINATED' WHERE backend_disposition='ACTIVE' AND state IN ('CANCELED','CANCEL_NOT_APPLIED','ALREADY_TERMINAL') AND EXISTS(SELECT 1 FROM agent_operation_boundaries WHERE run_id=execution_cancellations.run_id AND advance_state<>'UNKNOWN' AND quiesced=1);

CREATE TRIGGER tasks_current_insert BEFORE INSERT ON tasks
WHEN NEW.legacy_read_only<>0 OR NEW.agent_context IS NOT NULL OR NOT (
 (NEW.task_type='INSPECTION' AND NEW.task_scope='PROJECT' AND NEW.feature_area_id IS NULL AND NEW.work_owner_id IS NOT NULL)
 OR (NEW.task_scope='FEATURE_AREA' AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=NEW.feature_area_id AND f.project_id=NEW.project_id AND f.legacy_read_only=0 AND
 ((NEW.task_type='DEVELOPMENT' AND f.status='ACTIVE' AND NEW.work_owner_id IS NULL AND NEW.module_id IS NOT NULL AND NEW.repository_id IS NOT NULL AND NEW.codex_config_id IS NOT NULL) OR (NEW.task_type='REVIEW' AND f.status IN ('DISCOVERING','ACTIVE') AND NEW.work_owner_id IS NOT NULL)))))
BEGIN SELECT RAISE(ABORT,'Task requires exact type, scope and owner'); END;
CREATE TRIGGER task_work_identity_immutable BEFORE UPDATE OF task_type,task_scope,work_owner_id ON tasks
WHEN NEW.task_type IS NOT OLD.task_type OR NEW.task_scope IS NOT OLD.task_scope OR NEW.work_owner_id IS NOT OLD.work_owner_id
BEGIN SELECT RAISE(ABORT,'Task work ownership is immutable'); END;
CREATE TRIGGER task_revision_development_only BEFORE INSERT ON task_definition_revisions WHEN NOT EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND task_type='DEVELOPMENT' AND status<>'CANCELED')
BEGIN SELECT RAISE(ABORT,'only Development has definition revisions'); END;
CREATE TRIGGER review_work_binding_immutable BEFORE UPDATE OF task_id ON reviews WHEN NEW.task_id IS NOT OLD.task_id BEGIN SELECT RAISE(ABORT,'Review Task is immutable'); END;
CREATE TRIGGER probe_work_binding_immutable BEFORE UPDATE OF task_id,run_id,runtime_epoch ON context_probes WHEN NEW.task_id IS NOT OLD.task_id OR (OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id) OR (OLD.runtime_epoch IS NOT NULL AND NEW.runtime_epoch IS NOT OLD.runtime_epoch) BEGIN SELECT RAISE(ABORT,'Inspection identity is immutable'); END;
CREATE TRIGGER runs_operation_owner_immutable BEFORE UPDATE OF project_id,feature_area_id,operation_kind,operation_id,task_id,review_id,task_preparation_id,context_probe_id ON runs
WHEN NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.operation_kind IS NOT OLD.operation_kind OR NEW.operation_id IS NOT OLD.operation_id OR NEW.task_id IS NOT OLD.task_id OR NEW.review_id IS NOT OLD.review_id OR NEW.task_preparation_id IS NOT OLD.task_preparation_id OR NEW.context_probe_id IS NOT OLD.context_probe_id
BEGIN SELECT RAISE(ABORT,'Run operation owner is immutable'); END;
DROP TRIGGER runs_operation_owner_insert;
CREATE TRIGGER runs_operation_owner_insert BEFORE INSERT ON runs
WHEN NOT EXISTS(SELECT 1 FROM tasks t WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.legacy_read_only=0 AND t.status<>'CANCELED' AND
 ((NEW.operation_kind='INSPECTION' AND NEW.purpose='INSPECTION' AND NEW.feature_area_id IS NULL AND t.task_type='INSPECTION' AND t.task_scope='PROJECT' AND t.work_owner_id=NEW.context_probe_id AND NEW.operation_id=NEW.context_probe_id AND EXISTS(SELECT 1 FROM context_probes p WHERE p.id=NEW.context_probe_id AND p.task_id=t.id AND p.status='RUNNING'))
 OR (t.task_scope='FEATURE_AREA' AND t.feature_area_id=NEW.feature_area_id AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=t.feature_area_id AND f.legacy_read_only=0 AND ((NEW.operation_kind='REVIEW' AND f.status IN ('DISCOVERING','ACTIVE')) OR (NEW.operation_kind IN ('PREPARATION','EXECUTION') AND f.status='ACTIVE'))) AND
 ((NEW.operation_kind='REVIEW' AND t.task_type='REVIEW' AND t.work_owner_id=NEW.review_id AND NEW.operation_id=NEW.review_id AND NEW.purpose='SCOPE_REVIEW' AND NEW.execution_proposal_id IS NULL AND NEW.task_preparation_id IS NULL AND EXISTS(SELECT 1 FROM reviews v WHERE v.id=NEW.review_id AND v.task_id=t.id))
 OR (NEW.operation_kind='PREPARATION' AND t.task_type='DEVELOPMENT' AND NEW.operation_id=NEW.task_preparation_id AND NEW.review_id IS NULL AND NEW.execution_proposal_id IS NULL AND NEW.purpose='SCOPE_REVIEW' AND EXISTS(SELECT 1 FROM task_preparations p WHERE p.id=NEW.task_preparation_id AND p.task_id=t.id AND p.task_definition_revision_id IS NOT NULL))
 OR (NEW.operation_kind='EXECUTION' AND t.task_type='DEVELOPMENT' AND NEW.operation_id=NEW.execution_proposal_id AND NEW.review_id IS NULL AND NEW.task_preparation_id IS NULL AND NEW.purpose IN ('IMPLEMENTATION','VERIFICATION') AND EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=NEW.execution_proposal_id AND e.task_id=t.id AND e.outcome IN ('PENDING','RUNNING') AND e.task_definition_revision_id IS NOT NULL)))))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')))
BEGIN SELECT RAISE(ABORT,'Agent Run requires durable visible Task and exact work owner'); END;

DROP TRIGGER task_preparations_status_transition;
CREATE TRIGGER task_preparations_status_transition BEFORE UPDATE OF status ON task_preparations
WHEN NEW.status<>OLD.status AND NOT ((OLD.status='DRAFT' AND NEW.status IN ('PREPARING','CANCELED')) OR (OLD.status='PREPARING' AND NEW.status IN ('READY','CONFLICT','FAILED','CANCELED')))
BEGIN SELECT RAISE(ABORT,'invalid Preparation transition'); END;
CREATE TRIGGER preparation_canceled_immutable BEFORE UPDATE ON task_preparations WHEN OLD.status='CANCELED' BEGIN SELECT RAISE(ABORT,'canceled Preparation is terminal'); END;
DROP TRIGGER execution_proposals_outcome_transition;
CREATE TRIGGER execution_proposals_outcome_transition BEFORE UPDATE OF outcome ON execution_proposals
WHEN NEW.outcome<>OLD.outcome AND NOT ((OLD.outcome='PENDING' AND ((NEW.outcome='RUNNING' AND OLD.status='CONFIRMED' AND NEW.status='CONSUMED') OR NEW.outcome='CANCELED')) OR (OLD.outcome='RUNNING' AND NEW.outcome IN ('SUCCEEDED','FAILED','CANCELED')))
BEGIN SELECT RAISE(ABORT,'invalid Execution outcome transition'); END;
CREATE TRIGGER canceled_execution_authority BEFORE UPDATE ON execution_proposals WHEN OLD.outcome='CANCELED' AND (NEW.status IS NOT OLD.status OR NEW.outcome IS NOT OLD.outcome OR NEW.run_id IS NOT OLD.run_id OR NEW.verification_run_id IS NOT OLD.verification_run_id)
BEGIN SELECT RAISE(ABORT,'canceled Execution cannot advance'); END;
CREATE TRIGGER cancellation_owner_immutable BEFORE UPDATE OF owner_type,owner_id,task_id ON execution_cancellations WHEN NEW.owner_type IS NOT OLD.owner_type OR NEW.owner_id IS NOT OLD.owner_id OR NEW.task_id IS NOT OLD.task_id BEGIN SELECT RAISE(ABORT,'cancellation owner is immutable'); END;

CREATE TRIGGER runs_orphan_fence BEFORE UPDATE ON runs WHEN EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=OLD.id) BEGIN SELECT RAISE(ABORT,'orphaned Run authority permanently revoked'); END;
CREATE TRIGGER claims_orphan_fence BEFORE INSERT ON feature_area_agent_claims WHEN EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=NEW.run_id) BEGIN SELECT RAISE(ABORT,'orphaned operation cannot reclaim authority'); END;
CREATE TRIGGER claims_orphan_transfer_fence BEFORE UPDATE ON feature_area_agent_claims WHEN EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=NEW.run_id) BEGIN SELECT RAISE(ABORT,'orphaned operation cannot reclaim authority'); END;

-- Product synchronization belongs to the durable terminal transaction, including
-- startup reconciliation. Review remains its own result/evidence owner.
CREATE TRIGGER review_task_run_state AFTER UPDATE OF status ON runs WHEN NEW.operation_kind='REVIEW'
BEGIN UPDATE tasks SET status=CASE WHEN NEW.status='SUCCEEDED' THEN 'SUCCEEDED' WHEN NEW.status='CANCELED' THEN 'CANCELED' WHEN NEW.status IN ('FAILED','INFRASTRUCTURE_FAILED') THEN 'FAILED' ELSE 'RUNNING' END,updated_at=NEW.updated_at WHERE id=NEW.task_id; END;
CREATE TRIGGER inspection_task_state AFTER UPDATE OF status ON context_probes
BEGIN UPDATE tasks SET status=CASE NEW.status WHEN 'CREATED' THEN 'PLANNING' WHEN 'NO_GUIDANCE' THEN 'SUCCEEDED' ELSE NEW.status END,updated_at=NEW.updated_at WHERE id=NEW.task_id; END;

CREATE TRIGGER task_canceled_authority BEFORE UPDATE ON tasks
WHEN OLD.status='CANCELED' AND (NEW.status IS NOT OLD.status OR NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id OR NEW.work_spec_json IS NOT OLD.work_spec_json)
BEGIN SELECT RAISE(ABORT,'canceled Task has no workflow authority'); END;
CREATE TRIGGER task_decision_owner_guard BEFORE UPDATE OF status ON task_plans
WHEN NEW.status IN ('CONFIRMED','APPLIED') AND EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND status='CANCELED')
BEGIN SELECT RAISE(ABORT,'canceled Task decision is not actionable'); END;
CREATE TRIGGER cancellation_task_mutation_guard BEFORE UPDATE ON tasks
WHEN EXISTS(SELECT 1 FROM execution_cancellations WHERE task_id=OLD.id AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) AND (NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.confirmed_task_plan_id IS NOT OLD.confirmed_task_plan_id)
BEGIN SELECT RAISE(ABORT,'cancellation closes Task development authority'); END;

DROP TRIGGER feature_areas_decompose_guard;
CREATE TRIGGER feature_areas_decompose_guard BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status='DECOMPOSED' AND (EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=NEW.id AND legacy_read_only=0 AND task_type='DEVELOPMENT') OR NOT EXISTS(SELECT 1 FROM feature_area_relations WHERE source_feature_area_id=NEW.id))
BEGIN SELECT RAISE(ABORT,'FeatureArea decomposition requires children and zero Development Tasks'); END;
CREATE TRIGGER review_start_failure_task AFTER UPDATE OF start_failure_code ON reviews WHEN NEW.start_failure_code IS NOT NULL
BEGIN UPDATE tasks SET status='FAILED',updated_at=NEW.start_failure_at WHERE id=NEW.task_id AND status='PLANNING'; END;
CREATE TRIGGER preparation_task_terminal_guard BEFORE INSERT ON task_preparations
WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND status='CANCELED') OR EXISTS(SELECT 1 FROM execution_cancellations WHERE task_id=NEW.task_id AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
BEGIN SELECT RAISE(ABORT,'Task cancellation prevents Preparation admission'); END;
CREATE TRIGGER execution_task_terminal_guard BEFORE INSERT ON execution_proposals
WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND status='CANCELED') OR EXISTS(SELECT 1 FROM execution_cancellations WHERE task_id=NEW.task_id AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
BEGIN SELECT RAISE(ABORT,'Task cancellation prevents Execution admission'); END;
CREATE TRIGGER orphan_exact_record BEFORE INSERT ON agent_orphan_dispositions
WHEN NOT EXISTS(SELECT 1 FROM runs r JOIN tasks t ON t.id=r.task_id JOIN agent_operation_boundaries b ON b.run_id=r.id JOIN agent_conversations a ON a.id=b.agent_conversation_id JOIN feature_area_agent_claims c ON c.run_id=r.id
 WHERE r.id=NEW.run_id AND r.task_id=NEW.task_id AND r.feature_area_id=NEW.feature_area_id AND r.operation_kind=NEW.owner_type AND r.operation_id=NEW.owner_id
 AND r.runtime_epoch IS NEW.original_runtime_epoch AND NEW.observed_runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND r.thread_id IS NEW.thread_id AND r.turn_id IS NEW.turn_id AND r.workspace_id IS NEW.workspace_id
 AND a.id=NEW.conversation_id AND a.ended_at IS NOT NULL AND b.advance_state='UNKNOWN' AND t.status IN ('CANCELED','SUCCEEDED','FAILED')
 AND c.runtime_epoch IS r.runtime_epoch AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id
 AND (r.workspace_id IS NULL OR EXISTS(SELECT 1 FROM workspaces w WHERE w.id=r.workspace_id AND w.runtime_quarantined=1)))
BEGIN SELECT RAISE(ABORT,'orphan disposition requires exact fenced and quarantined owner'); END;
`
