package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/observability"
)

// ReprepareTaskCommit grants one successor to an exact rejected operation.
// Request replay is resolved before live state and never repeats Git. Only the
// first successful authority transaction invokes the already-authorized worker.
func (s *WorkflowProgressionStore) ReprepareTaskCommit(ctx context.Context, project string, in delivery.RetryInput) (delivery.RetryReceipt, error) {
	empty := delivery.RetryReceipt{}
	if project == "" || in.TaskID == "" || in.RepositoryID == "" || in.PredecessorOperationID == "" || !observability.ValidRequestID(in.RequestID) || strings.HasPrefix(in.RequestID, "initial/") {
		return empty, delivery.ErrRequestConflict
	}
	receipt, fresh, err := s.reprepareTaskCommit(ctx, project, in)
	if err != nil || !fresh {
		return receipt, err
	}
	_, err = s.RetryTaskCommit(ctx, project, in.TaskID)
	return receipt, err
}

func (s *WorkflowProgressionStore) reprepareTaskCommit(ctx context.Context, project string, in delivery.RetryInput) (delivery.RetryReceipt, bool, error) {
	empty := delivery.RetryReceipt{}
	if got, found, err := readTaskRetryReceipt(ctx, s.db.sql, project, in); found || err != nil {
		return got, false, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return empty, false, err
	}
	defer tx.Rollback()
	if got, found, err := readTaskRetryReceipt(ctx, tx, project, in); found || err != nil {
		return got, false, err
	}
	var predecessorTask, repository, payload, hash, member, execution, workspace, development, previousRequest string
	err = tx.QueryRowContext(ctx, `SELECT task_id,repository_id,payload_json,payload_sha256,member_id,execution_id,workset_id,development_context_id,request_id FROM task_delivery_operations WHERE operation_id=? AND project_id=?`, in.PredecessorOperationID, project).Scan(&predecessorTask, &repository, &payload, &hash, &member, &execution, &workspace, &development, &previousRequest)
	if err != nil {
		return empty, false, err
	}
	if predecessorTask != in.TaskID || repository != in.RepositoryID || previousRequest == in.RequestID {
		return empty, false, delivery.ErrRequestConflict
	}
	if err = taskActionAdmissionTx(ctx, tx, in.TaskID); err != nil {
		return empty, false, err
	}
	var successor bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_delivery_operations WHERE predecessor_operation_id=?)`, in.PredecessorOperationID).Scan(&successor); err != nil {
		return empty, false, err
	}
	if successor {
		return empty, false, delivery.ErrRetryConflict
	}
	var eligible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_delivery_operations o JOIN task_delivery_current_operations c ON c.operation_id=o.operation_id JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id JOIN tasks t ON t.id=o.task_id JOIN task_commit_receipts q ON q.task_id=t.id JOIN feature_areas a ON a.id=t.feature_area_id JOIN workspaces w ON w.id=q.workspace_id
 WHERE o.operation_id=? AND r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1 AND t.status='ACTIVE' AND t.phase IN ('DELIVERING','RECOVERY_REQUIRED') AND q.completed_at IS NULL AND q.revision_id=t.current_definition_revision_id AND a.current_workspace_id=q.workspace_id AND a.status='ACTIVE' AND w.status='READY' AND w.runtime_quarantined=0
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations x WHERE x.task_id=t.id AND (x.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED') OR `+unresolvedCancellationInterruptXSQL+` OR x.backend_disposition='UNKNOWN_ORPHANED' OR (x.owner_type='TASK' AND x.state='CANCELED'))))`, in.PredecessorOperationID).Scan(&eligible)
	if err != nil {
		return empty, false, err
	}
	if !eligible {
		return empty, false, git.ErrRecoveryRequired
	}
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.TaskID, project))
	if err != nil {
		return empty, false, err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, current.FeatureAreaID); err != nil {
		return empty, false, err
	}
	if err = commitBusy(ctx, tx, current.FeatureAreaID); err != nil {
		return empty, false, err
	}
	if err = ensureRepositoryActiveTx(ctx, tx, project, repository); err != nil {
		return empty, false, err
	}
	var frozen taskDeliveryDispatchInput
	if err = json.Unmarshal([]byte(payload), &frozen); err != nil {
		return empty, false, err
	}
	if frozen.TaskID != current.ID || frozen.RepositoryID != repository || frozen.RevisionID != current.CurrentDefinitionRevisionID || workflowHash(payload) != hash {
		return empty, false, git.ErrRecoveryRequired
	}
	var repositoryPath, rootIdentity string
	err = tx.QueryRowContext(ctx, `SELECT r.canonical_path,COALESCE(d.root_identity,'') FROM repositories r JOIN workspace_members m ON m.repository_id=r.id AND m.workspace_id=? JOIN worktrees wt ON wt.id=m.worktree_id LEFT JOIN task_commit_members d ON d.task_id=? AND d.repository_id=r.id
 WHERE r.id=? AND m.preparation='READY' AND wt.status='READY' AND wt.path=? AND wt.branch=? AND (d.task_id IS NULL OR (m.id=d.member_id AND m.identity_version=d.member_version AND m.worktree_id=d.worktree_id))`, workspace, in.TaskID, repository, frozen.WorkspacePath, frozen.Branch).Scan(&repositoryPath, &rootIdentity)
	if err != nil {
		return empty, false, git.ErrRecoveryRequired
	}
	if !strings.HasPrefix(development, "workspace/") {
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM development_cycles WHERE id=? AND project_id=? AND repository_id=? AND superseded_at IS NULL)`, development, project, repository).Scan(&active); err != nil {
			return empty, false, err
		}
		if !active {
			return empty, false, git.ErrRecoveryRequired
		}
	}
	g := git.NewClient(git.Config{})
	if err = g.ValidateManagedWorktree(ctx, repositoryPath, frozen.WorkspacePath, frozen.Branch); err != nil {
		return empty, false, err
	}
	if rootIdentity != "" {
		identity, e := upperV2RootIdentity(frozen.WorkspacePath)
		if e != nil || identity != rootIdentity {
			return empty, false, git.ErrRecoveryRequired
		}
	}
	if err = g.NoOperation(ctx, frozen.WorkspacePath); err != nil {
		return empty, false, err
	}
	head, err := g.ResolveHEAD(ctx, frozen.WorkspacePath)
	if err != nil {
		return empty, false, err
	}
	if head != frozen.ExpectedHead {
		return empty, false, git.ErrRecoveryRequired
	}
	if frozen.Selective {
		err = g.ValidateTaskPathsSnapshot(ctx, frozen.WorkspacePath, frozen.ExpectedHead, frozen.Snapshot, frozen.Paths)
	} else {
		var snapshot string
		snapshot, err = g.Snapshot(ctx, frozen.WorkspacePath)
		if err == nil && snapshot != frozen.Snapshot {
			err = git.ErrSnapshotDrift
		}
	}
	if err != nil {
		return empty, false, err
	}
	operation := "retry/" + workflowHash(project+"\x00"+in.RequestID)
	now := formatTime(time.Now().UTC())
	_, err = tx.ExecContext(ctx, `INSERT INTO task_delivery_operations(operation_id,project_id,task_id,repository_id,request_id,predecessor_operation_id,payload_json,payload_sha256,member_id,execution_id,workset_id,development_context_id,created_at) SELECT ?,project_id,task_id,repository_id,?,operation_id,payload_json,payload_sha256,member_id,execution_id,workset_id,development_context_id,? FROM task_delivery_operations WHERE operation_id=?`, operation, in.RequestID, now, in.PredecessorOperationID)
	if err != nil {
		return empty, false, err
	}
	cas, err := tx.ExecContext(ctx, `UPDATE task_delivery_current_operations SET operation_id=? WHERE task_id=? AND repository_id=? AND operation_id=?`, operation, in.TaskID, repository, in.PredecessorOperationID)
	if err != nil {
		return empty, false, err
	}
	if n, _ := cas.RowsAffected(); n != 1 {
		return empty, false, delivery.ErrRetryConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='DELIVERING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase IN ('DELIVERING','RECOVERY_REQUIRED')`, now, in.TaskID); err != nil {
		return empty, false, err
	}
	if err = tx.Commit(); err != nil {
		return empty, false, err
	}
	return delivery.RetryReceipt{ProjectID: project, TaskID: in.TaskID, RepositoryID: repository, PredecessorOperationID: in.PredecessorOperationID, OperationID: operation, RequestID: in.RequestID}, true, nil
}

func (s *WorkflowProgressionStore) TaskDeliveryRetryCandidates(ctx context.Context, project, taskID string) ([]delivery.RetryCandidate, error) {
	rows, err := s.db.sql.QueryContext(ctx, `SELECT o.repository_id,o.operation_id,r.error FROM task_delivery_current_operations c JOIN task_delivery_operations o ON o.operation_id=c.operation_id JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id JOIN tasks t ON t.id=o.task_id JOIN task_commit_receipts q ON q.task_id=t.id WHERE t.project_id=? AND t.id=? AND t.status='ACTIVE' AND q.completed_at IS NULL AND r.outcome='REJECTED_NOT_CONSUMED' ORDER BY o.repository_id`, project, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []delivery.RetryCandidate{}
	for rows.Next() {
		var v delivery.RetryCandidate
		if err = rows.Scan(&v.RepositoryID, &v.OperationID, &v.Error); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
