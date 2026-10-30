package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/run"
)

// RuntimeFacts reads exact authoritative runtime records. No status is persisted
// on an Execution and no current/latest Task or Run is used to associate facts.
func (s *AgentExecutionStore) RuntimeFacts(ctx context.Context, id string) (map[string]agentexecution.RuntimeFacts, error) {
	rows, err := s.db.sql.QueryContext(ctx, `WITH upper_reauthorized AS (`+upperReauthorizedRunSQL+`) SELECT r.id,
 EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id AND i.operation_kind=r.operation_kind AND i.operation_id=r.operation_id AND i.runtime_epoch=r.runtime_epoch AND i.applied_at IS NULL),
 CASE WHEN EXISTS(SELECT 1 FROM agent_orphan_dispositions o WHERE o.run_id=r.id AND o.task_id=r.task_id AND o.owner_type=r.operation_kind AND o.owner_id=r.operation_id) THEN 'UNKNOWN_ORPHANED'
 WHEN EXISTS(SELECT 1 FROM agent_operation_recovery_states x WHERE x.run_id=r.id AND x.classification IN ('UNKNOWN','CONFLICT')) THEN 'UNKNOWN'
 WHEN EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id AND i.operation_kind=r.operation_kind AND i.operation_id=r.operation_id AND i.runtime_epoch=r.runtime_epoch AND i.advance_state='UNKNOWN') THEN 'UNKNOWN'
 ELSE '' END,
 EXISTS(SELECT 1 FROM upper_reauthorized u WHERE u.id=r.id),
 COALESCE((SELECT c.state FROM execution_cancellations c WHERE c.run_id=r.id AND c.owner_type=r.operation_kind AND c.owner_id=r.operation_id ORDER BY c.requested_at DESC,c.request_id LIMIT 1),'')
 FROM runs r JOIN agent_executions e ON e.id=r.agent_execution_id WHERE e.id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]agentexecution.RuntimeFacts{}
	for rows.Next() {
		var id string
		var f agentexecution.RuntimeFacts
		if err := rows.Scan(&id, &f.FinalizationPending, &f.BackendDisposition, &f.UpperInterruptionSuperseded, &f.CancellationState); err != nil {
			return nil, err
		}
		out[id] = f
	}
	return out, rows.Err()
}

// ExactRuns bounds projection reads to the durable association, avoiding a
// full-history scan while retaining every attempt of this Execution.
func (s *AgentExecutionStore) ExactRuns(ctx context.Context, id string) ([]run.Run, error) {
	rows, err := s.db.sql.QueryContext(ctx, selectRun+` WHERE r.agent_execution_id=? ORDER BY r.created_at,r.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []run.Run{}
	for rows.Next() {
		v, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// CancellationFacts is a read-only lookup through immutable envelope identity.
// An empty runID means SQL NULL (pre-backend cancellation), never any/latest Run.
// Ordering chooses the last request only within this exact owner/Run target.
func (s *AgentExecutionStore) CancellationFacts(ctx context.Context, executionID, runID string) (agentexecution.RuntimeFacts, error) {
	var f agentexecution.RuntimeFacts
	err := s.db.sql.QueryRowContext(ctx, `SELECT c.state,c.backend_disposition
 FROM agent_executions e JOIN execution_cancellations c
 ON c.project_id=e.project_id AND c.task_id=e.task_id
 AND c.owner_type=e.semantic_owner_kind AND c.owner_id=e.semantic_owner_id
 WHERE e.id=? AND e.provenance_kind<>'LEGACY_UNKNOWN'
 AND c.run_id IS NULLIF(?,'')
 AND ((c.run_id IS NULL AND e.kind='DEVELOPMENT' AND c.execution_id=e.execution_proposal_id
       AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.agent_execution_id=e.id))
 OR EXISTS(SELECT 1 FROM runs r WHERE r.id=c.run_id AND r.agent_execution_id=e.id
           AND r.project_id=e.project_id AND r.task_id=e.task_id AND r.feature_area_id IS e.feature_area_id
           AND r.operation_kind=e.semantic_owner_kind AND r.operation_id=e.semantic_owner_id))
 ORDER BY c.requested_at DESC,c.request_id DESC LIMIT 1`, executionID, runID).Scan(&f.CancellationState, &f.BackendDisposition)
	if errors.Is(err, sql.ErrNoRows) {
		return agentexecution.RuntimeFacts{}, nil
	}
	return f, err
}
