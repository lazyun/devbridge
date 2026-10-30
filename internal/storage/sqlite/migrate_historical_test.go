// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

import ()

func historicalMigrations() []Migration {
	return []Migration{
		{Version: 1, SQL: schemaV1},
		{Version: 2, SQL: schemaV2, ForeignKeysOff: true},
		{Version: 3, SQL: schemaV3},
		{Version: 4, SQL: schemaV4},
		{Version: 5, SQL: schemaV5},
		{Version: 6, SQL: schemaV6},
		{Version: 7, SQL: schemaV7},
		{Version: 8, SQL: schemaV8},
		{Version: 9, SQL: schemaV9},
		{Version: 10, SQL: schemaV10},
		{Version: 11, SQL: schemaV11, ForeignKeysOff: true},
		{Version: 12, SQL: schemaV12},
		{Version: 13, SQL: schemaV13},
		{Version: 14, SQL: schemaV14},
		{Version: 15, SQL: schemaV15},
		{Version: 16, SQL: schemaV16, ForeignKeysOff: true},
		{Version: 17, SQL: schemaV17},
		{Version: 18, SQL: schemaV18},
		{Version: 19, SQL: schemaV19},
		{Version: 20, SQL: schemaV20},
		{Version: 21, SQL: schemaV21},
		{Version: 22, SQL: schemaV22},
		{Version: 23, SQL: schemaV23},
		{Version: 24, SQL: schemaV24},
		{Version: 25, SQL: schemaV25, ForeignKeysOff: true},
		{Version: 26, SQL: schemaV26, ForeignKeysOff: true},
		{Version: 27, SQL: schemaV27},
		{Version: 28, SQL: schemaV28, ForeignKeysOff: true},
		{Version: 29, SQL: schemaV29, ForeignKeysOff: true},
		{Version: 30, SQL: schemaV30},
		{Version: 31, SQL: schemaV31},
		{Version: 32, SQL: schemaV32},
		{Version: 33, SQL: schemaV33},
		{Version: 34, SQL: schemaV34},
		{Version: 35, SQL: schemaV35},
		{Version: 36, ForeignKeysOff: true, Apply: migrateUnifiedWork},
		{Version: 37, ForeignKeysOff: true, Apply: migrateRetirement},
		{Version: 38, SQL: schemaV38},
		{Version: 39, ForeignKeysOff: true, Apply: migrateDynamicModels},
		{Version: 40, SQL: schemaV40},
		{Version: 41, SQL: schemaV41},
		{Version: 43, ForeignKeysOff: true, Apply: migrateFinalV43},
	}
}

// schemaV32 closes Review provenance around the exact admission-time source
// state. Existing rows retain their immutable commit and explicitly mark
// branch/dirty facts unknown.
const schemaV32 = `
DROP TRIGGER review_repository_provenance_immutable;
ALTER TABLE review_repository_provenance ADD COLUMN repository_head TEXT;
ALTER TABLE review_repository_provenance ADD COLUMN branch_kind TEXT CHECK(branch_kind IS NULL OR branch_kind IN ('BRANCH','DETACHED','UNKNOWN'));
ALTER TABLE review_repository_provenance ADD COLUMN branch_ref TEXT;
ALTER TABLE review_repository_provenance ADD COLUMN working_tree_dirty INTEGER CHECK(working_tree_dirty IS NULL OR working_tree_dirty IN (0,1));
ALTER TABLE review_repository_provenance ADD COLUMN inspection_mode TEXT CHECK(inspection_mode IS NULL OR inspection_mode IN ('MANAGED_COMMIT_WORKTREE'));
UPDATE review_repository_provenance SET repository_head=reviewed_commit,branch_kind='UNKNOWN',inspection_mode='MANAGED_COMMIT_WORKTREE';
CREATE TRIGGER review_repository_provenance_immutable BEFORE UPDATE ON review_repository_provenance
BEGIN SELECT RAISE(ABORT, 'review repository provenance is immutable'); END;
`

// schemaV31 records only the durable authorization outcome and its
// authoritative result identity. Workflow state remains owned by the Task,
// TaskPlan, and ExecutionProposal tables.
const schemaV31 = `
CREATE TABLE decision_resolution_receipts (
 decision_id TEXT PRIMARY KEY CHECK(length(decision_id)>0 AND length(decision_id)<=256),
 project_id TEXT NOT NULL REFERENCES projects(id),
 decision_kind TEXT NOT NULL CHECK(length(decision_kind)>0 AND length(decision_kind)<=64),
 subject_kind TEXT NOT NULL CHECK(length(subject_kind)>0 AND length(subject_kind)<=64),
 subject_id TEXT NOT NULL CHECK(length(subject_id)>0 AND length(subject_id)<=128),
 option TEXT NOT NULL CHECK(length(option)>0 AND length(option)<=64),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 result_kind TEXT NOT NULL CHECK(length(result_kind)>0 AND length(result_kind)<=64),
 result_id TEXT NOT NULL CHECK(length(result_id)>0 AND length(result_id)<=128),
 resolved_at TEXT NOT NULL,
 UNIQUE(project_id,request_id)
);
CREATE INDEX decision_resolution_project_resolved ON decision_resolution_receipts(project_id,resolved_at,decision_id);
CREATE TRIGGER decision_resolution_receipts_immutable
BEFORE UPDATE ON decision_resolution_receipts
BEGIN SELECT RAISE(ABORT,'Decision resolution receipt is immutable'); END;
CREATE TRIGGER decision_resolution_receipts_no_delete
BEFORE DELETE ON decision_resolution_receipts
BEGIN SELECT RAISE(ABORT,'Decision resolution receipt is durable history'); END;
`

// schemaV25 renames the durable product vocabulary from Topic to Feature Area
// without rewriting history. Table and column identifiers become canonical
// (featureAreas -> feature_areas, feature_area_id -> feature_area_id), historical OPEN
// status maps to ACTIVE and CANCELED to RETIRED (RETIRED preserves the same
// "excluded from normal workflow" capability boundary CANCELED enforced), and
// the bounded FeatureAreaRelation split-provenance table is introduced.
// IDs, timestamps, Project ownership, and all child relations are preserved.
const schemaV25 = `
DROP TRIGGER IF EXISTS tasks_feature_area_project_insert_guard;
DROP TRIGGER IF EXISTS tasks_feature_area_project_update_guard;
DROP TRIGGER IF EXISTS tasks_feature_area_immutable;
DROP INDEX IF EXISTS tasks_featureArea_created;
DROP TRIGGER IF EXISTS reviews_identity_immutable;
DROP INDEX IF EXISTS reviews_featureArea_created;
DROP TRIGGER IF EXISTS task_preparations_source_review_guard;
DROP TRIGGER IF EXISTS task_preparations_identity_immutable;
DROP INDEX IF EXISTS task_preparations_featureArea_created;
DROP INDEX IF EXISTS documents_featureArea_created;

ALTER TABLE featureAreas RENAME TO feature_areas;
CREATE TABLE feature_areas_v25 (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 created_by_ai_session_id TEXT,
 title TEXT NOT NULL CHECK(length(title)>0 AND length(title)<=256),
 problem_statement TEXT NOT NULL CHECK(length(problem_statement)>0 AND length(problem_statement)<=16384),
 status TEXT NOT NULL CHECK(status IN ('DISCOVERING','ACTIVE','SPLITTING','DECOMPOSED','RETIRED')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id)
);
INSERT INTO feature_areas_v25(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at)
 SELECT id,project_id,created_by_ai_session_id,title,problem_statement,
 CASE status WHEN 'CANCELED' THEN 'RETIRED' ELSE 'ACTIVE' END,
 created_at,updated_at
FROM feature_areas;
DROP TABLE feature_areas;
ALTER TABLE feature_areas_v25 RENAME TO feature_areas;
CREATE INDEX feature_areas_project_created ON feature_areas(project_id,created_at DESC,id);

CREATE INDEX tasks_feature_area_created ON tasks(feature_area_id,created_at,id);
CREATE TRIGGER tasks_feature_area_project_insert_guard
BEFORE INSERT ON tasks
WHEN NEW.feature_area_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM feature_areas x WHERE x.id=NEW.feature_area_id AND x.project_id=NEW.project_id
)
BEGIN SELECT RAISE(ABORT, 'task Feature Area must belong to task Project'); END;

CREATE TRIGGER tasks_feature_area_project_update_guard
BEFORE UPDATE OF feature_area_id,project_id ON tasks
WHEN NEW.feature_area_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM feature_areas x WHERE x.id=NEW.feature_area_id AND x.project_id=NEW.project_id
)
BEGIN SELECT RAISE(ABORT, 'task Feature Area must belong to task Project'); END;

CREATE TRIGGER tasks_feature_area_immutable
BEFORE UPDATE OF feature_area_id ON tasks
WHEN NEW.feature_area_id<>OLD.feature_area_id OR (NEW.feature_area_id IS NULL)<>(OLD.feature_area_id IS NULL)
BEGIN SELECT RAISE(ABORT, 'task Feature Area is immutable'); END;

CREATE INDEX reviews_feature_area_created ON reviews(feature_area_id,created_at,id);
CREATE TRIGGER reviews_identity_immutable BEFORE UPDATE OF id,project_id,feature_area_id,created_by_ai_session_id,task_id,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at ON reviews
BEGIN SELECT RAISE(ABORT, 'review identity is immutable'); END;

CREATE INDEX task_preparations_feature_area_created ON task_preparations(feature_area_id,created_at,id);
CREATE TRIGGER task_preparations_source_review_guard
BEFORE INSERT ON task_preparations
WHEN NOT EXISTS(
 SELECT 1 FROM reviews v JOIN runs r ON r.id=v.linked_run_id
 WHERE v.id=NEW.source_review_id AND v.project_id=NEW.project_id
   AND v.feature_area_id=NEW.feature_area_id AND v.task_id=NEW.task_id
   AND r.status='SUCCEEDED' AND r.result_text IS NOT NULL AND length(trim(r.result_text))>0
)
BEGIN SELECT RAISE(ABORT, 'Task Preparation requires a successful Feature Area Review for the same Task'); END;
CREATE TRIGGER task_preparations_identity_immutable
BEFORE UPDATE OF id,project_id,feature_area_id,created_by_ai_session_id,task_id,source_review_id,title,base_commit,work_spec_json,verification_spec_json,feature_area_review_findings,created_at ON task_preparations
BEGIN SELECT RAISE(ABORT, 'Task Preparation identity and inputs are immutable'); END;

CREATE INDEX documents_feature_area_created ON documents(feature_area_id,created_at DESC,id) WHERE feature_area_id IS NOT NULL;

CREATE TABLE feature_area_relations (
 source_feature_area_id TEXT NOT NULL,
 target_feature_area_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('SPLIT_INTO')),
 project_id TEXT NOT NULL REFERENCES projects(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(source_feature_area_id,target_feature_area_id,kind),
 FOREIGN KEY(source_feature_area_id,project_id) REFERENCES feature_areas(id,project_id),
 FOREIGN KEY(target_feature_area_id,project_id) REFERENCES feature_areas(id,project_id)
);
CREATE TRIGGER feature_area_relations_immutable
BEFORE UPDATE ON feature_area_relations
BEGIN SELECT RAISE(ABORT, 'Feature Area relation is immutable'); END;
CREATE TRIGGER feature_area_relations_no_delete
BEFORE DELETE ON feature_area_relations
BEGIN SELECT RAISE(ABORT, 'Feature Area relation is durable history'); END;
`

// schemaV24 adds optional GPT-authored Task Goals and the Task Agent context
// continuation policy. Goal definitions are immutable and owned by their Task;
// per-Execution progress is a separate projection keyed by execution proposal
// and goal definition.
const schemaV24 = `
ALTER TABLE tasks ADD COLUMN agent_context TEXT
 CHECK(agent_context IS NULL OR json_valid(agent_context));

CREATE TABLE task_goals (
 id TEXT PRIMARY KEY CHECK(id GLOB 'goal_*'),
 task_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 position INTEGER NOT NULL CHECK(position>=0 AND position<16),
 title TEXT NOT NULL CHECK(length(title)>0 AND length(title)<=512),
 created_at TEXT NOT NULL,
 UNIQUE(task_id,position),
 UNIQUE(task_id,title),
 UNIQUE(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id)
);
CREATE INDEX task_goals_task_position ON task_goals(task_id,position);
CREATE TRIGGER task_goals_identity_immutable
BEFORE UPDATE ON task_goals
BEGIN SELECT RAISE(ABORT, 'Task Goal definition is immutable'); END;
CREATE TRIGGER task_goals_no_delete
BEFORE DELETE ON task_goals
BEGIN SELECT RAISE(ABORT, 'Task Goal definitions are durable history'); END;
CREATE TRIGGER task_goals_project_guard
BEFORE INSERT ON task_goals
WHEN NOT EXISTS(SELECT 1 FROM tasks t WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT, 'Task Goal must belong to its Task Project'); END;

CREATE TABLE execution_goal_progress (
 execution_proposal_id TEXT NOT NULL,
 goal_id TEXT NOT NULL REFERENCES task_goals(id),
 project_id TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('PENDING','RUNNING','COMPLETED','FAILED','SKIPPED')),
 updated_at TEXT NOT NULL,
 PRIMARY KEY(execution_proposal_id,goal_id),
 FOREIGN KEY(execution_proposal_id,project_id) REFERENCES execution_proposals(id,project_id)
);
CREATE UNIQUE INDEX execution_proposals_id_project ON execution_proposals(id,project_id);
CREATE INDEX execution_goal_progress_project_updated ON execution_goal_progress(project_id,updated_at,execution_proposal_id);
CREATE TRIGGER execution_goal_progress_identity_immutable
BEFORE UPDATE OF execution_proposal_id,goal_id,project_id ON execution_goal_progress
BEGIN SELECT RAISE(ABORT, 'Execution goal progress identity is immutable'); END;
CREATE TRIGGER execution_goal_progress_goal_task_guard
BEFORE INSERT ON execution_goal_progress
WHEN NOT EXISTS(
 SELECT 1 FROM task_goals g JOIN execution_proposals e ON e.task_id=g.task_id
 WHERE g.id=NEW.goal_id AND e.id=NEW.execution_proposal_id AND e.project_id=NEW.project_id
)
BEGIN SELECT RAISE(ABORT, 'goal progress requires a goal of the execution Task'); END;
CREATE TRIGGER execution_goal_progress_status_transition
BEFORE UPDATE OF status,updated_at ON execution_goal_progress
WHEN NOT (
 (OLD.status='PENDING' AND NEW.status IN ('RUNNING','COMPLETED','FAILED','SKIPPED')) OR
 (OLD.status='RUNNING' AND NEW.status IN ('COMPLETED','FAILED','SKIPPED')) OR
 (OLD.status=NEW.status)
)
BEGIN SELECT RAISE(ABORT, 'invalid goal progress status transition'); END;
`

// schemaV22 freezes the GPT-selected Codex model and reasoning effort on
// durable Reviews and Tasks. Existing rows remain NULL; Bridge never guesses
// historical selections.
const schemaV22 = `
ALTER TABLE reviews ADD COLUMN model TEXT
 CHECK(model IS NULL OR model IN ('gpt-5.6-luna','gpt-5.6-terra','gpt-5.6-sol'));
ALTER TABLE reviews ADD COLUMN reasoning_effort TEXT
 CHECK(reasoning_effort IS NULL OR reasoning_effort IN ('low','medium','high','xhigh'));
ALTER TABLE tasks ADD COLUMN model TEXT
 CHECK(model IS NULL OR model IN ('gpt-5.6-luna','gpt-5.6-terra','gpt-5.6-sol'));
ALTER TABLE tasks ADD COLUMN reasoning_effort TEXT
 CHECK(reasoning_effort IS NULL OR reasoning_effort IN ('low','medium','high','xhigh'));
`

// schemaV21 adds the optional GPT-authored organization-only Task work kind.
// Existing Tasks migrate with work_kind = NULL (unclassified) and no rows are
// rewritten. The CHECK allows only the closed enum and NULL.
const schemaV21 = `
ALTER TABLE tasks ADD COLUMN work_kind TEXT
 CHECK(work_kind IS NULL OR work_kind IN ('FEATURE','BUG_FIX','REFACTOR','MAINTENANCE','TEST_CHANGE','DOCUMENTATION','OTHER'));
CREATE INDEX tasks_work_kind_created ON tasks(work_kind,created_at,id);
`

// schemaV20 adds explicitly authored Task Preparation instructions, durable
// prompt audit fields for every backend Run, and the minimal Project-owned
// Document object. Historical evidence remains intact but is no longer a
// completion prerequisite.
const schemaV20 = `
ALTER TABLE task_preparations ADD COLUMN preparation_instruction TEXT NOT NULL DEFAULT '' CHECK(length(preparation_instruction)<=65536);
CREATE TRIGGER task_preparations_instruction_immutable
BEFORE UPDATE OF preparation_instruction ON task_preparations
BEGIN SELECT RAISE(ABORT, 'Task Preparation instruction is immutable'); END;

ALTER TABLE runs ADD COLUMN final_codex_prompt TEXT;
ALTER TABLE runs ADD COLUMN prompt_sha256 TEXT CHECK(prompt_sha256 IS NULL OR (length(prompt_sha256)=64 AND prompt_sha256 GLOB '[0-9a-f]*'));
ALTER TABLE runs ADD COLUMN prompt_bytes INTEGER CHECK(prompt_bytes IS NULL OR prompt_bytes>=0);
ALTER TABLE runs ADD COLUMN prompt_sent_at TEXT;
CREATE TRIGGER runs_prompt_immutable
BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON runs
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'Run prompt is immutable'); END;

ALTER TABLE context_probes ADD COLUMN final_codex_prompt TEXT;
ALTER TABLE context_probes ADD COLUMN prompt_sha256 TEXT CHECK(prompt_sha256 IS NULL OR (length(prompt_sha256)=64 AND prompt_sha256 GLOB '[0-9a-f]*'));
ALTER TABLE context_probes ADD COLUMN prompt_bytes INTEGER CHECK(prompt_bytes IS NULL OR prompt_bytes>=0);
ALTER TABLE context_probes ADD COLUMN prompt_sent_at TEXT;
CREATE TRIGGER context_probes_prompt_immutable
BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON context_probes
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'Context Probe prompt is immutable'); END;

CREATE TABLE documents (
 id TEXT PRIMARY KEY CHECK(id GLOB 'document_*'),
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT,
 created_by_ai_session_id TEXT NOT NULL,
 title TEXT NOT NULL CHECK(length(title)>0 AND length(title)<=256),
 content TEXT NOT NULL CHECK(length(content)>0 AND length(content)<=262144),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES featureAreas(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id)
);
CREATE INDEX documents_project_created ON documents(project_id,created_at DESC,id);
CREATE INDEX documents_featureArea_created ON documents(feature_area_id,created_at DESC,id) WHERE feature_area_id IS NOT NULL;
CREATE TRIGGER documents_content_immutable
BEFORE UPDATE ON documents
BEGIN SELECT RAISE(ABORT, 'Document is immutable'); END;
`

const schemaV18 = `
ALTER TABLE reviews ADD COLUMN continuation_attempted_at TEXT;
`

// schemaV19 introduces the Task-owned preparation aggregate. The product
// record and its bounded result are distinct from both Topic Review and the
// internal read-only Run which performs repository inspection.
const schemaV19 = `
CREATE TABLE task_preparations (
 id TEXT PRIMARY KEY CHECK(id GLOB 'task_preparation_*'),
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT NOT NULL,
 created_by_ai_session_id TEXT NOT NULL,
 task_id TEXT NOT NULL UNIQUE,
 source_review_id TEXT NOT NULL,
 linked_run_id TEXT UNIQUE REFERENCES runs(id),
 task_plan_id TEXT UNIQUE REFERENCES task_plans(id),
 title TEXT NOT NULL CHECK(length(title)>0 AND length(title)<=256),
 base_commit TEXT NOT NULL CHECK(length(base_commit) IN (40,64)),
 work_spec_json TEXT NOT NULL CHECK(json_valid(work_spec_json)),
 verification_spec_json TEXT NOT NULL CHECK(json_valid(verification_spec_json)),
 feature_area_review_findings TEXT NOT NULL CHECK(length(feature_area_review_findings)>0 AND length(feature_area_review_findings)<=65536),
 status TEXT NOT NULL CHECK(status IN ('DRAFT','PREPARING','READY','CONFLICT','FAILED')),
 result_json TEXT CHECK(result_json IS NULL OR json_valid(result_json)),
 failure_code TEXT CHECK(failure_code IS NULL OR length(failure_code)<=256),
 failure_summary TEXT CHECK(failure_summary IS NULL OR length(failure_summary)<=4096),
 final_codex_prompt TEXT,
 prompt_sha256 TEXT CHECK(prompt_sha256 IS NULL OR (length(prompt_sha256)=64 AND prompt_sha256 GLOB '[0-9a-f]*')),
 prompt_bytes INTEGER CHECK(prompt_bytes IS NULL OR prompt_bytes>=0),
 prompt_sent_at TEXT,
 materialization_attempted_at TEXT,
 continuation_attempted_at TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES featureAreas(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 FOREIGN KEY(source_review_id,project_id) REFERENCES reviews(id,project_id),
 CHECK((status='READY' AND result_json IS NOT NULL AND task_plan_id IS NOT NULL AND failure_code IS NULL AND failure_summary IS NULL) OR status<>'READY'),
 CHECK((status IN ('CONFLICT','FAILED') AND failure_code IS NOT NULL AND failure_summary IS NOT NULL) OR status NOT IN ('CONFLICT','FAILED')),
 CHECK((final_codex_prompt IS NULL AND prompt_sha256 IS NULL AND prompt_bytes IS NULL AND prompt_sent_at IS NULL) OR (final_codex_prompt IS NOT NULL AND prompt_sha256 IS NOT NULL AND prompt_bytes IS NOT NULL AND prompt_sent_at IS NOT NULL))
);
CREATE INDEX task_preparations_project_created ON task_preparations(project_id,created_at,id);
CREATE INDEX task_preparations_featureArea_created ON task_preparations(feature_area_id,created_at,id);
CREATE TRIGGER task_preparations_source_review_guard
BEFORE INSERT ON task_preparations
WHEN NOT EXISTS(
 SELECT 1 FROM reviews v JOIN runs r ON r.id=v.linked_run_id
 WHERE v.id=NEW.source_review_id AND v.project_id=NEW.project_id
   AND v.feature_area_id=NEW.feature_area_id AND v.task_id=NEW.task_id
   AND r.status='SUCCEEDED' AND r.result_text IS NOT NULL AND length(trim(r.result_text))>0
)
BEGIN SELECT RAISE(ABORT, 'Task Preparation requires a successful Topic Review for the same Task'); END;
CREATE TRIGGER task_preparations_identity_immutable
BEFORE UPDATE OF id,project_id,feature_area_id,created_by_ai_session_id,task_id,source_review_id,title,base_commit,work_spec_json,verification_spec_json,feature_area_review_findings,created_at ON task_preparations
BEGIN SELECT RAISE(ABORT, 'Task Preparation identity and inputs are immutable'); END;
CREATE TRIGGER task_preparations_prompt_immutable
BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON task_preparations
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'Task Preparation prompt is immutable'); END;
CREATE TRIGGER task_preparations_linked_run_matches_task
BEFORE UPDATE OF linked_run_id ON task_preparations
WHEN NEW.linked_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.linked_run_id AND task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'Task Preparation Run task mismatch'); END;
CREATE TRIGGER task_preparations_status_transition
BEFORE UPDATE OF status ON task_preparations
WHEN NOT ((OLD.status='DRAFT' AND NEW.status='PREPARING') OR (OLD.status='PREPARING' AND NEW.status IN ('READY','CONFLICT','FAILED')))
BEGIN SELECT RAISE(ABORT, 'invalid Task Preparation status transition'); END;

ALTER TABLE runs ADD COLUMN task_preparation_id TEXT REFERENCES task_preparations(id);
CREATE UNIQUE INDEX runs_task_preparation_id ON runs(task_preparation_id) WHERE task_preparation_id IS NOT NULL;
CREATE TRIGGER runs_task_preparation_matches_task_insert BEFORE INSERT ON runs
WHEN NEW.task_preparation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM task_preparations WHERE id=NEW.task_preparation_id AND task_id=NEW.task_id AND status='PREPARING')
BEGIN SELECT RAISE(ABORT, 'Run Task Preparation mismatch'); END;
CREATE TRIGGER runs_task_preparation_identity_immutable BEFORE UPDATE OF task_preparation_id ON runs
WHEN OLD.task_preparation_id IS NOT NULL OR NEW.task_preparation_id IS NULL
BEGIN SELECT RAISE(ABORT, 'Run Task Preparation identity is immutable'); END;

CREATE TABLE task_preparation_access_evidence (
 id TEXT PRIMARY KEY,
 task_preparation_id TEXT NOT NULL REFERENCES task_preparations(id),
 operation TEXT NOT NULL CHECK(length(operation)>0 AND length(operation)<=128),
 outcome TEXT NOT NULL CHECK(outcome IN ('SUCCESS','FORBIDDEN','FAILURE')),
 code TEXT CHECK(code IS NULL OR length(code)<=256),
 recorded_at TEXT NOT NULL
);
CREATE INDEX task_preparation_access_observed ON task_preparation_access_evidence(task_preparation_id,recorded_at,id);
`

const schemaV1 = `
CREATE TABLE projects (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE repositories (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), name TEXT NOT NULL,
 canonical_path TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(id, project_id)
);
CREATE TABLE tasks (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), repository_id TEXT NOT NULL,
 goal TEXT NOT NULL, constraints_json TEXT NOT NULL, base_commit TEXT NOT NULL, status TEXT NOT NULL,
 next_attempt INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 FOREIGN KEY(repository_id, project_id) REFERENCES repositories(id, project_id)
);
CREATE TABLE runs (
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), attempt INTEGER NOT NULL, status TEXT NOT NULL,
 worktree_id TEXT, thread_id TEXT, started_at TEXT, finished_at TEXT, failure_class TEXT, failure_code TEXT,
 failure_stage TEXT, failure_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(task_id, attempt)
);
CREATE TABLE worktrees (
 id TEXT PRIMARY KEY, repository_id TEXT NOT NULL REFERENCES repositories(id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 path TEXT NOT NULL UNIQUE, branch TEXT NOT NULL, base_commit TEXT NOT NULL, status TEXT NOT NULL,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(repository_id, branch)
);
CREATE TABLE artifacts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), kind TEXT NOT NULL, path TEXT NOT NULL UNIQUE,
 size_bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(id, run_id)
);
CREATE TABLE run_evidence (
 run_id TEXT PRIMARY KEY REFERENCES runs(id), base_commit TEXT NOT NULL, head_commit TEXT NOT NULL, dirty INTEGER NOT NULL,
 changed_files_json TEXT NOT NULL, diff_artifact_id TEXT, created_at TEXT NOT NULL,
 FOREIGN KEY(diff_artifact_id, run_id) REFERENCES artifacts(id, run_id)
);
CREATE TABLE events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, type TEXT NOT NULL,
 project_id TEXT REFERENCES projects(id), task_id TEXT REFERENCES tasks(id), run_id TEXT REFERENCES runs(id), entity_type TEXT NOT NULL, entity_id TEXT NOT NULL,
 payload_json TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE INDEX events_project_sequence ON events(project_id, sequence);
CREATE INDEX events_task_sequence ON events(task_id, sequence);
CREATE INDEX events_run_sequence ON events(run_id, sequence);
`

const schemaV2 = `
PRAGMA defer_foreign_keys = ON;

CREATE TABLE modules (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 key TEXT NOT NULL,
 name TEXT NOT NULL,
 parent_id TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(project_id, key),
 UNIQUE(id, project_id),
 CHECK(parent_id IS NULL OR parent_id <> id),
 FOREIGN KEY(parent_id, project_id) REFERENCES modules(id, project_id)
);

CREATE TABLE codex_configs (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 codex_path TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0, 1)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);

INSERT INTO codex_configs(id,name,codex_path,enabled,created_at,updated_at)
VALUES ('codex_config_default','Default Codex configuration','',1,
 strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'));

INSERT INTO modules(id,project_id,key,name,parent_id,created_at,updated_at)
SELECT printf('module_%032x', rowid),id,'default','Default',NULL,created_at,updated_at FROM projects;

CREATE TABLE tasks_v2 (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 module_id TEXT NOT NULL,
 repository_id TEXT NOT NULL,
 codex_config_id TEXT NOT NULL REFERENCES codex_configs(id),
 goal TEXT NOT NULL,
 constraints_json TEXT NOT NULL,
 base_commit TEXT NOT NULL,
 status TEXT NOT NULL,
 next_attempt INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(repository_id, project_id) REFERENCES repositories(id, project_id),
 FOREIGN KEY(module_id, project_id) REFERENCES modules(id, project_id)
);

INSERT INTO tasks_v2(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,next_attempt,created_at,updated_at)
SELECT t.id,t.project_id,m.id,t.repository_id,'codex_config_default',t.goal,t.constraints_json,t.base_commit,t.status,t.next_attempt,t.created_at,t.updated_at
FROM tasks t JOIN modules m ON m.project_id=t.project_id AND m.key='default';

DROP TABLE tasks;
ALTER TABLE tasks_v2 RENAME TO tasks;

CREATE TABLE run_token_usage (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 status TEXT NOT NULL CHECK(status IN ('PENDING','AVAILABLE','UNAVAILABLE')),
 start_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(start_input_tokens >= 0),
 start_cached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(start_cached_input_tokens >= 0),
 start_cache_write_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(start_cache_write_input_tokens >= 0),
 start_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(start_output_tokens >= 0),
 start_reasoning_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(start_reasoning_output_tokens >= 0),
 start_total_tokens INTEGER NOT NULL DEFAULT 0 CHECK(start_total_tokens >= 0),
 end_input_tokens INTEGER CHECK(end_input_tokens >= 0),
 end_cached_input_tokens INTEGER CHECK(end_cached_input_tokens >= 0),
 end_cache_write_input_tokens INTEGER CHECK(end_cache_write_input_tokens >= 0),
 end_output_tokens INTEGER CHECK(end_output_tokens >= 0),
 end_reasoning_output_tokens INTEGER CHECK(end_reasoning_output_tokens >= 0),
 end_total_tokens INTEGER CHECK(end_total_tokens >= 0),
 input_tokens INTEGER CHECK(input_tokens >= 0),
 cached_input_tokens INTEGER CHECK(cached_input_tokens >= 0),
 cache_write_input_tokens INTEGER CHECK(cache_write_input_tokens >= 0),
 output_tokens INTEGER CHECK(output_tokens >= 0),
 reasoning_output_tokens INTEGER CHECK(reasoning_output_tokens >= 0),
 total_tokens INTEGER CHECK(total_tokens >= 0),
 model_context_window INTEGER CHECK(model_context_window IS NULL OR model_context_window >= 0),
 updated_at TEXT NOT NULL
);

INSERT INTO run_token_usage(run_id,status,updated_at)
SELECT id,
 CASE status WHEN 'SUCCEEDED' THEN 'UNAVAILABLE' WHEN 'FAILED' THEN 'UNAVAILABLE'
  WHEN 'INFRASTRUCTURE_FAILED' THEN 'UNAVAILABLE' WHEN 'CANCELED' THEN 'UNAVAILABLE'
  ELSE 'PENDING' END,
 updated_at
FROM runs;
`

const schemaV3 = `
CREATE TABLE approvals (
 id TEXT PRIMARY KEY,
 run_id TEXT NOT NULL REFERENCES runs(id),
 thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 item_id TEXT,
 kind TEXT NOT NULL CHECK(kind IN ('COMMAND_EXECUTION','FILE_CHANGE','PERMISSIONS','LEGACY_COMMAND','LEGACY_FILE_CHANGE')),
 status TEXT NOT NULL CHECK(status IN ('PENDING','DECIDED','DELIVERED','CANCELED','EXPIRED')),
 request_payload_json TEXT NOT NULL,
 allowed_decisions_json TEXT NOT NULL,
 decision_json TEXT,
 created_at TEXT NOT NULL,
 decided_at TEXT,
 delivered_at TEXT,
 CHECK((status='PENDING' AND decision_json IS NULL AND decided_at IS NULL AND delivered_at IS NULL) OR
       (status='DECIDED' AND decision_json IS NOT NULL AND decided_at IS NOT NULL AND delivered_at IS NULL) OR
       (status='DELIVERED' AND decision_json IS NOT NULL AND decided_at IS NOT NULL AND delivered_at IS NOT NULL) OR
       (status='CANCELED' AND delivered_at IS NULL) OR
       (status='EXPIRED' AND delivered_at IS NULL))
);
CREATE INDEX approvals_run_status ON approvals(run_id,status,created_at,id);
CREATE INDEX approvals_thread_turn_item ON approvals(thread_id,turn_id,item_id);
`

const schemaV4 = `
CREATE TABLE project_selection (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 project_id TEXT NOT NULL REFERENCES projects(id),
 updated_at TEXT NOT NULL
);

CREATE TABLE ai_sessions (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX ai_sessions_project_created ON ai_sessions(project_id,created_at,id);

CREATE TRIGGER ai_sessions_project_immutable
BEFORE UPDATE OF project_id ON ai_sessions
WHEN NEW.project_id <> OLD.project_id
BEGIN
 SELECT RAISE(ABORT, 'AI session project is immutable');
END;
`

const schemaV5 = `
CREATE UNIQUE INDEX ai_sessions_id_project ON ai_sessions(id,project_id);
CREATE UNIQUE INDEX tasks_id_project ON tasks(id,project_id);

CREATE TABLE task_proposals (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('CREATE_TASK','CONTINUE_TASK')),
 module_id TEXT NOT NULL,
 repository_id TEXT NOT NULL,
 target_task_id TEXT,
 codex_config_id TEXT NOT NULL REFERENCES codex_configs(id),
 goal TEXT NOT NULL,
 constraints_json TEXT NOT NULL,
 base_revision TEXT,
 status TEXT NOT NULL CHECK(status IN ('PENDING_CONFIRMATION','CONFIRMED','REJECTED','CONSUMED')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 confirmed_at TEXT,
 rejected_at TEXT,
 consumed_at TEXT,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(module_id,project_id) REFERENCES modules(id,project_id),
 FOREIGN KEY(repository_id,project_id) REFERENCES repositories(id,project_id),
 FOREIGN KEY(target_task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((kind='CREATE_TASK' AND target_task_id IS NULL AND base_revision IS NOT NULL) OR
       (kind='CONTINUE_TASK' AND target_task_id IS NOT NULL AND base_revision IS NULL)),
 CHECK((status='PENDING_CONFIRMATION' AND confirmed_at IS NULL AND rejected_at IS NULL AND consumed_at IS NULL) OR
       (status='CONFIRMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NULL) OR
       (status='REJECTED' AND confirmed_at IS NULL AND rejected_at IS NOT NULL AND consumed_at IS NULL) OR
       (status='CONSUMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NOT NULL))
);
CREATE INDEX task_proposals_session_created ON task_proposals(ai_session_id,created_at DESC,id);
CREATE INDEX task_proposals_project_status ON task_proposals(project_id,status,created_at,id);
CREATE INDEX task_proposals_target_status ON task_proposals(target_task_id,status,created_at,id);

CREATE TRIGGER task_proposals_definition_immutable
BEFORE UPDATE OF ai_session_id,project_id,kind,module_id,repository_id,target_task_id,codex_config_id,goal,constraints_json,base_revision ON task_proposals
BEGIN
 SELECT RAISE(ABORT, 'task proposal definition is immutable');
END;

CREATE TRIGGER task_proposals_status_transition
BEFORE UPDATE OF status ON task_proposals
WHEN NOT ((OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('CONFIRMED','REJECTED')) OR
          (OLD.status='CONFIRMED' AND NEW.status='CONSUMED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid task proposal status transition');
END;
`

const schemaV6 = `
CREATE UNIQUE INDEX runs_one_active_per_task
ON runs(task_id)
WHERE status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING');
`

const schemaV7 = `
ALTER TABLE task_proposals ADD COLUMN result_task_id TEXT REFERENCES tasks(id);
CREATE INDEX task_proposals_result_task ON task_proposals(result_task_id);
CREATE UNIQUE INDEX task_proposals_id_session_project
ON task_proposals(id,ai_session_id,project_id);
CREATE UNIQUE INDEX runs_id_task ON runs(id,task_id);

CREATE TRIGGER task_proposals_result_task_insert_guard
BEFORE INSERT ON task_proposals
WHEN NEW.result_task_id IS NOT NULL
BEGIN
 SELECT RAISE(ABORT, 'task proposal result is assigned only during consumption');
END;

CREATE TRIGGER task_proposals_result_task_update_guard
BEFORE UPDATE OF result_task_id ON task_proposals
WHEN NOT (OLD.result_task_id IS NULL AND NEW.result_task_id IS NOT NULL AND
          OLD.status='CONFIRMED' AND NEW.status='CONSUMED' AND
          EXISTS (SELECT 1 FROM tasks t WHERE t.id=NEW.result_task_id AND t.project_id=OLD.project_id) AND
          ((OLD.kind='CREATE_TASK' AND OLD.target_task_id IS NULL) OR
           (OLD.kind='CONTINUE_TASK' AND OLD.target_task_id=NEW.result_task_id)))
BEGIN
 SELECT RAISE(ABORT, 'task proposal result is immutable');
END;

CREATE TABLE execution_proposals (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 source_task_proposal_id TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('PENDING_CONFIRMATION','CONFIRMED','REJECTED','CONSUMED')),
 run_id TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 confirmed_at TEXT,
 rejected_at TEXT,
 consumed_at TEXT,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 FOREIGN KEY(source_task_proposal_id,ai_session_id,project_id) REFERENCES task_proposals(id,ai_session_id,project_id),
 FOREIGN KEY(run_id,task_id) REFERENCES runs(id,task_id),
 UNIQUE(run_id),
 CHECK((status='PENDING_CONFIRMATION' AND confirmed_at IS NULL AND rejected_at IS NULL AND consumed_at IS NULL AND run_id IS NULL) OR
       (status='CONFIRMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NULL AND run_id IS NULL) OR
       (status='REJECTED' AND confirmed_at IS NULL AND rejected_at IS NOT NULL AND consumed_at IS NULL AND run_id IS NULL) OR
       (status='CONSUMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NOT NULL AND run_id IS NOT NULL))
);
CREATE INDEX execution_proposals_session_created ON execution_proposals(ai_session_id,created_at DESC,id);
CREATE INDEX execution_proposals_project_status ON execution_proposals(project_id,status,created_at,id);
CREATE INDEX execution_proposals_task_status ON execution_proposals(task_id,status,created_at,id);

CREATE TRIGGER execution_proposals_source_consumed
BEFORE INSERT ON execution_proposals
WHEN NOT EXISTS (
 SELECT 1 FROM task_proposals p
 WHERE p.id=NEW.source_task_proposal_id
   AND p.ai_session_id=NEW.ai_session_id
   AND p.project_id=NEW.project_id
   AND p.status='CONSUMED'
   AND p.result_task_id=NEW.task_id
)
BEGIN
 SELECT RAISE(ABORT, 'execution proposal requires consumed task proposal provenance');
END;

CREATE TRIGGER execution_proposals_definition_immutable
BEFORE UPDATE OF ai_session_id,project_id,task_id,source_task_proposal_id ON execution_proposals
BEGIN
 SELECT RAISE(ABORT, 'execution proposal definition is immutable');
END;

CREATE TRIGGER execution_proposals_status_transition
BEFORE UPDATE OF status ON execution_proposals
WHEN NOT ((OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('CONFIRMED','REJECTED')) OR
          (OLD.status='CONFIRMED' AND NEW.status='CONSUMED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution proposal status transition');
END;
`

const schemaV8 = `
CREATE TABLE workspaces (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
 worktree_id TEXT UNIQUE REFERENCES worktrees(id),
 thread_id TEXT UNIQUE,
 status TEXT NOT NULL CHECK(status IN ('READY','CONTINUITY_UNAVAILABLE')),
 token_baseline_available INTEGER NOT NULL CHECK(token_baseline_available IN (0,1)),
 token_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_input_tokens >= 0),
 token_cached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_cached_input_tokens >= 0),
 token_cache_write_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_cache_write_input_tokens >= 0),
 token_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_output_tokens >= 0),
 token_reasoning_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_reasoning_output_tokens >= 0),
 token_total_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_total_tokens >= 0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(id,task_id)
);
CREATE INDEX workspaces_status_updated ON workspaces(status,updated_at,id);

ALTER TABLE runs ADD COLUMN workspace_id TEXT REFERENCES workspaces(id);
ALTER TABLE runs ADD COLUMN turn_id TEXT;
ALTER TABLE runs ADD COLUMN start_snapshot TEXT CHECK(start_snapshot IS NULL OR length(start_snapshot) IN (40,64));
ALTER TABLE runs ADD COLUMN end_snapshot TEXT CHECK(end_snapshot IS NULL OR length(end_snapshot) IN (40,64));
CREATE INDEX runs_workspace_attempt ON runs(workspace_id,attempt);
CREATE UNIQUE INDEX runs_thread_turn ON runs(thread_id,turn_id) WHERE thread_id IS NOT NULL AND turn_id IS NOT NULL;

ALTER TABLE run_evidence ADD COLUMN start_snapshot TEXT CHECK(start_snapshot IS NULL OR length(start_snapshot) IN (40,64));
ALTER TABLE run_evidence ADD COLUMN end_snapshot TEXT CHECK(end_snapshot IS NULL OR length(end_snapshot) IN (40,64));

CREATE TRIGGER workspaces_task_immutable
BEFORE UPDATE OF task_id ON workspaces
WHEN NEW.task_id <> OLD.task_id
BEGIN
 SELECT RAISE(ABORT, 'workspace task is immutable');
END;

CREATE TRIGGER workspaces_worktree_immutable
BEFORE UPDATE OF worktree_id ON workspaces
WHEN OLD.worktree_id IS NOT NULL AND (NEW.worktree_id IS NULL OR NEW.worktree_id <> OLD.worktree_id)
BEGIN
 SELECT RAISE(ABORT, 'workspace worktree is immutable');
END;

CREATE TRIGGER workspaces_thread_immutable
BEFORE UPDATE OF thread_id ON workspaces
WHEN OLD.thread_id IS NOT NULL AND (NEW.thread_id IS NULL OR NEW.thread_id <> OLD.thread_id)
BEGIN
 SELECT RAISE(ABORT, 'workspace thread is immutable');
END;

CREATE TRIGGER runs_workspace_task_guard
BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND w.task_id=NEW.task_id
)
BEGIN
 SELECT RAISE(ABORT, 'run workspace must belong to run task');
END;

CREATE TRIGGER runs_workspace_immutable
BEFORE UPDATE OF workspace_id ON runs
WHEN OLD.workspace_id IS NOT NULL AND (NEW.workspace_id IS NULL OR NEW.workspace_id <> OLD.workspace_id)
BEGIN
 SELECT RAISE(ABORT, 'run workspace is immutable');
END;

CREATE TRIGGER runs_turn_immutable
BEFORE UPDATE OF turn_id ON runs
WHEN OLD.turn_id IS NOT NULL AND (NEW.turn_id IS NULL OR NEW.turn_id <> OLD.turn_id)
BEGIN
 SELECT RAISE(ABORT, 'run turn is immutable');
END;

CREATE TRIGGER runs_snapshots_immutable
BEFORE UPDATE OF start_snapshot,end_snapshot ON runs
WHEN (OLD.start_snapshot IS NOT NULL AND (NEW.start_snapshot IS NULL OR NEW.start_snapshot <> OLD.start_snapshot)) OR
     (OLD.end_snapshot IS NOT NULL AND (NEW.end_snapshot IS NULL OR NEW.end_snapshot <> OLD.end_snapshot))
BEGIN
 SELECT RAISE(ABORT, 'run snapshots are immutable');
END;
`

const schemaV9 = `
ALTER TABLE modules ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE modules ADD COLUMN system_owned INTEGER NOT NULL DEFAULT 0 CHECK(system_owned IN (0,1));

CREATE UNIQUE INDEX modules_one_system_unclassified
ON modules(project_id)
WHERE system_owned=1 AND key='_unclassified';

CREATE TRIGGER modules_system_identity_insert_guard
BEFORE INSERT ON modules
WHEN (NEW.system_owned=1 AND (NEW.key<>'_unclassified' OR NEW.parent_id IS NOT NULL)) OR
     (NEW.system_owned=0 AND NEW.key='_unclassified')
BEGIN
 SELECT RAISE(ABORT, 'invalid system module identity');
END;

CREATE TRIGGER modules_system_identity_update_guard
BEFORE UPDATE OF key,system_owned,parent_id ON modules
WHEN NEW.key<>OLD.key OR NEW.system_owned<>OLD.system_owned OR
     COALESCE(NEW.parent_id,'')<>COALESCE(OLD.parent_id,'')
BEGIN
 SELECT RAISE(ABORT, 'module identity is immutable');
END;

CREATE TABLE context_probes (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 repository_id TEXT NOT NULL,
 codex_config_id TEXT NOT NULL REFERENCES codex_configs(id),
 scope_kind TEXT NOT NULL CHECK(scope_kind IN ('PATHS','REPOSITORY')),
 scope_paths_json TEXT NOT NULL,
 codex_instruction TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('CREATED','RUNNING','SUCCEEDED','NO_GUIDANCE','FAILED','CANCELED')),
 thread_id TEXT,
 turn_id TEXT,
 started_at TEXT,
 finished_at TEXT,
 token_usage_status TEXT NOT NULL CHECK(token_usage_status IN ('PENDING','AVAILABLE','UNAVAILABLE')),
 input_tokens INTEGER CHECK(input_tokens >= 0),
 cached_input_tokens INTEGER CHECK(cached_input_tokens >= 0),
 cache_write_input_tokens INTEGER CHECK(cache_write_input_tokens >= 0),
 output_tokens INTEGER CHECK(output_tokens >= 0),
 reasoning_output_tokens INTEGER CHECK(reasoning_output_tokens >= 0),
 total_tokens INTEGER CHECK(total_tokens >= 0),
 model_context_window INTEGER CHECK(model_context_window >= 0),
 result TEXT,
 result_artifact_ref TEXT,
 failure_message TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(repository_id,project_id) REFERENCES repositories(id,project_id),
 CHECK((scope_kind='REPOSITORY' AND scope_paths_json='{"version":1,"paths":[]}') OR scope_kind='PATHS'),
 CHECK((status='CREATED' AND started_at IS NULL AND finished_at IS NULL) OR
       (status='RUNNING' AND started_at IS NOT NULL AND finished_at IS NULL) OR
       (status IN ('SUCCEEDED','NO_GUIDANCE','FAILED','CANCELED') AND started_at IS NOT NULL AND finished_at IS NOT NULL))
);
CREATE INDEX context_probes_session_created ON context_probes(ai_session_id,created_at DESC,id);
CREATE INDEX context_probes_project_created ON context_probes(project_id,created_at DESC,id);
CREATE UNIQUE INDEX context_probes_one_running_per_project ON context_probes(project_id) WHERE status='RUNNING';

CREATE TRIGGER context_probes_definition_immutable
BEFORE UPDATE OF ai_session_id,project_id,repository_id,codex_config_id,scope_kind,scope_paths_json,codex_instruction ON context_probes
BEGIN
 SELECT RAISE(ABORT, 'context probe definition is immutable');
END;

CREATE TRIGGER context_probes_status_transition
BEFORE UPDATE OF status ON context_probes
WHEN OLD.status<>NEW.status AND
     NOT (OLD.status='CREATED' AND NEW.status='RUNNING') AND
     NOT (OLD.status='RUNNING' AND NEW.status IN ('SUCCEEDED','NO_GUIDANCE','FAILED','CANCELED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid context probe status transition');
END;

CREATE TRIGGER context_probes_execution_ids_immutable
BEFORE UPDATE OF thread_id,turn_id ON context_probes
WHEN (OLD.thread_id IS NOT NULL AND (NEW.thread_id IS NULL OR NEW.thread_id<>OLD.thread_id)) OR
     (OLD.turn_id IS NOT NULL AND (NEW.turn_id IS NULL OR NEW.turn_id<>OLD.turn_id))
BEGIN
 SELECT RAISE(ABORT, 'context probe execution identifiers are immutable');
END;

CREATE TABLE project_contexts (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 status TEXT NOT NULL CHECK(status IN ('REQUIRED','RUNNING','READY','NO_GUIDANCE','FAILED')),
 root_guidance_identity TEXT,
 latest_probe_id TEXT REFERENCES context_probes(id),
 result TEXT,
 result_artifact_ref TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);

INSERT INTO project_contexts(project_id,status,created_at,updated_at)
SELECT id,'REQUIRED',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM projects;

CREATE TRIGGER project_context_probe_project_guard
BEFORE UPDATE OF latest_probe_id ON project_contexts
WHEN NEW.latest_probe_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM context_probes p WHERE p.id=NEW.latest_probe_id AND p.project_id=NEW.project_id
)
BEGIN
 SELECT RAISE(ABORT, 'project context probe must belong to project');
END;

CREATE TABLE task_classifications (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 from_module_id TEXT NOT NULL,
 to_module_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 FOREIGN KEY(from_module_id,project_id) REFERENCES modules(id,project_id),
 FOREIGN KEY(to_module_id,project_id) REFERENCES modules(id,project_id)
);
CREATE INDEX task_classifications_task_created ON task_classifications(task_id,created_at,id);

CREATE TRIGGER tasks_module_classification_guard
BEFORE UPDATE OF module_id ON tasks
WHEN NEW.module_id<>OLD.module_id AND NOT (
 EXISTS (SELECT 1 FROM modules m WHERE m.id=OLD.module_id AND m.project_id=OLD.project_id AND m.system_owned=1 AND m.key='_unclassified') AND
 EXISTS (SELECT 1 FROM modules m WHERE m.id=NEW.module_id AND m.project_id=OLD.project_id AND m.system_owned=0) AND
 NOT EXISTS (SELECT 1 FROM runs r WHERE r.task_id=OLD.id AND r.status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING'))
)
BEGIN
 SELECT RAISE(ABORT, 'invalid task module classification');
END;
`

const schemaV10 = `
ALTER TABLE task_proposals ADD COLUMN work_spec_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE task_proposals ADD COLUMN verification_spec_json TEXT NOT NULL DEFAULT '{}';
UPDATE task_proposals SET
 work_spec_json=json_object('version',1,'goal',goal,'constraints',json_extract(constraints_json,'$.items'),'codexInstruction',goal,'executionScope',json_object('kind','LEGACY_UNSPECIFIED')),
 verification_spec_json=json_object('version',1,'mode','NONE','waiver',json_object('source','LEGACY_UNSPECIFIED','reason','Task proposal predates explicit verification planning'));

ALTER TABLE tasks ADD COLUMN work_spec_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE tasks ADD COLUMN verification_spec_json TEXT NOT NULL DEFAULT '{}';
UPDATE tasks SET
 work_spec_json=json_object('version',1,'goal',goal,'constraints',json_extract(constraints_json,'$.items'),'codexInstruction',goal,'executionScope',json_object('kind','LEGACY_UNSPECIFIED')),
 verification_spec_json=json_object('version',1,'mode','NONE','waiver',json_object('source','LEGACY_UNSPECIFIED','reason','Task predates explicit verification planning'));

ALTER TABLE execution_proposals ADD COLUMN work_spec_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE execution_proposals ADD COLUMN verification_spec_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE execution_proposals ADD COLUMN outcome TEXT NOT NULL DEFAULT 'PENDING' CHECK(outcome IN ('PENDING','RUNNING','SUCCEEDED','FAILED','CANCELED'));
ALTER TABLE execution_proposals ADD COLUMN verification_run_id TEXT REFERENCES runs(id);
ALTER TABLE execution_proposals ADD COLUMN failure_code TEXT;
ALTER TABLE execution_proposals ADD COLUMN failure_message TEXT;
UPDATE execution_proposals SET
 work_spec_json=(SELECT work_spec_json FROM task_proposals WHERE id=source_task_proposal_id),
 verification_spec_json=(SELECT verification_spec_json FROM task_proposals WHERE id=source_task_proposal_id),
 outcome=CASE
  WHEN status<>'CONSUMED' THEN 'PENDING'
  WHEN EXISTS(SELECT 1 FROM runs WHERE id=execution_proposals.run_id AND status='SUCCEEDED') THEN 'SUCCEEDED'
  WHEN EXISTS(SELECT 1 FROM runs WHERE id=execution_proposals.run_id AND status='CANCELED') THEN 'CANCELED'
  WHEN EXISTS(SELECT 1 FROM runs WHERE id=execution_proposals.run_id AND status IN ('FAILED','INFRASTRUCTURE_FAILED')) THEN 'FAILED'
  ELSE 'RUNNING' END;

ALTER TABLE runs ADD COLUMN purpose TEXT NOT NULL DEFAULT 'IMPLEMENTATION' CHECK(purpose IN ('IMPLEMENTATION','VERIFICATION'));
ALTER TABLE runs ADD COLUMN execution_proposal_id TEXT REFERENCES execution_proposals(id);
ALTER TABLE runs ADD COLUMN verification_result_json TEXT;
UPDATE runs SET execution_proposal_id=(SELECT id FROM execution_proposals WHERE run_id=runs.id);
CREATE UNIQUE INDEX runs_execution_purpose ON runs(execution_proposal_id,purpose) WHERE execution_proposal_id IS NOT NULL;
CREATE UNIQUE INDEX execution_proposals_verification_run ON execution_proposals(verification_run_id) WHERE verification_run_id IS NOT NULL;

CREATE TRIGGER task_proposals_specs_immutable
BEFORE UPDATE OF work_spec_json,verification_spec_json ON task_proposals
BEGIN
 SELECT RAISE(ABORT, 'task proposal specs are immutable');
END;

CREATE TRIGGER tasks_specs_immutable
BEFORE UPDATE OF work_spec_json,verification_spec_json ON tasks
BEGIN
 SELECT RAISE(ABORT, 'task specs are immutable');
END;

CREATE TRIGGER execution_proposals_specs_immutable
BEFORE UPDATE OF work_spec_json,verification_spec_json ON execution_proposals
BEGIN
 SELECT RAISE(ABORT, 'execution proposal specs are immutable');
END;

CREATE TRIGGER execution_proposals_source_specs_guard
BEFORE INSERT ON execution_proposals
WHEN NOT EXISTS (
 SELECT 1 FROM task_proposals p
 WHERE p.id=NEW.source_task_proposal_id
   AND p.work_spec_json=NEW.work_spec_json
   AND p.verification_spec_json=NEW.verification_spec_json
)
BEGIN
 SELECT RAISE(ABORT, 'execution proposal specs must equal source task proposal specs');
END;

CREATE TRIGGER execution_proposals_verification_run_guard
BEFORE UPDATE OF verification_run_id ON execution_proposals
WHEN NOT (OLD.verification_run_id IS NULL AND NEW.verification_run_id IS NOT NULL AND OLD.status='CONSUMED' AND OLD.outcome='RUNNING' AND
          EXISTS(SELECT 1 FROM runs r WHERE r.id=NEW.verification_run_id AND r.task_id=OLD.task_id AND r.purpose='VERIFICATION' AND r.execution_proposal_id=OLD.id))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution verification run');
END;

CREATE TRIGGER execution_proposals_outcome_transition
BEFORE UPDATE OF outcome ON execution_proposals
WHEN NOT ((OLD.outcome='PENDING' AND NEW.outcome='RUNNING' AND OLD.status='CONFIRMED' AND NEW.status='CONSUMED') OR
          (OLD.outcome='RUNNING' AND NEW.outcome IN ('SUCCEEDED','FAILED','CANCELED')))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution outcome transition');
END;

CREATE TRIGGER runs_execution_identity_immutable
BEFORE UPDATE OF purpose,execution_proposal_id ON runs
BEGIN
 SELECT RAISE(ABORT, 'run execution identity is immutable');
END;
`

const schemaV11 = `
PRAGMA defer_foreign_keys = ON;

ALTER TABLE tasks ADD COLUMN problem_statement TEXT NOT NULL DEFAULT '';
UPDATE tasks SET problem_statement=goal WHERE problem_statement='';
UPDATE tasks SET status='PLANNING' WHERE status='DRAFT';

CREATE TABLE task_plans (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 work_spec_json TEXT NOT NULL,
 verification_spec_json TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('PENDING_CONFIRMATION','CONFIRMED','REJECTED','APPLIED')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 confirmed_at TEXT,
 rejected_at TEXT,
 applied_at TEXT,
 legacy_task_proposal_id TEXT UNIQUE,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((status='PENDING_CONFIRMATION' AND confirmed_at IS NULL AND rejected_at IS NULL AND applied_at IS NULL) OR
       (status='CONFIRMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND applied_at IS NULL) OR
       (status='REJECTED' AND confirmed_at IS NULL AND rejected_at IS NOT NULL AND applied_at IS NULL) OR
       (status='APPLIED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND applied_at IS NOT NULL))
);
CREATE INDEX task_plans_project_created ON task_plans(project_id,created_at DESC,id);
CREATE INDEX task_plans_task_status ON task_plans(task_id,status,created_at,id);
CREATE UNIQUE INDEX task_plans_one_open_per_task ON task_plans(task_id) WHERE status IN ('PENDING_CONFIRMATION','CONFIRMED');

INSERT INTO task_plans(id,ai_session_id,project_id,task_id,work_spec_json,verification_spec_json,status,created_at,updated_at,confirmed_at,applied_at,legacy_task_proposal_id)
SELECT 'task_plan_legacy_' || id,ai_session_id,project_id,result_task_id,work_spec_json,verification_spec_json,'APPLIED',created_at,updated_at,confirmed_at,consumed_at,id
FROM task_proposals WHERE status='CONSUMED' AND result_task_id IS NOT NULL;

ALTER TABLE tasks ADD COLUMN confirmed_task_plan_id TEXT REFERENCES task_plans(id);
UPDATE tasks SET confirmed_task_plan_id=(SELECT id FROM task_plans p WHERE p.task_id=tasks.id AND p.status='APPLIED' ORDER BY p.applied_at DESC,p.id LIMIT 1)
WHERE status<>'PLANNING';

DROP TRIGGER tasks_specs_immutable;
CREATE TRIGGER tasks_specs_immutable
BEFORE UPDATE OF work_spec_json,verification_spec_json ON tasks
WHEN NOT (OLD.status='PLANNING' AND NEW.status='READY' AND OLD.work_spec_json='{}' AND OLD.verification_spec_json='{}' AND NEW.work_spec_json<>'{}' AND NEW.verification_spec_json<>'{}' AND OLD.confirmed_task_plan_id IS NULL AND NEW.confirmed_task_plan_id IS NOT NULL)
BEGIN
 SELECT RAISE(ABORT, 'task specs are immutable outside Task Plan application');
END;

CREATE TRIGGER tasks_problem_immutable
BEFORE UPDATE OF problem_statement ON tasks
WHEN NEW.problem_statement<>OLD.problem_statement
BEGIN
 SELECT RAISE(ABORT, 'task problem statement is immutable');
END;

CREATE TRIGGER task_plans_definition_immutable
BEFORE UPDATE OF ai_session_id,project_id,task_id,work_spec_json,verification_spec_json,legacy_task_proposal_id ON task_plans
BEGIN
 SELECT RAISE(ABORT, 'task plan definition is immutable');
END;

CREATE TRIGGER task_plans_status_transition
BEFORE UPDATE OF status ON task_plans
WHEN NOT ((OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('CONFIRMED','REJECTED')) OR
          (OLD.status='CONFIRMED' AND NEW.status='APPLIED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid task plan status transition');
END;

CREATE TRIGGER task_plans_apply_guard
BEFORE UPDATE OF status ON task_plans
WHEN NEW.status='APPLIED' AND NOT EXISTS (
 SELECT 1 FROM tasks t WHERE t.id=OLD.task_id AND t.project_id=OLD.project_id AND
 t.status='READY' AND t.confirmed_task_plan_id=OLD.id AND
 t.work_spec_json=OLD.work_spec_json AND t.verification_spec_json=OLD.verification_spec_json
)
BEGIN
 SELECT RAISE(ABORT, 'Task Plan must be applied to its Task atomically');
END;

CREATE TABLE execution_proposals_v11 (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 task_plan_id TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('PENDING_CONFIRMATION','CONFIRMED','REJECTED','CONSUMED')),
 run_id TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 confirmed_at TEXT,
 rejected_at TEXT,
 consumed_at TEXT,
 work_spec_json TEXT NOT NULL,
 verification_spec_json TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('PENDING','RUNNING','SUCCEEDED','FAILED','CANCELED')),
 verification_run_id TEXT,
 failure_code TEXT,
 failure_message TEXT,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 FOREIGN KEY(task_plan_id) REFERENCES task_plans(id),
 FOREIGN KEY(run_id,task_id) REFERENCES runs(id,task_id),
 FOREIGN KEY(verification_run_id,task_id) REFERENCES runs(id,task_id),
 UNIQUE(run_id),
 CHECK((status='PENDING_CONFIRMATION' AND confirmed_at IS NULL AND rejected_at IS NULL AND consumed_at IS NULL AND run_id IS NULL) OR
       (status='CONFIRMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NULL AND run_id IS NULL) OR
       (status='REJECTED' AND confirmed_at IS NULL AND rejected_at IS NOT NULL AND consumed_at IS NULL AND run_id IS NULL) OR
       (status='CONSUMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND consumed_at IS NOT NULL AND run_id IS NOT NULL))
);
INSERT INTO execution_proposals_v11(id,ai_session_id,project_id,task_id,task_plan_id,status,run_id,created_at,updated_at,confirmed_at,rejected_at,consumed_at,work_spec_json,verification_spec_json,outcome,verification_run_id,failure_code,failure_message)
SELECT e.id,e.ai_session_id,e.project_id,e.task_id,p.id,e.status,e.run_id,e.created_at,e.updated_at,e.confirmed_at,e.rejected_at,e.consumed_at,e.work_spec_json,e.verification_spec_json,e.outcome,e.verification_run_id,e.failure_code,e.failure_message
FROM execution_proposals e JOIN task_plans p ON p.legacy_task_proposal_id=e.source_task_proposal_id;
DROP TABLE execution_proposals;
ALTER TABLE execution_proposals_v11 RENAME TO execution_proposals;
CREATE INDEX execution_proposals_session_created ON execution_proposals(ai_session_id,created_at DESC,id);
CREATE INDEX execution_proposals_project_status ON execution_proposals(project_id,status,created_at,id);
CREATE INDEX execution_proposals_task_status ON execution_proposals(task_id,status,created_at,id);
CREATE UNIQUE INDEX execution_proposals_verification_run ON execution_proposals(verification_run_id) WHERE verification_run_id IS NOT NULL;

CREATE TRIGGER execution_proposals_task_plan_applied
BEFORE INSERT ON execution_proposals
WHEN NOT EXISTS (
 SELECT 1 FROM task_plans p JOIN tasks t ON t.id=p.task_id
 WHERE p.id=NEW.task_plan_id AND p.project_id=NEW.project_id AND p.task_id=NEW.task_id AND p.status='APPLIED'
   AND t.confirmed_task_plan_id=p.id AND p.work_spec_json=NEW.work_spec_json AND p.verification_spec_json=NEW.verification_spec_json
)
BEGIN
 SELECT RAISE(ABORT, 'execution proposal requires exact applied Task Plan provenance');
END;
CREATE TRIGGER execution_proposals_definition_immutable
BEFORE UPDATE OF ai_session_id,project_id,task_id,task_plan_id,work_spec_json,verification_spec_json ON execution_proposals
BEGIN
 SELECT RAISE(ABORT, 'execution proposal definition is immutable');
END;
CREATE TRIGGER execution_proposals_status_transition
BEFORE UPDATE OF status ON execution_proposals
WHEN NOT ((OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('CONFIRMED','REJECTED')) OR (OLD.status='CONFIRMED' AND NEW.status='CONSUMED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution proposal status transition');
END;
CREATE TRIGGER execution_proposals_verification_run_guard
BEFORE UPDATE OF verification_run_id ON execution_proposals
WHEN NOT (OLD.verification_run_id IS NULL AND NEW.verification_run_id IS NOT NULL AND OLD.status='CONSUMED' AND OLD.outcome='RUNNING' AND
          EXISTS(SELECT 1 FROM runs r WHERE r.id=NEW.verification_run_id AND r.task_id=OLD.task_id AND r.purpose='VERIFICATION' AND r.execution_proposal_id=OLD.id))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution verification run');
END;
CREATE TRIGGER execution_proposals_outcome_transition
BEFORE UPDATE OF outcome ON execution_proposals
WHEN NOT ((OLD.outcome='PENDING' AND NEW.outcome='RUNNING' AND OLD.status='CONFIRMED' AND NEW.status='CONSUMED') OR
          (OLD.outcome='RUNNING' AND NEW.outcome IN ('SUCCEEDED','FAILED','CANCELED')))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution outcome transition');
END;

DROP TRIGGER tasks_module_classification_guard;
DROP TRIGGER execution_proposals_verification_run_guard;

CREATE TABLE runs_v11 (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL REFERENCES tasks(id),
 attempt INTEGER NOT NULL,
 status TEXT NOT NULL,
 worktree_id TEXT,
 thread_id TEXT,
 started_at TEXT,
 finished_at TEXT,
 failure_class TEXT,
 failure_code TEXT,
 failure_stage TEXT,
 failure_message TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 workspace_id TEXT REFERENCES workspaces(id),
 turn_id TEXT,
 start_snapshot TEXT CHECK(start_snapshot IS NULL OR length(start_snapshot) IN (40,64)),
 end_snapshot TEXT CHECK(end_snapshot IS NULL OR length(end_snapshot) IN (40,64)),
 purpose TEXT NOT NULL CHECK(purpose IN ('SCOPE_REVIEW','IMPLEMENTATION','VERIFICATION')),
 execution_proposal_id TEXT REFERENCES execution_proposals(id),
 verification_result_json TEXT,
 codex_instruction TEXT NOT NULL DEFAULT '',
 review_scope_kind TEXT CHECK(review_scope_kind IS NULL OR review_scope_kind IN ('PATHS','REPOSITORY')),
 review_scope_paths_json TEXT NOT NULL DEFAULT '{"version":1,"paths":[]}',
 result_text TEXT,
 UNIQUE(task_id,attempt),
 UNIQUE(id,task_id)
);
INSERT INTO runs_v11(id,task_id,attempt,status,worktree_id,thread_id,started_at,finished_at,failure_class,failure_code,failure_stage,failure_message,created_at,updated_at,workspace_id,turn_id,start_snapshot,end_snapshot,purpose,execution_proposal_id,verification_result_json)
SELECT id,task_id,attempt,status,worktree_id,thread_id,started_at,finished_at,failure_class,failure_code,failure_stage,failure_message,created_at,updated_at,workspace_id,turn_id,start_snapshot,end_snapshot,purpose,execution_proposal_id,verification_result_json FROM runs;
DROP TABLE runs;
ALTER TABLE runs_v11 RENAME TO runs;
CREATE UNIQUE INDEX runs_one_active_per_task ON runs(task_id) WHERE status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING');
CREATE INDEX runs_workspace_attempt ON runs(workspace_id,attempt);
CREATE UNIQUE INDEX runs_thread_turn ON runs(thread_id,turn_id) WHERE thread_id IS NOT NULL AND turn_id IS NOT NULL;
CREATE UNIQUE INDEX runs_execution_purpose ON runs(execution_proposal_id,purpose) WHERE execution_proposal_id IS NOT NULL;

CREATE TRIGGER runs_workspace_task_guard BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND w.task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'run workspace must belong to run task'); END;
CREATE TRIGGER runs_workspace_immutable BEFORE UPDATE OF workspace_id ON runs
WHEN OLD.workspace_id IS NOT NULL AND (NEW.workspace_id IS NULL OR NEW.workspace_id<>OLD.workspace_id)
BEGIN SELECT RAISE(ABORT, 'run workspace is immutable'); END;
CREATE TRIGGER runs_turn_immutable BEFORE UPDATE OF turn_id ON runs
WHEN OLD.turn_id IS NOT NULL AND (NEW.turn_id IS NULL OR NEW.turn_id<>OLD.turn_id)
BEGIN SELECT RAISE(ABORT, 'run turn is immutable'); END;
CREATE TRIGGER runs_snapshots_immutable BEFORE UPDATE OF start_snapshot,end_snapshot ON runs
WHEN (OLD.start_snapshot IS NOT NULL AND (NEW.start_snapshot IS NULL OR NEW.start_snapshot<>OLD.start_snapshot)) OR
     (OLD.end_snapshot IS NOT NULL AND (NEW.end_snapshot IS NULL OR NEW.end_snapshot<>OLD.end_snapshot))
BEGIN SELECT RAISE(ABORT, 'run snapshots are immutable'); END;
CREATE TRIGGER runs_execution_identity_immutable BEFORE UPDATE OF purpose,execution_proposal_id,codex_instruction,review_scope_kind,review_scope_paths_json ON runs
BEGIN SELECT RAISE(ABORT, 'run execution identity is immutable'); END;

CREATE TRIGGER execution_proposals_verification_run_guard
BEFORE UPDATE OF verification_run_id ON execution_proposals
WHEN NOT (OLD.verification_run_id IS NULL AND NEW.verification_run_id IS NOT NULL AND OLD.status='CONSUMED' AND OLD.outcome='RUNNING' AND
          EXISTS(SELECT 1 FROM runs r WHERE r.id=NEW.verification_run_id AND r.task_id=OLD.task_id AND r.purpose='VERIFICATION' AND r.execution_proposal_id=OLD.id))
BEGIN SELECT RAISE(ABORT, 'invalid execution verification run'); END;

CREATE TRIGGER tasks_module_classification_guard
BEFORE UPDATE OF module_id ON tasks
WHEN NEW.module_id<>OLD.module_id AND NOT (
 EXISTS (SELECT 1 FROM modules m WHERE m.id=OLD.module_id AND m.project_id=OLD.project_id AND m.system_owned=1 AND m.key='_unclassified') AND
 EXISTS (SELECT 1 FROM modules m WHERE m.id=NEW.module_id AND m.project_id=OLD.project_id AND m.system_owned=0) AND
 NOT EXISTS (SELECT 1 FROM runs r WHERE r.task_id=OLD.id AND r.status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING'))
)
BEGIN SELECT RAISE(ABORT, 'invalid task module classification'); END;
`

const schemaV12 = `
CREATE TABLE ai_project_selections (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 status TEXT NOT NULL CHECK(status IN ('RECORDED','CONSUMED')),
 ai_session_id TEXT UNIQUE,
 created_at TEXT NOT NULL,
 consumed_at TEXT,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 CHECK((status='RECORDED' AND ai_session_id IS NULL AND consumed_at IS NULL) OR
       (status='CONSUMED' AND ai_session_id IS NOT NULL AND consumed_at IS NOT NULL))
);
CREATE INDEX ai_project_selections_project_created ON ai_project_selections(project_id,created_at DESC,id);

CREATE TRIGGER ai_project_selections_definition_immutable
BEFORE UPDATE OF id,project_id,created_at ON ai_project_selections
BEGIN
 SELECT RAISE(ABORT, 'AI Project selection definition is immutable');
END;

CREATE TRIGGER ai_project_selections_status_transition
BEFORE UPDATE OF status,ai_session_id,consumed_at ON ai_project_selections
WHEN NOT (OLD.status='RECORDED' AND NEW.status='CONSUMED' AND
          OLD.ai_session_id IS NULL AND NEW.ai_session_id IS NOT NULL AND
          OLD.consumed_at IS NULL AND NEW.consumed_at IS NOT NULL)
BEGIN
 SELECT RAISE(ABORT, 'invalid AI Project selection transition');
END;
`

const schemaV13 = `
CREATE TABLE scope_expansions (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 current_scope_kind TEXT NOT NULL CHECK(current_scope_kind IN ('PATHS','REPOSITORY')),
 current_scope_paths_json TEXT NOT NULL CHECK(json_valid(current_scope_paths_json)),
 additional_scope_kind TEXT NOT NULL CHECK(additional_scope_kind='PATHS'),
 additional_scope_paths_json TEXT NOT NULL CHECK(json_valid(additional_scope_paths_json)),
 reason TEXT NOT NULL CHECK(length(reason)>0 AND length(reason)<=16384),
 status TEXT NOT NULL CHECK(status IN ('PENDING_CONFIRMATION','ALLOWED','REJECTED')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 decided_at TEXT,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((status='PENDING_CONFIRMATION' AND decided_at IS NULL) OR
       (status IN ('ALLOWED','REJECTED') AND decided_at IS NOT NULL))
);
CREATE INDEX scope_expansions_project_created ON scope_expansions(project_id,created_at DESC,id);
CREATE INDEX scope_expansions_task_status ON scope_expansions(task_id,status,created_at,id);
CREATE UNIQUE INDEX scope_expansions_one_pending_per_task ON scope_expansions(task_id) WHERE status='PENDING_CONFIRMATION';

CREATE TRIGGER scope_expansions_definition_immutable
BEFORE UPDATE OF id,ai_session_id,project_id,task_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,created_at ON scope_expansions
BEGIN
 SELECT RAISE(ABORT, 'scope expansion definition is immutable');
END;

CREATE TRIGGER scope_expansions_status_transition
BEFORE UPDATE OF status,decided_at ON scope_expansions
WHEN NOT (OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('ALLOWED','REJECTED') AND
          OLD.decided_at IS NULL AND NEW.decided_at IS NOT NULL)
BEGIN
 SELECT RAISE(ABORT, 'invalid scope expansion status transition');
END;
`

// schemaV14 introduces the product-level Topic without renaming the proven
// internal Task lineage. Historical rows are mapped one-to-one using the
// deterministic ID "featureArea_backfill_" || task.id. Topic text and timestamps
// come from the immutable historical Task, and only historically canceled
// Tasks produce canceled FeatureAreas. The non-unique tasks.feature_area_id linkage keeps
// the future Topic -> executable Task * cardinality open.
const schemaV14 = `
CREATE TABLE featureAreas (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 created_by_ai_session_id TEXT,
 title TEXT NOT NULL CHECK(length(title)>0 AND length(title)<=256),
 problem_statement TEXT NOT NULL CHECK(length(problem_statement)>0 AND length(problem_statement)<=16384),
 status TEXT NOT NULL CHECK(status IN ('OPEN','CANCELED')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id)
);
CREATE INDEX featureAreas_project_created ON featureAreas(project_id,created_at DESC,id);

INSERT INTO featureAreas(id,project_id,title,problem_statement,status,created_at,updated_at)
SELECT
 'featureArea_backfill_' || id,
 project_id,
 substr(CASE WHEN trim(problem_statement)<>'' THEN trim(problem_statement) WHEN trim(goal)<>'' THEN trim(goal) ELSE 'Historical development work' END,1,256),
 substr(CASE WHEN trim(problem_statement)<>'' THEN trim(problem_statement) WHEN trim(goal)<>'' THEN trim(goal) ELSE 'Historical development work' END,1,16384),
 CASE WHEN status='CANCELED' THEN 'CANCELED' ELSE 'OPEN' END,
 created_at,
 updated_at
FROM tasks
ORDER BY id;

ALTER TABLE tasks ADD COLUMN feature_area_id TEXT REFERENCES featureAreas(id);
UPDATE tasks SET feature_area_id='featureArea_backfill_' || id;
CREATE INDEX tasks_featureArea_created ON tasks(feature_area_id,created_at,id);

CREATE TRIGGER tasks_feature_area_project_insert_guard
BEFORE INSERT ON tasks
WHEN NEW.feature_area_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM featureAreas x WHERE x.id=NEW.feature_area_id AND x.project_id=NEW.project_id
)
BEGIN SELECT RAISE(ABORT, 'task Topic must belong to task Project'); END;

CREATE TRIGGER tasks_feature_area_project_update_guard
BEFORE UPDATE OF feature_area_id,project_id ON tasks
WHEN NEW.feature_area_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM featureAreas x WHERE x.id=NEW.feature_area_id AND x.project_id=NEW.project_id
)
BEGIN SELECT RAISE(ABORT, 'task Topic must belong to task Project'); END;

CREATE TRIGGER tasks_feature_area_immutable
BEFORE UPDATE OF feature_area_id ON tasks
WHEN NEW.feature_area_id<>OLD.feature_area_id OR (NEW.feature_area_id IS NULL)<>(OLD.feature_area_id IS NULL)
BEGIN SELECT RAISE(ABORT, 'task Topic is immutable'); END;

ALTER TABLE task_plans ADD COLUMN title TEXT NOT NULL DEFAULT '';
UPDATE task_plans SET title=substr(COALESCE(NULLIF(trim(json_extract(work_spec_json,'$.goal')),''),'Executable engineering task'),1,256);
CREATE TRIGGER task_plans_title_immutable
BEFORE UPDATE OF title ON task_plans
WHEN NEW.title<>OLD.title
BEGIN SELECT RAISE(ABORT, 'task title is immutable'); END;
`

// schemaV15 makes one unfinished Execution authorization the durable maximum
// for an executable Task. Historical terminal/rejected records remain valid.
const schemaV15 = `
CREATE UNIQUE INDEX execution_proposals_one_active_per_task_plan
ON execution_proposals(task_plan_id)
WHERE status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED')
  AND outcome IN ('PENDING','RUNNING');
`

// schemaV16 adds the durable Project Module Map, its cheap structural
// fingerprint, and the immutable initial Topic Review grant. ScopeExpansion
// is widened only enough to represent an explicit PATHS -> REPOSITORY grant.
const schemaV16 = `
ALTER TABLE project_contexts ADD COLUMN context_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE project_contexts ADD COLUMN diagnostics_json TEXT NOT NULL DEFAULT '{"version":1,"items":[]}';
ALTER TABLE runs ADD COLUMN review_mode TEXT CHECK(review_mode IS NULL OR review_mode IN ('FOCUSED','REPOSITORY'));
ALTER TABLE runs ADD COLUMN selected_modules_json TEXT NOT NULL DEFAULT '{"version":1,"items":[]}';
DROP TRIGGER runs_execution_identity_immutable;
CREATE TRIGGER runs_execution_identity_immutable BEFORE UPDATE OF purpose,execution_proposal_id,codex_instruction,review_scope_kind,review_scope_paths_json,review_mode,selected_modules_json ON runs
BEGIN SELECT RAISE(ABORT, 'run execution identity is immutable'); END;

CREATE TABLE project_module_map_entries (
 project_id TEXT NOT NULL,
 module_id TEXT NOT NULL,
 repository_id TEXT NOT NULL,
 responsibility TEXT NOT NULL CHECK(length(responsibility)>0 AND length(responsibility)<=4096),
 responsibility_source TEXT NOT NULL CHECK(responsibility_source IN ('GUIDANCE','PACKAGE_DOCUMENTATION','DERIVED')),
 production_roots_json TEXT NOT NULL CHECK(json_valid(production_roots_json)),
 entrypoints_json TEXT NOT NULL CHECK(json_valid(entrypoints_json)),
 related_modules_json TEXT NOT NULL CHECK(json_valid(related_modules_json)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(module_id,repository_id),
 FOREIGN KEY(module_id,project_id) REFERENCES modules(id,project_id),
 FOREIGN KEY(repository_id,project_id) REFERENCES repositories(id,project_id)
);
CREATE INDEX project_module_map_project ON project_module_map_entries(project_id,module_id,repository_id);
CREATE TRIGGER project_module_map_identity_immutable
BEFORE UPDATE OF project_id,module_id,repository_id,created_at ON project_module_map_entries
BEGIN SELECT RAISE(ABORT, 'Project Module Map identity is immutable'); END;

CREATE TABLE review_grants (
 task_id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 repository_id TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('FOCUSED','REPOSITORY')),
 initial_scope_kind TEXT NOT NULL CHECK(initial_scope_kind IN ('PATHS','REPOSITORY')),
 initial_scope_paths_json TEXT NOT NULL CHECK(json_valid(initial_scope_paths_json)),
 selected_modules_json TEXT NOT NULL CHECK(json_valid(selected_modules_json)),
 created_at TEXT NOT NULL,
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 FOREIGN KEY(repository_id,project_id) REFERENCES repositories(id,project_id),
 CHECK((mode='REPOSITORY' AND initial_scope_kind='REPOSITORY') OR (mode='FOCUSED' AND initial_scope_kind='PATHS'))
);
CREATE INDEX review_grants_project ON review_grants(project_id,created_at,task_id);
CREATE TRIGGER review_grants_immutable
BEFORE UPDATE ON review_grants
BEGIN SELECT RAISE(ABORT, 'initial Review grant is immutable'); END;

CREATE TABLE scope_expansions_v16 (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 current_scope_kind TEXT NOT NULL CHECK(current_scope_kind IN ('PATHS','REPOSITORY')),
 current_scope_paths_json TEXT NOT NULL CHECK(json_valid(current_scope_paths_json)),
 additional_scope_kind TEXT NOT NULL CHECK(additional_scope_kind IN ('PATHS','REPOSITORY')),
 additional_scope_paths_json TEXT NOT NULL CHECK(json_valid(additional_scope_paths_json)),
 reason TEXT NOT NULL CHECK(length(reason)>0 AND length(reason)<=16384),
 status TEXT NOT NULL CHECK(status IN ('PENDING_CONFIRMATION','ALLOWED','REJECTED')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 decided_at TEXT,
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((status='PENDING_CONFIRMATION' AND decided_at IS NULL) OR
       (status IN ('ALLOWED','REJECTED') AND decided_at IS NOT NULL)),
 CHECK((additional_scope_kind='REPOSITORY' AND json_type(additional_scope_paths_json,'$.paths')='array' AND json_array_length(additional_scope_paths_json,'$.paths')=0) OR additional_scope_kind='PATHS')
);
INSERT INTO scope_expansions_v16 SELECT * FROM scope_expansions;
DROP TABLE scope_expansions;
ALTER TABLE scope_expansions_v16 RENAME TO scope_expansions;
CREATE INDEX scope_expansions_project_created ON scope_expansions(project_id,created_at DESC,id);
CREATE INDEX scope_expansions_task_status ON scope_expansions(task_id,status,created_at,id);
CREATE UNIQUE INDEX scope_expansions_one_pending_per_task ON scope_expansions(task_id) WHERE status='PENDING_CONFIRMATION';
CREATE TRIGGER scope_expansions_definition_immutable
BEFORE UPDATE OF id,ai_session_id,project_id,task_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,created_at ON scope_expansions
BEGIN SELECT RAISE(ABORT, 'scope expansion definition is immutable'); END;
CREATE TRIGGER scope_expansions_status_transition
BEFORE UPDATE OF status,decided_at ON scope_expansions
WHEN NOT (OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('ALLOWED','REJECTED') AND
          OLD.decided_at IS NULL AND NEW.decided_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'invalid scope expansion status transition'); END;
`

// schemaV17 establishes the product Review boundary. A Review is not a Run:
// both links are nullable until the internal work has been allocated, then the
// store converges them transactionally. Access evidence stores observations,
// never request or response bodies.
const schemaV17 = `
DROP TRIGGER workspaces_thread_immutable;
CREATE TRIGGER workspaces_thread_immutable
BEFORE UPDATE OF thread_id ON workspaces
WHEN OLD.thread_id IS NOT NULL AND (
 NEW.thread_id IS NULL OR (
  NEW.thread_id <> OLD.thread_id AND NOT (
   NEW.status='READY' AND
   NEW.token_baseline_available=1 AND
   NEW.token_input_tokens=0 AND
   NEW.token_cached_input_tokens=0 AND
   NEW.token_cache_write_input_tokens=0 AND
   NEW.token_output_tokens=0 AND
   NEW.token_reasoning_output_tokens=0 AND
   NEW.token_total_tokens=0
  )
 )
)
BEGIN
 SELECT RAISE(ABORT, 'workspace thread replacement must reset its token baseline');
END;
CREATE TABLE reviews (
 id TEXT PRIMARY KEY CHECK(id GLOB 'review_*'),
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT NOT NULL,
 created_by_ai_session_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 linked_run_id TEXT UNIQUE REFERENCES runs(id),
 focus_kind TEXT NOT NULL CHECK(focus_kind IN ('FOCUSED','REPOSITORY')),
 focus_summary TEXT NOT NULL CHECK(length(focus_summary)<=4096),
 focus_paths_json TEXT NOT NULL CHECK(json_valid(focus_paths_json)),
 engineering_goal TEXT NOT NULL CHECK(length(engineering_goal)>0 AND length(engineering_goal)<=16384),
 instruction TEXT NOT NULL CHECK(length(instruction)<=16384),
 final_codex_prompt TEXT,
 prompt_sha256 TEXT CHECK(prompt_sha256 IS NULL OR (length(prompt_sha256)=64 AND prompt_sha256 GLOB '[0-9a-f]*')),
 prompt_bytes INTEGER CHECK(prompt_bytes IS NULL OR prompt_bytes>=0),
 prompt_sent_at TEXT,
 start_failure_code TEXT,
 start_blocker TEXT CHECK(start_blocker IS NULL OR length(start_blocker)<=4096),
 start_failure_at TEXT,
 created_at TEXT NOT NULL,
 UNIQUE(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES featureAreas(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((focus_kind='REPOSITORY' AND json_type(focus_paths_json,'$.paths')='array' AND json_array_length(focus_paths_json,'$.paths')=0) OR focus_kind='FOCUSED'),
 CHECK((final_codex_prompt IS NULL AND prompt_sha256 IS NULL AND prompt_bytes IS NULL AND prompt_sent_at IS NULL) OR (final_codex_prompt IS NOT NULL AND prompt_sha256 IS NOT NULL AND prompt_bytes IS NOT NULL AND prompt_sent_at IS NOT NULL)),
 CHECK((start_failure_code IS NULL AND start_blocker IS NULL AND start_failure_at IS NULL) OR (start_failure_code IS NOT NULL AND start_blocker IS NOT NULL AND start_failure_at IS NOT NULL))
);
CREATE INDEX reviews_featureArea_created ON reviews(feature_area_id,created_at,id);
CREATE INDEX reviews_project_created ON reviews(project_id,created_at,id);
CREATE TRIGGER reviews_identity_immutable BEFORE UPDATE OF id,project_id,feature_area_id,created_by_ai_session_id,task_id,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at ON reviews
BEGIN SELECT RAISE(ABORT, 'review identity is immutable'); END;
CREATE TRIGGER reviews_prompt_immutable BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON reviews
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'review prompt is immutable'); END;
CREATE TRIGGER reviews_linked_run_matches_task BEFORE UPDATE OF linked_run_id ON reviews
WHEN NEW.linked_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.linked_run_id AND task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'review run task mismatch'); END;
ALTER TABLE runs ADD COLUMN review_id TEXT REFERENCES reviews(id);
CREATE UNIQUE INDEX runs_review_id ON runs(review_id) WHERE review_id IS NOT NULL;
CREATE UNIQUE INDEX runs_codex_interaction ON runs(thread_id,turn_id) WHERE thread_id IS NOT NULL AND thread_id<>'' AND turn_id IS NOT NULL AND turn_id<>'';
CREATE TRIGGER runs_review_matches_task BEFORE UPDATE OF review_id ON runs
WHEN NEW.review_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM reviews WHERE id=NEW.review_id AND task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'run review task mismatch'); END;
CREATE TABLE review_access_evidence (
 id TEXT PRIMARY KEY,
 review_id TEXT NOT NULL REFERENCES reviews(id),
 operation TEXT NOT NULL CHECK(length(operation)>0 AND length(operation)<=128),
 outcome TEXT NOT NULL CHECK(outcome IN ('SUCCESS','FORBIDDEN','FAILURE')),
 code TEXT CHECK(code IS NULL OR length(code)<=256),
 recorded_at TEXT NOT NULL
);
CREATE INDEX review_access_evidence_review_observed ON review_access_evidence(review_id,recorded_at,id);
`

const schemaV23 = `
CREATE TABLE review_repository_provenance (
 review_id TEXT PRIMARY KEY REFERENCES reviews(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 reviewed_commit TEXT NOT NULL CHECK(length(reviewed_commit)>0 AND length(reviewed_commit)<=64),
 captured_at TEXT NOT NULL
);
CREATE INDEX review_repository_provenance_repository ON review_repository_provenance(repository_id,reviewed_commit);
CREATE TRIGGER review_repository_provenance_immutable BEFORE UPDATE ON review_repository_provenance
BEGIN SELECT RAISE(ABORT, 'review repository provenance is immutable'); END;
CREATE TRIGGER review_repository_provenance_no_delete AFTER DELETE ON review_repository_provenance
BEGIN SELECT RAISE(ABORT, 'review repository provenance is durable history'); END;
`
