package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateAppServerContinuityV79(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE app_server_run_bindings(run_id TEXT PRIMARY KEY REFERENCES runs(id),binding_json TEXT NOT NULL CHECK(json_valid(binding_json)),checkpoint_json TEXT CHECK(checkpoint_json IS NULL OR json_valid(checkpoint_json)),created_at TEXT NOT NULL);
 CREATE TABLE app_server_observations(run_id TEXT PRIMARY KEY REFERENCES runs(id), observation_json TEXT NOT NULL CHECK(json_valid(observation_json)),updated_at TEXT NOT NULL);
 CREATE TRIGGER app_server_binding_immutable BEFORE UPDATE OF run_id,binding_json ON app_server_run_bindings BEGIN SELECT RAISE(ABORT,'App Server binding immutable'); END;
 CREATE TRIGGER app_server_binding_no_delete BEFORE DELETE ON app_server_run_bindings BEGIN SELECT RAISE(ABORT,'App Server binding durable'); END;
 CREATE TABLE app_server_reattachments(run_id TEXT NOT NULL REFERENCES runs(id),previous_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),current_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),previous_connection_id TEXT NOT NULL,current_connection_id TEXT NOT NULL,thread_id TEXT NOT NULL,turn_id TEXT NOT NULL,binding_json TEXT NOT NULL CHECK(json_valid(binding_json)),created_at TEXT NOT NULL,PRIMARY KEY(run_id,current_connection_id));
 CREATE TRIGGER app_server_reattachment_immutable BEFORE UPDATE ON app_server_reattachments BEGIN SELECT RAISE(ABORT,'reattachment immutable'); END;
 CREATE TRIGGER app_server_reattachment_no_delete BEFORE DELETE ON app_server_reattachments BEGIN SELECT RAISE(ABORT,'reattachment durable'); END;`)
	if err != nil {
		return err
	}
	for _, spec := range []struct{ name, oldRun string }{{"runs_runtime_epoch_immutable", "OLD.id"}, {"claims_runtime_epoch_immutable", "OLD.run_id"}} {
		var ddl string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, spec.name).Scan(&ddl); err != nil {
			return err
		}
		marker := "\nBEGIN SELECT RAISE"
		at := strings.Index(ddl, marker)
		if at < 0 {
			return fmt.Errorf("unexpected epoch trigger %s", spec.name)
		}
		exception := fmt.Sprintf(` AND NOT EXISTS(SELECT 1 FROM app_server_reattachments c WHERE c.run_id=%s AND c.previous_epoch=OLD.runtime_epoch AND c.current_epoch=NEW.runtime_epoch AND c.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))`, spec.oldRun)
		ddl = ddl[:at] + exception + ddl[at:]
		if _, err = tx.ExecContext(ctx, "DROP TRIGGER "+spec.name); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DROP TRIGGER probe_work_binding_immutable;
 CREATE TRIGGER probe_work_binding_immutable BEFORE UPDATE OF task_id,run_id,runtime_epoch ON context_probes
 WHEN NEW.task_id IS NOT OLD.task_id OR (OLD.run_id IS NOT NULL AND NEW.run_id IS NOT OLD.run_id) OR (OLD.runtime_epoch IS NOT NULL AND NEW.runtime_epoch IS NOT OLD.runtime_epoch AND NOT EXISTS(SELECT 1 FROM app_server_reattachments a WHERE a.run_id=OLD.run_id AND a.previous_epoch=OLD.runtime_epoch AND a.current_epoch=NEW.runtime_epoch AND a.thread_id=OLD.thread_id AND a.turn_id=OLD.turn_id AND NEW.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)))
 BEGIN SELECT RAISE(ABORT,'Inspection identity is immutable'); END;
 DROP TRIGGER agent_runtime_identity_immutable;
 CREATE TRIGGER agent_runtime_identity_immutable BEFORE UPDATE OF run_id,runtime_epoch,connection_id,thread_id,turn_id ON agent_runtime_states
 WHEN (NEW.run_id<>OLD.run_id OR NEW.runtime_epoch<>OLD.runtime_epoch OR NEW.connection_id<>OLD.connection_id OR NEW.thread_id<>OLD.thread_id OR NEW.turn_id<>OLD.turn_id)
 AND NOT EXISTS(SELECT 1 FROM app_server_reattachments c WHERE c.run_id=OLD.run_id AND NEW.run_id=OLD.run_id AND c.previous_epoch=OLD.runtime_epoch AND c.current_epoch=NEW.runtime_epoch AND c.previous_connection_id=OLD.connection_id AND c.current_connection_id=NEW.connection_id AND c.thread_id=OLD.thread_id AND NEW.thread_id=OLD.thread_id AND c.turn_id=OLD.turn_id AND NEW.turn_id=OLD.turn_id AND c.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))
 BEGIN SELECT RAISE(ABORT,'agent runtime identity is immutable'); END;`)
	return err
}
