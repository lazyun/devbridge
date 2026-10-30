package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// v81 changes only the Run purpose CHECK and adds an exact-source child
// exception. Every existing row, index and unrelated trigger is preserved.
func migrateRecoveryStatusV81(ctx context.Context, tx *sql.Tx) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&ddl); err != nil {
		return err
	}
	old := "purpose IN ('SCOPE_REVIEW','IMPLEMENTATION','VERIFICATION','INSPECTION')"
	if !strings.Contains(ddl, old) {
		return fmt.Errorf("unexpected v80 Run purpose constraint")
	}
	ddl = strings.Replace(ddl, old, "purpose IN ('SCOPE_REVIEW','IMPLEMENTATION','VERIFICATION','INSPECTION','RECOVERY_STATUS')", 1)
	ddl = "CREATE TABLE runs_v81 " + ddl[strings.Index(ddl, "("):]
	type def struct{ kind, name, sql string }
	var defs []def
	rows, err := tx.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL AND (type='trigger' OR(type='index' AND tbl_name='runs')) ORDER BY rowid`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var d def
		if err = rows.Scan(&d.kind, &d.name, &d.sql); err != nil {
			rows.Close()
			return err
		}
		defs = append(defs, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range defs {
		if d.kind == "trigger" {
			if _, err = tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(d.name, `"`, `""`)+`"`); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs_v81 SELECT * FROM runs; DROP TABLE runs; ALTER TABLE runs_v81 RENAME TO runs;`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, recoveryStatusV81SQL); err != nil {
		return err
	}
	for _, d := range defs {
		if d.name == "runs_execution_purpose" || d.name == "runs_one_active_per_task" || d.name == "runs_review_id" || d.name == "runs_context_probe" {
			d.sql += " AND purpose<>'RECOVERY_STATUS'"
		}
		if d.name == "runs_runtime_epoch_immutable" {
			j := strings.Index(d.sql, "\nBEGIN ")
			if j < 0 {
				return fmt.Errorf("unexpected epoch trigger")
			}
			d.sql = d.sql[:j] + ` AND NOT EXISTS(SELECT 1 FROM recovery_status_adoptions x WHERE x.run_id=OLD.id AND x.previous_epoch=OLD.runtime_epoch AND x.current_epoch=NEW.runtime_epoch AND x.current_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1))` + d.sql[j:]
		}
		if d.name == "runs_operation_owner_insert" || d.name == "runs_agent_execution_insert" {
			i := strings.Index(d.sql, "\nWHEN ")
			j := strings.Index(d.sql, "\nBEGIN ")
			if i < 0 || j < 0 {
				return fmt.Errorf("unexpected Run owner trigger %s", d.name)
			}
			condition := d.sql[i+6 : j]
			exception := `NOT EXISTS(SELECT 1 FROM recovery_status_records x JOIN runs s ON s.id=x.source_run_id WHERE x.recovery_run_id=NEW.id AND NEW.purpose='RECOVERY_STATUS' AND NEW.project_id=s.project_id AND NEW.feature_area_id IS s.feature_area_id AND NEW.task_id=s.task_id AND NEW.operation_kind=s.operation_kind AND NEW.operation_id=s.operation_id AND NEW.agent_execution_id=s.agent_execution_id AND NEW.execution_proposal_id IS s.execution_proposal_id AND NEW.review_id IS s.review_id AND NEW.context_probe_id IS s.context_probe_id AND x.runtime_epoch=NEW.runtime_epoch AND x.state='PREPARED')`
			d.sql = d.sql[:i] + "\nWHEN (" + condition + ") AND " + exception + d.sql[j:]
		}
		if _, err = tx.ExecContext(ctx, d.sql); err != nil {
			return fmt.Errorf("restore %s: %w", d.name, err)
		}
	}
	return nil
}

const recoveryStatusV81SQL = `
CREATE TABLE recovery_status_adoptions(recovery_id TEXT NOT NULL,run_id TEXT NOT NULL REFERENCES runs(id),previous_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),current_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),created_at TEXT NOT NULL,PRIMARY KEY(recovery_id,current_epoch));
CREATE TRIGGER recovery_status_adoption_immutable BEFORE UPDATE ON recovery_status_adoptions BEGIN SELECT RAISE(ABORT,'Recovery adoption immutable'); END;
CREATE TRIGGER recovery_status_adoption_no_delete BEFORE DELETE ON recovery_status_adoptions BEGIN SELECT RAISE(ABORT,'Recovery adoption durable'); END;
CREATE TABLE recovery_status_records(
 id TEXT PRIMARY KEY,
 source_run_id TEXT NOT NULL REFERENCES runs(id),
 source_turn_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>0),
 request_id TEXT NOT NULL UNIQUE,
 recovery_run_id TEXT UNIQUE REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 runtime_epoch TEXT NOT NULL REFERENCES runtime_epochs(id),
 state TEXT NOT NULL CHECK(state IN ('PREPARED','RUN_CREATED','UNKNOWN','RUNNING','REPORT_READY','ASSESSED','RESOLVED','FAILED')),
 spec_json TEXT NOT NULL CHECK(json_valid(spec_json)),
 record_json TEXT NOT NULL CHECK(json_valid(record_json)),
 UNIQUE(source_run_id,source_turn_id,generation)
);
CREATE TABLE recovery_status_continuations(successor_execution_id TEXT PRIMARY KEY REFERENCES execution_proposals(id),recovery_id TEXT NOT NULL UNIQUE REFERENCES recovery_status_records(id),source_execution_id TEXT NOT NULL REFERENCES execution_proposals(id),revision_id TEXT NOT NULL REFERENCES task_definition_revisions(id),workset_hash TEXT NOT NULL,instruction TEXT NOT NULL,instruction_sha256 TEXT NOT NULL CHECK(length(instruction_sha256)=64),request_id TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TRIGGER recovery_status_continuation_immutable BEFORE UPDATE ON recovery_status_continuations BEGIN SELECT RAISE(ABORT,'Recovery continuation instruction immutable'); END;
CREATE TRIGGER recovery_status_continuation_no_delete BEFORE DELETE ON recovery_status_continuations BEGIN SELECT RAISE(ABORT,'Recovery continuation evidence durable'); END;
CREATE TRIGGER recovery_status_json_identity BEFORE UPDATE OF record_json ON recovery_status_records
WHEN json_extract(NEW.record_json,'$.spec') IS NOT json(NEW.spec_json) OR json_extract(NEW.record_json,'$.id') IS NOT NEW.id
 OR (json_extract(OLD.record_json,'$.recoveryTurnId') IS NOT NULL AND json_extract(OLD.record_json,'$.recoveryTurnId') IS NOT json_extract(NEW.record_json,'$.recoveryTurnId'))
BEGIN SELECT RAISE(ABORT,'Recovery record identity immutable'); END;
CREATE TRIGGER recovery_status_identity BEFORE UPDATE OF id,source_run_id,source_turn_id,generation,request_id,spec_json ON recovery_status_records BEGIN SELECT RAISE(ABORT,'Recovery source and instruction are immutable'); END;
CREATE TRIGGER recovery_status_no_delete BEFORE DELETE ON recovery_status_records BEGIN SELECT RAISE(ABORT,'Recovery evidence is durable'); END;
CREATE TRIGGER recovery_status_run_once BEFORE UPDATE OF recovery_run_id ON recovery_status_records WHEN OLD.recovery_run_id IS NOT NULL AND NEW.recovery_run_id IS NOT OLD.recovery_run_id BEGIN SELECT RAISE(ABORT,'Recovery Run is immutable'); END;
CREATE TRIGGER recovery_status_artifacts_immutable BEFORE UPDATE OF record_json ON recovery_status_records
WHEN (json_extract(OLD.record_json,'$._exactAfter') IS NOT NULL AND json_extract(OLD.record_json,'$._exactAfter') IS NOT json_extract(NEW.record_json,'$._exactAfter'))
 OR (json_extract(OLD.record_json,'$._exactReport') IS NOT NULL AND json_extract(OLD.record_json,'$._exactReport') IS NOT json_extract(NEW.record_json,'$._exactReport'))
 OR (json_extract(OLD.record_json,'$._exactAssessment') IS NOT NULL AND json_extract(OLD.record_json,'$._exactAssessment') IS NOT json_extract(NEW.record_json,'$._exactAssessment'))
 OR (json_extract(OLD.record_json,'$._exactFacts') IS NOT NULL AND json_extract(OLD.record_json,'$._exactFacts') IS NOT json_extract(NEW.record_json,'$._exactFacts'))
 OR (json_extract(OLD.record_json,'$.facts') IS NOT NULL AND json_extract(OLD.record_json,'$.facts') IS NOT json_extract(NEW.record_json,'$.facts'))
 OR (json_extract(OLD.record_json,'$.report') IS NOT NULL AND json_extract(OLD.record_json,'$.report') IS NOT json_extract(NEW.record_json,'$.report'))
 OR (json_extract(OLD.record_json,'$.assessment') IS NOT NULL AND json_extract(OLD.record_json,'$.assessment') IS NOT json_extract(NEW.record_json,'$.assessment'))
 OR (json_extract(OLD.record_json,'$.resolution') IS NOT NULL AND json_extract(OLD.record_json,'$.resolution') IS NOT json_extract(NEW.record_json,'$.resolution'))
BEGIN SELECT RAISE(ABORT,'Recovery artifacts are immutable'); END;
`
