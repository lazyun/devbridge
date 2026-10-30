package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/observability"
)

func (s *WorkflowProgressionStore) ReprepareFeatureAreaMerge(ctx context.Context, project string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error) {
	empty := delivery.MergeRetryReceipt{}
	if project == "" || in.FeatureAreaID == "" || in.MergeRequestID == "" || in.RepositoryID == "" || in.PredecessorOperationID == "" || !observability.ValidRequestID(in.RequestID) || strings.HasPrefix(in.RequestID, "merge-initial/") {
		return empty, delivery.ErrRequestConflict
	}
	if in.ReleasePlan {
		return s.releaseFeatureAreaMerge(ctx, project, in)
	}
	receipt, plan, fresh, err := s.reprepareFeatureAreaMerge(ctx, project, in)
	if err != nil || !fresh {
		return receipt, err
	}
	_, err = s.CommitFeatureAreaPlan(ctx, project, in.FeatureAreaID, in.MergeRequestID, plan)
	return receipt, err
}
func (s *WorkflowProgressionStore) reprepareFeatureAreaMerge(ctx context.Context, project string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, delivery.FeatureAreaPlan, bool, error) {
	empty, plan := delivery.MergeRetryReceipt{}, delivery.FeatureAreaPlan{}
	if got, found, err := readMergeRetryReceipt(ctx, s.db.sql, project, in); found || err != nil {
		return got, plan, false, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, plan, false, err
	}
	defer tx.Rollback()
	if got, found, err := readMergeRetryReceipt(ctx, tx, project, in); found || err != nil {
		return got, plan, false, err
	}
	var payload, hash, rawPlan, oldRequest, area, request, repository string
	err = tx.QueryRowContext(ctx, `SELECT o.payload_json,o.payload_sha256,p.plan_json,o.request_id,o.feature_area_id,o.merge_request_id,o.repository_id FROM merge_delivery_operations o JOIN feature_area_commit_receipts p ON p.request_id=o.merge_request_id WHERE o.operation_id=? AND o.project_id=?`, in.PredecessorOperationID, project).Scan(&payload, &hash, &rawPlan, &oldRequest, &area, &request, &repository)
	if err != nil {
		return empty, plan, false, err
	}
	if area != in.FeatureAreaID || request != in.MergeRequestID || repository != in.RepositoryID || oldRequest == in.RequestID {
		return empty, plan, false, delivery.ErrRequestConflict
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM merge_delivery_operations WHERE predecessor_operation_id=?)`, in.PredecessorOperationID).Scan(&exists); err != nil {
		return empty, plan, false, err
	}
	if exists {
		return empty, plan, false, delivery.ErrRetryConflict
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM merge_delivery_current_operations c JOIN merge_delivery_outcomes r ON r.operation_id=c.operation_id JOIN pending_feature_area_commits p ON p.request_id=c.merge_request_id WHERE c.operation_id=? AND r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1 AND p.completed_at IS NULL)`, in.PredecessorOperationID).Scan(&eligible)
	if err != nil {
		return empty, plan, false, err
	}
	if !eligible {
		return empty, plan, false, git.ErrRecoveryRequired
	}
	var frozen mergeDispatchInput
	if json.Unmarshal([]byte(payload), &frozen) != nil || workflowHash(payload) != hash || json.Unmarshal([]byte(rawPlan), &plan) != nil || !plan.Valid() || plan.SHA256 != frozen.PlanSHA256 || plan.ProjectID != project || plan.FeatureAreaID != area {
		return empty, plan, false, git.ErrRecoveryRequired
	}
	if err = s.validateMergeAuthority(ctx, tx, frozen); err != nil {
		return empty, plan, false, err
	}
	m := frozen.Member
	g := git.NewClient(git.Config{})
	if err = g.NoOperation(ctx, m.TargetPath); err != nil {
		return empty, plan, false, err
	}
	if err = g.RequireClean(ctx, m.TargetPath); err != nil {
		return empty, plan, false, err
	}
	target, err := g.InspectRepositoryState(ctx, m.TargetPath)
	if err != nil {
		return empty, plan, false, err
	}
	source, err := g.ResolveHEAD(ctx, m.SourcePath)
	if err != nil {
		return empty, plan, false, err
	}
	if target.Detached || target.BranchRef != m.DevelopmentRef || target.HeadCommit != m.ExpectedDevHead || source != m.SourceHead {
		return empty, plan, false, git.ErrRecoveryRequired
	}
	operation := "merge-retry/" + workflowHash(project+"\x00"+in.RequestID)
	_, err = tx.ExecContext(ctx, `INSERT INTO merge_delivery_operations(operation_id,project_id,feature_area_id,merge_request_id,repository_id,task_id,execution_id,member_id,workset_id,development_context_id,request_id,predecessor_operation_id,payload_json,payload_sha256,created_at) SELECT ?,project_id,feature_area_id,merge_request_id,repository_id,task_id,execution_id,member_id,workset_id,development_context_id,?,operation_id,payload_json,payload_sha256,? FROM merge_delivery_operations WHERE operation_id=?`, operation, in.RequestID, formatTime(time.Now().UTC()), in.PredecessorOperationID)
	if err != nil {
		return empty, plan, false, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE merge_delivery_current_operations SET operation_id=? WHERE merge_request_id=? AND repository_id=? AND operation_id=?`, operation, request, repository, in.PredecessorOperationID)
	if err != nil {
		return empty, plan, false, err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return empty, plan, false, delivery.ErrRetryConflict
	}
	if err = tx.Commit(); err != nil {
		return empty, plan, false, err
	}
	return delivery.MergeRetryReceipt{ProjectID: project, MergeRetryInput: in, OperationID: operation}, plan, true, nil
}
func (s *WorkflowProgressionStore) FeatureAreaMergeRetryCandidates(ctx context.Context, project, area string) ([]delivery.MergeRetryCandidate, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT o.merge_request_id,o.repository_id,o.operation_id,r.error,NOT EXISTS(SELECT 1 FROM merge_release_unsafe_members u WHERE u.request_id=p.request_id) FROM merge_delivery_current_operations c JOIN merge_delivery_operations o ON o.operation_id=c.operation_id JOIN merge_delivery_outcomes r ON r.operation_id=o.operation_id JOIN pending_feature_area_commits p ON p.request_id=o.merge_request_id WHERE o.project_id=? AND o.feature_area_id=? AND r.outcome='REJECTED_NOT_CONSUMED' AND p.completed_at IS NULL ORDER BY o.repository_id`, project, area)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []delivery.MergeRetryCandidate{}
	for rows.Next() {
		var v delivery.MergeRetryCandidate
		if err = rows.Scan(&v.MergeRequestID, &v.RepositoryID, &v.OperationID, &v.Error, &v.CanRelease); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// RecoverFeatureAreaMerge resumes only an existing exact parent, never a new
// plan. UNKNOWN outcomes remain fenced even when other members are resumable.
func (s *WorkflowProgressionStore) RecoverFeatureAreaMerge(ctx context.Context, project, area, request string) (featurearea.FeatureArea, error) {
	var raw string
	var completed bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT r.plan_json,r.completed_at IS NOT NULL FROM feature_area_commit_receipts r JOIN feature_areas a ON a.id=r.feature_area_id WHERE r.request_id=? AND r.feature_area_id=? AND a.project_id=? AND r.protocol_version=2`, request, area, project).Scan(&raw, &completed)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	if !completed {
		cap, e := s.CommitCapability(ctx, project, area)
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		if !cap.CanRecoverMerge || cap.RequestID != request {
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
	}
	var plan delivery.FeatureAreaPlan
	if json.Unmarshal([]byte(raw), &plan) != nil {
		return featurearea.FeatureArea{}, git.ErrRecoveryRequired
	}
	return s.CommitFeatureAreaPlan(ctx, project, area, request, plan)
}
