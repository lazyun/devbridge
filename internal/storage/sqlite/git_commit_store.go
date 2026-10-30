package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
)

// The DataRoot writer lease excludes a second Core. Keyed command locks only
// serialize finalization, while durable receipts fence agent/task admission.
var gitCommandLocks sync.Map
var errFeatureAreaBusy = errors.New("FEATURE_AREA_BUSY")

func lockGitCommand(key string) func() {
	v, _ := gitCommandLocks.LoadOrStore(key, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

func commitBusy(ctx context.Context, q definitionQuerier, area string) error {
	authority, err := readRuntimeAuthority(ctx, q, area)
	if err != nil {
		return err
	}
	if authority.Busy {
		return errFeatureAreaBusy
	}
	return nil
}

func (s *WorkflowProgressionStore) RetryTaskCommit(ctx context.Context, projectID, taskID string) (resultTask task.Task, resultErr error) {
	if current, completed, err := readCompletedTaskCommit(ctx, s.db.sql, projectID, taskID); completed || err != nil {
		return current, err
	}
	defer func() {
		if resultErr != nil {
			// Failure reporting is a mutation too. WithoutCancel retains the
			// callback fence; an obsolete worker cannot overwrite newer state.
			errorCtx := context.WithoutCancel(ctx)
			errorTx, err := s.db.beginWrite(errorCtx)
			if err != nil {
				return
			}
			defer errorTx.Rollback()
			if _, err = errorTx.ExecContext(errorCtx, `UPDATE task_commit_receipts SET last_error=? WHERE task_id=? AND completed_at IS NULL AND task_id IN (SELECT id FROM tasks WHERE project_id=?)`, resultErr.Error(), taskID, projectID); err == nil {
				_ = errorTx.Commit()
			}
		}
	}()
	unlock := lockGitCommand(s.db.stateRoot + "/task/" + taskID)
	defer unlock()
	g := git.NewClient(git.Config{})
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return task.Task{}, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, taskID, projectID))
	if err != nil {
		return t, err
	}
	// Recheck completion under the writer lock: another caller may have
	// completed the receipt since the initial read-only observation.
	var deliveryComplete bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts WHERE task_id=? AND feature_area_id=? AND completed_at IS NOT NULL)`, taskID, t.FeatureAreaID).Scan(&deliveryComplete); err != nil {
		return t, err
	}
	if deliveryComplete {
		var outcome string
		if err = tx.QueryRowContext(ctx, `SELECT terminal_outcome FROM task_commit_receipts WHERE task_id=?`, taskID).Scan(&outcome); err != nil {
			return t, err
		}
		if outcome == "ABANDONED" {
			return t, delivery.ErrAbandoned
		}
		return t, nil
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, t.FeatureAreaID); err != nil {
		return t, err
	}
	// Recovery may project saved v2 success, but must never dispatch another
	// effect. retryMultiTaskCommit enforces that distinction for every member.
	receiptOnly := t.Status == task.StatusActive && t.Phase == task.PhaseRecoveryRequired && len(t.RepositoryTargets) != 0
	if t.Type != task.TypeDevelopment || (!taskCanReceiveDeliveryAdmission(t.Status, t.Phase) && !receiptOnly) {
		return t, errors.New("TASK_NOT_ACCEPTED")
	}
	if len(t.RepositoryTargets) != 0 {
		// Multi-member Git effects occur outside a SQLite writer transaction.
		// Release this read/admission transaction before child recovery writes.
		_ = tx.Rollback()
		return s.retryMultiTaskCommit(ctx, t)
	}
	var completed sql.NullString
	var snapshot, pre, path, branch, workspace, revision string
	err = tx.QueryRowContext(ctx, `SELECT verified_snapshot,COALESCE(pre_commit_head,''),workspace_path,branch,workspace_id,revision_id,completed_at FROM task_commit_receipts WHERE task_id=?`, taskID).Scan(&snapshot, &pre, &path, &branch, &workspace, &revision, &completed)
	if err != nil {
		return t, err
	}
	if completed.Valid {
		return t, nil
	}
	dispatch := taskDeliveryDispatchInput{TaskID: t.ID, RepositoryID: t.RepositoryID, RevisionID: revision, WorkspacePath: path, Branch: branch, ExpectedHead: pre, Snapshot: snapshot, Message: taskCommitMessage(t)}
	if saved, found, e := readTaskDeliveryOutcome(ctx, tx, dispatch); e != nil || found {
		if e != nil {
			return t, e
		}
		// Exact success is historical evidence; completing its database
		// checkpoint does not require today's checkout to exist. Durable
		// acceptance/context and cancellation gates still apply.
		var current bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas a JOIN workspaces w ON w.id=a.current_workspace_id
 WHERE a.id=? AND a.project_id=? AND a.status='ACTIVE' AND w.id=? AND w.status='READY' AND w.runtime_quarantined=0)`, t.FeatureAreaID, t.ProjectID, workspace).Scan(&current)
		if e != nil {
			return t, e
		}
		if !current || revision != t.CurrentDefinitionRevisionID {
			return t, git.ErrRecoveryRequired
		}
		if e = commitBusy(ctx, tx, t.FeatureAreaID); e != nil {
			return t, e
		}
		_ = tx.Rollback()
		if e = completeTaskCommitTx(ctx, s.db, t.ProjectID, taskID, saved.Commit, time.Now().UTC()); e != nil {
			return t, e
		}
		return NewTaskStore(s.db).Get(ctx, taskID)
	}
	if err = commitBusy(ctx, tx, t.FeatureAreaID); err != nil {
		return t, err
	}
	var canonical, repoPath, life string
	err = tx.QueryRowContext(ctx, `SELECT a.current_workspace_id,r.canonical_path,a.status FROM feature_areas a JOIN workspaces w ON w.id=a.current_workspace_id JOIN workspace_members m ON m.workspace_id=w.id AND m.repository_id=? AND m.preparation='READY' JOIN worktrees wt ON wt.id=m.worktree_id AND wt.status='READY' AND wt.path=? AND wt.branch=? JOIN repositories r ON r.id=m.repository_id WHERE a.id=? AND w.runtime_quarantined=0 AND w.status='READY'`, t.RepositoryID, path, branch, t.FeatureAreaID).Scan(&canonical, &repoPath, &life)
	if errors.Is(err, sql.ErrNoRows) {
		return t, errDeliveryMemberUnavailable
	}
	if err != nil {
		return t, err
	}
	if canonical != workspace || revision != t.CurrentDefinitionRevisionID || life != "ACTIVE" {
		// A stale delivery target is a confirmed conflict, not a transient
		// block: leave the durable fast path only through RECOVERY_REQUIRED.
		if markErr := markTaskDeliveryRecovery(ctx, tx, t.ProjectID, taskID, time.Now().UTC()); markErr != nil {
			return t, markErr
		}
		if err = tx.Commit(); err != nil {
			return t, err
		}
		return t, git.ErrRecoveryRequired
	}
	if err = g.ValidateManagedWorktree(ctx, repoPath, path, branch); err != nil {
		return t, err
	}
	if pre == "" {
		if snapshot == "" {
			return t, git.ErrSnapshotDrift
		}
		if err = g.NoOperation(ctx, path); err != nil {
			return t, err
		}
		fresh, e := g.Snapshot(ctx, path)
		if e != nil {
			return t, e
		}
		if fresh != snapshot {
			// Confirmed drift of the verified snapshot forbids any further
			// blind Git effect on this receipt.
			if markErr := markTaskDeliveryRecovery(ctx, tx, t.ProjectID, taskID, time.Now().UTC()); markErr != nil {
				return t, markErr
			}
			if err = tx.Commit(); err != nil {
				return t, err
			}
			return t, git.ErrSnapshotDrift
		}
		pre, err = g.ResolveHEAD(ctx, path)
		if err != nil {
			return t, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE task_commit_receipts SET pre_commit_head=? WHERE task_id=? AND pre_commit_head IS NULL`, pre, taskID); err != nil {
			return t, err
		}
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	dispatch.ExpectedHead = pre

	invoke := func(observe bool) (taskDeliveryDispatchResult, error) {
		var commit string
		var callErr error
		if observe {
			commit, callErr = g.ObserveTask(ctx, path, pre, snapshot, t.ID, revision, dispatch.Message)
		} else {
			commit, callErr = g.FinalizeTask(ctx, path, pre, snapshot, t.ID, revision, dispatch.Message)
		}
		return taskDeliveryDispatchResult{Commit: commit, Tree: snapshot, Changed: commit != ""}, callErr
	}
	delivered, err := s.dispatchTaskDeliveryEffect(ctx, dispatch, func() (taskDeliveryDispatchResult, error) { return invoke(false) }, func() (taskDeliveryDispatchResult, error) { return invoke(true) })
	result := delivered.Commit
	if err != nil {
		_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE task_commit_receipts SET last_error=? WHERE task_id=?`, err.Error(), taskID)
		return t, err
	}
	if err = completeTaskCommitTx(ctx, s.db, t.ProjectID, taskID, result, time.Now().UTC()); err != nil {
		return t, err
	}
	return NewTaskStore(s.db).Get(ctx, taskID)
}

type taskCommitMemberRecord struct {
	repositoryID, repositoryPath, memberID, worktreeID, workspaceID, revisionID, resultID, originCommit string
	startSnapshot, verifiedSnapshot, expectedHead, commitTree, workspacePath, branch, rootIdentity      string
	changedPathsJSON, state, resultingCommit, lastError                                                 string
	memberVersion                                                                                       int
}

// retryMultiTaskCommit advances independently durable member intents. It never
// changes the real Git index and never rolls back a repository that has already
// committed. A parent receipt becomes complete only after every child reached a
// terminal state.
func (s *WorkflowProgressionStore) retryMultiTaskCommit(ctx context.Context, current task.Task) (task.Task, error) {
	receiptOnly := current.Phase == task.PhaseRecoveryRequired
	rows, err := s.db.sql.QueryContext(ctx, `SELECT m.repository_id,r.canonical_path,m.member_id,m.member_version,m.worktree_id,m.workspace_id,m.revision_id,m.result_id,m.origin_commit,m.start_snapshot,m.verified_snapshot,m.expected_head,m.commit_tree,m.workspace_path,m.branch,m.root_identity,m.changed_paths_json,m.state,m.resulting_commit,m.last_error FROM task_commit_members m JOIN repositories r ON r.id=m.repository_id WHERE m.task_id=? ORDER BY m.repository_id`, current.ID)
	if err != nil {
		return current, err
	}
	var members []taskCommitMemberRecord
	for rows.Next() {
		var m taskCommitMemberRecord
		if err = rows.Scan(&m.repositoryID, &m.repositoryPath, &m.memberID, &m.memberVersion, &m.worktreeID, &m.workspaceID, &m.revisionID, &m.resultID, &m.originCommit, &m.startSnapshot, &m.verifiedSnapshot, &m.expectedHead, &m.commitTree, &m.workspacePath, &m.branch, &m.rootIdentity, &m.changedPathsJSON, &m.state, &m.resultingCommit, &m.lastError); err != nil {
			rows.Close()
			return current, err
		}
		members = append(members, m)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return current, err
	}
	rows.Close()
	if len(members) != len(current.RepositoryTargets) {
		// A frozen member set that no longer matches the admission is a
		// confirmed integrity conflict, not a transient retry condition.
		_ = markTaskDeliveryRecovery(context.WithoutCancel(ctx), s.db.sql, current.ProjectID, current.ID, time.Now().UTC())
		return current, git.ErrRecoveryRequired
	}
	g := git.NewClient(git.Config{})
	for _, member := range members {
		if !receiptOnly && (member.state == "COMPLETED" || member.state == "NO_CHANGE") {
			continue
		}
		var paths []string
		if err = json.Unmarshal([]byte(member.changedPathsJSON), &paths); err != nil {
			_ = markTaskDeliveryRecovery(context.WithoutCancel(ctx), s.db.sql, current.ProjectID, current.ID, time.Now().UTC())
			return current, git.ErrRecoveryRequired
		}
		dispatch := taskDeliveryDispatchInput{TaskID: current.ID, RepositoryID: member.repositoryID, RevisionID: member.revisionID, WorkspacePath: member.workspacePath, Branch: member.branch, ExpectedHead: member.expectedHead, Snapshot: member.verifiedSnapshot, Message: taskCommitMessage(current), Paths: paths, Selective: true}
		commit, tree, changed, memberErr := func() (string, string, bool, error) {
			if saved, found, err := readTaskDeliveryOutcome(ctx, s.db.sql, dispatch); err != nil || found {
				return saved.Commit, saved.Tree, saved.Changed, err
			}
			if receiptOnly {
				return "", "", false, git.ErrRecoveryRequired
			}
			common, err := g.CommonDirectory(ctx, member.repositoryPath)
			if err != nil {
				return "", "", false, err
			}
			ref := "refs/heads/" + member.branch
			unlock := lockGitCommand(common + "/" + ref)
			defer unlock()
			key := sha256.Sum256([]byte(ref))
			lease, err := dataroot.AcquireWriterLock(filepath.Join(common, "devbridge-task-"+hex.EncodeToString(key[:16])+".lock"))
			if err != nil {
				return "", "", false, err
			}
			defer lease.Close()
			if err := g.ValidateManagedWorktree(ctx, member.repositoryPath, member.workspacePath, member.branch); err != nil {
				return "", "", false, err
			}
			identity, err := upperV2RootIdentity(member.workspacePath)
			if err != nil || identity != member.rootIdentity {
				return "", "", false, git.ErrRecoveryRequired
			}
			invoke := func(observe bool) (taskDeliveryDispatchResult, error) {
				var result taskDeliveryDispatchResult
				var err error
				if observe {
					result.Commit, result.Tree, result.Changed, err = g.ObserveTaskPaths(ctx, member.workspacePath, member.expectedHead, member.verifiedSnapshot, current.ID, member.revisionID, dispatch.Message, paths)
				} else {
					result.Commit, result.Tree, result.Changed, err = g.FinalizeTaskPaths(ctx, member.workspacePath, member.expectedHead, member.verifiedSnapshot, current.ID, member.revisionID, dispatch.Message, paths)
				}
				return result, err
			}
			delivered, err := s.dispatchTaskDeliveryEffect(ctx, dispatch, func() (taskDeliveryDispatchResult, error) { return invoke(false) }, func() (taskDeliveryDispatchResult, error) { return invoke(true) })
			return delivered.Commit, delivered.Tree, delivered.Changed, err
		}()
		if memberErr != nil {
			state := "UNKNOWN"
			if errors.Is(memberErr, delivery.ErrRejected) {
				state = "FAILED"
			}
			_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE task_commit_members SET state=?,last_error=? WHERE task_id=? AND repository_id=? AND state IN ('PENDING','UNKNOWN','FAILED')`, state, memberErr.Error(), current.ID, member.repositoryID)
			if errors.Is(memberErr, git.ErrRecoveryRequired) {
				// A confirmed hard failure (stale frozen root identity, etc.)
				// leaves the DELIVERING fast path in the same authoritative
				// step; transient validation errors stay retryable.
				_ = markTaskDeliveryRecovery(context.WithoutCancel(ctx), s.db.sql, current.ProjectID, current.ID, time.Now().UTC())
			}
			return current, memberErr
		}
		if s.deliveryFaultHook != nil {
			if hookErr := s.deliveryFaultHook("TASK_COMMIT_AFTER_GIT", member.repositoryID); hookErr != nil {
				return current, hookErr
			}
		}
		state, completedAt := "COMPLETED", formatTime(time.Now().UTC())
		if !changed {
			state = "NO_CHANGE"
		}
		if _, err = s.db.sql.ExecContext(ctx, `UPDATE task_commit_members SET state=?,resulting_commit=?,commit_tree=?,last_error='',completed_at=? WHERE task_id=? AND repository_id=? AND state IN ('PENDING','UNKNOWN','FAILED')`, state, commit, tree, completedAt, current.ID, member.repositoryID); err != nil {
			return current, err
		}
	}
	if err = s.completeMultiTaskDeliveryTx(ctx, current, receiptOnly); err != nil {
		return current, err
	}
	return NewTaskStore(s.db).Get(ctx, current.ID)
}

func taskCommitMessage(value task.Task) string {
	kind := strings.ToLower(string(value.WorkKind))
	if kind == "" {
		kind = "task"
	}
	title := strings.Join(strings.Fields(value.ProblemStatement), " ")
	if title == "" {
		title = strings.Join(strings.Fields(value.Goal), " ")
	}
	if len([]rune(title)) > 128 {
		title = string([]rune(title)[:128])
	}
	return kind + ": " + title
}

const incompleteFeatureAreaTasksSQL = `EXISTS(SELECT 1 FROM tasks t WHERE t.feature_area_id=? AND t.task_type='DEVELOPMENT' AND ` + deliveryRelevantTaskSQL + ` AND (NOT ` + acceptedDeliveryTaskSQL + ` OR (t.commit_sha IS NULL AND NOT EXISTS(SELECT 1 FROM task_commit_receipts r WHERE r.task_id=t.id AND r.protocol_version=2 AND r.terminal_outcome='APPLIED' AND r.completed_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=t.id AND m.state NOT IN ('COMPLETED','NO_CHANGE'))))))`

// PrepareFeatureAreaDeliveryPlan snapshots the exact per-repository merge
// inputs before a user confirms delivery. It performs read-only Git probes;
// CommitFeatureAreaPlan later refuses any substituted or newly discovered
// member instead of consulting the current Project inventory.
func (s *WorkflowProgressionStore) PrepareFeatureAreaDeliveryPlan(ctx context.Context, projectID, areaID string) (delivery.FeatureAreaPlan, error) {
	var plan delivery.FeatureAreaPlan
	var workspace string
	err := s.db.sql.QueryRowContext(ctx, `SELECT current_workspace_id FROM feature_areas WHERE id=? AND project_id=? AND status='ACTIVE'`, areaID, projectID).Scan(&workspace)
	if err != nil {
		return plan, err
	}
	plan.ProjectID, plan.FeatureAreaID, plan.WorkspaceID = projectID, areaID, workspace
	var pendingMerge bool
	if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=? AND protocol_version=2 AND completed_at IS NULL)`, areaID).Scan(&pendingMerge); err != nil {
		return plan, err
	}
	if pendingMerge {
		return plan, git.ErrRecoveryRequired
	}
	if completion, e := s.releasedTaskCompletion(ctx, projectID, areaID); e != nil {
		return plan, e
	} else if completion != nil {
		return s.PrepareTaskCompletionMergePlan(ctx, projectID, areaID, completion.Plan.TaskID)
	}

	// Plan only terminal deliveries, including scalar receipts from earlier Tasks.
	var incomplete bool
	err = s.db.sql.QueryRowContext(ctx, `SELECT `+incompleteFeatureAreaTasksSQL, areaID).Scan(&incomplete)
	if err != nil {
		return plan, err
	}
	if incomplete {
		return plan, errors.New("TASK_COMMIT_PENDING")
	}
	rows, err := s.db.sql.QueryContext(ctx, `SELECT DISTINCT m.repository_id,m.member_id,m.member_version,m.worktree_id,m.workspace_id,m.workspace_path,m.branch,r.canonical_path,c.development_ref,c.id
FROM (`+completionAcceptedDeliveryCommitsSQL+`) m JOIN tasks t ON t.id=m.task_id JOIN repositories r ON r.id=m.repository_id
JOIN development_cycles c ON c.project_id=t.project_id AND c.repository_id=m.repository_id AND c.superseded_at IS NULL
WHERE t.feature_area_id=? AND t.project_id=? AND `+acceptedDeliveryTaskSQL+` AND EXISTS(SELECT 1 FROM task_commit_receipts p WHERE p.task_id=t.id AND p.completed_at IS NOT NULL AND p.terminal_outcome='APPLIED') AND m.state='COMPLETED' AND m.resulting_commit<>''
AND `+unmergedDeliveryCommitSQL+`
ORDER BY m.repository_id`, areaID, projectID)
	if err != nil {
		return plan, err
	}
	g := git.NewClient(git.Config{})
	for rows.Next() {
		var member delivery.FeatureAreaMember
		var repositoryPath string
		if err = rows.Scan(&member.RepositoryID, &member.MemberID, &member.MemberVersion, &member.WorktreeID, &workspace, &member.SourcePath, &member.SourceBranch, &repositoryPath, &member.DevelopmentRef, &member.DevelopmentCycleID); err != nil {
			rows.Close()
			return plan, err
		}
		if workspace != plan.WorkspaceID || member.MemberID == "" || member.WorktreeID == "" {
			rows.Close()
			return plan, git.ErrRecoveryRequired
		}
		member.TargetPath, err = g.DevelopmentCheckout(ctx, repositoryPath, member.DevelopmentRef)
		if err != nil {
			rows.Close()
			return plan, err
		}
		if err = g.ValidateManagedWorktree(ctx, repositoryPath, member.SourcePath, member.SourceBranch); err != nil {
			rows.Close()
			return plan, err
		}
		member.SourceHead, err = g.ResolveHEAD(ctx, member.SourcePath)
		if err != nil {
			rows.Close()
			return plan, err
		}
		member.ExpectedDevHead, err = g.ResolveHEAD(ctx, member.TargetPath)
		if err != nil {
			rows.Close()
			return plan, err
		}
		commitRows, e := s.db.sql.QueryContext(ctx, `SELECT DISTINCT m.resulting_commit FROM (`+completionAcceptedDeliveryCommitsSQL+`) m JOIN tasks t ON t.id=m.task_id WHERE t.feature_area_id=? AND m.repository_id=? AND `+unmergedDeliveryCommitSQL+` ORDER BY m.resulting_commit`, areaID, member.RepositoryID)
		if e != nil {
			rows.Close()
			return plan, e
		}
		for commitRows.Next() {
			var sha string
			if e = commitRows.Scan(&sha); e != nil {
				commitRows.Close()
				rows.Close()
				return plan, e
			}
			member.TaskCommits = append(member.TaskCommits, sha)
		}
		e = commitRows.Err()
		commitRows.Close()
		if e != nil {
			rows.Close()
			return plan, e
		}
		allRows, e := s.db.sql.QueryContext(ctx, `SELECT DISTINCT m.resulting_commit FROM (`+completionAcceptedDeliveryCommitsSQL+`) m JOIN tasks t ON t.id=m.task_id WHERE t.feature_area_id=? AND m.repository_id=? AND m.state='COMPLETED' AND m.resulting_commit<>''`, areaID, member.RepositoryID)
		if e != nil {
			rows.Close()
			return plan, e
		}
		allowed := map[string]bool{}
		for allRows.Next() {
			var sha string
			if e = allRows.Scan(&sha); e != nil {
				allRows.Close()
				rows.Close()
				return plan, e
			}
			allowed[sha] = true
		}
		e = allRows.Err()
		allRows.Close()
		if e != nil {
			rows.Close()
			return plan, e
		}
		sourceOnly, e := g.CommitsExclusive(ctx, member.SourcePath, member.ExpectedDevHead, member.SourceHead)
		if e != nil {
			rows.Close()
			return plan, e
		}
		for _, sha := range sourceOnly {
			if !allowed[sha] {
				rows.Close()
				return plan, git.ErrRecoveryRequired
			}
		}
		if e = validateMergeCommitCoverage(ctx, g, member); e != nil {
			rows.Close()
			return plan, e
		}
		plan.Members = append(plan.Members, member)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return plan, err
	}
	if err = plan.Canonicalize(); err != nil {
		return plan, err
	}
	if !plan.Valid() {
		return delivery.FeatureAreaPlan{}, errors.New("MULTI_DELIVERY_PLAN_INVALID")
	}
	return plan, nil
}

// CommitFeatureAreaPlan applies one previously confirmed, immutable v2 plan.
// It creates every child receipt before the first Git mutation, then records
// each repository independently. A failed later member does not undo an
// earlier successful merge.
func (s *WorkflowProgressionStore) CommitFeatureAreaPlan(ctx context.Context, projectID, areaID, requestID string, plan delivery.FeatureAreaPlan) (featurearea.FeatureArea, error) {
	if strings.TrimSpace(requestID) == "" || !plan.Valid() || plan.ProjectID != projectID || plan.FeatureAreaID != areaID {
		return featurearea.FeatureArea{}, errors.New("INVALID_DELIVERY_PLAN")
	}
	unlockRequest := lockGitCommand(s.db.stateRoot + "/merge/" + requestID)
	defer unlockRequest()
	encoded, err := json.Marshal(plan)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	// Exact request replay reads the stored plan, never current mapping.
	var stored string
	var released, completed bool
	err = s.db.sql.QueryRowContext(ctx, `SELECT plan_json,released_at IS NOT NULL,completed_at IS NOT NULL FROM feature_area_commit_receipts WHERE request_id=?`, requestID).Scan(&stored, &released, &completed)
	if err == nil && released {
		return featurearea.FeatureArea{}, delivery.ErrPlanReleased
	}
	if err == nil && stored != string(encoded) {
		return featurearea.FeatureArea{}, git.ErrRecoveryRequired
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		// The initial request must carry the exact read-only plan just shown to
		// the user. A stale source/target, changed task commit, or expanded
		// workspace produces a different digest before any receipt is written.
		var fresh delivery.FeatureAreaPlan
		var e error
		if plan.CompletionTaskID != "" && plan.PredecessorMergeRequestID == "" {
			completion, readErr := s.ReadTaskCompletion(ctx, projectID, plan.CompletionTaskID)
			if readErr != nil || completion == nil || completion.MergeRequestID != requestID {
				return featurearea.FeatureArea{}, git.ErrRecoveryRequired
			}
			fresh, e = s.taskCompletionMergePlan(ctx, completion.Plan)
		} else if plan.PredecessorMergeRequestID != "" {
			fresh, e = s.PrepareTaskCompletionMergePlan(ctx, projectID, areaID, plan.CompletionTaskID)
		} else {
			fresh, e = s.PrepareFeatureAreaDeliveryPlan(ctx, projectID, areaID)
		}
		if e != nil || fresh.SHA256 != plan.SHA256 {
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
		tx, e := s.db.beginWrite(ctx)
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		defer tx.Rollback()
		var currentWorkspace string
		if e = tx.QueryRowContext(ctx, `SELECT current_workspace_id FROM feature_areas WHERE id=? AND project_id=? AND status='ACTIVE'`, areaID, projectID).Scan(&currentWorkspace); e != nil {
			return featurearea.FeatureArea{}, e
		}
		if currentWorkspace != plan.WorkspaceID {
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
		if e = directFileAdmissionTx(ctx, tx, s.db, areaID); e != nil {
			return featurearea.FeatureArea{}, e
		}
		if e = commitBusy(ctx, tx, areaID); e != nil {
			return featurearea.FeatureArea{}, e
		}
		// Verify every frozen member against durable facts; do not enumerate the
		// current Workspace, which could have acquired unrelated resources.
		for _, m := range plan.Members {
			var member, cycle string
			var version int
			e = tx.QueryRowContext(ctx, `SELECT m.id,m.identity_version,c.id FROM workspace_members m JOIN development_cycles c ON c.project_id=? AND c.repository_id=m.repository_id AND c.superseded_at IS NULL WHERE m.workspace_id=? AND m.repository_id=? AND m.worktree_id=? AND m.preparation='READY'`, projectID, plan.WorkspaceID, m.RepositoryID, m.WorktreeID).Scan(&member, &version, &cycle)
			if e != nil || member != m.MemberID || version != m.MemberVersion || cycle != m.DevelopmentCycleID {
				return featurearea.FeatureArea{}, git.ErrRecoveryRequired
			}
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO feature_area_commit_receipts(request_id,feature_area_id,repository_id,development_ref,source_head,expected_dev_head,source_path,source_branch,target_path,protocol_version,plan_sha256,plan_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, requestID, areaID, nil, "", "", "", "", "", "", 2, plan.SHA256, string(encoded))
		if e != nil {
			return featurearea.FeatureArea{}, mergeReservationError(e)
		}
		if plan.PredecessorMergeRequestID != "" {
			// The released parent's error remains in its immutable history;
			// the current completion summary now follows this confirmed plan.
			if _, e = tx.ExecContext(ctx, `UPDATE task_completion_intents SET last_error='' WHERE task_id=? AND completed_at IS NULL`, plan.CompletionTaskID); e != nil {
				return featurearea.FeatureArea{}, e
			}
		}
		for _, m := range plan.Members {
			commits, e := json.Marshal(m.TaskCommits)
			if e != nil {
				return featurearea.FeatureArea{}, e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO feature_area_commit_members(request_id,repository_id,member_id,member_version,worktree_id,source_path,source_branch,source_head,expected_dev_head,development_ref,development_cycle_id,target_path,task_commits_json,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, requestID, m.RepositoryID, m.MemberID, m.MemberVersion, m.WorktreeID, m.SourcePath, m.SourceBranch, m.SourceHead, m.ExpectedDevHead, m.DevelopmentRef, m.DevelopmentCycleID, m.TargetPath, string(commits), "PENDING")
			if e != nil {
				return featurearea.FeatureArea{}, mergeReservationError(e)
			}
		}
		for _, m := range plan.Members {
			if e = insertMergeOperation(ctx, tx, requestID, mergeInput(plan, m)); e != nil {
				return featurearea.FeatureArea{}, e
			}
		}
		if e = tx.Commit(); e != nil {
			return featurearea.FeatureArea{}, e
		}
	}
	g := git.NewClient(git.Config{})
	for _, m := range plan.Members {
		var state string
		if err = s.db.sql.QueryRowContext(ctx, `SELECT state FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, requestID, m.RepositoryID).Scan(&state); err != nil {
			return featurearea.FeatureArea{}, err
		}
		if state == "COMPLETED" || state == "NO_CHANGE" {
			continue
		}
		if completed {
			// A terminal parent with missing terminal member evidence cannot
			// authorize a new dispatch or repair itself during observation.
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
		result, memberErr := s.dispatchMerge(ctx, requestID, mergeInput(plan, m), func() (string, error) {
			common, e := g.CommonDirectory(ctx, m.TargetPath)
			if e != nil {
				return "", &git.BeforeEffectError{Err: e}
			}
			unlock := lockGitCommand(common + "/" + m.DevelopmentRef)
			defer unlock()
			key := sha256.Sum256([]byte(m.DevelopmentRef))
			lease, e := dataroot.AcquireWriterLock(filepath.Join(common, "devbridge-merge-"+hex.EncodeToString(key[:16])+".lock"))
			if e != nil {
				return "", &git.BeforeEffectError{Err: e}
			}
			defer lease.Close()
			return g.MergeFeatureAreaFrozenOnce(ctx, m.SourcePath, m.SourceBranch, m.TargetPath, m.DevelopmentRef, m.SourceHead, m.ExpectedDevHead)
		})
		if memberErr != nil {
			errorCtx := context.WithoutCancel(ctx)
			if errorTx, writeErr := s.db.beginWrite(errorCtx); writeErr == nil {
				_, writeErr = errorTx.ExecContext(errorCtx, `UPDATE feature_area_commit_members SET state='UNKNOWN',last_error=? WHERE request_id=? AND repository_id=? AND state IN ('PENDING','UNKNOWN','FAILED')`, memberErr.Error(), requestID, m.RepositoryID)
				if writeErr == nil {
					_, writeErr = errorTx.ExecContext(errorCtx, `UPDATE feature_area_commit_receipts SET last_error=? WHERE request_id=? AND completed_at IS NULL`, memberErr.Error(), requestID)
				}
				if writeErr == nil {
					_ = errorTx.Commit()
				}
				_ = errorTx.Rollback()
			}
			return featurearea.FeatureArea{}, memberErr
		}
		if s.deliveryFaultHook != nil {
			if hookErr := s.deliveryFaultHook("AREA_MERGE_AFTER_GIT", m.RepositoryID); hookErr != nil {
				return featurearea.FeatureArea{}, hookErr
			}
		}
		state = "COMPLETED"
		if result == "" {
			state = "NO_CHANGE"
		}
		if _, e := s.db.sql.ExecContext(ctx, `UPDATE feature_area_commit_members SET state=?,resulting_dev_head=?,last_error='',completed_at=? WHERE request_id=? AND repository_id=? AND state IN ('PENDING','UNKNOWN','FAILED')`, state, result, formatTime(time.Now().UTC()), requestID, m.RepositoryID); e != nil {
			return featurearea.FeatureArea{}, e
		}
	}
	var pending bool
	if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_area_commit_members WHERE request_id=? AND state NOT IN ('COMPLETED','NO_CHANGE'))`, requestID).Scan(&pending); err != nil {
		return featurearea.FeatureArea{}, err
	}
	if pending {
		return featurearea.FeatureArea{}, git.ErrRecoveryRequired
	}
	if completed {
		// The parent already records the atomic completion projection.
		// Exact replay observes history without needing current writer authority.
		if err = s.finishCompletionMergeSuccessor(ctx, projectID, requestID, plan); err != nil {
			return featurearea.FeatureArea{}, err
		}
		return NewFeatureAreaStore(s.db).Get(ctx, areaID)
	}
	if err = s.completeFeatureAreaPlan(ctx, areaID, requestID); err != nil {
		return featurearea.FeatureArea{}, err
	}
	if err = s.finishCompletionMergeSuccessor(ctx, projectID, requestID, plan); err != nil {
		return featurearea.FeatureArea{}, err
	}
	return NewFeatureAreaStore(s.db).Get(ctx, areaID)
}

func (s *WorkflowProgressionStore) CommitCapability(ctx context.Context, projectID, areaID string) (featurearea.CommitCapability, error) {
	return s.commitCapability(ctx, projectID, areaID, true)
}

// CommitInventoryCapability reads durable eligibility only. Commands still
// revalidate Git/workspace conditions through CommitCapability.
func (s *WorkflowProgressionStore) CommitInventoryCapability(ctx context.Context, projectID, areaID string) (featurearea.CommitCapability, error) {
	return s.commitCapability(ctx, projectID, areaID, false)
}

func (s *WorkflowProgressionStore) commitCapability(ctx context.Context, projectID, areaID string, inspectGit bool) (featurearea.CommitCapability, error) {
	out := featurearea.CommitCapability{CommitStatus: "COMMITTED"}
	var pending bool
	var life string
	var mergedAt sql.NullString
	err := s.db.sql.QueryRowContext(ctx, `SELECT status,COALESCE(merged_commit,''),merged_at,EXISTS(SELECT 1 FROM tasks t WHERE t.feature_area_id=feature_areas.id AND t.task_type='DEVELOPMENT' AND `+deliveryRelevantTaskSQL+` AND (t.commit_sha IS NULL OR t.merged_commit IS NULL)) FROM feature_areas WHERE id=? AND project_id=?`, areaID, projectID).Scan(&life, &out.MergedCommit, &mergedAt, &pending)
	if err != nil {
		return out, err
	}
	var multi bool
	if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts r JOIN tasks t ON t.id=r.task_id WHERE t.feature_area_id=? AND r.protocol_version=2) OR EXISTS(SELECT 1 FROM task_completion_intents c JOIN tasks t ON t.id=c.task_id WHERE t.feature_area_id=?)`, areaID, areaID).Scan(&multi); err != nil {
		return out, err
	}
	if multi {
		return s.multiCommitCapability(ctx, projectID, areaID, out)
	}
	out.MergedAt, err = parseOptionalTime(mergedAt)
	if err != nil {
		return out, err
	}
	if pending {
		out.CommitStatus = "UNCOMMITTED"
	}
	_ = s.db.sql.QueryRowContext(ctx, `SELECT id FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT' AND ((status='ACTIVE' AND phase='DELIVERING') OR (status='SUCCEEDED' AND commit_sha IS NULL)) LIMIT 1`, areaID).Scan(&out.RetryTaskID)
	_ = s.db.sql.QueryRowContext(ctx, `SELECT request_id FROM pending_feature_area_commits WHERE feature_area_id=? AND completed_at IS NULL`, areaID).Scan(&out.RequestID)
	if out.RequestID != "" {
		var applied bool
		if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM legacy_merge_outcomes WHERE request_id=?)`, out.RequestID).Scan(&applied); err != nil {
			return out, err
		}
		if !applied {
			out.MergeRecoveryState = "UNKNOWN"
			out.Reason = "RECOVERY_REQUIRED"
			return out, nil
		}
		// This command can only project saved evidence and needs no checkout.
		out.CanCommit = true
		return out, nil
	}
	if life != "ACTIVE" && life != "DISCOVERING" {
		out.Reason = "FEATURE_AREA_UNAVAILABLE"
		return out, nil
	}
	if err = directFileAdmissionTx(ctx, s.db.sql, s.db, areaID); err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	if err = commitBusy(ctx, s.db.sql, areaID); err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	if found, err := s.projectRejectedTaskCommit(ctx, projectID, areaID, &out); err != nil || found {
		return out, err
	}
	out.CanRetry = life == "ACTIVE" && out.RetryTaskID != ""
	var deliveryRecovery bool
	err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT' AND status='ACTIVE' AND phase='RECOVERY_REQUIRED' AND EXISTS(SELECT 1 FROM task_commit_receipts r WHERE r.task_id=tasks.id AND r.completed_at IS NULL))`, areaID).Scan(&deliveryRecovery)
	if err != nil {
		return out, err
	}
	if deliveryRecovery {
		// A confirmed delivery conflict is never advertised as a plain retry.
		out.Reason = "RECOVERY_REQUIRED"
		return out, nil
	}
	var blocked bool
	err = s.db.sql.QueryRowContext(ctx, `SELECT `+incompleteScalarFeatureAreaTasksSQL+` OR EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=? AND completed_at IS NULL) OR EXISTS(SELECT 1 FROM review_approval_decisions d WHERE d.feature_area_id=? AND NOT EXISTS(SELECT 1 FROM decision_resolution_receipts r WHERE r.decision_id=d.decision_id))`, areaID, areaID, areaID).Scan(&blocked)
	if err != nil {
		return out, err
	}
	if blocked {
		out.Reason = "TASK_COMMIT_PENDING"
		return out, nil
	}
	var repo, ref string
	repo, err = featureAreaDeliveryRepository(ctx, s.db.sql, areaID)
	if err != nil {
		out.CanRetry = false
		out.Reason = err.Error()
		return out, nil
	}
	if inspectGit {
		var sourcePath, targetPath string
		repo, sourcePath, _, targetPath, ref, err = s.commitWorkspace(ctx, projectID, areaID)
		if err != nil {
			out.Reason = err.Error()
			return out, nil
		}
		g := git.NewClient(git.Config{})
		for _, path := range []string{sourcePath, targetPath} {
			if err = g.NoOperation(ctx, path); err != nil {
				out.Reason = err.Error()
				return out, nil
			}
			if err = g.RequireClean(ctx, path); err != nil {
				out.Reason = err.Error()
				return out, nil
			}
		}
	} else {
		// Durable availability only; neither development-checkout discovery nor
		// managed-worktree validation belongs in Project inventory.
		err = s.db.sql.QueryRowContext(ctx, `SELECT r.id,c.development_ref FROM feature_areas a JOIN workspaces w ON w.id=a.current_workspace_id JOIN workspace_members m ON m.workspace_id=w.id AND m.repository_id=? AND m.preparation='READY' JOIN worktrees wt ON wt.id=m.worktree_id AND wt.status='READY' JOIN repositories r ON r.id=m.repository_id JOIN development_cycles c ON c.project_id=a.project_id AND c.repository_id=r.id AND c.superseded_at IS NULL WHERE a.id=? AND a.project_id=? AND w.runtime_quarantined=0 AND w.status='READY'`, repo, areaID, projectID).Scan(&repo, &ref)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return out, err
			}
			out.Reason = "COMMIT_WORKSPACE_UNAVAILABLE"
			return out, nil
		}
	}

	var repositoryPending bool
	if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pending_feature_area_commits p WHERE p.feature_area_id<>? AND ((p.repository_id=? AND p.development_ref=?) OR EXISTS(SELECT 1 FROM feature_area_commit_members m WHERE m.request_id=p.request_id AND m.repository_id=? AND m.development_ref=?)))`, areaID, repo, ref, repo, ref).Scan(&repositoryPending); err != nil {
		return out, err
	}
	if repositoryPending {
		out.Reason = "GIT_FINALIZATION_PENDING"
		return out, nil
	}
	out.DevelopmentBranch = strings.TrimPrefix(ref, "refs/heads/")
	out.CanCommit = pending || out.RequestID != ""
	return out, nil
}

func (s *WorkflowProgressionStore) multiCommitCapability(ctx context.Context, projectID, areaID string, out featurearea.CommitCapability) (featurearea.CommitCapability, error) {
	var life string
	if err := s.db.sql.QueryRowContext(ctx, `SELECT status FROM feature_areas WHERE id=? AND project_id=?`, areaID, projectID).Scan(&life); err != nil {
		return out, err
	}
	if life != "ACTIVE" && life != "DISCOVERING" {
		out.Reason = "FEATURE_AREA_UNAVAILABLE"
		return out, nil
	}
	var taskPending, mergePending, mergeNeeded bool
	err := s.db.sql.QueryRowContext(ctx, `SELECT
 `+incompleteFeatureAreaTasksSQL+`,
 EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=? AND protocol_version=2 AND completed_at IS NULL),
 EXISTS(SELECT 1 FROM (`+completionAcceptedDeliveryCommitsSQL+`) m JOIN tasks t ON t.id=m.task_id WHERE t.feature_area_id=? AND `+unmergedDeliveryCommitSQL+`)`, areaID, areaID, areaID).Scan(&taskPending, &mergePending, &mergeNeeded)
	if err != nil {
		return out, err
	}
	out.CommitStatus = "COMMITTED"
	if taskPending || mergePending || mergeNeeded {
		out.CommitStatus = "UNCOMMITTED"
	}
	if mergePending {
		_ = s.db.sql.QueryRowContext(ctx, `SELECT request_id FROM pending_feature_area_commits WHERE feature_area_id=? AND protocol_version=2 AND completed_at IS NULL ORDER BY request_id LIMIT 1`, areaID).Scan(&out.RequestID)
		out.Reason = "RECOVERY_REQUIRED"
		candidates, candidateErr := s.FeatureAreaMergeRetryCandidates(ctx, projectID, areaID)
		if candidateErr != nil {
			return out, candidateErr
		}
		// A missing operation or dispatched operation without an outcome is
		// UNKNOWN, even if another repository has a positive retry receipt.
		var unknown bool
		if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM feature_area_commit_members m
 LEFT JOIN merge_delivery_current_operations c ON c.merge_request_id=m.request_id AND c.repository_id=m.repository_id
 LEFT JOIN merge_delivery_dispatches d ON d.operation_id=c.operation_id
 LEFT JOIN merge_delivery_outcomes o ON o.operation_id=c.operation_id
 WHERE m.request_id=? AND m.state NOT IN ('COMPLETED','NO_CHANGE')
 AND (c.operation_id IS NULL OR (d.operation_id IS NOT NULL AND o.operation_id IS NULL)))`, out.RequestID).Scan(&unknown); err != nil {
			return out, err
		}
		out.MergeRecoveryState = "UNKNOWN"
		if unknown {
			return out, nil
		}
		if len(candidates) > 0 {
			out.Reason = delivery.ErrRejected.Error()
			out.MergeRecoveryState = "REPREPARE_REQUIRED"
			out.MergeRetryOperation = &candidates[0]
		} else {
			out.MergeRecoveryState = "RESUMABLE"
			out.CanRecoverMerge = true
		}
		return out, nil
	}
	if found, err := s.projectRejectedTaskCommit(ctx, projectID, areaID, &out); err != nil || found {
		return out, err
	}
	var deliveryRecovery bool
	if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT' AND status='ACTIVE' AND phase='RECOVERY_REQUIRED' AND EXISTS(SELECT 1 FROM task_commit_receipts r WHERE r.task_id=tasks.id AND r.completed_at IS NULL))`, areaID).Scan(&deliveryRecovery); err != nil {
		return out, err
	}
	if deliveryRecovery {
		// A recovery Task can expose database-only completion when every member
		// already has exact success. The command rechecks payload and identity.
		e := s.db.sql.QueryRowContext(ctx, `SELECT t.id FROM tasks t JOIN task_commit_receipts q ON q.task_id=t.id
 WHERE t.feature_area_id=? AND t.project_id=? AND t.status='ACTIVE' AND t.phase='RECOVERY_REQUIRED'
 AND q.protocol_version=2 AND q.completed_at IS NULL
 AND EXISTS(SELECT 1 FROM task_commit_members m WHERE m.task_id=t.id)
 AND NOT EXISTS(SELECT 1 FROM task_commit_members m LEFT JOIN task_delivery_current_operations c ON c.task_id=m.task_id AND c.repository_id=m.repository_id
 LEFT JOIN task_delivery_operation_outcomes r ON r.operation_id=c.operation_id WHERE m.task_id=t.id AND (r.outcome IS NULL OR r.outcome<>'APPLIED'))
 ORDER BY t.id LIMIT 1`, areaID, projectID).Scan(&out.RetryTaskID)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		out.CanRetry = e == nil
		out.Reason = "RECOVERY_REQUIRED"
		return out, nil
	}
	if taskPending {
		_ = s.db.sql.QueryRowContext(ctx, `SELECT t.id FROM tasks t WHERE t.feature_area_id=? AND t.task_type='DEVELOPMENT' AND ((t.status='ACTIVE' AND t.phase='DELIVERING') OR (t.status='SUCCEEDED' AND t.commit_sha IS NULL)) AND NOT EXISTS(SELECT 1 FROM task_commit_receipts r WHERE r.task_id=t.id AND r.protocol_version=2 AND r.completed_at IS NOT NULL) LIMIT 1`, areaID).Scan(&out.RetryTaskID)
		out.CanRetry = out.RetryTaskID != ""
		out.Reason = "TASK_COMMIT_PENDING"
		return out, nil
	}
	if mergeNeeded {
		// Mirror the durable target reservation in both detail and inventory
		// capabilities. Only this area's unmerged repositories can block it.
		var targetBusy bool
		err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM (`+completionAcceptedDeliveryCommitsSQL+`) m JOIN tasks t ON t.id=m.task_id
 JOIN development_cycles c ON c.project_id=t.project_id AND c.repository_id=m.repository_id AND c.superseded_at IS NULL
 WHERE t.feature_area_id=? AND t.project_id=? AND `+unmergedDeliveryCommitSQL+`
 AND EXISTS(SELECT 1 FROM pending_feature_area_commits p WHERE p.feature_area_id<>t.feature_area_id AND (
 (p.protocol_version=1 AND p.repository_id=m.repository_id AND p.development_ref=c.development_ref)
 OR EXISTS(SELECT 1 FROM feature_area_commit_members busy WHERE busy.request_id=p.request_id AND busy.repository_id=m.repository_id AND busy.development_ref=c.development_ref))))`, areaID, projectID).Scan(&targetBusy)
		if err != nil {
			return out, err
		}
		if targetBusy {
			out.Reason = "GIT_FINALIZATION_PENDING"
			return out, nil
		}
		out.CanCommit = true
	}
	return out, nil
}

func (s *WorkflowProgressionStore) commitWorkspace(ctx context.Context, projectID, areaID string) (repo, source, branch, target, ref string, err error) {
	var repoPath string
	repo, err = featureAreaDeliveryRepository(ctx, s.db.sql, areaID)
	if err != nil {
		return
	}
	err = s.db.sql.QueryRowContext(ctx, `SELECT r.id,r.canonical_path,wt.path,wt.branch,c.development_ref FROM feature_areas a JOIN workspaces w ON w.id=a.current_workspace_id JOIN workspace_members m ON m.workspace_id=w.id AND m.repository_id=? AND m.preparation='READY' JOIN worktrees wt ON wt.id=m.worktree_id AND wt.status='READY' JOIN repositories r ON r.id=m.repository_id JOIN development_cycles c ON c.project_id=a.project_id AND c.repository_id=r.id AND c.superseded_at IS NULL WHERE a.id=? AND a.project_id=? AND w.runtime_quarantined=0 AND w.status='READY'`, repo, areaID, projectID).Scan(&repo, &repoPath, &source, &branch, &ref)
	if errors.Is(err, sql.ErrNoRows) {
		err = errDeliveryMemberUnavailable
		return
	}
	if err != nil {
		return
	}
	g := git.NewClient(git.Config{})
	target, err = g.DevelopmentCheckout(ctx, repoPath, ref)
	if err != nil {
		return
	}
	err = g.ValidateManagedWorktree(ctx, target, source, branch)
	if err != nil {
		return
	}

	return
}

func (s *WorkflowProgressionStore) CommitFeatureArea(ctx context.Context, projectID, areaID, requestID string) (featurearea.FeatureArea, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 128 {
		return featurearea.FeatureArea{}, errors.New("INVALID_REQUEST_ID")
	}
	unlockArea := lockGitCommand(s.db.stateRoot + "/area/" + areaID)
	defer unlockArea()
	// Public submit surfaces share the v2 planner and durable dispatcher.
	// Replay resolves the saved request before consulting current Task inventory;
	// it cannot acquire a new plan or fall through to legacy Git recovery.
	var protocol int
	var planJSON, savedArea, savedProject string
	err := s.db.sql.QueryRowContext(ctx, `SELECT r.protocol_version,r.plan_json,r.feature_area_id,a.project_id FROM feature_area_commit_receipts r JOIN feature_areas a ON a.id=r.feature_area_id WHERE r.request_id=?`, requestID).Scan(&protocol, &planJSON, &savedArea, &savedProject)
	if err == nil {
		if savedArea != areaID || savedProject != projectID {
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
		if protocol == 2 {
			var plan delivery.FeatureAreaPlan
			if json.Unmarshal([]byte(planJSON), &plan) != nil {
				return featurearea.FeatureArea{}, git.ErrRecoveryRequired
			}
			return s.CommitFeatureAreaPlan(ctx, projectID, areaID, requestID, plan)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, err
	} else {
		var multi bool
		if err = s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts r JOIN tasks t ON t.id=r.task_id WHERE t.feature_area_id=? AND t.project_id=? AND r.protocol_version=2) OR EXISTS(SELECT 1 FROM task_completion_intents c JOIN tasks t ON t.id=c.task_id WHERE t.feature_area_id=? AND t.project_id=?)`, areaID, projectID, areaID, projectID).Scan(&multi); err != nil {
			return featurearea.FeatureArea{}, err
		}
		if multi {
			plan, e := s.PrepareFeatureAreaDeliveryPlan(ctx, projectID, areaID)
			if e != nil {
				return featurearea.FeatureArea{}, e
			}
			return s.CommitFeatureAreaPlan(ctx, projectID, areaID, requestID, plan)
		}
	}
	var durableArea, durableResult, durableError string
	var durableCompleted sql.NullString
	replayErr := s.db.sql.QueryRowContext(ctx, `SELECT r.feature_area_id,COALESCE(r.resulting_dev_head,''),COALESCE(r.last_error,''),r.completed_at FROM feature_area_commit_receipts r JOIN feature_areas a ON a.id=r.feature_area_id WHERE r.request_id=? AND a.project_id=?`, requestID, projectID).Scan(&durableArea, &durableResult, &durableError, &durableCompleted)
	if replayErr == nil {
		if durableArea != areaID {
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
		if durableCompleted.Valid {
			if durableError != "" {
				return featurearea.FeatureArea{}, errors.New(durableError)
			}
			area, e := NewFeatureAreaStore(s.db).Get(ctx, areaID)
			if e != nil {
				return area, e
			}
			area.MergedCommit = durableResult
			area.MergedAt, e = parseOptionalTime(durableCompleted)
			return area, e
		}

		// Reservation is the at-most-once boundary, including historical v1
		// requests. Only an exact durable success permits database projection.
		var result string
		e := s.db.sql.QueryRowContext(ctx, `SELECT resulting_dev_head FROM legacy_merge_outcomes WHERE request_id=?`, requestID).Scan(&result)
		if errors.Is(e, sql.ErrNoRows) {
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		return s.finalizeLegacyMerge(ctx, areaID, requestID, result)
	} else if !errors.Is(replayErr, sql.ErrNoRows) {
		return featurearea.FeatureArea{}, replayErr
	}
	// Resolve the target before locking; revalidate all facts while holding lock.
	repo, sourcePath, sourceBranch, targetPath, ref, err := s.commitWorkspace(ctx, projectID, areaID)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	g := git.NewClient(git.Config{})
	common, err := g.CommonDirectory(ctx, targetPath)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	unlockRepo := lockGitCommand(common + "/" + ref)
	defer unlockRepo()
	lockKey := sha256.Sum256([]byte(ref))
	lease, err := dataroot.AcquireWriterLock(filepath.Join(common, "devbridge-submit-"+hex.EncodeToString(lockKey[:16])+".lock"))
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	defer lease.Close()

	var source, expected string
	{
		cap, e := s.CommitCapability(ctx, projectID, areaID)
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		if cap.RequestID != "" {
			return featurearea.FeatureArea{}, git.ErrRecoveryRequired
		}
		if cap.Reason == developmentcycle.ErrFinalizationPending.Error() {
			return featurearea.FeatureArea{}, developmentcycle.ErrFinalizationPending
		}
		if cap.Reason != "" {
			return featurearea.FeatureArea{}, errors.New("FEATURE_AREA_COMMIT_BLOCKED: " + cap.Reason)
		}
		source, err = g.ResolveHEAD(ctx, sourcePath)
		if err != nil {
			return featurearea.FeatureArea{}, err
		}
		expected, err = g.ResolveHEAD(ctx, targetPath)
		if err != nil {
			return featurearea.FeatureArea{}, err
		}
		tx, e := s.db.beginWrite(ctx)
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		defer tx.Rollback()
		if e = directFileAdmissionTx(ctx, tx, s.db, areaID); e != nil {
			return featurearea.FeatureArea{}, e
		}
		if e = commitBusy(ctx, tx, areaID); e != nil {
			return featurearea.FeatureArea{}, e
		}
		var blocked bool
		e = tx.QueryRowContext(ctx, `SELECT `+incompleteScalarFeatureAreaTasksSQL+` OR EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=? AND completed_at IS NULL) OR NOT EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND status IN ('ACTIVE','DISCOVERING')) OR EXISTS(SELECT 1 FROM review_approval_decisions d WHERE d.feature_area_id=? AND NOT EXISTS(SELECT 1 FROM decision_resolution_receipts r WHERE r.decision_id=d.decision_id))`, areaID, areaID, areaID, areaID).Scan(&blocked)
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		if blocked {
			return featurearea.FeatureArea{}, errors.New("TASK_COMMIT_PENDING")
		}
		rows, e := tx.QueryContext(ctx, `SELECT commit_sha FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT' AND commit_sha IS NOT NULL AND merged_commit IS NULL`, areaID)
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		var commits []string
		for rows.Next() {
			var sha string
			if e = rows.Scan(&sha); e != nil {
				rows.Close()
				return featurearea.FeatureArea{}, e
			}
			commits = append(commits, sha)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return featurearea.FeatureArea{}, e
		}
		for _, sha := range commits {
			included, e := g.IsAncestor(ctx, sourcePath, sha, source)
			if e != nil {
				return featurearea.FeatureArea{}, e
			}
			if !included {
				return featurearea.FeatureArea{}, git.ErrRecoveryRequired
			}
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO feature_area_commit_receipts(request_id,feature_area_id,repository_id,development_ref,source_head,expected_dev_head,source_path,source_branch,target_path) VALUES(?,?,?,?,?,?,?,?,?)`, requestID, areaID, repo, ref, source, expected, sourcePath, sourceBranch, targetPath)
		if e != nil {
			return featurearea.FeatureArea{}, mergeReservationError(e)
		}
		if e = tx.Commit(); e != nil {
			return featurearea.FeatureArea{}, e
		}
	}
	result, err := g.MergeFeatureAreaOnce(ctx, sourcePath, sourceBranch, targetPath, ref, source, expected)
	if err != nil {
		if errors.Is(err, git.ErrMergeConflict) {
			_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE feature_area_commit_receipts SET last_error=?,completed_at=? WHERE request_id=?`, err.Error(), formatTime(time.Now().UTC()), requestID)
			return featurearea.FeatureArea{}, err
		}
		_, _ = s.db.sql.ExecContext(context.WithoutCancel(ctx), `UPDATE feature_area_commit_receipts SET last_error=? WHERE request_id=?`, err.Error(), requestID)
		return featurearea.FeatureArea{}, err
	}
	if s.deliveryFaultHook != nil {
		if err = s.deliveryFaultHook("LEGACY_AREA_MERGE_BEFORE_RESULT", repo); err != nil {
			return featurearea.FeatureArea{}, err
		}
	}
	// Commit evidence separately: a projection failure must not lose proof or
	// authorize a second Git effect, even after a cold reopen or branch reset.
	if _, err = s.db.sql.ExecContext(context.WithoutCancel(ctx), `INSERT INTO legacy_merge_outcomes(request_id,resulting_dev_head,created_at) VALUES(?,?,?)`, requestID, result, formatTime(time.Now().UTC())); err != nil {
		return featurearea.FeatureArea{}, err
	}
	return s.finalizeLegacyMerge(ctx, areaID, requestID, result)
}

func (s *WorkflowProgressionStore) finalizeLegacyMerge(ctx context.Context, areaID, requestID, result string) (featurearea.FeatureArea, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	if _, err = tx.ExecContext(ctx, `UPDATE feature_areas SET merged_commit=?,merged_at=? WHERE id=?`, result, now, areaID); err != nil {
		return featurearea.FeatureArea{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET merged_commit=?,merged_at=? WHERE feature_area_id=? AND task_type='DEVELOPMENT' AND commit_sha IS NOT NULL AND merged_commit IS NULL`, result, now, areaID); err != nil {
		return featurearea.FeatureArea{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE feature_area_commit_receipts SET resulting_dev_head=?,completed_at=?,last_error=NULL WHERE request_id=?`, result, now, requestID); err != nil {
		return featurearea.FeatureArea{}, err
	}
	if err = tx.Commit(); err != nil {
		return featurearea.FeatureArea{}, err
	}
	return NewFeatureAreaStore(s.db).Get(ctx, areaID)
}

// A known refusal requires new successor authority, regardless of receipt
// protocol. UNKNOWN operations are deliberately absent from this projection.
func (s *WorkflowProgressionStore) projectRejectedTaskCommit(ctx context.Context, projectID, areaID string, out *featurearea.CommitCapability) (bool, error) {
	var retry delivery.RetryCandidate
	err := s.db.sql.QueryRowContext(ctx, `SELECT t.id,o.repository_id,o.operation_id,r.error FROM tasks t JOIN task_delivery_current_operations c ON c.task_id=t.id JOIN task_delivery_operations o ON o.operation_id=c.operation_id JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id JOIN task_commit_receipts q ON q.task_id=t.id WHERE t.feature_area_id=? AND t.project_id=? AND t.status='ACTIVE' AND q.completed_at IS NULL AND r.outcome='REJECTED_NOT_CONSUMED' AND r.before_external_effect=1 ORDER BY t.id,o.repository_id LIMIT 1`, areaID, projectID).Scan(&out.RetryTaskID, &retry.RepositoryID, &retry.OperationID, &retry.Error)
	if err == nil {
		out.CanRetry = true
		out.RetryOperation = &retry
		out.Reason = delivery.ErrRejected.Error()
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return false, nil
}

// Translate this reservation guard at its command boundary, without exposing
// driver diagnostics or reclassifying unrelated storage failures.
func mergeReservationError(err error) error {
	if err != nil && strings.Contains(err.Error(), "GIT_FINALIZATION_PENDING") {
		return developmentcycle.ErrFinalizationPending
	}
	return err
}
