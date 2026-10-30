package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

var errCompletionNotReady = errors.New("TASK_COMPLETION_VERIFICATION_NOT_PASSED")

func requireCompletionVerificationTx(ctx context.Context, tx *sql.Tx, e executionproposal.Proposal) error {
	if e.WorkSpec.ImplementationMode == task.ImplementationDirectFile {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT r.result_json FROM direct_file_verifications v JOIN direct_file_implementation_results r ON r.id=v.result_id WHERE v.execution_id=?`, e.ID).Scan(&raw); err != nil {
			return errCompletionNotReady
		}
		var result directfile.ImplementationResult
		if json.Unmarshal([]byte(raw), &result) != nil {
			return errCompletionNotReady
		}
		owner, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, e.TaskID))
		if err != nil {
			return err
		}
		if err = requireFileVerificationEvidenceTx(ctx, tx, owner, result); err != nil {
			return errCompletionNotReady
		}
	} else if err := requireAcceptableVerificationTx(ctx, tx, e); err != nil {
		return errCompletionNotReady
	}
	if e.VerificationSpec.Mode == task.VerificationModeNone {
		return nil
	}
	r, err := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE id=?`, e.VerificationRunID))
	if err != nil || r.VerificationReport == nil || !run.VerificationAssessmentPassed(*r.VerificationReport, e.VerificationSpec) {
		return errCompletionNotReady
	}
	for _, c := range r.VerificationReport.Checks {
		if c.Required && c.Observed != nil && c.Observed.ExitCode != nil && *c.Observed.ExitCode != 0 {
			return errCompletionNotReady
		}
	}
	return nil
}

func (s *WorkflowProgressionStore) PrepareTaskCompletionPlan(ctx context.Context, project, id, execution, revision string) (delivery.TaskCompletionPlan, error) {
	var plan delivery.TaskCompletionPlan
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return plan, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, id, project))
	if err != nil {
		return plan, err
	}
	e, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND task_id=?`, execution, id))
	if err != nil {
		return plan, err
	}
	if t.CurrentDefinitionRevisionID != revision || e.TaskDefinitionRevisionID != revision {
		return plan, task.ErrConflict
	}
	if err = validateAcceptanceEligibilityTx(ctx, tx, t, e, false); err != nil {
		return plan, err
	}
	if err = requireCompletionVerificationTx(ctx, tx, e); err != nil {
		return plan, err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, t.FeatureAreaID); err != nil {
		return plan, err
	}
	var held bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_holds WHERE task_id=?)`, id).Scan(&held); err != nil {
		return plan, err
	}
	if held {
		return plan, errors.New("TASK_DEFERRED")
	}
	plan = delivery.TaskCompletionPlan{ProjectID: project, FeatureAreaID: t.FeatureAreaID, TaskID: id, ExecutionID: execution, RevisionID: revision, Members: []delivery.TaskCompletionMember{}}
	// Capture only the exact Task's writable members, never Project inventory.
	memberBinding := ""
	if len(t.RepositoryTargets) > 0 {
		memberBinding = "MULTI"
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.repository_id,m.id,m.identity_version,m.worktree_id,wt.path,wt.branch,r.canonical_path,r.name,c.development_ref,c.id,m.workspace_id,
 CASE WHEN ?='' THEN COALESCE(v.end_snapshot,'') ELSE COALESCE(b.end_snapshot,'') END
 FROM feature_areas a JOIN workspace_members m ON m.workspace_id=a.current_workspace_id
 JOIN worktrees wt ON wt.id=m.worktree_id AND wt.status='READY'
 JOIN repositories r ON r.id=m.repository_id AND r.project_id=a.project_id AND r.registration_state='ACTIVE'
 JOIN development_cycles c ON c.project_id=a.project_id AND c.repository_id=m.repository_id AND c.superseded_at IS NULL
 JOIN runs v ON v.id=? AND v.task_id=? AND v.execution_proposal_id=?
 LEFT JOIN run_repository_bindings b ON b.run_id=v.id AND b.repository_id=m.repository_id AND b.member_id=m.id AND b.member_version=m.identity_version AND b.observation_state='NO_CHANGE'
 WHERE a.id=? AND a.project_id=? AND a.status='ACTIVE' AND m.preparation='READY'
 AND ((?='' AND m.repository_id=?) OR (?<>'' AND EXISTS(SELECT 1 FROM json_each((SELECT json_extract(payload_json,'$.repositoryTargets') FROM task_definition_revisions WHERE id=?)) j WHERE json_extract(j.value,'$.repositoryId')=m.repository_id AND json_extract(j.value,'$.write.kind')<>'NONE')))
 ORDER BY m.repository_id`, memberBinding, terminalExecutionRunID(e), id, execution, t.FeatureAreaID, project, memberBinding, t.RepositoryID, memberBinding, revision)
	if err != nil {
		return plan, err
	}
	type source struct {
		member          delivery.TaskCompletionMember
		repo, workspace string
	}
	var sources []source
	for rows.Next() {
		var v source
		m := &v.member
		if err = rows.Scan(&m.RepositoryID, &m.MemberID, &m.MemberVersion, &m.WorktreeID, &m.SourcePath, &m.SourceBranch, &v.repo, &m.RepositoryName, &m.DevelopmentRef, &m.DevelopmentCycleID, &v.workspace, &m.VerifiedSnapshot); err != nil {
			rows.Close()
			return plan, err
		}
		sources = append(sources, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return plan, err
	}
	if len(sources) == 0 {
		return plan, errCompletionNotReady
	}
	// The materialized revision JSON is not assumed to expose SQL-only columns.
	// Every source is checked again against the decoded frozen target set below.
	g := git.NewClient(git.Config{})
	for _, v := range sources {
		m := v.member
		if plan.WorkspaceID == "" {
			plan.WorkspaceID = v.workspace
		}
		if plan.WorkspaceID != v.workspace || m.VerifiedSnapshot == "" {
			return plan, task.ErrConflict
		}
		if err = g.ValidateManagedWorktree(ctx, v.repo, m.SourcePath, m.SourceBranch); err != nil {
			return plan, err
		}
		if err = g.NoOperation(ctx, m.SourcePath); err != nil {
			return plan, err
		}
		if m.SourceHead, err = g.ResolveHEAD(ctx, m.SourcePath); err != nil {
			return plan, err
		}
		snapshot, e := g.Snapshot(ctx, m.SourcePath)
		if e != nil {
			return plan, e
		}
		if snapshot != m.VerifiedSnapshot {
			return plan, git.ErrSnapshotDrift
		}
		if m.TargetPath, err = g.DevelopmentCheckout(ctx, v.repo, m.DevelopmentRef); err != nil {
			return plan, err
		}
		if err = g.NoOperation(ctx, m.TargetPath); err != nil {
			return plan, err
		}
		if err = g.RequireClean(ctx, m.TargetPath); err != nil {
			return plan, err
		}
		if m.ExpectedDevHead, err = g.ResolveHEAD(ctx, m.TargetPath); err != nil {
			return plan, err
		}
		m.TaskCommits, err = g.CommitsExclusive(ctx, m.SourcePath, m.ExpectedDevHead, m.SourceHead)
		if err != nil {
			return plan, err
		}
		for _, sha := range m.TaskCommits {
			var allowed bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM (`+completionAcceptedDeliveryCommitsSQL+`) ac JOIN tasks t ON t.id=ac.task_id WHERE t.feature_area_id=? AND t.project_id=? AND ac.repository_id=? AND ac.resulting_commit=?)`, t.FeatureAreaID, project, m.RepositoryID, sha).Scan(&allowed); err != nil {
				return plan, err
			}
			if !allowed {
				return plan, errors.New("TASK_COMPLETION_UNACCEPTED_SOURCE_COMMIT")
			}
		}
		plan.Members = append(plan.Members, m)
	}
	if err = plan.Canonicalize(); err != nil {
		return plan, err
	}
	if !plan.Valid() {
		return plan, errors.New("TASK_COMPLETION_PLAN_INVALID")
	}
	return plan, nil
}

// reserveTaskCompletionTx shares the acceptance transaction. No Task may become
// SUCCEEDED between its commit and the integration covered by this plan.
func reserveTaskCompletionTx(ctx context.Context, tx *sql.Tx, p delivery.TaskCompletionPlan, request string) error {
	if !p.Valid() {
		return task.ErrConflict
	}
	encoded, _ := json.Marshal(p)
	merge := "task-completion/" + workflowHash(p.ProjectID+"\x00"+p.TaskID+"\x00"+request)
	_, err := tx.ExecContext(ctx, `INSERT INTO task_completion_intents(task_id,execution_id,revision_id,plan_json,plan_sha256,acceptance_request_id,merge_request_id) VALUES(?,?,?,?,?,?,?)`, p.TaskID, p.ExecutionID, p.RevisionID, string(encoded), p.SHA256, request, merge)
	return err
}

func validateCompletionReplay(ctx context.Context, tx *sql.Tx, id, request string, p *delivery.TaskCompletionPlan) error {
	var hash, prior string
	err := tx.QueryRowContext(ctx, `SELECT plan_sha256,acceptance_request_id FROM task_completion_intents WHERE task_id=?`, id).Scan(&hash, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		if p != nil {
			return task.ErrConflict
		}
		return nil
	}
	if err != nil {
		return err
	}
	if p == nil || !p.Valid() || hash != p.SHA256 || prior != request {
		return task.ErrConflict
	}
	return nil
}

func (s *WorkflowProgressionStore) ReadTaskCompletion(ctx context.Context, project, id string) (*delivery.TaskCompletionStatus, error) {
	var raw string
	var done bool
	out := &delivery.TaskCompletionStatus{}
	err := s.db.sql.QueryRowContext(ctx, `SELECT c.plan_json,(SELECT a.merge_request_id FROM task_completion_merge_authority a WHERE a.task_id=c.task_id ORDER BY depth DESC LIMIT 1),c.completed_at IS NOT NULL,COALESCE(p.last_error,c.last_error) FROM task_completion_intents c JOIN tasks t ON t.id=c.task_id LEFT JOIN feature_area_commit_receipts p ON p.request_id=(SELECT a.merge_request_id FROM task_completion_merge_authority a WHERE a.task_id=c.task_id ORDER BY depth DESC LIMIT 1) WHERE t.id=? AND t.project_id=?`, id, project).Scan(&raw, &out.MergeRequestID, &done, &out.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &out.Plan); err != nil || !out.Plan.Valid() {
		return nil, task.ErrConflict
	}
	out.State = "DELIVERING"
	if out.Error != "" {
		out.State = "BLOCKED"
	}
	if !done {
		var released bool
		if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE request_id=? AND released_at IS NOT NULL)`, out.MergeRequestID).Scan(&released); err != nil {
			return nil, err
		}
		if released {
			out.State = "REPLAN_REQUIRED"
		}
	}
	if done {
		out.State = "COMPLETED"
	}
	return out, nil
}

// taskCompletionMergePlan expands only the commit generated by the exact
// accepted snapshot. Development refs, heads, member set and prior commits stay
// frozen from the user's original confirmation.
func (s *WorkflowProgressionStore) taskCompletionMergePlan(ctx context.Context, p delivery.TaskCompletionPlan) (delivery.FeatureAreaPlan, error) {
	out := delivery.FeatureAreaPlan{CompletionTaskID: p.TaskID, ProjectID: p.ProjectID, FeatureAreaID: p.FeatureAreaID, WorkspaceID: p.WorkspaceID, Members: []delivery.FeatureAreaMember{}}
	var ready bool
	if err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts q JOIN tasks t ON t.id=q.task_id WHERE t.id=? AND t.project_id=? AND t.current_definition_revision_id=? AND t.current_execution_id=? AND t.status='ACTIVE' AND t.phase='DELIVERING' AND q.revision_id=? AND q.workspace_id=? AND q.completed_at IS NOT NULL AND q.terminal_outcome='APPLIED')`, p.TaskID, p.ProjectID, p.RevisionID, p.ExecutionID, p.RevisionID, p.WorkspaceID).Scan(&ready); err != nil {
		return out, err
	}
	if !ready {
		return out, task.ErrConflict
	}
	g := git.NewClient(git.Config{})
	for _, frozen := range p.Members {
		m := frozen.FeatureAreaMember
		m.TaskCommits = append([]string{}, frozen.TaskCommits...)
		var commit string
		var found bool
		if err := s.db.sql.QueryRowContext(ctx, `SELECT COALESCE((SELECT resulting_commit FROM task_commit_members WHERE task_id=? AND repository_id=? AND state='COMPLETED'),(SELECT resulting_commit FROM task_commit_receipts WHERE task_id=? AND protocol_version=1),'')`, p.TaskID, m.RepositoryID, p.TaskID).Scan(&commit); err != nil {
			return out, err
		}
		if commit != "" {
			m.SourceHead = commit
			m.TaskCommits = append(m.TaskCommits, commit)
		}
		if len(m.TaskCommits) == 0 {
			head, err := g.ResolveHEAD(ctx, m.TargetPath)
			if err != nil {
				return out, err
			}
			if head != m.ExpectedDevHead {
				return out, git.ErrRecoveryRequired
			}
			if err = g.RequireClean(ctx, m.TargetPath); err != nil {
				return out, err
			}
			var errCheck error
			found, errCheck = g.IsAncestor(ctx, m.TargetPath, m.SourceHead, m.ExpectedDevHead)
			if errCheck != nil {
				return out, errCheck
			}
			if !found {
				return out, git.ErrRecoveryRequired
			}
			continue
		}
		sourceOnly, err := g.CommitsExclusive(ctx, m.SourcePath, m.ExpectedDevHead, m.SourceHead)
		if err != nil {
			return out, err
		}
		allowed := map[string]bool{}
		for _, sha := range m.TaskCommits {
			allowed[sha] = true
		}
		for _, sha := range sourceOnly {
			if !allowed[sha] {
				return out, git.ErrRecoveryRequired
			}
		}
		out.Members = append(out.Members, m)
	}
	if err := out.Canonicalize(); err != nil {
		return out, err
	}
	return out, nil
}

func (s *WorkflowProgressionStore) FinishTaskCompletion(ctx context.Context, project, id string) (result task.Task, failure error) {
	defer func() {
		if failure != nil {
			_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE task_completion_intents SET last_error=? WHERE task_id=? AND completed_at IS NULL`, failure.Error(), id)
		}
	}()
	status, err := s.ReadTaskCompletion(ctx, project, id)
	if err != nil {
		return task.Task{}, err
	}
	if status == nil {
		return task.Task{}, task.ErrNotFound
	}
	if status.State == "COMPLETED" {
		return NewTaskStore(s.db).Get(ctx, id)
	}
	p := status.Plan
	var plan delivery.FeatureAreaPlan
	var stored string
	err = s.db.sql.QueryRowContext(ctx, `SELECT plan_json FROM feature_area_commit_receipts WHERE request_id=?`, status.MergeRequestID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		plan, err = s.taskCompletionMergePlan(ctx, p)
	} else if err == nil {
		plan, err = decodeCompletionMergePlan(stored, id)
	}
	if err == nil && len(plan.Members) > 0 {
		_, err = s.CommitFeatureAreaPlan(ctx, project, p.FeatureAreaID, status.MergeRequestID, plan)
	}
	if err != nil {
		_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE task_completion_intents SET last_error=? WHERE task_id=? AND completed_at IS NULL`, err.Error(), id)
		return task.Task{}, err
	}
	if plan.PredecessorMergeRequestID != "" {
		// The confirmed successor owns its completion checkpoint, including
		// recovery of a saved merge result. Do not terminalize it twice.
		return NewTaskStore(s.db).Get(ctx, id)
	}
	return s.finishTaskCompletionCheckpoint(ctx, project, id, p, status.MergeRequestID, plan)
}

func (s *WorkflowProgressionStore) finishTaskCompletionCheckpoint(ctx context.Context, project, id string, p delivery.TaskCompletionPlan, mergeRequest string, plan delivery.FeatureAreaPlan) (task.Task, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	if err = completionMergeCurrentTx(ctx, tx, id, mergeRequest); err != nil {
		return task.Task{}, err
	}

	var pending bool
	if len(plan.Members) > 0 {
		if err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM feature_area_commit_receipts WHERE request_id=? AND completed_at IS NOT NULL)`, mergeRequest).Scan(&pending); err != nil {
			return task.Task{}, err
		}
		if pending {
			return task.Task{}, git.ErrRecoveryRequired
		}
	}
	if err = commitBusy(ctx, tx, p.FeatureAreaID); err != nil {
		return task.Task{}, err
	}
	now := time.Now().UTC()
	at := formatTime(now)
	if pending, e := hasPendingDeliveryCancellation(ctx, tx, id); e != nil {
		return task.Task{}, e
	} else if pending {
		return task.Task{}, git.ErrRecoveryRequired
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_completion_intents SET completed_at=?,last_error='' WHERE task_id=? AND execution_id=? AND revision_id=? AND completed_at IS NULL`, at, id, p.ExecutionID, p.RevisionID); err != nil {
		return task.Task{}, err
	}
	mergedHead := ""
	if len(p.Members) > 0 {
		mergedHead = p.Members[0].ExpectedDevHead
		_ = tx.QueryRowContext(ctx, `SELECT resulting_dev_head FROM feature_area_commit_members WHERE request_id=? AND repository_id=? AND state IN ('COMPLETED','NO_CHANGE')`, mergeRequest, p.Members[0].RepositoryID).Scan(&mergedHead)
	}
	changed, err := tx.ExecContext(ctx, `UPDATE tasks SET status='SUCCEEDED',merged_commit=CASE WHEN commit_sha IS NOT NULL THEN COALESCE(merged_commit,?) ELSE NULL END,merged_at=CASE WHEN commit_sha IS NOT NULL THEN COALESCE(merged_at,?) ELSE NULL END,updated_at=? WHERE id=? AND project_id=? AND status='ACTIVE' AND phase='DELIVERING' AND current_execution_id=? AND current_definition_revision_id=?`, mergedHead, at, at, id, project, p.ExecutionID, p.RevisionID)
	if err != nil {
		return task.Task{}, err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return task.Task{}, task.ErrConflict
	}
	payload, _ := event.Payload(map[string]any{"from": task.StatusActive, "to": task.StatusSucceeded, "phase": task.PhaseDelivering, "completion_merge_request_id": mergeRequest})
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: project, TaskID: id, EntityType: "task", EntityID: id, Payload: payload, CreatedAt: now}); err != nil {
		return task.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return task.Task{}, err
	}
	return NewTaskStore(s.db).Get(ctx, id)
}
