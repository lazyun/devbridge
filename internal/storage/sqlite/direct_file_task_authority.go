package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

type taskFileAuthorization struct {
	directfile.State
	caller                                    sql.NullString
	canonical, rootIdentity, registrationKind string
}

const taskFileSelect = `SELECT workflow_id,ai_session_id,caller_scope,project_id,feature_area_id,task_id,task_definition_revision_id,decision_id,COALESCE(repository_id,''),COALESCE(workspace_id,''),COALESCE(worktree_id,''),COALESCE(canonical_path,''),COALESCE(root_identity,''),execution_scope_json,state,failure_code,failure_reason,started_at,last_operation,registration_kind FROM direct_file_task_authorizations`

func readTaskFileAuthorization(ctx context.Context, q definitionQuerier, condition string, args ...any) (v taskFileAuthorization, err error) {
	var scope string
	var started sql.NullString
	err = q.QueryRowContext(ctx, taskFileSelect+condition, args...).Scan(&v.WorkflowID, &v.AISessionID, &v.caller, &v.ProjectID, &v.FeatureAreaID, &v.TaskID, &v.TaskDefinitionRevisionID, &v.DecisionID, &v.RepositoryID, &v.WorkspaceID, &v.WorktreeID, &v.canonical, &v.rootIdentity, &scope, &v.State.State, &v.FailureCode, &v.FailureReason, &started, &v.LastOperation, &v.registrationKind)
	if err != nil {
		return
	}
	err = json.Unmarshal([]byte(scope), &v.ExecutionScope)
	if err != nil {
		return
	}
	v.StartedAt, err = parseOptionalTime(started)
	if err != nil {
		return
	}
	// v76 fields are deliberately read separately so the legacy projection and
	// its historical scalar semantics remain byte-for-byte stable.
	var targets string
	if err = q.QueryRowContext(ctx, `SELECT protocol_version,repository_targets_json,runtime_epoch,generation FROM direct_file_task_authorizations WHERE workflow_id=?`, v.WorkflowID).Scan(&v.ProtocolVersion, &targets, &v.RuntimeEpoch, &v.GrantGeneration); err != nil {
		return
	}
	if v.ProtocolVersion == 2 {
		if err = json.Unmarshal([]byte(targets), &v.RepositoryTargets); err != nil {
			return
		}
		if len(v.RepositoryTargets) == 0 || v.RepositoryID != "" || v.WorktreeID != "" {
			err = directFileError(directfile.NotEnabled, "v2 direct-file authority has scalar root")
			return
		}
	}
	v.Enabled = v.State.State == "ACTIVE"
	v.AllowedOperations = []string{}
	if v.Enabled {
		v.AllowedOperations = []string{"LIST", "READ", "CREATE", "REPLACE", "DELETE", "MOVE"}
	}
	return
}

// readTaskFileMembers reads the durable v2 authority, never current workspace
// membership.  The workspace can gain unrelated members after approval.
func readTaskFileMembers(ctx context.Context, q definitionQuerier, workflowID string) ([]directfile.MemberGrant, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_group_array(json_object('repositoryId',repository_id,'memberId',member_id,'memberVersion',member_version,'target',json(target_json),'workspaceId',workspace_id,'worktreeId',COALESCE(worktree_id,''),'state',preparation,'startHead',start_head,'startSnapshot',start_snapshot,'failure',failure)),'[]') FROM (SELECT * FROM direct_file_task_members WHERE workflow_id=? ORDER BY repository_id)`, workflowID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var encoded []struct {
		RepositoryID  string         `json:"repositoryId"`
		MemberID      string         `json:"memberId"`
		MemberVersion int            `json:"memberVersion"`
		Target        workset.Target `json:"target"`
		WorkspaceID   string         `json:"workspaceId"`
		WorktreeID    string         `json:"worktreeId"`
		State         string         `json:"state"`
		StartHead     string         `json:"startHead"`
		StartSnapshot string         `json:"startSnapshot"`
		Failure       string         `json:"failure"`
	}
	if err = json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, err
	}
	out := make([]directfile.MemberGrant, 0, len(encoded))
	for _, value := range encoded {
		m := directfile.MemberGrant{Target: value.Target, WorkspaceID: value.WorkspaceID, WorktreeID: value.WorktreeID, State: value.State, StartHead: value.StartHead, StartSnapshot: value.StartSnapshot, Failure: value.Failure}
		if m.Target.RepositoryID != value.RepositoryID || m.Target.MemberID != value.MemberID || m.Target.MemberVersion != value.MemberVersion {
			return nil, directFileError(directfile.NotEnabled, "stored member authority is inconsistent")
		}
		out = append(out, m)
	}
	return out, nil
}

func taskFileCaller(ctx context.Context, q definitionQuerier, session string) (project string, caller sql.NullString, err error) {
	err = q.QueryRowContext(ctx, `SELECT project_id,caller_scope FROM ai_sessions WHERE id=?`, session).Scan(&project, &caller)
	if err != nil {
		return
	}
	if caller.Valid {
		actual, ok := aisession.CallerScopeFromContext(ctx)
		if !ok || actual != caller.String {
			err = directFileError(directfile.NotEnabled, "authenticated caller does not own this Session")
		}
	}
	return
}

func validateTaskFileAuthorization(ctx context.Context, q definitionQuerier, v taskFileAuthorization) error {
	project, caller, err := taskFileCaller(ctx, q, v.AISessionID)
	if err != nil {
		return err
	}
	if project != v.ProjectID || caller != v.caller {
		return directFileError(directfile.NotEnabled, "Session authorization changed")
	}
	var valid bool
	if v.ProtocolVersion == 2 {
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN feature_areas a ON a.id=t.feature_area_id JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id JOIN review_approval_materializations m ON m.decision_id=? JOIN decision_resolution_receipts r ON r.decision_id=m.decision_id WHERE t.id=? AND t.project_id=? AND t.feature_area_id=? AND t.repository_id IS NULL AND t.current_definition_revision_id=? AND d.task_id=t.id AND t.status='ACTIVE' AND t.phase IN ('IMPLEMENTATION_READY','IMPLEMENTING') AND t.legacy_read_only=0 AND a.legacy_read_only=0 AND a.status IN ('DISCOVERING','ACTIVE') AND m.task_id=t.id AND m.decision_id=t.review_approval_id AND m.initial_revision_id=d.id AND m.ai_session_id=? AND r.option='APPROVE' AND r.decision_kind='REVIEW_APPROVAL' AND r.subject_kind='TASK_REVIEW_GATE' AND r.subject_id=t.id AND r.result_id=t.id AND r.project_id=t.project_id)`, v.DecisionID, v.TaskID, v.ProjectID, v.FeatureAreaID, v.TaskDefinitionRevisionID, v.AISessionID).Scan(&valid)
	} else {
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN feature_areas a ON a.id=t.feature_area_id JOIN task_definition_revisions d ON d.id=t.current_definition_revision_id JOIN review_approval_materializations m ON m.decision_id=? JOIN decision_resolution_receipts r ON r.decision_id=m.decision_id WHERE t.id=? AND t.project_id=? AND t.feature_area_id=? AND t.repository_id=? AND t.current_definition_revision_id=? AND d.task_id=t.id AND t.status='ACTIVE' AND t.phase IN ('IMPLEMENTATION_READY','IMPLEMENTING') AND t.legacy_read_only=0 AND a.legacy_read_only=0 AND a.status IN ('DISCOVERING','ACTIVE') AND m.task_id=t.id AND m.decision_id=t.review_approval_id AND EXISTS(SELECT 1 FROM review_approval_decisions ad WHERE ad.decision_id=m.decision_id AND ad.task_id=t.id AND ad.review_id=t.current_review_id AND ad.project_id=t.project_id) AND m.initial_revision_id=d.id AND m.ai_session_id=? AND r.option='APPROVE' AND r.decision_kind='REVIEW_APPROVAL' AND r.subject_kind='TASK_REVIEW_GATE' AND r.subject_id=t.id AND r.result_id=t.id AND r.project_id=t.project_id)`, v.DecisionID, v.TaskID, v.ProjectID, v.FeatureAreaID, v.RepositoryID, v.TaskDefinitionRevisionID, v.AISessionID).Scan(&valid)
	}
	if err != nil {
		return err
	}
	if !valid {
		return directFileError(directfile.NotEnabled, "approved Task identity or lifecycle changed")
	}
	authority, err := readRuntimeAuthority(ctx, q, v.FeatureAreaID)
	if err != nil {
		return err
	}
	if authority.Busy {
		return directFileError(directfile.CodexActive, "another runtime owner or unknown operation blocks file access")
	}
	var pending bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=? AND completed_at IS NULL) OR EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=? AND completed_at IS NULL)`, v.FeatureAreaID, v.FeatureAreaID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return directFileError(directfile.CodexActive, "Git finalization is in progress")
	}
	return nil
}

func taskFileRootIdentity(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", directFileError(directfile.WorktreeUnavailable, "filesystem identity is unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

// Task grants resolve the exact approved repository's current area workspace.
// A development branch is valid here; the registered checkout and aliases are not.
func taskFileWorkspace(ctx context.Context, q definitionQuerier, v taskFileAuthorization) (workspace, worktree, canonical, identity string, err error) {
	var path, repositoryPath string
	err = q.QueryRowContext(ctx, `SELECT w.id,wt.id,wt.path,r.canonical_path FROM feature_areas a JOIN workspaces w ON w.id=a.current_workspace_id AND w.feature_area_id=a.id AND w.project_id=a.project_id AND w.status='READY' AND w.runtime_quarantined=0 JOIN workspace_members wm ON wm.workspace_id=w.id JOIN worktrees wt ON wt.id=wm.worktree_id AND wt.repository_id=wm.repository_id AND wt.status='READY' JOIN repositories r ON r.id=wm.repository_id AND r.project_id=a.project_id WHERE a.id=? AND a.project_id=? AND wm.repository_id=? AND wm.preparation='READY'`, v.FeatureAreaID, v.ProjectID, v.RepositoryID).Scan(&workspace, &worktree, &path, &repositoryPath)
	if errors.Is(err, sql.ErrNoRows) {
		var preparationAllowed bool
		e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas a LEFT JOIN workspaces w ON w.id=a.current_workspace_id WHERE a.id=? AND a.project_id=? AND a.status IN ('DISCOVERING','ACTIVE') AND (a.current_workspace_id IS NULL OR (w.project_id=a.project_id AND w.feature_area_id=a.id AND w.repository_id=? AND w.runtime_quarantined=0 AND w.status='READY' AND w.worktree_id IS NULL)))`, v.FeatureAreaID, v.ProjectID, v.RepositoryID).Scan(&preparationAllowed)
		if e != nil {
			err = e
			return
		}
		if preparationAllowed {
			err = directFileError("DIRECT_FILE_WORKSPACE_PREPARATION_REQUIRED", "approved file workspace requires technical preparation")
		} else {
			err = directFileError(directfile.WorktreeUnavailable, "approved workspace is unavailable or has an identity/quarantine conflict")
		}
		return
	}
	if err != nil {
		return
	}
	canonical, err = filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) {
		err = directFileError(directfile.WorktreeUnavailable, "cannot canonicalize approved worktree")
		return
	}
	repo, e := filepath.EvalSymlinks(repositoryPath)
	if e != nil || repo == canonical {
		err = directFileError(directfile.WorktreeUnavailable, "registered checkout is not an implementation worktree")
		return
	}
	info, e := os.Stat(canonical)
	if e != nil || !info.IsDir() {
		err = directFileError(directfile.WorktreeUnavailable, "approved worktree is unavailable")
		return
	}
	var aliasJSON string
	err = q.QueryRowContext(ctx, `SELECT json_group_array(path) FROM (SELECT canonical_path AS path FROM repositories UNION ALL SELECT path FROM worktrees WHERE id<>? AND status='READY')`, worktree).Scan(&aliasJSON)
	if err != nil {
		return
	}
	var aliases []string
	if err = json.Unmarshal([]byte(aliasJSON), &aliases); err != nil {
		return
	}
	for _, other := range aliases {
		otherInfo, e := os.Stat(other)
		if e == nil && os.SameFile(info, otherInfo) {
			err = directFileError(directfile.WorktreeUnavailable, "worktree aliases another registered filesystem root")
			return
		}
	}
	identity, err = taskFileRootIdentity(info)
	return
}

// taskFileMemberWorkspace resolves exactly one durable v2 member. It is
// intentionally anchored at direct_file_task_members instead of the mutable
// workspace member list.
func taskFileMemberWorkspace(ctx context.Context, q definitionQuerier, workflowID string, target workset.Target) (directfile.MemberResult, error) {
	var out directfile.MemberResult
	var path, root, startHead, startSnapshot, state string
	err := q.QueryRowContext(ctx, `SELECT m.workspace_id,m.worktree_id,wt.path,wt.branch,m.canonical_path,m.root_identity,m.start_head,m.start_snapshot,m.preparation,m.origin_commit
FROM direct_file_task_members m JOIN worktrees wt ON wt.id=m.worktree_id AND wt.repository_id=m.repository_id AND wt.status='READY'
WHERE m.workflow_id=? AND m.repository_id=? AND m.member_id=? AND m.member_version=?`, workflowID, target.RepositoryID, target.MemberID, target.MemberVersion).Scan(&out.WorkspaceID, &out.WorktreeID, &path, &out.Branch, &root, &out.RootIdentity, &startHead, &startSnapshot, &state, &out.OriginCommit)
	if errors.Is(err, sql.ErrNoRows) {
		return out, directFileError(directfile.WorktreeUnavailable, "approved member worktree is unavailable")
	}
	if err != nil {
		return out, err
	}
	if state != "READY" || root == "" || out.RootIdentity == "" || startHead == "" || startSnapshot == "" || out.OriginCommit != target.OriginCommit {
		return out, directFileError(directfile.WorktreeUnavailable, "approved member preparation is incomplete")
	}
	canonical, e := filepath.EvalSymlinks(path)
	if e != nil || canonical != root || !filepath.IsAbs(canonical) {
		return out, directFileError(directfile.WorktreeUnavailable, "approved member root changed")
	}
	info, e := os.Stat(canonical)
	if e != nil || !info.IsDir() {
		return out, directFileError(directfile.WorktreeUnavailable, "approved member root is unavailable")
	}
	identity, e := taskFileRootIdentity(info)
	if e != nil || identity != out.RootIdentity {
		return out, directFileError(directfile.WorktreeUnavailable, "approved member root identity changed")
	}
	out.RepositoryID, out.MemberID, out.MemberVersion = target.RepositoryID, target.MemberID, target.MemberVersion
	out.Target = target
	out.WorktreePath, out.StartHead, out.StartSnapshot = canonical, startHead, startSnapshot
	return out, nil
}

func (a *DirectFileAuthority) TaskStatus(ctx context.Context, session, taskID string) (directfile.State, error) {
	project, _, err := taskFileCaller(ctx, a.db.sql, session)
	if err != nil {
		return directfile.State{}, err
	}
	v, err := readTaskFileAuthorization(ctx, a.db.sql, ` WHERE ai_session_id=? AND project_id=? AND task_id=? AND task_definition_revision_id=(SELECT current_definition_revision_id FROM tasks WHERE id=? AND project_id=?)`, session, project, taskID, taskID, project)
	if errors.Is(err, sql.ErrNoRows) {
		empty := directfile.State{State: "UNAVAILABLE", AISessionID: session, ProjectID: project, TaskID: taskID, AllowedOperations: []string{}, FailureCode: "NO_APPROVED_FILE_AUTHORIZATION"}
		if e := a.db.sql.QueryRowContext(ctx, `SELECT COALESCE(feature_area_id,''),COALESCE(current_definition_revision_id,''),COALESCE(review_approval_id,''),COALESCE(repository_id,'') FROM tasks WHERE id=? AND project_id=?`, taskID, project).Scan(&empty.FeatureAreaID, &empty.TaskDefinitionRevisionID, &empty.DecisionID, &empty.RepositoryID); e != nil {
			return directfile.State{}, e
		}
		return empty, nil
	}
	if err != nil {
		return directfile.State{}, err
	}
	return currentTaskFileState(ctx, a.db.sql, v)
}

func currentTaskFileState(ctx context.Context, q definitionQuerier, v taskFileAuthorization) (directfile.State, error) {
	if v.State.State == "CLOSED" {
		return v.State, nil
	}
	if err := validateTaskFileAuthorization(ctx, q, v); err != nil {
		var failure *directfile.Error
		if !errors.As(err, &failure) {
			return directfile.State{}, err
		}
		v.Enabled = false
		v.State.State = "UNAVAILABLE"
		v.AllowedOperations = []string{}
		v.FailureCode = failure.Code
		v.FailureReason = failure.Message
		return v.State, nil
	}
	if v.ProtocolVersion == 2 {
		var epoch string
		if err := q.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&epoch); err != nil {
			return directfile.State{}, err
		}
		if v.State.State == "RECOVERY_REQUIRED" || v.RuntimeEpoch != epoch {
			enrolled, err := directFileWorkerEnrolled(ctx, q, v.WorkflowID)
			if err != nil {
				return directfile.State{}, err
			}
			if enrolled {
				v.Enabled, v.State.State, v.AllowedOperations = false, "RECOVERY_REQUIRED", []string{}
				v.FailureCode, v.FailureReason = directfile.WorkerContinuationUnsupported, fileWorkerRecoveryReason
				return v.State, nil
			}
		}
	}
	if v.State.State == "ACTIVE" {
		if v.ProtocolVersion == 2 {
			members, memberErr := readTaskFileMembers(ctx, q, v.WorkflowID)
			if memberErr != nil || len(members) != len(v.RepositoryTargets) {
				v.Enabled, v.State.State, v.AllowedOperations = false, "UNAVAILABLE", []string{}
				v.FailureCode, v.FailureReason = directfile.WorktreeUnavailable, "approved member set is unavailable"
				return v.State, nil
			}
			for _, member := range members {
				if _, memberErr = taskFileMemberWorkspace(ctx, q, v.WorkflowID, member.Target); memberErr != nil {
					v.Enabled, v.State.State, v.AllowedOperations = false, "UNAVAILABLE", []string{}
					v.FailureCode, v.FailureReason = directfile.WorktreeUnavailable, "approved member root is unavailable"
					return v.State, nil
				}
			}
			return v.State, nil
		}
		w, wt, path, id, err := taskFileWorkspace(ctx, q, v)
		if err != nil || w != v.WorkspaceID || wt != v.WorktreeID || path != v.canonical || id != v.rootIdentity {
			if err != nil {
				var failure *directfile.Error
				if !errors.As(err, &failure) {
					return directfile.State{}, err
				}
			}
			v.Enabled = false
			v.State.State = "UNAVAILABLE"
			v.AllowedOperations = []string{}
			v.FailureCode = directfile.WorktreeUnavailable
			v.FailureReason = "pinned implementation worktree is unavailable"
		}
	}
	return v.State, nil
}

// materializeTaskFileAuthorizationTx participates in the approval transaction.
// No worktree is created here. A missing ready workspace records PREPARING.
func materializeTaskFileAuthorizationTx(ctx context.Context, tx *sql.Tx, t task.Task, session, decision string, registered bool, now time.Time) (directfile.State, error) {
	if len(t.RepositoryTargets) > 0 {
		return directfile.State{}, directFileError("MULTI_REPOSITORY_DIRECT_FILE_UNSUPPORTED", "target-set file grants require durable per-member authorization; legacy grant cannot be expanded")
	}
	existing, err := readTaskFileAuthorization(ctx, tx, ` WHERE task_definition_revision_id=?`, t.CurrentDefinitionRevisionID)
	if err == nil {
		if existing.AISessionID != session || existing.DecisionID != decision || existing.TaskID != t.ID {
			return directfile.State{}, directFileError(directfile.NotEnabled, "existing authorization has another exact owner")
		}
		return currentTaskFileState(ctx, tx, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return directfile.State{}, err
	}
	project, caller, err := taskFileCaller(ctx, tx, session)
	if err != nil {
		return directfile.State{}, err
	}
	if project != t.ProjectID {
		return directfile.State{}, directFileError(directfile.NotEnabled, "Session Project mismatch")
	}
	scope := t.WorkSpec.ExecutionScope
	if scope.Kind != task.ExecutionScopePaths && scope.Kind != task.ExecutionScopeRepository {
		return directfile.State{}, directFileError(directfile.InvalidPath, "an explicit approved file scope is required")
	}
	if scope.Kind == task.ExecutionScopePaths && len(scope.Paths) == 0 {
		return directfile.State{}, directFileError(directfile.InvalidPath, "approved file paths are empty")
	}
	for _, p := range scope.Paths {
		if !safeApprovedFilePath(p) {
			return directfile.State{}, directFileError(directfile.InvalidPath, "approved file path is invalid")
		}
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return directfile.State{}, err
	}
	id, err := eventID()
	if err != nil {
		return directfile.State{}, err
	}
	kind := "APPROVED_DEFINITION"
	if registered {
		kind = "EXPLICIT_CHANNEL_REGISTRATION"
	}
	v := taskFileAuthorization{State: directfile.State{State: "PREPARING", WorkflowID: "direct_file_" + id, AISessionID: session, ProjectID: t.ProjectID, FeatureAreaID: t.FeatureAreaID, TaskID: t.ID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, DecisionID: decision, RepositoryID: t.RepositoryID, ExecutionScope: scope, AllowedOperations: []string{}}, caller: caller, registrationKind: kind}
	if err = validateTaskFileAuthorization(ctx, tx, v); err != nil {
		return directfile.State{}, err
	}
	w, wt, path, rootIdentity, prepErr := taskFileWorkspace(ctx, tx, v)
	if prepErr == nil {
		v.State.State = "ACTIVE"
		v.Enabled = true
		v.WorkspaceID = w
		v.WorktreeID = wt
		v.canonical = path
		v.rootIdentity = rootIdentity
		v.AllowedOperations = []string{"LIST", "READ", "CREATE", "REPLACE", "DELETE", "MOVE"}
	} else {
		var failure *directfile.Error
		if !errors.As(prepErr, &failure) {
			return directfile.State{}, prepErr
		}
		v.FailureCode = failure.Code
		v.FailureReason = failure.Message
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO direct_file_task_authorizations(workflow_id,ai_session_id,caller_scope,project_id,feature_area_id,task_id,task_definition_revision_id,decision_id,implementation_mode,registration_kind,execution_scope_json,state,repository_id,workspace_id,worktree_id,canonical_path,root_identity,failure_code,failure_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'DIRECT_FILE',?,?,?,?,?,?,?,?,?,?,?,?)`, v.WorkflowID, session, caller, t.ProjectID, t.FeatureAreaID, t.ID, t.CurrentDefinitionRevisionID, decision, kind, string(encoded), v.State.State, t.RepositoryID, nullString(w), nullString(wt), nullString(path), nullString(rootIdentity), v.FailureCode, v.FailureReason, formatTime(now), formatTime(now))
	return v.State, err
}

func safeApprovedFilePath(p string) bool {
	if p == "" || len(p) > directfile.MaxPathBytes || filepath.IsAbs(p) || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || filepath.Clean(p) != p {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

// StartTaskAuthorization is the sole v2 write-grant entry point.  It records
// the consumed start identity before any workspace/filesystem preparation;
// Status, reads and a NO_START approval cannot call this method implicitly.
func (a *DirectFileAuthority) StartTaskAuthorization(ctx context.Context, in directfile.StartInput) (directfile.State, error) {
	a.db.directFileOperations.Lock()
	defer a.db.directFileOperations.Unlock()
	if in.Action != "START_DIRECT_FILE" && in.Action != "RECOVER_DIRECT_FILE" && in.Action != "CANCEL_DIRECT_FILE" || !fileResultIDs(in.AISessionID, in.TaskID, in.TaskDefinitionRevisionID, in.DecisionID, in.RequestID, in.ConfirmationIntentID) {
		return directfile.State{}, directFileError(directfile.NotEnabled, "exact direct-file confirmation start is required")
	}
	payload, err := fileResultHash(in)
	if err != nil {
		return directfile.State{}, err
	}
	tx, err := a.db.beginWrite(ctx)
	if err != nil {
		return directfile.State{}, err
	}
	defer tx.Rollback()
	project, caller, err := taskFileCaller(ctx, tx, in.AISessionID)
	if err != nil {
		return directfile.State{}, err
	}
	// Confirmation's transaction has already validated the selected immutable
	// definition and passes its durable intent ID. The authorization stores that
	// ID immutably; recovery reconciliation verifies its presence before it can
	// activate a member. (The intent JSON is updated after this step, so querying
	// a step state here would race the confirmation store's own commit.)
	var intentExists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM confirmation_intents WHERE intent_id=? AND ai_session_id=? AND project_id=?)`, in.ConfirmationIntentID, in.AISessionID, project).Scan(&intentExists); err != nil {
		return directfile.State{}, err
	}
	if !intentExists {
		return directfile.State{}, directFileError(directfile.NotEnabled, "direct-file confirmation intent is unavailable")
	}
	var priorHash, priorWorkflow string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256,workflow_id FROM direct_file_start_receipts WHERE ai_session_id=? AND request_id=?`, in.AISessionID, in.RequestID).Scan(&priorHash, &priorWorkflow)
	if err == nil {
		if priorHash != payload {
			return directfile.State{}, fileResultConflict()
		}
		v, e := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, priorWorkflow)
		if e != nil {
			return directfile.State{}, e
		}
		return v.State, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return directfile.State{}, err
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.TaskID, project))
	if err != nil {
		return directfile.State{}, err
	}
	if err = taskActionAdmissionTx(ctx, tx, t.ID); err != nil {
		return directfile.State{}, err
	}
	if t.CurrentDefinitionRevisionID != in.TaskDefinitionRevisionID || t.ReviewApprovalID != in.DecisionID || t.Status != task.StatusActive || (in.Action == "START_DIRECT_FILE" && t.Phase != task.PhaseImplementationReady) || (in.Action != "START_DIRECT_FILE" && t.Phase != task.PhaseImplementationReady && t.Phase != task.PhaseImplementing) || len(t.RepositoryTargets) == 0 || t.RepositoryID != "" {
		return directfile.State{}, fileResultConflict()
	}
	revision, err := getDefinitionRevision(ctx, tx, in.TaskDefinitionRevisionID)
	if err != nil {
		return directfile.State{}, err
	}
	if revision.Definition.WorkSpec.ImplementationMode != task.ImplementationDirectFile || revision.Definition.Version != 2 || !reflect.DeepEqual(revision.Definition.RepositoryTargets, t.RepositoryTargets) {
		return directfile.State{}, fileResultConflict()
	}
	if err = validateDirectFileConfirmationTx(ctx, tx, in, revision); err != nil {
		return directfile.State{}, err
	}
	if in.Action != "CANCEL_DIRECT_FILE" {
		if err = freezeFileVerificationSelectionTx(ctx, tx, in, revision, time.Now().UTC()); err != nil {
			return directfile.State{}, err
		}
	}
	var workspaceID string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(current_workspace_id,'') FROM feature_areas WHERE id=? AND project_id=? AND status='ACTIVE' AND legacy_read_only=0`, t.FeatureAreaID, project).Scan(&workspaceID); err != nil || workspaceID == "" {
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "approved workspace is not available")
	}
	if in.Action == "CANCEL_DIRECT_FILE" || in.Action == "RECOVER_DIRECT_FILE" {
		if in.WorkflowID == "" {
			return directfile.State{}, directFileError(directfile.NotEnabled, "recovery or cancellation requires exact workflow")
		}
		v, e := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, in.WorkflowID)
		if e != nil {
			return directfile.State{}, e
		}
		if v.ProtocolVersion != 2 || v.AISessionID != in.AISessionID || v.TaskID != t.ID || v.TaskDefinitionRevisionID != revision.ID || v.DecisionID != t.ReviewApprovalID || (in.GrantGeneration != 0 && in.GrantGeneration != v.GrantGeneration) {
			return directfile.State{}, fileResultConflict()
		}
		if in.Action == "RECOVER_DIRECT_FILE" {
			enrolled, e := directFileWorkerEnrolled(ctx, tx, v.WorkflowID)
			if e != nil {
				return directfile.State{}, e
			}
			if enrolled {
				return directfile.State{}, directfile.Failure(directfile.WorkerContinuationUnsupported, fileWorkerRecoveryReason)
			}
		}
		now := time.Now().UTC()
		if in.Action == "CANCEL_DIRECT_FILE" {
			if v.State.State == "CLOSED" {
				return v.State, tx.Commit()
			}
			var unresolved bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_operation_receipts WHERE workflow_id=? AND state IN ('PENDING','UNKNOWN'))`, v.WorkflowID).Scan(&unresolved); e != nil {
				return directfile.State{}, e
			}
			if unresolved {
				// This refusal precedes every cancellation effect and receipt. Do
				// not conflate it with an ambiguous failure after dispatch/commit.
				return directfile.State{}, directFileError(directfile.CancellationMutationUnresolved, "unresolved file mutation receipt requires recovery before cancellation")
			}
			if _, e = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='CLOSED',failure_code='',failure_reason='',updated_at=? WHERE workflow_id=? AND protocol_version=2 AND state IN ('PREPARING','ACTIVE','RECOVERY_REQUIRED')`, formatTime(now), v.WorkflowID); e != nil {
				return directfile.State{}, e
			}
			v.State.State, v.State.Enabled = "CLOSED", false
			if e = insertDirectFileStartReceipt(ctx, tx, in, payload, v.State, now); e != nil {
				return directfile.State{}, e
			}
			return v.State, tx.Commit()
		}
		var currentEpoch string
		if e = tx.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&currentEpoch); e != nil {
			return directfile.State{}, e
		}
		if in.RuntimeEpoch != "" && in.RuntimeEpoch != v.RuntimeEpoch {
			return directfile.State{}, directFileError(directfile.NotEnabled, "recovery confirmation does not name the observed epoch")
		}
		if v.State.State != "RECOVERY_REQUIRED" && v.State.State != "PREPARING" && v.RuntimeEpoch == currentEpoch {
			return directfile.State{}, directFileError(directfile.NotEnabled, "direct-file workflow does not require recovery")
		}
		// Only this consumed recovery may retry a failed preparation. Immutable
		// ready-member roots and initial snapshots remain pinned.
		if _, e = tx.ExecContext(ctx, `UPDATE direct_file_task_members SET preparation='PENDING',failure='' WHERE workflow_id=? AND preparation='FAILED'`, v.WorkflowID); e != nil {
			return directfile.State{}, e
		}
		generation := v.GrantGeneration + 1
		if _, e = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='PREPARING',runtime_epoch=?,generation=?,failure_code='',failure_reason='',updated_at=? WHERE workflow_id=? AND protocol_version=2`, currentEpoch, generation, formatTime(now), v.WorkflowID); e != nil {
			return directfile.State{}, e
		}
		v.State.State, v.State.Enabled, v.RuntimeEpoch, v.GrantGeneration = "PREPARING", false, currentEpoch, generation
		if e = insertDirectFileStartReceipt(ctx, tx, in, payload, v.State, now); e != nil {
			return directfile.State{}, e
		}
		return v.State, tx.Commit()
	}
	if directFileActiveTx(a.db, t.FeatureAreaID) {
		return directfile.State{}, directFileError(directfile.CapabilityActive, "another direct-file workflow owns this FeatureArea")
	}
	authority, err := readRuntimeAuthority(ctx, tx, t.FeatureAreaID)
	if err != nil {
		return directfile.State{}, err
	}
	if authority.Busy {
		return directfile.State{}, directFileError(directfile.CodexActive, "another runtime owner blocks file access")
	}
	var epoch string
	if err = tx.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&epoch); err != nil {
		return directfile.State{}, err
	}
	if in.RuntimeEpoch != "" && in.RuntimeEpoch != epoch {
		return directfile.State{}, directFileError(directfile.NotEnabled, "runtime epoch changed")
	}
	id, err := eventID()
	if err != nil {
		return directfile.State{}, err
	}
	workflowID := "direct_file_" + id
	targets := workset.CloneTargets(t.RepositoryTargets)
	for _, target := range targets {
		if err := ensureRepositoryActiveTx(ctx, tx, project, target.RepositoryID); err != nil {
			return directfile.State{}, err
		}
	}
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		return directfile.State{}, err
	}
	scopeJSON, err := json.Marshal(t.WorkSpec.ExecutionScope)
	if err != nil {
		return directfile.State{}, err
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO direct_file_task_authorizations(workflow_id,ai_session_id,caller_scope,project_id,feature_area_id,task_id,task_definition_revision_id,decision_id,implementation_mode,registration_kind,execution_scope_json,state,repository_id,workspace_id,worktree_id,canonical_path,root_identity,failure_code,failure_reason,created_at,updated_at,protocol_version,repository_targets_json,runtime_epoch,generation,start_request_id,confirmation_intent_id) VALUES(?,?,?,?,?,?,?,?,'DIRECT_FILE','APPROVED_DEFINITION',?,'PREPARING',NULL,?,NULL,NULL,NULL,'','',?,?,2,?,?,1,?,?)`, workflowID, in.AISessionID, caller, project, t.FeatureAreaID, t.ID, t.CurrentDefinitionRevisionID, t.ReviewApprovalID, string(scopeJSON), workspaceID, formatTime(now), formatTime(now), string(targetJSON), epoch, in.RequestID, in.ConfirmationIntentID)
	if err != nil {
		return directfile.State{}, err
	}
	for _, target := range targets {
		raw, e := json.Marshal(target)
		if e != nil {
			return directfile.State{}, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO direct_file_task_members(workflow_id,repository_id,member_id,member_version,target_json,workspace_id,origin_commit) VALUES(?,?,?,?,?,?,?)`, workflowID, target.RepositoryID, target.MemberID, target.MemberVersion, string(raw), workspaceID, target.OriginCommit); e != nil {
			return directfile.State{}, e
		}
	}
	grants := make([]directfile.MemberGrant, 0, len(targets))
	for _, target := range targets {
		grants = append(grants, directfile.MemberGrant{Target: target, WorkspaceID: workspaceID, State: "PENDING"})
	}
	v := directfile.State{State: "PREPARING", AISessionID: in.AISessionID, ProjectID: project, FeatureAreaID: t.FeatureAreaID, TaskID: t.ID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, DecisionID: t.ReviewApprovalID, WorkflowID: workflowID, WorkspaceID: workspaceID, ProtocolVersion: 2, RuntimeEpoch: epoch, GrantGeneration: 1, RepositoryTargets: grants}
	if err = insertDirectFileStartReceipt(ctx, tx, in, payload, v, now); err != nil {
		return directfile.State{}, err
	}
	return v, tx.Commit()
}

func insertDirectFileStartReceipt(ctx context.Context, tx *sql.Tx, in directfile.StartInput, hash string, state directfile.State, now time.Time) error {
	receipt, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO direct_file_start_receipts(ai_session_id,request_id,payload_sha256,payload_json,workflow_id,action,runtime_epoch,generation,receipt_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, in.AISessionID, in.RequestID, hash, mustJSON(in), state.WorkflowID, in.Action, state.RuntimeEpoch, state.GrantGeneration, string(receipt), formatTime(now))
	return err
}

func mustJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }

func (a *DirectFileAuthority) ReconcileTaskAuthorization(ctx context.Context, in directfile.Registration) (directfile.State, error) {
	tx, err := a.db.beginWrite(ctx)
	if err != nil {
		return directfile.State{}, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, in.TaskID))
	if err != nil {
		return directfile.State{}, err
	}
	if t.CurrentDefinitionRevisionID != in.TaskDefinitionRevisionID || t.ReviewApprovalID != in.DecisionID {
		return directfile.State{}, directFileError(directfile.NotEnabled, "approved revision or receipt mismatch")
	}
	project, _, ownerErr := taskFileCaller(ctx, tx, in.AISessionID)
	if ownerErr != nil {
		return directfile.State{}, ownerErr
	}
	if project != t.ProjectID {
		return directfile.State{}, directFileError(directfile.NotEnabled, "Session Project mismatch")
	}
	revision, err := getDefinitionRevision(ctx, tx, in.TaskDefinitionRevisionID)
	if err != nil {
		return directfile.State{}, err
	}
	mode := string(revision.Definition.WorkSpec.ImplementationMode)
	if len(revision.Definition.RepositoryTargets) > 0 {
		if mode != "DIRECT_FILE" || revision.Definition.Version != 2 {
			return directfile.State{}, directFileError(directfile.NotEnabled, "exact multi-repository Direct File definition is required")
		}
		return a.reconcileMultiTaskAuthorization(ctx, tx, t, revision, in, project)
	}
	registered := mode == ""
	if mode != "DIRECT_FILE" && !(registered && in.Confirmed && in.ImplementationMode == "DIRECT_FILE") {
		return directfile.State{}, directFileError(directfile.NotEnabled, "explicit Direct File channel confirmation is required")
	}
	if in.ImplementationMode != "" && in.ImplementationMode != "DIRECT_FILE" {
		return directfile.State{}, directFileError(directfile.NotEnabled, "implementation channel mismatch")
	}
	if directFileActiveTx(a.db, t.FeatureAreaID) {
		return directfile.State{}, directFileError(directfile.CapabilityActive, "standalone file workflow owns this FeatureArea")
	}
	t.WorkSpec = revision.Definition.WorkSpec
	state, err := materializeTaskFileAuthorizationTx(ctx, tx, t, in.AISessionID, in.DecisionID, registered, time.Now().UTC())
	if err != nil {
		return state, err
	}
	if state.State == "PREPARING" {
		v, e := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, state.WorkflowID)
		if e != nil {
			return state, e
		}
		if e = validateTaskFileAuthorization(ctx, tx, v); e != nil {
			return state, e
		}
		w, wt, path, id, e := taskFileWorkspace(ctx, tx, v)
		if e == nil {
			_, err = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='ACTIVE',workspace_id=?,worktree_id=?,canonical_path=?,root_identity=?,failure_code='',failure_reason='',updated_at=? WHERE workflow_id=? AND state='PREPARING'`, w, wt, path, id, formatTime(time.Now().UTC()), v.WorkflowID)
			if err != nil {
				return state, err
			}
			v, err = readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, v.WorkflowID)
			if err != nil {
				return state, err
			}
			state = v.State
		} else {
			var failure *directfile.Error
			if !errors.As(e, &failure) {
				return state, e
			}
		}
	}
	return state, tx.Commit()
}

func (a *DirectFileAuthority) reconcileMultiTaskAuthorization(ctx context.Context, tx *sql.Tx, t task.Task, revision task.DefinitionRevision, in directfile.Registration, project string) (directfile.State, error) {
	v, err := readTaskFileAuthorization(ctx, tx, ` WHERE task_definition_revision_id=? AND task_id=? AND ai_session_id=?`, revision.ID, t.ID, in.AISessionID)
	if err != nil {
		return directfile.State{}, err
	}
	if v.ProtocolVersion != 2 || v.ProjectID != project || v.DecisionID != t.ReviewApprovalID || v.State.State == "CLOSED" || v.State.State == "RECOVERY_REQUIRED" || !sameMemberTargets(v.RepositoryTargets, revision.Definition.RepositoryTargets) {
		return directfile.State{}, fileResultConflict()
	}
	if err = validateDirectFilePreparationConfirmationTx(ctx, tx, v, revision); err != nil {
		return directfile.State{}, err
	}
	var startIntent string
	if err = tx.QueryRowContext(ctx, `SELECT confirmation_intent_id FROM direct_file_task_authorizations WHERE workflow_id=? AND start_request_id<>''`, v.WorkflowID).Scan(&startIntent); err != nil || startIntent == "" {
		return directfile.State{}, directFileError(directfile.NotEnabled, "v2 preparation lacks a consumed start authorization")
	}
	if err = validateTaskFileAuthorization(ctx, tx, v); err != nil {
		return directfile.State{}, err
	}
	// A crash can occur after the fsync'd journal but before the receipt
	// completion transaction. Settle only matching durable journal proofs before
	// comparing the frozen initial snapshots; a missing journal remains an
	// unresolved fact and cannot be treated as a legitimate workspace change.
	pending, err := tx.QueryContext(ctx, `SELECT request_id,payload_sha256 FROM direct_file_operation_receipts WHERE workflow_id=? AND state IN ('PENDING','UNKNOWN')`, v.WorkflowID)
	if err != nil {
		return directfile.State{}, err
	}
	for pending.Next() {
		var requestID, payload string
		if err = pending.Scan(&requestID, &payload); err != nil {
			pending.Close()
			return directfile.State{}, err
		}
		proof, ok := a.readMutationJournal(v.WorkflowID, requestID, payload)
		if !ok {
			pending.Close()
			return directfile.State{}, directFileError(directfile.OperationFailed, "unresolved direct-file mutation has no durable proof")
		}
		raw, marshalErr := json.Marshal(proof)
		if marshalErr != nil {
			pending.Close()
			return directfile.State{}, marshalErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE direct_file_operation_receipts SET state='COMPLETED',result_json=?,error='',updated_at=? WHERE workflow_id=? AND request_id=? AND payload_sha256=? AND state IN ('PENDING','UNKNOWN')`, string(raw), formatTime(time.Now().UTC()), v.WorkflowID, requestID, payload); err != nil {
			pending.Close()
			return directfile.State{}, err
		}
	}
	if err = pending.Err(); err != nil {
		pending.Close()
		return directfile.State{}, err
	}
	pending.Close()
	// This is idempotent: a READY member's frozen root/snapshots are read and
	// checked, never overwritten. PENDING members are completed only with the
	// exact workspace member named by the immutable definition.
	for _, target := range revision.Definition.RepositoryTargets {
		var preparation, wtID, storedPath, storedIdentity, storedHead, storedSnapshot string
		if err = tx.QueryRowContext(ctx, `SELECT preparation,COALESCE(worktree_id,''),canonical_path,root_identity,start_head,start_snapshot FROM direct_file_task_members WHERE workflow_id=? AND repository_id=? AND member_id=? AND member_version=?`, v.WorkflowID, target.RepositoryID, target.MemberID, target.MemberVersion).Scan(&preparation, &wtID, &storedPath, &storedIdentity, &storedHead, &storedSnapshot); err != nil {
			return directfile.State{}, err
		}
		var actualWT, path string
		if err = tx.QueryRowContext(ctx, `SELECT wm.worktree_id,wt.path FROM workspace_members wm JOIN worktrees wt ON wt.id=wm.worktree_id AND wt.status='READY' AND wt.repository_id=wm.repository_id WHERE wm.workspace_id=? AND wm.repository_id=? AND wm.id=? AND wm.identity_version=? AND wm.preparation='READY'`, v.WorkspaceID, target.RepositoryID, target.MemberID, target.MemberVersion).Scan(&actualWT, &path); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				_, _ = tx.ExecContext(ctx, `UPDATE direct_file_task_members SET preparation='FAILED',failure='approved workspace member is not ready' WHERE workflow_id=? AND repository_id=? AND preparation='PENDING'`, v.WorkflowID, target.RepositoryID)
				return directfile.State{State: "PREPARING", WorkflowID: v.WorkflowID}, tx.Commit()
			}
			return directfile.State{}, err
		}
		if wtID != "" && wtID != actualWT {
			return directfile.State{}, fileResultConflict()
		}
		canonical, e := filepath.EvalSymlinks(path)
		if e != nil {
			return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "cannot canonicalize approved member worktree")
		}
		info, e := os.Stat(canonical)
		if e != nil || !info.IsDir() {
			return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "approved member worktree is unavailable")
		}
		identity, e := taskFileRootIdentity(info)
		if e != nil {
			return directfile.State{}, e
		}
		g := git.NewClient(git.Config{})
		head, e := g.ResolveHEAD(ctx, canonical)
		if e != nil {
			return directfile.State{}, e
		}
		snapshot, e := g.Snapshot(ctx, canonical)
		if e != nil {
			return directfile.State{}, e
		}
		if preparation == "READY" {
			if storedPath != canonical || storedIdentity != identity || storedHead != head {
				return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "frozen direct-file member changed")
			}
			if storedSnapshot != snapshot {
				changed, changeErr := changedPathsBetweenSnapshots(ctx, canonical, storedSnapshot, snapshot)
				if changeErr != nil {
					return directfile.State{}, changeErr
				}
				var receipts string
				if err = tx.QueryRowContext(ctx, `SELECT COALESCE(json_group_array(json_object('path',path,'destination',destination)),'[]') FROM direct_file_operation_receipts WHERE workflow_id=? AND repository_id=? AND member_id=? AND state='COMPLETED'`, v.WorkflowID, target.RepositoryID, target.MemberID).Scan(&receipts); err != nil {
					return directfile.State{}, err
				}
				var paths []struct {
					Path        string `json:"path"`
					Destination string `json:"destination"`
				}
				if err = json.Unmarshal([]byte(receipts), &paths); err != nil {
					return directfile.State{}, err
				}
				allowed := map[string]bool{}
				for _, value := range paths {
					allowed[value.Path] = true
					if value.Destination != "" {
						allowed[value.Destination] = true
					}
				}
				for _, value := range changed {
					if !allowed[value] || !taskFileTargetPathAllowed(target, value) {
						return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "unproven direct-file member change")
					}
				}
			}
			continue
		}
		if preparation != "PENDING" {
			return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "member preparation requires explicit recovery")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE direct_file_task_members SET worktree_id=?,canonical_path=?,root_identity=?,start_head=?,start_snapshot=?,preparation='READY',failure='' WHERE workflow_id=? AND repository_id=? AND preparation='PENDING'`, actualWT, canonical, identity, head, snapshot, v.WorkflowID, target.RepositoryID); e != nil {
			return directfile.State{}, e
		}
	}
	now := formatTime(time.Now().UTC())
	result, err := tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='ACTIVE',failure_code='',failure_reason='',updated_at=? WHERE workflow_id=? AND protocol_version=2 AND state='PREPARING' AND NOT EXISTS(SELECT 1 FROM direct_file_task_members WHERE workflow_id=? AND preparation<>'READY')`, now, v.WorkflowID, v.WorkflowID)
	if err != nil {
		return directfile.State{}, err
	}
	if n, _ := result.RowsAffected(); n == 0 && v.State.State != "ACTIVE" {
		return directfile.State{}, fileResultConflict()
	}
	v, err = readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, v.WorkflowID)
	if err != nil {
		return directfile.State{}, err
	}
	receipt, e := json.Marshal(v.State)
	if e != nil {
		return directfile.State{}, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE direct_file_start_receipts SET receipt_json=? WHERE workflow_id=? AND action IN ('START_DIRECT_FILE','RECOVER_DIRECT_FILE') AND runtime_epoch=? AND generation=?`, string(receipt), v.WorkflowID, v.RuntimeEpoch, v.GrantGeneration); e != nil {
		return directfile.State{}, e
	}
	return v.State, tx.Commit()
}

func sameMemberTargets(members []directfile.MemberGrant, targets []workset.Target) bool {
	if len(members) != len(targets) {
		return false
	}
	byRepository := make(map[string]workset.Target, len(targets))
	for _, target := range targets {
		byRepository[target.RepositoryID] = target
	}
	for _, member := range members {
		if target, ok := byRepository[member.RepositoryID]; !ok || !reflect.DeepEqual(member.Target, target) {
			return false
		}
	}
	return true
}

// Scope checks run under the same writer transaction as the actual file action.
// Listing an ancestor is allowed, but the service receives a filter for every
// child, so unrelated sibling names are never disclosed.
func taskFilePathAllowed(scope task.ExecutionScope, p string, list bool) bool {
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean != "." && !safeApprovedFilePath(clean) {
		return false
	}
	if scope.Kind == task.ExecutionScopeRepository {
		return true
	}
	if scope.Kind != task.ExecutionScopePaths {
		return false
	}
	for _, allowed := range scope.Paths {
		if clean == allowed || strings.HasPrefix(clean, allowed+"/") {
			return true
		}
		if list && (clean == "." || strings.HasPrefix(allowed, clean+"/")) {
			return true
		}
	}
	return false
}

var _ directfile.TaskAuthority = (*DirectFileAuthority)(nil)

// ReconcilePendingTaskAuthorizations resumes only durable approved intentions.
// It never creates a workspace, changes an approved source, or reopens CLOSED.
func (a *DirectFileAuthority) ReconcilePendingTaskAuthorizations(ctx context.Context) error {
	rows, err := a.db.sql.QueryContext(ctx, `SELECT ai_session_id,task_id,task_definition_revision_id,decision_id,caller_scope,registration_kind FROM direct_file_task_authorizations WHERE state='PREPARING' AND protocol_version=1 ORDER BY created_at`)
	if err != nil {
		return err
	}
	type pending struct {
		in     directfile.Registration
		caller sql.NullString
	}
	var values []pending
	for rows.Next() {
		var v pending
		var kind string
		if err = rows.Scan(&v.in.AISessionID, &v.in.TaskID, &v.in.TaskDefinitionRevisionID, &v.in.DecisionID, &v.caller, &kind); err != nil {
			rows.Close()
			return err
		}
		v.in.ImplementationMode = "DIRECT_FILE"
		v.in.Confirmed = kind == "EXPLICIT_CHANNEL_REGISTRATION"
		values = append(values, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range values {
		owned := ctx
		if v.caller.Valid {
			owned = aisession.WithCallerScope(ctx, v.caller.String)
		}
		state, reconcileErr := a.ReconcileTaskAuthorization(owned, v.in)
		if reconcileErr != nil {
			var failure *directfile.Error
			if !errors.As(reconcileErr, &failure) {
				return reconcileErr
			}
			continue
		}
		if state.State != "PREPARING" {
			continue
		}
		exact, e := readTaskFileAuthorization(owned, a.db.sql, ` WHERE workflow_id=?`, state.WorkflowID)
		if e != nil {
			return e
		}
		if e = validateTaskFileAuthorization(owned, a.db.sql, exact); e != nil {
			var failure *directfile.Error
			if !errors.As(e, &failure) {
				return e
			}
			continue
		}
		_, _, _, _, preparationErr := taskFileWorkspace(owned, a.db.sql, exact)
		var needsPreparation *directfile.Error
		if !errors.As(preparationErr, &needsPreparation) || needsPreparation.Code != "DIRECT_FILE_WORKSPACE_PREPARATION_REQUIRED" {
			continue
		}
		a.prepareMu.RLock()
		prepare := a.prepareTaskWorkspace
		a.prepareMu.RUnlock()
		if prepare == nil {
			continue
		}
		// Git/filesystem preparation is outside any database transaction. Admission
		// below repeats the exact current approval, lifecycle and conflict checks.
		if e = prepare(owned, state); e != nil {
			tx, writeErr := a.db.beginWrite(owned)
			if writeErr != nil {
				return writeErr
			}
			_, writeErr = tx.ExecContext(owned, `UPDATE direct_file_task_authorizations SET failure_code='DIRECT_FILE_WORKSPACE_PREPARATION_FAILED',failure_reason=?,updated_at=? WHERE workflow_id=? AND state='PREPARING'`, e.Error(), formatTime(time.Now().UTC()), state.WorkflowID)
			if writeErr == nil {
				writeErr = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			if writeErr != nil {
				return writeErr
			}
			continue
		}
		if _, e = a.ReconcileTaskAuthorization(owned, v.in); e != nil {
			var failure *directfile.Error
			if !errors.As(e, &failure) {
				return e
			}
		}

	}
	return nil
}

func taskFileWithCapabilityTx(ctx context.Context, tx *sql.Tx, s directfile.Scope, v taskFileAuthorization, complete bool, fn func(*os.Root) error) error {
	if v.AISessionID != s.AISessionID || v.FeatureAreaID != s.FeatureAreaID || v.WorkflowID != s.WorkflowID || v.State.State != "ACTIVE" {
		return directFileError(directfile.NotEnabled, "exact file workflow is not active")
	}
	if err := validateTaskFileAuthorization(ctx, tx, v); err != nil {
		return err
	}
	if v.ProtocolVersion == 2 {
		return taskFileWithCapabilityV2Tx(ctx, tx, s, v, complete, fn)
	}
	w, wt, path, id, err := taskFileWorkspace(ctx, tx, v)
	if err != nil {
		return err
	}
	if w != v.WorkspaceID || wt != v.WorktreeID || path != v.canonical || id != v.rootIdentity {
		return directFileError(directfile.WorktreeUnavailable, "pinned implementation workspace changed")
	}
	op, ok := directfile.OperationFromContext(ctx)
	if !ok {
		return directFileError(directfile.OperationFailed, "typed file operation is required")
	}
	if err := validateDirectFileMember(ctx, tx, op, v.WorkspaceID, v.RepositoryID, v.WorktreeID); err != nil {
		return err
	}
	switch op.Kind {
	case "LIST", "READ", "CREATE", "REPLACE", "DELETE", "MOVE":
	default:
		return directFileError(directfile.OperationFailed, "file operation is unsupported")
	}
	if !taskFilePathAllowed(v.ExecutionScope, op.Path, op.Kind == "LIST") || (op.Kind == "MOVE" && !taskFilePathAllowed(v.ExecutionScope, op.Destination, false)) {
		return directFileError(directfile.OutsideWorktree, "path is outside the approved implementation scope")
	}
	directfile.SetPathFilter(ctx, func(p string, list bool) bool { return taskFilePathAllowed(v.ExecutionScope, p, list) })
	root, err := os.OpenRoot(path)
	if err != nil {
		return directFileError(directfile.WorktreeUnavailable, "cannot open approved worktree")
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	opened, err := taskFileRootIdentity(info)
	if err != nil {
		return err
	}
	if opened != v.rootIdentity {
		return directFileError(directfile.WorktreeUnavailable, "worktree changed while opening")
	}
	if fn == nil {
		return directFileError(directfile.OperationFailed, "file operation is unavailable")
	}
	operationErr := fn(root)
	now := formatTime(time.Now().UTC())
	if complete {
		if _, err = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='CLOSED',failure_code='',failure_reason='',updated_at=? WHERE workflow_id=? AND state='ACTIVE'`, now, v.WorkflowID); err != nil {
			return err
		}
	}
	if operationErr == nil && op.Kind != "READ" && op.Kind != "LIST" {
		if _, err = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET started_at=COALESCE(started_at,?),last_operation=?,operation_count=operation_count+1,updated_at=? WHERE workflow_id=?`, now, op.Kind, now, v.WorkflowID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='IMPLEMENTING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='IMPLEMENTATION_READY' AND current_definition_revision_id=?`, now, v.TaskID, v.TaskDefinitionRevisionID); err != nil {
			return err
		}
	}
	if operationErr != nil && !complete {
		return operationErr
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return operationErr
}

func taskFileWithCapabilityV2Tx(ctx context.Context, tx *sql.Tx, s directfile.Scope, v taskFileAuthorization, complete bool, fn func(*os.Root) error) error {
	op, ok := directfile.OperationFromContext(ctx)
	if !ok || op.RepositoryID == "" || op.MemberID == "" || op.MemberVersion < 1 || op.RuntimeEpoch != v.RuntimeEpoch || op.GrantGeneration != v.GrantGeneration {
		return directFileError(directfile.NotEnabled, "exact v2 repository member and runtime fence are required")
	}
	var targetJSON, preparation string
	err := tx.QueryRowContext(ctx, `SELECT target_json,preparation FROM direct_file_task_members WHERE workflow_id=? AND repository_id=? AND member_id=? AND member_version=?`, v.WorkflowID, op.RepositoryID, op.MemberID, op.MemberVersion).Scan(&targetJSON, &preparation)
	if errors.Is(err, sql.ErrNoRows) {
		return directFileError(directfile.NotEnabled, "repository member is outside the approved direct-file workset")
	}
	if err != nil {
		return err
	}
	var target workset.Target
	if err = json.Unmarshal([]byte(targetJSON), &target); err != nil {
		return err
	}
	if preparation != "READY" || !target.Read {
		return directFileError(directfile.NotEnabled, "repository member is not readable")
	}
	mutating := op.Kind != "READ" && op.Kind != "LIST"
	if mutating {
		if op.RequestID == "" || (op.Kind != "CREATE" && op.ExpectedDigest == "") {
			return directFileError(directfile.NotEnabled, "v2 mutation requires exact request and source digest")
		}
		if target.Write.Kind == "NONE" || (target.Write.Kind == "PATHS" && (!taskFileTargetPathAllowed(target, op.Path) || (op.Kind == "MOVE" && !taskFileTargetPathAllowed(target, op.Destination)))) {
			return directFileError(directfile.OutsideWorktree, "path is outside the approved repository write scope")
		}
	}
	member, err := taskFileMemberWorkspace(ctx, tx, v.WorkflowID, target)
	if err != nil {
		return err
	}
	if fn == nil {
		return directFileError(directfile.OperationFailed, "file operation is unavailable")
	}
	root, err := os.OpenRoot(member.WorktreePath)
	if err != nil {
		return directFileError(directfile.WorktreeUnavailable, "cannot open approved member root")
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	identity, err := taskFileRootIdentity(info)
	if err != nil || identity != member.RootIdentity {
		return directFileError(directfile.WorktreeUnavailable, "approved member root changed while opening")
	}
	if mutating {
		payload, e := fileResultHash(op)
		if e != nil {
			return e
		}
		var savedHash, state string
		e = tx.QueryRowContext(ctx, `SELECT payload_sha256,state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, v.WorkflowID, op.RequestID).Scan(&savedHash, &state)
		if e == nil {
			if savedHash != payload || state != "COMPLETED" {
				return directFileError(directfile.OperationFailed, "direct-file mutation receipt is unresolved or conflicts")
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = ensureRepositoryActiveTx(ctx, tx, v.ProjectID, op.RepositoryID); e != nil {
			return e
		}
		raw, _ := json.Marshal(op)
		now := formatTime(time.Now().UTC())
		if _, e = tx.ExecContext(ctx, `INSERT INTO direct_file_operation_receipts(workflow_id,request_id,repository_id,member_id,payload_sha256,payload_json,operation,path,destination,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'PENDING',?,?)`, v.WorkflowID, op.RequestID, op.RepositoryID, op.MemberID, payload, string(raw), op.Kind, op.Path, op.Destination, now, now); e != nil {
			return e
		}
	}
	directfile.SetPathFilter(ctx, func(p string, list bool) bool {
		return taskFileTargetPathAllowed(target, p) || (list && taskFileTargetListAllowed(target, p))
	})
	operationErr := fn(root)
	if mutating {
		state, message := "COMPLETED", ""
		if operationErr != nil {
			state, message = "FAILED", operationErr.Error()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE direct_file_operation_receipts SET state=?,error=?,updated_at=? WHERE workflow_id=? AND request_id=? AND state='PENDING'`, state, message, formatTime(time.Now().UTC()), v.WorkflowID, op.RequestID); err != nil {
			return err
		}
	}
	if operationErr != nil {
		return operationErr
	}
	if complete {
		return directFileError(directfile.OperationFailed, "v2 direct-file completion requires formal result submission")
	}
	if mutating {
		now := formatTime(time.Now().UTC())
		if _, err = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET started_at=COALESCE(started_at,?),last_operation=?,operation_count=operation_count+1,updated_at=? WHERE workflow_id=? AND state='ACTIVE'`, now, op.Kind, now, v.WorkflowID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='IMPLEMENTING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='IMPLEMENTATION_READY' AND current_definition_revision_id=?`, now, v.TaskID, v.TaskDefinitionRevisionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func taskFileTargetPathAllowed(target workset.Target, value string) bool {
	if target.Write.Kind == "REPOSITORY" {
		return safeApprovedFilePath(value)
	}
	if target.Write.Kind != "PATHS" || !safeApprovedFilePath(value) {
		return false
	}
	for _, allowed := range target.Write.Paths {
		if value == allowed || strings.HasPrefix(value, allowed+"/") {
			return true
		}
	}
	return false
}
func taskFileTargetListAllowed(target workset.Target, value string) bool {
	// Read permission is repository-wide for an admitted member. Write PATHS
	// constrains mutations only; it must not hide a read-only member's files.
	if target.Read {
		return value == "." || safeApprovedFilePath(value)
	}
	if target.Write.Kind == "REPOSITORY" {
		return safeApprovedFilePath(value) || value == "."
	}
	for _, allowed := range target.Write.Paths {
		if value == "." || strings.HasPrefix(allowed, value+"/") || value == allowed {
			return true
		}
	}
	return false
}
