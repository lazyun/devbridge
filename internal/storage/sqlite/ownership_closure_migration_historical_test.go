// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

// v28 closes current ownership while retaining uncertain pre-revision history.
// Historical NULL revision links mean unknown; they are never guessed from a
// mutable Task. SPLITTING has no durable completion marker and is quarantined.
// Rebuilt tables preserve IDs, foreign keys, and all runtime safety triggers.
const schemaV28 = `
DROP TRIGGER agent_conversations_current_lifecycle;

DROP TRIGGER agent_conversations_history_immutable;

DROP TRIGGER agent_conversations_identity_immutable;

DROP TRIGGER agent_conversations_linear_predecessor;

DROP TRIGGER agent_conversations_no_delete;

DROP TRIGGER agent_conversations_thread_immutable;

DROP TRIGGER agent_finalization_applied_once;

DROP TRIGGER agent_finalization_intent_identity;

DROP TRIGGER agent_finalization_intent_no_delete;

DROP TRIGGER agent_operation_entry_immutable;

DROP TRIGGER agent_operation_exit_immutable;

DROP TRIGGER agent_operation_history_no_delete;

DROP TRIGGER agent_operation_recovery_immutable;

DROP TRIGGER agent_operation_recovery_no_delete;

DROP TRIGGER ai_project_selections_definition_immutable;

DROP TRIGGER ai_project_selections_status_transition;

DROP TRIGGER ai_sessions_project_immutable;

DROP TRIGGER claims_runtime_epoch_immutable;

DROP TRIGGER context_probes_definition_immutable;

DROP TRIGGER context_probes_execution_ids_immutable;

DROP TRIGGER context_probes_prompt_immutable;

DROP TRIGGER context_probes_status_transition;

DROP TRIGGER documents_content_immutable;

DROP TRIGGER execution_goal_progress_goal_task_guard;

DROP TRIGGER execution_goal_progress_identity_immutable;

DROP TRIGGER execution_goal_progress_status_transition;

DROP TRIGGER execution_proposals_definition_immutable;

DROP TRIGGER execution_proposals_outcome_transition;

DROP TRIGGER execution_proposals_status_transition;

DROP TRIGGER execution_proposals_task_plan_applied;

DROP TRIGGER execution_proposals_verification_run_guard;

DROP TRIGGER feature_area_agent_busy_lifecycle;

DROP TRIGGER feature_area_agent_end;

DROP TRIGGER feature_area_relations_immutable;

DROP TRIGGER feature_area_relations_no_delete;

DROP TRIGGER modules_system_identity_insert_guard;

DROP TRIGGER modules_system_identity_update_guard;

DROP TRIGGER project_context_probe_project_guard;

DROP TRIGGER project_module_map_identity_immutable;

DROP TRIGGER review_grants_immutable;

DROP TRIGGER review_repository_provenance_immutable;

DROP TRIGGER review_repository_provenance_no_delete;

DROP TRIGGER reviews_identity_immutable;

DROP TRIGGER reviews_linked_run_matches_task;

DROP TRIGGER reviews_prompt_immutable;

DROP TRIGGER runs_execution_identity_immutable;

DROP TRIGGER runs_prompt_immutable;

DROP TRIGGER runs_review_matches_task;

DROP TRIGGER runs_runtime_epoch_immutable;

DROP TRIGGER runs_snapshots_immutable;

DROP TRIGGER runs_task_preparation_identity_immutable;

DROP TRIGGER runs_task_preparation_matches_task_insert;

DROP TRIGGER runs_turn_immutable;

DROP TRIGGER runs_workspace_immutable;

DROP TRIGGER runs_workspace_task_guard;

DROP TRIGGER runtime_epoch_immutable;

DROP TRIGGER runtime_epoch_no_delete;

DROP TRIGGER scope_expansions_definition_immutable;

DROP TRIGGER scope_expansions_status_transition;

DROP TRIGGER task_goals_identity_immutable;

DROP TRIGGER task_goals_no_delete;

DROP TRIGGER task_goals_project_guard;

DROP TRIGGER task_plans_apply_guard;

DROP TRIGGER task_plans_definition_immutable;

DROP TRIGGER task_plans_status_transition;

DROP TRIGGER task_plans_title_immutable;

DROP TRIGGER task_preparations_identity_immutable;

DROP TRIGGER task_preparations_instruction_immutable;

DROP TRIGGER task_preparations_linked_run_matches_task;

DROP TRIGGER task_preparations_prompt_immutable;

DROP TRIGGER task_preparations_source_review_guard;

DROP TRIGGER task_preparations_status_transition;

DROP TRIGGER task_proposals_definition_immutable;

DROP TRIGGER task_proposals_result_task_insert_guard;

DROP TRIGGER task_proposals_result_task_update_guard;

DROP TRIGGER task_proposals_specs_immutable;

DROP TRIGGER task_proposals_status_transition;

DROP TRIGGER tasks_feature_area_immutable;

DROP TRIGGER tasks_feature_area_project_insert_guard;

DROP TRIGGER tasks_feature_area_project_update_guard;

DROP TRIGGER tasks_module_classification_guard;

DROP TRIGGER tasks_problem_immutable;

DROP TRIGGER tasks_specs_immutable;

DROP TRIGGER workspace_runtime_quarantine_immutable;

DROP TRIGGER workspaces_task_immutable;

DROP TRIGGER workspaces_thread_immutable;

DROP TRIGGER workspaces_worktree_immutable;

CREATE TABLE agent_operation_recoveries_v28 (
 request_id TEXT NOT NULL,
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 operation_kind TEXT NOT NULL,
 operation_id TEXT NOT NULL,
 old_conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 new_conversation_id TEXT REFERENCES agent_conversations(id),
 authorized_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(feature_area_id,request_id),
 UNIQUE(run_id)
);
INSERT INTO agent_operation_recoveries_v28 SELECT * FROM agent_operation_recoveries;
DROP TABLE agent_operation_recoveries;
ALTER TABLE agent_operation_recoveries_v28 RENAME TO agent_operation_recoveries;

ALTER TABLE feature_areas ADD COLUMN legacy_read_only INTEGER NOT NULL DEFAULT 0 CHECK(legacy_read_only IN (0,1));
ALTER TABLE feature_areas ADD COLUMN legacy_status TEXT;
UPDATE feature_areas SET legacy_status=status,legacy_read_only=1,status='DISCOVERING' WHERE status='SPLITTING';
UPDATE agent_conversations SET ended_at=(SELECT updated_at FROM feature_areas WHERE id=feature_area_id)
 WHERE ended_at IS NULL AND feature_area_id IN (SELECT id FROM feature_areas WHERE legacy_read_only=1)
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=agent_conversations.feature_area_id);
ALTER TABLE tasks ADD COLUMN legacy_read_only INTEGER NOT NULL DEFAULT 0 CHECK(legacy_read_only IN (0,1));
ALTER TABLE tasks ADD COLUMN current_definition_revision_id TEXT REFERENCES task_definition_revisions(id);
CREATE TABLE legacy_task_audit(task_id TEXT PRIMARY KEY REFERENCES tasks(id),agent_context TEXT,reason TEXT NOT NULL);
INSERT INTO legacy_task_audit(task_id,agent_context,reason)
 SELECT id,agent_context,CASE WHEN feature_area_id IS NULL OR feature_area_id='' THEN 'FEATURE_AREA_UNKNOWN' ELSE 'RETIRED_CONTEXT_SELECTION' END FROM tasks
 WHERE agent_context IS NOT NULL OR feature_area_id IS NULL OR feature_area_id='';
UPDATE tasks SET legacy_read_only=1 WHERE feature_area_id IS NULL OR feature_area_id='';
-- Review carriers were never tagged. Without durable explicit intent evidence,
-- quarantine ambiguous rows; do not delete or relabel them as user Tasks.
INSERT INTO legacy_task_audit(task_id,agent_context,reason)
 SELECT t.id,t.agent_context,'AMBIGUOUS_REVIEW_CARRIER' FROM tasks t
 WHERE EXISTS(SELECT 1 FROM reviews r WHERE r.task_id=t.id)
 AND t.confirmed_task_plan_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM task_plans p WHERE p.task_id=t.id)
 AND NOT EXISTS(SELECT 1 FROM task_preparations p WHERE p.task_id=t.id)
 AND NOT EXISTS(SELECT 1 FROM execution_proposals e WHERE e.task_id=t.id)
 AND NOT EXISTS(SELECT 1 FROM task_goals g WHERE g.task_id=t.id)
 ON CONFLICT(task_id) DO UPDATE SET reason='AMBIGUOUS_REVIEW_CARRIER';
UPDATE tasks SET legacy_read_only=1 WHERE id IN (SELECT task_id FROM legacy_task_audit WHERE reason='AMBIGUOUS_REVIEW_CARRIER');
CREATE TABLE task_definition_revisions (
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>0),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 created_at TEXT NOT NULL, UNIQUE(task_id,ordinal), UNIQUE(task_id,id)
);
ALTER TABLE execution_proposals ADD COLUMN task_definition_revision_id TEXT REFERENCES task_definition_revisions(id);
ALTER TABLE execution_proposals ADD COLUMN task_preparation_id TEXT REFERENCES task_preparations(id);

CREATE TABLE reviews_v28 (
 id TEXT PRIMARY KEY CHECK(id GLOB 'review_*'),
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT NOT NULL,
 created_by_ai_session_id TEXT NOT NULL,
 legacy_task_id TEXT,
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
 created_at TEXT NOT NULL, continuation_attempted_at TEXT, model TEXT
 CHECK(model IS NULL OR model IN ('gpt-5.6-luna','gpt-5.6-terra','gpt-5.6-sol')), reasoning_effort TEXT
 CHECK(reasoning_effort IS NULL OR reasoning_effort IN ('low','medium','high','xhigh')),
 repository_id TEXT REFERENCES repositories(id),
 codex_config_id TEXT REFERENCES codex_configs(id),
 base_commit TEXT,
 next_attempt INTEGER NOT NULL DEFAULT 0,
 UNIQUE(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES "feature_areas"(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(legacy_task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((focus_kind='REPOSITORY' AND json_type(focus_paths_json,'$.paths')='array' AND json_array_length(focus_paths_json,'$.paths')=0) OR focus_kind='FOCUSED'),
 CHECK((final_codex_prompt IS NULL AND prompt_sha256 IS NULL AND prompt_bytes IS NULL AND prompt_sent_at IS NULL) OR (final_codex_prompt IS NOT NULL AND prompt_sha256 IS NOT NULL AND prompt_bytes IS NOT NULL AND prompt_sent_at IS NOT NULL)),
 CHECK((start_failure_code IS NULL AND start_blocker IS NULL AND start_failure_at IS NULL) OR (start_failure_code IS NOT NULL AND start_blocker IS NOT NULL AND start_failure_at IS NOT NULL))
);

INSERT INTO reviews_v28(id,project_id,feature_area_id,created_by_ai_session_id,legacy_task_id,linked_run_id,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at,start_failure_code,start_blocker,start_failure_at,created_at,continuation_attempted_at,model,reasoning_effort) SELECT id,project_id,feature_area_id,created_by_ai_session_id,task_id,linked_run_id,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at,start_failure_code,start_blocker,start_failure_at,created_at,continuation_attempted_at,model,reasoning_effort FROM reviews;

DROP TABLE reviews;

ALTER TABLE reviews_v28 RENAME TO reviews;

CREATE INDEX reviews_feature_area_created ON reviews(feature_area_id,created_at,id);

CREATE INDEX reviews_project_created ON reviews(project_id,created_at,id);

CREATE TABLE runs_v28 (
 id TEXT PRIMARY KEY,
 task_id TEXT REFERENCES tasks(id),
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
 result_text TEXT, review_mode TEXT CHECK(review_mode IS NULL OR review_mode IN ('FOCUSED','REPOSITORY')), selected_modules_json TEXT NOT NULL DEFAULT '{"version":1,"items":[]}', review_id TEXT REFERENCES reviews(id), task_preparation_id TEXT REFERENCES task_preparations(id), final_codex_prompt TEXT, prompt_sha256 TEXT CHECK(prompt_sha256 IS NULL OR (length(prompt_sha256)=64 AND prompt_sha256 GLOB '[0-9a-f]*')), prompt_bytes INTEGER CHECK(prompt_bytes IS NULL OR prompt_bytes>=0), prompt_sent_at TEXT, runtime_epoch TEXT REFERENCES runtime_epochs(id),
 project_id TEXT REFERENCES projects(id),
 feature_area_id TEXT REFERENCES feature_areas(id),
 operation_kind TEXT CHECK(operation_kind IN ('REVIEW','PREPARATION','EXECUTION')),
 operation_id TEXT,
 UNIQUE(task_id,attempt),
 UNIQUE(id,task_id)
);

INSERT INTO runs_v28(id,task_id,attempt,status,worktree_id,thread_id,started_at,finished_at,failure_class,failure_code,failure_stage,failure_message,created_at,updated_at,workspace_id,turn_id,start_snapshot,end_snapshot,purpose,execution_proposal_id,verification_result_json,codex_instruction,review_scope_kind,review_scope_paths_json,result_text,review_mode,selected_modules_json,review_id,task_preparation_id,final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at,runtime_epoch) SELECT id,task_id,attempt,status,worktree_id,thread_id,started_at,finished_at,failure_class,failure_code,failure_stage,failure_message,created_at,updated_at,workspace_id,turn_id,start_snapshot,end_snapshot,purpose,execution_proposal_id,verification_result_json,codex_instruction,review_scope_kind,review_scope_paths_json,result_text,review_mode,selected_modules_json,review_id,task_preparation_id,final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at,runtime_epoch FROM runs;

DROP TABLE runs;

ALTER TABLE runs_v28 RENAME TO runs;

CREATE UNIQUE INDEX runs_codex_interaction ON runs(thread_id,turn_id) WHERE thread_id IS NOT NULL AND thread_id<>'' AND turn_id IS NOT NULL AND turn_id<>'';

CREATE UNIQUE INDEX runs_execution_purpose ON runs(execution_proposal_id,purpose) WHERE execution_proposal_id IS NOT NULL;

CREATE UNIQUE INDEX runs_one_active_per_task ON runs(task_id) WHERE status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING');

CREATE UNIQUE INDEX runs_review_id ON runs(review_id) WHERE review_id IS NOT NULL;

CREATE UNIQUE INDEX runs_task_preparation_id ON runs(task_preparation_id) WHERE task_preparation_id IS NOT NULL;

CREATE UNIQUE INDEX runs_thread_turn ON runs(thread_id,turn_id) WHERE thread_id IS NOT NULL AND turn_id IS NOT NULL;

CREATE INDEX runs_workspace_attempt ON runs(workspace_id,attempt);

CREATE TABLE workspaces_v28 (
 id TEXT PRIMARY KEY,
 task_id TEXT UNIQUE REFERENCES tasks(id),
 worktree_id TEXT UNIQUE REFERENCES worktrees(id),
 legacy_thread_id TEXT,
 status TEXT NOT NULL CHECK(status IN ('READY','CONTINUITY_UNAVAILABLE')),
 token_baseline_available INTEGER NOT NULL CHECK(token_baseline_available IN (0,1)),
 token_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_input_tokens >= 0),
 token_cached_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_cached_input_tokens >= 0),
 token_cache_write_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_cache_write_input_tokens >= 0),
 token_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_output_tokens >= 0),
 token_reasoning_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_reasoning_output_tokens >= 0),
 token_total_tokens INTEGER NOT NULL DEFAULT 0 CHECK(token_total_tokens >= 0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL, runtime_quarantined INTEGER NOT NULL DEFAULT 0 CHECK(runtime_quarantined IN (0,1)),
 review_id TEXT UNIQUE REFERENCES reviews(id),
 CHECK((task_id IS NOT NULL AND review_id IS NULL) OR (task_id IS NULL AND review_id IS NOT NULL)),
 UNIQUE(id,task_id)
);

INSERT INTO workspaces_v28(id,task_id,worktree_id,legacy_thread_id,status,token_baseline_available,token_input_tokens,token_cached_input_tokens,token_cache_write_input_tokens,token_output_tokens,token_reasoning_output_tokens,token_total_tokens,created_at,updated_at,runtime_quarantined) SELECT id,task_id,worktree_id,thread_id,status,token_baseline_available,token_input_tokens,token_cached_input_tokens,token_cache_write_input_tokens,token_output_tokens,token_reasoning_output_tokens,token_total_tokens,created_at,updated_at,runtime_quarantined FROM workspaces;

DROP TABLE workspaces;

ALTER TABLE workspaces_v28 RENAME TO workspaces;

CREATE INDEX workspaces_status_updated ON workspaces(status,updated_at,id);

CREATE INDEX workspaces_thread_history ON workspaces(legacy_thread_id);

CREATE TABLE task_preparations_v28 (
 id TEXT PRIMARY KEY CHECK(id GLOB 'task_preparation_*'),
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT NOT NULL,
 created_by_ai_session_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 source_review_id TEXT,
 linked_run_id TEXT UNIQUE REFERENCES runs(id),
 task_plan_id TEXT UNIQUE REFERENCES task_plans(id),
 title TEXT NOT NULL CHECK(length(title)>0 AND length(title)<=256),
 base_commit TEXT NOT NULL CHECK(length(base_commit) IN (40,64)),
 work_spec_json TEXT NOT NULL CHECK(json_valid(work_spec_json)),
 verification_spec_json TEXT NOT NULL CHECK(json_valid(verification_spec_json)),
 feature_area_review_findings TEXT NOT NULL CHECK(length(feature_area_review_findings)<=65536),
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
 updated_at TEXT NOT NULL, preparation_instruction TEXT NOT NULL DEFAULT '' CHECK(length(preparation_instruction)<=65536),
 ordinal INTEGER NOT NULL DEFAULT 1 CHECK(ordinal>0),
 task_definition_revision_id TEXT REFERENCES task_definition_revisions(id),
 repository_id TEXT REFERENCES repositories(id),
 UNIQUE(task_id,ordinal),
 UNIQUE(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES "feature_areas"(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 FOREIGN KEY(source_review_id,project_id) REFERENCES reviews(id,project_id),
 CHECK((status='READY' AND result_json IS NOT NULL AND task_plan_id IS NOT NULL AND failure_code IS NULL AND failure_summary IS NULL) OR status<>'READY'),
 CHECK((status IN ('CONFLICT','FAILED') AND failure_code IS NOT NULL AND failure_summary IS NOT NULL) OR status NOT IN ('CONFLICT','FAILED')),
 CHECK((final_codex_prompt IS NULL AND prompt_sha256 IS NULL AND prompt_bytes IS NULL AND prompt_sent_at IS NULL) OR (final_codex_prompt IS NOT NULL AND prompt_sha256 IS NOT NULL AND prompt_bytes IS NOT NULL AND prompt_sent_at IS NOT NULL))
);

INSERT INTO task_preparations_v28(id,project_id,feature_area_id,created_by_ai_session_id,task_id,source_review_id,linked_run_id,task_plan_id,title,base_commit,work_spec_json,verification_spec_json,feature_area_review_findings,status,result_json,failure_code,failure_summary,final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at,materialization_attempted_at,continuation_attempted_at,created_at,updated_at,preparation_instruction) SELECT id,project_id,feature_area_id,created_by_ai_session_id,task_id,source_review_id,linked_run_id,task_plan_id,title,base_commit,work_spec_json,verification_spec_json,feature_area_review_findings,status,result_json,failure_code,failure_summary,final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at,materialization_attempted_at,continuation_attempted_at,created_at,updated_at,preparation_instruction FROM task_preparations;

DROP TABLE task_preparations;

ALTER TABLE task_preparations_v28 RENAME TO task_preparations;

CREATE INDEX task_preparations_feature_area_created ON task_preparations(feature_area_id,created_at,id);

CREATE INDEX task_preparations_project_created ON task_preparations(project_id,created_at,id);

UPDATE reviews SET repository_id=(SELECT repository_id FROM tasks WHERE id=reviews.legacy_task_id),
 codex_config_id=(SELECT codex_config_id FROM tasks WHERE id=reviews.legacy_task_id),
 base_commit=(SELECT base_commit FROM tasks WHERE id=reviews.legacy_task_id),
 next_attempt=(SELECT COUNT(*) FROM runs WHERE review_id=reviews.id);
UPDATE task_preparations SET repository_id=(SELECT repository_id FROM tasks WHERE id=task_preparations.task_id);
UPDATE runs SET project_id=COALESCE((SELECT project_id FROM reviews WHERE id=runs.review_id),(SELECT project_id FROM tasks WHERE id=runs.task_id)),
 feature_area_id=COALESCE((SELECT feature_area_id FROM reviews WHERE id=runs.review_id),(SELECT feature_area_id FROM tasks WHERE id=runs.task_id)),
 operation_kind=CASE WHEN review_id IS NOT NULL THEN 'REVIEW' WHEN task_preparation_id IS NOT NULL THEN 'PREPARATION' WHEN execution_proposal_id IS NOT NULL THEN 'EXECUTION' ELSE (SELECT operation_kind FROM feature_area_agent_claims WHERE run_id=runs.id) END,
 operation_id=COALESCE(review_id,task_preparation_id,execution_proposal_id,(SELECT operation_id FROM feature_area_agent_claims WHERE run_id=runs.id));
ALTER TABLE review_grants RENAME TO legacy_review_grants;
CREATE TABLE review_grants (
 review_id TEXT PRIMARY KEY REFERENCES reviews(id), feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 project_id TEXT NOT NULL, repository_id TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('FOCUSED','REPOSITORY')),
 initial_scope_kind TEXT NOT NULL CHECK(initial_scope_kind IN ('PATHS','REPOSITORY')),
 initial_scope_paths_json TEXT NOT NULL CHECK(json_valid(initial_scope_paths_json)),
 selected_modules_json TEXT NOT NULL CHECK(json_valid(selected_modules_json)),
 created_at TEXT NOT NULL,
 FOREIGN KEY(repository_id,project_id) REFERENCES repositories(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES feature_areas(id,project_id),
 CHECK((mode='REPOSITORY' AND initial_scope_kind='REPOSITORY') OR (mode='FOCUSED' AND initial_scope_kind='PATHS'))
);
INSERT INTO review_grants SELECT r.id,r.feature_area_id,g.project_id,g.repository_id,g.mode,g.initial_scope_kind,g.initial_scope_paths_json,g.selected_modules_json,g.created_at FROM reviews r JOIN legacy_review_grants g ON g.task_id=r.legacy_task_id;
CREATE TABLE feature_area_lifecycle_requests (
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 action TEXT NOT NULL CHECK(action IN ('ACTIVATE','DECOMPOSE','RETIRE')),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),created_at TEXT NOT NULL,
 PRIMARY KEY(feature_area_id,request_id)
);

CREATE TRIGGER agent_conversations_current_lifecycle
BEFORE INSERT ON agent_conversations WHEN NEW.ended_at IS NULL AND NOT EXISTS(
 SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE'))
BEGIN SELECT RAISE(ABORT,'Feature Area cannot own current agent context'); END;

CREATE TRIGGER agent_conversations_history_immutable
BEFORE UPDATE ON agent_conversations WHEN OLD.ended_at IS NOT NULL
BEGIN SELECT RAISE(ABORT,'historical agent conversation is immutable'); END;

CREATE TRIGGER agent_conversations_identity_immutable
BEFORE UPDATE OF id,project_id,feature_area_id,backend,host_tool_contract_version,predecessor_id,created_at ON agent_conversations
BEGIN SELECT RAISE(ABORT,'agent conversation identity is immutable'); END;

CREATE TRIGGER agent_conversations_linear_predecessor
BEFORE INSERT ON agent_conversations WHEN NEW.predecessor_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM agent_conversations WHERE id=NEW.predecessor_id AND feature_area_id=NEW.feature_area_id AND ended_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'agent conversation predecessor must be historical in the same Feature Area'); END;

CREATE TRIGGER agent_conversations_no_delete BEFORE DELETE ON agent_conversations
BEGIN SELECT RAISE(ABORT,'agent conversation history cannot be deleted'); END;

CREATE TRIGGER agent_conversations_thread_immutable
BEFORE UPDATE OF backend_thread_id ON agent_conversations
WHEN OLD.backend_thread_id IS NOT NULL AND NEW.backend_thread_id IS NOT OLD.backend_thread_id
BEGIN SELECT RAISE(ABORT,'agent conversation backend thread is immutable'); END;

CREATE TRIGGER agent_finalization_applied_once BEFORE UPDATE OF applied_at ON agent_finalization_intents
WHEN OLD.applied_at IS NOT NULL AND NEW.applied_at IS NOT OLD.applied_at
BEGIN SELECT RAISE(ABORT,'finalization application is immutable'); END;

CREATE TRIGGER agent_finalization_intent_identity BEFORE UPDATE OF run_id,feature_area_id,operation_kind,operation_id,agent_conversation_id,runtime_epoch,advance_state,exit_boundary_turn_id,quiesced,created_at ON agent_finalization_intents
BEGIN SELECT RAISE(ABORT,'finalization intent is immutable'); END;

CREATE TRIGGER agent_finalization_intent_no_delete BEFORE DELETE ON agent_finalization_intents
BEGIN SELECT RAISE(ABORT,'finalization intent history cannot be deleted'); END;

CREATE TRIGGER agent_operation_entry_immutable
BEFORE UPDATE OF run_id,agent_conversation_id,entry_boundary_turn_id ON agent_operation_boundaries
BEGIN SELECT RAISE(ABORT,'agent operation entry provenance is immutable'); END;

CREATE TRIGGER agent_operation_exit_immutable
BEFORE UPDATE ON agent_operation_boundaries WHEN OLD.quiesced=1
BEGIN SELECT RAISE(ABORT,'agent operation exit provenance is immutable'); END;

CREATE TRIGGER agent_operation_history_no_delete BEFORE DELETE ON agent_operation_boundaries
BEGIN SELECT RAISE(ABORT,'agent operation provenance cannot be deleted'); END;

CREATE TABLE scope_expansions_v28 (
 id TEXT PRIMARY KEY,
 ai_session_id TEXT NOT NULL,
 project_id TEXT NOT NULL,
 legacy_task_id TEXT,
 review_id TEXT REFERENCES reviews(id),
 feature_area_id TEXT REFERENCES feature_areas(id),
 repository_id TEXT REFERENCES repositories(id),
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
 FOREIGN KEY(legacy_task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((status='PENDING_CONFIRMATION' AND decided_at IS NULL) OR
       (status IN ('ALLOWED','REJECTED') AND decided_at IS NOT NULL)),
 CHECK((additional_scope_kind='REPOSITORY' AND json_type(additional_scope_paths_json,'$.paths')='array' AND json_array_length(additional_scope_paths_json,'$.paths')=0) OR additional_scope_kind='PATHS')
);
INSERT INTO scope_expansions_v28(id,ai_session_id,project_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,status,created_at,updated_at,decided_at,legacy_task_id) SELECT id,ai_session_id,project_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,status,created_at,updated_at,decided_at,task_id FROM scope_expansions;
DROP TABLE scope_expansions;
ALTER TABLE scope_expansions_v28 RENAME TO scope_expansions;
CREATE INDEX scope_expansions_project_created ON scope_expansions(project_id,created_at DESC,id);
CREATE INDEX scope_expansions_review_status ON scope_expansions(review_id,status,created_at,id);
CREATE UNIQUE INDEX scope_expansions_one_pending_per_review ON scope_expansions(review_id) WHERE status='PENDING_CONFIRMATION';

CREATE TRIGGER agent_operation_recovery_immutable BEFORE UPDATE ON agent_operation_recoveries
BEGIN SELECT RAISE(ABORT,'Agent recovery is immutable'); END;

CREATE TRIGGER agent_operation_recovery_no_delete BEFORE DELETE ON agent_operation_recoveries
BEGIN SELECT RAISE(ABORT,'Agent recovery history cannot be deleted'); END;

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

CREATE TRIGGER ai_sessions_project_immutable
BEFORE UPDATE OF project_id ON ai_sessions
WHEN NEW.project_id <> OLD.project_id
BEGIN
 SELECT RAISE(ABORT, 'AI session project is immutable');
END;

CREATE TRIGGER claims_runtime_epoch_immutable BEFORE UPDATE OF runtime_epoch ON feature_area_agent_claims
WHEN NEW.runtime_epoch IS NOT OLD.runtime_epoch
BEGIN SELECT RAISE(ABORT,'claim runtime epoch is immutable'); END;

CREATE TRIGGER context_probes_definition_immutable
BEFORE UPDATE OF ai_session_id,project_id,repository_id,codex_config_id,scope_kind,scope_paths_json,codex_instruction ON context_probes
BEGIN
 SELECT RAISE(ABORT, 'context probe definition is immutable');
END;

CREATE TRIGGER context_probes_execution_ids_immutable
BEFORE UPDATE OF thread_id,turn_id ON context_probes
WHEN (OLD.thread_id IS NOT NULL AND (NEW.thread_id IS NULL OR NEW.thread_id<>OLD.thread_id)) OR
     (OLD.turn_id IS NOT NULL AND (NEW.turn_id IS NULL OR NEW.turn_id<>OLD.turn_id))
BEGIN
 SELECT RAISE(ABORT, 'context probe execution identifiers are immutable');
END;

CREATE TRIGGER context_probes_prompt_immutable
BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON context_probes
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'Context Probe prompt is immutable'); END;

CREATE TRIGGER context_probes_status_transition
BEFORE UPDATE OF status ON context_probes
WHEN OLD.status<>NEW.status AND
     NOT (OLD.status='CREATED' AND NEW.status='RUNNING') AND
     NOT (OLD.status='RUNNING' AND NEW.status IN ('SUCCEEDED','NO_GUIDANCE','FAILED','CANCELED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid context probe status transition');
END;

CREATE TRIGGER documents_content_immutable
BEFORE UPDATE ON documents
BEGIN SELECT RAISE(ABORT, 'Document is immutable'); END;

CREATE TRIGGER execution_goal_progress_goal_task_guard
BEFORE INSERT ON execution_goal_progress
WHEN NOT EXISTS(
 SELECT 1 FROM task_goals g JOIN execution_proposals e ON e.task_id=g.task_id
 WHERE g.id=NEW.goal_id AND e.id=NEW.execution_proposal_id AND e.project_id=NEW.project_id
)
BEGIN SELECT RAISE(ABORT, 'goal progress requires a goal of the execution Task'); END;

CREATE TRIGGER execution_goal_progress_identity_immutable
BEFORE UPDATE OF execution_proposal_id,goal_id,project_id ON execution_goal_progress
BEGIN SELECT RAISE(ABORT, 'Execution goal progress identity is immutable'); END;

CREATE TRIGGER execution_goal_progress_status_transition
BEFORE UPDATE OF status,updated_at ON execution_goal_progress
WHEN NOT (
 (OLD.status='PENDING' AND NEW.status IN ('RUNNING','COMPLETED','FAILED','SKIPPED')) OR
 (OLD.status='RUNNING' AND NEW.status IN ('COMPLETED','FAILED','SKIPPED')) OR
 (OLD.status=NEW.status)
)
BEGIN SELECT RAISE(ABORT, 'invalid goal progress status transition'); END;

CREATE TRIGGER execution_proposals_definition_immutable
BEFORE UPDATE OF task_definition_revision_id,task_preparation_id,ai_session_id,project_id,task_id,task_plan_id,work_spec_json,verification_spec_json ON execution_proposals
BEGIN
 SELECT RAISE(ABORT, 'execution proposal definition is immutable');
END;

CREATE TRIGGER execution_proposals_outcome_transition
BEFORE UPDATE OF outcome ON execution_proposals
WHEN NOT ((OLD.outcome='PENDING' AND NEW.outcome='RUNNING' AND OLD.status='CONFIRMED' AND NEW.status='CONSUMED') OR
          (OLD.outcome='RUNNING' AND NEW.outcome IN ('SUCCEEDED','FAILED','CANCELED')))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution outcome transition');
END;

CREATE TRIGGER execution_proposals_status_transition
BEFORE UPDATE OF status ON execution_proposals
WHEN NOT ((OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('CONFIRMED','REJECTED')) OR (OLD.status='CONFIRMED' AND NEW.status='CONSUMED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid execution proposal status transition');
END;

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

CREATE TRIGGER execution_proposals_verification_run_guard
BEFORE UPDATE OF verification_run_id ON execution_proposals
WHEN NOT (OLD.verification_run_id IS NULL AND NEW.verification_run_id IS NOT NULL AND OLD.status='CONSUMED' AND OLD.outcome='RUNNING' AND
          EXISTS(SELECT 1 FROM runs r WHERE r.id=NEW.verification_run_id AND r.task_id=OLD.task_id AND r.purpose='VERIFICATION' AND r.execution_proposal_id=OLD.id))
BEGIN SELECT RAISE(ABORT, 'invalid execution verification run'); END;

CREATE TRIGGER feature_area_agent_busy_lifecycle BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status<>OLD.status AND EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_BUSY'); END;

CREATE TRIGGER feature_area_agent_end AFTER UPDATE OF status ON feature_areas
WHEN NEW.status IN ('DECOMPOSED','RETIRED')
BEGIN UPDATE agent_conversations SET ended_at=NEW.updated_at WHERE feature_area_id=NEW.id AND ended_at IS NULL; END;

CREATE TRIGGER feature_area_relations_immutable
BEFORE UPDATE ON feature_area_relations
BEGIN SELECT RAISE(ABORT, 'Feature Area relation is immutable'); END;

CREATE TRIGGER feature_area_relations_no_delete
BEFORE DELETE ON feature_area_relations
BEGIN SELECT RAISE(ABORT, 'Feature Area relation is durable history'); END;

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

CREATE TRIGGER project_context_probe_project_guard
BEFORE UPDATE OF latest_probe_id ON project_contexts
WHEN NEW.latest_probe_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM context_probes p WHERE p.id=NEW.latest_probe_id AND p.project_id=NEW.project_id
)
BEGIN
 SELECT RAISE(ABORT, 'project context probe must belong to project');
END;

CREATE TRIGGER project_module_map_identity_immutable
BEFORE UPDATE OF project_id,module_id,repository_id,created_at ON project_module_map_entries
BEGIN SELECT RAISE(ABORT, 'Project Module Map identity is immutable'); END;

CREATE TRIGGER review_grants_immutable
BEFORE UPDATE ON review_grants
BEGIN SELECT RAISE(ABORT, 'initial Review grant is immutable'); END;

CREATE TRIGGER review_repository_provenance_immutable BEFORE UPDATE ON review_repository_provenance
BEGIN SELECT RAISE(ABORT, 'review repository provenance is immutable'); END;

CREATE TRIGGER review_repository_provenance_no_delete AFTER DELETE ON review_repository_provenance
BEGIN SELECT RAISE(ABORT, 'review repository provenance is durable history'); END;

CREATE TRIGGER reviews_identity_immutable BEFORE UPDATE OF id,project_id,feature_area_id,created_by_ai_session_id,legacy_task_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at ON reviews
BEGIN SELECT RAISE(ABORT, 'review identity is immutable'); END;

CREATE TRIGGER reviews_prompt_immutable BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON reviews
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'review prompt is immutable'); END;

CREATE TRIGGER runs_execution_identity_immutable BEFORE UPDATE OF purpose,execution_proposal_id,codex_instruction,review_scope_kind,review_scope_paths_json,review_mode,selected_modules_json ON runs
BEGIN SELECT RAISE(ABORT, 'run execution identity is immutable'); END;

CREATE TRIGGER runs_prompt_immutable
BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON runs
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'Run prompt is immutable'); END;

CREATE TRIGGER runs_runtime_epoch_immutable BEFORE UPDATE OF runtime_epoch ON runs
WHEN NEW.runtime_epoch IS NOT OLD.runtime_epoch
BEGIN SELECT RAISE(ABORT,'Run runtime epoch is immutable'); END;

CREATE TRIGGER runs_snapshots_immutable BEFORE UPDATE OF start_snapshot,end_snapshot ON runs
WHEN (OLD.start_snapshot IS NOT NULL AND (NEW.start_snapshot IS NULL OR NEW.start_snapshot<>OLD.start_snapshot)) OR
     (OLD.end_snapshot IS NOT NULL AND (NEW.end_snapshot IS NULL OR NEW.end_snapshot<>OLD.end_snapshot))
BEGIN SELECT RAISE(ABORT, 'run snapshots are immutable'); END;

CREATE TRIGGER runs_task_preparation_identity_immutable BEFORE UPDATE OF task_preparation_id ON runs
WHEN OLD.task_preparation_id IS NOT NULL OR NEW.task_preparation_id IS NULL
BEGIN SELECT RAISE(ABORT, 'Run Task Preparation identity is immutable'); END;

CREATE TRIGGER runs_task_preparation_matches_task_insert BEFORE INSERT ON runs
WHEN NEW.task_preparation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM task_preparations WHERE id=NEW.task_preparation_id AND task_id=NEW.task_id AND status='PREPARING')
BEGIN SELECT RAISE(ABORT, 'Run Task Preparation mismatch'); END;

CREATE TRIGGER runs_turn_immutable BEFORE UPDATE OF turn_id ON runs
WHEN OLD.turn_id IS NOT NULL AND (NEW.turn_id IS NULL OR NEW.turn_id<>OLD.turn_id)
BEGIN SELECT RAISE(ABORT, 'run turn is immutable'); END;

CREATE TRIGGER runs_workspace_immutable BEFORE UPDATE OF workspace_id ON runs
WHEN OLD.workspace_id IS NOT NULL AND (NEW.workspace_id IS NULL OR NEW.workspace_id<>OLD.workspace_id)
BEGIN SELECT RAISE(ABORT, 'run workspace is immutable'); END;

CREATE TRIGGER runtime_epoch_immutable BEFORE UPDATE ON runtime_epochs
BEGIN SELECT RAISE(ABORT,'runtime epoch is immutable'); END;

CREATE TRIGGER runtime_epoch_no_delete BEFORE DELETE ON runtime_epochs
BEGIN SELECT RAISE(ABORT,'runtime epoch history cannot be deleted'); END;

CREATE TRIGGER scope_expansions_definition_immutable
BEFORE UPDATE OF id,ai_session_id,project_id,review_id,feature_area_id,repository_id,legacy_task_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,created_at ON scope_expansions
BEGIN SELECT RAISE(ABORT, 'scope expansion definition is immutable'); END;

CREATE TRIGGER scope_expansions_status_transition
BEFORE UPDATE OF status,decided_at ON scope_expansions
WHEN NOT (OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('ALLOWED','REJECTED') AND
          OLD.decided_at IS NULL AND NEW.decided_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'invalid scope expansion status transition'); END;

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

CREATE TRIGGER task_plans_title_immutable
BEFORE UPDATE OF title ON task_plans
WHEN NEW.title<>OLD.title
BEGIN SELECT RAISE(ABORT, 'task title is immutable'); END;

CREATE TRIGGER task_preparations_identity_immutable
BEFORE UPDATE OF ordinal,task_definition_revision_id,repository_id,id,project_id,feature_area_id,created_by_ai_session_id,task_id,source_review_id,title,base_commit,work_spec_json,verification_spec_json,feature_area_review_findings,created_at ON task_preparations
BEGIN SELECT RAISE(ABORT, 'Task Preparation identity and inputs are immutable'); END;

CREATE TRIGGER task_preparations_instruction_immutable
BEFORE UPDATE OF preparation_instruction ON task_preparations
BEGIN SELECT RAISE(ABORT, 'Task Preparation instruction is immutable'); END;

CREATE TRIGGER task_preparations_linked_run_matches_task
BEFORE UPDATE OF linked_run_id ON task_preparations
WHEN NEW.linked_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.linked_run_id AND task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'Task Preparation Run task mismatch'); END;

CREATE TRIGGER task_preparations_prompt_immutable
BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON task_preparations
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'Task Preparation prompt is immutable'); END;

CREATE TRIGGER task_preparations_status_transition
BEFORE UPDATE OF status ON task_preparations
WHEN NOT ((OLD.status='DRAFT' AND NEW.status='PREPARING') OR (OLD.status='PREPARING' AND NEW.status IN ('READY','CONFLICT','FAILED')))
BEGIN SELECT RAISE(ABORT, 'invalid Task Preparation status transition'); END;

CREATE TRIGGER task_proposals_definition_immutable
BEFORE UPDATE OF ai_session_id,project_id,kind,module_id,repository_id,target_task_id,codex_config_id,goal,constraints_json,base_revision ON task_proposals
BEGIN
 SELECT RAISE(ABORT, 'task proposal definition is immutable');
END;

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

CREATE TRIGGER task_proposals_specs_immutable
BEFORE UPDATE OF work_spec_json,verification_spec_json ON task_proposals
BEGIN
 SELECT RAISE(ABORT, 'task proposal specs are immutable');
END;

CREATE TRIGGER task_proposals_status_transition
BEFORE UPDATE OF status ON task_proposals
WHEN NOT ((OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('CONFIRMED','REJECTED')) OR
          (OLD.status='CONFIRMED' AND NEW.status='CONSUMED'))
BEGIN
 SELECT RAISE(ABORT, 'invalid task proposal status transition');
END;

CREATE TRIGGER tasks_feature_area_immutable
BEFORE UPDATE OF feature_area_id ON tasks
WHEN NEW.feature_area_id<>OLD.feature_area_id OR (NEW.feature_area_id IS NULL)<>(OLD.feature_area_id IS NULL)
BEGIN SELECT RAISE(ABORT, 'task Feature Area is immutable'); END;

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

CREATE TRIGGER tasks_module_classification_guard
BEFORE UPDATE OF module_id ON tasks
WHEN NEW.module_id<>OLD.module_id AND NOT (
 EXISTS (SELECT 1 FROM modules m WHERE m.id=OLD.module_id AND m.project_id=OLD.project_id AND m.system_owned=1 AND m.key='_unclassified') AND
 EXISTS (SELECT 1 FROM modules m WHERE m.id=NEW.module_id AND m.project_id=OLD.project_id AND m.system_owned=0) AND
 NOT EXISTS (SELECT 1 FROM runs r WHERE r.task_id=OLD.id AND r.status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING'))
)
BEGIN SELECT RAISE(ABORT, 'invalid task module classification'); END;

CREATE TRIGGER tasks_problem_immutable
BEFORE UPDATE OF problem_statement ON tasks
WHEN NEW.problem_statement<>OLD.problem_statement
BEGIN
 SELECT RAISE(ABORT, 'task problem statement is immutable');
END;

CREATE TRIGGER tasks_specs_immutable BEFORE UPDATE OF work_spec_json,verification_spec_json ON tasks
WHEN (NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.verification_spec_json IS NOT OLD.verification_spec_json)
AND EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=NEW.feature_area_id)
BEGIN SELECT RAISE(ABORT,'Task definition cannot change during an Agent operation'); END;

CREATE TRIGGER workspace_runtime_quarantine_immutable BEFORE UPDATE OF runtime_quarantined ON workspaces
WHEN OLD.runtime_quarantined=1 AND NEW.runtime_quarantined<>1
BEGIN SELECT RAISE(ABORT,'abandoned runtime workspace must remain isolated'); END;

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

CREATE TRIGGER feature_areas_current_insert BEFORE INSERT ON feature_areas
WHEN NEW.legacy_read_only<>0 OR NEW.legacy_status IS NOT NULL OR NEW.status NOT IN ('DISCOVERING','ACTIVE')
BEGIN SELECT RAISE(ABORT,'new FeatureArea must be current'); END;
CREATE TRIGGER feature_areas_lifecycle_transition BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status<>OLD.status AND NOT (
 OLD.legacy_read_only=0 AND ((OLD.status='DISCOVERING' AND NEW.status IN ('ACTIVE','DECOMPOSED')) OR (OLD.status='ACTIVE' AND NEW.status='RETIRED')))
BEGIN SELECT RAISE(ABORT,'invalid FeatureArea lifecycle transition'); END;
CREATE TRIGGER feature_areas_decompose_guard BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status='DECOMPOSED' AND (EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=NEW.id AND legacy_read_only=0)
 OR NOT EXISTS(SELECT 1 FROM feature_area_relations WHERE source_feature_area_id=NEW.id))
BEGIN SELECT RAISE(ABORT,'FeatureArea decomposition requires children and zero product Tasks'); END;
CREATE TRIGGER tasks_current_insert BEFORE INSERT ON tasks
WHEN NEW.legacy_read_only<>0 OR NEW.agent_context IS NOT NULL OR NOT EXISTS(
 SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND project_id=NEW.project_id AND status='ACTIVE' AND legacy_read_only=0)
BEGIN SELECT RAISE(ABORT,'Task requires ACTIVE FeatureArea in expected Project'); END;
CREATE TRIGGER tasks_legacy_read_only BEFORE UPDATE ON tasks WHEN OLD.legacy_read_only=1
BEGIN SELECT RAISE(ABORT,'legacy Task is read-only'); END;
CREATE TRIGGER tasks_legacy_context_immutable BEFORE UPDATE OF agent_context,legacy_read_only ON tasks
WHEN NEW.agent_context IS NOT OLD.agent_context OR NEW.legacy_read_only IS NOT OLD.legacy_read_only
BEGIN SELECT RAISE(ABORT,'legacy Task context and compatibility status are immutable'); END;
CREATE TRIGGER task_revision_pointer_guard BEFORE UPDATE OF current_definition_revision_id ON tasks
WHEN NEW.current_definition_revision_id IS NULL OR NOT EXISTS(SELECT 1 FROM task_definition_revisions WHERE id=NEW.current_definition_revision_id AND task_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'Task definition revision must belong to Task'); END;
CREATE TRIGGER task_definition_revisions_immutable BEFORE UPDATE ON task_definition_revisions
BEGIN SELECT RAISE(ABORT,'Task definition revision is immutable'); END;
CREATE TRIGGER task_definition_revisions_no_delete BEFORE DELETE ON task_definition_revisions
BEGIN SELECT RAISE(ABORT,'Task definition revision is durable history'); END;
CREATE TRIGGER reviews_current_owner BEFORE INSERT ON reviews
WHEN NEW.legacy_task_id IS NOT NULL OR NEW.repository_id IS NULL OR NEW.codex_config_id IS NULL OR length(COALESCE(NEW.base_commit,'')) NOT IN (40,64)
 OR NOT EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND project_id=NEW.project_id AND legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE'))
 OR NOT EXISTS(SELECT 1 FROM repositories WHERE id=NEW.repository_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Review requires current FeatureArea and repository ownership'); END;
CREATE TRIGGER reviews_linked_run_owner BEFORE UPDATE OF linked_run_id ON reviews
WHEN NEW.linked_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.linked_run_id AND review_id=NEW.id AND operation_kind='REVIEW' AND operation_id=NEW.id AND feature_area_id=NEW.feature_area_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Review Run owner mismatch'); END;
CREATE TRIGGER runs_operation_owner_insert BEFORE INSERT ON runs
WHEN NEW.operation_id IS NULL OR NEW.operation_id='' OR NEW.operation_kind IS NULL OR NOT EXISTS(
 SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND project_id=NEW.project_id AND legacy_read_only=0
 AND ((NEW.operation_kind='REVIEW' AND status IN ('DISCOVERING','ACTIVE')) OR (NEW.operation_kind IN ('PREPARATION','EXECUTION') AND status='ACTIVE')))
 OR NOT COALESCE((
 (NEW.operation_kind='REVIEW' AND NEW.review_id=NEW.operation_id AND NEW.task_id IS NULL AND NEW.task_preparation_id IS NULL AND NEW.execution_proposal_id IS NULL AND NEW.purpose='SCOPE_REVIEW' AND EXISTS(SELECT 1 FROM reviews WHERE id=NEW.operation_id AND feature_area_id=NEW.feature_area_id AND project_id=NEW.project_id))
 OR (NEW.operation_kind='PREPARATION' AND NEW.task_preparation_id=NEW.operation_id AND NEW.review_id IS NULL AND NEW.execution_proposal_id IS NULL AND NEW.purpose='SCOPE_REVIEW' AND EXISTS(SELECT 1 FROM task_preparations p JOIN tasks t ON t.id=p.task_id WHERE p.id=NEW.operation_id AND p.task_id=NEW.task_id AND p.feature_area_id=NEW.feature_area_id AND p.project_id=NEW.project_id AND p.task_definition_revision_id IS NOT NULL AND t.legacy_read_only=0))
 OR (NEW.operation_kind='EXECUTION' AND NEW.execution_proposal_id=NEW.operation_id AND NEW.review_id IS NULL AND NEW.task_preparation_id IS NULL AND NEW.purpose IN ('IMPLEMENTATION','VERIFICATION') AND EXISTS(SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id WHERE e.id=NEW.operation_id AND e.task_id=NEW.task_id AND t.feature_area_id=NEW.feature_area_id AND e.project_id=NEW.project_id AND e.task_definition_revision_id IS NOT NULL AND t.legacy_read_only=0))),0)
BEGIN SELECT RAISE(ABORT,'engineering Run requires exact FeatureArea operation owner'); END;
CREATE TRIGGER runs_operation_owner_immutable BEFORE UPDATE OF project_id,feature_area_id,operation_kind,operation_id,task_id,review_id,task_preparation_id ON runs
WHEN NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.operation_kind IS NOT OLD.operation_kind OR NEW.operation_id IS NOT OLD.operation_id OR NEW.task_id IS NOT OLD.task_id OR NEW.review_id IS NOT OLD.review_id OR NEW.task_preparation_id IS NOT OLD.task_preparation_id
BEGIN SELECT RAISE(ABORT,'Run operation owner is immutable'); END;


CREATE TRIGGER task_proposals_retired_insert BEFORE INSERT ON task_proposals
BEGIN SELECT RAISE(ABORT,'legacy TaskProposal authoring is retired'); END;
CREATE TRIGGER task_proposals_retired_update BEFORE UPDATE ON task_proposals
BEGIN SELECT RAISE(ABORT,'legacy TaskProposal history is read-only'); END;
CREATE TRIGGER task_proposals_retired_delete BEFORE DELETE ON task_proposals
BEGIN SELECT RAISE(ABORT,'legacy TaskProposal history is durable'); END;

CREATE TRIGGER scope_expansions_current_owner BEFORE INSERT ON scope_expansions
WHEN NEW.legacy_task_id IS NOT NULL OR NEW.review_id IS NULL OR NOT EXISTS(
 SELECT 1 FROM reviews r JOIN review_grants g ON g.review_id=r.id JOIN feature_areas f ON f.id=r.feature_area_id
 WHERE r.id=NEW.review_id AND r.project_id=NEW.project_id AND r.feature_area_id=NEW.feature_area_id AND r.repository_id=NEW.repository_id
 AND g.project_id=NEW.project_id AND g.feature_area_id=NEW.feature_area_id AND g.repository_id=NEW.repository_id
 AND f.legacy_read_only=0 AND f.status IN ('DISCOVERING','ACTIVE'))
BEGIN SELECT RAISE(ABORT,'scope expansion requires exact current Review grant'); END;
CREATE TRIGGER scope_expansions_legacy_readonly BEFORE UPDATE ON scope_expansions WHEN OLD.legacy_task_id IS NOT NULL
BEGIN SELECT RAISE(ABORT,'legacy scope expansion is read-only'); END;
CREATE TRIGGER scope_expansions_no_delete BEFORE DELETE ON scope_expansions
BEGIN SELECT RAISE(ABORT,'scope expansion is durable history'); END;

CREATE TRIGGER claims_exact_owner_insert BEFORE INSERT ON feature_area_agent_claims
WHEN NOT EXISTS(SELECT 1 FROM runs r JOIN feature_areas f ON f.id=r.feature_area_id WHERE r.id=NEW.run_id AND r.feature_area_id=NEW.feature_area_id AND r.operation_kind=NEW.operation_kind AND r.operation_id=NEW.operation_id AND r.runtime_epoch IS NEW.runtime_epoch AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND r.status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING') AND f.legacy_read_only=0 AND ((r.operation_kind='REVIEW' AND f.status IN ('DISCOVERING','ACTIVE')) OR (r.operation_kind IN ('PREPARATION','EXECUTION') AND f.status='ACTIVE')))
BEGIN SELECT RAISE(ABORT,'claim must match exact Run owner and epoch'); END;
CREATE TRIGGER claims_exact_owner_update BEFORE UPDATE OF run_id,feature_area_id,operation_kind,operation_id ON feature_area_agent_claims
WHEN NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.run_id AND feature_area_id=NEW.feature_area_id AND operation_kind=NEW.operation_kind AND operation_id=NEW.operation_id AND runtime_epoch IS NEW.runtime_epoch)
BEGIN SELECT RAISE(ABORT,'claim must match exact Run owner and epoch'); END;
CREATE TRIGGER task_preparations_current_definition BEFORE INSERT ON task_preparations
WHEN NEW.task_definition_revision_id IS NULL OR NOT EXISTS(
 SELECT 1 FROM tasks t JOIN task_definition_revisions d ON d.task_id=t.id JOIN feature_areas f ON f.id=t.feature_area_id
 WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.repository_id=NEW.repository_id
 AND t.legacy_read_only=0 AND f.legacy_read_only=0 AND f.status='ACTIVE' AND d.id=NEW.task_definition_revision_id
 AND json_extract(d.payload_json,'$.baseCommit')=NEW.base_commit)
BEGIN SELECT RAISE(ABORT,'Preparation requires exact Task definition and ACTIVE FeatureArea'); END;
CREATE TRIGGER task_preparations_source_review_guard BEFORE INSERT ON task_preparations
WHEN NEW.source_review_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM reviews WHERE id=NEW.source_review_id AND project_id=NEW.project_id AND feature_area_id=NEW.feature_area_id)
BEGIN SELECT RAISE(ABORT,'source Review must belong to Preparation FeatureArea'); END;
CREATE TRIGGER task_preparations_final_result_immutable BEFORE UPDATE OF status,result_json,task_plan_id,failure_code,failure_summary ON task_preparations
WHEN OLD.status IN ('READY','CONFLICT','FAILED') AND (NEW.status IS NOT OLD.status OR NEW.result_json IS NOT OLD.result_json OR NEW.task_plan_id IS NOT OLD.task_plan_id OR NEW.failure_code IS NOT OLD.failure_code OR NEW.failure_summary IS NOT OLD.failure_summary)
BEGIN SELECT RAISE(ABORT,'final Preparation is immutable'); END;
CREATE TRIGGER execution_proposals_exact_preparation BEFORE INSERT ON execution_proposals
WHEN NEW.task_definition_revision_id IS NULL OR NEW.task_preparation_id IS NULL OR NOT EXISTS(
 SELECT 1 FROM task_preparations p JOIN tasks t ON t.id=p.task_id JOIN feature_areas f ON f.id=t.feature_area_id
 WHERE p.id=NEW.task_preparation_id AND p.task_id=NEW.task_id AND p.project_id=NEW.project_id AND p.status='READY'
 AND p.task_plan_id=NEW.task_plan_id AND p.task_definition_revision_id=NEW.task_definition_revision_id AND t.legacy_read_only=0 AND f.legacy_read_only=0 AND f.status='ACTIVE')
BEGIN SELECT RAISE(ABORT,'Execution requires exact ready Preparation and definition'); END;
CREATE TRIGGER runs_workspace_owner_guard BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND ((NEW.operation_kind='REVIEW' AND w.review_id=NEW.review_id AND w.task_id IS NULL) OR (NEW.operation_kind<>'REVIEW' AND w.task_id=NEW.task_id AND w.review_id IS NULL)))
BEGIN SELECT RAISE(ABORT,'Run workspace must match operation environment'); END;
CREATE TRIGGER workspaces_review_immutable BEFORE UPDATE OF review_id ON workspaces WHEN NEW.review_id IS NOT OLD.review_id
BEGIN SELECT RAISE(ABORT,'Workspace Review owner is immutable'); END;
CREATE TRIGGER workspaces_legacy_thread_immutable BEFORE UPDATE OF legacy_thread_id ON workspaces WHEN NEW.legacy_thread_id IS NOT OLD.legacy_thread_id
BEGIN SELECT RAISE(ABORT,'legacy Workspace thread is audit history'); END;
CREATE TRIGGER workspaces_no_current_thread BEFORE INSERT ON workspaces WHEN NEW.legacy_thread_id IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Workspace cannot own canonical Agent thread'); END;
CREATE TRIGGER feature_area_lifecycle_requests_immutable BEFORE UPDATE ON feature_area_lifecycle_requests
BEGIN SELECT RAISE(ABORT,'lifecycle request receipt is immutable'); END;
CREATE TRIGGER feature_area_lifecycle_requests_no_delete BEFORE DELETE ON feature_area_lifecycle_requests
BEGIN SELECT RAISE(ABORT,'lifecycle request receipt is durable history'); END;

CREATE TRIGGER feature_areas_legacy_immutable BEFORE UPDATE OF legacy_read_only,legacy_status ON feature_areas
WHEN NEW.legacy_read_only IS NOT OLD.legacy_read_only OR NEW.legacy_status IS NOT OLD.legacy_status
BEGIN SELECT RAISE(ABORT,'legacy FeatureArea compatibility state is immutable'); END;
CREATE TRIGGER review_grants_exact_owner BEFORE INSERT ON review_grants
WHEN NOT EXISTS(SELECT 1 FROM reviews WHERE id=NEW.review_id AND project_id=NEW.project_id AND feature_area_id=NEW.feature_area_id AND repository_id=NEW.repository_id)
BEGIN SELECT RAISE(ABORT,'Review grant must match exact Review owner'); END;
CREATE TRIGGER legacy_review_grants_no_update BEFORE UPDATE ON legacy_review_grants
BEGIN SELECT RAISE(ABORT,'legacy grant is audit history'); END;
CREATE TRIGGER legacy_task_audit_no_update BEFORE UPDATE ON legacy_task_audit
BEGIN SELECT RAISE(ABORT,'legacy Task audit is immutable'); END;
`
