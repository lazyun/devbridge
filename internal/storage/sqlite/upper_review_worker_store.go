package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/review"
)

type UpperReviewWorkerStore struct{ db *DB }

func NewUpperReviewWorkerStore(db *DB) *UpperReviewWorkerStore {
	return &UpperReviewWorkerStore{db: db}
}
func workerDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func workerScopeTx(ctx context.Context, tx *sql.Tx, session string) (string, error) {
	scope, ok := aisession.CallerScopeFromContext(ctx)
	if !ok || scope == "" {
		return "", review.ErrNotFound
	}
	var matches bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_sessions WHERE id=? AND caller_scope=?)`, session, scope).Scan(&matches); err != nil {
		return "", err
	}
	if !matches {
		return "", review.ErrNotFound
	}
	return scope, nil
}

// Only frozen authority participates: access observation timestamps and newly
// accumulated evidence must not invalidate an otherwise exact worker claim.
func workerBindingTx(ctx context.Context, tx *sql.Tx, target review.UpperTarget) (string, string, error) {
	var instruction, raw, forbiddenRaw string
	err := tx.QueryRowContext(ctx, `SELECT r.codex_instruction,g.forbidden_paths_json,json_array(g.protocol_version,g.run_id,g.runtime_epoch,g.generation,g.workspace_id,g.worktree_id,g.baseline,g.initial_scope_paths_json,g.forbidden_paths_json,r.start_snapshot,r.runtime_workset_json,v.repository_targets_json,
 COALESCE((SELECT json_group_array(json_array(repository_id,member_id,member_version,workspace_id,worktree_id,origin_commit,start_snapshot,root_identity,root_path)) FROM (SELECT * FROM run_repository_bindings WHERE run_id=r.id ORDER BY repository_id)), '[]'))
 FROM review_grants g JOIN reviews v ON v.id=g.review_id JOIN runs r ON r.id=g.run_id WHERE g.review_id=? AND g.run_id=? AND g.runtime_epoch=? AND g.generation=? AND g.state IN ('ACTIVE','AWAITING_REPORT')`, target.ReviewID, target.RunID, target.RuntimeEpoch, target.GrantGeneration).Scan(&instruction, &forbiddenRaw, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", review.ErrTargetStale
	}
	if err != nil {
		return "", "", err
	}
	forbidden, err := reviewForbiddenPathsJSON(ctx, tx, target.ReviewID, forbiddenRaw)
	if err != nil {
		return "", "", err
	}
	return instruction, workerDigest([]byte(raw + "\n" + forbidden + "\n" + workerDigest([]byte(instruction)))), nil
}

func (s *UpperReviewWorkerStore) ClaimUpperReviewWorker(ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
	return s.claim(ctx, in, true)
}
func (s *UpperReviewWorkerStore) GetUpperReviewWorkerClaim(ctx context.Context, in review.WorkerClaimInput) (review.WorkerClaimResult, error) {
	return s.claim(ctx, in, false)
}
func upperWorkerReplayTx(ctx context.Context, tx *sql.Tx, in review.WorkerClaimInput, scope string) (review.WorkerClaimResult, bool, error) {
	var out review.WorkerClaimResult
	var storedScope, request, worker, targetRaw, receiptRaw, state string
	var report sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT caller_scope,request_id,worker_id,target_json,receipt_json,state,report_receipt_json FROM upper_review_worker_claims WHERE run_id=?`, in.RunID).Scan(&storedScope, &request, &worker, &targetRaw, &receiptRaw, &state, &report)
	targetBytes, _ := json.Marshal(in.UpperTarget)
	if err == nil {
		if scope != storedScope {
			return out, true, review.ErrNotFound
		}
		if request != in.RequestID || worker != in.WorkerID || targetRaw != string(targetBytes) {
			return out, true, review.ErrRequestConflict
		}
		if err = json.Unmarshal([]byte(receiptRaw), &out.Receipt); err != nil {
			return out, true, err
		}
		out.Replay = true
		out.State = review.WorkerOutcomeUnknown
		if state == "COMPLETED" && report.Valid {
			var r review.ReportReceipt
			if err = json.Unmarshal([]byte(report.String), &r); err != nil {
				return out, true, err
			}
			out.ResultReceipt = &r
			out.State = review.WorkerResultRecorded
		}
		return out, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, true, err
	}
	return out, false, nil
}

func (s *UpperReviewWorkerStore) claim(ctx context.Context, in review.WorkerClaimInput, create bool) (review.WorkerClaimResult, error) {
	var out review.WorkerClaimResult
	if !validUpperTarget(in.UpperTarget) || !validUpperID(in.RequestID) || !validUpperID(in.WorkerID) {
		return out, review.ErrInvalidArgument
	}
	observed, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	scope, err := workerScopeTx(ctx, observed, in.AISessionID)
	if err != nil {
		observed.Rollback()
		return out, err
	}
	existing, found, err := upperWorkerReplayTx(ctx, observed, in, scope)
	observed.Rollback()
	if err != nil || found {
		return existing, err
	}
	if !create {
		return out, review.ErrNotFound
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	scope, err = workerScopeTx(ctx, tx, in.AISessionID)
	if err != nil {
		return out, err
	}
	if existing, found, e := upperWorkerReplayTx(ctx, tx, in, scope); e != nil || found {
		return existing, e
	}
	targetBytes, _ := json.Marshal(in.UpperTarget)
	if err = requireUpperReviewCurrentTargetTx(ctx, tx, in.UpperTarget); err != nil {
		return out, err
	}
	instruction, binding, err := workerBindingTx(ctx, tx, in.UpperTarget)
	if err != nil {
		return out, err
	}
	access, err := upperReviewAccessForSessionQuery(ctx, tx, in.AISessionID, in.ReviewID)
	if err != nil {
		return out, err
	}
	if access.Target != in.UpperTarget {
		return out, review.ErrTargetStale
	}
	payload := review.WorkerExecutionPayload{Instruction: instruction, Access: access}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	out.Receipt = review.WorkerClaimReceipt{UpperTarget: in.UpperTarget, WorkerClaimIdentity: in.WorkerClaimIdentity, ClaimID: "upper_worker_" + workerDigest([]byte(in.RunID)), InstructionSHA256: workerDigest([]byte(instruction)), WorksetSHA256: binding, PayloadSHA256: workerDigest(encoded), CreatedAt: time.Now().UTC()}
	receipt, err := json.Marshal(out.Receipt)
	if err != nil {
		return out, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO upper_review_worker_claims(run_id,review_id,ai_session_id,caller_scope,request_id,worker_id,target_json,binding_sha256,receipt_json,state) VALUES(?,?,?,?,?,?,?,?,?,'DISPATCH_INTENT')`, in.RunID, in.ReviewID, in.AISessionID, scope, in.RequestID, in.WorkerID, string(targetBytes), binding, string(receipt))
	if err != nil {
		return out, conflict(err)
	}
	if err = tx.Commit(); err != nil {
		return review.WorkerClaimResult{}, err
	}
	out.State = review.WorkerDispatchIntent
	out.ExecutionPayload = &payload
	return out, nil
}

func requireUpperWorkerSubmissionTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput) error {
	var scope, request, worker, target, binding, state string
	err := tx.QueryRowContext(ctx, `SELECT caller_scope,request_id,worker_id,target_json,binding_sha256,state FROM upper_review_worker_claims WHERE run_id=?`, in.RunID).Scan(&scope, &request, &worker, &target, &binding, &state)
	if errors.Is(err, sql.ErrNoRows) {
		if in.WorkerClaim != nil {
			return review.ErrTargetStale
		}
		return nil
	}
	if err != nil {
		return err
	}
	actual, err := workerScopeTx(ctx, tx, in.AISessionID)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(in.UpperTarget)
	if in.WorkerClaim == nil || actual != scope || in.WorkerClaim.RequestID != request || in.WorkerClaim.WorkerID != worker || target != string(raw) || state != "DISPATCH_INTENT" {
		return review.ErrTargetStale
	}
	_, current, err := workerBindingTx(ctx, tx, in.UpperTarget)
	if err != nil {
		return err
	}
	if binding != current {
		return review.ErrTargetStale
	}
	return nil
}
func completeUpperWorkerSubmissionTx(ctx context.Context, tx *sql.Tx, in review.SubmitInput, receipt []byte) error {
	if in.WorkerClaim == nil {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE upper_review_worker_claims SET state='COMPLETED',report_receipt_json=? WHERE run_id=? AND state='DISPATCH_INTENT'`, string(receipt), in.RunID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return review.ErrTargetStale
	}
	return nil
}
