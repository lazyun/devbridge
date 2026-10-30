PRAGMA foreign_keys=OFF;
-- Historical validation fixture: genuine v26 sample advanced through the original v41 migrations.
-- Never loaded by production; disposed entities contain no records.
BEGIN TRANSACTION;
PRAGMA defer_foreign_keys=ON;
CREATE TABLE agent_context_resets (
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 PRIMARY KEY(feature_area_id,request_id)
);
CREATE TABLE agent_conversations (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL,
 feature_area_id TEXT NOT NULL,
 backend TEXT NOT NULL CHECK(backend='CODEX'),
 backend_thread_id TEXT UNIQUE,
 host_tool_contract_version INTEGER NOT NULL CHECK(host_tool_contract_version>0),
 current_boundary_turn_id TEXT,
 continuation_safe INTEGER NOT NULL DEFAULT 1 CHECK(continuation_safe IN (0,1)),
 predecessor_id TEXT UNIQUE REFERENCES agent_conversations(id),
 rotation_reason TEXT CHECK(rotation_reason IN ('EXPLICIT_RESET','BACKEND_THREAD_UNAVAILABLE','HOST_TOOL_CONTRACT_UPGRADE','UNSAFE_CONTEXT_BOUNDARY')),
 created_at TEXT NOT NULL,
 ended_at TEXT,
 UNIQUE(id,feature_area_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES feature_areas(id,project_id),
 CHECK(rotation_reason IS NULL OR ended_at IS NOT NULL),
 CHECK(current_boundary_turn_id IS NULL OR backend_thread_id IS NOT NULL)
);
CREATE TABLE agent_finalization_intents (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 operation_kind TEXT NOT NULL,
 operation_id TEXT NOT NULL,
 agent_conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 advance_state TEXT NOT NULL CHECK(advance_state IN ('NO_ADVANCE','KNOWN_ADVANCE','UNKNOWN')),
 exit_boundary_turn_id TEXT,
 quiesced INTEGER NOT NULL CHECK(quiesced IN (0,1)),
 created_at TEXT NOT NULL,
 applied_at TEXT,
 CHECK((advance_state='KNOWN_ADVANCE' AND exit_boundary_turn_id IS NOT NULL AND quiesced=1) OR
       (advance_state IN ('NO_ADVANCE','UNKNOWN') AND exit_boundary_turn_id IS NULL))
);
CREATE TABLE agent_operation_boundaries (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 agent_conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 entry_boundary_turn_id TEXT,
 exit_boundary_turn_id TEXT,
 advance_state TEXT NOT NULL CHECK(advance_state IN ('NO_ADVANCE','KNOWN_ADVANCE','UNKNOWN')),
 quiesced INTEGER NOT NULL DEFAULT 0 CHECK(quiesced IN (0,1)), thread_start_pending INTEGER NOT NULL DEFAULT 0 CHECK(thread_start_pending IN (0,1)),
 CHECK((advance_state='KNOWN_ADVANCE' AND exit_boundary_turn_id IS NOT NULL) OR
       (advance_state IN ('NO_ADVANCE','UNKNOWN') AND exit_boundary_turn_id IS NULL))
);
CREATE TABLE "agent_operation_recoveries" (
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
CREATE TABLE agent_operation_recovery_states (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 classification TEXT NOT NULL CHECK(classification IN ('NO_ADVANCE','KNOWN_SAFE','UNKNOWN','CONFLICT')),
 detail TEXT NOT NULL,
 classified_epoch TEXT NOT NULL REFERENCES runtime_epochs(id)
);
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
CREATE TABLE agent_process_launches (
 launch_id TEXT PRIMARY KEY,
 pid INTEGER NOT NULL CHECK(pid>0),
 executable TEXT NOT NULL,
 started_at TEXT NOT NULL,
 ended_at TEXT,
 exit_code INTEGER,
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id)
);
CREATE TABLE agent_runtime_states (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 connection_id TEXT NOT NULL UNIQUE,
 thread_id TEXT NOT NULL,
 turn_id TEXT NOT NULL,
 sequence INTEGER NOT NULL CHECK(sequence>0),
 snapshot_json TEXT NOT NULL CHECK(json_valid(snapshot_json)),
 updated_at TEXT NOT NULL
);
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
CREATE TABLE ai_sessions (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
INSERT INTO "ai_sessions" VALUES('session','p','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
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
CREATE TABLE artifacts (
 id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), kind TEXT NOT NULL, path TEXT NOT NULL UNIQUE,
 size_bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(id, run_id)
);
CREATE TABLE codex_configs (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 codex_path TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0, 1)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
INSERT INTO "codex_configs" VALUES('codex_config_default','Default Codex configuration','',1,'2026-09-10T17:32:42.587Z','2026-09-10T17:32:42.587Z');
CREATE TABLE codex_server_requests (
 connection_id TEXT NOT NULL,
 request_id TEXT NOT NULL CHECK(json_valid(request_id)),
 run_id TEXT NOT NULL REFERENCES runs(id),
 request_json TEXT NOT NULL CHECK(json_valid(request_json)),
 PRIMARY KEY(connection_id,request_id)
);
CREATE TABLE codex_usage_ledgers (
 thread_id TEXT PRIMARY KEY CHECK(length(thread_id)>0),
 accounting_epoch INTEGER NOT NULL CHECK(accounting_epoch>0),
 previous_raw_cumulative TEXT NOT NULL,
 logical_cumulative TEXT NOT NULL,
 anchored INTEGER NOT NULL CHECK(anchored IN (0,1)),
 last_turn_id TEXT NOT NULL,
 in_flight_turn_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0)
);
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
 updated_at TEXT NOT NULL, final_codex_prompt TEXT, prompt_sha256 TEXT CHECK(prompt_sha256 IS NULL OR (length(prompt_sha256)=64 AND prompt_sha256 GLOB '[0-9a-f]*')), prompt_bytes INTEGER CHECK(prompt_bytes IS NULL OR prompt_bytes>=0), prompt_sent_at TEXT, task_id TEXT REFERENCES tasks(id), run_id TEXT REFERENCES runs(id), runtime_epoch TEXT REFERENCES runtime_epochs(id),
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(repository_id,project_id) REFERENCES repositories(id,project_id),
 CHECK((scope_kind='REPOSITORY' AND scope_paths_json='{"version":1,"paths":[]}') OR scope_kind='PATHS'),
 CHECK((status='CREATED' AND started_at IS NULL AND finished_at IS NULL) OR
       (status='RUNNING' AND started_at IS NOT NULL AND finished_at IS NULL) OR
       (status IN ('SUCCEEDED','NO_GUIDANCE','FAILED','CANCELED') AND started_at IS NOT NULL AND finished_at IS NOT NULL))
);
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
CREATE TABLE development_branch_requests (
 request_id TEXT PRIMARY KEY REFERENCES development_cycles(request_id),
 result_json TEXT NOT NULL
);
CREATE TABLE development_cycles (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 development_ref TEXT NOT NULL CHECK(development_ref LIKE 'refs/heads/%' AND length(development_ref)>11),
 selected_head_sha TEXT NOT NULL,
 created_at TEXT NOT NULL,
 superseded_at TEXT,
 end_reason TEXT CHECK(end_reason IS NULL OR end_reason='BRANCH_RESELECTED'),
 request_id TEXT NOT NULL UNIQUE CHECK(length(request_id)>0 AND length(request_id)<=128),
 selected_by_ai_session_id TEXT REFERENCES ai_sessions(id),
 CHECK((superseded_at IS NULL AND end_reason IS NULL) OR (superseded_at IS NOT NULL AND end_reason='BRANCH_RESELECTED'))
);
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
 FOREIGN KEY(feature_area_id,project_id) REFERENCES "feature_areas"(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id)
);
CREATE TABLE events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, type TEXT NOT NULL,
 project_id TEXT REFERENCES projects(id), task_id TEXT REFERENCES tasks(id), run_id TEXT REFERENCES runs(id), entity_type TEXT NOT NULL, entity_id TEXT NOT NULL,
 payload_json TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE "execution_cancellations" (
 project_id TEXT NOT NULL REFERENCES projects(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 execution_id TEXT REFERENCES execution_proposals(id),
 run_id TEXT REFERENCES runs(id),
 requested_by_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 reason TEXT NOT NULL CHECK(length(reason)<=2048),
 requested_at TEXT NOT NULL,
 runtime_epoch TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('CANCEL_REQUESTED','CANCELED','CANCEL_NOT_APPLIED','ALREADY_TERMINAL','RECOVERY_REQUIRED')),
 interrupt_state TEXT NOT NULL CHECK(interrupt_state IN ('NOT_SENT','REQUESTED','ACKNOWLEDGED','OUTCOME_UNKNOWN')),
 thread_id TEXT,
 turn_id TEXT,
 terminal_observed INTEGER NOT NULL DEFAULT 0 CHECK(terminal_observed IN (0,1)),
 resolved_at TEXT, owner_type TEXT NOT NULL DEFAULT 'EXECUTION' CHECK(owner_type IN ('REVIEW','PREPARATION','EXECUTION','TASK')), owner_id TEXT, task_id TEXT REFERENCES tasks(id), backend_disposition TEXT NOT NULL DEFAULT 'ACTIVE' CHECK(backend_disposition IN ('ACTIVE','TERMINATED','NO_BACKEND','UNKNOWN_ORPHANED')),
 PRIMARY KEY(project_id,request_id)
);
CREATE TABLE execution_goal_progress (
 execution_proposal_id TEXT NOT NULL,
 goal_id TEXT NOT NULL REFERENCES task_goals(id),
 project_id TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('PENDING','RUNNING','COMPLETED','FAILED','SKIPPED')),
 updated_at TEXT NOT NULL,
 PRIMARY KEY(execution_proposal_id,goal_id),
 FOREIGN KEY(execution_proposal_id,project_id) REFERENCES execution_proposals(id,project_id)
);
CREATE TABLE "execution_proposals" (
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
 failure_message TEXT, task_definition_revision_id TEXT REFERENCES task_definition_revisions(id), task_preparation_id TEXT REFERENCES task_preparations(id),
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
CREATE TABLE feature_area_agent_claims (
 feature_area_id TEXT PRIMARY KEY REFERENCES feature_areas(id),
 operation_kind TEXT NOT NULL CHECK(operation_kind IN ('REVIEW','PREPARATION','EXECUTION')),
 operation_id TEXT NOT NULL,
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 created_at TEXT NOT NULL, runtime_epoch TEXT REFERENCES runtime_epochs(id),
 UNIQUE(operation_kind,operation_id)
);
CREATE TABLE feature_area_lifecycle_requests (
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 action TEXT NOT NULL CHECK(action IN ('ACTIVATE','DECOMPOSE','RETIRE')),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),created_at TEXT NOT NULL,
 PRIMARY KEY(feature_area_id,request_id)
);
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
CREATE TABLE feature_area_retirements (
 feature_area_id TEXT PRIMARY KEY REFERENCES feature_areas(id),
 request_id TEXT NOT NULL,
 requested_by_ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 requested_at TEXT NOT NULL,
 completed_at TEXT,
 last_error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE TABLE feature_area_review_start_requests (
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64 AND payload_sha256 GLOB '[0-9a-f]*'),
 outcome TEXT NOT NULL CHECK(outcome IN ('ACCEPTED','FEATURE_AREA_BUSY')),
 review_id TEXT REFERENCES reviews(id),
 run_id TEXT REFERENCES runs(id),
 owner_operation_kind TEXT CHECK(owner_operation_kind IN ('REVIEW','PREPARATION','EXECUTION')),
 owner_operation_id TEXT,
 owner_run_id TEXT REFERENCES runs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(feature_area_id,request_id),
 CHECK(
  (outcome='ACCEPTED' AND review_id IS NOT NULL AND run_id IS NOT NULL AND owner_operation_kind IS NULL AND owner_operation_id IS NULL AND owner_run_id IS NULL) OR
  (outcome='FEATURE_AREA_BUSY' AND review_id IS NULL AND run_id IS NULL AND owner_operation_kind IS NOT NULL AND owner_operation_id IS NOT NULL AND owner_run_id IS NOT NULL)
 )
);
CREATE TABLE "feature_areas" (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 created_by_ai_session_id TEXT,
 title TEXT NOT NULL CHECK(length(title)>0 AND length(title)<=256),
 problem_statement TEXT NOT NULL CHECK(length(problem_statement)>0 AND length(problem_statement)<=16384),
 status TEXT NOT NULL CHECK(status IN ('DISCOVERING','ACTIVE','SPLITTING','DECOMPOSED','RETIRED','RETIRING')),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL, legacy_read_only INTEGER NOT NULL DEFAULT 0 CHECK(legacy_read_only IN (0,1)), legacy_status TEXT,
 UNIQUE(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id)
);
INSERT INTO "feature_areas" VALUES('area','p','session','Legacy','Historical capability','ACTIVE','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z',0,NULL);
CREATE TABLE "legacy_review_grants" (
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
CREATE TABLE legacy_task_audit(task_id TEXT PRIMARY KEY REFERENCES tasks(id),agent_context TEXT,reason TEXT NOT NULL);
CREATE TABLE modules (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 key TEXT NOT NULL,
 name TEXT NOT NULL,
 parent_id TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', system_owned INTEGER NOT NULL DEFAULT 0 CHECK(system_owned IN (0,1)),
 UNIQUE(project_id, key),
 UNIQUE(id, project_id),
 CHECK(parent_id IS NULL OR parent_id <> id),
 FOREIGN KEY(parent_id, project_id) REFERENCES modules(id, project_id)
);
INSERT INTO "modules" VALUES('module','p','core','Core',NULL,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z','',0);
CREATE TABLE project_contexts (
 project_id TEXT PRIMARY KEY REFERENCES projects(id),
 status TEXT NOT NULL CHECK(status IN ('REQUIRED','RUNNING','READY','NO_GUIDANCE','FAILED')),
 root_guidance_identity TEXT,
 latest_probe_id TEXT REFERENCES context_probes(id),
 result TEXT,
 result_artifact_ref TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
, context_fingerprint TEXT NOT NULL DEFAULT '', diagnostics_json TEXT NOT NULL DEFAULT '{"version":1,"items":[]}');
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
CREATE TABLE project_selection (
 singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
 project_id TEXT NOT NULL REFERENCES projects(id),
 updated_at TEXT NOT NULL
);
CREATE TABLE projects (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT INTO "projects" VALUES('p','Legacy','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
CREATE TABLE repositories (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), name TEXT NOT NULL,
 canonical_path TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(id, project_id)
);
INSERT INTO "repositories" VALUES('repo','p','Repo','/legacy/repo','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z');
CREATE TABLE review_access_evidence (
 id TEXT PRIMARY KEY,
 review_id TEXT NOT NULL REFERENCES reviews(id),
 operation TEXT NOT NULL CHECK(length(operation)>0 AND length(operation)<=128),
 outcome TEXT NOT NULL CHECK(outcome IN ('SUCCESS','FORBIDDEN','FAILURE')),
 code TEXT CHECK(code IS NULL OR length(code)<=256),
 recorded_at TEXT NOT NULL
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
CREATE TABLE review_approval_materializations (
 decision_id TEXT PRIMARY KEY REFERENCES decision_resolution_receipts(decision_id),
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
 initial_revision_id TEXT NOT NULL UNIQUE REFERENCES task_definition_revisions(id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 created_at TEXT NOT NULL
);
CREATE TABLE review_approval_receipt_context (
 decision_id TEXT PRIMARY KEY REFERENCES decision_resolution_receipts(decision_id),
 ai_session_id TEXT NOT NULL REFERENCES ai_sessions(id),
 created_at TEXT NOT NULL
);
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
CREATE TABLE review_repository_provenance (
 review_id TEXT PRIMARY KEY REFERENCES reviews(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 reviewed_commit TEXT NOT NULL CHECK(length(reviewed_commit)>0 AND length(reviewed_commit)<=64),
 captured_at TEXT NOT NULL
, repository_head TEXT, branch_kind TEXT CHECK(branch_kind IS NULL OR branch_kind IN ('BRANCH','DETACHED','UNKNOWN')), branch_ref TEXT, working_tree_dirty INTEGER CHECK(working_tree_dirty IS NULL OR working_tree_dirty IN (0,1)), inspection_mode TEXT CHECK(inspection_mode IS NULL OR inspection_mode IN ('MANAGED_COMMIT_WORKTREE')));
CREATE TABLE review_workflow_sources (
 review_id TEXT PRIMARY KEY REFERENCES reviews(id),
 development_cycle_id TEXT REFERENCES development_cycles(id),
 development_ref TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE "reviews" (
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
, reasoning_effort TEXT
 CHECK(reasoning_effort IS NULL OR reasoning_effort IN ('low','medium','high','xhigh')),
 repository_id TEXT REFERENCES repositories(id),
 codex_config_id TEXT REFERENCES codex_configs(id),
 base_commit TEXT,
 next_attempt INTEGER NOT NULL DEFAULT 0, task_id TEXT REFERENCES tasks(id),
 UNIQUE(id,project_id),
 FOREIGN KEY(feature_area_id,project_id) REFERENCES "feature_areas"(id,project_id),
 FOREIGN KEY(created_by_ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(legacy_task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((focus_kind='REPOSITORY' AND json_type(focus_paths_json,'$.paths')='array' AND json_array_length(focus_paths_json,'$.paths')=0) OR focus_kind='FOCUSED'),
 CHECK((final_codex_prompt IS NULL AND prompt_sha256 IS NULL AND prompt_bytes IS NULL AND prompt_sent_at IS NULL) OR (final_codex_prompt IS NOT NULL AND prompt_sha256 IS NOT NULL AND prompt_bytes IS NOT NULL AND prompt_sent_at IS NOT NULL)),
 CHECK((start_failure_code IS NULL AND start_blocker IS NULL AND start_failure_at IS NULL) OR (start_failure_code IS NOT NULL AND start_blocker IS NOT NULL AND start_failure_at IS NOT NULL))
);
CREATE TABLE run_agent_processes (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 launch_id TEXT NOT NULL REFERENCES agent_process_launches(launch_id)
);
CREATE TABLE run_evidence (
 run_id TEXT PRIMARY KEY REFERENCES runs(id), base_commit TEXT NOT NULL, head_commit TEXT NOT NULL, dirty INTEGER NOT NULL,
 changed_files_json TEXT NOT NULL, diff_artifact_id TEXT, created_at TEXT NOT NULL, start_snapshot TEXT CHECK(start_snapshot IS NULL OR length(start_snapshot) IN (40,64)), end_snapshot TEXT CHECK(end_snapshot IS NULL OR length(end_snapshot) IN (40,64)),
 FOREIGN KEY(diff_artifact_id, run_id) REFERENCES artifacts(id, run_id)
);
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
INSERT INTO "run_token_usage" VALUES('legacy-run','UNAVAILABLE',0,0,0,0,0,0,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'2026-09-01T00:00:00Z');
CREATE TABLE run_usage_attributions (
 thread_id TEXT NOT NULL REFERENCES codex_usage_ledgers(thread_id),
 turn_id TEXT NOT NULL CHECK(length(turn_id)>0),
 run_id TEXT UNIQUE REFERENCES runs(id),
 attribution_json TEXT NOT NULL,
 PRIMARY KEY(thread_id,turn_id)
);
CREATE TABLE "runs" (
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
 purpose TEXT NOT NULL CHECK(purpose IN ('SCOPE_REVIEW','IMPLEMENTATION','VERIFICATION','INSPECTION')),
 execution_proposal_id TEXT REFERENCES execution_proposals(id),
 verification_result_json TEXT,
 codex_instruction TEXT NOT NULL DEFAULT '',
 review_scope_kind TEXT CHECK(review_scope_kind IS NULL OR review_scope_kind IN ('PATHS','REPOSITORY')),
 review_scope_paths_json TEXT NOT NULL DEFAULT '{"version":1,"paths":[]}',
 result_text TEXT, review_mode TEXT CHECK(review_mode IS NULL OR review_mode IN ('FOCUSED','REPOSITORY')), selected_modules_json TEXT NOT NULL DEFAULT '{"version":1,"items":[]}', review_id TEXT REFERENCES reviews(id), task_preparation_id TEXT REFERENCES task_preparations(id), final_codex_prompt TEXT, prompt_sha256 TEXT CHECK(prompt_sha256 IS NULL OR (length(prompt_sha256)=64 AND prompt_sha256 GLOB '[0-9a-f]*')), prompt_bytes INTEGER CHECK(prompt_bytes IS NULL OR prompt_bytes>=0), prompt_sent_at TEXT, runtime_epoch TEXT REFERENCES runtime_epochs(id),
 project_id TEXT REFERENCES projects(id),
 feature_area_id TEXT REFERENCES feature_areas(id),
 operation_kind TEXT CHECK(operation_kind IN ('REVIEW','PREPARATION','EXECUTION','INSPECTION')),
 operation_id TEXT, context_probe_id TEXT REFERENCES context_probes(id),
 UNIQUE(task_id,attempt),
 UNIQUE(id,task_id)
);
INSERT INTO "runs" VALUES('legacy-run','task',1,'RUNNING',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z',NULL,NULL,NULL,NULL,'IMPLEMENTATION',NULL,NULL,'',NULL,'{"version":1,"paths":[]}',NULL,NULL,'{"version":1,"items":[]}',NULL,NULL,NULL,NULL,NULL,NULL,NULL,'p','area',NULL,NULL,NULL);
CREATE TABLE runtime_authority (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 epoch_id TEXT NOT NULL REFERENCES runtime_epochs(id)
);
CREATE TABLE runtime_epochs (
 id TEXT PRIMARY KEY,
 created_at TEXT NOT NULL
);
CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	);
INSERT INTO "schema_migrations" VALUES(1,'2026-09-10T17:32:42.586Z');
INSERT INTO "schema_migrations" VALUES(2,'2026-09-10T17:32:42.587Z');
INSERT INTO "schema_migrations" VALUES(3,'2026-09-10T17:32:42.588Z');
INSERT INTO "schema_migrations" VALUES(4,'2026-09-10T17:32:42.588Z');
INSERT INTO "schema_migrations" VALUES(5,'2026-09-10T17:32:42.589Z');
INSERT INTO "schema_migrations" VALUES(6,'2026-09-10T17:32:42.589Z');
INSERT INTO "schema_migrations" VALUES(7,'2026-09-10T17:32:42.590Z');
INSERT INTO "schema_migrations" VALUES(8,'2026-09-10T17:32:42.592Z');
INSERT INTO "schema_migrations" VALUES(9,'2026-09-10T17:32:42.594Z');
INSERT INTO "schema_migrations" VALUES(10,'2026-09-10T17:32:42.599Z');
INSERT INTO "schema_migrations" VALUES(11,'2026-09-10T17:32:42.605Z');
INSERT INTO "schema_migrations" VALUES(12,'2026-09-10T17:32:42.606Z');
INSERT INTO "schema_migrations" VALUES(13,'2026-09-10T17:32:42.606Z');
INSERT INTO "schema_migrations" VALUES(14,'2026-09-10T17:32:42.608Z');
INSERT INTO "schema_migrations" VALUES(15,'2026-09-10T17:32:42.609Z');
INSERT INTO "schema_migrations" VALUES(16,'2026-09-10T17:32:42.613Z');
INSERT INTO "schema_migrations" VALUES(17,'2026-09-10T17:32:42.615Z');
INSERT INTO "schema_migrations" VALUES(18,'2026-09-10T17:32:42.616Z');
INSERT INTO "schema_migrations" VALUES(19,'2026-09-10T17:32:42.618Z');
INSERT INTO "schema_migrations" VALUES(20,'2026-09-10T17:32:42.625Z');
INSERT INTO "schema_migrations" VALUES(21,'2026-09-10T17:32:42.627Z');
INSERT INTO "schema_migrations" VALUES(22,'2026-09-10T17:32:42.630Z');
INSERT INTO "schema_migrations" VALUES(23,'2026-09-10T17:32:42.630Z');
INSERT INTO "schema_migrations" VALUES(24,'2026-09-10T17:32:42.632Z');
INSERT INTO "schema_migrations" VALUES(25,'2026-09-10T17:32:42.638Z');
INSERT INTO "schema_migrations" VALUES(26,'2026-09-10T17:32:42.643Z');
INSERT INTO "schema_migrations" VALUES(27,'2026-09-10T17:32:42.651Z');
INSERT INTO "schema_migrations" VALUES(28,'2026-09-10T17:32:42.677Z');
INSERT INTO "schema_migrations" VALUES(29,'2026-09-10T17:32:42.683Z');
INSERT INTO "schema_migrations" VALUES(30,'2026-09-10T17:32:42.683Z');
INSERT INTO "schema_migrations" VALUES(31,'2026-09-10T17:32:42.684Z');
INSERT INTO "schema_migrations" VALUES(32,'2026-09-10T17:32:42.691Z');
INSERT INTO "schema_migrations" VALUES(33,'2026-09-10T17:32:42.692Z');
INSERT INTO "schema_migrations" VALUES(34,'2026-09-10T17:32:42.692Z');
INSERT INTO "schema_migrations" VALUES(35,'2026-09-10T17:32:42.693Z');
INSERT INTO "schema_migrations" VALUES(36,'2026-09-10T17:32:42.732Z');
INSERT INTO "schema_migrations" VALUES(37,'2026-09-10T17:32:42.746Z');
INSERT INTO "schema_migrations" VALUES(38,'2026-09-10T17:32:42.749Z');
INSERT INTO "schema_migrations" VALUES(39,'2026-09-10T17:32:42.768Z');
INSERT INTO "schema_migrations" VALUES(40,'2026-09-10T17:32:42.769Z');
INSERT INTO "schema_migrations" VALUES(41,'2026-09-10T17:32:42.770Z');
CREATE TABLE "scope_expansions" (
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
CREATE TABLE task_definition_revisions (
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>0),
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 created_at TEXT NOT NULL, UNIQUE(task_id,ordinal), UNIQUE(task_id,id)
);
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
 legacy_task_proposal_id TEXT UNIQUE, title TEXT NOT NULL DEFAULT '',
 FOREIGN KEY(ai_session_id,project_id) REFERENCES ai_sessions(id,project_id),
 FOREIGN KEY(task_id,project_id) REFERENCES tasks(id,project_id),
 CHECK((status='PENDING_CONFIRMATION' AND confirmed_at IS NULL AND rejected_at IS NULL AND applied_at IS NULL) OR
       (status='CONFIRMED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND applied_at IS NULL) OR
       (status='REJECTED' AND confirmed_at IS NULL AND rejected_at IS NOT NULL AND applied_at IS NULL) OR
       (status='APPLIED' AND confirmed_at IS NOT NULL AND rejected_at IS NULL AND applied_at IS NOT NULL))
);
CREATE TABLE task_preparation_access_evidence (
 id TEXT PRIMARY KEY,
 task_preparation_id TEXT NOT NULL REFERENCES task_preparations(id),
 operation TEXT NOT NULL CHECK(length(operation)>0 AND length(operation)<=128),
 outcome TEXT NOT NULL CHECK(outcome IN ('SUCCESS','FORBIDDEN','FAILURE')),
 code TEXT CHECK(code IS NULL OR length(code)<=256),
 recorded_at TEXT NOT NULL
);
CREATE TABLE "task_preparations" (
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
 status TEXT NOT NULL CHECK(status IN ('DRAFT','PREPARING','READY','CONFLICT','FAILED','CANCELED')),
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
 consumed_at TEXT, result_task_id TEXT REFERENCES tasks(id), work_spec_json TEXT NOT NULL DEFAULT '{}', verification_spec_json TEXT NOT NULL DEFAULT '{}',
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
CREATE TABLE "tasks" (
 id TEXT PRIMARY KEY,
 project_id TEXT NOT NULL REFERENCES projects(id),
 module_id TEXT,
 repository_id TEXT,
 codex_config_id TEXT REFERENCES codex_configs(id),
 goal TEXT NOT NULL,
 constraints_json TEXT NOT NULL,
 base_commit TEXT NOT NULL,
 status TEXT NOT NULL,
 next_attempt INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL, work_spec_json TEXT NOT NULL DEFAULT '{}', verification_spec_json TEXT NOT NULL DEFAULT '{}', problem_statement TEXT NOT NULL DEFAULT '', confirmed_task_plan_id TEXT REFERENCES task_plans(id), feature_area_id TEXT REFERENCES "feature_areas"(id), work_kind TEXT
 CHECK(work_kind IS NULL OR work_kind IN ('FEATURE','BUG_FIX','REFACTOR','MAINTENANCE','TEST_CHANGE','DOCUMENTATION','OTHER')), model TEXT
, reasoning_effort TEXT
 CHECK(reasoning_effort IS NULL OR reasoning_effort IN ('low','medium','high','xhigh')), agent_context TEXT
 CHECK(agent_context IS NULL OR json_valid(agent_context)), legacy_read_only INTEGER NOT NULL DEFAULT 0 CHECK(legacy_read_only IN (0,1)), current_definition_revision_id TEXT REFERENCES task_definition_revisions(id), task_type TEXT NOT NULL DEFAULT 'DEVELOPMENT' CHECK(task_type IN ('DEVELOPMENT','REVIEW','INSPECTION')), task_scope TEXT NOT NULL DEFAULT 'FEATURE_AREA' CHECK(task_scope IN ('FEATURE_AREA','PROJECT')), work_owner_id TEXT, work_legacy_envelope INTEGER NOT NULL DEFAULT 0 CHECK(work_legacy_envelope IN (0,1)), review_approval_id TEXT REFERENCES review_approval_decisions(decision_id),
 FOREIGN KEY(repository_id, project_id) REFERENCES repositories(id, project_id),
 FOREIGN KEY(module_id, project_id) REFERENCES modules(id, project_id)
);
INSERT INTO "tasks" VALUES('task','p','module','repo','codex_config_default','Goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',0,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z','{}','{}','',NULL,'area',NULL,NULL,NULL,NULL,0,NULL,'DEVELOPMENT','FEATURE_AREA',NULL,0,NULL);
CREATE TABLE "workspaces" (
 id TEXT PRIMARY KEY,
 task_id TEXT REFERENCES tasks(id),
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
 preparation_id TEXT UNIQUE REFERENCES task_preparations(id),
 CHECK((task_id IS NOT NULL AND review_id IS NULL) OR (task_id IS NULL AND review_id IS NOT NULL)),
 UNIQUE(id,task_id)
);
CREATE TABLE worktrees (
 id TEXT PRIMARY KEY, repository_id TEXT NOT NULL REFERENCES repositories(id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 path TEXT NOT NULL UNIQUE, branch TEXT NOT NULL, base_commit TEXT NOT NULL, status TEXT NOT NULL,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(repository_id, branch)
);
CREATE INDEX events_project_sequence ON events(project_id, sequence);
CREATE INDEX events_task_sequence ON events(task_id, sequence);
CREATE INDEX events_run_sequence ON events(run_id, sequence);
CREATE INDEX approvals_run_status ON approvals(run_id,status,created_at,id);
CREATE INDEX approvals_thread_turn_item ON approvals(thread_id,turn_id,item_id);
CREATE INDEX ai_sessions_project_created ON ai_sessions(project_id,created_at,id);
CREATE UNIQUE INDEX ai_sessions_id_project ON ai_sessions(id,project_id);
CREATE INDEX task_proposals_session_created ON task_proposals(ai_session_id,created_at DESC,id);
CREATE INDEX task_proposals_project_status ON task_proposals(project_id,status,created_at,id);
CREATE INDEX task_proposals_target_status ON task_proposals(target_task_id,status,created_at,id);
CREATE INDEX task_proposals_result_task ON task_proposals(result_task_id);
CREATE UNIQUE INDEX task_proposals_id_session_project
ON task_proposals(id,ai_session_id,project_id);
CREATE UNIQUE INDEX modules_one_system_unclassified
ON modules(project_id)
WHERE system_owned=1 AND key='_unclassified';
CREATE INDEX context_probes_session_created ON context_probes(ai_session_id,created_at DESC,id);
CREATE INDEX context_probes_project_created ON context_probes(project_id,created_at DESC,id);
CREATE UNIQUE INDEX context_probes_one_running_per_project ON context_probes(project_id) WHERE status='RUNNING';
CREATE INDEX task_classifications_task_created ON task_classifications(task_id,created_at,id);
CREATE INDEX task_plans_project_created ON task_plans(project_id,created_at DESC,id);
CREATE INDEX task_plans_task_status ON task_plans(task_id,status,created_at,id);
CREATE INDEX execution_proposals_session_created ON execution_proposals(ai_session_id,created_at DESC,id);
CREATE INDEX execution_proposals_project_status ON execution_proposals(project_id,status,created_at,id);
CREATE INDEX execution_proposals_task_status ON execution_proposals(task_id,status,created_at,id);
CREATE UNIQUE INDEX execution_proposals_verification_run ON execution_proposals(verification_run_id) WHERE verification_run_id IS NOT NULL;
CREATE INDEX ai_project_selections_project_created ON ai_project_selections(project_id,created_at DESC,id);
CREATE UNIQUE INDEX execution_proposals_one_active_per_task_plan
ON execution_proposals(task_plan_id)
WHERE status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED')
  AND outcome IN ('PENDING','RUNNING');
CREATE INDEX project_module_map_project ON project_module_map_entries(project_id,module_id,repository_id);
CREATE INDEX review_grants_project ON "legacy_review_grants"(project_id,created_at,task_id);
CREATE INDEX review_access_evidence_review_observed ON review_access_evidence(review_id,recorded_at,id);
CREATE INDEX task_preparation_access_observed ON task_preparation_access_evidence(task_preparation_id,recorded_at,id);
CREATE INDEX documents_project_created ON documents(project_id,created_at DESC,id);
CREATE INDEX review_repository_provenance_repository ON review_repository_provenance(repository_id,reviewed_commit);
CREATE INDEX task_goals_task_position ON task_goals(task_id,position);
CREATE UNIQUE INDEX execution_proposals_id_project ON execution_proposals(id,project_id);
CREATE INDEX execution_goal_progress_project_updated ON execution_goal_progress(project_id,updated_at,execution_proposal_id);
CREATE INDEX documents_feature_area_created ON documents(feature_area_id,created_at DESC,id) WHERE feature_area_id IS NOT NULL;
CREATE UNIQUE INDEX agent_conversations_one_current ON agent_conversations(feature_area_id) WHERE ended_at IS NULL;
CREATE INDEX agent_conversations_history ON agent_conversations(feature_area_id,created_at,id);
CREATE INDEX scope_expansions_project_created ON scope_expansions(project_id,created_at DESC,id);
CREATE INDEX scope_expansions_review_status ON scope_expansions(review_id,status,created_at,id);
CREATE UNIQUE INDEX scope_expansions_one_pending_per_review ON scope_expansions(review_id) WHERE status='PENDING_CONFIRMATION';
CREATE INDEX workspaces_status_updated ON workspaces(status,updated_at,id);
CREATE UNIQUE INDEX workspaces_legacy_task ON workspaces(task_id) WHERE preparation_id IS NULL;
CREATE UNIQUE INDEX feature_area_review_start_accepted_review ON feature_area_review_start_requests(review_id) WHERE review_id IS NOT NULL;
CREATE UNIQUE INDEX feature_area_review_start_accepted_run ON feature_area_review_start_requests(run_id) WHERE run_id IS NOT NULL;
CREATE INDEX decision_resolution_project_resolved ON decision_resolution_receipts(project_id,resolved_at,decision_id);
CREATE UNIQUE INDEX development_cycles_one_current ON development_cycles(project_id,repository_id) WHERE superseded_at IS NULL;
CREATE UNIQUE INDEX execution_cancellation_active_run ON execution_cancellations(run_id) WHERE state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED');
CREATE INDEX execution_cancellation_execution ON execution_cancellations(project_id,execution_id,requested_at);
CREATE UNIQUE INDEX runs_codex_interaction ON runs(thread_id,turn_id) WHERE thread_id IS NOT NULL AND thread_id<>'' AND turn_id IS NOT NULL AND turn_id<>'';
CREATE UNIQUE INDEX runs_execution_purpose ON runs(execution_proposal_id,purpose) WHERE execution_proposal_id IS NOT NULL;
CREATE UNIQUE INDEX runs_one_active_per_task ON runs(task_id) WHERE status IN ('PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING');
CREATE UNIQUE INDEX runs_review_id ON runs(review_id) WHERE review_id IS NOT NULL;
CREATE UNIQUE INDEX runs_task_preparation_id ON runs(task_preparation_id) WHERE task_preparation_id IS NOT NULL;
CREATE UNIQUE INDEX runs_thread_turn ON runs(thread_id,turn_id) WHERE thread_id IS NOT NULL AND turn_id IS NOT NULL;
CREATE INDEX runs_workspace_attempt ON runs(workspace_id,attempt);
CREATE INDEX task_preparations_feature_area_created ON task_preparations(feature_area_id,created_at,id);
CREATE INDEX task_preparations_project_created ON task_preparations(project_id,created_at,id);
CREATE UNIQUE INDEX probes_work_task ON context_probes(task_id) WHERE task_id IS NOT NULL;
CREATE UNIQUE INDEX runs_context_probe ON runs(context_probe_id) WHERE context_probe_id IS NOT NULL;
CREATE UNIQUE INDEX cancellations_active_owner ON execution_cancellations(owner_type,owner_id) WHERE state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED');
CREATE INDEX feature_areas_project_created ON feature_areas(project_id,created_at DESC,id);
CREATE INDEX reviews_feature_area_created ON reviews(feature_area_id,created_at,id);
CREATE INDEX reviews_project_created ON reviews(project_id,created_at,id);
CREATE UNIQUE INDEX reviews_work_task ON reviews(task_id) WHERE task_id IS NOT NULL;
CREATE INDEX tasks_feature_area_created ON tasks(feature_area_id,created_at,id);
CREATE UNIQUE INDEX tasks_id_project ON tasks(id,project_id);
CREATE UNIQUE INDEX tasks_review_approval ON tasks(review_approval_id) WHERE review_approval_id IS NOT NULL;
CREATE INDEX tasks_work_kind_created ON tasks(work_kind,created_at,id);
CREATE UNIQUE INDEX tasks_work_owner ON tasks(task_type,work_owner_id) WHERE work_owner_id IS NOT NULL;
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
CREATE TRIGGER approved_execution_binding BEFORE INSERT ON execution_proposals
 WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL)
 AND (NEW.status<>'CONFIRMED' OR NOT EXISTS(
 SELECT 1 FROM tasks t JOIN review_approval_materializations m ON m.task_id=t.id JOIN task_preparations p ON p.task_id=t.id JOIN task_plans plan ON plan.id=p.task_plan_id
 WHERE t.id=NEW.task_id AND p.id=NEW.task_preparation_id AND p.status='READY' AND plan.status='APPLIED' AND NEW.task_plan_id=p.task_plan_id AND NEW.task_definition_revision_id=m.initial_revision_id AND t.current_definition_revision_id=m.initial_revision_id)
 OR EXISTS(SELECT 1 FROM execution_proposals WHERE task_preparation_id=NEW.task_preparation_id))
 BEGIN SELECT RAISE(ABORT,'EXECUTION_PREREQUISITE_NOT_SATISFIED'); END;
CREATE TRIGGER approved_preparation_binding BEFORE INSERT ON task_preparations
 WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM tasks t JOIN review_approval_materializations m ON m.task_id=t.id JOIN review_approval_decisions d ON d.decision_id=m.decision_id WHERE t.id=NEW.task_id AND NEW.task_definition_revision_id=m.initial_revision_id AND NEW.source_review_id=d.review_id AND t.current_definition_revision_id=m.initial_revision_id)
 BEGIN SELECT RAISE(ABORT,'REVIEW_APPROVAL_REQUIRED'); END;
CREATE TRIGGER approved_review_result_immutable BEFORE UPDATE OF result_text ON runs
 WHEN NEW.result_text IS NOT OLD.result_text AND EXISTS(SELECT 1 FROM review_approval_decisions WHERE review_id=OLD.review_id)
 BEGIN SELECT RAISE(ABORT,'Review Approval result is immutable'); END;
CREATE TRIGGER approved_task_goals_delete BEFORE DELETE ON task_goals WHEN EXISTS(SELECT 1 FROM tasks WHERE id=OLD.task_id AND review_approval_id IS NOT NULL) BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_goals_insert BEFORE INSERT ON task_goals WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL) BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_goals_update BEFORE UPDATE ON task_goals WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND review_approval_id IS NOT NULL) BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_insert BEFORE INSERT ON tasks WHEN NEW.review_approval_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM review_approval_decisions d JOIN decision_resolution_receipts r ON r.decision_id=d.decision_id
 WHERE d.decision_id=NEW.review_approval_id AND d.project_id=NEW.project_id AND d.feature_area_id=NEW.feature_area_id
 AND r.option='APPROVE' AND r.result_id=NEW.id AND r.result_kind='DEVELOPMENT_TASK')
 BEGIN SELECT RAISE(ABORT,'REVIEW_APPROVAL_REQUIRED'); END;
CREATE TRIGGER approved_task_revision_immutable BEFORE UPDATE OF current_definition_revision_id ON tasks
 WHEN EXISTS(SELECT 1 FROM review_approval_materializations m WHERE m.task_id=NEW.id AND m.initial_revision_id IS NOT NEW.current_definition_revision_id)
 BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_semantics_immutable BEFORE UPDATE OF project_id,feature_area_id,repository_id,module_id,codex_config_id,model,reasoning_effort,goal,constraints_json,base_commit,problem_statement,work_spec_json,verification_spec_json ON tasks
 WHEN OLD.review_approval_id IS NOT NULL AND (NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.repository_id IS NOT OLD.repository_id OR NEW.module_id IS NOT OLD.module_id OR NEW.codex_config_id IS NOT OLD.codex_config_id OR NEW.model IS NOT OLD.model OR NEW.reasoning_effort IS NOT OLD.reasoning_effort OR NEW.goal IS NOT OLD.goal OR NEW.constraints_json IS NOT OLD.constraints_json OR NEW.base_commit IS NOT OLD.base_commit OR NEW.problem_statement IS NOT OLD.problem_statement OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.verification_spec_json IS NOT OLD.verification_spec_json)
 BEGIN SELECT RAISE(ABORT,'REVIEW_REVISION_REQUIRED'); END;
CREATE TRIGGER approved_task_source_immutable BEFORE UPDATE OF review_approval_id ON tasks
 WHEN NEW.review_approval_id IS NOT OLD.review_approval_id BEGIN SELECT RAISE(ABORT,'Task approval source is immutable'); END;
CREATE TRIGGER canceled_execution_authority BEFORE UPDATE ON execution_proposals WHEN OLD.outcome='CANCELED' AND (NEW.status IS NOT OLD.status OR NEW.outcome IS NOT OLD.outcome OR NEW.run_id IS NOT OLD.run_id OR NEW.verification_run_id IS NOT OLD.verification_run_id)
BEGIN SELECT RAISE(ABORT,'canceled Execution cannot advance'); END;
CREATE TRIGGER cancellation_owner_immutable BEFORE UPDATE OF owner_type,owner_id,task_id ON execution_cancellations WHEN NEW.owner_type IS NOT OLD.owner_type OR NEW.owner_id IS NOT OLD.owner_id OR NEW.task_id IS NOT OLD.task_id BEGIN SELECT RAISE(ABORT,'cancellation owner is immutable'); END;
CREATE TRIGGER cancellation_task_mutation_guard BEFORE UPDATE ON tasks
WHEN EXISTS(SELECT 1 FROM execution_cancellations WHERE task_id=OLD.id AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')) AND (NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.confirmed_task_plan_id IS NOT OLD.confirmed_task_plan_id)
BEGIN SELECT RAISE(ABORT,'cancellation closes Task development authority'); END;
CREATE TRIGGER claims_exact_owner_insert BEFORE INSERT ON feature_area_agent_claims
WHEN NOT EXISTS(SELECT 1 FROM runs r JOIN feature_areas f ON f.id=r.feature_area_id WHERE r.id=NEW.run_id AND r.feature_area_id=NEW.feature_area_id AND r.operation_kind=NEW.operation_kind AND r.operation_id=NEW.operation_id AND r.runtime_epoch IS NEW.runtime_epoch AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND r.status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL','COLLECTING') AND f.legacy_read_only=0 AND ((r.operation_kind='REVIEW' AND f.status IN ('DISCOVERING','ACTIVE')) OR (r.operation_kind IN ('PREPARATION','EXECUTION') AND f.status='ACTIVE')))
BEGIN SELECT RAISE(ABORT,'claim must match exact Run owner and epoch'); END;
CREATE TRIGGER claims_exact_owner_update BEFORE UPDATE OF run_id,feature_area_id,operation_kind,operation_id ON feature_area_agent_claims
WHEN NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.run_id AND feature_area_id=NEW.feature_area_id AND operation_kind=NEW.operation_kind AND operation_id=NEW.operation_id AND runtime_epoch IS NEW.runtime_epoch)
BEGIN SELECT RAISE(ABORT,'claim must match exact Run owner and epoch'); END;
CREATE TRIGGER claims_orphan_fence BEFORE INSERT ON feature_area_agent_claims WHEN EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=NEW.run_id) BEGIN SELECT RAISE(ABORT,'orphaned operation cannot reclaim authority'); END;
CREATE TRIGGER claims_orphan_transfer_fence BEFORE UPDATE ON feature_area_agent_claims WHEN EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=NEW.run_id) BEGIN SELECT RAISE(ABORT,'orphaned operation cannot reclaim authority'); END;
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
CREATE TRIGGER decision_resolution_receipts_immutable
BEFORE UPDATE ON decision_resolution_receipts
BEGIN SELECT RAISE(ABORT,'Decision resolution receipt is immutable'); END;
CREATE TRIGGER decision_resolution_receipts_no_delete
BEFORE DELETE ON decision_resolution_receipts
BEGIN SELECT RAISE(ABORT,'Decision resolution receipt is durable history'); END;
CREATE TRIGGER development_branch_request_immutable BEFORE UPDATE ON development_branch_requests
BEGIN SELECT RAISE(ABORT,'Development branch receipt is immutable'); END;
CREATE TRIGGER development_branch_request_no_delete BEFORE DELETE ON development_branch_requests
BEGIN SELECT RAISE(ABORT,'Development branch receipt is durable'); END;
CREATE TRIGGER development_cycle_immutable BEFORE UPDATE ON development_cycles
WHEN OLD.superseded_at IS NOT NULL OR NEW.superseded_at IS NULL OR NEW.end_reason IS NOT 'BRANCH_RESELECTED'
 OR NEW.id IS NOT OLD.id OR NEW.project_id IS NOT OLD.project_id OR NEW.repository_id IS NOT OLD.repository_id
 OR NEW.development_ref IS NOT OLD.development_ref OR NEW.selected_head_sha IS NOT OLD.selected_head_sha
 OR NEW.created_at IS NOT OLD.created_at OR NEW.request_id IS NOT OLD.request_id
 OR NEW.selected_by_ai_session_id IS NOT OLD.selected_by_ai_session_id
BEGIN SELECT RAISE(ABORT,'DevelopmentCycle provenance is immutable'); END;
CREATE TRIGGER development_cycle_no_delete BEFORE DELETE ON development_cycles
BEGIN SELECT RAISE(ABORT,'DevelopmentCycle is durable history'); END;
CREATE TRIGGER development_cycle_project BEFORE INSERT ON development_cycles
WHEN NOT EXISTS(SELECT 1 FROM repositories WHERE id=NEW.repository_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'REPOSITORY_PROJECT_MISMATCH'); END;
CREATE TRIGGER development_cycle_session_project BEFORE INSERT ON development_cycles
WHEN NEW.selected_by_ai_session_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM ai_sessions WHERE id=NEW.selected_by_ai_session_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'REPOSITORY_PROJECT_MISMATCH'); END;
CREATE TRIGGER documents_content_immutable
BEFORE UPDATE ON documents
BEGIN SELECT RAISE(ABORT, 'Document is immutable'); END;
CREATE TRIGGER execution_cancellation_identity_immutable BEFORE UPDATE ON execution_cancellations
WHEN NEW.project_id<>OLD.project_id OR NEW.request_id<>OLD.request_id OR NEW.execution_id<>OLD.execution_id OR NEW.run_id<>OLD.run_id OR NEW.requested_by_ai_session_id<>OLD.requested_by_ai_session_id OR NEW.reason<>OLD.reason OR NEW.requested_at<>OLD.requested_at OR NEW.runtime_epoch<>OLD.runtime_epoch
BEGIN SELECT RAISE(ABORT,'cancellation request identity is immutable'); END;
CREATE TRIGGER execution_cancellation_no_delete BEFORE DELETE ON execution_cancellations
BEGIN SELECT RAISE(ABORT,'cancellation receipt is durable history'); END;
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
CREATE TRIGGER execution_proposals_exact_preparation BEFORE INSERT ON execution_proposals
WHEN NEW.task_definition_revision_id IS NULL OR NEW.task_preparation_id IS NULL OR NOT EXISTS(
 SELECT 1 FROM task_preparations p JOIN tasks t ON t.id=p.task_id JOIN feature_areas f ON f.id=t.feature_area_id
 WHERE p.id=NEW.task_preparation_id AND p.task_id=NEW.task_id AND p.project_id=NEW.project_id AND p.status='READY'
 AND p.task_plan_id=NEW.task_plan_id AND p.task_definition_revision_id=NEW.task_definition_revision_id AND t.legacy_read_only=0 AND f.legacy_read_only=0 AND f.status='ACTIVE')
BEGIN SELECT RAISE(ABORT,'Execution requires exact ready Preparation and definition'); END;
CREATE TRIGGER execution_proposals_outcome_transition BEFORE UPDATE OF outcome ON execution_proposals
WHEN NEW.outcome<>OLD.outcome AND NOT ((OLD.outcome='PENDING' AND ((NEW.outcome='RUNNING' AND OLD.status='CONFIRMED' AND NEW.status='CONSUMED') OR NEW.outcome='CANCELED')) OR (OLD.outcome='RUNNING' AND NEW.outcome IN ('SUCCEEDED','FAILED','CANCELED')))
BEGIN SELECT RAISE(ABORT,'invalid Execution outcome transition'); END;
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
CREATE TRIGGER execution_task_terminal_guard BEFORE INSERT ON execution_proposals
WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND status='CANCELED') OR EXISTS(SELECT 1 FROM execution_cancellations WHERE task_id=NEW.task_id AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
BEGIN SELECT RAISE(ABORT,'Task cancellation prevents Execution admission'); END;
CREATE TRIGGER feature_area_agent_busy_lifecycle BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status<>OLD.status AND NEW.status<>'RETIRING' AND EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_BUSY'); END;
CREATE TRIGGER feature_area_agent_end AFTER UPDATE OF status ON feature_areas
WHEN NEW.status IN ('DECOMPOSED','RETIRED')
BEGIN UPDATE agent_conversations SET ended_at=NEW.updated_at WHERE feature_area_id=NEW.id AND ended_at IS NULL; END;
CREATE TRIGGER feature_area_lifecycle_requests_immutable BEFORE UPDATE ON feature_area_lifecycle_requests
BEGIN SELECT RAISE(ABORT,'lifecycle request receipt is immutable'); END;
CREATE TRIGGER feature_area_lifecycle_requests_no_delete BEFORE DELETE ON feature_area_lifecycle_requests
BEGIN SELECT RAISE(ABORT,'lifecycle request receipt is durable history'); END;
CREATE TRIGGER feature_area_relations_immutable
BEFORE UPDATE ON feature_area_relations
BEGIN SELECT RAISE(ABORT, 'Feature Area relation is immutable'); END;
CREATE TRIGGER feature_area_relations_no_delete
BEFORE DELETE ON feature_area_relations
BEGIN SELECT RAISE(ABORT, 'Feature Area relation is durable history'); END;
CREATE TRIGGER feature_area_review_start_requests_immutable BEFORE UPDATE ON feature_area_review_start_requests
BEGIN SELECT RAISE(ABORT,'Feature Area Review start receipt is immutable'); END;
CREATE TRIGGER feature_area_review_start_requests_no_delete BEFORE DELETE ON feature_area_review_start_requests
BEGIN SELECT RAISE(ABORT,'Feature Area Review start receipt is durable history'); END;
CREATE TRIGGER feature_areas_current_insert BEFORE INSERT ON feature_areas
WHEN NEW.legacy_read_only<>0 OR NEW.legacy_status IS NOT NULL OR NEW.status NOT IN ('DISCOVERING','ACTIVE')
BEGIN SELECT RAISE(ABORT,'new FeatureArea must be current'); END;
CREATE TRIGGER feature_areas_decompose_guard BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status='DECOMPOSED' AND (EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=NEW.id AND legacy_read_only=0 AND task_type='DEVELOPMENT') OR NOT EXISTS(SELECT 1 FROM feature_area_relations WHERE source_feature_area_id=NEW.id))
BEGIN SELECT RAISE(ABORT,'FeatureArea decomposition requires children and zero Development Tasks'); END;
CREATE TRIGGER feature_areas_legacy_immutable BEFORE UPDATE OF legacy_read_only,legacy_status ON feature_areas
WHEN NEW.legacy_read_only IS NOT OLD.legacy_read_only OR NEW.legacy_status IS NOT OLD.legacy_status
BEGIN SELECT RAISE(ABORT,'legacy FeatureArea compatibility state is immutable'); END;
CREATE TRIGGER feature_areas_lifecycle_transition BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status<>OLD.status AND NOT (OLD.legacy_read_only=0 AND (
 (OLD.status='DISCOVERING' AND NEW.status IN ('ACTIVE','DECOMPOSED','RETIRING')) OR
 (OLD.status='ACTIVE' AND NEW.status='RETIRING') OR (OLD.status='RETIRING' AND NEW.status='RETIRED')))
BEGIN SELECT RAISE(ABORT,'invalid FeatureArea lifecycle transition'); END;
CREATE TRIGGER inspection_task_state AFTER UPDATE OF status ON context_probes
BEGIN UPDATE tasks SET status=CASE NEW.status WHEN 'CREATED' THEN 'PLANNING' WHEN 'NO_GUIDANCE' THEN 'SUCCEEDED' ELSE NEW.status END,updated_at=NEW.updated_at WHERE id=NEW.task_id; END;
CREATE TRIGGER legacy_review_grants_no_update BEFORE UPDATE ON legacy_review_grants
BEGIN SELECT RAISE(ABORT,'legacy grant is audit history'); END;
CREATE TRIGGER legacy_task_audit_no_update BEFORE UPDATE ON legacy_task_audit
BEGIN SELECT RAISE(ABORT,'legacy Task audit is immutable'); END;
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
CREATE TRIGGER orphan_disposition_immutable BEFORE UPDATE ON agent_orphan_dispositions BEGIN SELECT RAISE(ABORT,'orphan disposition is permanent history'); END;
CREATE TRIGGER orphan_disposition_no_delete BEFORE DELETE ON agent_orphan_dispositions BEGIN SELECT RAISE(ABORT,'orphan disposition is permanent history'); END;
CREATE TRIGGER orphan_exact_record BEFORE INSERT ON agent_orphan_dispositions
WHEN NOT EXISTS(SELECT 1 FROM runs r JOIN tasks t ON t.id=r.task_id JOIN agent_operation_boundaries b ON b.run_id=r.id JOIN agent_conversations a ON a.id=b.agent_conversation_id JOIN feature_area_agent_claims c ON c.run_id=r.id
 WHERE r.id=NEW.run_id AND r.task_id=NEW.task_id AND r.feature_area_id=NEW.feature_area_id AND r.operation_kind=NEW.owner_type AND r.operation_id=NEW.owner_id
 AND r.runtime_epoch IS NEW.original_runtime_epoch AND NEW.observed_runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND r.thread_id IS NEW.thread_id AND r.turn_id IS NEW.turn_id AND r.workspace_id IS NEW.workspace_id
 AND a.id=NEW.conversation_id AND a.ended_at IS NOT NULL AND b.advance_state='UNKNOWN' AND t.status IN ('CANCELED','SUCCEEDED','FAILED')
 AND c.runtime_epoch IS r.runtime_epoch AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id
 AND (r.workspace_id IS NULL OR EXISTS(SELECT 1 FROM workspaces w WHERE w.id=r.workspace_id AND w.runtime_quarantined=1)))
BEGIN SELECT RAISE(ABORT,'orphan disposition requires exact fenced and quarantined owner'); END;
CREATE TRIGGER preparation_canceled_immutable BEFORE UPDATE ON task_preparations WHEN OLD.status='CANCELED' BEGIN SELECT RAISE(ABORT,'canceled Preparation is terminal'); END;
CREATE TRIGGER preparation_task_terminal_guard BEFORE INSERT ON task_preparations
WHEN EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND status='CANCELED') OR EXISTS(SELECT 1 FROM execution_cancellations WHERE task_id=NEW.task_id AND state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED'))
BEGIN SELECT RAISE(ABORT,'Task cancellation prevents Preparation admission'); END;
CREATE TRIGGER probe_work_binding_immutable BEFORE UPDATE OF task_id,run_id,runtime_epoch ON context_probes WHEN NEW.task_id IS NOT OLD.task_id OR (OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id) OR (OLD.runtime_epoch IS NOT NULL AND NEW.runtime_epoch IS NOT OLD.runtime_epoch) BEGIN SELECT RAISE(ABORT,'Inspection identity is immutable'); END;
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
CREATE TRIGGER retirement_approval_decision BEFORE UPDATE OF status ON approvals WHEN NEW.status='DECIDED' AND EXISTS(SELECT 1 FROM runs r JOIN feature_areas f ON f.id=r.feature_area_id WHERE r.id=NEW.run_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_completion_guard BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status='RETIRED' AND (
 NOT EXISTS(SELECT 1 FROM feature_area_retirements WHERE feature_area_id=NEW.id) OR
 EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=NEW.id AND legacy_read_only=0 AND status NOT IN ('SUCCEEDED','FAILED','CANCELED')) OR
 EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=NEW.id) OR
 EXISTS(SELECT 1 FROM task_preparations WHERE feature_area_id=NEW.id AND status IN ('DRAFT','PREPARING')) OR
 EXISTS(SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id WHERE t.feature_area_id=NEW.id AND e.outcome IN ('PENDING','RUNNING') AND e.status IN ('PENDING_CONFIRMATION','CONFIRMED','CONSUMED')))
BEGIN SELECT RAISE(ABORT,'retirement convergence incomplete'); END;
CREATE TRIGGER retirement_execution_advance BEFORE UPDATE ON execution_proposals WHEN (NEW.status IN ('CONFIRMED','CONSUMED') AND NEW.status<>OLD.status OR NEW.verification_run_id IS NOT OLD.verification_run_id) AND EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_execution_insert BEFORE INSERT ON execution_proposals WHEN EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_identity_immutable BEFORE UPDATE OF feature_area_id,request_id,requested_by_ai_session_id,requested_at ON feature_area_retirements
BEGIN SELECT RAISE(ABORT,'retirement identity is immutable'); END;
CREATE TRIGGER retirement_intent_required BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status='RETIRING' AND NOT EXISTS(SELECT 1 FROM feature_area_retirements WHERE feature_area_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'retirement intent required'); END;
CREATE TRIGGER retirement_no_delete BEFORE DELETE ON feature_area_retirements BEGIN SELECT RAISE(ABORT,'retirement is permanent history'); END;
CREATE TRIGGER retirement_plan_decision BEFORE UPDATE OF status ON task_plans WHEN NEW.status IN ('CONFIRMED','APPLIED') AND EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_plan_insert BEFORE INSERT ON task_plans WHEN EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_preparation_insert BEFORE INSERT ON task_preparations WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_preparation_start BEFORE UPDATE OF status ON task_preparations WHEN NEW.status IN ('PREPARING','READY') AND NEW.status<>OLD.status AND EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_review_insert BEFORE INSERT ON reviews WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_revision_insert BEFORE INSERT ON task_definition_revisions WHEN EXISTS(SELECT 1 FROM tasks t JOIN feature_areas f ON f.id=t.feature_area_id WHERE t.id=NEW.task_id AND f.status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_run_insert BEFORE INSERT ON runs WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_run_start BEFORE UPDATE OF status,thread_id,turn_id ON runs
WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) AND
 ((NEW.status IN ('PREPARING','RUNNING') AND NEW.status IS NOT OLD.status) OR NEW.thread_id IS NOT OLD.thread_id OR NEW.turn_id IS NOT OLD.turn_id)
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_task_advance BEFORE UPDATE ON tasks WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) AND
 (NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id OR NEW.work_spec_json IS NOT OLD.work_spec_json OR NEW.verification_spec_json IS NOT OLD.verification_spec_json OR NEW.model IS NOT OLD.model OR NEW.reasoning_effort IS NOT OLD.reasoning_effort OR NEW.confirmed_task_plan_id IS NOT OLD.confirmed_task_plan_id OR (NEW.status IN ('READY','RUNNING') AND NEW.status IS NOT OLD.status))
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER retirement_task_insert BEFORE INSERT ON tasks WHEN EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('RETIRING','RETIRED')) BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_RETIRING'); END;
CREATE TRIGGER review_approval_immutable BEFORE UPDATE ON review_approval_decisions BEGIN SELECT RAISE(ABORT,'Review Approval is immutable'); END;
CREATE TRIGGER review_approval_no_delete BEFORE DELETE ON review_approval_decisions BEGIN SELECT RAISE(ABORT,'Review Approval is durable'); END;
CREATE TRIGGER review_grants_exact_owner BEFORE INSERT ON review_grants
WHEN NOT EXISTS(SELECT 1 FROM reviews WHERE id=NEW.review_id AND project_id=NEW.project_id AND feature_area_id=NEW.feature_area_id AND repository_id=NEW.repository_id)
BEGIN SELECT RAISE(ABORT,'Review grant must match exact Review owner'); END;
CREATE TRIGGER review_grants_immutable
BEFORE UPDATE ON review_grants
BEGIN SELECT RAISE(ABORT, 'initial Review grant is immutable'); END;
CREATE TRIGGER review_materialization_immutable BEFORE UPDATE ON review_approval_materializations BEGIN SELECT RAISE(ABORT,'Review materialization is immutable'); END;
CREATE TRIGGER review_materialization_no_delete BEFORE DELETE ON review_approval_materializations BEGIN SELECT RAISE(ABORT,'Review materialization is durable'); END;
CREATE TRIGGER review_receipt_context_immutable BEFORE UPDATE ON review_approval_receipt_context BEGIN SELECT RAISE(ABORT,'Review receipt context is immutable'); END;
CREATE TRIGGER review_receipt_context_no_delete BEFORE DELETE ON review_approval_receipt_context BEGIN SELECT RAISE(ABORT,'Review receipt context is durable'); END;
CREATE TRIGGER review_repository_provenance_immutable BEFORE UPDATE ON review_repository_provenance
BEGIN SELECT RAISE(ABORT, 'review repository provenance is immutable'); END;
CREATE TRIGGER review_repository_provenance_no_delete AFTER DELETE ON review_repository_provenance
BEGIN SELECT RAISE(ABORT, 'review repository provenance is durable history'); END;
CREATE TRIGGER review_start_failure_task AFTER UPDATE OF start_failure_code ON reviews WHEN NEW.start_failure_code IS NOT NULL
BEGIN UPDATE tasks SET status='FAILED',updated_at=NEW.start_failure_at WHERE id=NEW.task_id AND status='PLANNING'; END;
CREATE TRIGGER review_task_run_state AFTER UPDATE OF status ON runs WHEN NEW.operation_kind='REVIEW'
BEGIN UPDATE tasks SET status=CASE WHEN NEW.status='SUCCEEDED' THEN 'SUCCEEDED' WHEN NEW.status='CANCELED' THEN 'CANCELED' WHEN NEW.status IN ('FAILED','INFRASTRUCTURE_FAILED') THEN 'FAILED' ELSE 'RUNNING' END,updated_at=NEW.updated_at WHERE id=NEW.task_id; END;
CREATE TRIGGER review_work_binding_immutable BEFORE UPDATE OF task_id ON reviews WHEN NEW.task_id IS NOT OLD.task_id BEGIN SELECT RAISE(ABORT,'Review Task is immutable'); END;
CREATE TRIGGER review_workflow_source_immutable BEFORE UPDATE ON review_workflow_sources BEGIN SELECT RAISE(ABORT,'Review workflow source is immutable'); END;
CREATE TRIGGER review_workflow_source_no_delete BEFORE DELETE ON review_workflow_sources BEGIN SELECT RAISE(ABORT,'Review workflow source is durable'); END;
CREATE TRIGGER reviews_current_owner BEFORE INSERT ON reviews
WHEN NEW.legacy_task_id IS NOT NULL OR NEW.repository_id IS NULL OR NEW.codex_config_id IS NULL OR length(COALESCE(NEW.base_commit,'')) NOT IN (40,64)
 OR NOT EXISTS(SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND project_id=NEW.project_id AND legacy_read_only=0 AND status IN ('DISCOVERING','ACTIVE'))
 OR NOT EXISTS(SELECT 1 FROM repositories WHERE id=NEW.repository_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Review requires current FeatureArea and repository ownership'); END;
CREATE TRIGGER reviews_identity_immutable BEFORE UPDATE OF id,project_id,feature_area_id,created_by_ai_session_id,legacy_task_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at ON reviews
BEGIN SELECT RAISE(ABORT, 'review identity is immutable'); END;
CREATE TRIGGER reviews_linked_run_owner BEFORE UPDATE OF linked_run_id ON reviews
WHEN NEW.linked_run_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM runs WHERE id=NEW.linked_run_id AND review_id=NEW.id AND operation_kind='REVIEW' AND operation_id=NEW.id AND feature_area_id=NEW.feature_area_id AND project_id=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Review Run owner mismatch'); END;
CREATE TRIGGER reviews_prompt_immutable BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON reviews
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'review prompt is immutable'); END;
CREATE TRIGGER runs_execution_identity_immutable BEFORE UPDATE OF purpose,execution_proposal_id,codex_instruction,review_scope_kind,review_scope_paths_json,review_mode,selected_modules_json ON runs
BEGIN SELECT RAISE(ABORT, 'run execution identity is immutable'); END;
CREATE TRIGGER runs_operation_owner_immutable BEFORE UPDATE OF project_id,feature_area_id,operation_kind,operation_id,task_id,review_id,task_preparation_id,context_probe_id ON runs
WHEN NEW.project_id IS NOT OLD.project_id OR NEW.feature_area_id IS NOT OLD.feature_area_id OR NEW.operation_kind IS NOT OLD.operation_kind OR NEW.operation_id IS NOT OLD.operation_id OR NEW.task_id IS NOT OLD.task_id OR NEW.review_id IS NOT OLD.review_id OR NEW.task_preparation_id IS NOT OLD.task_preparation_id OR NEW.context_probe_id IS NOT OLD.context_probe_id
BEGIN SELECT RAISE(ABORT,'Run operation owner is immutable'); END;
CREATE TRIGGER runs_operation_owner_insert BEFORE INSERT ON runs
WHEN NOT EXISTS(SELECT 1 FROM tasks t WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.legacy_read_only=0 AND t.status<>'CANCELED' AND
 ((NEW.operation_kind='INSPECTION' AND NEW.purpose='INSPECTION' AND NEW.feature_area_id IS NULL AND t.task_type='INSPECTION' AND t.task_scope='PROJECT' AND t.work_owner_id=NEW.context_probe_id AND NEW.operation_id=NEW.context_probe_id AND EXISTS(SELECT 1 FROM context_probes p WHERE p.id=NEW.context_probe_id AND p.task_id=t.id AND p.status='RUNNING'))
 OR (t.task_scope='FEATURE_AREA' AND t.feature_area_id=NEW.feature_area_id AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=t.feature_area_id AND f.legacy_read_only=0 AND ((NEW.operation_kind='REVIEW' AND f.status IN ('DISCOVERING','ACTIVE')) OR (NEW.operation_kind IN ('PREPARATION','EXECUTION') AND f.status='ACTIVE'))) AND
 ((NEW.operation_kind='REVIEW' AND t.task_type='REVIEW' AND t.work_owner_id=NEW.review_id AND NEW.operation_id=NEW.review_id AND NEW.purpose='SCOPE_REVIEW' AND NEW.execution_proposal_id IS NULL AND NEW.task_preparation_id IS NULL AND EXISTS(SELECT 1 FROM reviews v WHERE v.id=NEW.review_id AND v.task_id=t.id))
 OR (NEW.operation_kind='PREPARATION' AND t.task_type='DEVELOPMENT' AND NEW.operation_id=NEW.task_preparation_id AND NEW.review_id IS NULL AND NEW.execution_proposal_id IS NULL AND NEW.purpose='SCOPE_REVIEW' AND EXISTS(SELECT 1 FROM task_preparations p WHERE p.id=NEW.task_preparation_id AND p.task_id=t.id AND p.task_definition_revision_id IS NOT NULL))
 OR (NEW.operation_kind='EXECUTION' AND t.task_type='DEVELOPMENT' AND NEW.operation_id=NEW.execution_proposal_id AND NEW.review_id IS NULL AND NEW.task_preparation_id IS NULL AND NEW.purpose IN ('IMPLEMENTATION','VERIFICATION') AND EXISTS(SELECT 1 FROM execution_proposals e WHERE e.id=NEW.execution_proposal_id AND e.task_id=t.id AND e.outcome IN ('PENDING','RUNNING') AND e.task_definition_revision_id IS NOT NULL)))))
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=t.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')))
BEGIN SELECT RAISE(ABORT,'Agent Run requires durable visible Task and exact work owner'); END;
CREATE TRIGGER runs_orphan_fence BEFORE UPDATE ON runs WHEN EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=OLD.id) BEGIN SELECT RAISE(ABORT,'orphaned Run authority permanently revoked'); END;
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
CREATE TRIGGER runs_workspace_owner_guard BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND ((NEW.operation_kind='REVIEW' AND w.review_id=NEW.review_id AND w.task_id IS NULL) OR (NEW.operation_kind<>'REVIEW' AND w.task_id=NEW.task_id AND w.review_id IS NULL)))
BEGIN SELECT RAISE(ABORT,'Run workspace must match operation environment'); END;
CREATE TRIGGER runtime_epoch_immutable BEFORE UPDATE ON runtime_epochs
BEGIN SELECT RAISE(ABORT,'runtime epoch is immutable'); END;
CREATE TRIGGER runtime_epoch_no_delete BEFORE DELETE ON runtime_epochs
BEGIN SELECT RAISE(ABORT,'runtime epoch history cannot be deleted'); END;
CREATE TRIGGER scope_expansions_current_owner BEFORE INSERT ON scope_expansions
WHEN NEW.legacy_task_id IS NOT NULL OR NEW.review_id IS NULL OR NOT EXISTS(
 SELECT 1 FROM reviews r JOIN review_grants g ON g.review_id=r.id JOIN feature_areas f ON f.id=r.feature_area_id
 WHERE r.id=NEW.review_id AND r.project_id=NEW.project_id AND r.feature_area_id=NEW.feature_area_id AND r.repository_id=NEW.repository_id
 AND g.project_id=NEW.project_id AND g.feature_area_id=NEW.feature_area_id AND g.repository_id=NEW.repository_id
 AND f.legacy_read_only=0 AND f.status IN ('DISCOVERING','ACTIVE'))
BEGIN SELECT RAISE(ABORT,'scope expansion requires exact current Review grant'); END;
CREATE TRIGGER scope_expansions_definition_immutable
BEFORE UPDATE OF id,ai_session_id,project_id,review_id,feature_area_id,repository_id,legacy_task_id,current_scope_kind,current_scope_paths_json,additional_scope_kind,additional_scope_paths_json,reason,created_at ON scope_expansions
BEGIN SELECT RAISE(ABORT, 'scope expansion definition is immutable'); END;
CREATE TRIGGER scope_expansions_legacy_readonly BEFORE UPDATE ON scope_expansions WHEN OLD.legacy_task_id IS NOT NULL
BEGIN SELECT RAISE(ABORT,'legacy scope expansion is read-only'); END;
CREATE TRIGGER scope_expansions_no_delete BEFORE DELETE ON scope_expansions
BEGIN SELECT RAISE(ABORT,'scope expansion is durable history'); END;
CREATE TRIGGER scope_expansions_status_transition
BEFORE UPDATE OF status,decided_at ON scope_expansions
WHEN NOT (OLD.status='PENDING_CONFIRMATION' AND NEW.status IN ('ALLOWED','REJECTED') AND
          OLD.decided_at IS NULL AND NEW.decided_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'invalid scope expansion status transition'); END;
CREATE TRIGGER task_canceled_authority BEFORE UPDATE ON tasks
WHEN OLD.status='CANCELED' AND (NEW.status IS NOT OLD.status OR NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id OR NEW.work_spec_json IS NOT OLD.work_spec_json)
BEGIN SELECT RAISE(ABORT,'canceled Task has no workflow authority'); END;
CREATE TRIGGER task_decision_owner_guard BEFORE UPDATE OF status ON task_plans
WHEN NEW.status IN ('CONFIRMED','APPLIED') AND EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND status='CANCELED')
BEGIN SELECT RAISE(ABORT,'canceled Task decision is not actionable'); END;
CREATE TRIGGER task_definition_revisions_immutable BEFORE UPDATE ON task_definition_revisions
BEGIN SELECT RAISE(ABORT,'Task definition revision is immutable'); END;
CREATE TRIGGER task_definition_revisions_no_delete BEFORE DELETE ON task_definition_revisions
BEGIN SELECT RAISE(ABORT,'Task definition revision is durable history'); END;
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
CREATE TRIGGER task_preparations_current_definition BEFORE INSERT ON task_preparations
WHEN NEW.task_definition_revision_id IS NULL OR NOT EXISTS(
 SELECT 1 FROM tasks t JOIN task_definition_revisions d ON d.task_id=t.id JOIN feature_areas f ON f.id=t.feature_area_id
 WHERE t.id=NEW.task_id AND t.project_id=NEW.project_id AND t.feature_area_id=NEW.feature_area_id AND t.repository_id=NEW.repository_id
 AND t.legacy_read_only=0 AND f.legacy_read_only=0 AND f.status='ACTIVE' AND d.id=NEW.task_definition_revision_id
 AND json_extract(d.payload_json,'$.repositoryId')=NEW.repository_id
 AND json_extract(d.payload_json,'$.featureAreaId')=NEW.feature_area_id
 AND json_extract(d.payload_json,'$.projectId')=NEW.project_id)
BEGIN SELECT RAISE(ABORT,'Preparation requires exact Task definition and ACTIVE FeatureArea'); END;
CREATE TRIGGER task_preparations_final_result_immutable BEFORE UPDATE OF status,result_json,task_plan_id,failure_code,failure_summary ON task_preparations
WHEN OLD.status IN ('READY','CONFLICT','FAILED') AND (NEW.status IS NOT OLD.status OR NEW.result_json IS NOT OLD.result_json OR NEW.task_plan_id IS NOT OLD.task_plan_id OR NEW.failure_code IS NOT OLD.failure_code OR NEW.failure_summary IS NOT OLD.failure_summary)
BEGIN SELECT RAISE(ABORT,'final Preparation is immutable'); END;
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
CREATE TRIGGER task_preparations_one_live BEFORE INSERT ON task_preparations
WHEN NEW.status IN ('DRAFT','PREPARING') AND EXISTS(SELECT 1 FROM task_preparations WHERE task_id=NEW.task_id AND status IN ('DRAFT','PREPARING'))
BEGIN SELECT RAISE(ABORT,'Task already has a live Preparation'); END;
CREATE TRIGGER task_preparations_prompt_immutable
BEFORE UPDATE OF final_codex_prompt,prompt_sha256,prompt_bytes,prompt_sent_at ON task_preparations
WHEN OLD.final_codex_prompt IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'Task Preparation prompt is immutable'); END;
CREATE TRIGGER task_preparations_source_review_guard BEFORE INSERT ON task_preparations
WHEN NEW.source_review_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM reviews WHERE id=NEW.source_review_id AND project_id=NEW.project_id AND feature_area_id=NEW.feature_area_id)
BEGIN SELECT RAISE(ABORT,'source Review must belong to Preparation FeatureArea'); END;
CREATE TRIGGER task_preparations_status_transition BEFORE UPDATE OF status ON task_preparations
WHEN NEW.status<>OLD.status AND NOT ((OLD.status='DRAFT' AND NEW.status IN ('PREPARING','CANCELED')) OR (OLD.status='PREPARING' AND NEW.status IN ('READY','CONFLICT','FAILED','CANCELED')))
BEGIN SELECT RAISE(ABORT,'invalid Preparation transition'); END;
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
CREATE TRIGGER task_proposals_retired_delete BEFORE DELETE ON task_proposals
BEGIN SELECT RAISE(ABORT,'legacy TaskProposal history is durable'); END;
CREATE TRIGGER task_proposals_retired_insert BEFORE INSERT ON task_proposals
BEGIN SELECT RAISE(ABORT,'legacy TaskProposal authoring is retired'); END;
CREATE TRIGGER task_proposals_retired_update BEFORE UPDATE ON task_proposals
BEGIN SELECT RAISE(ABORT,'legacy TaskProposal history is read-only'); END;
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
CREATE TRIGGER task_revision_development_only BEFORE INSERT ON task_definition_revisions WHEN NOT EXISTS(SELECT 1 FROM tasks WHERE id=NEW.task_id AND task_type='DEVELOPMENT' AND status<>'CANCELED')
BEGIN SELECT RAISE(ABORT,'only Development has definition revisions'); END;
CREATE TRIGGER task_revision_pointer_guard BEFORE UPDATE OF current_definition_revision_id ON tasks
WHEN NEW.current_definition_revision_id IS NULL OR NOT EXISTS(SELECT 1 FROM task_definition_revisions WHERE id=NEW.current_definition_revision_id AND task_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'Task definition revision must belong to Task'); END;
CREATE TRIGGER task_work_identity_immutable BEFORE UPDATE OF task_type,task_scope,work_owner_id ON tasks
WHEN NEW.task_type IS NOT OLD.task_type OR NEW.task_scope IS NOT OLD.task_scope OR NEW.work_owner_id IS NOT OLD.work_owner_id
BEGIN SELECT RAISE(ABORT,'Task work ownership is immutable'); END;
CREATE TRIGGER tasks_current_insert BEFORE INSERT ON tasks
WHEN NEW.legacy_read_only<>0 OR NEW.agent_context IS NOT NULL OR NOT (
 (NEW.task_type='INSPECTION' AND NEW.task_scope='PROJECT' AND NEW.feature_area_id IS NULL AND NEW.work_owner_id IS NOT NULL)
 OR (NEW.task_scope='FEATURE_AREA' AND EXISTS(SELECT 1 FROM feature_areas f WHERE f.id=NEW.feature_area_id AND f.project_id=NEW.project_id AND f.legacy_read_only=0 AND
 ((NEW.task_type='DEVELOPMENT' AND f.status='ACTIVE' AND NEW.work_owner_id IS NULL AND NEW.module_id IS NOT NULL AND NEW.repository_id IS NOT NULL AND NEW.codex_config_id IS NOT NULL) OR (NEW.task_type='REVIEW' AND f.status IN ('DISCOVERING','ACTIVE') AND NEW.work_owner_id IS NOT NULL)))))
BEGIN SELECT RAISE(ABORT,'Task requires exact type, scope and owner'); END;
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
CREATE TRIGGER tasks_legacy_context_immutable BEFORE UPDATE OF agent_context,legacy_read_only ON tasks
WHEN NEW.agent_context IS NOT OLD.agent_context OR NEW.legacy_read_only IS NOT OLD.legacy_read_only
BEGIN SELECT RAISE(ABORT,'legacy Task context and compatibility status are immutable'); END;
CREATE TRIGGER tasks_legacy_read_only BEFORE UPDATE ON tasks WHEN OLD.legacy_read_only=1
BEGIN SELECT RAISE(ABORT,'legacy Task is read-only'); END;
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
CREATE TRIGGER workspaces_legacy_thread_immutable BEFORE UPDATE OF legacy_thread_id ON workspaces WHEN NEW.legacy_thread_id IS NOT OLD.legacy_thread_id
BEGIN SELECT RAISE(ABORT,'legacy Workspace thread is audit history'); END;
CREATE TRIGGER workspaces_no_current_thread BEFORE INSERT ON workspaces WHEN NEW.legacy_thread_id IS NOT NULL
BEGIN SELECT RAISE(ABORT,'Workspace cannot own canonical Agent thread'); END;
CREATE TRIGGER workspaces_preparation_immutable BEFORE UPDATE OF preparation_id ON workspaces
WHEN NEW.preparation_id IS NOT OLD.preparation_id
BEGIN SELECT RAISE(ABORT,'Workspace Preparation is immutable'); END;
CREATE TRIGGER workspaces_preparation_owner BEFORE INSERT ON workspaces
WHEN NEW.preparation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM task_preparations WHERE id=NEW.preparation_id AND task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'Workspace requires exact Preparation Task owner'); END;
CREATE TRIGGER workspaces_review_immutable BEFORE UPDATE OF review_id ON workspaces WHEN NEW.review_id IS NOT OLD.review_id
BEGIN SELECT RAISE(ABORT,'Workspace Review owner is immutable'); END;
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
CREATE INDEX codex_server_requests_run ON codex_server_requests(run_id);
CREATE TRIGGER agent_runtime_identity_immutable BEFORE UPDATE OF run_id,runtime_epoch,connection_id,thread_id,turn_id ON agent_runtime_states
WHEN NEW.run_id<>OLD.run_id OR NEW.runtime_epoch<>OLD.runtime_epoch OR NEW.connection_id<>OLD.connection_id OR NEW.thread_id<>OLD.thread_id OR NEW.turn_id<>OLD.turn_id
BEGIN SELECT RAISE(ABORT,'agent runtime identity is immutable'); END;
CREATE INDEX run_agent_process_launch ON run_agent_processes(launch_id);
CREATE TRIGGER agent_process_identity_immutable BEFORE UPDATE OF launch_id,pid,executable,started_at,runtime_epoch ON agent_process_launches
BEGIN SELECT RAISE(ABORT,'process launch identity is immutable'); END;
CREATE TRIGGER run_agent_process_immutable BEFORE UPDATE ON run_agent_processes
BEGIN SELECT RAISE(ABORT,'Run process binding is immutable'); END;
DELETE FROM "sqlite_sequence";
COMMIT;
PRAGMA foreign_keys=ON;
