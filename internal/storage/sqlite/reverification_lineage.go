package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/executionproposal"
)

// implementationEvidence follows only durable same-definition reverification
// receipts. It never reparents a Run or assigns an ancestor Run to a successor.
func implementationEvidence(ctx context.Context, q definitionQuerier, execution executionproposal.Proposal) (executionproposal.Proposal, error) {
	var id string
	var count int
	err := q.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(e.id),'') FROM execution_implementation_lineage l JOIN execution_proposals e ON e.id=l.source_id JOIN runs r ON r.id=e.run_id AND r.execution_proposal_id=e.id AND r.task_id=e.task_id WHERE l.execution_id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=? AND r.purpose='IMPLEMENTATION' AND r.status='SUCCEEDED'`, execution.ID, execution.ProjectID, execution.TaskID, execution.TaskDefinitionRevisionID).Scan(&count, &id)
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	if count != 1 {
		return executionproposal.Proposal{}, executionproposal.ErrConflict
	}
	return scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, id))
}
