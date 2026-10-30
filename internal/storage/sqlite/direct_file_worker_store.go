package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
)

// DirectFileWorkerStore owns delivery correlation subordinate to an already
// started v2 file authorization. It never creates a Run, grant or acceptance.
type DirectFileWorkerStore struct{ db *DB }

func NewDirectFileWorkerStore(db *DB) *DirectFileWorkerStore { return &DirectFileWorkerStore{db: db} }
func (s *DirectFileWorkerStore) ClaimFileImplementationWorker(ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
	return s.claim(ctx, in, true)
}
func (s *DirectFileWorkerStore) GetFileImplementationWorker(ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
	return s.claim(ctx, in, false)
}

func fileWorkerConflict() error {
	return directfile.Failure(directfile.NotEnabled, "exact file worker identity or authority is unavailable")
}

const fileWorkerRecoveryReason = "same-workflow worker continuation is unsupported; existing explicit cancellation may revoke Core file authority after unresolved file effects are reconciled; any successor requires separate authorization; external worker termination is not proven"

func directFileWorkerScope(ctx context.Context, q definitionQuerier, session string) (string, error) {
	scope, ok := aisession.CallerScopeFromContext(ctx)
	if !ok || scope == "" {
		return "", fileWorkerConflict()
	}
	var matches bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_sessions WHERE id=? AND caller_scope=?)`, session, scope).Scan(&matches); err != nil {
		return "", err
	}
	if !matches {
		return "", fileWorkerConflict()
	}
	return scope, nil
}
func fileWorkerTarget(v taskFileAuthorization) directfile.WorkerTarget {
	return directfile.WorkerTarget{AISessionID: v.AISessionID, FeatureAreaID: v.FeatureAreaID, TaskID: v.TaskID, TaskDefinitionRevisionID: v.TaskDefinitionRevisionID, DecisionID: v.DecisionID, WorkflowID: v.WorkflowID, ProtocolVersion: v.ProtocolVersion, RuntimeEpoch: v.RuntimeEpoch, GrantGeneration: v.GrantGeneration}
}
func validFileWorkerTarget(v directfile.WorkerTarget) bool {
	return v.ProtocolVersion == 2 && v.GrantGeneration > 0 && fileResultIDs(v.AISessionID, v.FeatureAreaID, v.TaskID, v.TaskDefinitionRevisionID, v.DecisionID, v.WorkflowID, v.RuntimeEpoch)
}

// Frozen metadata only: operation count, observed end snapshots and mutable
// readiness timestamps are deliberately excluded from the binding.
func directFileWorkerBinding(ctx context.Context, q definitionQuerier, v taskFileAuthorization) (task.DefinitionRevision, string, error) {
	revision, err := getDefinitionRevision(ctx, q, v.TaskDefinitionRevisionID)
	if err != nil {
		return revision, "", err
	}
	if revision.TaskID != v.TaskID || revision.Definition.Version != 2 || revision.Definition.WorkSpec.ImplementationMode != task.ImplementationDirectFile || !sameMemberTargets(v.RepositoryTargets, revision.Definition.RepositoryTargets) {
		return revision, "", fileWorkerConflict()
	}
	var raw string
	err = q.QueryRowContext(ctx, `SELECT json_array(g.workflow_id,g.ai_session_id,g.caller_scope,g.project_id,g.feature_area_id,g.task_id,g.task_definition_revision_id,g.decision_id,g.implementation_mode,g.registration_kind,g.execution_scope_json,g.protocol_version,g.repository_targets_json,g.runtime_epoch,g.generation,g.workspace_id,g.start_request_id,g.confirmation_intent_id,
 COALESCE((SELECT json_group_array(json_array(repository_id,member_id,member_version,target_json,workspace_id,worktree_id,origin_commit,canonical_path,root_identity,start_head,start_snapshot)) FROM (SELECT * FROM direct_file_task_members WHERE workflow_id=g.workflow_id ORDER BY repository_id)), '[]')) FROM direct_file_task_authorizations g WHERE g.workflow_id=?`, v.WorkflowID).Scan(&raw)
	if err != nil {
		return revision, "", err
	}
	return revision, workerDigest([]byte(revision.PayloadSHA256 + "\n" + raw)), nil
}
func directFileWorkerReplay(ctx context.Context, q definitionQuerier, in directfile.WorkerClaimInput, scope string) (out directfile.WorkerClaimResult, found bool, err error) {
	var storedScope, request, worker, target, receipt, state string
	var result sql.NullString
	err = q.QueryRowContext(ctx, `SELECT caller_scope,request_id,worker_id,target_json,receipt_json,state,result_id FROM direct_file_worker_claims WHERE workflow_id=?`, in.WorkflowID).Scan(&storedScope, &request, &worker, &target, &receipt, &state, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, true, err
	}
	raw, _ := json.Marshal(in.WorkerTarget)
	if scope != storedScope || request != in.RequestID || worker != in.WorkerID || target != string(raw) {
		return out, true, fileWorkerConflict()
	}
	if err = json.Unmarshal([]byte(receipt), &out.Receipt); err != nil {
		return out, true, err
	}
	out.Replay = true
	out.State = directfile.WorkerOutcomeUnknown
	if state == "COMPLETED" && result.Valid {
		var body string
		if err = q.QueryRowContext(ctx, `SELECT result_json FROM direct_file_implementation_results WHERE id=? AND workflow_id=? AND ai_session_id=?`, result.String, in.WorkflowID, in.AISessionID).Scan(&body); err != nil {
			return out, true, err
		}
		var value directfile.ImplementationResult
		if err = json.Unmarshal([]byte(body), &value); err != nil {
			return out, true, err
		}
		// Keep the immutable dispatch receipt separate from current, read-only
		// verification/acceptance projection, exactly as formal result_get does.
		if err = projectFileVerification(ctx, q, &value); err != nil {
			return out, true, err
		}
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_implementation_acceptances WHERE result_id=? AND ai_session_id=?)`, value.ID, in.AISessionID).Scan(&value.Accepted); err != nil {
			return out, true, err
		}
		out.ResultReceipt = &value
		out.State = directfile.WorkerResultRecorded
	}
	return out, true, nil
}
func (s *DirectFileWorkerStore) claim(ctx context.Context, in directfile.WorkerClaimInput, create bool) (out directfile.WorkerClaimResult, err error) {
	if !validFileWorkerTarget(in.WorkerTarget) || !fileResultIDs(in.RequestID, in.WorkerID) {
		return out, fileWorkerConflict()
	}
	// Exact historical receipt reads do not need current runtime writer ownership.
	read, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	scope, err := directFileWorkerScope(ctx, read, in.AISessionID)
	if err != nil {
		read.Rollback()
		return out, err
	}
	prior, found, err := directFileWorkerReplay(ctx, read, in, scope)
	read.Rollback()
	if err != nil || found {
		return prior, err
	}
	if !create {
		return out, fileWorkerConflict()
	}
	s.db.directFileOperations.Lock()
	defer s.db.directFileOperations.Unlock()
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	scope, err = directFileWorkerScope(ctx, tx, in.AISessionID)
	if err != nil {
		return out, err
	}
	if prior, found, err := directFileWorkerReplay(ctx, tx, in, scope); err != nil || found {
		return prior, err
	}
	grant, err := readTaskFileAuthorization(ctx, tx, ` WHERE workflow_id=?`, in.WorkflowID)
	if err != nil {
		return out, err
	}
	if fileWorkerTarget(grant) != in.WorkerTarget || grant.State.State != "ACTIVE" || grant.caller.String != scope {
		return out, fileWorkerConflict()
	}
	if err = validateTaskFileAuthorization(ctx, tx, grant); err != nil {
		return out, err
	}
	// A prepared canonical action is already an exclusive decision boundary;
	// worker enrollment cannot arrive between its preparation and consumption.
	if err = taskActionAdmissionTx(ctx, tx, grant.TaskID); err != nil {
		return out, err
	}
	var epoch string
	if err = tx.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&epoch); err != nil {
		return out, err
	}
	if epoch != grant.RuntimeEpoch {
		return out, fileWorkerConflict()
	}
	var used bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_operation_receipts WHERE workflow_id=?) OR EXISTS(SELECT 1 FROM direct_file_implementation_results WHERE workflow_id=?)`, in.WorkflowID, in.WorkflowID).Scan(&used); err != nil {
		return out, err
	}
	if used {
		return out, directfile.Failure(directfile.NotEnabled, "file worker enrollment cannot adopt prior file mutation or result history")
	}
	access, err := currentTaskFileState(ctx, tx, grant)
	if err != nil {
		return out, err
	}
	if !access.Enabled || access.State != "ACTIVE" {
		return out, fileWorkerConflict()
	}
	access.RepositoryTargets, err = readTaskFileMembers(ctx, tx, in.WorkflowID)
	if err != nil {
		return out, err
	}
	for _, member := range access.RepositoryTargets {
		if member.State != "READY" {
			return out, fileWorkerConflict()
		}
	}
	revision, binding, err := directFileWorkerBinding(ctx, tx, grant)
	if err != nil {
		return out, err
	}
	payload := directfile.WorkerExecutionPayload{Instruction: revision.Definition.WorkSpec.CodexInstruction, Definition: revision.Definition, Access: access, Verification: revision.Definition.VerificationSpec}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	out.Receipt = directfile.WorkerClaimReceipt{WorkerTarget: in.WorkerTarget, WorkerClaimIdentity: in.WorkerClaimIdentity, ClaimID: "file_worker_" + workerDigest([]byte(in.WorkflowID)), InstructionSHA256: workerDigest([]byte(payload.Instruction)), DefinitionSHA256: revision.PayloadSHA256, WorksetSHA256: binding, PayloadSHA256: workerDigest(encoded), CreatedAt: time.Now().UTC()}
	receipt, err := json.Marshal(out.Receipt)
	if err != nil {
		return out, err
	}
	target, err := json.Marshal(in.WorkerTarget)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO direct_file_worker_claims(workflow_id,ai_session_id,caller_scope,request_id,worker_id,target_json,binding_sha256,receipt_json,state) VALUES(?,?,?,?,?,?,?,?,'DISPATCH_INTENT')`, in.WorkflowID, in.AISessionID, scope, in.RequestID, in.WorkerID, string(target), binding, string(receipt))
	if err != nil {
		return out, conflict(err)
	}
	if err = tx.Commit(); err != nil {
		return directfile.WorkerClaimResult{}, err
	}
	out.State = directfile.WorkerDispatchIntent
	out.ExecutionPayload = &payload
	return out, nil
}

// Authenticates historical completed effect/result replay without consulting a
// live epoch, current revision, grant readiness, or the worker's terminal state.
func requireDirectFileWorkerReplayTx(ctx context.Context, q definitionQuerier, workflowID, sessionID string, identity *directfile.WorkerClaimIdentity) error {
	var session, scope, request, worker string
	err := q.QueryRowContext(ctx, `SELECT ai_session_id,caller_scope,request_id,worker_id FROM direct_file_worker_claims WHERE workflow_id=?`, workflowID).Scan(&session, &scope, &request, &worker)
	if errors.Is(err, sql.ErrNoRows) {
		if identity != nil {
			return fileWorkerConflict()
		}
		return nil
	}
	if err != nil {
		return err
	}
	actual, err := directFileWorkerScope(ctx, q, sessionID)
	if err != nil {
		return err
	}
	if identity == nil || sessionID != session || actual != scope || identity.RequestID != request || identity.WorkerID != worker {
		return fileWorkerConflict()
	}
	return nil
}
func requireDirectFileWorkerTx(ctx context.Context, q definitionQuerier, grant taskFileAuthorization, identity *directfile.WorkerClaimIdentity) error {
	if err := requireDirectFileWorkerReplayTx(ctx, q, grant.WorkflowID, grant.AISessionID, identity); err != nil {
		return err
	}
	if identity == nil {
		return nil
	}
	if grant.ProtocolVersion != 2 || grant.State.State != "ACTIVE" {
		return fileWorkerConflict()
	}
	var epoch string
	if err := q.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&epoch); err != nil {
		return err
	}
	if epoch != grant.RuntimeEpoch {
		return fileWorkerConflict()
	}
	var target, binding, state string
	if err := q.QueryRowContext(ctx, `SELECT target_json,binding_sha256,state FROM direct_file_worker_claims WHERE workflow_id=?`, grant.WorkflowID).Scan(&target, &binding, &state); err != nil {
		return err
	}
	raw, _ := json.Marshal(fileWorkerTarget(grant))
	if target != string(raw) || state != "DISPATCH_INTENT" {
		return fileWorkerConflict()
	}
	_, current, err := directFileWorkerBinding(ctx, q, grant)
	if err != nil {
		return err
	}
	if current != binding {
		return fileWorkerConflict()
	}
	return nil
}
func completeDirectFileWorkerTx(ctx context.Context, tx *sql.Tx, grant taskFileAuthorization, identity *directfile.WorkerClaimIdentity, resultID string) error {
	if identity == nil {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE direct_file_worker_claims SET state='COMPLETED',result_id=? WHERE workflow_id=? AND state='DISPATCH_INTENT'`, resultID, grant.WorkflowID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fileWorkerConflict()
	}
	return nil
}
func directFileWorkerEnrolled(ctx context.Context, q definitionQuerier, workflowID string) (bool, error) {
	var enrolled bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_worker_claims WHERE workflow_id=?)`, workflowID).Scan(&enrolled)
	return enrolled, err
}

// Claimed completed results can be read even by a stale runtime context. This
// authenticates exact historical input before any writer/current-epoch gate;
// the existing writer path still repeats lookup before all fresh work.
func (s *WorkflowProgressionStore) replayFileWorkerResult(ctx context.Context, in directfile.ImplementationResultInput, hash string) (out directfile.ImplementationResult, found bool, err error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, false, err
	}
	defer tx.Rollback()
	if _, _, err = taskFileCaller(ctx, tx, in.AISessionID); err != nil {
		return out, false, err
	}
	var stored, body string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256,result_json FROM direct_file_implementation_results WHERE ai_session_id=? AND request_id=?`, in.AISessionID, in.RequestID).Scan(&stored, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, true, err
	}
	if stored != hash {
		return out, true, fileResultConflict()
	}
	if err = requireDirectFileWorkerReplayTx(ctx, tx, in.WorkflowID, in.AISessionID, in.WorkerClaim); err != nil {
		return out, true, err
	}
	if err = json.Unmarshal([]byte(body), &out); err != nil {
		return out, true, err
	}
	if err = projectFileVerification(ctx, tx, &out); err != nil {
		return out, true, err
	}
	out.Replay = true
	return out, true, nil
}
