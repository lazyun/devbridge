package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/review"
)

// reviewClaimExecutorTx never infers the executor from absent backend fields.
// A missing or unknown durable Review is a conflict, not a Coding fallback.
func reviewClaimExecutorTx(ctx context.Context, tx *sql.Tx, claim agentconversation.Claim) (review.ExecutorKind, error) {
	var executor review.ExecutorKind
	err := tx.QueryRowContext(ctx, `SELECT executor_kind FROM reviews WHERE id=? AND linked_run_id=?`, claim.OperationID, claim.RunID).Scan(&executor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", agentconversation.ErrConflict
	}
	if err != nil {
		return "", err
	}
	if !executor.Valid() {
		return "", agentconversation.ErrConflict
	}
	return executor, nil
}

const upperWorkerContinuationGuidance = "Same-Run Upper Review worker continuation is unsupported. Use the existing explicit Review cancellation flow; any successor requires separate user authorization. Cancellation does not prove that the external worker stopped."

type upperReviewRecoveryFacts struct {
	workerEnrolled      bool
	epoch               string
	generation          int64
	state               string
	reason              string
	currentOwner        bool
	authorityEpoch      string
	projectID           string
	lifecycleAllowed    bool
	pendingCancellation bool
	workspaceBound      bool
	finalized           bool
}

// upperReviewOperationFactsTx extracts the existing Upper Recover admission and
// its v48 trigger prerequisites. It executes SELECT statements only. It does
// not prepare a workspace, inspect files, consume a receipt, or reauthorize a
// grant. The caller must distinguish this protocol admission from file access:
// the existing command only returns the grant to PREPARING.
func upperReviewOperationFactsTx(ctx context.Context, tx *sql.Tx, claim agentconversation.Claim, currentEpoch string) (upperReviewRecoveryFacts, error) {
	f := upperReviewRecoveryFacts{}
	if claim.Kind != agentconversation.Review {
		f.reason = "The exact operation is not a Review."
		return f, nil
	}
	var authority string
	if err := tx.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&authority); err != nil {
		return f, err
	}
	if currentEpoch == "" || authority != currentEpoch {
		f.reason = "The current Core does not hold this runtime authority."
		return f, nil
	}
	f.authorityEpoch = authority
	var epoch string
	var admissible, backendAbsent, ownerMatches, orphaned, legacy bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(g.runtime_epoch,''),g.generation,g.state,COALESCE(u.runtime_epoch,''),
 u.status IN ('CREATED','PREPARING','RUNNING','COLLECTING'),
 u.thread_id IS NULL AND u.turn_id IS NULL AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=u.id),
 v.executor_kind='UPPER_LLM' AND v.linked_run_id=u.id AND v.feature_area_id=?
 AND u.review_id=v.id AND u.operation_kind='REVIEW' AND u.operation_id=v.id AND u.purpose='SCOPE_REVIEW'
 AND u.task_id=v.task_id AND u.project_id=v.project_id AND u.feature_area_id=v.feature_area_id
 AND t.project_id=v.project_id AND t.feature_area_id=v.feature_area_id AND a.project_id=v.project_id
 AND g.feature_area_id=v.feature_area_id AND g.project_id=v.project_id AND (g.protocol_version=2 OR g.repository_id=v.repository_id),
 COALESCE(t.current_review_id=v.id AND t.status='ACTIVE' AND t.phase='REVIEWING',0),v.project_id,
 a.legacy_read_only=0 AND a.status IN ('DISCOVERING','ACTIVE') AND t.legacy_read_only=0,
 EXISTS(SELECT 1 FROM execution_cancellations k WHERE k.run_id=u.id AND k.owner_type='REVIEW' AND k.owner_id=v.id AND k.state IN ('CANCEL_REQUESTED','RECOVERY_REQUIRED')),
 COALESCE(CASE WHEN g.protocol_version=2 THEN v.workspace_id<>'' AND g.worktree_id IS NULL AND COALESCE(g.baseline,'')='' AND u.workspace_id IS NULL AND u.worktree_id IS NULL AND (g.workspace_id IS NULL OR g.workspace_id=v.workspace_id)
 AND (SELECT count(*) FROM run_repository_bindings rb WHERE rb.run_id=u.id)=json_array_length(v.repository_targets_json)
 AND json_array_length(v.repository_targets_json)>0
 AND NOT EXISTS(SELECT 1 FROM run_repository_bindings rb WHERE rb.run_id=u.id AND (rb.review_id<>v.id OR rb.workspace_id<>v.workspace_id OR NOT EXISTS(SELECT 1 FROM json_each(v.repository_targets_json) target WHERE json_extract(target.value,'$.repositoryId')=rb.repository_id AND json_extract(target.value,'$.memberId')=rb.member_id AND json_extract(target.value,'$.memberVersion')=rb.member_version AND json_extract(target.value,'$.originCommit')=rb.origin_commit))) ELSE (g.workspace_id IS NULL OR g.workspace_id=u.workspace_id) AND (g.worktree_id IS NULL OR g.worktree_id=u.worktree_id) AND (g.baseline IS NULL OR g.baseline=v.base_commit) END,0),
 EXISTS(SELECT 1 FROM review_reports p WHERE p.review_id=v.id OR p.run_id=u.id),
 EXISTS(SELECT 1 FROM agent_orphan_dispositions o WHERE o.run_id=u.id),
 NOT EXISTS(SELECT 1 FROM agent_executions e WHERE e.id=u.agent_execution_id AND e.provenance_kind<>'LEGACY_UNKNOWN'
 AND e.task_id=u.task_id AND e.project_id=u.project_id AND e.feature_area_id=u.feature_area_id AND e.semantic_owner_kind=u.operation_kind AND e.semantic_owner_id=u.operation_id)
 FROM reviews v JOIN review_grants g ON g.review_id=v.id JOIN runs u ON u.id=g.run_id
 JOIN tasks t ON t.id=v.task_id JOIN feature_areas a ON a.id=v.feature_area_id
 WHERE v.id=? AND u.id=?`, claim.FeatureAreaID, claim.OperationID, claim.RunID).Scan(&f.epoch, &f.generation, &f.state, &epoch, &admissible, &backendAbsent, &ownerMatches, &f.currentOwner, &f.projectID, &f.lifecycleAllowed, &f.pendingCancellation, &f.workspaceBound, &f.finalized, &orphaned, &legacy)
	if errors.Is(err, sql.ErrNoRows) {
		f.reason = "The exact Upper Review, Run, Task, FeatureArea, or grant is missing."
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM upper_review_worker_claims WHERE run_id=? AND review_id=?)`, claim.RunID, claim.OperationID).Scan(&f.workerEnrolled); err != nil {
		return f, err
	}
	if !f.workspaceBound {
		canonical, canonicalErr := canonicalUpperReviewWorkspaceAtEpochTx(ctx, tx, claim.OperationID, claim.RunID, true, true)
		if canonicalErr == nil && canonical {
			f.workspaceBound = true
		}
	}
	switch {
	case !ownerMatches || epoch != claim.RuntimeEpoch || f.epoch != claim.RuntimeEpoch:
		f.reason = "The Upper Review claim, grant, and durable owner identities do not match."
	case legacy || orphaned:
		f.reason = "This legacy or orphaned Run cannot acquire runtime authority."
	case !backendAbsent:
		f.reason = "The Upper Review has conflicting Coding backend or conversation boundary facts."
	case !admissible:
		f.reason = "The Upper Review Run is not in an existing reauthorization state."
	case f.generation <= 0:
		f.reason = "The Upper Review grant generation is invalid."
	}
	return f, nil
}

// upperReviewRecoveryFactsTx is the shared new-command predicate. In addition
// to the original prior-epoch/grant-state conditions it fences lifecycle, current
// ownership, pending cancellation, immutable bindings, and generation overflow.
func upperReviewRecoveryFactsTx(ctx context.Context, tx *sql.Tx, claim agentconversation.Claim, currentEpoch string) (upperReviewRecoveryFacts, error) {
	f, err := upperReviewOperationFactsTx(ctx, tx, claim, currentEpoch)
	if err != nil || f.reason != "" {
		return f, err
	}
	if f.workerEnrolled {
		f.reason = upperWorkerContinuationGuidance
	} else if !f.lifecycleAllowed || !f.currentOwner {
		f.reason = "Upper Review recovery requires a current ACTIVE Task Review in a DISCOVERING or ACTIVE FeatureArea."
	} else if f.pendingCancellation {
		f.reason = "The exact Upper Review has an accepted cancellation awaiting convergence."
	} else if f.finalized {
		f.reason = "The exact Upper Review already has a completed report."
	} else if !f.workspaceBound {
		f.reason = "The Upper Review workspace or baseline no longer matches its immutable grant."
	} else if f.generation == math.MaxInt64 {
		f.reason = "The Upper Review grant generation cannot be advanced."
	} else if claim.RuntimeEpoch == "" || claim.RuntimeEpoch == currentEpoch {
		f.reason = "The Upper Review must belong to an exact older runtime before reauthorization."
	} else if f.state != "RECOVERY_REQUIRED" {
		f.reason = "The Upper Review grant is not awaiting explicit reauthorization."
	}
	return f, nil
}

func (f upperReviewRecoveryFacts) observedVersion() *agentconversation.UpperVersion {
	return &agentconversation.UpperVersion{AuthorityEpoch: f.authorityEpoch, GrantEpoch: f.epoch, GrantGeneration: f.generation}
}
