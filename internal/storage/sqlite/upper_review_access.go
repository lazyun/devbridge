package sqlite

// This file is the SQLite authority for Upper LLM review file access. It does
// not involve the Codex or agent-conversation runtime: the durable Run claim
// is inspected as an ownership fence only.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
)

const upperReviewReadOperation = "UPPER_LLM_FILE_READ"
const upperReviewListOperation = "UPPER_LLM_FILE_LIST"

type UpperReviewAccessStore struct{ db *DB }

func NewUpperReviewAccessStore(db *DB) *UpperReviewAccessStore {
	return &UpperReviewAccessStore{db: db}
}

func invalidUpperAccess() error { return review.ErrInvalidArgument }

// UpperReviewAccessForSession is a pure status read. It deliberately exposes
// no filesystem root and requires the durable review owner session.
func (s *UpperReviewAccessStore) UpperReviewAccessForSession(ctx context.Context, sessionID, reviewID string) (review.UpperAccess, error) {
	if s == nil || s.db == nil || sessionID == "" || reviewID == "" {
		return review.UpperAccess{}, invalidUpperAccess()
	}
	var v review.UpperAccess
	var allowed, forbidden, targetsJSON string
	var observed sql.NullString
	var created string
	err := s.db.sql.QueryRowContext(ctx, `SELECT r.created_by_ai_session_id,r.feature_area_id,r.task_id,g.run_id,g.runtime_epoch,g.generation,g.state,COALESCE(g.workspace_id,r.workspace_id,''),COALESCE(g.worktree_id,''),COALESCE(g.baseline,''),g.initial_scope_paths_json,g.forbidden_paths_json,g.last_observed_at,COALESCE((SELECT start_snapshot FROM runs WHERE id=g.run_id),''),COALESCE(g.preparation_error,''),g.protocol_version,r.repository_targets_json,r.created_at
FROM reviews r JOIN review_grants g ON g.review_id=r.id WHERE r.id=? AND r.executor_kind='UPPER_LLM' AND r.created_by_ai_session_id=?`, reviewID, sessionID).Scan(
		&v.Target.AISessionID, &v.Target.FeatureAreaID, &v.Target.TaskID, &v.Target.RunID, &v.Target.RuntimeEpoch, &v.Target.GrantGeneration, &v.State, &v.WorkspaceID, &v.WorktreeID, &v.Baseline, &allowed, &forbidden, &observed, &v.StartSnapshot, &v.Blocker, &v.ProtocolVersion, &targetsJSON, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return v, review.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return v, err
	}
	v.ReviewCreatedAt = &createdAt
	if err = json.Unmarshal([]byte(targetsJSON), &v.RepositoryTargets); err != nil {
		return v, err
	}
	v.Target.ReviewID = reviewID
	if v.AllowedPaths, err = decodeReviewScopePaths(allowed); err != nil {
		return review.UpperAccess{}, fmt.Errorf("decode upper review allowed paths")
	}
	forbidden, err = reviewForbiddenPathsJSON(ctx, s.db.sql, reviewID, forbidden)
	if err != nil {
		return review.UpperAccess{}, err
	}
	if err = json.Unmarshal([]byte(forbidden), &v.ForbiddenPaths); err != nil || v.ForbiddenPaths == nil {
		return review.UpperAccess{}, fmt.Errorf("decode upper review forbidden paths")
	}
	if observed.Valid {
		t, e := parseTime(observed.String)
		if e != nil {
			return review.UpperAccess{}, e
		}
		v.LastObservedAt = &t
	}
	return v, nil
}

// Prepare observes the immutable workspace snapshot without a writer lock,
// then rechecks the exact target and lifecycle before activating the grant.
// A recovery receipt itself never calls Prepare or implies ACTIVE access.
func (s *UpperReviewAccessStore) Prepare(ctx context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	if s == nil || s.db == nil || !validUpperTarget(target) {
		return review.UpperAccess{}, invalidUpperAccess()
	}
	var protocol int
	if err := s.db.sql.QueryRowContext(ctx, `SELECT protocol_version FROM review_grants WHERE review_id=?`, target.ReviewID).Scan(&protocol); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return review.UpperAccess{}, review.ErrTargetStale
		}
		return review.UpperAccess{}, err
	}
	if protocol == 2 {
		return s.prepareV2(ctx, target)
	}
	observed, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return review.UpperAccess{}, err
	}
	defer observed.Rollback()
	access, allowedJSON, forbiddenJSON, err := s.upperPrepareTx(ctx, observed, target)
	if err != nil {
		return review.UpperAccess{}, err
	}
	access.ProtocolVersion = protocol
	snapshot, err := observeUpperSnapshot(ctx, observed, target, access.WorktreeID, allowedJSON, forbiddenJSON, true)
	if err != nil {
		return review.UpperAccess{}, err
	}
	if err = observed.Rollback(); err != nil {
		return review.UpperAccess{}, err
	}

	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return review.UpperAccess{}, err
	}
	defer tx.Rollback()
	current, allowedNow, forbiddenNow, err := s.upperPrepareTx(ctx, tx, target)
	if err != nil {
		return review.UpperAccess{}, err
	}
	if !sameUpperAccessBinding(access, current) || allowedNow != allowedJSON || forbiddenNow != forbiddenJSON {
		return review.UpperAccess{}, review.ErrTargetStale
	}
	access.StartSnapshot, err = commitUpperSnapshotTx(ctx, tx, target, access.WorktreeID, snapshot, true)
	if err != nil {
		return review.UpperAccess{}, err
	}
	if current.State == "PREPARING" {
		changed, e := tx.ExecContext(ctx, `UPDATE review_grants SET state='ACTIVE',preparation_error=NULL,workspace_id=?,worktree_id=?,baseline=? WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND state='PREPARING'`, access.WorkspaceID, access.WorktreeID, access.Baseline, target.ReviewID, target.RunID, target.RuntimeEpoch, target.GrantGeneration)
		if e != nil {
			return review.UpperAccess{}, e
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return review.UpperAccess{}, review.ErrTargetStale
		}
		access.State = "ACTIVE"
	} else if current.State != "ACTIVE" && current.State != "AWAITING_REPORT" {
		return review.UpperAccess{}, review.ErrTargetStale
	} else {
		access.State = current.State
	}
	access.Target = target
	if access.AllowedPaths, err = decodeReviewScopePaths(allowedJSON); err != nil {
		return review.UpperAccess{}, err
	}
	if err = json.Unmarshal([]byte(forbiddenJSON), &access.ForbiddenPaths); err != nil {
		return review.UpperAccess{}, err
	}
	if err = tx.Commit(); err != nil {
		return review.UpperAccess{}, err
	}
	return access, nil
}

// prepareV2 activates the parent grant only after every frozen Run binding is
// ready and its own root snapshot has been observed. This is intentionally a
// single state transition: a partially prepared multi-repository Review is
// still PREPARING and has no file capability.
func (s *UpperReviewAccessStore) prepareV2(ctx context.Context, target review.UpperTarget) (review.UpperAccess, error) {
	readTx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return review.UpperAccess{}, err
	}
	defer readTx.Rollback()
	if err = requireUpperReviewCurrentTargetV2Tx(ctx, readTx, target); err != nil {
		return review.UpperAccess{}, err
	}
	var allowedJSON, forbiddenJSON, state string
	if err = readTx.QueryRowContext(ctx, `SELECT state,initial_scope_paths_json,forbidden_paths_json FROM review_grants WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND protocol_version=2`, target.ReviewID, target.RunID, target.RuntimeEpoch, target.GrantGeneration).Scan(&state, &allowedJSON, &forbiddenJSON); err != nil {
		return review.UpperAccess{}, err
	}
	forbiddenJSON, err = reviewForbiddenPathsJSON(ctx, readTx, target.ReviewID, forbiddenJSON)
	if err != nil {
		return review.UpperAccess{}, err
	}
	allowed, err := decodeReviewScopePaths(allowedJSON)
	if err != nil {
		return review.UpperAccess{}, err
	}
	var forbidden []string
	if err = json.Unmarshal([]byte(forbiddenJSON), &forbidden); err != nil {
		return review.UpperAccess{}, err
	}
	type snapshot struct {
		repo, member, worktree, root, digest, identity string
		version                                        int
	}
	rows, err := readTx.QueryContext(ctx, `SELECT b.repository_id,b.member_id,b.member_version,b.worktree_id,wt.path FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id AND wt.status='READY' WHERE b.run_id=? AND b.review_id=? AND b.preparation='READY' ORDER BY b.repository_id`, target.RunID, target.ReviewID)
	if err != nil {
		return review.UpperAccess{}, err
	}
	defer rows.Close()
	var observed []snapshot
	for rows.Next() {
		var x snapshot
		if err = rows.Scan(&x.repo, &x.member, &x.version, &x.worktree, &x.root); err != nil {
			return review.UpperAccess{}, err
		}
		x.root, err = upperV2CanonicalRoot(x.root)
		if err != nil {
			return review.UpperAccess{}, review.ErrTargetStale
		}
		x.identity, err = upperV2RootIdentity(x.root)
		if err != nil {
			return review.UpperAccess{}, err
		}
		x.digest, err = reviewaccess.ScopedSnapshot(ctx, x.root, allowed, forbidden)
		if err != nil {
			return review.UpperAccess{}, fmt.Errorf("%w: member snapshot unavailable: %v", review.ErrEvidenceMismatch, err)
		}
		observed = append(observed, x)
	}
	if err = rows.Err(); err != nil {
		return review.UpperAccess{}, err
	}
	if len(observed) == 0 {
		return review.UpperAccess{}, review.ErrTargetStale
	}
	if err = readTx.Rollback(); err != nil {
		return review.UpperAccess{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return review.UpperAccess{}, err
	}
	defer tx.Rollback()
	if err = requireUpperReviewCurrentTargetV2Tx(ctx, tx, target); err != nil {
		return review.UpperAccess{}, err
	}
	for _, x := range observed {
		var path string
		var ready bool
		err = tx.QueryRowContext(ctx, `SELECT wt.path,b.preparation='READY' FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id WHERE b.run_id=? AND b.repository_id=? AND b.member_id=? AND b.member_version=?`, target.RunID, x.repo, x.member, x.version).Scan(&path, &ready)
		if err != nil || !ready || path != x.root {
			return review.UpperAccess{}, review.ErrTargetStale
		}
		identity, e := upperV2RootIdentity(x.root)
		if e != nil || identity != x.identity {
			return review.UpperAccess{}, review.ErrTargetStale
		}
		changed, e := tx.ExecContext(ctx, `UPDATE run_repository_bindings SET start_snapshot=?,root_path=?,root_identity=? WHERE run_id=? AND repository_id=? AND member_id=? AND member_version=? AND (start_snapshot IS NULL OR (start_snapshot=? AND root_path=? AND root_identity=?))`, x.digest, x.root, identity, target.RunID, x.repo, x.member, x.version, x.digest, x.root, identity)
		if e != nil {
			return review.UpperAccess{}, e
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return review.UpperAccess{}, review.ErrTargetStale
		}
	}
	if state == "PREPARING" {
		changed, err := tx.ExecContext(ctx, `UPDATE review_grants SET state='ACTIVE',workspace_id=(SELECT workspace_id FROM reviews WHERE id=review_grants.review_id),preparation_error=NULL WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND protocol_version=2 AND state='PREPARING' AND NOT EXISTS(SELECT 1 FROM run_repository_bindings WHERE run_id=? AND (preparation<>'READY' OR worktree_id IS NULL OR start_snapshot IS NULL)) AND (SELECT count(*) FROM run_repository_bindings WHERE run_id=?)=(SELECT json_array_length(repository_targets_json) FROM reviews WHERE id=?)`, target.ReviewID, target.RunID, target.RuntimeEpoch, target.GrantGeneration, target.RunID, target.RunID, target.ReviewID)
		if err != nil {
			return review.UpperAccess{}, err
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return review.UpperAccess{}, review.ErrTargetStale
		}
	}
	if err = tx.Commit(); err != nil {
		return review.UpperAccess{}, err
	}
	return s.UpperReviewAccessForSession(ctx, target.AISessionID, target.ReviewID)
}

func upperV2CanonicalRoot(path string) (string, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(canonical) {
		return "", review.ErrTargetStale
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", review.ErrTargetStale
	}
	return canonical, nil
}

func upperV2RootIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return taskFileRootIdentity(info)
}

func sameUpperAccessBinding(a, b review.UpperAccess) bool {
	return a.Target == b.Target && a.WorkspaceID == b.WorkspaceID && a.WorktreeID == b.WorktreeID && a.Baseline == b.Baseline
}

// ListEvidenceForSession returns only the evidence bound to the Upper LLM's
// own review owner. It is deliberately content-free: report references use
// the immutable digest and line bounds, never an evidence body.
func (s *UpperReviewAccessStore) ListEvidenceForSession(ctx context.Context, sessionID, reviewID string) ([]review.FileEvidence, error) {
	if s == nil || s.db == nil || sessionID == "" || reviewID == "" {
		return nil, invalidUpperAccess()
	}
	rows, err := s.db.sql.QueryContext(ctx, `SELECT e.id,COALESCE(e.repository_id,''),COALESCE(m.id,''),COALESCE(m.identity_version,0),COALESCE(e.path,''),COALESCE(e.digest,''),COALESCE(e.byte_count,0),COALESCE(e.line_count,0),e.recorded_at
FROM review_access_evidence e JOIN reviews r ON r.id=e.review_id
LEFT JOIN workspace_members m ON m.workspace_id=e.workspace_id AND m.repository_id=e.repository_id AND m.worktree_id=e.worktree_id
WHERE e.review_id=? AND r.created_by_ai_session_id=? AND r.executor_kind='UPPER_LLM' AND e.operation=? AND e.outcome='SUCCESS' ORDER BY e.recorded_at DESC,e.id DESC LIMIT 512`, reviewID, sessionID, upperReviewReadOperation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []review.FileEvidence{}
	for rows.Next() {
		var v review.FileEvidence
		var raw string
		if err = rows.Scan(&v.EvidenceID, &v.RepositoryID, &v.MemberID, &v.MemberVersion, &v.Path, &v.Digest, &v.ByteCount, &v.LineCount, &raw); err != nil {
			return nil, err
		}
		v.RecordedAt, err = parseTime(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *UpperReviewAccessStore) upperPrepareTx(ctx context.Context, tx *sql.Tx, t review.UpperTarget) (review.UpperAccess, string, string, error) {
	if err := requireUpperReviewCurrentTargetTx(ctx, tx, t); err != nil {
		return review.UpperAccess{}, "", "", err
	}
	var v review.UpperAccess
	var allowed, forbidden string
	var grantRunID, grantEpoch, grantWorkspaceID, grantWorktreeID, grantBaseline string
	var workspaceID, worktreeID, baseline, runWorkspaceID, runWorktreeID, authorityEpoch, currentReviewID string
	err := tx.QueryRowContext(ctx, `SELECT g.state,COALESCE(g.run_id,''),COALESCE(g.runtime_epoch,''),COALESCE(g.workspace_id,''),COALESCE(g.worktree_id,''),COALESCE(g.baseline,''),g.initial_scope_paths_json,g.forbidden_paths_json,w.id,wt.id,r.base_commit,COALESCE(u.workspace_id,''),COALESCE(u.worktree_id,''),ra.epoch_id,t.current_review_id
FROM reviews r JOIN review_grants g ON g.review_id=r.id JOIN runs u ON u.id=r.linked_run_id
JOIN workspaces w ON w.id=(SELECT current_workspace_id FROM feature_areas WHERE id=r.feature_area_id) AND w.status='READY' AND w.runtime_quarantined=0
JOIN workspace_members wm ON wm.workspace_id=w.id AND wm.repository_id=r.repository_id AND wm.preparation='READY'
JOIN worktrees wt ON wt.id=wm.worktree_id AND wt.repository_id=r.repository_id AND wt.status='READY'
JOIN tasks t ON t.id=r.task_id AND t.current_review_id=r.id AND t.status='ACTIVE' AND t.phase='REVIEWING'
JOIN runtime_authority ra ON ra.singleton=1
JOIN feature_area_agent_claims c ON c.feature_area_id=r.feature_area_id
WHERE r.id=? AND r.executor_kind='UPPER_LLM' AND r.created_by_ai_session_id=? AND r.feature_area_id=? AND r.task_id=?
AND g.run_id=? AND g.runtime_epoch=? AND g.generation=? AND g.state IN ('PREPARING','ACTIVE','AWAITING_REPORT')
AND u.id=? AND u.status='RUNNING' AND u.purpose='SCOPE_REVIEW' AND u.review_id=r.id AND u.task_id=r.task_id AND u.feature_area_id=r.feature_area_id AND u.runtime_epoch=?
AND ra.epoch_id=u.runtime_epoch AND c.operation_kind='REVIEW' AND c.operation_id=r.id AND c.run_id=u.id AND c.runtime_epoch=u.runtime_epoch`, t.ReviewID, t.AISessionID, t.FeatureAreaID, t.TaskID, t.RunID, t.RuntimeEpoch, t.GrantGeneration, t.RunID, t.RuntimeEpoch).Scan(&v.State, &grantRunID, &grantEpoch, &grantWorkspaceID, &grantWorktreeID, &grantBaseline, &allowed, &forbidden, &workspaceID, &worktreeID, &baseline, &runWorkspaceID, &runWorktreeID, &authorityEpoch, &currentReviewID)
	if errors.Is(err, sql.ErrNoRows) {
		return v, "", "", review.ErrConflict
	}
	if err != nil {
		return v, "", "", err
	}
	if currentReviewID != t.ReviewID || authorityEpoch != t.RuntimeEpoch || runWorkspaceID != workspaceID || runWorktreeID != worktreeID || (grantRunID != "" && (grantRunID != t.RunID || grantEpoch != t.RuntimeEpoch || (grantWorkspaceID != "" && grantWorkspaceID != workspaceID) || (grantWorktreeID != "" && grantWorktreeID != worktreeID) || grantBaseline != baseline)) || len(baseline) != 40 {
		return v, "", "", review.ErrConflict
	}
	v.WorkspaceID, v.WorktreeID, v.Baseline, v.Target = workspaceID, worktreeID, baseline, t
	if grantRunID != "" {
		v.Target.RunID, v.Target.RuntimeEpoch = grantRunID, grantEpoch
	}
	forbidden, err = reviewForbiddenPathsJSON(ctx, tx, t.ReviewID, forbidden)
	if err != nil {
		return review.UpperAccess{}, "", "", err
	}
	return v, allowed, forbidden, nil
}

// Read and List perform filesystem IO under a read snapshot. Nothing is
// returned until a short writer transaction rechecks lifecycle and the exact
// grant version, then durably publishes the observation.
func (s *UpperReviewAccessStore) Read(ctx context.Context, target review.UpperTarget, in directfile.Input) (directfile.Result, error) {
	return s.observeSource(ctx, target, in, upperReviewReadOperation)
}

func (s *UpperReviewAccessStore) List(ctx context.Context, target review.UpperTarget, in directfile.Input) (directfile.Result, error) {
	return s.observeSource(ctx, target, in, upperReviewListOperation)
}

func (s *UpperReviewAccessStore) observeSource(ctx context.Context, target review.UpperTarget, in directfile.Input, operation string) (directfile.Result, error) {
	if s == nil || s.db == nil || !validUpperTarget(target) || in.ReviewTarget == nil || *in.ReviewTarget != target {
		return directfile.Result{}, invalidUpperAccess()
	}
	// v2 has no scalar Review repository/worktree authority. Resolve the
	// caller's explicit repository/member against the immutable Run binding.
	var protocol int
	if err := s.db.sql.QueryRowContext(ctx, `SELECT protocol_version FROM review_grants WHERE review_id=?`, target.ReviewID).Scan(&protocol); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return directfile.Result{}, review.ErrTargetStale
		}
		return directfile.Result{}, err
	}
	if protocol == 2 {
		return s.observeV2Source(ctx, target, in, operation)
	}
	observed, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return directfile.Result{}, err
	}
	defer observed.Rollback()
	access, allowedJSON, forbiddenJSON, err := s.upperAccessTx(ctx, observed, target, false)
	if err != nil {
		return directfile.Result{}, err
	}
	var repositoryID string
	if err := observed.QueryRowContext(ctx, `SELECT repository_id FROM worktrees WHERE id=?`, access.WorktreeID).Scan(&repositoryID); err != nil {
		return directfile.Result{}, err
	}
	if err := validateDirectFileMember(ctx, observed, directfile.Operation{RepositoryID: in.RepositoryID, MemberID: in.MemberID, MemberVersion: in.MemberVersion}, access.WorkspaceID, repositoryID, access.WorktreeID); err != nil {
		return directfile.Result{}, err
	}
	allowed, err := decodeReviewScopePaths(allowedJSON)
	if err != nil {
		return directfile.Result{}, err
	}
	var forbidden []string
	if err = json.Unmarshal([]byte(forbiddenJSON), &forbidden); err != nil {
		return directfile.Result{}, err
	}
	var result directfile.Result
	var outcome review.AccessOutcome
	var code string
	var sourceErr error
	if operation == upperReviewReadOperation {
		result, outcome, code, sourceErr = upperReadFile(access.WorktreeID, in.Path, allowed, forbidden, observed, ctx)
	} else {
		result, outcome, code, sourceErr = upperListFiles(access.WorktreeID, in.Path, allowed, forbidden, observed, ctx)
	}
	var snapshot upperSnapshotObservation
	if sourceErr == nil {
		snapshot, err = observeUpperSnapshot(ctx, observed, target, access.WorktreeID, allowedJSON, forbiddenJSON, false)
		if err != nil {
			result = directfile.Result{}
			outcome, code, sourceErr = review.AccessFailure, "REVIEW_EVIDENCE_MISMATCH", err
		}
	}
	if err = observed.Rollback(); err != nil {
		return directfile.Result{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return directfile.Result{}, err
	}
	defer tx.Rollback()
	current, allowedNow, forbiddenNow, err := s.upperAccessTx(ctx, tx, target, false)
	if err != nil {
		return directfile.Result{}, err
	}
	if !sameUpperAccessBinding(access, current) || allowedNow != allowedJSON || forbiddenNow != forbiddenJSON {
		return directfile.Result{}, review.ErrTargetStale
	}
	if sourceErr == nil {
		if _, err = commitUpperSnapshotTx(ctx, tx, target, access.WorktreeID, snapshot, false); err != nil {
			return directfile.Result{}, err
		}
	}
	now, evidenceID := time.Now().UTC(), upperEvidenceID()
	if err = s.recordUpperEvidenceTx(ctx, tx, evidenceID, target, access, result, operation, outcome, code, now); err != nil {
		return directfile.Result{}, err
	}
	if sourceErr == nil && in.WorkflowComplete {
		changed, e := tx.ExecContext(ctx, `UPDATE review_grants SET state='AWAITING_REPORT',last_observed_at=? WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND state='ACTIVE'`, formatTime(now), target.ReviewID, target.RunID, target.RuntimeEpoch, target.GrantGeneration)
		if e != nil {
			return directfile.Result{}, e
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return directfile.Result{}, review.ErrTargetStale
		}
	}
	if err = tx.Commit(); err != nil {
		return directfile.Result{}, err
	}
	if sourceErr != nil {
		return directfile.Result{}, sourceErr
	}
	result.EvidenceID, result.WorkflowComplete = evidenceID, in.WorkflowComplete
	return result, nil
}

func (s *UpperReviewAccessStore) observeV2Source(ctx context.Context, target review.UpperTarget, in directfile.Input, operation string) (directfile.Result, error) {
	readTx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return directfile.Result{}, err
	}
	defer readTx.Rollback()
	if err = requireUpperReviewCurrentTargetV2Tx(ctx, readTx, target); err != nil {
		return directfile.Result{}, err
	}
	b, state, allowedJSON, forbiddenJSON, err := s.upperV2BindingTx(ctx, readTx, target, in, false)
	if err != nil {
		return directfile.Result{}, err
	}
	_ = state
	allowed, err := decodeReviewScopePaths(allowedJSON)
	if err != nil {
		return directfile.Result{}, err
	}
	var forbidden []string
	if err = json.Unmarshal([]byte(forbiddenJSON), &forbidden); err != nil {
		return directfile.Result{}, err
	}
	var result directfile.Result
	var outcome review.AccessOutcome
	var code string
	var sourceErr error
	if operation == upperReviewReadOperation {
		result, outcome, code, sourceErr = upperReadFile(b.worktreeID, in.Path, allowed, forbidden, readTx, ctx)
	} else {
		result, outcome, code, sourceErr = upperListFiles(b.worktreeID, in.Path, allowed, forbidden, readTx, ctx)
	}
	if sourceErr == nil {
		currentRoot, rootErr := upperV2CanonicalRoot(bRootPath(readTx, ctx, b.worktreeID))
		identity := ""
		if rootErr == nil {
			identity, rootErr = upperV2RootIdentity(currentRoot)
		}
		currentSnapshot, snapshotErr := reviewaccess.ScopedSnapshot(ctx, currentRoot, allowed, forbidden)
		if rootErr != nil || snapshotErr != nil || b.startSnapshot == "" || b.rootPath != currentRoot || b.rootIdentity != identity || currentSnapshot != b.startSnapshot {
			if rootErr != nil {
				snapshotErr = rootErr
			}
			if snapshotErr == nil {
				snapshotErr = review.ErrEvidenceMismatch
			}
			result = directfile.Result{}
			outcome, code, sourceErr = review.AccessFailure, "REVIEW_EVIDENCE_MISMATCH", snapshotErr
		}
	}
	if err = readTx.Rollback(); err != nil {
		return directfile.Result{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return directfile.Result{}, err
	}
	defer tx.Rollback()
	if err = requireUpperReviewCurrentTargetV2Tx(ctx, tx, target); err != nil {
		return directfile.Result{}, err
	}
	current, _, nowAllowed, nowForbidden, err := s.upperV2BindingTx(ctx, tx, target, in, false)
	if err != nil {
		return directfile.Result{}, err
	}
	if current != b || nowAllowed != allowedJSON || nowForbidden != forbiddenJSON {
		return directfile.Result{}, review.ErrTargetStale
	}
	now, evidenceID := time.Now().UTC(), upperEvidenceID()
	var digest any
	var bytes, lines any
	if outcome == review.AccessSuccess {
		digest = result.Digest
		bytes = result.Size
		lines = strings.Count(result.Content, "\n") + 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO review_access_evidence(id,review_id,operation,outcome,code,recorded_at,run_id,repository_id,workspace_id,worktree_id,baseline,runtime_epoch,grant_generation,path,digest,byte_count,line_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, evidenceID, target.ReviewID, operation, outcome, nullString(code), formatTime(now), target.RunID, b.repositoryID, b.workspaceID, b.worktreeID, b.baseline, target.RuntimeEpoch, target.GrantGeneration, nullString(result.Path), digest, bytes, lines)
	if err != nil {
		return directfile.Result{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET last_observed_at=? WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND protocol_version=2`, formatTime(now), target.ReviewID, target.RunID, target.RuntimeEpoch, target.GrantGeneration); err != nil {
		return directfile.Result{}, err
	}
	if sourceErr == nil && in.WorkflowComplete {
		if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET state='AWAITING_REPORT' WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=? AND protocol_version=2 AND state='ACTIVE'`, target.ReviewID, target.RunID, target.RuntimeEpoch, target.GrantGeneration); err != nil {
			return directfile.Result{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return directfile.Result{}, err
	}
	if sourceErr != nil {
		return directfile.Result{}, sourceErr
	}
	result.RepositoryID, result.MemberID, result.MemberVersion = b.repositoryID, b.memberID, b.memberVersion
	result.SourceKind, result.WorkspaceID, result.WorktreeID, result.OriginCommit, result.StartSnapshot = "MANAGED_WORKTREE", b.workspaceID, b.worktreeID, b.baseline, b.startSnapshot
	result.EvidenceID = evidenceID
	result.WorkflowComplete = in.WorkflowComplete
	return result, nil
}

func bRootPath(tx *sql.Tx, ctx context.Context, worktreeID string) string {
	var path string
	if err := tx.QueryRowContext(ctx, `SELECT path FROM worktrees WHERE id=?`, worktreeID).Scan(&path); err != nil {
		return ""
	}
	return path
}

func validUpperTarget(t review.UpperTarget) bool {
	return validUpperID(t.AISessionID) && validUpperID(t.FeatureAreaID) && validUpperID(t.TaskID) && validUpperID(t.ReviewID) && validUpperID(t.RunID) && validUpperID(t.RuntimeEpoch) && t.GrantGeneration > 0
}

func (s *UpperReviewAccessStore) upperAccessTx(ctx context.Context, tx *sql.Tx, t review.UpperTarget, allowPreparing bool) (review.UpperAccess, string, string, error) {
	if err := requireUpperReviewCurrentTargetTx(ctx, tx, t); err != nil {
		return review.UpperAccess{}, "", "", err
	}
	var v review.UpperAccess
	var allowed, forbidden string
	err := tx.QueryRowContext(ctx, `SELECT g.state,COALESCE(g.workspace_id,''),COALESCE(g.worktree_id,''),COALESCE(g.baseline,''),g.initial_scope_paths_json,g.forbidden_paths_json
FROM reviews r JOIN review_grants g ON g.review_id=r.id JOIN runs u ON u.id=g.run_id
JOIN workspaces w ON w.id=g.workspace_id JOIN worktrees wt ON wt.id=g.worktree_id
JOIN tasks tsk ON tsk.id=r.task_id AND tsk.current_review_id=r.id AND tsk.status='ACTIVE' AND tsk.phase='REVIEWING'
JOIN runtime_authority ra ON ra.singleton=1
JOIN feature_area_agent_claims c ON c.feature_area_id=r.feature_area_id
WHERE r.id=? AND r.executor_kind='UPPER_LLM' AND r.created_by_ai_session_id=? AND r.feature_area_id=? AND r.task_id=?
AND g.run_id=? AND g.runtime_epoch=? AND g.generation=? AND u.id=? AND u.status='RUNNING' AND u.purpose='SCOPE_REVIEW' AND u.review_id=r.id AND u.task_id=r.task_id AND u.feature_area_id=r.feature_area_id AND u.runtime_epoch=?
AND w.feature_area_id=r.feature_area_id AND w.status='READY' AND w.runtime_quarantined=0 AND wt.repository_id=r.repository_id AND wt.status='READY' AND EXISTS(SELECT 1 FROM workspace_members wm WHERE wm.workspace_id=w.id AND wm.repository_id=r.repository_id AND wm.worktree_id=wt.id AND wm.preparation='READY')
AND u.workspace_id=g.workspace_id AND u.worktree_id=g.worktree_id AND g.baseline=r.base_commit AND ra.epoch_id=u.runtime_epoch AND c.operation_kind='REVIEW' AND c.operation_id=r.id AND c.run_id=u.id AND c.runtime_epoch=u.runtime_epoch`,
		t.ReviewID, t.AISessionID, t.FeatureAreaID, t.TaskID, t.RunID, t.RuntimeEpoch, t.GrantGeneration, t.RunID, t.RuntimeEpoch).Scan(&v.State, &v.WorkspaceID, &v.WorktreeID, &v.Baseline, &allowed, &forbidden)
	if errors.Is(err, sql.ErrNoRows) {
		return v, "", "", review.ErrTargetStale
	}
	if err != nil {
		return v, "", "", err
	}
	if !allowPreparing && v.State != "ACTIVE" {
		return v, "", "", review.ErrTargetStale
	}
	v.Target = t
	forbidden, err = reviewForbiddenPathsJSON(ctx, tx, t.ReviewID, forbidden)
	if err != nil {
		return review.UpperAccess{}, "", "", err
	}
	return v, allowed, forbidden, nil
}

func upperReadFile(worktreeID, path string, allowed, forbidden []string, tx *sql.Tx, ctx context.Context) (directfile.Result, review.AccessOutcome, string, error) {
	if err := reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationRead, []string{path}); err != nil {
		return directfile.Result{}, review.AccessForbidden, "REVIEW_SCOPE_FORBIDDEN", err
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if !upperPathAllowed(path, allowed, forbidden) {
		return directfile.Result{}, review.AccessForbidden, "REVIEW_SCOPE_FORBIDDEN", reviewaccess.ErrScopeForbidden
	}
	var rootPath string
	if err := tx.QueryRowContext(ctx, `SELECT path FROM worktrees WHERE id=?`, worktreeID).Scan(&rootPath); err != nil {
		return directfile.Result{}, review.AccessFailure, "WORKTREE_UNAVAILABLE", err
	}
	root, err := reviewaccess.OpenRoot(rootPath)
	if err != nil {
		return directfile.Result{}, review.AccessFailure, "WORKTREE_UNAVAILABLE", err
	}
	defer root.Close()
	f, normalized, err := root.OpenFileStrict(reviewaccess.OperationRead, path)
	if err != nil {
		return directfile.Result{}, review.AccessForbidden, "REVIEW_SCOPE_FORBIDDEN", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return directfile.Result{}, review.AccessFailure, "INVALID_FILE_TARGET", fmt.Errorf("not a regular review file")
	}
	if info.Size() > directfile.MaxReadBytes {
		return directfile.Result{}, review.AccessFailure, directfile.TooLarge, directfile.Failure(directfile.TooLarge, "file exceeds read limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, directfile.MaxReadBytes+1))
	if err != nil {
		return directfile.Result{}, review.AccessFailure, "FILE_OPERATION_FAILED", err
	}
	if len(b) > directfile.MaxReadBytes {
		return directfile.Result{}, review.AccessFailure, directfile.TooLarge, directfile.Failure(directfile.TooLarge, "file exceeds read limit")
	}
	if !utf8Valid(b) {
		return directfile.Result{}, review.AccessFailure, "BINARY_FILE_UNAVAILABLE", fmt.Errorf("binary review file is unavailable")
	}
	sum := sha256.Sum256(b)
	return directfile.Result{Path: normalized, Content: string(b), ContentEncoding: "utf8", Size: int64(len(b)), Digest: hex.EncodeToString(sum[:])}, review.AccessSuccess, "", nil
}

func upperListFiles(worktreeID, path string, allowed, forbidden []string, tx *sql.Tx, ctx context.Context) (directfile.Result, review.AccessOutcome, string, error) {
	if path == "" {
		path = "."
	}
	if path != "." {
		if err := reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationRead, []string{path}); err != nil {
			return directfile.Result{}, review.AccessForbidden, "REVIEW_SCOPE_FORBIDDEN", err
		}
		path = filepath.ToSlash(filepath.Clean(path))
		if !upperDirectoryReachable(path, allowed, forbidden) {
			return directfile.Result{}, review.AccessForbidden, "REVIEW_SCOPE_FORBIDDEN", reviewaccess.ErrScopeForbidden
		}
	}
	var rootPath string
	if err := tx.QueryRowContext(ctx, `SELECT path FROM worktrees WHERE id=?`, worktreeID).Scan(&rootPath); err != nil {
		return directfile.Result{}, review.AccessFailure, "WORKTREE_UNAVAILABLE", err
	}
	root, err := reviewaccess.OpenRoot(rootPath)
	if err != nil {
		return directfile.Result{}, review.AccessFailure, "WORKTREE_UNAVAILABLE", err
	}
	defer root.Close()
	dir, normalized, err := root.OpenDirectoryStrict(reviewaccess.OperationRead, path)
	if err != nil {
		return directfile.Result{}, review.AccessForbidden, "REVIEW_SCOPE_FORBIDDEN", err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(directfile.MaxListEntries + 1)
	if err != nil {
		return directfile.Result{}, review.AccessFailure, "FILE_OPERATION_FAILED", err
	}
	out := directfile.Result{Path: normalized, Entries: []directfile.Entry{}}
	for _, entry := range entries {
		child := entry.Name()
		if normalized != "." {
			child = normalized + "/" + child
		}
		child = filepath.ToSlash(child)
		if !upperPathAllowed(child, allowed, forbidden) && !upperDirectoryReachable(child, allowed, forbidden) {
			continue
		}
		typ := ""
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			continue
		case entry.IsDir():
			typ = "directory"
		case entry.Type().IsRegular():
			typ = "file"
		default:
			continue
		}
		if len(out.Entries) == directfile.MaxListEntries {
			out.Truncated = true
			break
		}
		out.Entries = append(out.Entries, directfile.Entry{Name: entry.Name(), Type: typ})
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
	return out, review.AccessSuccess, "", nil
}

func upperPathAllowed(path string, allowed, forbidden []string) bool {
	if reviewaccess.UpperPathProtected(path) {
		return false
	}
	for _, p := range forbidden {
		if path == p || strings.HasPrefix(path, p+"/") {
			return false
		}
	}
	if len(allowed) == 0 {
		return true
	}
	for _, p := range allowed {
		p = filepath.ToSlash(filepath.Clean(p))
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
func upperDirectoryReachable(path string, allowed, forbidden []string) bool {
	if reviewaccess.UpperPathProtected(path) {
		return false
	}
	if path == "." {
		return true
	}
	for _, p := range forbidden {
		if path == p || strings.HasPrefix(path, p+"/") {
			return false
		}
	}
	if len(allowed) == 0 {
		return true
	}
	for _, p := range allowed {
		if strings.HasPrefix(p, path+"/") {
			return true
		}
	}
	return upperPathAllowed(path, allowed, forbidden)
}
func utf8Valid(b []byte) bool { return utf8.Valid(b) }

func (s *UpperReviewAccessStore) recordUpperEvidenceTx(ctx context.Context, tx *sql.Tx, id string, t review.UpperTarget, a review.UpperAccess, r directfile.Result, operation string, outcome review.AccessOutcome, code string, now time.Time) error {
	if err := requireUpperReviewCurrentTargetTx(ctx, tx, t); err != nil {
		return err
	}
	var digest any
	var bytes, lines any
	if outcome == review.AccessSuccess {
		digest = r.Digest
		bytes = r.Size
		lines = strings.Count(r.Content, "\n") + 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO review_access_evidence(id,review_id,operation,outcome,code,recorded_at,run_id,repository_id,workspace_id,worktree_id,baseline,runtime_epoch,grant_generation,path,digest,byte_count,line_count) SELECT ?,?,?,?,?,?,g.run_id,g.repository_id,g.workspace_id,g.worktree_id,g.baseline,g.runtime_epoch,g.generation,?,?,?,? FROM review_grants g WHERE g.review_id=? AND g.run_id=? AND g.runtime_epoch=? AND g.generation=? AND g.workspace_id=? AND g.worktree_id=? AND g.baseline=?`, id, t.ReviewID, operation, outcome, nullString(code), formatTime(now), nullString(r.Path), digest, bytes, lines, t.ReviewID, t.RunID, t.RuntimeEpoch, t.GrantGeneration, a.WorkspaceID, a.WorktreeID, a.Baseline)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE review_grants SET last_observed_at=? WHERE review_id=? AND run_id=? AND runtime_epoch=? AND generation=?`, formatTime(now), t.ReviewID, t.RunID, t.RuntimeEpoch, t.GrantGeneration)
	return err
}
func upperEvidenceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "evidence_" + hex.EncodeToString(b[:])
}

// upperSnapshotObservation contains only an already observed filesystem
// digest and the durable binding used to observe it. Final commits do no IO.
type upperSnapshotObservation struct{ rootPath, original, current string }

func upperSnapshotBindingTx(ctx context.Context, tx *sql.Tx, target review.UpperTarget, worktreeID string) (rootPath, original string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT w.path,COALESCE(r.start_snapshot,'') FROM worktrees w JOIN runs r ON r.worktree_id=w.id WHERE w.id=? AND r.id=? AND r.review_id=? AND r.runtime_epoch=?`, worktreeID, target.RunID, target.ReviewID, target.RuntimeEpoch).Scan(&rootPath, &original)
	if errors.Is(err, sql.ErrNoRows) {
		err = review.ErrTargetStale
	}
	return
}

func observeUpperSnapshot(ctx context.Context, tx *sql.Tx, target review.UpperTarget, worktreeID, allowedJSON, forbiddenJSON string, initialize bool) (upperSnapshotObservation, error) {
	var v upperSnapshotObservation
	allowed, err := decodeReviewScopePaths(allowedJSON)
	if err != nil {
		return v, err
	}
	var forbidden []string
	if err = json.Unmarshal([]byte(forbiddenJSON), &forbidden); err != nil {
		return v, err
	}
	v.rootPath, v.original, err = upperSnapshotBindingTx(ctx, tx, target, worktreeID)
	if err != nil {
		return v, err
	}
	v.current, err = reviewaccess.ScopedSnapshot(ctx, v.rootPath, allowed, forbidden)
	if err != nil {
		return v, fmt.Errorf("%w: scoped snapshot unavailable: %v", review.ErrEvidenceMismatch, err)
	}
	if v.original != v.current && !(v.original == "" && initialize) {
		return v, fmt.Errorf("%w: authorized working snapshot changed; preserve this Review baseline and submit BLOCKED or start a subsequent Review", review.ErrEvidenceMismatch)
	}
	return v, nil
}

func commitUpperSnapshotTx(ctx context.Context, tx *sql.Tx, target review.UpperTarget, worktreeID string, observed upperSnapshotObservation, initialize bool) (string, error) {
	rootPath, original, err := upperSnapshotBindingTx(ctx, tx, target, worktreeID)
	if err != nil {
		return "", err
	}
	if rootPath != observed.rootPath || observed.current == "" {
		return "", review.ErrTargetStale
	}
	if original == "" && initialize && observed.original == "" {
		changed, err := tx.ExecContext(ctx, `UPDATE runs SET start_snapshot=? WHERE id=? AND review_id=? AND runtime_epoch=? AND start_snapshot IS NULL`, observed.current, target.RunID, target.ReviewID, target.RuntimeEpoch)
		if err != nil {
			return "", err
		}
		if n, _ := changed.RowsAffected(); n != 1 {
			return "", review.ErrTargetStale
		}
		return observed.current, nil
	}
	if original != observed.current || (observed.original != "" && original != observed.original) {
		return "", review.ErrEvidenceMismatch
	}
	return original, nil
}

func (s *UpperReviewAccessStore) RecordPreparationFailure(ctx context.Context, reviewID, runID, message string) error {
	if len(message) > 2048 {
		message = message[:2048]
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET preparation_error=? WHERE review_id=? AND run_id=? AND generation=1 AND state='PREPARING' AND runtime_epoch=?`, message, reviewID, runID, s.db.epoch); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordTargetPreparationFailure fences diagnostics to the explicitly attempted
// epoch/generation; an old preparation cannot overwrite a recovered blocker.
func (s *UpperReviewAccessStore) RecordTargetPreparationFailure(ctx context.Context, target review.UpperTarget, message string) error {
	if !validUpperTarget(target) || target.RuntimeEpoch != s.db.epoch {
		return review.ErrTargetStale
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireUpperReviewTargetTx(ctx, tx, target, true); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE review_grants SET preparation_error=? WHERE review_id=? AND run_id=? AND generation=? AND state='PREPARING' AND runtime_epoch=? AND NOT EXISTS(SELECT 1 FROM runtime_preparation_failures f WHERE f.owner_id=review_grants.review_id AND f.run_id=review_grants.run_id)`, message, target.ReviewID, target.RunID, target.GrantGeneration, target.RuntimeEpoch); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *UpperReviewAccessStore) CheckUnscopedRepositorySearch(ctx context.Context, sessionID, repositoryID string) error {
	var active bool
	if err := s.db.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reviews r JOIN review_grants g ON g.review_id=r.id WHERE r.created_by_ai_session_id=? AND (r.repository_id=? OR (g.protocol_version=2 AND r.project_id=(SELECT project_id FROM repositories WHERE id=?))) AND r.executor_kind='UPPER_LLM' AND g.state NOT IN ('CLOSED','CANCELED'))`, sessionID, repositoryID, repositoryID).Scan(&active); err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w: use the active Upper Review's file_list/file_read target; unscoped repository search cannot bypass its grant", reviewaccess.ErrScopeForbidden)
	}
	return nil
}

func validUpperID(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, unicode.IsSpace) < 0 && !strings.ContainsRune(value, '\x00')
}
