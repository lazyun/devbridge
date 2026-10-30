// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

// v26 does not infer lineage from legacy Task/Workspace Thread selections.
// Workspace Thread values remain readable history, without global ownership.
const schemaV26 = `
DROP TRIGGER runs_workspace_task_guard;
CREATE TABLE workspaces_v26 (
 id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id),
 worktree_id TEXT UNIQUE REFERENCES worktrees(id),
 thread_id TEXT,
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
INSERT INTO workspaces_v26 SELECT * FROM workspaces;
DROP TABLE workspaces;
ALTER TABLE workspaces_v26 RENAME TO workspaces;
CREATE INDEX workspaces_status_updated ON workspaces(status,updated_at,id);
CREATE INDEX workspaces_thread_history ON workspaces(thread_id);
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
CREATE TRIGGER runs_workspace_task_guard
BEFORE UPDATE OF workspace_id ON runs
WHEN NEW.workspace_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM workspaces w WHERE w.id=NEW.workspace_id AND w.task_id=NEW.task_id
)
BEGIN
 SELECT RAISE(ABORT, 'run workspace must belong to run task');
END;


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
CREATE UNIQUE INDEX agent_conversations_one_current ON agent_conversations(feature_area_id) WHERE ended_at IS NULL;
CREATE INDEX agent_conversations_history ON agent_conversations(feature_area_id,created_at,id);
CREATE TRIGGER agent_conversations_identity_immutable
BEFORE UPDATE OF id,project_id,feature_area_id,backend,host_tool_contract_version,predecessor_id,created_at ON agent_conversations
BEGIN SELECT RAISE(ABORT,'agent conversation identity is immutable'); END;
CREATE TRIGGER agent_conversations_thread_immutable
BEFORE UPDATE OF backend_thread_id ON agent_conversations
WHEN OLD.backend_thread_id IS NOT NULL AND NEW.backend_thread_id IS NOT OLD.backend_thread_id
BEGIN SELECT RAISE(ABORT,'agent conversation backend thread is immutable'); END;
CREATE TRIGGER agent_conversations_history_immutable
BEFORE UPDATE ON agent_conversations WHEN OLD.ended_at IS NOT NULL
BEGIN SELECT RAISE(ABORT,'historical agent conversation is immutable'); END;
CREATE TRIGGER agent_conversations_no_delete BEFORE DELETE ON agent_conversations
BEGIN SELECT RAISE(ABORT,'agent conversation history cannot be deleted'); END;
CREATE TRIGGER agent_conversations_current_lifecycle
BEFORE INSERT ON agent_conversations WHEN NEW.ended_at IS NULL AND NOT EXISTS(
 SELECT 1 FROM feature_areas WHERE id=NEW.feature_area_id AND status IN ('DISCOVERING','ACTIVE','SPLITTING'))
BEGIN SELECT RAISE(ABORT,'Feature Area cannot own current agent context'); END;
CREATE TRIGGER agent_conversations_linear_predecessor
BEFORE INSERT ON agent_conversations WHEN NEW.predecessor_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM agent_conversations WHERE id=NEW.predecessor_id AND feature_area_id=NEW.feature_area_id AND ended_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'agent conversation predecessor must be historical in the same Feature Area'); END;

CREATE TABLE feature_area_agent_claims (
 feature_area_id TEXT PRIMARY KEY REFERENCES feature_areas(id),
 operation_kind TEXT NOT NULL CHECK(operation_kind IN ('REVIEW','PREPARATION','EXECUTION')),
 operation_id TEXT NOT NULL,
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 created_at TEXT NOT NULL,
 UNIQUE(operation_kind,operation_id)
);
CREATE TABLE agent_operation_boundaries (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 agent_conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 entry_boundary_turn_id TEXT,
 exit_boundary_turn_id TEXT,
 advance_state TEXT NOT NULL CHECK(advance_state IN ('NO_ADVANCE','KNOWN_ADVANCE','UNKNOWN')),
 quiesced INTEGER NOT NULL DEFAULT 0 CHECK(quiesced IN (0,1)),
 CHECK((advance_state='KNOWN_ADVANCE' AND exit_boundary_turn_id IS NOT NULL) OR
       (advance_state IN ('NO_ADVANCE','UNKNOWN') AND exit_boundary_turn_id IS NULL))
);
CREATE TRIGGER agent_operation_entry_immutable
BEFORE UPDATE OF run_id,agent_conversation_id,entry_boundary_turn_id ON agent_operation_boundaries
BEGIN SELECT RAISE(ABORT,'agent operation entry provenance is immutable'); END;
CREATE TRIGGER agent_operation_exit_immutable
BEFORE UPDATE ON agent_operation_boundaries WHEN OLD.quiesced=1
BEGIN SELECT RAISE(ABORT,'agent operation exit provenance is immutable'); END;
CREATE TRIGGER agent_operation_history_no_delete BEFORE DELETE ON agent_operation_boundaries
BEGIN SELECT RAISE(ABORT,'agent operation provenance cannot be deleted'); END;
CREATE TABLE agent_context_resets (
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 request_id TEXT NOT NULL CHECK(length(request_id)>0 AND length(request_id)<=128),
 conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 PRIMARY KEY(feature_area_id,request_id)
);

-- Terminal product state alone cannot release a runtime that may still emit
-- backend events. An absent boundary means execution never entered BeginRun.
CREATE VIEW releasable_agent_claims AS
 SELECT c.feature_area_id FROM feature_area_agent_claims c
 JOIN runs r ON r.id=c.run_id
 LEFT JOIN agent_operation_boundaries b ON b.run_id=r.id
 WHERE r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')
 AND (b.run_id IS NULL OR b.quiesced=1)
 AND (c.operation_kind<>'EXECUTION' OR r.execution_proposal_id IS NULL OR EXISTS(
  SELECT 1 FROM execution_proposals e WHERE e.id=c.operation_id AND e.outcome IN ('SUCCEEDED','FAILED','CANCELED')))
 AND (c.operation_kind<>'PREPARATION' OR EXISTS(
  SELECT 1 FROM task_preparations p WHERE p.id=c.operation_id AND p.status IN ('READY','CONFLICT','FAILED')));
CREATE TRIGGER agent_claim_release_run AFTER UPDATE OF status ON runs
BEGIN DELETE FROM feature_area_agent_claims WHERE feature_area_id IN (SELECT feature_area_id FROM releasable_agent_claims); END;
CREATE TRIGGER agent_claim_release_execution AFTER UPDATE OF outcome ON execution_proposals
BEGIN DELETE FROM feature_area_agent_claims WHERE feature_area_id IN (SELECT feature_area_id FROM releasable_agent_claims); END;
CREATE TRIGGER agent_claim_release_preparation AFTER UPDATE OF status ON task_preparations
BEGIN DELETE FROM feature_area_agent_claims WHERE feature_area_id IN (SELECT feature_area_id FROM releasable_agent_claims); END;
CREATE TRIGGER agent_claim_release_boundary AFTER UPDATE OF quiesced ON agent_operation_boundaries
BEGIN DELETE FROM feature_area_agent_claims WHERE feature_area_id IN (SELECT feature_area_id FROM releasable_agent_claims); END;
CREATE TRIGGER feature_area_agent_busy_lifecycle BEFORE UPDATE OF status ON feature_areas
WHEN NEW.status<>OLD.status AND EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE feature_area_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'FEATURE_AREA_BUSY'); END;
CREATE TRIGGER feature_area_agent_end AFTER UPDATE OF status ON feature_areas
WHEN NEW.status IN ('DECOMPOSED','RETIRED')
BEGIN UPDATE agent_conversations SET ended_at=NEW.updated_at WHERE feature_area_id=NEW.id AND ended_at IS NULL; END;
`
