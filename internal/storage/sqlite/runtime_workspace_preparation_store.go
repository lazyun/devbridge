package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

// ClaimWorkspacePreparation durably records an external-operation claim.
// Replays return true and must read the original receipt instead of calling
// the managed-worktree builder again.
func (s *RunStore) ClaimWorkspacePreparation(ctx context.Context, planID, runID, requestID, payloadHash string, now time.Time) (bool, error) {
	if planID == "" || runID == "" || requestID == "" || len(payloadHash) != 64 {
		return false, ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exactRun, oldRequest, oldHash string
	err = tx.QueryRowContext(ctx, `SELECT run_id,request_id,payload_hash FROM runtime_preparation_attempts WHERE plan_id=?`, planID).Scan(&exactRun, &oldRequest, &oldHash)
	if err != nil {
		return false, err
	}
	if exactRun != runID {
		return false, ErrConflict
	}
	var savedRun, savedHash string
	err = tx.QueryRowContext(ctx, `SELECT run_id,payload_hash FROM runtime_workspace_preparation_claims WHERE request_id=?`, requestID).Scan(&savedRun, &savedHash)
	if err == nil {
		if savedRun != runID || savedHash != payloadHash {
			return false, ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_workspace_preparation_claims(request_id,payload_hash,plan_id,run_id,claimed_at) VALUES(?,?,?,?,?)`, requestID, payloadHash, planID, runID, formatTime(now))
	if err != nil {
		return false, conflict(err)
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

// RecordWorkspaceObservations persists one complete fresh observation set
// after the external builder returns. No Workset or start authority is written.
func (s *RunStore) RecordWorkspaceObservations(ctx context.Context, planID, runID, requestID, payloadHash string, observations []runtimepreparation.WorkspaceObservation, now time.Time) (string, error) {
	if len(observations) == 0 || len(observations) > 128 {
		return "", ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var projectID, targetsJSON, headsJSON, claimRun, claimHash, claimAt string
	var topology int64
	var development string
	err = tx.QueryRowContext(ctx, `SELECT p.project_id,p.repository_targets_json,p.expected_source_heads_json,p.topology_revision,p.development_context_hash,c.run_id,c.payload_hash,c.claimed_at FROM runtime_scope_plans p JOIN runtime_workspace_preparation_claims c ON c.plan_id=p.plan_id WHERE p.plan_id=? AND c.request_id=?`, planID, requestID).Scan(&projectID, &targetsJSON, &headsJSON, &topology, &development, &claimRun, &claimHash, &claimAt)
	if err != nil {
		return "", err
	}
	if claimRun != runID || claimHash != payloadHash {
		return "", ErrConflict
	}
	claimTime, err := parseTime(claimAt)
	if err != nil {
		return "", err
	}
	currentTopology, currentDevelopment, err := purposeProjectContextTx(ctx, tx, projectID)
	if err != nil {
		return "", err
	}
	if currentTopology != topology || currentDevelopment != development {
		return "", ErrConflict
	}
	var targets []workset.Target
	var heads map[string]string
	if err = json.Unmarshal([]byte(targetsJSON), &targets); err != nil {
		return "", err
	}
	if err = json.Unmarshal([]byte(headsJSON), &heads); err != nil {
		return "", err
	}
	if len(targets) != len(observations) {
		return "", ErrConflict
	}
	targetByMember := map[string]workset.Target{}
	for _, target := range targets {
		targetByMember[target.MemberID] = target
	}
	seen := map[string]bool{}
	for _, observation := range observations {
		if err = observation.Validate(); err != nil {
			return "", err
		}
		target, ok := targetByMember[observation.MemberID]
		if !ok || seen[observation.MemberID] || observation.PlanID != planID || observation.RunID != runID || observation.RepositoryID != target.RepositoryID || observation.StartHEAD != heads[observation.MemberID] || observation.ObservedAt.Before(claimTime) || observation.ObservedAt.After(now.Add(time.Minute)) {
			return "", ErrConflict
		}
		seen[observation.MemberID] = true
		var reused int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_repository_bindings WHERE worktree_id=? AND run_id<>? UNION ALL SELECT 1 FROM runtime_workspace_observations WHERE worktree_id=? AND run_id<>?)`, observation.WorktreeID, runID, observation.WorktreeID, runID).Scan(&reused); err != nil {
			return "", err
		}
		if reused != 0 {
			// The FeatureArea workspace may retain a managed member worktree
			// across an approved Review and later Execution continuations.
			// Every prior owner must be terminal and belong to this Task; this
			// attempt still captures fresh HEAD, snapshot and root observations.
			var ownerKind string
			var priorBindings, unsafeBindings, unsafeObservations int
			err = tx.QueryRowContext(ctx, `SELECT owner_kind FROM runtime_scope_plans WHERE plan_id=?`, planID).Scan(&ownerKind)
			if err != nil {
				return "", err
			}
			if ownerKind == "REVIEW" {
				if err = validateRecoveryReviewWorkspaceReuseTx(ctx, tx, planID, runID, observation, s.db.epoch); err != nil {
					return "", err
				}
				continue
			}
			if ownerKind != "EXECUTION" && ownerKind != "IMPLEMENTATION" {
				return "", ErrConflict
			}
			err = tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM run_repository_bindings WHERE worktree_id=? AND run_id<>?),
 (SELECT count(*) FROM run_repository_bindings b JOIN runs r ON r.id=b.run_id WHERE b.worktree_id=? AND b.run_id<>? AND NOT (r.task_id=(SELECT task_id FROM runs WHERE id=?) AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED') AND b.repository_id=? AND b.member_id=? AND b.workspace_id=?)),
 (SELECT count(*) FROM runtime_workspace_observations o JOIN runs r ON r.id=o.run_id WHERE o.worktree_id=? AND o.run_id<>? AND NOT (r.task_id=(SELECT task_id FROM runs WHERE id=?) AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED') AND o.repository_id=? AND o.member_id=? AND o.workspace_id=?))`, observation.WorktreeID, runID, observation.WorktreeID, runID, runID, observation.RepositoryID, observation.MemberID, observation.WorkspaceID, observation.WorktreeID, runID, runID, observation.RepositoryID, observation.MemberID, observation.WorkspaceID).Scan(&priorBindings, &unsafeBindings, &unsafeObservations)
			if err != nil {
				return "", err
			}
			// Scalar FeatureArea workspaces persist across Tasks. Permit their
			// observation-only history only under the current exclusive claim,
			// with every previous runtime safely finalized in this same workspace.
			if priorBindings == 0 && len(targets) == 1 && target.MemberID == target.RepositoryID {
				var safe bool
				err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM runs current JOIN feature_area_agent_claims c ON c.run_id=current.id
 JOIN workspaces w ON w.id=? AND w.feature_area_id=current.feature_area_id AND w.project_id=current.project_id
 WHERE current.id=? AND current.runtime_epoch=? AND c.runtime_epoch=current.runtime_epoch
 AND w.repository_id=? AND w.worktree_id=? AND w.runtime_quarantined=0
 AND NOT EXISTS(SELECT 1 FROM runtime_workspace_observations o JOIN runs r ON r.id=o.run_id
 WHERE o.worktree_id=? AND o.run_id<>current.id AND NOT (
 r.project_id=current.project_id AND r.feature_area_id=current.feature_area_id
 AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')
 AND o.repository_id=? AND o.member_id=? AND o.workspace_id=w.id
 AND EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id AND b.quiesced=1 AND b.advance_state<>'UNKNOWN'))))`, observation.WorkspaceID, runID, s.db.epoch, observation.RepositoryID, observation.WorktreeID, observation.WorktreeID, observation.RepositoryID, observation.MemberID).Scan(&safe)
				if err != nil || !safe {
					return "", ErrConflict
				}
			} else if priorBindings == 0 || unsafeBindings != 0 || unsafeObservations != 0 {
				return "", ErrConflict
			}
		}
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].MemberID < observations[j].MemberID })
	raw, err := json.Marshal(observations)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	observationHash := hex.EncodeToString(digest[:])
	var savedPayload, savedPlan, savedRun, savedOutcome, savedHash string
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,plan_id,COALESCE(run_id,''),outcome,observation_set_hash FROM runtime_preparation_receipts WHERE request_id=?`, requestID).Scan(&savedPayload, &savedPlan, &savedRun, &savedOutcome, &savedHash)
	if err == nil {
		if savedPayload != payloadHash || savedPlan != planID || savedRun != runID || savedOutcome != "OBSERVATION_RECORDED" || savedHash != observationHash {
			return "", ErrConflict
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return savedHash, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	for _, o := range observations {
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_workspace_observations(observation_id,plan_id,run_id,workspace_id,repository_id,member_id,worktree_id,canonical_root,root_identity,start_head,start_snapshot,branch,metadata_digest,observation_generation,observed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.ID, o.PlanID, o.RunID, o.WorkspaceID, o.RepositoryID, o.MemberID, o.WorktreeID, o.CanonicalRoot, o.RootIdentity, o.StartHEAD, o.StartSnapshot, o.Branch, o.MetadataDigest, o.Generation, formatTime(o.ObservedAt))
		if err != nil {
			return "", conflict(err)
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_receipts(request_id,payload_hash,plan_id,run_id,outcome,observation_set_hash,recorded_at) VALUES(?,?,?,?,'OBSERVATION_RECORDED',?,?)`, requestID, payloadHash, planID, runID, observationHash, formatTime(now))
	if err != nil {
		return "", conflict(err)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return observationHash, nil
}
