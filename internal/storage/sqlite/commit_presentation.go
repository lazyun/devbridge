package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/git"
)

// CommitPresentationFacts only reads v43 facts; mutation and capability owners
// remain unchanged. Error-free pending receipts represent unfinished work, not failure.
func (s *WorkflowProgressionStore) CommitPresentationFacts(ctx context.Context, projectID, areaID string) (featurearea.CommitFacts, error) {
	return s.commitPresentationFacts(ctx, projectID, areaID, true)
}

// CommitInventoryFacts excludes optional live Git diagnostics.
func (s *WorkflowProgressionStore) CommitInventoryFacts(ctx context.Context, projectID, areaID string) (featurearea.CommitFacts, error) {
	return s.commitPresentationFacts(ctx, projectID, areaID, false)
}
func (s *WorkflowProgressionStore) commitPresentationFacts(ctx context.Context, projectID, areaID string, inspectGit bool) (featurearea.CommitFacts, error) {
	out := featurearea.CommitFacts{Tasks: []featurearea.TaskCommitFact{}}
	rows, err := s.db.sql.QueryContext(ctx, `SELECT r.task_id,r.completed_at IS NULL,COALESCE(r.last_error,'') FROM task_commit_receipts r JOIN tasks t ON t.id=r.task_id WHERE t.project_id=? AND t.feature_area_id=? ORDER BY r.task_id`, projectID, areaID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id string
		var f featurearea.TaskCommitFact
		if err = rows.Scan(&id, &f.Pending, &f.LastError); err != nil {
			rows.Close()
			return out, err
		}
		f.TaskID = id
		f.Delivery, err = s.taskDeliveryStatus(ctx, id)
		if err != nil {
			rows.Close()
			return out, err
		}
		if len(f.Delivery.Members) > 0 {
			var needed, merged int
			err = s.db.sql.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(EXISTS(
 SELECT 1 FROM feature_area_commit_members a JOIN feature_area_commit_receipts p ON p.request_id=a.request_id, json_each(a.task_commits_json) j
 WHERE p.feature_area_id=? AND a.repository_id=m.repository_id AND a.state='COMPLETED' AND j.value=m.resulting_commit)),0)
 FROM task_commit_members m WHERE m.task_id=? AND m.state<>'NO_CHANGE'`, areaID, id).Scan(&needed, &merged)
			if err != nil {
				rows.Close()
				return out, err
			}
			f.IntegrationState = "UNMERGED"
			if !f.Pending && f.Delivery.State == "COMPLETED" && needed == merged {
				f.IntegrationState = "MERGED"
			} else if merged > 0 {
				f.IntegrationState = "PARTIAL"
			}
		}
		if len(f.Delivery.Members) == 0 {
			var mergedAt sql.NullString
			e := s.db.sql.QueryRowContext(ctx, `SELECT m.resulting_dev_head,m.completed_at
 FROM tasks t JOIN task_commit_receipts q ON q.task_id=t.id
 JOIN feature_area_commit_receipts p ON p.feature_area_id=t.feature_area_id
 JOIN feature_area_commit_members m ON m.request_id=p.request_id AND m.repository_id=t.repository_id
 WHERE t.id=? AND t.status='SUCCEEDED' AND t.merged_commit IS NULL
 AND q.protocol_version=1 AND q.completed_at IS NOT NULL AND q.terminal_outcome='APPLIED' AND q.resulting_commit=t.commit_sha
 AND m.state='COMPLETED' AND m.resulting_dev_head<>'' AND m.completed_at IS NOT NULL
 AND EXISTS(SELECT 1 FROM json_each(m.task_commits_json) j WHERE j.value=t.commit_sha)
 ORDER BY m.completed_at,m.request_id LIMIT 1`, id).Scan(&f.MergedCommit, &mergedAt)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				rows.Close()
				return out, e
			}
			if e == nil {
				f.MergedAt, e = parseOptionalTime(mergedAt)
				if e != nil {
					rows.Close()
					return out, e
				}
				f.IntegrationState = "MERGED"
			}
		}
		out.Tasks = append(out.Tasks, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	err = s.db.sql.QueryRowContext(ctx, `SELECT r.request_id,(r.completed_at IS NULL AND r.released_at IS NULL),COALESCE(r.last_error,''),r.source_head,r.expected_dev_head FROM feature_area_commit_receipts r JOIN feature_areas a ON a.id=r.feature_area_id WHERE a.project_id=? AND a.id=? ORDER BY (r.completed_at IS NULL AND r.released_at IS NULL) DESC,COALESCE(r.completed_at,r.released_at) DESC LIMIT 1`, projectID, areaID).Scan(&out.RequestID, &out.Pending, &out.LastError, &out.SourceHead, &out.ExpectedDevHead)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if !inspectGit {
		out.Delivery, _ = s.featureAreaDeliveryStatus(ctx, areaID, out.RequestID)
		return out, nil
	}
	_, source, _, target, _, e := s.commitWorkspace(ctx, projectID, areaID)
	if e == nil {
		g := git.NewClient(git.Config{})
		out.WorkspaceHead, _ = g.ResolveHEAD(ctx, source)
		out.DevHead, _ = g.ResolveHEAD(ctx, target)
	}
	out.Delivery, _ = s.featureAreaDeliveryStatus(ctx, areaID, out.RequestID)
	return out, nil
}

func (s *WorkflowProgressionStore) taskDeliveryStatus(ctx context.Context, taskID string) (delivery.Status, error) {
	out := delivery.Status{State: ""}
	rows, err := s.db.sql.QueryContext(ctx, `SELECT repository_id,member_id,member_version,worktree_id,state,resulting_commit,last_error FROM task_commit_members WHERE task_id=? ORDER BY repository_id`, taskID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var m delivery.Member
		if err = rows.Scan(&m.RepositoryID, &m.MemberID, &m.MemberVersion, &m.WorktreeID, &m.State, &m.Commit, &m.Error); err != nil {
			rows.Close()
			return out, err
		}
		out.Members = append(out.Members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Members) > 0 {
		out.State = "PENDING"
		done := true
		for _, m := range out.Members {
			if m.State != "COMPLETED" && m.State != "NO_CHANGE" {
				done = false
			}
		}
		if done {
			out.State = "COMPLETED"
		}
	}
	var outcome string
	err = s.db.sql.QueryRowContext(ctx, `SELECT terminal_outcome FROM task_commit_receipts WHERE task_id=?`, taskID).Scan(&outcome)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if outcome == "ABANDONED" {
		out.State = "ABANDONED"
		// Older scalar abandonments retained success only in immutable operation
		// history. Project it without rewriting those historical receipts.
		if len(out.Members) == 0 {
			var m delivery.Member
			err = s.db.sql.QueryRowContext(ctx, `SELECT o.repository_id,o.member_id,r.commit_oid
 FROM task_commit_receipts q JOIN task_delivery_current_operations c ON c.task_id=q.task_id
 JOIN task_delivery_operations o ON o.operation_id=c.operation_id
 JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id
 WHERE q.task_id=? AND q.protocol_version=1 AND r.outcome='APPLIED'`, taskID).Scan(&m.RepositoryID, &m.MemberID, &m.Commit)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			if err == nil {
				m.State = "COMPLETED"
				out.Members = append(out.Members, m)
			}
		}
	}
	return out, nil
}

func (s *WorkflowProgressionStore) featureAreaDeliveryStatus(ctx context.Context, areaID, requestID string) (delivery.Status, error) {
	out := delivery.Status{RequestID: requestID}
	var released bool
	if requestID != "" {
		if err := s.db.sql.QueryRowContext(ctx, `SELECT released_at IS NOT NULL FROM feature_area_commit_receipts WHERE request_id=? AND feature_area_id=?`, requestID, areaID).Scan(&released); err != nil {
			return out, err
		}
	}
	if requestID == "" {
		return out, nil
	}
	rows, err := s.db.sql.QueryContext(ctx, `SELECT repository_id,member_id,member_version,worktree_id,state,resulting_dev_head,last_error,development_ref,expected_dev_head,source_head FROM feature_area_commit_members WHERE request_id=? ORDER BY repository_id`, requestID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var m delivery.Member
		if err = rows.Scan(&m.RepositoryID, &m.MemberID, &m.MemberVersion, &m.WorktreeID, &m.State, &m.Commit, &m.Error, &m.DevelopmentRef, &m.ExpectedHead, &m.SourceHead); err != nil {
			rows.Close()
			return out, err
		}
		out.Members = append(out.Members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Members) > 0 {
		out.State = "PENDING"
		done := true
		for _, m := range out.Members {
			if m.State != "COMPLETED" && m.State != "NO_CHANGE" {
				done = false
			}
		}
		if done {
			out.State = "COMPLETED"
		}
		if released {
			out.State = "RELEASED"
		}
	}
	return out, nil
}
