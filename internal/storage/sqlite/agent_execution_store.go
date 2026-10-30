package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

type AgentExecutionStore struct{ db *DB }

func NewAgentExecutionStore(db *DB) *AgentExecutionStore { return &AgentExecutionStore{db: db} }

var _ agentexecution.Store = (*AgentExecutionStore)(nil)

const selectAgentExecution = `SELECT id,COALESCE(project_id,''),COALESCE(task_id,''),COALESCE(feature_area_id,''),kind,COALESCE(semantic_owner_kind,''),COALESCE(semantic_owner_id,''),COALESCE(execution_proposal_id,''),provenance_kind,created_at FROM agent_executions`

func scanAgentExecution(s runScanner) (agentexecution.Execution, error) {
	var v agentexecution.Execution
	var created string
	err := s.Scan(&v.ID, &v.ProjectID, &v.TaskID, &v.FeatureAreaID, &v.Kind, &v.SemanticOwnerKind, &v.SemanticOwnerID, &v.ExecutionProposalID, &v.ProvenanceKind, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return v, agentexecution.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.CreatedAt, err = parseTime(created)
	return v, err
}
func (s *AgentExecutionStore) Get(ctx context.Context, id string) (agentexecution.Execution, error) {
	return scanAgentExecution(s.db.sql.QueryRowContext(ctx, selectAgentExecution+` WHERE id=?`, id))
}
func (s *AgentExecutionStore) ListByProject(ctx context.Context, id string) ([]agentexecution.Execution, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectAgentExecution+` WHERE project_id=? ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]agentexecution.Execution, 0)
	for rows.Next() {
		v, err := scanAgentExecution(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func insertRunAgentExecutionTx(ctx context.Context, tx *sql.Tx, v *run.Run) error {
	var kind agentexecution.Kind
	switch v.OperationKind {
	case run.OperationExecution:
		v.AgentExecutionID = v.ExecutionProposalID
		var exact bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_executions WHERE id=? AND task_id=? AND project_id=? AND feature_area_id=? AND kind='DEVELOPMENT' AND semantic_owner_kind='EXECUTION' AND semantic_owner_id=? AND execution_proposal_id=? AND provenance_kind<>'LEGACY_UNKNOWN')`, v.AgentExecutionID, v.TaskID, v.ProjectID, v.FeatureAreaID, v.OperationID, v.ExecutionProposalID).Scan(&exact)
		if err != nil {
			return err
		}
		if !exact {
			return agentexecution.ErrInvalidAssociation
		}
		return nil
	case run.OperationReview:
		kind = agentexecution.KindReview
		v.AgentExecutionID = "review:" + v.OperationID
	case run.OperationInspection:
		kind = agentexecution.KindInspection
		v.AgentExecutionID = "inspection:" + v.OperationID
	default:
		return agentexecution.ErrInvalidAssociation
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_executions(id,project_id,task_id,feature_area_id,kind,semantic_owner_kind,semantic_owner_id,provenance_kind,created_at) VALUES(?,?,?,?,?,?,?,'NATIVE',?)`, v.AgentExecutionID, v.ProjectID, v.TaskID, nullString(v.FeatureAreaID), kind, v.OperationKind, v.OperationID, formatTime(v.CreatedAt))
	return err
}

// ValidateRunExecution is a mandatory pre-handoff proof. It reads durable exact
// identity and current authority together under the existing writer/epoch lock.
func (s *RunStore) ValidateRunExecution(ctx context.Context, v run.Run) error {
	return s.validateRunExecution(ctx, v, false)
}
func (s *RunStore) ValidateRunExecutionDispatch(ctx context.Context, v run.Run) error {
	return s.validateRunExecution(ctx, v, true)
}
func (s *RunStore) validateRunExecution(ctx context.Context, v run.Run, requireBoundary bool) error {
	if v.AgentExecutionID == "" {
		return agentexecution.ErrInvalidAssociation
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.db.checkRunTx(ctx, tx, v.ID); err != nil {
		return err
	}
	live, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, v.ID))
	if err != nil {
		return err
	}
	if !live.SameOwner(v) {
		return agentexecution.ErrInvalidAssociation
	}
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN agent_executions e ON e.id=r.agent_execution_id JOIN tasks t ON t.id=e.task_id
 WHERE r.id=? AND r.runtime_epoch=? AND r.status IN ('CREATED','PREPARING','RUNNING','WAITING_APPROVAL') AND t.legacy_read_only=0 AND t.status<>'CANCELED'
 AND e.provenance_kind<>'LEGACY_UNKNOWN' AND e.project_id=r.project_id AND t.project_id=r.project_id AND e.task_id=r.task_id AND e.feature_area_id IS r.feature_area_id AND t.feature_area_id IS r.feature_area_id AND e.semantic_owner_kind=r.operation_kind AND e.semantic_owner_id=r.operation_id
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.run_id=r.id AND c.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED','CANCELED'))
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents f WHERE f.run_id=r.id)
 AND ((e.kind='INSPECTION' AND r.purpose='INSPECTION' AND r.feature_area_id IS NULL AND t.task_type='INSPECTION' AND t.task_scope='PROJECT' AND t.work_owner_id=r.context_probe_id AND EXISTS(SELECT 1 FROM context_probes p WHERE p.id=r.context_probe_id AND p.id=e.semantic_owner_id AND p.task_id=t.id AND p.project_id=r.project_id AND p.run_id=r.id AND p.runtime_epoch=r.runtime_epoch AND p.status='RUNNING')) OR
 (EXISTS(SELECT 1 FROM feature_area_agent_claims c WHERE c.run_id=r.id AND c.feature_area_id=r.feature_area_id AND c.operation_kind=r.operation_kind AND c.operation_id=r.operation_id AND c.runtime_epoch=r.runtime_epoch)
 AND ((?=0 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id)) OR EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN agent_conversations a ON a.id=b.agent_conversation_id WHERE b.run_id=r.id AND a.feature_area_id=r.feature_area_id AND a.project_id=r.project_id AND a.ended_at IS NULL AND (?=0 OR COALESCE(a.backend_thread_id,'')=?) AND (r.thread_id IS NULL OR a.backend_thread_id=r.thread_id)))
 AND ((e.kind='REVIEW' AND t.task_type='DEVELOPMENT' AND t.current_review_id=r.review_id AND r.purpose='SCOPE_REVIEW' AND EXISTS(SELECT 1 FROM reviews o WHERE o.id=e.semantic_owner_id AND o.id=r.review_id AND o.task_id=t.id AND o.project_id=e.project_id AND o.feature_area_id=e.feature_area_id)) OR
 (e.kind='DEVELOPMENT' AND t.task_type='DEVELOPMENT' AND r.purpose IN ('IMPLEMENTATION','VERIFICATION') AND e.execution_proposal_id=r.execution_proposal_id AND EXISTS(SELECT 1 FROM execution_proposals o WHERE o.id=e.semantic_owner_id AND o.id=r.execution_proposal_id AND o.task_id=t.id AND o.project_id=e.project_id AND o.outcome IN ('PENDING','RUNNING')))))))`, v.ID, s.db.epoch, requireBoundary, requireBoundary, v.ThreadID).Scan(&valid)
	if err != nil {
		return fmt.Errorf("validate Agent Execution: %w", err)
	}
	if !valid {
		return agentexecution.ErrInvalidAssociation
	}
	return tx.Commit()
}
func (s *AgentExecutionStore) ValidateRunExecution(ctx context.Context, v run.Run) error {
	return NewRunStore(s.db).ValidateRunExecution(ctx, v)
}
func (s *ContextProbeStore) ValidateInspectionExecution(ctx context.Context, p contextprobe.Probe) error {
	fence, ok := runtimeepoch.FromContext(ctx)
	if !ok || fence.RunID != p.RunID || fence.Epoch != p.RuntimeEpoch {
		return runtimeepoch.ErrFenced
	}
	live, err := s.Get(ctx, p.ID)
	if err != nil {
		return err
	}
	if live.AgentExecutionID != p.AgentExecutionID || live.ID != p.ID || live.TaskID != p.TaskID || live.ProjectID != p.ProjectID || live.RunID != p.RunID || live.RuntimeEpoch != p.RuntimeEpoch || live.Status != contextprobe.StatusRunning || live.ThreadID != p.ThreadID {
		return agentexecution.ErrInvalidAssociation
	}
	r, err := NewRunStore(s.db).Get(ctx, p.RunID)
	if err != nil {
		return err
	}
	if r.AgentExecutionID != p.AgentExecutionID || r.ContextProbeID != p.ID || r.TaskID != p.TaskID || r.ProjectID != p.ProjectID || r.FeatureAreaID != "" || r.RuntimeEpoch != p.RuntimeEpoch || r.ThreadID != p.ThreadID {
		return agentexecution.ErrInvalidAssociation
	}
	return NewRunStore(s.db).ValidateRunExecution(ctx, r)
}

// Exact identity proof is also required for offline cancellation/recovery. It
// deliberately accepts an old runtime epoch: recovery itself fences that epoch.
func validateRunAgentExecutionIdentityTx(ctx context.Context, tx *sql.Tx, r run.Run) error {
	var exact bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=? AND e.provenance_kind<>'LEGACY_UNKNOWN' AND e.task_id=? AND e.project_id=? AND COALESCE(e.feature_area_id,'')=? AND t.project_id=e.project_id AND t.feature_area_id IS e.feature_area_id AND e.semantic_owner_kind=? AND e.semantic_owner_id=?)`, r.AgentExecutionID, r.TaskID, r.ProjectID, r.FeatureAreaID, r.OperationKind, r.OperationID).Scan(&exact)
	if err != nil {
		return err
	}
	if !exact {
		return agentexecution.ErrInvalidAssociation
	}
	return nil
}
