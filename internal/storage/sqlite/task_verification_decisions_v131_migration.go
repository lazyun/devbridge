package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const taskVerificationDecisionsV131SQL = `
CREATE TABLE task_completion_intents (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 revision_id TEXT NOT NULL,
 plan_json TEXT NOT NULL CHECK(json_valid(plan_json)),
 plan_sha256 TEXT NOT NULL,
 acceptance_request_id TEXT NOT NULL,
 merge_request_id TEXT NOT NULL UNIQUE,
 completed_at TEXT,
 last_error TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER task_completion_intent_no_delete BEFORE DELETE ON task_completion_intents BEGIN SELECT RAISE(ABORT,'task completion intent is durable'); END;
CREATE TRIGGER task_completion_completed_immutable BEFORE UPDATE OF completed_at ON task_completion_intents WHEN OLD.completed_at IS NOT NULL AND NEW.completed_at IS NOT OLD.completed_at BEGIN SELECT RAISE(ABORT,'task completion receipt is immutable'); END;
CREATE TRIGGER task_completion_intent_immutable BEFORE UPDATE OF task_id,execution_id,revision_id,plan_json,plan_sha256,acceptance_request_id,merge_request_id ON task_completion_intents
BEGIN SELECT RAISE(ABORT,'task completion identity is immutable'); END;
CREATE TRIGGER task_completion_requires_merge BEFORE UPDATE OF status ON tasks
WHEN NEW.status='SUCCEEDED' AND EXISTS(SELECT 1 FROM task_completion_intents c WHERE c.task_id=NEW.id AND c.completed_at IS NULL)
BEGIN SELECT RAISE(ABORT,'task completion requires integration'); END;
CREATE TABLE task_holds (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id),
 execution_id TEXT NOT NULL REFERENCES execution_proposals(id),
 revision_id TEXT NOT NULL,
 note TEXT NOT NULL,
 held_at TEXT NOT NULL
);
CREATE TABLE task_hold_receipts (
 actor TEXT NOT NULL,
 request_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(id),
 execution_id TEXT NOT NULL,
 revision_id TEXT NOT NULL,
 payload_sha256 TEXT NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('DEFER_TASK','CONTINUE_TASK')),
 created_at TEXT NOT NULL,
 PRIMARY KEY(actor,request_id)
);
CREATE TRIGGER task_hold_receipt_immutable BEFORE UPDATE ON task_hold_receipts BEGIN SELECT RAISE(ABORT,'task choice receipt is immutable'); END;
CREATE TRIGGER task_hold_receipt_no_delete BEFORE DELETE ON task_hold_receipts BEGIN SELECT RAISE(ABORT,'task choice receipt is durable'); END;
CREATE TRIGGER held_task_no_execution BEFORE INSERT ON execution_proposals
WHEN EXISTS(SELECT 1 FROM task_holds h WHERE h.task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'TASK_DEFERRED'); END;
CREATE TRIGGER held_task_no_run BEFORE INSERT ON runs
WHEN EXISTS(SELECT 1 FROM task_holds h WHERE h.task_id=NEW.task_id)
BEGIN SELECT RAISE(ABORT,'TASK_DEFERRED'); END;
CREATE TRIGGER held_task_no_phase_advance BEFORE UPDATE OF phase,current_definition_revision_id,current_execution_id ON tasks
WHEN EXISTS(SELECT 1 FROM task_holds h WHERE h.task_id=NEW.id) AND
 (NEW.phase IS NOT OLD.phase OR NEW.current_definition_revision_id IS NOT OLD.current_definition_revision_id OR NEW.current_execution_id IS NOT OLD.current_execution_id)
BEGIN SELECT RAISE(ABORT,'TASK_DEFERRED'); END;
`

func migrateTaskVerificationDecisionsV131(ctx context.Context, tx *sql.Tx) error {
	var trigger string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='merge_operation_initial_exact'`).Scan(&trigger); err != nil {
		return err
	}
	if !strings.Contains(trigger, "t.status='SUCCEEDED'") {
		return errors.New("merge admission trigger has an unknown contract")
	}
	trigger = strings.ReplaceAll(trigger, "t.status='SUCCEEDED'", acceptedDeliveryTaskSQL)
	outer := "AND " + acceptedDeliveryTaskSQL + " AND q.terminal_outcome"
	if strings.Count(trigger, outer) != 1 {
		return errors.New("merge Task completion guard is not unique")
	}
	trigger = strings.Replace(trigger, outer, "AND "+acceptedDeliveryTaskSQL+" AND (t.status='SUCCEEDED' OR EXISTS(SELECT 1 FROM task_completion_intents ci WHERE ci.task_id=t.id AND ci.merge_request_id=NEW.merge_request_id AND ci.revision_id=t.current_definition_revision_id AND ci.execution_id=t.current_execution_id AND json_extract(p.plan_json,'$.completionTaskId')=t.id)) AND q.terminal_outcome", 1)
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER merge_operation_initial_exact`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, trigger); err != nil {
		return err
	}
	var enqueue string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='workflow_event_enqueue'`).Scan(&enqueue); err != nil {
		return err
	}
	enqueue = strings.Replace(enqueue, "'task.status_changed',", "'task.status_changed','task.verification_choice',", 1)
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER workflow_event_enqueue`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, enqueue)
	return err
}
