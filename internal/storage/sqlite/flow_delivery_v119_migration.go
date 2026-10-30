package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// An ancestor is eligible only through exact Core reverification receipts and
// the same frozen revision/runtime layout. UNION also terminates corrupt cycles.
const reverificationLineageV119SQL = `
CREATE VIEW execution_implementation_lineage AS
WITH RECURSIVE lineage(execution_id,source_id) AS (
 SELECT id,id FROM execution_proposals
 UNION
 SELECT l.execution_id,r.subject_id FROM lineage l
 JOIN execution_proposals child ON child.id=l.source_id
 JOIN decision_resolution_receipts r ON r.result_id=child.id AND r.project_id=child.project_id
 JOIN execution_proposals source ON source.id=r.subject_id
 WHERE r.decision_kind='TASK_ACCEPTANCE' AND r.subject_kind='EXECUTION_PROPOSAL'
 AND r.option='REQUEST_REVERIFICATION' AND r.result_kind='EXECUTION_PROPOSAL'
 AND source.project_id=child.project_id AND source.task_id=child.task_id
 AND source.task_definition_revision_id=child.task_definition_revision_id
 AND source.primary_member_id IS child.primary_member_id
 AND source.approved_workset_hash IS child.approved_workset_hash
 AND source.runtime_adapter_identity IS child.runtime_adapter_identity
 AND source.runtime_protocol_identity IS child.runtime_protocol_identity
) SELECT execution_id,source_id FROM lineage;
`

func migrateFlowDeliveryV119(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, reverificationLineageV119SQL); err != nil {
		return err
	}
	var trigger string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='task_commit_member_owner'`).Scan(&trigger); err != nil {
		return err
	}
	if strings.Count(trigger, "i.execution_proposal_id=v.execution_proposal_id") != 1 {
		return errors.New("unexpected Task delivery ownership trigger")
	}
	trigger = strings.Replace(trigger, "i.execution_proposal_id=v.execution_proposal_id", "EXISTS(SELECT 1 FROM execution_implementation_lineage l WHERE l.execution_id=v.execution_proposal_id AND l.source_id=i.execution_proposal_id)", 1)
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER task_commit_member_owner`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, trigger); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, mergeDeliveryV119SQL)
	return err
}
