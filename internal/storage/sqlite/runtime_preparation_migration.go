package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// v90 records intended runtime scope separately from actual Run-bound workspace
// observations. No legacy row is promoted into these tables: missing evidence
// remains missing and active historical authority must fail closed at dispatch.
const runtimePreparationMigrationV90SQL = `
CREATE TABLE runtime_scope_plans (
 plan_id TEXT PRIMARY KEY,
 generation INTEGER NOT NULL CHECK(generation>0),
 owner_kind TEXT NOT NULL CHECK(owner_kind IN ('IMPLEMENTATION','VERIFICATION','REVIEW','INSPECTION','RECOVERY_STATUS')),
 owner_id TEXT NOT NULL,
 owner_execution_id TEXT REFERENCES execution_proposals(id),
 owner_review_id TEXT REFERENCES reviews(id),
 owner_inspection_id TEXT REFERENCES context_probes(id),
 owner_recovery_id TEXT REFERENCES recovery_status_records(id),
 project_id TEXT NOT NULL REFERENCES projects(id),
 feature_area_id TEXT REFERENCES feature_areas(id),
 task_id TEXT REFERENCES tasks(id),
 source_command TEXT NOT NULL,
 source_receipt_id TEXT NOT NULL DEFAULT '',
 selected_variant_id TEXT NOT NULL DEFAULT '',
 proposal_hash TEXT NOT NULL DEFAULT '',
 task_definition_revision_id TEXT NOT NULL DEFAULT '',
 source_execution_id TEXT NOT NULL DEFAULT '',
 source_review_id TEXT NOT NULL DEFAULT '',
 successor_execution_id TEXT NOT NULL DEFAULT '',
 successor_review_id TEXT NOT NULL DEFAULT '',
 repository_targets_json TEXT NOT NULL CHECK(json_valid(repository_targets_json)),
 primary_member_id TEXT NOT NULL,
 backend_identity TEXT NOT NULL,
 model TEXT NOT NULL,
 reasoning_effort TEXT NOT NULL,
 runtime_adapter_identity TEXT NOT NULL,
 topology_revision INTEGER NOT NULL CHECK(topology_revision>=0),
 development_context_hash TEXT NOT NULL,
 development_refs_json TEXT NOT NULL CHECK(json_valid(development_refs_json)),
 expected_source_heads_json TEXT NOT NULL CHECK(json_valid(expected_source_heads_json)),
 write_scopes_json TEXT NOT NULL CHECK(json_valid(write_scopes_json)),
 preparation_request_id TEXT NOT NULL,
 preparation_payload_hash TEXT NOT NULL CHECK(length(preparation_payload_hash)=64),
 content_hash TEXT NOT NULL CHECK(length(content_hash)=64),
 created_at TEXT NOT NULL,
 UNIQUE(owner_kind,owner_id),
 UNIQUE(preparation_request_id),
 CHECK((owner_kind IN ('IMPLEMENTATION','VERIFICATION') AND owner_execution_id=owner_id AND owner_review_id IS NULL AND owner_inspection_id IS NULL AND owner_recovery_id IS NULL) OR
       (owner_kind='REVIEW' AND owner_review_id=owner_id AND owner_execution_id IS NULL AND owner_inspection_id IS NULL AND owner_recovery_id IS NULL) OR
       (owner_kind='INSPECTION' AND owner_inspection_id=owner_id AND owner_execution_id IS NULL AND owner_review_id IS NULL AND owner_recovery_id IS NULL) OR
       (owner_kind='RECOVERY_STATUS' AND owner_recovery_id=owner_id AND owner_execution_id IS NULL AND owner_review_id IS NULL AND owner_inspection_id IS NULL))
);
CREATE TABLE runtime_preparation_authorities (
 plan_id TEXT PRIMARY KEY REFERENCES runtime_scope_plans(plan_id),
 owner_kind TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>0),
 request_id TEXT NOT NULL,
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 disposition TEXT NOT NULL CHECK(disposition IN ('CURRENT','CANCELED','SUPERSEDED')),
 created_at TEXT NOT NULL,
 UNIQUE(request_id)
);
CREATE TABLE runtime_preparation_attempts (
 attempt_id TEXT PRIMARY KEY,
 plan_id TEXT NOT NULL REFERENCES runtime_scope_plans(plan_id),
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 request_id TEXT NOT NULL,
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 stage TEXT NOT NULL CHECK(stage IN ('RUN_CREATED','WORKSPACE_CLAIMED','OBSERVATION_RECORDED','BINDING_COMMITTED','UNKNOWN','FAILED')),
 outcome TEXT NOT NULL CHECK(outcome IN ('PENDING','SUCCEEDED','FAILED','UNKNOWN')),
 created_at TEXT NOT NULL,
 UNIQUE(plan_id,request_id)
);
CREATE TABLE runtime_workspace_preparation_claims (
 request_id TEXT PRIMARY KEY,
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id),
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 claimed_at TEXT NOT NULL
);
CREATE TABLE runtime_workspace_observations (
 observation_id TEXT PRIMARY KEY,
 plan_id TEXT NOT NULL REFERENCES runtime_scope_plans(plan_id),
 run_id TEXT NOT NULL REFERENCES runs(id),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id),
 repository_id TEXT NOT NULL REFERENCES repositories(id),
 member_id TEXT NOT NULL,
 worktree_id TEXT NOT NULL,
 canonical_root TEXT NOT NULL,
 root_identity TEXT NOT NULL,
 start_head TEXT NOT NULL,
 start_snapshot TEXT NOT NULL,
 branch TEXT NOT NULL,
 metadata_digest TEXT NOT NULL CHECK(length(metadata_digest)=64),
 observation_generation INTEGER NOT NULL CHECK(observation_generation>0),
 observed_at TEXT NOT NULL,
 UNIQUE(plan_id,run_id,member_id)
);
CREATE TABLE runtime_workset_bindings (
 plan_id TEXT PRIMARY KEY REFERENCES runtime_scope_plans(plan_id),
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 owner_kind TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 workset_id TEXT NOT NULL UNIQUE,
 workset_json TEXT NOT NULL CHECK(json_valid(workset_json)),
 workset_content_hash TEXT NOT NULL CHECK(length(workset_content_hash)=64),
 observation_set_hash TEXT NOT NULL CHECK(length(observation_set_hash)=64),
 start_authority_id TEXT NOT NULL UNIQUE,
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 bound_at TEXT NOT NULL
);
CREATE TABLE runtime_start_authorities (
 authority_id TEXT PRIMARY KEY,
 plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_workset_bindings(plan_id),
 run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 owner_kind TEXT NOT NULL,
 owner_id TEXT NOT NULL,
 workset_content_hash TEXT NOT NULL,
 observation_set_hash TEXT NOT NULL,
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 created_at TEXT NOT NULL
);
CREATE TABLE runtime_preparation_receipts (
 request_id TEXT PRIMARY KEY,
 payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 plan_id TEXT NOT NULL REFERENCES runtime_scope_plans(plan_id),
 run_id TEXT REFERENCES runs(id),
 outcome TEXT NOT NULL CHECK(outcome IN ('RUN_CREATED','OBSERVATION_RECORDED','BINDING_COMMITTED','FAILED','UNKNOWN')),
 failure_code TEXT NOT NULL DEFAULT '',
 observation_set_hash TEXT NOT NULL DEFAULT '',
 workset_content_hash TEXT NOT NULL DEFAULT '',
 start_authority_id TEXT NOT NULL DEFAULT '',
 recorded_at TEXT NOT NULL
);
`

func migrateRuntimePreparationV90(ctx context.Context, tx *sql.Tx) error {
	// Historical v89 Review consumptions keep their recorded Run as legacy
	// evidence. New v2 consumptions must not create one; the Run is allocated
	// only by the common preparation transaction.
	type triggerDefinition struct{ name, sql string }
	var triggers []triggerDefinition
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql LIKE '%recovery_purpose_confirmation_consumptions%' ORDER BY name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var definition triggerDefinition
		if err = rows.Scan(&definition.name, &definition.sql); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, definition)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, definition := range triggers {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`DROP TRIGGER "%s"`, definition.name)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, runtimePurposeConsumptionV90SQL); err != nil {
		return err
	}
	for _, definition := range triggers {
		if _, err = tx.ExecContext(ctx, definition.sql); err != nil {
			return err
		}
	}
	for _, table := range []string{"runtime_scope_plans", "runtime_preparation_authorities", "runtime_preparation_attempts", "runtime_workspace_preparation_claims", "runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities", "runtime_preparation_receipts"} {
		for _, event := range []string{"UPDATE", "DELETE"} {
			name := table + "_immutable_" + event
			statement := fmt.Sprintf("CREATE TRIGGER %s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'runtime preparation fact immutable'); END", name, event, table)
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}

const runtimePurposeConsumptionV90SQL = `
CREATE TABLE recovery_purpose_confirmation_consumptions_v90 (
 intent_ai_session_id TEXT NOT NULL,
 intent_id TEXT NOT NULL,
 intent_version INTEGER NOT NULL,
 intent_content_hash TEXT NOT NULL,
 consume_action TEXT NOT NULL CHECK(consume_action IN ('APPLY_CONFIRMED_IMPLEMENTATION_PURPOSE','APPLY_CONFIRMED_VERIFICATION_PURPOSE','APPLY_CONFIRMED_REVIEW_PURPOSE')),
 consume_request_id TEXT NOT NULL,
 consume_payload_hash TEXT NOT NULL CHECK(length(consume_payload_hash)=64),
 source_owner_kind TEXT NOT NULL CHECK(source_owner_kind IN ('IMPLEMENTATION','VERIFICATION','REVIEW')),
 selected_variant_id TEXT NOT NULL,
 proposal_hash TEXT NOT NULL,
 successor_task_definition_revision_id TEXT NOT NULL DEFAULT '',
 successor_execution_id TEXT NOT NULL DEFAULT '',
 successor_review_id TEXT NOT NULL DEFAULT '',
 successor_run_id TEXT NOT NULL DEFAULT '',
 consumed_at TEXT NOT NULL,
 protocol_version INTEGER NOT NULL DEFAULT 2 CHECK(protocol_version IN (1,2)),
 PRIMARY KEY(intent_ai_session_id,intent_id,intent_version,intent_content_hash),
 UNIQUE(intent_ai_session_id,consume_request_id),
 FOREIGN KEY(intent_ai_session_id,intent_id,intent_version,intent_content_hash)
   REFERENCES recovery_purpose_confirmation_receipts(intent_ai_session_id,intent_id,intent_version,intent_content_hash),
 CHECK((source_owner_kind='REVIEW' AND consume_action='APPLY_CONFIRMED_REVIEW_PURPOSE' AND successor_review_id<>'' AND successor_execution_id='' AND successor_task_definition_revision_id='' AND ((protocol_version=1 AND successor_run_id<>'') OR (protocol_version=2 AND successor_run_id=''))) OR
       (source_owner_kind='IMPLEMENTATION' AND consume_action='APPLY_CONFIRMED_IMPLEMENTATION_PURPOSE' AND successor_task_definition_revision_id<>'' AND successor_execution_id<>'' AND successor_review_id='' AND successor_run_id='') OR
       (source_owner_kind='VERIFICATION' AND consume_action='APPLY_CONFIRMED_VERIFICATION_PURPOSE' AND successor_task_definition_revision_id<>'' AND successor_execution_id<>'' AND successor_review_id='' AND successor_run_id=''))
);
INSERT INTO recovery_purpose_confirmation_consumptions_v90(intent_ai_session_id,intent_id,intent_version,intent_content_hash,consume_action,consume_request_id,consume_payload_hash,source_owner_kind,selected_variant_id,proposal_hash,successor_task_definition_revision_id,successor_execution_id,successor_review_id,successor_run_id,consumed_at,protocol_version)
SELECT intent_ai_session_id,intent_id,intent_version,intent_content_hash,consume_action,consume_request_id,consume_payload_hash,source_owner_kind,selected_variant_id,proposal_hash,successor_task_definition_revision_id,successor_execution_id,successor_review_id,successor_run_id,consumed_at,1 FROM recovery_purpose_confirmation_consumptions;
DROP TABLE recovery_purpose_confirmation_consumptions;
ALTER TABLE recovery_purpose_confirmation_consumptions_v90 RENAME TO recovery_purpose_confirmation_consumptions;
`
