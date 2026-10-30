package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func fileResultHash(v any) (string, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func fileResultIDs(ids ...string) bool {
	for _, id := range ids {
		if id == "" || len(id) > 128 || strings.TrimSpace(id) != id {
			return false
		}
	}
	return true
}
func fileResultConflict() error {
	return directfile.Failure("FILE_RESULT_CONFLICT", "exact file result identity, authorization or lifecycle changed")
}

func (s *WorkflowProgressionStore) GetFileImplementationResult(ctx context.Context, in directfile.ImplementationResultReadInput) (out directfile.ImplementationResult, err error) {
	if !fileResultIDs(in.AISessionID, in.TaskID, in.TaskDefinitionRevisionID, in.WorkflowID) {
		return out, fileResultConflict()
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	project, caller, err := taskFileCaller(ctx, tx, in.AISessionID)
	if err != nil {
		return out, err
	}
	grant, err := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, in.WorkflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if grant.ProjectID != project || grant.caller != caller || grant.AISessionID != in.AISessionID || grant.TaskID != in.TaskID || grant.TaskDefinitionRevisionID != in.TaskDefinitionRevisionID {
		return out, fileResultConflict()
	}
	// Historical results retain exact ownership even after the current revision,
	// grant state, runtime epoch, or Task lifecycle advances. No fresh grant
	// validation, workspace access, or command replay belongs to this read.
	var body string
	var accepted bool
	err = tx.QueryRowContext(ctx, `SELECT r.result_json,EXISTS(SELECT 1 FROM direct_file_implementation_acceptances a WHERE a.result_id=r.id AND a.ai_session_id=r.ai_session_id) FROM direct_file_implementation_results r JOIN tasks t ON t.id=r.task_id AND t.project_id=r.project_id JOIN task_definition_revisions d ON d.id=r.revision_id AND d.task_id=t.id WHERE r.ai_session_id=? AND r.project_id=? AND r.task_id=? AND r.revision_id=? AND r.workflow_id=?`, in.AISessionID, project, in.TaskID, in.TaskDefinitionRevisionID, in.WorkflowID).Scan(&body, &accepted)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(body), &out); err != nil {
		return out, err
	}
	if out.AISessionID != in.AISessionID || out.TaskID != in.TaskID || out.TaskDefinitionRevisionID != in.TaskDefinitionRevisionID || out.WorkflowID != in.WorkflowID {
		return directfile.ImplementationResult{}, fileResultConflict()
	}
	if err = projectFileVerification(ctx, tx, &out); err != nil {
		return out, err
	}
	out.Accepted = accepted
	return out, nil
}

func (s *WorkflowProgressionStore) SubmitFileImplementationResult(ctx context.Context, in directfile.ImplementationResultInput) (out directfile.ImplementationResult, err error) {
	s.db.directFileOperations.Lock()
	defer s.db.directFileOperations.Unlock()
	if !fileResultIDs(in.AISessionID, in.TaskID, in.TaskDefinitionRevisionID, in.WorkflowID, in.RequestID) || strings.TrimSpace(in.Summary) == "" || len(in.Summary) > 65536 || len(in.VerificationNotes) > 65536 || !utf8.ValidString(in.Summary) || !utf8.ValidString(in.VerificationNotes) {
		return out, fileResultConflict()
	}
	hash, err := fileResultHash(in)
	if err != nil {
		return out, err
	}
	if in.WorkerClaim != nil {
		if prior, found, readErr := s.replayFileWorkerResult(ctx, in, hash); readErr != nil || found {
			return prior, readErr
		}
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	project, _, err := taskFileCaller(ctx, tx, in.AISessionID)
	if err != nil {
		return out, err
	}
	var stored, body string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256,result_json FROM direct_file_implementation_results WHERE ai_session_id=? AND request_id=?`, in.AISessionID, in.RequestID).Scan(&stored, &body)
	if err == nil {
		if stored != hash {
			return out, fileResultConflict()
		}
		if err = requireDirectFileWorkerReplayTx(ctx, tx, in.WorkflowID, in.AISessionID, in.WorkerClaim); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(body), &out); err != nil {
			return out, err
		}
		if err = projectFileVerification(ctx, tx, &out); err != nil {
			return out, err
		}
		out.Replay = true
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	grant, err := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, in.WorkflowID)
	if err != nil {
		return out, err
	}
	if grant.ProjectID != project || grant.AISessionID != in.AISessionID || grant.TaskID != in.TaskID || grant.TaskDefinitionRevisionID != in.TaskDefinitionRevisionID || (grant.State.State != "ACTIVE" && grant.State.State != "CLOSED") {
		return out, fileResultConflict()
	}
	if grant.ProtocolVersion == 2 {
		return s.submitMultiFileImplementationResult(ctx, tx, project, grant, in, hash)
	}
	if in.WorkerClaim != nil {
		return out, fileWorkerConflict()
	}
	if err = validateTaskFileAuthorization(ctx, tx, grant); err != nil {
		return out, err
	}
	revision, err := getDefinitionRevision(ctx, tx, in.TaskDefinitionRevisionID)
	if err != nil {
		return out, err
	}
	if revision.Definition.WorkSpec.ImplementationMode != task.ImplementationDirectFile {
		return out, fileResultConflict()
	}
	workspace, worktree, path, identity, err := taskFileWorkspace(ctx, tx, grant)
	if err != nil {
		return out, err
	}
	if workspace != grant.WorkspaceID || worktree != grant.WorktreeID || path != grant.canonical || identity != grant.rootIdentity {
		return out, fileResultConflict()
	}
	g := git.NewClient(git.Config{})
	if err = g.NoOperation(ctx, path); err != nil {
		return out, err
	}
	snapshot, err := g.Snapshot(ctx, path)
	if err != nil {
		return out, err
	}
	out = directfile.ImplementationResult{ID: "file_result_" + hash[:32], AISessionID: in.AISessionID, TaskID: in.TaskID, TaskDefinitionRevisionID: in.TaskDefinitionRevisionID, WorkflowID: in.WorkflowID, RequestID: in.RequestID, Summary: in.Summary, VerificationNotes: in.VerificationNotes, VerificationStatus: "NOT_RUN", Snapshot: snapshot}
	encoded, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	now := s.now()
	_, err = tx.ExecContext(ctx, `INSERT INTO direct_file_implementation_results(id,ai_session_id,project_id,task_id,revision_id,workflow_id,request_id,payload_sha256,result_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, out.ID, in.AISessionID, project, in.TaskID, in.TaskDefinitionRevisionID, in.WorkflowID, in.RequestID, hash, string(encoded), formatTime(now))
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='CLOSED',updated_at=? WHERE workflow_id=? AND state='ACTIVE'`, formatTime(now), in.WorkflowID); err != nil {
		return out, err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, grant.FeatureAreaID); err != nil {
		return out, err
	}
	if err = createFileVerificationTx(ctx, tx, &out, now); err != nil {
		return out, err
	}
	phase := "ACCEPTANCE_PENDING"
	if out.ExecutionID != "" {
		phase = "VERIFICATION_READY"
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase=?,updated_at=? WHERE id=? AND current_definition_revision_id=? AND status='ACTIVE' AND phase IN ('IMPLEMENTATION_READY','IMPLEMENTING') AND current_execution_id IS ?`, phase, formatTime(now), in.TaskID, in.TaskDefinitionRevisionID, nullString(out.ExecutionID))
	if err != nil {
		return out, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return out, fileResultConflict()
	}
	payload, _ := event.Payload(map[string]any{"file_result_id": out.ID, "verification_status": out.VerificationStatus, "phase": phase, "execution_id": out.ExecutionID})
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: project, TaskID: in.TaskID, EntityType: "task", EntityID: in.TaskID, Payload: payload, CreatedAt: now}); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *WorkflowProgressionStore) submitMultiFileImplementationResult(ctx context.Context, tx *sql.Tx, project string, grant taskFileAuthorization, in directfile.ImplementationResultInput, hash string) (out directfile.ImplementationResult, err error) {
	if in.ProtocolVersion != 2 || in.RuntimeEpoch != grant.RuntimeEpoch || in.GrantGeneration != grant.GrantGeneration {
		return out, fileResultConflict()
	}
	// Exact recorded replay was handled before entering this fresh-result path.
	// Only v2 requires live authority here; v1 retains close-before-submit semantics.
	if grant.State.State != "ACTIVE" {
		return out, directfile.Failure(directfile.NotEnabled, "new v2 file result requires active authorization")
	}
	if err = validateTaskFileAuthorization(ctx, tx, grant); err != nil {
		return out, err
	}
	if err = requireDirectFileWorkerTx(ctx, tx, grant, in.WorkerClaim); err != nil {
		return out, err
	}
	var unresolved bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_operation_receipts WHERE workflow_id=? AND state IN ('PENDING','UNKNOWN'))`, grant.WorkflowID).Scan(&unresolved); err != nil {
		return out, err
	}
	if unresolved {
		return out, directfile.Failure(directfile.OperationFailed, "unresolved file mutation receipt blocks formal result submission")
	}
	revision, err := getDefinitionRevision(ctx, tx, in.TaskDefinitionRevisionID)
	if err != nil {
		return out, err
	}
	if revision.Definition.Version != 2 || revision.Definition.WorkSpec.ImplementationMode != task.ImplementationDirectFile || !sameMemberTargets(grant.RepositoryTargets, revision.Definition.RepositoryTargets) {
		return out, fileResultConflict()
	}
	members, err := multiResultMembers(ctx, tx, grant)
	if err != nil {
		return out, err
	}
	now := s.now()
	out = directfile.ImplementationResult{ID: "file_result_" + hash[:32], AISessionID: in.AISessionID, TaskID: in.TaskID, TaskDefinitionRevisionID: in.TaskDefinitionRevisionID, WorkflowID: in.WorkflowID, RequestID: in.RequestID, Summary: in.Summary, VerificationNotes: in.VerificationNotes, VerificationStatus: "NOT_RUN", ProtocolVersion: 2, RuntimeEpoch: grant.RuntimeEpoch, GrantGeneration: grant.GrantGeneration, Members: members, RepositoryTargets: workset.CloneTargets(revision.Definition.RepositoryTargets)}
	// A multi-root result has no fake aggregate snapshot. The per-member end
	// snapshots are authoritative and persisted in result_json.
	encoded, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO direct_file_implementation_results(id,ai_session_id,project_id,task_id,revision_id,workflow_id,request_id,payload_sha256,result_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, out.ID, in.AISessionID, project, in.TaskID, in.TaskDefinitionRevisionID, in.WorkflowID, in.RequestID, hash, string(encoded), formatTime(now)); err != nil {
		return out, err
	}
	if err = completeDirectFileWorkerTx(ctx, tx, grant, in.WorkerClaim, out.ID); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='CLOSED',updated_at=? WHERE workflow_id=? AND protocol_version=2 AND state='ACTIVE'`, formatTime(now), in.WorkflowID); err != nil {
		return out, err
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, grant.FeatureAreaID); err != nil {
		return out, err
	}
	if err = createFileVerificationTx(ctx, tx, &out, now); err != nil {
		return out, err
	}
	phase := "ACCEPTANCE_PENDING"
	if out.ExecutionID != "" {
		phase = "VERIFICATION_READY"
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase=?,updated_at=? WHERE id=? AND current_definition_revision_id=? AND status='ACTIVE' AND phase IN ('IMPLEMENTATION_READY','IMPLEMENTING') AND current_execution_id IS ?`, phase, formatTime(now), in.TaskID, in.TaskDefinitionRevisionID, nullString(out.ExecutionID))
	if err != nil {
		return out, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return out, fileResultConflict()
	}
	payload, _ := event.Payload(map[string]any{"file_result_id": out.ID, "verification_status": out.VerificationStatus, "phase": phase, "execution_id": out.ExecutionID})
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: project, TaskID: in.TaskID, EntityType: "task", EntityID: in.TaskID, Payload: payload, CreatedAt: now}); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func multiResultMembers(ctx context.Context, tx *sql.Tx, grant taskFileAuthorization) ([]directfile.MemberResult, error) {
	members := make([]directfile.MemberResult, 0, len(grant.RepositoryTargets))
	g := git.NewClient(git.Config{})
	for _, memberGrant := range grant.RepositoryTargets {
		member, err := taskFileMemberWorkspace(ctx, tx, grant.WorkflowID, memberGrant.Target)
		if err != nil {
			return nil, err
		}
		if err = g.NoOperation(ctx, member.WorktreePath); err != nil {
			return nil, err
		}
		head, headErr := g.ResolveHEAD(ctx, member.WorktreePath)
		if headErr != nil {
			return nil, headErr
		}
		if head != member.StartHead {
			return nil, directfile.Failure(directfile.OperationFailed, "member HEAD changed outside Direct File")
		}
		member.EndSnapshot, err = g.Snapshot(ctx, member.WorktreePath)
		if err != nil {
			return nil, err
		}
		member.ChangedPaths, err = changedPathsBetweenSnapshots(ctx, member.WorktreePath, member.StartSnapshot, member.EndSnapshot)
		if err != nil {
			return nil, err
		}
		if memberGrant.Write.Kind == "NONE" && len(member.ChangedPaths) != 0 {
			return nil, directfile.Failure(directfile.OperationFailed, "read-only member changed")
		}
		if memberGrant.Write.Kind == "PATHS" {
			for _, path := range member.ChangedPaths {
				if !taskFileTargetPathAllowed(memberGrant.Target, path) {
					return nil, directfile.Failure(directfile.OutsideWorktree, "actual changed path is outside approved write scope")
				}
			}
		}
		if err = validateMemberPostState(ctx, tx, grant.WorkflowID, member); err != nil {
			return nil, err
		}
		member.State = "COMPLETED"
		if len(member.ChangedPaths) == 0 {
			member.State = "NO_CHANGE"
		}
		members = append(members, member)
	}
	return members, nil
}

// validateMemberPostState binds each final changed pathname to the durable
// post-effect Result written before receipt completion. It detects a later
// external overwrite at an otherwise authorized path.
func validateMemberPostState(ctx context.Context, tx *sql.Tx, workflowID string, member directfile.MemberResult) error {
	rows, err := tx.QueryContext(ctx, `SELECT path,destination,result_json FROM direct_file_operation_receipts WHERE workflow_id=? AND repository_id=? AND member_id=? AND state='COMPLETED' ORDER BY created_at`, workflowID, member.RepositoryID, member.MemberID)
	if err != nil {
		return err
	}
	defer rows.Close()
	proofs := map[string]directfile.Result{}
	for rows.Next() {
		var path, destination, raw string
		if err = rows.Scan(&path, &destination, &raw); err != nil {
			return err
		}
		var result directfile.Result
		if json.Unmarshal([]byte(raw), &result) != nil {
			return directfile.Failure(directfile.OperationFailed, "completed mutation has no result proof")
		}
		if result.Path != "" {
			proofs[result.Path] = result
		}
		if path != "" && path != result.Path {
			proofs[path] = directfile.Result{Path: path, Digest: "ABSENT"}
		}
		if destination != "" {
			proofs[destination] = result
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, path := range member.ChangedPaths {
		proof, ok := proofs[path]
		if !ok || proof.Digest == "" {
			return directfile.Failure(directfile.OperationFailed, "changed path lacks durable post-state proof")
		}
		actual, readErr := os.ReadFile(filepath.Join(member.WorktreePath, filepath.FromSlash(path)))
		if proof.Digest == "ABSENT" {
			if readErr == nil {
				return directfile.Failure(directfile.OperationFailed, "deleted path reappeared after receipt")
			}
			if os.IsNotExist(readErr) {
				continue
			}
			return readErr
		}
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(actual)
		if hex.EncodeToString(sum[:]) != proof.Digest {
			return directfile.Failure(directfile.OperationFailed, "final file bytes differ from durable mutation proof")
		}
	}
	return nil
}

func changedPathsBetweenSnapshots(ctx context.Context, root, start, end string) ([]string, error) {
	if start == end {
		return []string{}, nil
	}
	command := exec.CommandContext(ctx, "git", "-C", root, "diff", "--name-only", "-z", "--no-renames", start, end)
	raw, err := command.Output()
	if err != nil {
		return nil, err
	}
	values := strings.Split(string(raw), "\x00")
	if len(values) > 0 && values[len(values)-1] == "" {
		values = values[:len(values)-1]
	}
	return uniqueFileResultPaths(values), nil
}

func (s *WorkflowProgressionStore) AcceptFileImplementationResult(ctx context.Context, in directfile.ImplementationAcceptInput) (out directfile.ImplementationResult, err error) {
	s.db.directFileOperations.Lock()
	defer s.db.directFileOperations.Unlock()
	if !fileResultIDs(in.AISessionID, in.TaskID, in.TaskDefinitionRevisionID, in.WorkflowID, in.ResultID, in.RequestID) {
		return out, fileResultConflict()
	}
	hash, err := fileResultHash(in)
	if err != nil {
		return out, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	project, _, err := taskFileCaller(ctx, tx, in.AISessionID)
	if err != nil {
		return out, err
	}
	var stored, resultID string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256,result_id FROM direct_file_implementation_acceptances WHERE ai_session_id=? AND request_id=?`, in.AISessionID, in.RequestID).Scan(&stored, &resultID)
	replay := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if replay && (stored != hash || resultID != in.ResultID) {
		return out, fileResultConflict()
	}
	var body string
	err = tx.QueryRowContext(ctx, `SELECT result_json FROM direct_file_implementation_results WHERE id=? AND ai_session_id=? AND project_id=? AND task_id=? AND revision_id=? AND workflow_id=?`, in.ResultID, in.AISessionID, project, in.TaskID, in.TaskDefinitionRevisionID, in.WorkflowID).Scan(&body)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal([]byte(body), &out); err != nil {
		return out, err
	}
	if err = projectFileVerification(ctx, tx, &out); err != nil {
		return out, err
	}
	if replay {
		if err = validateCompletionReplay(ctx, tx, in.TaskID, in.RequestID, in.CompletionPlan); err != nil {
			return out, err
		}
		out.Accepted = true
		out.Replay = true
		return out, nil
	}
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, in.TaskID, project))
	if err != nil {
		return out, err
	}
	if current.Status != task.StatusActive || current.Phase != task.PhaseAcceptancePending || current.CurrentDefinitionRevisionID != in.TaskDefinitionRevisionID || current.LegacyReadOnly {
		return out, fileResultConflict()
	}
	revision, err := getDefinitionRevision(ctx, tx, in.TaskDefinitionRevisionID)
	if err != nil {
		return out, err
	}
	if revision.Definition.WorkSpec.ImplementationMode != task.ImplementationDirectFile {
		return out, fileResultConflict()
	}
	if err = requireFileVerificationEvidenceTx(ctx, tx, current, out); err != nil {
		return out, err
	}
	grant, err := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, in.WorkflowID)
	if err != nil {
		return out, err
	}
	if grant.State.State != "CLOSED" || grant.ProjectID != project || grant.AISessionID != in.AISessionID || grant.TaskID != current.ID || grant.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || grant.DecisionID != current.ReviewApprovalID {
		return out, fileResultConflict()
	}
	if in.CompletionPlan != nil {
		if in.CompletionPlan.TaskID != current.ID || in.CompletionPlan.RevisionID != current.CurrentDefinitionRevisionID || in.CompletionPlan.ExecutionID != current.CurrentExecutionID {
			return out, fileResultConflict()
		}
		proposal, e := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, current.CurrentExecutionID))
		if e != nil {
			return out, e
		}
		if e = requireCompletionVerificationTx(ctx, tx, proposal); e != nil {
			return out, e
		}
		if e = reserveTaskCompletionTx(ctx, tx, *in.CompletionPlan, in.RequestID); e != nil {
			return out, e
		}
	}
	if err = recordFileVerificationAcceptanceTx(ctx, tx, current, in, s.now()); err != nil {
		return out, err
	}
	if grant.ProtocolVersion == 2 {
		return s.acceptMultiFileImplementationResult(ctx, tx, project, current, revision, grant, in, hash, out)
	}
	_, caller, callerErr := taskFileCaller(ctx, tx, in.AISessionID)
	if callerErr != nil {
		return out, callerErr
	}
	if caller != grant.caller {
		return out, fileResultConflict()
	}
	var approved bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_approval_materializations m JOIN review_approval_decisions d ON d.decision_id=m.decision_id JOIN decision_resolution_receipts r ON r.decision_id=m.decision_id WHERE m.decision_id=? AND m.task_id=? AND m.initial_revision_id=? AND m.ai_session_id=? AND d.review_id=? AND d.project_id=? AND r.option='APPROVE' AND r.subject_id=? AND r.result_id=? AND r.project_id=?)`, current.ReviewApprovalID, current.ID, current.CurrentDefinitionRevisionID, in.AISessionID, current.CurrentReviewID, project, current.ID, current.ID, project).Scan(&approved); err != nil {
		return out, err
	}
	if !approved {
		return out, fileResultConflict()
	}
	if err = directFileAdmissionTx(ctx, tx, s.db, current.FeatureAreaID); err != nil {
		return out, err
	}
	if err = commitBusy(ctx, tx, current.FeatureAreaID); err != nil {
		return out, err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND project_id=? AND status='ACTIVE' AND legacy_read_only=0)`, current.FeatureAreaID, project).Scan(&active); err != nil || !active {
		return out, fileResultConflict()
	}
	workspace, worktree, path, identity, err := taskFileWorkspace(ctx, tx, grant)
	if err != nil {
		return out, err
	}
	if workspace != grant.WorkspaceID || worktree != grant.WorktreeID || path != grant.canonical || identity != grant.rootIdentity {
		return out, fileResultConflict()
	}
	g := git.NewClient(git.Config{})
	if err = g.NoOperation(ctx, path); err != nil {
		return out, err
	}
	snapshot, err := g.Snapshot(ctx, path)
	if err != nil {
		return out, err
	}
	if snapshot != out.Snapshot {
		return out, git.ErrSnapshotDrift
	}
	now := s.now()
	_, err = tx.ExecContext(ctx, `INSERT INTO direct_file_implementation_acceptances(ai_session_id,request_id,payload_sha256,result_id,created_at) VALUES(?,?,?,?,?)`, in.AISessionID, in.RequestID, hash, in.ResultID, formatTime(now))
	if err != nil {
		return out, err
	}
	admitted, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='DELIVERING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='ACCEPTANCE_PENDING' AND current_definition_revision_id=?`, formatTime(now), current.ID, in.TaskDefinitionRevisionID)
	if err != nil {
		return out, err
	}
	if affected, _ := admitted.RowsAffected(); affected != 1 {
		return out, fileResultConflict()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_commit_receipts(task_id,feature_area_id,revision_id,verified_snapshot,workspace_id,workspace_path,branch) SELECT ?,?,?,?,?,?,branch FROM worktrees WHERE id=?`, current.ID, current.FeatureAreaID, in.TaskDefinitionRevisionID, out.Snapshot, workspace, path, worktree)
	if err != nil {
		return out, err
	}
	payload, _ := event.Payload(map[string]any{"file_result_id": out.ID, "from": "ACTIVE", "to": "ACTIVE", "phase_from": "ACCEPTANCE_PENDING", "phase_to": "DELIVERING", "verification_status": "NOT_RUN"})
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: project, TaskID: current.ID, EntityType: "task", EntityID: current.ID, Payload: payload, CreatedAt: now}); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.Accepted = true
	if _, commitErr := s.RetryTaskCommit(context.WithoutCancel(ctx), project, current.ID); commitErr != nil {
		out.CommitError = commitErr.Error()
	}
	return out, nil
}

func (s *WorkflowProgressionStore) acceptMultiFileImplementationResult(ctx context.Context, tx *sql.Tx, project string, current task.Task, revision task.DefinitionRevision, grant taskFileAuthorization, in directfile.ImplementationAcceptInput, hash string, out directfile.ImplementationResult) (directfile.ImplementationResult, error) {
	if in.ProtocolVersion != 2 || in.RuntimeEpoch != grant.RuntimeEpoch || in.GrantGeneration != grant.GrantGeneration || out.ProtocolVersion != 2 || out.RuntimeEpoch != grant.RuntimeEpoch || out.GrantGeneration != grant.GrantGeneration || !sameMemberTargets(grant.RepositoryTargets, revision.Definition.RepositoryTargets) || !reflect.DeepEqual(out.RepositoryTargets, revision.Definition.RepositoryTargets) {
		return out, fileResultConflict()
	}
	observed, err := multiResultMembers(ctx, tx, grant)
	if err != nil {
		return out, err
	}
	if !sameResultMembers(out.Members, observed) {
		return out, fileResultConflict()
	}
	// Stored JSON intentionally omits local root paths/inode identities.  The
	// delivery intent receives only this freshly re-resolved frozen authority.
	out.Members = observed
	now := s.now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO direct_file_implementation_acceptances(ai_session_id,request_id,payload_sha256,result_id,created_at) VALUES(?,?,?,?,?)`, in.AISessionID, in.RequestID, hash, in.ResultID, formatTime(now)); err != nil {
		return out, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='DELIVERING',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='ACCEPTANCE_PENDING' AND current_definition_revision_id=?`, formatTime(now), current.ID, in.TaskDefinitionRevisionID)
	if err != nil {
		return out, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return out, fileResultConflict()
	}
	if err = insertMultiTaskDeliveryTx(ctx, tx, current, out, now); err != nil {
		return out, err
	}
	payload, _ := event.Payload(map[string]any{"file_result_id": out.ID, "from": "ACTIVE", "to": "ACTIVE", "phase_from": "ACCEPTANCE_PENDING", "phase_to": "DELIVERING", "verification_status": "NOT_RUN"})
	if err = appendEvent(ctx, tx, event.Event{Type: event.TaskStatusChanged, ProjectID: project, TaskID: current.ID, EntityType: "task", EntityID: current.ID, Payload: payload, CreatedAt: now}); err != nil {
		return out, err
	}
	if err = tx.Commit(); err != nil {
		return out, err
	}
	out.Accepted = true
	if _, commitErr := s.RetryTaskCommit(context.WithoutCancel(ctx), project, current.ID); commitErr != nil {
		out.CommitError = commitErr.Error()
	}
	return out, nil
}

func sameResultMembers(left, right []directfile.MemberResult) bool {
	if len(left) != len(right) {
		return false
	}
	byRepository := make(map[string]directfile.MemberResult, len(right))
	for _, value := range right {
		byRepository[value.RepositoryID] = value
	}
	for _, value := range left {
		other, ok := byRepository[value.RepositoryID]
		if !ok || value.MemberID != other.MemberID || value.MemberVersion != other.MemberVersion || value.WorkspaceID != other.WorkspaceID || value.WorktreeID != other.WorktreeID || value.OriginCommit != other.OriginCommit || value.StartHead != other.StartHead || value.StartSnapshot != other.StartSnapshot || value.EndSnapshot != other.EndSnapshot || !reflect.DeepEqual(uniqueFileResultPaths(value.ChangedPaths), uniqueFileResultPaths(other.ChangedPaths)) {
			return false
		}
	}
	return true
}

func uniqueFileResultPaths(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
