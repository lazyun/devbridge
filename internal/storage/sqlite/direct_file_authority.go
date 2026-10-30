package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// DirectFileAuthority retains standalone grants in memory and approved Task
// grants in durable storage. Both hold the same writer transaction across I/O.
type DirectFileAuthority struct {
	db                   *DB
	prepareMu            sync.RWMutex
	prepareTaskWorkspace func(context.Context, directfile.State) error
	afterFileEffect      func() error
}

type mutationJournal struct {
	PayloadSHA256 string            `json:"payloadSha256"`
	Result        directfile.Result `json:"result"`
}

func (a *DirectFileAuthority) mutationJournalPath(workflow, request string) string {
	sum := sha256.Sum256([]byte(workflow + "\x00" + request))
	return filepath.Join(a.db.stateRoot, "direct-file-journal", fmt.Sprintf("%x.json", sum[:]))
}
func (a *DirectFileAuthority) writeMutationJournal(workflow, request, hash string, result directfile.Result) error {
	dir := filepath.Dir(a.mutationJournalPath(workflow, request))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(mutationJournal{PayloadSHA256: hash, Result: result})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, a.mutationJournalPath(workflow, request)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (a *DirectFileAuthority) readMutationJournal(workflow, request, hash string) (directfile.Result, bool) {
	raw, err := os.ReadFile(a.mutationJournalPath(workflow, request))
	if err != nil {
		return directfile.Result{}, false
	}
	var value mutationJournal
	if json.Unmarshal(raw, &value) != nil || value.PayloadSHA256 != hash || value.Result.Path == "" {
		return directfile.Result{}, false
	}
	return value.Result, true
}

// SetAfterFileEffectHook is a test-only fault seam for the filesystem/receipt
// boundary. Production never installs it; a failure leaves the durable PENDING
// receipt for explicit recovery rather than replaying the mutation.
func (a *DirectFileAuthority) SetAfterFileEffectHook(fn func() error) {
	a.prepareMu.Lock()
	defer a.prepareMu.Unlock()
	a.afterFileEffect = fn
}

// SetTaskWorkspacePreparer installs Core technical continuation at bootstrap.
// It never represents a new user authorization.
func (a *DirectFileAuthority) SetTaskWorkspacePreparer(fn func(context.Context, directfile.State) error) {
	a.prepareMu.Lock()
	a.prepareTaskWorkspace = fn
	a.prepareMu.Unlock()
}

func NewDirectFileAuthority(db *DB) *DirectFileAuthority { return &DirectFileAuthority{db: db} }

var directFileGrants = struct {
	sync.Mutex
	byDB map[*DB]map[string]*directFileGrant
}{byDB: make(map[*DB]map[string]*directFileGrant)}

type directFileGrant struct {
	scope                                                           directfile.Scope
	projectID, repositoryID, workspaceID, worktreeID, canonicalPath string
	rootInfo                                                        os.FileInfo
	root                                                            *os.Root
}

func directFileError(code, message string) error { return directfile.Failure(code, message) }

func validDirectFileScope(s directfile.Scope) bool {
	return strings.TrimSpace(s.AISessionID) != "" && strings.TrimSpace(s.FeatureAreaID) != "" && strings.TrimSpace(s.WorkflowID) != "" && len(s.AISessionID) <= 128 && len(s.FeatureAreaID) <= 128 && len(s.WorkflowID) <= 128
}

// directFileWorkspaceTx resolves only the Feature Area's current ready
// workspace.  It never accepts a caller supplied root.
func directFileWorkspaceTx(ctx context.Context, tx *sql.Tx, s directfile.Scope) (projectID, repositoryID, worktreeID, path string, err error) {
	if !validDirectFileScope(s) {
		err = directFileError(directfile.WorktreeUnavailable, "an explicit session, feature area, and workflow are required")
		return
	}
	err = tx.QueryRowContext(ctx, `SELECT a.project_id,w.repository_id,wt.id,wt.path
FROM ai_sessions x
JOIN feature_areas a ON a.id=? AND a.project_id=x.project_id AND a.legacy_read_only=0 AND a.status IN ('DISCOVERING','ACTIVE')
JOIN workspaces w ON w.id=a.current_workspace_id AND w.feature_area_id=a.id AND w.project_id=a.project_id AND w.runtime_quarantined=0 AND w.status='READY'
JOIN worktrees wt ON wt.id=w.worktree_id AND wt.repository_id=w.repository_id AND wt.status='READY'
JOIN repositories r ON r.id=w.repository_id AND r.project_id=a.project_id
WHERE NOT EXISTS(SELECT 1 FROM development_cycles c WHERE c.project_id=a.project_id AND c.repository_id=w.repository_id AND c.superseded_at IS NULL AND c.development_ref='refs/heads/' || wt.branch) AND x.id=?`, s.FeatureAreaID, s.AISessionID).Scan(&projectID, &repositoryID, &worktreeID, &path)
	if errors.Is(err, sql.ErrNoRows) {
		err = directFileError(directfile.WorktreeUnavailable, "the feature area has no current writable worktree")
	}
	return
}

func directFileEnableBusyTx(ctx context.Context, tx *sql.Tx, featureAreaID string) error {
	if err := commitBusy(ctx, tx, featureAreaID); err != nil {
		if errors.Is(err, errFeatureAreaBusy) {
			return directFileError(directfile.CodexActive, "a writable operation owns this feature area")
		}
		return err
	}
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=? AND completed_at IS NULL) OR EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=? AND completed_at IS NULL)`, featureAreaID, featureAreaID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return directFileError(directfile.CodexActive, "a Git finalization is in progress")
	}
	return nil
}

func directFileActiveTx(db *DB, featureAreaID string) bool {
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	return directFileGrants.byDB[db] != nil && directFileGrants.byDB[db][featureAreaID] != nil
}

func directFileAdmissionTx(ctx context.Context, q definitionQuerier, db *DB, featureAreaID string) error {
	var durable bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_task_authorizations WHERE feature_area_id=? AND state IN ('ACTIVE','PREPARING','RECOVERY_REQUIRED'))`, featureAreaID).Scan(&durable); err != nil {
		return err
	}
	if durable {
		return directFileError(directfile.CapabilityActive, "an approved Direct File workflow owns this feature area")
	}

	if directFileActiveTx(db, featureAreaID) {
		return directFileError(directfile.CapabilityActive, "direct file capability owns this feature area")
	}
	return nil
}

func invalidateDirectFile(db *DB, featureAreaID string) {
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	if grant := directFileGrants.byDB[db][featureAreaID]; grant != nil {
		_ = grant.root.Close()
		delete(directFileGrants.byDB[db], featureAreaID)
	}
}

func invalidateAllDirectFiles(db *DB) {
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	for _, grant := range directFileGrants.byDB[db] {
		_ = grant.root.Close()
	}
	delete(directFileGrants.byDB, db)
}

func (a *DirectFileAuthority) Enable(ctx context.Context, s directfile.Scope) (directfile.State, error) {
	if !validDirectFileScope(s) {
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "an explicit session, feature area, and workflow are required")
	}
	tx, err := a.db.beginWrite(ctx)
	if err != nil {
		return directfile.State{}, err
	}
	defer tx.Rollback()
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	projectID, repositoryID, worktreeID, path, err := directFileWorkspaceTx(ctx, tx, s)
	if err != nil {
		return directfile.State{}, err
	}
	var approvedOwner bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_task_authorizations WHERE feature_area_id=? AND state IN ('ACTIVE','PREPARING','RECOVERY_REQUIRED')) OR EXISTS(SELECT 1 FROM tasks WHERE feature_area_id=? AND status='ACTIVE' AND task_type='DEVELOPMENT')`, s.FeatureAreaID, s.FeatureAreaID).Scan(&approvedOwner); err != nil {
		return directfile.State{}, err
	}
	if approvedOwner {
		return directfile.State{}, directFileError(directfile.CapabilityActive, "Task file access must come from its exact approved definition")
	}
	if err = directFileEnableBusyTx(ctx, tx, s.FeatureAreaID); err != nil {
		return directfile.State{}, err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) {
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "cannot canonicalize current worktree")
	}
	var repositoryPath string
	if err = tx.QueryRowContext(ctx, `SELECT canonical_path FROM repositories WHERE id=? AND project_id=?`, repositoryID, projectID).Scan(&repositoryPath); err != nil {
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "repository identity is unavailable")
	}
	if repositoryCanonical, e := filepath.EvalSymlinks(repositoryPath); e != nil || repositoryCanonical == canonical {
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "the canonical repository checkout is not a Direct File worktree")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "current worktree is unavailable")
	}
	// Worktree aliases and registered repository roots cannot borrow this grant.
	rows, err := tx.QueryContext(ctx, `SELECT canonical_path FROM repositories UNION ALL SELECT path FROM worktrees WHERE id<>? AND status='READY'`, worktreeID)
	if err != nil {
		return directfile.State{}, err
	}
	for rows.Next() {
		var other string
		if err = rows.Scan(&other); err != nil {
			rows.Close()
			return directfile.State{}, err
		}
		otherInfo, e := os.Stat(other)
		if e == nil && os.SameFile(info, otherInfo) {
			rows.Close()
			return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "worktree aliases another registered filesystem root")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return directfile.State{}, err
	}
	var workspaceID string
	if err = tx.QueryRowContext(ctx, `SELECT current_workspace_id FROM feature_areas WHERE id=?`, s.FeatureAreaID).Scan(&workspaceID); err != nil {
		return directfile.State{}, err
	}
	var memberCount int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM workspace_members WHERE workspace_id=?`, workspaceID).Scan(&memberCount); err != nil {
		return directfile.State{}, err
	}
	if memberCount > 1 {
		return directfile.State{}, directFileError("MULTI_REPOSITORY_DIRECT_FILE_UNSUPPORTED", "standalone workflow needs explicit repository member authorization")
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "cannot open current worktree")
	}
	openedInfo, statErr := root.Stat(".")
	if statErr != nil || !os.SameFile(info, openedInfo) {
		root.Close()
		return directfile.State{}, directFileError(directfile.WorktreeUnavailable, "worktree changed while enabling")
	}
	if directFileGrants.byDB[a.db] == nil {
		directFileGrants.byDB[a.db] = make(map[string]*directFileGrant)
	}
	if existing := directFileGrants.byDB[a.db][s.FeatureAreaID]; existing != nil {
		_ = root.Close()
		return directfile.State{}, directFileError(directfile.CapabilityActive, "a different direct file workflow is active")
	}
	grant := &directFileGrant{scope: s, projectID: projectID, repositoryID: repositoryID, workspaceID: workspaceID, worktreeID: worktreeID, canonicalPath: canonical, rootInfo: info, root: root}
	directFileGrants.byDB[a.db][s.FeatureAreaID] = grant
	if err = tx.Commit(); err != nil {
		delete(directFileGrants.byDB[a.db], s.FeatureAreaID)
		_ = root.Close()
		return directfile.State{}, err
	}
	return stateForDirectFileGrant(grant), nil
}

func stateForDirectFileGrant(g *directFileGrant) directfile.State {
	return directfile.State{State: "ACTIVE", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}, AllowedOperations: []string{"LIST", "READ", "CREATE", "REPLACE", "DELETE", "MOVE"}, Enabled: true, ProjectID: g.projectID, RepositoryID: g.repositoryID, FeatureAreaID: g.scope.FeatureAreaID, WorktreeID: g.worktreeID, WorkflowID: g.scope.WorkflowID}
}

func (a *DirectFileAuthority) Disable(ctx context.Context, s directfile.Scope) (directfile.State, error) {
	a.db.directFileOperations.Lock()
	defer a.db.directFileOperations.Unlock()
	tx, err := a.db.beginWrite(ctx)
	if err != nil {
		return directfile.State{}, err
	}
	defer tx.Rollback()
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	approved, e := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, s.WorkflowID)
	if e == nil {
		if approved.ProtocolVersion == 2 {
			return directfile.State{}, directFileError(directfile.NotEnabled, "v2 Direct File closure requires consumed CANCEL_DIRECT_FILE confirmation")
		}
		project, caller, checkErr := taskFileCaller(ctx, tx, s.AISessionID)
		if checkErr != nil {
			return directfile.State{}, checkErr
		}
		if approved.AISessionID != s.AISessionID || approved.FeatureAreaID != s.FeatureAreaID || approved.ProjectID != project || approved.caller != caller {
			return directfile.State{}, directFileError(directfile.NotEnabled, "workflow belongs to another exact owner")
		}
		if approved.State.State != "CLOSED" {
			if _, e = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='CLOSED',failure_code='',failure_reason='',updated_at=? WHERE workflow_id=?`, formatTime(time.Now().UTC()), s.WorkflowID); e != nil {
				return directfile.State{}, e
			}
		}
		approved.Enabled = false
		approved.State.State = "CLOSED"
		approved.AllowedOperations = []string{}
		return approved.State, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return directfile.State{}, e
	}
	grant := directFileGrants.byDB[a.db][s.FeatureAreaID]
	if grant == nil {
		return directfile.State{State: "UNAVAILABLE", AllowedOperations: []string{}}, nil
	}
	if grant.scope != s {
		return directfile.State{}, directFileError(directfile.NotEnabled, "this workflow does not own the capability")
	}
	delete(directFileGrants.byDB[a.db], s.FeatureAreaID)
	_ = grant.root.Close()
	if err = tx.Commit(); err != nil {
		return directfile.State{}, err
	}
	return directfile.State{State: "CLOSED", AllowedOperations: []string{}, FeatureAreaID: s.FeatureAreaID, WorkflowID: s.WorkflowID}, nil
}

func (a *DirectFileAuthority) WithCapability(ctx context.Context, s directfile.Scope, complete bool, fn func(*os.Root) error) error {
	// v2 holds this writer fence through the filesystem effect so result/cancel
	// cannot close the grant between authorization and publication.
	a.db.directFileOperations.Lock()
	defer a.db.directFileOperations.Unlock()
	tx, err := a.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	approved, e := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, s.WorkflowID)
	if e == nil {
		if approved.ProtocolVersion == 2 {
			_ = tx.Rollback()
			return a.withTaskCapabilityV2(ctx, s, approved, complete, fn)
		}
		return taskFileWithCapabilityTx(ctx, tx, s, approved, complete, fn)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	grant := directFileGrants.byDB[a.db][s.FeatureAreaID]
	if grant == nil {
		return directFileError(directfile.NotEnabled, "direct file capability is not enabled")
	}
	if grant.scope != s {
		if grant.scope.AISessionID == s.AISessionID {
			delete(directFileGrants.byDB[a.db], s.FeatureAreaID)
			_ = grant.root.Close()
		}
		return directFileError(directfile.NotEnabled, "direct file capability is not enabled")
	}
	if complete {
		defer func() { delete(directFileGrants.byDB[a.db], s.FeatureAreaID); _ = grant.root.Close() }()
	}
	_, repositoryID, worktreeID, path, err := directFileWorkspaceTx(ctx, tx, s)
	if err != nil {
		delete(directFileGrants.byDB[a.db], s.FeatureAreaID)
		_ = grant.root.Close()
		return err
	}
	var workspaceID string
	if err = tx.QueryRowContext(ctx, `SELECT current_workspace_id FROM feature_areas WHERE id=?`, s.FeatureAreaID).Scan(&workspaceID); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(canonical)
	if err != nil || statErr != nil || workspaceID != grant.workspaceID || repositoryID != grant.repositoryID || worktreeID != grant.worktreeID || canonical != grant.canonicalPath || !os.SameFile(info, grant.rootInfo) {
		delete(directFileGrants.byDB[a.db], s.FeatureAreaID)
		_ = grant.root.Close()
		return directFileError(directfile.WorktreeUnavailable, "the pinned worktree changed")
	}
	if op, ok := directfile.OperationFromContext(ctx); ok {
		if err := validateDirectFileMember(ctx, tx, op, grant.workspaceID, grant.repositoryID, grant.worktreeID); err != nil {
			return err
		}
	}
	if fn == nil {
		return directFileError(directfile.OperationFailed, "file operation is unavailable")
	}
	if err = fn(grant.root); err != nil {
		return err
	}
	return tx.Commit()
}

// withTaskCapabilityV2 separates the durable intent transaction from the
// filesystem effect. A process loss after the effect leaves PENDING/UNKNOWN
// evidence and never permits a blind replay.
func (a *DirectFileAuthority) withTaskCapabilityV2(ctx context.Context, s directfile.Scope, initial taskFileAuthorization, complete bool, fn func(*os.Root) error) error {
	if complete {
		return directFileError(directfile.OperationFailed, "v2 completion requires formal result submission")
	}
	op, ok := directfile.OperationFromContext(ctx)
	if !ok {
		return directFileError(directfile.OperationFailed, "typed file operation is required")
	}
	tx, err := a.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, s.WorkflowID)
	if err != nil {
		return err
	}
	if v.ProtocolVersion == 2 && op.RequestID != "" && op.Kind != "READ" && op.Kind != "LIST" && v.AISessionID == s.AISessionID && v.FeatureAreaID == s.FeatureAreaID {
		payload, hashErr := fileResultHash(op)
		if hashErr != nil {
			return hashErr
		}
		var savedHash, resultJSON string
		receiptErr := tx.QueryRowContext(ctx, `SELECT payload_sha256,result_json FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=? AND repository_id=? AND member_id=? AND state='COMPLETED'`, v.WorkflowID, op.RequestID, op.RepositoryID, op.MemberID).Scan(&savedHash, &resultJSON)
		if receiptErr == nil {
			if savedHash != payload {
				return directFileError(directfile.OperationFailed, "direct-file mutation receipt conflicts")
			}
			var replay directfile.Result
			if json.Unmarshal([]byte(resultJSON), &replay) != nil || replay.Path == "" {
				return directFileError(directfile.OperationFailed, "completed mutation receipt has no result proof")
			}
			directfile.SetCapturedResult(ctx, replay)
			return nil
		}
		if !errors.Is(receiptErr, sql.ErrNoRows) {
			return receiptErr
		}
	}
	if v.ProtocolVersion != 2 || v.State.State != "ACTIVE" || v.AISessionID != s.AISessionID || v.FeatureAreaID != s.FeatureAreaID || v.WorkflowID != s.WorkflowID {
		return directFileError(directfile.NotEnabled, "exact v2 file workflow is not active")
	}
	if err = validateTaskFileAuthorization(ctx, tx, v); err != nil {
		return err
	}
	if op.RepositoryID == "" || op.MemberID == "" || op.MemberVersion < 1 || op.RuntimeEpoch != v.RuntimeEpoch || op.GrantGeneration != v.GrantGeneration {
		return directFileError(directfile.NotEnabled, "exact v2 repository member and runtime fence are required")
	}
	var targetJSON, preparation string
	if err = tx.QueryRowContext(ctx, `SELECT target_json,preparation FROM direct_file_task_members WHERE workflow_id=? AND repository_id=? AND member_id=? AND member_version=?`, v.WorkflowID, op.RepositoryID, op.MemberID, op.MemberVersion).Scan(&targetJSON, &preparation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return directFileError(directfile.NotEnabled, "repository member is outside approved workset")
		}
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
	mutationHash := ""
	if mutating && (op.RequestID == "" || (op.Kind != "CREATE" && op.ExpectedDigest == "")) {
		return directFileError(directfile.NotEnabled, "v2 mutation requires exact request and source digest")
	}
	if mutating && (target.Write.Kind == "NONE" || (target.Write.Kind == "PATHS" && (!taskFileTargetPathAllowed(target, op.Path) || (op.Kind == "MOVE" && !taskFileTargetPathAllowed(target, op.Destination))))) {
		return directFileError(directfile.OutsideWorktree, "path is outside approved write scope")
	}
	member, err := taskFileMemberWorkspace(ctx, tx, v.WorkflowID, target)
	if err != nil {
		return err
	}
	if mutating {
		payload, e := fileResultHash(op)
		if e != nil {
			return e
		}
		mutationHash = payload
		var savedHash, state, resultJSON string
		e = tx.QueryRowContext(ctx, `SELECT payload_sha256,state,result_json FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, v.WorkflowID, op.RequestID).Scan(&savedHash, &state, &resultJSON)
		if e == nil {
			if savedHash != payload {
				return directFileError(directfile.OperationFailed, "direct-file mutation receipt conflicts")
			}
			if state == "COMPLETED" {
				var replay directfile.Result
				if json.Unmarshal([]byte(resultJSON), &replay) != nil {
					return directFileError(directfile.OperationFailed, "completed mutation receipt has no result proof")
				}
				directfile.SetCapturedResult(ctx, replay)
				return nil
			}
			if state == "PENDING" || state == "UNKNOWN" {
				if replay, proved := a.readMutationJournal(v.WorkflowID, op.RequestID, payload); proved {
					raw, _ := json.Marshal(replay)
					if _, updateErr := tx.ExecContext(ctx, `UPDATE direct_file_operation_receipts SET state='COMPLETED',result_json=?,error='',updated_at=? WHERE workflow_id=? AND request_id=? AND payload_sha256=? AND state IN ('PENDING','UNKNOWN')`, string(raw), formatTime(time.Now().UTC()), v.WorkflowID, op.RequestID, payload); updateErr != nil {
						return updateErr
					}
					if commitErr := tx.Commit(); commitErr != nil {
						return commitErr
					}
					directfile.SetCapturedResult(ctx, replay)
					return nil
				}
			}
			return directFileError(directfile.OperationFailed, "direct-file mutation outcome requires receipt read or recovery")
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		raw, _ := json.Marshal(op)
		now := formatTime(time.Now().UTC())
		if _, e = tx.ExecContext(ctx, `INSERT INTO direct_file_operation_receipts(workflow_id,request_id,repository_id,member_id,payload_sha256,payload_json,operation,path,destination,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'PENDING',?,?)`, v.WorkflowID, op.RequestID, op.RepositoryID, op.MemberID, payload, string(raw), op.Kind, op.Path, op.Destination, now, now); e != nil {
			return e
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	markNoEffect := func(cause error) error {
		if !mutating {
			return cause
		}
		final, beginErr := a.db.beginWrite(context.WithoutCancel(ctx))
		if beginErr != nil {
			return beginErr
		}
		defer final.Rollback()
		if _, beginErr = final.ExecContext(ctx, `UPDATE direct_file_operation_receipts SET state='FAILED',error=?,updated_at=? WHERE workflow_id=? AND request_id=? AND state='PENDING'`, cause.Error(), formatTime(time.Now().UTC()), v.WorkflowID, op.RequestID); beginErr != nil {
			return beginErr
		}
		if beginErr = final.Commit(); beginErr != nil {
			return beginErr
		}
		return cause
	}
	root, err := os.OpenRoot(member.WorktreePath)
	if err != nil {
		return markNoEffect(directFileError(directfile.WorktreeUnavailable, "cannot open approved member root"))
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return markNoEffect(err)
	}
	identity, err := taskFileRootIdentity(info)
	if err != nil || identity != member.RootIdentity {
		return markNoEffect(directFileError(directfile.WorktreeUnavailable, "approved member root changed while opening"))
	}
	if mutating && op.Kind != "CREATE" {
		f, e := root.Open(op.Path)
		if e != nil {
			return markNoEffect(directFileError(directfile.OperationFailed, "cannot prove mutation source state"))
		}
		bytes, e := io.ReadAll(io.LimitReader(f, directfile.MaxReadBytes+1))
		_ = f.Close()
		if e != nil {
			return markNoEffect(e)
		}
		if len(bytes) > directfile.MaxReadBytes {
			return markNoEffect(directFileError(directfile.TooLarge, "mutation source exceeds bounded digest limit"))
		}
		sum := sha256.Sum256(bytes)
		if op.ExpectedDigest != fmt.Sprintf("%x", sum[:]) {
			return markNoEffect(directFileError(directfile.OperationFailed, "mutation source digest changed"))
		}
	}
	directfile.SetPathFilter(ctx, func(p string, list bool) bool {
		return taskFileTargetPathAllowed(target, p) || (list && taskFileTargetListAllowed(target, p))
	})
	if fn == nil {
		return markNoEffect(directFileError(directfile.OperationFailed, "file operation unavailable"))
	}
	operationErr := fn(root)
	if operationErr == nil && mutating {
		captured, ok := directfile.CapturedResult(ctx)
		if !ok || captured.Path == "" {
			return directFileError(directfile.OperationFailed, "file mutation produced no durable result")
		}
		if err := a.writeMutationJournal(v.WorkflowID, op.RequestID, mutationHash, captured); err != nil {
			return err
		}
	}
	if operationErr == nil && mutating {
		a.prepareMu.RLock()
		hook := a.afterFileEffect
		a.prepareMu.RUnlock()
		if hook != nil {
			if e := hook(); e != nil {
				return e
			}
		}
	}
	final, err := a.db.beginWrite(context.WithoutCancel(ctx))
	if err != nil {
		return err
	}
	defer final.Rollback()
	state, message := "COMPLETED", ""
	if operationErr != nil {
		state, message = "FAILED", operationErr.Error()
	}
	if mutating {
		resultJSON := "{}"
		if operationErr == nil {
			if captured, ok := directfile.CapturedResult(ctx); ok {
				raw, marshalErr := json.Marshal(captured)
				if marshalErr != nil {
					return marshalErr
				}
				resultJSON = string(raw)
			}
		}
		if _, err = final.ExecContext(ctx, `UPDATE direct_file_operation_receipts SET state=?,result_json=?,error=?,updated_at=? WHERE workflow_id=? AND request_id=? AND state='PENDING'`, state, resultJSON, message, formatTime(time.Now().UTC()), v.WorkflowID, op.RequestID); err != nil {
			return err
		}
	}
	if operationErr == nil && mutating {
		now := formatTime(time.Now().UTC())
		if _, err = final.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET started_at=COALESCE(started_at,?),last_operation=?,operation_count=operation_count+1,updated_at=? WHERE workflow_id=? AND state='ACTIVE'`, now, op.Kind, now, v.WorkflowID); err != nil {
			return err
		}
		if _, err = final.ExecContext(ctx, `UPDATE tasks SET phase='IMPLEMENTING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='IMPLEMENTATION_READY' AND current_definition_revision_id=?`, now, v.TaskID, v.TaskDefinitionRevisionID); err != nil {
			return err
		}
	}
	if err = final.Commit(); err != nil {
		return err
	}
	if operationErr != nil {
		return operationErr
	}
	if complete {
		return directFileError(directfile.OperationFailed, "v2 completion requires formal result")
	}
	return nil
}

func (a *DirectFileAuthority) Status(ctx context.Context, sessionID, featureAreaID string) (directfile.State, error) {
	projectID, _, scopeErr := taskFileCaller(ctx, a.db.sql, sessionID)
	if scopeErr != nil {
		return directfile.State{}, scopeErr
	}
	approved, e := readTaskFileAuthorization(ctx, a.db.sql, ` WHERE ai_session_id=? AND project_id=? AND feature_area_id=? AND task_definition_revision_id=(SELECT current_definition_revision_id FROM tasks WHERE id=direct_file_task_authorizations.task_id) ORDER BY created_at DESC LIMIT 1`, sessionID, projectID, featureAreaID)
	if e == nil {
		return currentTaskFileState(ctx, a.db.sql, approved)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return directfile.State{}, e
	}
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	g := directFileGrants.byDB[a.db][featureAreaID]
	if g == nil || g.projectID != projectID {
		return directfile.State{State: "UNAVAILABLE", AllowedOperations: []string{}}, nil
	}
	return stateForDirectFileGrant(g), nil
}

var _ directfile.Authority = (*DirectFileAuthority)(nil)

// Each supplied repository/member is compared with the durable grant. Omitted
// identities remain supported only for historical grants already pinned to one
// repository; membership never authorizes selecting a different root.
func validateDirectFileMember(ctx context.Context, q definitionQuerier, op directfile.Operation, workspaceID, repositoryID, worktreeID string) error {
	if op.RepositoryID != "" && op.RepositoryID != repositoryID {
		return directFileError(directfile.NotEnabled, "repository differs from the exact file grant")
	}
	if op.DestinationRepositoryID != "" && op.DestinationRepositoryID != repositoryID {
		return directFileError("CROSS_REPOSITORY_MOVE_UNSUPPORTED", "destination repository requires a separate authorization")
	}
	if op.MemberID != "" || op.MemberVersion != 0 || op.DestinationMemberID != "" {
		var id string
		var version int
		if err := q.QueryRowContext(ctx, `SELECT id,identity_version FROM workspace_members WHERE workspace_id=? AND repository_id=? AND worktree_id=? AND preparation='READY'`, workspaceID, repositoryID, worktreeID).Scan(&id, &version); err != nil {
			return directFileError(directfile.WorktreeUnavailable, "exact authorized workspace member unavailable")
		}
		if (op.MemberID != "" && op.MemberID != id) || (op.MemberVersion != 0 && op.MemberVersion != version) || (op.DestinationMemberID != "" && op.DestinationMemberID != id) {
			return directFileError(directfile.NotEnabled, "member differs from the exact file grant")
		}
	}
	return nil
}
