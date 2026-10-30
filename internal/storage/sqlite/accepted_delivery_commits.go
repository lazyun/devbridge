package sqlite

import (
	"context"
	"strings"
	"time"
)

// Read both receipt formats without converting historical scalar receipts into
// v2 member receipts. Missing legacy workspace bindings remain visible so a
// planner cannot silently drop their commits; authority checks reject them.
// A completion-confirmed Task stays ACTIVE until its merge is recorded.
const acceptedDeliveryTaskSQL = `(t.status='SUCCEEDED' OR (t.status='ACTIVE' AND t.phase='DELIVERING' AND EXISTS(SELECT 1 FROM task_completion_intents ci WHERE ci.task_id=t.id AND ci.execution_id=t.current_execution_id AND ci.revision_id=t.current_definition_revision_id)))`

const acceptedDeliveryCommitsSQL = `
SELECT m.task_id,m.repository_id,m.member_id,m.member_version,m.worktree_id,
 m.workspace_id,m.workspace_path,m.branch,m.state,m.resulting_commit,NULL AS legacy_merged
FROM task_commit_members m JOIN tasks t ON t.id=m.task_id JOIN task_commit_receipts q ON q.task_id=t.id
WHERE t.status='SUCCEEDED' AND q.protocol_version=2 AND q.completed_at IS NOT NULL AND q.terminal_outcome='APPLIED'
 AND m.state='COMPLETED' AND m.resulting_commit<>''
UNION ALL
SELECT t.id,t.repository_id,COALESCE(wm.id,''),COALESCE(wm.identity_version,0),COALESCE(wt.id,''),
 q.workspace_id,q.workspace_path,q.branch,'COMPLETED',t.commit_sha,t.merged_commit
FROM tasks t JOIN task_commit_receipts q ON q.task_id=t.id
LEFT JOIN workspace_members wm ON wm.workspace_id=q.workspace_id AND wm.repository_id=t.repository_id
LEFT JOIN worktrees wt ON wt.id=wm.worktree_id AND wt.path=q.workspace_path AND wt.branch=q.branch
WHERE t.status='SUCCEEDED' AND q.protocol_version=1 AND q.completed_at IS NOT NULL AND q.terminal_outcome='APPLIED'
 AND t.commit_sha IS NOT NULL AND t.commit_sha<>''`

// Runtime-only extension. Historical migration SQL keeps its original
// terminal-Task predicate; v131 upgrades its exact trigger after adding tables.
var completionAcceptedDeliveryCommitsSQL = strings.ReplaceAll(acceptedDeliveryCommitsSQL, "t.status='SUCCEEDED'", acceptedDeliveryTaskSQL)

// Alias m is a row of acceptedDeliveryCommitsSQL; t is its owning Task.
// A completed merge from another FeatureArea cannot discharge this area's work.
const unmergedDeliveryCommitSQL = `m.legacy_merged IS NULL AND NOT EXISTS(
 SELECT 1 FROM feature_area_commit_members done JOIN feature_area_commit_receipts p ON p.request_id=done.request_id,
 json_each(done.task_commits_json) committed
 WHERE p.feature_area_id=t.feature_area_id AND done.repository_id=m.repository_id
 AND done.state='COMPLETED' AND committed.value=m.resulting_commit)`

// Scalar Task readers retain their checkpoint contract. Only commits included
// in this exact completed member can gain a merge checkpoint; later Tasks and
// unrelated repositories are never marked merged by an area-wide update.
func (s *WorkflowProgressionStore) completeFeatureAreaPlan(ctx context.Context, areaID, requestID string) error {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET merged_commit=(
 SELECT m.resulting_dev_head FROM feature_area_commit_members m,json_each(m.task_commits_json) j
 WHERE m.request_id=? AND m.repository_id=tasks.repository_id AND m.state='COMPLETED' AND j.value=tasks.commit_sha),merged_at=?
 WHERE feature_area_id=? AND status='SUCCEEDED' AND merged_commit IS NULL
 AND EXISTS(SELECT 1 FROM task_commit_receipts q WHERE q.task_id=tasks.id AND q.protocol_version=1
 AND q.completed_at IS NOT NULL AND q.terminal_outcome='APPLIED' AND q.resulting_commit=tasks.commit_sha)
 AND EXISTS(SELECT 1 FROM feature_area_commit_members m JOIN feature_area_commit_receipts p ON p.request_id=m.request_id,json_each(m.task_commits_json) j
 WHERE m.request_id=? AND p.feature_area_id=tasks.feature_area_id AND m.repository_id=tasks.repository_id
 AND m.state='COMPLETED' AND j.value=tasks.commit_sha)`, requestID, now, areaID, requestID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE feature_area_commit_receipts SET completed_at=?,last_error=NULL WHERE request_id=? AND feature_area_id=? AND protocol_version=2 AND completed_at IS NULL`, now, requestID, areaID); err != nil {
		return err
	}
	return tx.Commit()
}
