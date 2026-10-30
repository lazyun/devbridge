// Historical validation fixture for pre-v0.1 source schemas.
// Test-only: excluded from the production binary and final schema.
package sqlite

// Legacy epoch ownership stays NULL: migration cannot attribute historical work
// to the new service incarnation. Only startup recovery may reconcile it.
const schemaV27 = `
CREATE TABLE runtime_epochs (
 id TEXT PRIMARY KEY,
 created_at TEXT NOT NULL
);
CREATE TABLE runtime_authority (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 epoch_id TEXT NOT NULL REFERENCES runtime_epochs(id)
);
CREATE TRIGGER runtime_epoch_immutable BEFORE UPDATE ON runtime_epochs
BEGIN SELECT RAISE(ABORT,'runtime epoch is immutable'); END;
CREATE TRIGGER runtime_epoch_no_delete BEFORE DELETE ON runtime_epochs
BEGIN SELECT RAISE(ABORT,'runtime epoch history cannot be deleted'); END;
ALTER TABLE runs ADD COLUMN runtime_epoch TEXT REFERENCES runtime_epochs(id);
ALTER TABLE feature_area_agent_claims ADD COLUMN runtime_epoch TEXT REFERENCES runtime_epochs(id);
ALTER TABLE agent_operation_boundaries ADD COLUMN thread_start_pending INTEGER NOT NULL DEFAULT 0 CHECK(thread_start_pending IN (0,1));
CREATE TRIGGER runs_runtime_epoch_immutable BEFORE UPDATE OF runtime_epoch ON runs
WHEN NEW.runtime_epoch IS NOT OLD.runtime_epoch
BEGIN SELECT RAISE(ABORT,'Run runtime epoch is immutable'); END;
CREATE TRIGGER claims_runtime_epoch_immutable BEFORE UPDATE OF runtime_epoch ON feature_area_agent_claims
WHEN NEW.runtime_epoch IS NOT OLD.runtime_epoch
BEGIN SELECT RAISE(ABORT,'claim runtime epoch is immutable'); END;

DROP TRIGGER agent_claim_release_run;
DROP TRIGGER agent_claim_release_execution;
DROP TRIGGER agent_claim_release_preparation;
DROP TRIGGER agent_claim_release_boundary;
DROP VIEW releasable_agent_claims;

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
CREATE TRIGGER agent_finalization_intent_identity BEFORE UPDATE OF run_id,feature_area_id,operation_kind,operation_id,agent_conversation_id,runtime_epoch,advance_state,exit_boundary_turn_id,quiesced,created_at ON agent_finalization_intents
BEGIN SELECT RAISE(ABORT,'finalization intent is immutable'); END;
CREATE TRIGGER agent_finalization_intent_no_delete BEFORE DELETE ON agent_finalization_intents
BEGIN SELECT RAISE(ABORT,'finalization intent history cannot be deleted'); END;

ALTER TABLE workspaces ADD COLUMN runtime_quarantined INTEGER NOT NULL DEFAULT 0 CHECK(runtime_quarantined IN (0,1));
CREATE TRIGGER workspace_runtime_quarantine_immutable BEFORE UPDATE OF runtime_quarantined ON workspaces
WHEN OLD.runtime_quarantined=1 AND NEW.runtime_quarantined<>1
BEGIN SELECT RAISE(ABORT,'abandoned runtime workspace must remain isolated'); END;
CREATE TABLE agent_operation_recovery_states (
 run_id TEXT PRIMARY KEY REFERENCES runs(id),
 classification TEXT NOT NULL CHECK(classification IN ('NO_ADVANCE','KNOWN_SAFE','UNKNOWN','CONFLICT')),
 detail TEXT NOT NULL,
 classified_epoch TEXT NOT NULL REFERENCES runtime_epochs(id)
);
CREATE TRIGGER agent_finalization_applied_once BEFORE UPDATE OF applied_at ON agent_finalization_intents
WHEN OLD.applied_at IS NOT NULL AND NEW.applied_at IS NOT OLD.applied_at
BEGIN SELECT RAISE(ABORT,'finalization application is immutable'); END;

CREATE TABLE agent_operation_recoveries (
 request_id TEXT NOT NULL,
 feature_area_id TEXT NOT NULL REFERENCES feature_areas(id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 operation_kind TEXT NOT NULL,
 operation_id TEXT NOT NULL,
 old_conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 new_conversation_id TEXT NOT NULL REFERENCES agent_conversations(id),
 authorized_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 created_at TEXT NOT NULL,
 PRIMARY KEY(feature_area_id,request_id),
 UNIQUE(run_id)
);
CREATE TRIGGER agent_operation_recovery_immutable BEFORE UPDATE ON agent_operation_recoveries
BEGIN SELECT RAISE(ABORT,'Agent recovery is immutable'); END;
CREATE TRIGGER agent_operation_recovery_no_delete BEFORE DELETE ON agent_operation_recoveries
BEGIN SELECT RAISE(ABORT,'Agent recovery history cannot be deleted'); END;
`
