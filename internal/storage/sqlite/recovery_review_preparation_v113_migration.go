package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateRecoveryReviewPreparationV113(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE review_purpose_preparation_adoptions (
 review_id TEXT NOT NULL REFERENCES reviews(id), run_id TEXT NOT NULL REFERENCES runs(id), plan_id TEXT NOT NULL REFERENCES runtime_scope_plans(plan_id),
 previous_epoch TEXT NOT NULL REFERENCES runtime_epochs(id), current_epoch TEXT NOT NULL REFERENCES runtime_epochs(id), created_at TEXT NOT NULL,
 PRIMARY KEY(run_id,current_epoch), CHECK(previous_epoch<>current_epoch));
 CREATE TRIGGER review_purpose_adoption_exact BEFORE INSERT ON review_purpose_preparation_adoptions
 WHEN NOT EXISTS(SELECT 1 FROM runs r JOIN reviews v ON v.id=r.review_id JOIN tasks t ON t.id=v.task_id
 JOIN runtime_preparation_attempts attempt ON attempt.run_id=r.id JOIN runtime_scope_plans p ON p.plan_id=attempt.plan_id
 JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id JOIN feature_area_review_start_requests receipt ON receipt.review_id=v.id AND receipt.run_id=r.id
 JOIN feature_area_agent_claims claim ON claim.run_id=r.id AND claim.operation_id=v.id AND claim.runtime_epoch=r.runtime_epoch
 WHERE v.id=NEW.review_id AND r.id=NEW.run_id AND p.plan_id=NEW.plan_id AND p.source_command='APPLY_CONFIRMED_REVIEW_PURPOSE' AND a.disposition='CURRENT'
 AND t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING' AND v.linked_run_id=r.id
 AND receipt.outcome='ACCEPTED' AND receipt.request_id=p.preparation_request_id AND receipt.payload_sha256=p.preparation_payload_hash
 AND r.status='CREATED' AND r.runtime_epoch=NEW.previous_epoch AND NEW.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND r.thread_id IS NULL AND r.turn_id IS NULL AND r.runtime_workset_json IS NULL
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM app_server_run_bindings b WHERE b.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM runtime_workspace_preparation_claims c WHERE c.run_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(v.id,t.id))
 AND ((v.executor_kind='CODING_AGENT' AND EXISTS(SELECT 1 FROM runtime_handoffs h WHERE h.run_id=r.id AND h.state='REGISTERED' AND h.runtime_epoch=r.runtime_epoch))
 OR(v.executor_kind='UPPER_LLM' AND EXISTS(SELECT 1 FROM review_grants g WHERE g.review_id=v.id AND g.run_id=r.id AND g.state='PREPARING' AND g.runtime_epoch=r.runtime_epoch AND g.preparation_error IS NULL))))
 BEGIN SELECT RAISE(ABORT,'Review adoption requires exact undispatched purpose preparation'); END;
 CREATE TRIGGER review_purpose_adoption_immutable BEFORE UPDATE ON review_purpose_preparation_adoptions BEGIN SELECT RAISE(ABORT,'Review adoption immutable'); END;
 CREATE TRIGGER review_purpose_adoption_no_delete BEFORE DELETE ON review_purpose_preparation_adoptions BEGIN SELECT RAISE(ABORT,'Review adoption durable'); END;`)
	if err != nil {
		return err
	}
	for _, spec := range []struct{ name, run string }{{"runs_runtime_epoch_immutable", "OLD.id"}, {"claims_runtime_epoch_immutable", "OLD.run_id"}} {
		var ddl string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, spec.name).Scan(&ddl); err != nil {
			return err
		}
		at := strings.Index(ddl, "\nBEGIN SELECT RAISE")
		if at < 0 {
			return fmt.Errorf("unexpected epoch trigger %s", spec.name)
		}
		ddl = ddl[:at] + fmt.Sprintf(` AND NOT EXISTS(SELECT 1 FROM review_purpose_preparation_adoptions a WHERE a.run_id=%s AND a.previous_epoch=OLD.runtime_epoch AND a.current_epoch=NEW.runtime_epoch AND a.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))`, spec.run) + ddl[at:]
		if _, err = tx.ExecContext(ctx, "DROP TRIGGER "+spec.name); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	var grantDDL string
	if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE name='upper_review_grant_binding'`).Scan(&grantDDL); err != nil {
		return err
	}
	marker := "AND a.grant_generation=NEW.generation AND NEW.generation=OLD.generation+1 AND OLD.state='RECOVERY_REQUIRED'))"
	replacement := "AND a.grant_generation=NEW.generation AND NEW.generation=OLD.generation+1 AND OLD.state='RECOVERY_REQUIRED') AND NOT EXISTS(SELECT 1 FROM review_purpose_preparation_adoptions adopted WHERE adopted.review_id=OLD.review_id AND adopted.run_id=OLD.run_id AND adopted.previous_epoch=OLD.runtime_epoch AND adopted.current_epoch=NEW.runtime_epoch AND adopted.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1) AND NEW.generation=OLD.generation AND OLD.state='PREPARING' AND NEW.state=OLD.state))"
	if strings.Count(grantDDL, marker) != 1 {
		return fmt.Errorf("unexpected Upper Review grant epoch predicate")
	}
	grantDDL = strings.Replace(grantDDL, marker, replacement, 1)
	if _, err = tx.ExecContext(ctx, "DROP TRIGGER upper_review_grant_binding"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, grantDDL); err != nil {
		return err
	}
	return nil
}
