package sqlite

// Execution start authorization is an append-only command fact. Decision
// creation and runtime delivery are deliberately distinct: a NO_START row can
// never be upgraded by replaying the same actor-scoped request.
const executionStartEffectMigrationSQL = `
CREATE TABLE execution_start_effects (
 project_id TEXT NOT NULL REFERENCES projects(id),
 actor_kind TEXT NOT NULL CHECK(actor_kind IN ('AI_SESSION','LOCAL_OPERATOR')),
 actor_id TEXT NOT NULL,
 request_id TEXT NOT NULL,
 action TEXT NOT NULL,
 effect TEXT NOT NULL CHECK(effect IN ('NO_START','START_AUTHORIZED')),
 task_id TEXT NOT NULL REFERENCES tasks(id),
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 task_definition_revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 expected_phase TEXT NOT NULL CHECK(expected_phase IN ('IMPLEMENTATION_READY','VERIFICATION_READY')),
 decision_id TEXT,
 payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256)=64),
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,actor_kind,actor_id,request_id)
);
CREATE INDEX execution_start_effects_queue ON execution_start_effects(effect,execution_id,created_at);
CREATE TRIGGER execution_start_effect_immutable BEFORE UPDATE ON execution_start_effects BEGIN SELECT RAISE(ABORT,'Execution start effect is immutable'); END;
CREATE TRIGGER execution_start_effect_no_delete BEFORE DELETE ON execution_start_effects BEGIN SELECT RAISE(ABORT,'Execution start effect is durable'); END;
`
