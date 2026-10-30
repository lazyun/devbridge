package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// insertMultiTaskDeliveryTx creates the complete v2 delivery intent in the
// same transaction as business acceptance.  Git is deliberately performed
// later, outside this SQLite transaction; every member is nevertheless
// durable before acceptance becomes visible.
func insertMultiTaskDeliveryTx(ctx context.Context, tx *sql.Tx, current task.Task, result directfile.ImplementationResult, now time.Time) error {
	if len(current.RepositoryTargets) == 0 || result.ProtocolVersion != 2 || result.ID == "" || result.TaskID != current.ID || result.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID {
		return errors.New("MULTI_DELIVERY_RESULT_CONFLICT")
	}
	if err := workset.ValidateTargets(current.RepositoryTargets); err != nil {
		return err
	}
	if len(result.Members) != len(current.RepositoryTargets) {
		return errors.New("MULTI_DELIVERY_MEMBER_SET_CONFLICT")
	}
	targets := make(map[string]workset.Target, len(current.RepositoryTargets))
	for _, target := range current.RepositoryTargets {
		targets[target.RepositoryID] = target
	}
	members := append([]directfile.MemberResult(nil), result.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].RepositoryID < members[j].RepositoryID })
	seen := map[string]bool{}
	workspace := ""
	type preparedMember struct {
		member        directfile.MemberResult
		paths         string
		state, reason string
	}
	prepared := make([]preparedMember, 0, len(members))
	for _, member := range members {
		if err := ensureRepositoryActiveTx(ctx, tx, current.ProjectID, member.RepositoryID); err != nil {
			return err
		}
		target, ok := targets[member.RepositoryID]
		if !ok || seen[member.RepositoryID] || member.MemberID != target.MemberID || member.MemberVersion != target.MemberVersion || member.OriginCommit != target.OriginCommit || member.WorkspaceID == "" || member.WorktreeID == "" || member.WorktreePath == "" || member.Branch == "" || member.RootIdentity == "" || member.StartHead == "" || member.StartSnapshot == "" || member.EndSnapshot == "" {
			return errors.New("MULTI_DELIVERY_MEMBER_SET_CONFLICT")
		}
		if workspace == "" {
			workspace = member.WorkspaceID
		} else if workspace != member.WorkspaceID {
			return errors.New("MULTI_DELIVERY_WORKSPACE_CONFLICT")
		}
		seen[member.RepositoryID] = true
		paths, err := canonicalDeliveryPaths(member.ChangedPaths)
		if err != nil {
			return err
		}
		if target.Write.Kind == "NONE" && len(paths) != 0 {
			return errors.New("MULTI_DELIVERY_READ_ONLY_CHANGED")
		}
		if target.Write.Kind == "PATHS" {
			for _, changed := range paths {
				allowed := false
				for _, root := range target.Write.Paths {
					if changed == root || strings.HasPrefix(changed, root+"/") {
						allowed = true
						break
					}
				}
				if !allowed {
					return errors.New("MULTI_DELIVERY_PATH_OUTSIDE_APPROVED_SCOPE")
				}
			}
		}
		state := "PENDING"
		reason := ""
		if target.Write.Kind == "NONE" {
			state, reason = "NO_CHANGE", "READ_ONLY"
		} else if len(paths) == 0 {
			state, reason = "NO_CHANGE", "NO_CHANGE"
		}
		encoded, err := json.Marshal(paths)
		if err != nil {
			return err
		}
		prepared = append(prepared, preparedMember{member: member, paths: string(encoded), state: state, reason: reason})
	}
	if workspace == "" {
		return errors.New("MULTI_DELIVERY_WORKSPACE_CONFLICT")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch,protocol_version,result_id)
VALUES(?,?,?,?,?,?,?,?,?)`, current.ID, current.FeatureAreaID, current.CurrentDefinitionRevisionID, "", workspace, "", "", 2, result.ID)
	if err != nil {
		return err
	}
	for _, value := range prepared {
		member := value.member
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_commit_members(task_id,repository_id,member_id,member_version,worktree_id,workspace_id,revision_id,result_id,origin_commit,start_snapshot,verified_snapshot,expected_head,workspace_path,branch,root_identity,changed_paths_json,state,last_error,completed_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, current.ID, member.RepositoryID, member.MemberID, member.MemberVersion, member.WorktreeID, member.WorkspaceID, current.CurrentDefinitionRevisionID, result.ID, member.OriginCommit, member.StartSnapshot, member.EndSnapshot, member.StartHead, member.WorktreePath, member.Branch, member.RootIdentity, value.paths, value.state, value.reason, nullableCompletion(value.state, now)); err != nil {
			return err
		}
	}
	return nil
}

// insertCodingTaskDeliveryTx projects the accepted Implementation/Verification
// observations into the same v76 per-member delivery machinery used by Direct
// File. It records Run identities as the evidence source and never creates a
// Direct File result or mutation receipt.
func insertCodingTaskDeliveryTx(ctx context.Context, tx *sql.Tx, current task.Task, execution executionproposal.Proposal, now time.Time) error {
	implementation, err := implementationEvidence(ctx, tx, execution)
	if err != nil {
		return err
	}
	if execution.ApprovedWorksetHash == "" || implementation.ImplementationRunID == "" || execution.VerificationRunID == "" || len(current.RepositoryTargets) == 0 {
		return errors.New("CODING_DELIVERY_EVIDENCE_INCOMPLETE")
	}
	if err := workset.ValidateTargets(current.RepositoryTargets); err != nil {
		return err
	}
	type observedMember struct {
		repositoryID, memberID, worktreeID, workspaceID, originCommit string
		startSnapshot, verifiedSnapshot, expectedHead                 string
		workspacePath, branch, rootIdentity, changedPathsJSON         string
		memberVersion                                                 int
	}
	rows, err := tx.QueryContext(ctx, `SELECT i.repository_id,i.member_id,i.member_version,i.worktree_id,i.workspace_id,i.origin_commit,i.start_snapshot,v.end_snapshot,i.start_head,wt.path,wt.branch,i.runtime_root_identity,i.changed_paths_json
FROM run_repository_bindings i
JOIN run_repository_bindings v ON v.run_id=? AND v.repository_id=i.repository_id AND v.member_id=i.member_id AND v.member_version=i.member_version AND v.worktree_id=i.worktree_id
JOIN worktrees wt ON wt.id=i.worktree_id AND wt.status='READY'
WHERE i.run_id=? AND i.execution_id=? AND v.execution_id=? AND i.observation_state IN ('OBSERVED','NO_CHANGE') AND v.observation_state='NO_CHANGE'
ORDER BY i.repository_id`, execution.VerificationRunID, implementation.ImplementationRunID, implementation.ID, execution.ID)
	if err != nil {
		return err
	}
	var members []observedMember
	for rows.Next() {
		var value observedMember
		if err = rows.Scan(&value.repositoryID, &value.memberID, &value.memberVersion, &value.worktreeID, &value.workspaceID, &value.originCommit, &value.startSnapshot, &value.verifiedSnapshot, &value.expectedHead, &value.workspacePath, &value.branch, &value.rootIdentity, &value.changedPathsJSON); err != nil {
			rows.Close()
			return err
		}
		if err = ensureRepositoryActiveTx(ctx, tx, current.ProjectID, value.repositoryID); err != nil {
			rows.Close()
			return err
		}
		members = append(members, value)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(members) != len(current.RepositoryTargets) {
		return errors.New("CODING_DELIVERY_MEMBER_SET_CONFLICT")
	}
	targets := make(map[string]workset.Target, len(current.RepositoryTargets))
	for _, target := range current.RepositoryTargets {
		targets[target.RepositoryID] = target
	}
	workspaceID := ""
	for index := range members {
		member := &members[index]
		target, ok := targets[member.repositoryID]
		if !ok || member.memberID != target.MemberID || member.memberVersion != target.MemberVersion || member.originCommit != target.OriginCommit || member.startSnapshot == "" || member.verifiedSnapshot == "" || member.expectedHead == "" || member.rootIdentity == "" {
			return errors.New("CODING_DELIVERY_MEMBER_SET_CONFLICT")
		}
		if workspaceID == "" {
			workspaceID = member.workspaceID
		} else if workspaceID != member.workspaceID {
			return errors.New("CODING_DELIVERY_WORKSPACE_CONFLICT")
		}
		member.rootIdentity, err = upperV2RootIdentity(member.workspacePath)
		if err != nil {
			return err
		}
	}
	if workspaceID == "" {
		return errors.New("CODING_DELIVERY_WORKSPACE_CONFLICT")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch,protocol_version,result_id,evidence_kind,implementation_run_id,verification_run_id)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, current.ID, current.FeatureAreaID, current.CurrentDefinitionRevisionID, "", workspaceID, "", "", 2, "", "CODING", implementation.ImplementationRunID, execution.VerificationRunID); err != nil {
		return err
	}
	for _, member := range members {
		target := targets[member.repositoryID]
		var changed []string
		if err = json.Unmarshal([]byte(member.changedPathsJSON), &changed); err != nil {
			return errors.New("CODING_DELIVERY_CHANGED_PATHS_INVALID")
		}
		changed, err = canonicalDeliveryPaths(changed)
		if err != nil {
			return err
		}
		if target.Write.Kind == "NONE" && len(changed) != 0 {
			return errors.New("MULTI_DELIVERY_READ_ONLY_CHANGED")
		}
		if target.Write.Kind == "PATHS" && !deliveryPathsAllowed(target.Write.Paths, changed) {
			return errors.New("MULTI_DELIVERY_PATH_OUTSIDE_APPROVED_SCOPE")
		}
		encoded, err := json.Marshal(changed)
		if err != nil {
			return err
		}
		state, reason := "PENDING", ""
		if target.Write.Kind == "NONE" {
			state, reason = "NO_CHANGE", "READ_ONLY"
		} else if len(changed) == 0 {
			state, reason = "NO_CHANGE", "NO_CHANGE"
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_commit_members(task_id,repository_id,member_id,member_version,worktree_id,workspace_id,revision_id,result_id,evidence_kind,implementation_run_id,verification_run_id,origin_commit,start_snapshot,verified_snapshot,expected_head,workspace_path,branch,root_identity,changed_paths_json,state,last_error,completed_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, current.ID, member.repositoryID, member.memberID, member.memberVersion, member.worktreeID, member.workspaceID, current.CurrentDefinitionRevisionID, "", "CODING", implementation.ImplementationRunID, execution.VerificationRunID, member.originCommit, member.startSnapshot, member.verifiedSnapshot, member.expectedHead, member.workspacePath, member.branch, member.rootIdentity, string(encoded), state, reason, nullableCompletion(state, now)); err != nil {
			return err
		}
	}
	return nil
}

func deliveryPathsAllowed(roots, changed []string) bool {
	for _, value := range changed {
		allowed := false
		for _, root := range roots {
			if value == root || strings.HasPrefix(value, root+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func canonicalDeliveryPaths(values []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") || seen[value] {
			return nil, errors.New("MULTI_DELIVERY_CHANGED_PATH_INVALID")
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

// uniqueDeliveryPaths is retained for comparison of historical result JSON.
// New durable intent creation uses canonicalDeliveryPaths and rejects malformed
// values rather than normalizing them.
func uniqueDeliveryPaths(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func nullableCompletion(state string, now time.Time) any {
	if state == "PENDING" {
		return nil
	}
	return formatTime(now)
}
