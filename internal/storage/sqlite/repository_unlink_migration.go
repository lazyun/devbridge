package sqlite

import (
	"context"
	"database/sql"
)

// v84 adds the one-way availability lifecycle to the historical Repository
// identity and a Project-level topology authority. Neither is membership.
func migrateRepositoryUnlinkV84(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
ALTER TABLE repositories ADD COLUMN registration_state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(registration_state IN ('ACTIVE','UNLINKED'));
ALTER TABLE repositories ADD COLUMN registration_generation INTEGER NOT NULL DEFAULT 1 CHECK(registration_generation>0);
ALTER TABLE repositories ADD COLUMN unlinked_at TEXT;
ALTER TABLE repositories ADD COLUMN unlink_request_id TEXT;
ALTER TABLE repositories ADD COLUMN unlink_reason TEXT NOT NULL DEFAULT '';
CREATE TRIGGER repositories_registration_identity_immutable_v84 BEFORE UPDATE OF id,project_id,canonical_path,name ON repositories
BEGIN SELECT RAISE(ABORT,'Repository historical identity is immutable'); END;
CREATE TRIGGER repositories_registration_transition_v84 BEFORE UPDATE OF registration_state,registration_generation,unlinked_at,unlink_request_id,unlink_reason ON repositories
WHEN NOT (OLD.registration_state='ACTIVE' AND NEW.registration_state='UNLINKED' AND NEW.registration_generation=OLD.registration_generation+1 AND NEW.unlinked_at IS NOT NULL AND NEW.unlink_request_id<>'')
BEGIN SELECT RAISE(ABORT,'Repository registration permits only ACTIVE to UNLINKED'); END;
CREATE TABLE project_repository_topologies(project_id TEXT PRIMARY KEY REFERENCES projects(id),revision INTEGER NOT NULL CHECK(revision>0),updated_at TEXT NOT NULL);
INSERT INTO project_repository_topologies(project_id,revision,updated_at) SELECT id,1,updated_at FROM projects;
CREATE TRIGGER project_repository_topology_create_v84 AFTER INSERT ON projects
BEGIN INSERT INTO project_repository_topologies(project_id,revision,updated_at) VALUES(NEW.id,1,NEW.updated_at); END;
CREATE TRIGGER project_repository_topology_repository_insert_v84 AFTER INSERT ON repositories
BEGIN UPDATE project_repository_topologies SET revision=revision+1,updated_at=NEW.updated_at WHERE project_id=NEW.project_id; END;
CREATE TRIGGER project_repository_topology_no_delete_v84 BEFORE DELETE ON project_repository_topologies BEGIN SELECT RAISE(ABORT,'Project repository topology is durable'); END;
CREATE TRIGGER repository_active_task_insert_v84 BEFORE INSERT ON tasks
WHEN NEW.repository_id IS NOT NULL AND EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.project_id=NEW.project_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_definition_insert_v84 BEFORE INSERT ON task_definition_revisions
WHEN EXISTS(SELECT 1 FROM repositories r WHERE r.id=json_extract(NEW.payload_json,'$.repositoryId') AND r.registration_state='UNLINKED')
 OR EXISTS(SELECT 1 FROM json_each(NEW.payload_json,'$.repositoryTargets') x JOIN repositories r ON r.id=json_extract(x.value,'$.repositoryId') WHERE r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_workspace_insert_v84 BEFORE INSERT ON workspaces
WHEN NEW.repository_id IS NOT NULL AND EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.project_id=NEW.project_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_workspace_member_insert_v84 BEFORE INSERT ON workspace_members
WHEN EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_review_insert_v84 BEFORE INSERT ON reviews
WHEN (NEW.repository_id IS NOT NULL AND EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.project_id=NEW.project_id AND r.registration_state='UNLINKED'))
 OR EXISTS(SELECT 1 FROM json_each(NEW.repository_targets_json) x JOIN repositories r ON r.id=json_extract(x.value,'$.repositoryId') WHERE r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_run_binding_insert_v84 BEFORE INSERT ON run_repository_bindings
WHEN EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_direct_file_insert_v84 BEFORE INSERT ON direct_file_task_authorizations
WHEN NEW.repository_id IS NOT NULL AND EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.project_id=NEW.project_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_direct_file_member_insert_v84 BEFORE INSERT ON direct_file_task_members
WHEN EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_task_commit_member_insert_v84 BEFORE INSERT ON task_commit_members
WHEN EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_area_commit_insert_v84 BEFORE INSERT ON feature_area_commit_receipts
WHEN NEW.repository_id IS NOT NULL AND EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TRIGGER repository_active_area_commit_member_insert_v84 BEFORE INSERT ON feature_area_commit_members
WHEN EXISTS(SELECT 1 FROM repositories r WHERE r.id=NEW.repository_id AND r.registration_state='UNLINKED')
BEGIN SELECT RAISE(ABORT,'REPOSITORY_UNLINKED'); END;
CREATE TABLE project_repository_unlink_receipts(
 request_id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id),repository_id TEXT NOT NULL REFERENCES repositories(id),
 expected_registration_generation INTEGER NOT NULL CHECK(expected_registration_generation>0),expected_topology_revision INTEGER NOT NULL CHECK(expected_topology_revision>0),payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),reason TEXT NOT NULL,result_json TEXT NOT NULL CHECK(json_valid(result_json)),created_at TEXT NOT NULL,completed_at TEXT NOT NULL,resulting_registration_generation INTEGER NOT NULL CHECK(resulting_registration_generation>0),resulting_topology_revision INTEGER NOT NULL CHECK(resulting_topology_revision>0),result_state TEXT NOT NULL CHECK(result_state='UNLINKED')
);
CREATE TRIGGER project_repository_unlink_receipts_immutable_v84 BEFORE UPDATE ON project_repository_unlink_receipts BEGIN SELECT RAISE(ABORT,'Repository unlink receipt is immutable'); END;
CREATE TRIGGER project_repository_unlink_receipts_no_delete_v84 BEFORE DELETE ON project_repository_unlink_receipts BEGIN SELECT RAISE(ABORT,'Repository unlink receipt is durable'); END;`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
CREATE TABLE development_cycles_v84 (id TEXT PRIMARY KEY,project_id TEXT NOT NULL REFERENCES projects(id),repository_id TEXT NOT NULL REFERENCES repositories(id),development_ref TEXT NOT NULL CHECK(development_ref LIKE 'refs/heads/%' AND length(development_ref)>11),selected_head_sha TEXT NOT NULL,created_at TEXT NOT NULL,superseded_at TEXT,end_reason TEXT CHECK(end_reason IS NULL OR end_reason IN ('BRANCH_RESELECTED','REPOSITORY_UNLINKED')),request_id TEXT NOT NULL UNIQUE CHECK(length(request_id)>0 AND length(request_id)<=128),selected_by_ai_session_id TEXT REFERENCES ai_sessions(id),CHECK((superseded_at IS NULL AND end_reason IS NULL) OR (superseded_at IS NOT NULL AND end_reason IN ('BRANCH_RESELECTED','REPOSITORY_UNLINKED'))));
INSERT INTO development_cycles_v84 SELECT id,project_id,repository_id,development_ref,selected_head_sha,created_at,superseded_at,end_reason,request_id,selected_by_ai_session_id FROM development_cycles;
DROP TABLE development_cycles; ALTER TABLE development_cycles_v84 RENAME TO development_cycles;
CREATE UNIQUE INDEX development_cycles_one_current ON development_cycles(project_id,repository_id) WHERE superseded_at IS NULL;
CREATE TRIGGER development_cycle_project BEFORE INSERT ON development_cycles WHEN NOT EXISTS(SELECT 1 FROM repositories WHERE id=NEW.repository_id AND project_id=NEW.project_id AND registration_state='ACTIVE') BEGIN SELECT RAISE(ABORT,'REPOSITORY_PROJECT_MISMATCH'); END;
CREATE TRIGGER development_cycle_session_project BEFORE INSERT ON development_cycles WHEN NEW.selected_by_ai_session_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM ai_sessions WHERE id=NEW.selected_by_ai_session_id AND project_id=NEW.project_id) BEGIN SELECT RAISE(ABORT,'REPOSITORY_PROJECT_MISMATCH'); END;
CREATE TRIGGER development_cycle_immutable BEFORE UPDATE ON development_cycles WHEN OLD.superseded_at IS NOT NULL OR NEW.superseded_at IS NULL OR NEW.end_reason NOT IN ('BRANCH_RESELECTED','REPOSITORY_UNLINKED') OR NEW.id IS NOT OLD.id OR NEW.project_id IS NOT OLD.project_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.development_ref IS NOT OLD.development_ref OR NEW.selected_head_sha IS NOT OLD.selected_head_sha OR NEW.created_at IS NOT OLD.created_at OR NEW.request_id IS NOT OLD.request_id OR NEW.selected_by_ai_session_id IS NOT OLD.selected_by_ai_session_id BEGIN SELECT RAISE(ABORT,'DevelopmentCycle provenance is immutable'); END;
CREATE TRIGGER development_cycle_no_delete BEFORE DELETE ON development_cycles BEGIN SELECT RAISE(ABORT,'DevelopmentCycle is durable history'); END;
CREATE TRIGGER development_selection_commit_fence BEFORE UPDATE ON development_cycles WHEN EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE repository_id=OLD.repository_id AND development_ref=OLD.development_ref AND completed_at IS NULL) BEGIN SELECT RAISE(ABORT,'GIT_FINALIZATION_PENDING'); END;`)
	return err
}
