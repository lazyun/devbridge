package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
)

// AuthorizeTaskExecution materializes the current candidate after Task approval.
func (s *WorkflowProgressionStore) AuthorizeTaskExecution(ctx context.Context, sessionID, taskID string) (executionproposal.Proposal, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=(SELECT project_id FROM ai_sessions WHERE id=?)`, taskID, sessionID))
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	if t.Status != task.StatusActive {
		return executionproposal.Proposal{}, executionproposal.ErrConflict
	}
	if t.CurrentExecutionID != "" {
		return scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND task_id=? AND task_definition_revision_id=?`, t.CurrentExecutionID, t.ID, t.CurrentDefinitionRevisionID))
	}
	if t.Phase != task.PhaseImplementationReady || t.CurrentDefinitionRevisionID == "" {
		return executionproposal.Proposal{}, executionproposal.ErrConflict
	}
	if err = createApprovedExecutionTx(ctx, tx, t, sessionID, s.now()); err != nil {
		return executionproposal.Proposal{}, err
	}
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, "execution_approved_"+t.ID))
	if err != nil {
		return value, err
	}
	return value, tx.Commit()
}
