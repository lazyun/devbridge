package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateRecoveryChildPreparationV117(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE recovery_child_preparation_failures (
 recovery_id TEXT PRIMARY KEY REFERENCES recovery_status_records(id), recovery_generation INTEGER NOT NULL CHECK(recovery_generation>0),
 plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 attempt_id TEXT NOT NULL UNIQUE REFERENCES runtime_preparation_attempts(attempt_id),
 preparation_request_id TEXT NOT NULL UNIQUE REFERENCES runtime_workspace_preparation_claims(request_id), preparation_payload_hash TEXT NOT NULL CHECK(length(preparation_payload_hash)=64),
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id), failed_stage TEXT NOT NULL CHECK(failed_stage='MEMBER_OBSERVATION'),
 failure_code TEXT NOT NULL CHECK(failure_code='RECOVERY_PREPARATION_FAILED'), message TEXT NOT NULL,
 external_result TEXT NOT NULL CHECK(external_result='DEFINITE_FAILURE'), quiescence_proof TEXT NOT NULL CHECK(quiescence_proof='SYNCHRONOUS_PREPARER_RETURNED'),
 artifact_inventory_json TEXT NOT NULL CHECK(json_valid(artifact_inventory_json)), artifact_inventory_hash TEXT NOT NULL CHECK(length(artifact_inventory_hash)=64), source_admission_hash TEXT NOT NULL CHECK(length(source_admission_hash)=64), recorded_at TEXT NOT NULL);
 CREATE TABLE recovery_child_preparation_abandonments (
 recovery_id TEXT PRIMARY KEY REFERENCES recovery_child_preparation_failures(recovery_id),
 plan_id TEXT NOT NULL UNIQUE REFERENCES runtime_scope_plans(plan_id), run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
 project_id TEXT NOT NULL REFERENCES projects(id), request_id TEXT NOT NULL, payload_hash TEXT NOT NULL CHECK(length(payload_hash)=64),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)), recorded_at TEXT NOT NULL, UNIQUE(project_id,request_id),
 FOREIGN KEY(project_id,request_id) REFERENCES execution_cancellations(project_id,request_id));
 CREATE TRIGGER recovery_child_preparation_failure_exact BEFORE INSERT ON recovery_child_preparation_failures
 WHEN NOT EXISTS(SELECT 1 FROM recovery_status_records x JOIN recovery_child_admissions a ON a.recovery_id=x.id
 JOIN runtime_scope_plans p ON p.plan_id=a.child_plan_id JOIN runtime_preparation_attempts attempt ON attempt.plan_id=p.plan_id
 JOIN runtime_workspace_preparation_claims claim ON claim.plan_id=p.plan_id AND claim.run_id=attempt.run_id
 JOIN runs child ON child.id=x.recovery_run_id JOIN runs source ON source.id=x.source_run_id
 JOIN recovery_current_generations generation ON generation.recovery_id=x.id AND generation.generation=x.generation
 WHERE x.id=NEW.recovery_id AND x.generation=NEW.recovery_generation AND a.recovery_generation=x.generation
 AND p.plan_id=NEW.plan_id AND p.owner_kind='RECOVERY_STATUS' AND p.owner_id=x.id AND p.generation=x.generation
 AND p.source_command='RECOVERY_STATUS_CHILD' AND attempt.attempt_id=NEW.attempt_id AND attempt.run_id=child.id AND child.id=NEW.run_id
 AND claim.request_id=NEW.preparation_request_id AND claim.payload_hash=NEW.preparation_payload_hash
 AND x.state='RUN_CREATED' AND child.purpose='RECOVERY_STATUS' AND child.status IN('CREATED','PREPARING')
 AND child.thread_id IS NULL AND child.turn_id IS NULL AND child.runtime_workset_json IS NULL
 AND child.project_id=source.project_id AND child.task_id=source.task_id AND child.operation_kind=source.operation_kind AND child.operation_id=source.operation_id
 AND x.runtime_epoch=NEW.runtime_epoch AND child.runtime_epoch=NEW.runtime_epoch AND NEW.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND NOT EXISTS(SELECT 1 FROM recovery_child_thread_attempts t WHERE t.recovery_id=x.id)
 AND NOT EXISTS(SELECT 1 FROM recovery_child_turn_attempts t WHERE t.recovery_id=x.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_workset_bindings b WHERE b.run_id=child.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_start_authorities b WHERE b.run_id=child.id)
 AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=child.id)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=child.id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.task_id=source.task_id AND c.state IN('CANCEL_REQUESTED','RECOVERY_REQUIRED')))
 BEGIN SELECT RAISE(ABORT,'Recovery failure requires exact undispatched preparation'); END;
 CREATE TRIGGER recovery_child_preparation_abandonment_exact BEFORE INSERT ON recovery_child_preparation_abandonments
 WHEN NOT EXISTS(SELECT 1 FROM recovery_child_preparation_failures f JOIN recovery_status_records x ON x.id=f.recovery_id
 JOIN runs child ON child.id=f.run_id JOIN runs source ON source.id=x.source_run_id
 JOIN execution_cancellations c ON c.project_id=NEW.project_id AND c.request_id=NEW.request_id
 WHERE f.recovery_id=NEW.recovery_id AND f.plan_id=NEW.plan_id AND f.run_id=NEW.run_id AND x.generation=f.recovery_generation
 AND x.state='FAILED' AND json_extract(x.record_json,'$.failureReason')='RECOVERY_CANCELED'
 AND child.status='CANCELED' AND source.status='CANCELED' AND child.thread_id IS NULL AND child.turn_id IS NULL
 AND c.run_id=child.id AND c.owner_type=source.operation_kind AND c.owner_id=source.operation_id AND c.task_id=source.task_id
 AND c.project_id=source.project_id AND c.state='CANCELED' AND c.backend_disposition='NO_BACKEND' AND c.interrupt_state='NOT_SENT'
 AND c.thread_id IS NULL AND c.turn_id IS NULL AND c.result_json=NEW.result_json
 AND NOT EXISTS(SELECT 1 FROM feature_area_agent_claims claim WHERE claim.run_id=source.id)
 AND NOT EXISTS(SELECT 1 FROM project_inspection_agent_claims claim WHERE claim.run_id=source.id))
 BEGIN SELECT RAISE(ABORT,'Recovery abandonment requires atomic exact cancellation'); END;`)
	if err != nil {
		return err
	}
	for _, table := range []string{"recovery_child_preparation_failures", "recovery_child_preparation_abandonments"} {
		for _, event := range []string{"UPDATE", "DELETE"} {
			if _, err = tx.ExecContext(ctx, fmt.Sprintf("CREATE TRIGGER %s_immutable_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'Recovery preparation history immutable'); END", table, event, event, table)); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities"} {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER %s_recovery_failed_guard BEFORE INSERT ON %s
 WHEN EXISTS(SELECT 1 FROM recovery_child_preparation_failures f WHERE f.plan_id=NEW.plan_id OR f.run_id=NEW.run_id)
 BEGIN SELECT RAISE(ABORT,'settled Recovery preparation cannot gain authority'); END`, table, table)); err != nil {
			return err
		}
	}
	for _, table := range []string{"recovery_child_thread_attempts", "recovery_child_turn_attempts"} {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER %s_failed_guard BEFORE INSERT ON %s
 WHEN EXISTS(SELECT 1 FROM recovery_child_preparation_failures f WHERE f.recovery_id=NEW.recovery_id)
 BEGIN SELECT RAISE(ABORT,'settled Recovery preparation cannot dispatch'); END`, table, table)); err != nil {
			return err
		}
	}
	return nil
}

func ensureRecoveryChildPreparationGuards(ctx context.Context, db *sql.DB) error {
	guards := map[string][]string{
		"recovery_child_preparation_failures":          {"recovery_generation", "SYNCHRONOUS_PREPARER_RETURNED", "artifact_inventory_hash", "source_admission_hash"},
		"recovery_child_preparation_abandonments":      {"REFERENCES recovery_child_preparation_failures", "FOREIGN KEY(project_id,request_id)"},
		"recovery_child_preparation_failure_exact":     {"RAISE(ABORT", "x.generation=NEW.recovery_generation", "recovery_child_thread_attempts", "runtime_authority", "Recovery failure requires exact undispatched preparation"},
		"recovery_child_preparation_abandonment_exact": {"RAISE(ABORT", "source.status='CANCELED'", "c.backend_disposition='NO_BACKEND'", "Recovery abandonment requires atomic exact cancellation"},
	}
	for _, table := range []string{"runtime_workset_bindings", "runtime_start_authorities", "runtime_workspace_observations"} {
		guards[table+"_recovery_failed_guard"] = []string{"recovery_child_preparation_failures", "f.plan_id=NEW.plan_id OR f.run_id=NEW.run_id", "RAISE(ABORT"}
	}
	for _, table := range []string{"recovery_child_thread_attempts", "recovery_child_turn_attempts"} {
		guards[table+"_failed_guard"] = []string{"recovery_child_preparation_failures", "f.recovery_id=NEW.recovery_id", "RAISE(ABORT"}
	}
	for _, table := range []string{"recovery_child_preparation_failures", "recovery_child_preparation_abandonments"} {
		for _, event := range []string{"UPDATE", "DELETE"} {
			guards[table+"_immutable_"+event] = []string{"BEFORE " + event + " ON " + table, "RAISE(ABORT", "Recovery preparation history immutable"}
		}
	}
	for name, fragments := range guards {
		var ddl string
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&ddl); err != nil {
			return err
		}
		for _, fragment := range fragments {
			if !strings.Contains(ddl, fragment) {
				return fmt.Errorf("database lacks v117 Recovery preparation guard %s", name)
			}
		}
	}
	return nil
}
