package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
)

// A read finds only a released tail; it does not create a successor or move any
// branch. The resulting plan must pass the existing explicit merge confirmation.
func (s *WorkflowProgressionStore) releasedTaskCompletion(ctx context.Context, project, area string) (*delivery.TaskCompletionStatus, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT ci.task_id FROM task_completion_intents ci
 JOIN tasks t ON t.id=ci.task_id JOIN task_completion_merge_authority a ON a.task_id=ci.task_id
 JOIN feature_area_commit_receipts p ON p.request_id=a.merge_request_id
 WHERE t.project_id=? AND t.feature_area_id=? AND t.status='ACTIVE' AND t.phase='DELIVERING'
 AND ci.completed_at IS NULL AND ci.execution_id=t.current_execution_id AND ci.revision_id=t.current_definition_revision_id
 AND p.released_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM feature_area_commit_receipts n WHERE json_extract(NULLIF(n.plan_json,''),'$.predecessorMergeRequestId')=p.request_id)`, project, area)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 1 {
		return nil, errors.New("TASK_COMPLETION_SELECTION_REQUIRED")
	}
	return s.ReadTaskCompletion(ctx, project, ids[0])
}

// PrepareTaskCompletionMergePlan addresses the original Task explicitly when
// an area contains several released completions. No newest-Task substitution.
func (s *WorkflowProgressionStore) PrepareTaskCompletionMergePlan(ctx context.Context, project, area, id string) (delivery.FeatureAreaPlan, error) {
	state, err := s.ReadTaskCompletion(ctx, project, id)
	if err != nil {
		return delivery.FeatureAreaPlan{}, err
	}
	if state == nil || state.Plan.FeatureAreaID != area || state.State != "REPLAN_REQUIRED" {
		return delivery.FeatureAreaPlan{}, task.ErrConflict
	}
	var eligible bool
	err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN feature_areas a ON a.id=t.feature_area_id JOIN workspaces w ON w.id=a.current_workspace_id
 WHERE t.id=? AND t.project_id=? AND t.feature_area_id=? AND t.status='ACTIVE' AND t.phase='DELIVERING'
 AND t.current_execution_id=? AND t.current_definition_revision_id=? AND a.status='ACTIVE' AND w.id=? AND w.status='READY' AND w.runtime_quarantined=0)
 AND NOT EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=?)`, id, project, area, state.Plan.ExecutionID, state.Plan.RevisionID, state.Plan.WorkspaceID, area).Scan(&eligible)
	if err != nil {
		return delivery.FeatureAreaPlan{}, err
	}
	if !eligible {
		return delivery.FeatureAreaPlan{}, task.ErrConflict
	}
	return s.prepareCompletionMergeSuccessor(ctx, state)
}

func (s *WorkflowProgressionStore) prepareCompletionMergeSuccessor(ctx context.Context, state *delivery.TaskCompletionStatus) (delivery.FeatureAreaPlan, error) {
	p := state.Plan
	p.Members = append([]delivery.TaskCompletionMember(nil), p.Members...)
	g := git.NewClient(git.Config{})
	for i := range p.Members {
		m := &p.Members[i]
		var repository string
		err := s.db.sql.QueryRowContext(ctx, `SELECT r.canonical_path,c.id,c.development_ref FROM repositories r JOIN development_cycles c ON c.repository_id=r.id AND c.project_id=r.project_id AND c.superseded_at IS NULL WHERE r.id=? AND r.project_id=? AND r.registration_state='ACTIVE'`, m.RepositoryID, p.ProjectID).Scan(&repository, &m.DevelopmentCycleID, &m.DevelopmentRef)
		if err != nil {
			return delivery.FeatureAreaPlan{}, err
		}
		if m.TargetPath, err = g.DevelopmentCheckout(ctx, repository, m.DevelopmentRef); err != nil {
			return delivery.FeatureAreaPlan{}, err
		}
		if err = g.NoOperation(ctx, m.TargetPath); err != nil {
			return delivery.FeatureAreaPlan{}, err
		}
		if err = g.RequireClean(ctx, m.TargetPath); err != nil {
			return delivery.FeatureAreaPlan{}, err
		}
		if m.ExpectedDevHead, err = g.ResolveHEAD(ctx, m.TargetPath); err != nil {
			return delivery.FeatureAreaPlan{}, err
		}
	}
	plan, err := s.taskCompletionMergePlan(ctx, p)
	if err != nil {
		return plan, err
	}
	plan.PredecessorMergeRequestID = state.MergeRequestID
	if err = plan.Canonicalize(); err != nil {
		return plan, err
	}
	if !plan.Valid() {
		return plan, task.ErrConflict
	}
	return plan, nil
}

// Only a receipt in this completion's immutable successor chain can discharge
// its final checkpoint. The original acceptance and plan remain unchanged.
func (s *WorkflowProgressionStore) finishCompletionMergeSuccessor(ctx context.Context, project, request string, plan delivery.FeatureAreaPlan) error {
	if plan.PredecessorMergeRequestID == "" {
		return nil
	}
	state, err := s.ReadTaskCompletion(ctx, project, plan.CompletionTaskID)
	if err != nil {
		return err
	}
	if state == nil || state.MergeRequestID != request {
		return task.ErrConflict
	}
	if state.State == "COMPLETED" {
		return nil
	}
	_, err = s.finishTaskCompletionCheckpoint(ctx, project, plan.CompletionTaskID, state.Plan, request, plan)
	if err != nil {
		_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE task_completion_intents SET last_error=? WHERE task_id=? AND completed_at IS NULL`, err.Error(), plan.CompletionTaskID)
	}
	return err
}

func completionMergeCurrentTx(ctx context.Context, tx *sql.Tx, id, request string) error {
	var current string
	err := tx.QueryRowContext(ctx, `SELECT merge_request_id FROM task_completion_merge_authority WHERE task_id=? ORDER BY depth DESC LIMIT 1`, id).Scan(&current)
	if err != nil {
		return err
	}
	if current != request {
		return task.ErrConflict
	}
	return nil
}

// Keep a small decoder here so malformed saved receipts never become a fresh
// plan or silently fall back to the original completion request.
func decodeCompletionMergePlan(raw string, taskID string) (delivery.FeatureAreaPlan, error) {
	var p delivery.FeatureAreaPlan
	if json.Unmarshal([]byte(raw), &p) != nil || !p.Valid() || p.CompletionTaskID != taskID {
		return p, task.ErrConflict
	}
	return p, nil
}
