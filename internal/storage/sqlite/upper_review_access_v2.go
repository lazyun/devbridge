package sqlite

// Protocol v2 deliberately resolves an Upper read through the Run binding.
// A Review grant is the parent capability; it is never a licence to pick the
// first worktree belonging to that Review.

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
)

type upperV2Binding struct {
	workspaceID, repositoryID, memberID, worktreeID, baseline string
	startSnapshot, rootPath, rootIdentity                     string
	memberVersion                                             int
}

func (s *UpperReviewAccessStore) upperV2BindingTx(ctx context.Context, tx *sql.Tx, target review.UpperTarget, in directfile.Input, allowPreparing bool) (upperV2Binding, string, string, string, error) {
	var b upperV2Binding
	var state, allowed, forbidden string
	if in.RepositoryID == "" || in.MemberID == "" || in.MemberVersion < 1 {
		return b, "", "", "", review.ErrInvalidArgument
	}
	if err := requireUpperReviewCurrentTargetV2Tx(ctx, tx, target); err != nil {
		return b, "", "", "", err
	}
	err := tx.QueryRowContext(ctx, `SELECT g.state,g.initial_scope_paths_json,g.forbidden_paths_json,
 b.workspace_id,b.repository_id,b.member_id,b.member_version,COALESCE(b.worktree_id,''),b.origin_commit,COALESCE(b.start_snapshot,''),COALESCE(b.root_path,''),COALESCE(b.root_identity,'')
 FROM reviews r JOIN review_grants g ON g.review_id=r.id JOIN runs u ON u.id=g.run_id
 JOIN run_repository_bindings b ON b.run_id=u.id AND b.review_id=r.id
 JOIN workspaces w ON w.id=b.workspace_id AND w.id=(SELECT current_workspace_id FROM feature_areas WHERE id=r.feature_area_id) AND w.feature_area_id=r.feature_area_id AND w.status='READY' AND w.runtime_quarantined=0
 JOIN workspace_members m ON m.workspace_id=b.workspace_id AND m.repository_id=b.repository_id AND m.id=b.member_id AND m.identity_version=b.member_version AND m.origin_commit=b.origin_commit AND m.worktree_id=b.worktree_id AND m.preparation='READY'
 JOIN worktrees wt ON wt.id=b.worktree_id AND wt.repository_id=b.repository_id AND wt.status='READY'
 JOIN tasks t ON t.id=r.task_id AND t.current_review_id=r.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
 JOIN runtime_authority a ON a.singleton=1 JOIN feature_area_agent_claims c ON c.feature_area_id=r.feature_area_id
 WHERE r.id=? AND r.created_by_ai_session_id=? AND r.feature_area_id=? AND r.task_id=? AND r.executor_kind='UPPER_LLM'
 AND g.protocol_version=2 AND g.run_id=? AND g.runtime_epoch=? AND g.generation=?
 AND g.state IN ('PREPARING','ACTIVE','AWAITING_REPORT')
 AND u.status='RUNNING' AND u.purpose='SCOPE_REVIEW' AND u.review_id=r.id AND u.task_id=r.task_id AND u.feature_area_id=r.feature_area_id AND u.runtime_epoch=g.runtime_epoch
 AND a.epoch_id=u.runtime_epoch AND c.operation_kind='REVIEW' AND c.operation_id=r.id AND c.run_id=u.id AND c.runtime_epoch=u.runtime_epoch
 AND b.repository_id=? AND b.member_id=? AND b.member_version=? AND b.preparation='READY'`,
		target.ReviewID, target.AISessionID, target.FeatureAreaID, target.TaskID, target.RunID, target.RuntimeEpoch, target.GrantGeneration, in.RepositoryID, in.MemberID, in.MemberVersion).
		Scan(&state, &allowed, &forbidden, &b.workspaceID, &b.repositoryID, &b.memberID, &b.memberVersion, &b.worktreeID, &b.baseline, &b.startSnapshot, &b.rootPath, &b.rootIdentity)
	if errors.Is(err, sql.ErrNoRows) {
		return b, "", "", "", review.ErrTargetStale
	}
	if err != nil {
		return b, "", "", "", err
	}
	if !allowPreparing && state != "ACTIVE" {
		return b, "", "", "", review.ErrTargetStale
	}
	forbidden, err = reviewForbiddenPathsJSON(ctx, tx, target.ReviewID, forbidden)
	if err != nil {
		return b, "", "", "", err
	}
	return b, state, allowed, forbidden, nil
}

// requireUpperReviewCurrentTargetV2Tx is intentionally binding based.  The
// old scalar repository/base columns are v1 compatibility data and cannot be
// authority for a v2 Review.
func requireUpperReviewCurrentTargetV2Tx(ctx context.Context, tx *sql.Tx, target review.UpperTarget) error {
	var protocol int
	if err := tx.QueryRowContext(ctx, `SELECT protocol_version FROM review_grants WHERE review_id=?`, target.ReviewID).Scan(&protocol); err != nil {
		return err
	}
	if protocol != 2 {
		return review.ErrTargetStale
	}
	return requireUpperReviewCurrentTargetTx(ctx, tx, target)
}
