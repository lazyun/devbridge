package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/git"
)

type mergeDispatchInput struct {
	ProjectID, FeatureAreaID, WorkspaceID, PlanSHA256 string
	CompletionTaskID                                  string
	Member                                            delivery.FeatureAreaMember
}

func mergeInput(plan delivery.FeatureAreaPlan, m delivery.FeatureAreaMember) mergeDispatchInput {
	return mergeDispatchInput{ProjectID: plan.ProjectID, FeatureAreaID: plan.FeatureAreaID, WorkspaceID: plan.WorkspaceID, PlanSHA256: plan.SHA256, CompletionTaskID: plan.CompletionTaskID, Member: m}
}

// Persisted operations keep their original bytes and hash. Before completion
// plans existed, ordinary area merges omitted CompletionTaskID altogether.
// Accept that exact encoding only for an ordinary merge; never erase a Task's
// completion authority or accept arbitrary re-encodings of a stored operation.
func mergePayloadMatches(payload, hash string, in mergeDispatchInput) bool {
	if workflowHash(payload) != hash {
		return false
	}
	raw, err := json.Marshal(in)
	if err == nil && payload == string(raw) {
		return true
	}
	if in.CompletionTaskID != "" {
		return false
	}
	legacy := struct {
		ProjectID, FeatureAreaID, WorkspaceID, PlanSHA256 string
		Member                                            delivery.FeatureAreaMember
	}{in.ProjectID, in.FeatureAreaID, in.WorkspaceID, in.PlanSHA256, in.Member}
	raw, err = json.Marshal(legacy)
	return err == nil && payload == string(raw)
}

// Every advertised Task commit must be covered by the frozen merge inputs.
// A rewound source is safe only when the target already contains the commit.
func validateMergeCommitCoverage(ctx context.Context, g *git.Client, m delivery.FeatureAreaMember) error {
	if len(m.TaskCommits) == 0 {
		return git.ErrRecoveryRequired
	}
	for _, commit := range m.TaskCommits {
		included, err := g.IsAncestor(ctx, m.SourcePath, commit, m.SourceHead)
		if err != nil {
			return err
		}
		if !included {
			included, err = g.IsAncestor(ctx, m.TargetPath, commit, m.ExpectedDevHead)
			if err != nil {
				return err
			}
		}
		if !included {
			return git.ErrRecoveryRequired
		}
	}
	return nil
}

// Initial operations are reserved atomically with every member of the confirmed
// plan. Missing operations on old pending plans cannot authorize a new dispatch.
func insertMergeOperation(ctx context.Context, tx *sql.Tx, request string, in mergeDispatchInput) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	operation := "merge-initial/" + workflowHash(request+"\x00"+in.Member.RepositoryID)
	var taskID, execution string
	err = tx.QueryRowContext(ctx, `SELECT t.id,COALESCE(NULLIF(t.current_execution_id,''),'direct/'||q.result_id) FROM (`+completionAcceptedDeliveryCommitsSQL+`) m JOIN tasks t ON t.id=m.task_id JOIN task_commit_receipts q ON q.task_id=t.id WHERE t.feature_area_id=? AND t.project_id=? AND m.repository_id=? AND m.state='COMPLETED' AND EXISTS(SELECT 1 FROM feature_area_commit_members a,json_each(a.task_commits_json) j WHERE a.request_id=? AND a.repository_id=m.repository_id AND j.value=m.resulting_commit) ORDER BY t.id LIMIT 1`, in.FeatureAreaID, in.ProjectID, in.Member.RepositoryID, request).Scan(&taskID, &execution)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO merge_delivery_operations(operation_id,project_id,feature_area_id,merge_request_id,repository_id,task_id,execution_id,member_id,workset_id,development_context_id,request_id,payload_json,payload_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operation, in.ProjectID, in.FeatureAreaID, request, in.Member.RepositoryID, taskID, execution, in.Member.MemberID, in.WorkspaceID, in.Member.DevelopmentCycleID, operation, string(raw), workflowHash(string(raw)), formatTime(time.Now().UTC()))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO merge_delivery_current_operations VALUES(?,?,?)`, request, in.Member.RepositoryID, operation)
	return err
}

// validateMergeAuthority is shared by dispatch and explicit reprepare. It
// checks live authorization without altering the frozen target or workset.
func (s *WorkflowProgressionStore) validateMergeAuthority(ctx context.Context, q definitionQuerier, in mergeDispatchInput) error {
	if err := directFileAdmissionTx(ctx, q, s.db, in.FeatureAreaID); err != nil {
		return err
	}
	if err := commitBusy(ctx, q, in.FeatureAreaID); err != nil {
		return err
	}
	var incomplete bool
	if err := q.QueryRowContext(ctx, `SELECT `+incompleteFeatureAreaTasksSQL, in.FeatureAreaID).Scan(&incomplete); err != nil {
		return err
	}
	if incomplete && in.CompletionTaskID == "" {
		return errors.New("TASK_COMMIT_PENDING")
	}
	if in.CompletionTaskID != "" {
		var exact bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_completion_intents ci JOIN tasks t ON t.id=ci.task_id WHERE ci.task_id=? AND t.project_id=? AND t.feature_area_id=? AND t.status='ACTIVE' AND t.phase='DELIVERING' AND ci.revision_id=t.current_definition_revision_id AND ci.execution_id=t.current_execution_id AND ci.completed_at IS NULL AND json_extract(ci.plan_json,'$.workspaceId')=?)`, in.CompletionTaskID, in.ProjectID, in.FeatureAreaID, in.WorkspaceID).Scan(&exact); err != nil {
			return err
		}
		if !exact {
			return git.ErrRecoveryRequired
		}
	}
	m := in.Member
	var repositoryPath string
	err := q.QueryRowContext(ctx, `SELECT r.canonical_path FROM feature_areas a JOIN workspaces w ON w.id=a.current_workspace_id JOIN workspace_members m ON m.workspace_id=w.id JOIN worktrees wt ON wt.id=m.worktree_id JOIN repositories r ON r.id=m.repository_id JOIN development_cycles c ON c.project_id=a.project_id AND c.repository_id=r.id AND c.superseded_at IS NULL WHERE a.id=? AND a.project_id=? AND a.status='ACTIVE' AND w.id=? AND w.status='READY' AND w.runtime_quarantined=0 AND m.id=? AND m.identity_version=? AND m.repository_id=? AND m.preparation='READY' AND wt.id=? AND wt.status='READY' AND wt.path=? AND wt.branch=? AND r.registration_state='ACTIVE' AND c.id=? AND c.development_ref=?`, in.FeatureAreaID, in.ProjectID, in.WorkspaceID, m.MemberID, m.MemberVersion, m.RepositoryID, m.WorktreeID, m.SourcePath, m.SourceBranch, m.DevelopmentCycleID, m.DevelopmentRef).Scan(&repositoryPath)
	if err != nil {
		return git.ErrRecoveryRequired
	}
	if len(m.TaskCommits) == 0 {
		return git.ErrRecoveryRequired
	}
	for _, sha := range m.TaskCommits {
		var accepted bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM (`+completionAcceptedDeliveryCommitsSQL+`) m JOIN tasks t ON t.id=m.task_id JOIN task_commit_receipts p ON p.task_id=t.id WHERE t.feature_area_id=? AND t.project_id=? AND `+acceptedDeliveryTaskSQL+` AND p.completed_at IS NOT NULL AND p.terminal_outcome='APPLIED' AND p.workspace_id=? AND p.revision_id=t.current_definition_revision_id AND m.repository_id=? AND m.member_id=? AND m.member_version=? AND m.worktree_id=? AND m.state='COMPLETED' AND m.resulting_commit=? AND (p.protocol_version=2 OR p.resulting_commit=m.resulting_commit))`, in.FeatureAreaID, in.ProjectID, in.WorkspaceID, m.RepositoryID, m.MemberID, m.MemberVersion, m.WorktreeID, sha).Scan(&accepted)
		if err != nil {
			return err
		}
		if !accepted {
			return git.ErrRecoveryRequired
		}
	}
	g := git.NewClient(git.Config{})
	if err = g.ValidateManagedWorktree(ctx, repositoryPath, m.SourcePath, m.SourceBranch); err != nil {
		return err
	}
	target, err := g.DevelopmentCheckout(ctx, repositoryPath, m.DevelopmentRef)
	if err != nil {
		return err
	}
	if target != m.TargetPath {
		return git.ErrRecoveryRequired
	}
	return validateMergeCommitCoverage(ctx, g, m)
}

func (s *WorkflowProgressionStore) dispatchMerge(ctx context.Context, request string, in mergeDispatchInput, effect func() (string, error)) (string, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var operation, payload, hash string
	err = tx.QueryRowContext(ctx, `SELECT o.operation_id,o.payload_json,o.payload_sha256 FROM merge_delivery_current_operations c JOIN merge_delivery_operations o ON o.operation_id=c.operation_id WHERE c.merge_request_id=? AND c.repository_id=?`, request, in.Member.RepositoryID).Scan(&operation, &payload, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", git.ErrRecoveryRequired
	}
	if err != nil {
		return "", err
	}
	if !mergePayloadMatches(payload, hash, in) {
		return "", git.ErrRecoveryRequired
	}
	var outcome, merge, detail string
	err = tx.QueryRowContext(ctx, `SELECT outcome,merge_oid,error FROM merge_delivery_outcomes WHERE operation_id=?`, operation).Scan(&outcome, &merge, &detail)
	if err == nil {
		if outcome == "APPLIED" {
			return merge, nil
		}
		return "", fmt.Errorf("%w: %s", delivery.ErrRejected, detail)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var dispatched bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM merge_delivery_dispatches WHERE operation_id=?)`, operation).Scan(&dispatched); err != nil {
		return "", err
	}
	if dispatched {
		return "", git.ErrRecoveryRequired
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO merge_delivery_dispatches VALUES(?,?,?)`, operation, s.db.epoch, formatTime(time.Now().UTC())); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	if s.deliveryFaultHook != nil {
		if err = s.deliveryFaultHook("AREA_MERGE_BEFORE_GIT", in.Member.RepositoryID); err != nil {
			return "", err
		}
	}
	// All errors here precede Git. The exact refusal is durable; restoring live
	// state later cannot turn this same rejected operation into a fresh attempt.
	if err = s.validateMergeAuthority(ctx, s.db.sql, in); err != nil {
		err = &git.BeforeEffectError{Err: err}
	} else {
		merge, err = effect()
	}
	rejection := ""
	if err != nil {
		var before *git.BeforeEffectError
		if !errors.As(err, &before) {
			return "", err
		}
		rejection = before.Error()
	}
	if s.deliveryFaultHook != nil {
		if hookErr := s.deliveryFaultHook("AREA_MERGE_BEFORE_RESULT", in.Member.RepositoryID); hookErr != nil {
			return "", hookErr
		}
	}
	outcome, before := "APPLIED", 0
	if rejection != "" {
		outcome, before = "REJECTED_NOT_CONSUMED", 1
		merge = ""
	}
	_, saveErr := s.db.sql.ExecContext(context.WithoutCancel(ctx), `INSERT INTO merge_delivery_outcomes(operation_id,outcome,before_external_effect,commit_oid,merge_oid,error,created_at) VALUES(?,?,?,?,?,?,?)`, operation, outcome, before, in.Member.SourceHead, merge, rejection, formatTime(time.Now().UTC()))
	if saveErr != nil {
		return "", saveErr
	}
	if rejection != "" {
		return "", fmt.Errorf("%w: %s", delivery.ErrRejected, rejection)
	}
	return merge, nil
}
