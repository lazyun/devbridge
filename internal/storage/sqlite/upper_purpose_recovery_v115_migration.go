package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateUpperPurposeRecoveryV115(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE runtime_preparation_failures (
 attempt_id TEXT PRIMARY KEY REFERENCES runtime_preparation_attempts(attempt_id), plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 preparation_request_id TEXT NOT NULL UNIQUE REFERENCES runtime_workspace_preparation_claims(request_id), preparation_payload_hash TEXT NOT NULL CHECK(length(preparation_payload_hash)=64),
 owner_kind TEXT NOT NULL CHECK(owner_kind='REVIEW'), owner_id TEXT NOT NULL REFERENCES reviews(id), task_id TEXT NOT NULL REFERENCES tasks(id), runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 failed_stage TEXT NOT NULL, failure_code TEXT NOT NULL, message TEXT NOT NULL,
 external_result TEXT NOT NULL CHECK(external_result='DEFINITE_FAILURE'), quiescence_proof TEXT NOT NULL CHECK(quiescence_proof='SYNCHRONOUS_PREPARER_RETURNED'),
 artifact_inventory_json TEXT NOT NULL CHECK(json_valid(artifact_inventory_json)), artifact_inventory_hash TEXT NOT NULL CHECK(length(artifact_inventory_hash)=64), recorded_at TEXT NOT NULL);
 CREATE TABLE runtime_preparation_revocations (
 plan_id TEXT PRIMARY KEY REFERENCES runtime_scope_plans(plan_id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id), attempt_id TEXT NOT NULL UNIQUE REFERENCES runtime_preparation_failures(attempt_id),
 project_id TEXT NOT NULL REFERENCES projects(id), request_id TEXT NOT NULL, recorded_at TEXT NOT NULL,
 FOREIGN KEY(project_id,request_id) REFERENCES execution_cancellations(project_id,request_id));
 CREATE TABLE runtime_preparation_abandonments (
 plan_id TEXT PRIMARY KEY REFERENCES runtime_preparation_revocations(plan_id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id), attempt_id TEXT NOT NULL UNIQUE REFERENCES runtime_preparation_failures(attempt_id),
 project_id TEXT NOT NULL REFERENCES projects(id), request_id TEXT NOT NULL, payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64), owner_id TEXT NOT NULL REFERENCES reviews(id), task_id TEXT NOT NULL REFERENCES tasks(id),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)), recorded_at TEXT NOT NULL, UNIQUE(project_id,request_id),
 FOREIGN KEY(project_id,request_id) REFERENCES execution_cancellations(project_id,request_id));
 CREATE TRIGGER runtime_workspace_preparation_claims_upper_guard BEFORE INSERT ON runtime_workspace_preparation_claims
 WHEN EXISTS(SELECT 1 FROM runtime_scope_plans p JOIN reviews v ON v.id=p.owner_id WHERE p.plan_id=NEW.plan_id AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE' AND v.executor_kind='UPPER_LLM')
 AND NOT EXISTS(SELECT 1 FROM runs r JOIN reviews v ON v.id=r.review_id JOIN tasks t ON t.id=v.task_id JOIN runtime_preparation_attempts attempt ON attempt.run_id=r.id
 JOIN feature_area_agent_claims claim ON claim.run_id=r.id AND claim.operation_id=v.id AND claim.runtime_epoch=r.runtime_epoch
 WHERE r.id=NEW.run_id AND attempt.plan_id=NEW.plan_id AND v.linked_run_id=r.id AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
 AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND r.status IN('CREATED','PREPARING') AND r.thread_id IS NULL AND r.turn_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(v.id,t.id))
 AND NOT EXISTS(SELECT 1 FROM runtime_preparation_revocations revoked WHERE revoked.plan_id=NEW.plan_id))
 BEGIN SELECT RAISE(ABORT,'workspace preparation requires exact active owner'); END;
 CREATE TRIGGER runtime_preparation_failure_exact BEFORE INSERT ON runtime_preparation_failures
 WHEN NOT EXISTS(SELECT 1 FROM runtime_preparation_attempts a JOIN runtime_scope_plans p ON p.plan_id=a.plan_id
 JOIN runtime_workspace_preparation_claims c ON c.plan_id=p.plan_id AND c.run_id=a.run_id
 JOIN runs r ON r.id=a.run_id JOIN reviews v ON v.id=r.review_id JOIN tasks t ON t.id=v.task_id JOIN review_grants g ON g.review_id=v.id
 JOIN feature_area_agent_claims claim ON claim.run_id=r.id AND claim.operation_id=v.id AND claim.operation_kind='REVIEW' AND claim.runtime_epoch=r.runtime_epoch
 WHERE a.attempt_id=NEW.attempt_id AND p.plan_id=NEW.plan_id AND r.id=NEW.run_id AND p.owner_kind='REVIEW' AND p.owner_id=NEW.owner_id
 AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE' AND v.executor_kind='UPPER_LLM' AND v.id=NEW.owner_id AND t.id=NEW.task_id
 AND v.linked_run_id=r.id AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
 AND c.request_id=NEW.preparation_request_id AND c.payload_hash=NEW.preparation_payload_hash
 AND g.run_id=r.id AND g.runtime_epoch=r.runtime_epoch AND g.state='PREPARING' AND r.runtime_epoch=NEW.runtime_epoch AND r.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND r.status='PREPARING' AND r.thread_id IS NULL AND r.turn_id IS NULL AND r.runtime_workset_json IS NULL
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=r.id)

 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_start_authorities b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations k WHERE k.owner_id IN(v.id,t.id)))
 BEGIN SELECT RAISE(ABORT,'preparation failure requires exact quiescent Upper attempt'); END;
 CREATE TRIGGER runtime_preparation_revocation_exact BEFORE INSERT ON runtime_preparation_revocations
 WHEN NOT EXISTS(SELECT 1 FROM runtime_preparation_failures f JOIN execution_cancellations c ON c.project_id=NEW.project_id AND c.request_id=NEW.request_id
 WHERE f.plan_id=NEW.plan_id AND f.run_id=NEW.run_id AND f.attempt_id=NEW.attempt_id AND c.run_id=f.run_id AND c.owner_type='TASK' AND c.owner_id=f.task_id AND c.state='CANCEL_REQUESTED')
 BEGIN SELECT RAISE(ABORT,'preparation revocation requires exact abandonment intent'); END;
 CREATE TRIGGER runtime_preparation_abandonment_exact BEFORE INSERT ON runtime_preparation_abandonments
 WHEN NOT EXISTS(SELECT 1 FROM runtime_preparation_revocations a JOIN runtime_preparation_failures f ON f.attempt_id=a.attempt_id
 JOIN runs r ON r.id=f.run_id JOIN tasks t ON t.id=f.task_id JOIN review_grants g ON g.review_id=f.owner_id
 JOIN execution_cancellations c ON c.project_id=a.project_id AND c.request_id=a.request_id
 WHERE a.plan_id=NEW.plan_id AND a.run_id=NEW.run_id AND a.attempt_id=NEW.attempt_id AND a.project_id=NEW.project_id AND a.request_id=NEW.request_id
 AND f.owner_id=NEW.owner_id AND f.task_id=NEW.task_id AND r.status='INFRASTRUCTURE_FAILED' AND r.failure_code='RUNTIME_PREPARATION_ABANDONED'
 AND t.status='CANCELED' AND t.current_review_id=f.owner_id AND g.state='CANCELED' AND c.state='CANCELED'
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims claim WHERE claim.run_id=r.id))
 BEGIN SELECT RAISE(ABORT,'preparation abandonment requires atomic terminal outcomes'); END;`)
	if err != nil {
		return err
	}
	for _, table := range []string{"runtime_preparation_failures", "runtime_preparation_revocations", "runtime_preparation_abandonments"} {
		for _, event := range []string{"UPDATE", "DELETE"} {
			if _, err = tx.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s_immutable_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'preparation failure history immutable'); END", table, event, event, table)); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities"} {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER %s_failed_guard BEFORE INSERT ON %s
 WHEN EXISTS(SELECT 1 FROM runtime_preparation_failures f WHERE f.plan_id=NEW.plan_id OR f.run_id=NEW.run_id)
 BEGIN SELECT RAISE(ABORT,'failed preparation cannot gain runtime authority'); END`, table, table)); err != nil {
			return err
		}
	}
	var ddl string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='upper_review_grant_transition'`).Scan(&ddl); err != nil {
		return err
	}
	marker := "\nBEGIN SELECT RAISE"
	if strings.Count(ddl, marker) != 1 {
		return fmt.Errorf("unexpected Upper grant transition predicate")
	}
	exception := ` AND NOT (OLD.state='RECOVERY_REQUIRED' AND NEW.state='CANCELED' AND NEW.runtime_epoch=OLD.runtime_epoch AND NEW.generation=OLD.generation
 AND EXISTS(SELECT 1 FROM runtime_preparation_revocations revoked JOIN runtime_preparation_failures f ON f.attempt_id=revoked.attempt_id
 JOIN execution_cancellations c ON c.project_id=revoked.project_id AND c.request_id=revoked.request_id JOIN tasks t ON t.id=f.task_id
 WHERE f.owner_id=OLD.review_id AND f.run_id=OLD.run_id AND f.runtime_epoch=OLD.runtime_epoch AND revoked.run_id=f.run_id
 AND c.owner_type='TASK' AND c.owner_id=f.task_id AND c.run_id=f.run_id AND c.state='CANCEL_REQUESTED' AND c.runtime_epoch=OLD.runtime_epoch
 AND t.current_review_id=f.owner_id AND t.status='ACTIVE' AND t.phase='REVIEWING'
 AND json_extract(c.request_payload_json,'$.expectedUpperVersion.grantEpoch')=OLD.runtime_epoch
 AND json_extract(c.request_payload_json,'$.expectedUpperVersion.grantGeneration')=OLD.generation
 AND json_extract(c.request_payload_json,'$.expectedUpperVersion.authorityEpoch')=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)))`
	ddl = strings.Replace(ddl, marker, exception+marker, 1)
	if _, err = tx.ExecContext(ctx, `DROP TRIGGER upper_review_grant_transition`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	return nil
}
