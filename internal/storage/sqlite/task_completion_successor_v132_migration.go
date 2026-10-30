package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Completion intent and acceptance stay immutable. Each explicitly confirmed
// successor is another immutable merge receipt linked to a settled release.
const taskCompletionSuccessorV132SQL = `
CREATE VIEW task_completion_merge_authority AS
WITH RECURSIVE chain(task_id,merge_request_id,depth) AS (
 SELECT task_id,merge_request_id,0 FROM task_completion_intents
 UNION ALL
 SELECT c.task_id,p.request_id,c.depth+1 FROM chain c
 JOIN feature_area_commit_receipts p ON json_extract(NULLIF(p.plan_json,''),'$.predecessorMergeRequestId')=c.merge_request_id
 WHERE json_extract(NULLIF(p.plan_json,''),'$.completionTaskId')=c.task_id
) SELECT * FROM chain;
CREATE UNIQUE INDEX completion_merge_one_successor ON feature_area_commit_receipts(json_extract(NULLIF(plan_json,''),'$.predecessorMergeRequestId'))
 WHERE json_extract(NULLIF(plan_json,''),'$.predecessorMergeRequestId') IS NOT NULL;
CREATE TRIGGER completion_merge_successor_exact BEFORE INSERT ON feature_area_commit_receipts
WHEN json_extract(NULLIF(NEW.plan_json,''),'$.predecessorMergeRequestId') IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM task_completion_merge_authority a
 JOIN task_completion_intents ci ON ci.task_id=a.task_id JOIN tasks t ON t.id=ci.task_id
 JOIN feature_area_commit_receipts old ON old.request_id=a.merge_request_id
 WHERE a.merge_request_id=json_extract(NULLIF(NEW.plan_json,''),'$.predecessorMergeRequestId')
 AND t.id=json_extract(NULLIF(NEW.plan_json,''),'$.completionTaskId') AND t.feature_area_id=NEW.feature_area_id
 AND t.project_id=json_extract(NULLIF(NEW.plan_json,''),'$.projectId') AND t.status='ACTIVE' AND t.phase='DELIVERING'
 AND ci.completed_at IS NULL AND ci.execution_id=t.current_execution_id AND ci.revision_id=t.current_definition_revision_id
 AND json_extract(ci.plan_json,'$.workspaceId')=json_extract(NULLIF(NEW.plan_json,''),'$.workspaceId')
 AND old.released_at IS NOT NULL AND old.completed_at IS NULL AND NEW.protocol_version=2
 AND NOT EXISTS(SELECT 1 FROM feature_area_commit_receipts next WHERE json_extract(NULLIF(next.plan_json,''),'$.predecessorMergeRequestId')=old.request_id))
BEGIN SELECT RAISE(ABORT,'Completion merge requires exact released predecessor'); END;
`

func migrateTaskCompletionSuccessorV132(ctx context.Context, tx *sql.Tx) error {
	var trigger string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='merge_operation_initial_exact'`).Scan(&trigger); err != nil {
		return err
	}
	old := "ci.merge_request_id=NEW.merge_request_id"
	if strings.Count(trigger, old) != 1 {
		return errors.New("unknown completion merge admission contract")
	}
	trigger = strings.Replace(trigger, old, "EXISTS(SELECT 1 FROM task_completion_merge_authority ca WHERE ca.task_id=ci.task_id AND ca.merge_request_id=NEW.merge_request_id)", 1)
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER merge_operation_initial_exact`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, trigger)
	return err
}
