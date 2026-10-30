package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
)

// A corrected Review may consume the same supplemental request under a new
// confirmation. That exact link closes only the old, deterministically refused
// start; the old PARTIAL result and frozen content remain historical.
func taskActionSuccessorReviewClosedTx(ctx context.Context, q definitionQuerier, i confirmation.Intent) (bool, error) {
	if i.Commit == nil || i.Purpose != confirmation.Review || i.Action != "REQUEST_SUPPLEMENTAL_REVIEW" || i.State != "PARTIAL" || i.Content.Review == nil || i.Content.Review.ExpectedTarget == nil || len(i.Steps) != 2 {
		return false, nil
	}
	b, request, start := i.Content.Review, i.Steps[0], i.Steps[1]
	if b.ReviewDepth != "FAST" || request.Kind != "REQUEST_REVIEW" || request.State != "COMPLETED" || request.TaskID != b.TaskID || request.RequestID == "" || start.Kind != "START_REVIEW" || start.RequestID == "" || start.Code != "REVIEW_DEPTH_TOO_SHALLOW" || start.ReviewID != "" || start.RunID != "" {
		return false, nil
	}
	var proven bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM task_supplemental_review_requests r
 JOIN task_supplemental_review_starts s ON s.request_id=r.id
 JOIN reviews v ON v.id=s.review_id AND v.task_id=r.task_id AND v.project_id=r.project_id AND v.feature_area_id=r.feature_area_id
 JOIN feature_area_review_start_requests a ON a.review_id=v.id AND a.feature_area_id=r.feature_area_id AND a.outcome='ACCEPTED'
 JOIN runs n ON n.id=a.run_id AND n.review_id=v.id AND n.task_id=r.task_id
 WHERE r.ai_session_id=? AND r.request_id=? AND r.task_id=? AND r.project_id=? AND r.feature_area_id=?
 AND r.source_revision_id=? AND r.source_review_id=? AND r.source_execution_id=? AND r.source_phase=? AND r.purpose=?
 AND v.review_depth='DEEP' AND a.request_id<>?
 AND NOT EXISTS(SELECT 1 FROM feature_area_review_start_requests old WHERE old.feature_area_id=r.feature_area_id AND old.request_id=?)
)`, i.AISessionID, request.RequestID, b.TaskID, i.ProjectID, b.FeatureAreaID, b.ExpectedTarget.TaskDefinitionRevisionID, b.ExpectedTarget.CurrentReviewID, b.ExpectedTarget.CurrentExecutionID, string(b.ExpectedTarget.Phase), b.SupplementalPurpose, start.RequestID, start.RequestID).Scan(&proven)
	return proven, err
}

func taskActionReleaseReasonTx(ctx context.Context, q definitionQuerier, i confirmation.Intent) (string, error) {
	for _, proof := range []struct {
		reason string
		check  func(context.Context, definitionQuerier, confirmation.Intent) (bool, error)
	}{
		{"SHALLOW_START_NO_EFFECT", taskActionShallowStartClosedTx},
		{"SUPPLEMENTAL_REVIEW_STARTED_BY_SUCCESSOR", taskActionSuccessorReviewClosedTx},
		{"FILE_PREPARATION_REQUIRES_RECOVERY", taskActionFilePreparationClosedTx},
	} {
		closed, err := proof.check(ctx, q, i)
		if err != nil {
			return "", err
		}
		if closed {
			return proof.reason, nil
		}
	}
	return "", nil
}

type taskActionConflictFacts struct {
	active   []confirmation.Intent
	settled  []confirmation.Intent
	releases map[string]string
}

// Shared by read projections and writers. Never equate a missing receipt or a
// newer Task pointer with completion. Foreign-session identities stay internal.
func taskActionConflictFactsTx(ctx context.Context, tx *sql.Tx, taskID string) (facts taskActionConflictFacts, err error) {
	values, err := taskActionConflictCandidates(ctx, tx, taskID)
	if err != nil {
		return facts, err
	}
	var epoch string
	if err = tx.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&epoch); err != nil {
		return facts, err
	}
	facts.releases = make(map[string]string)
	for _, value := range values {
		if value.Commit != nil {
			value, err = recoverConfirmationTx(ctx, tx, value, epoch)
			if err != nil {
				return facts, err
			}
		}
		if value.State == "COMPLETED" || value.State == "REJECTED" {
			facts.settled = append(facts.settled, value)
			continue
		}
		reason, e := taskActionReleaseReasonTx(ctx, tx, value)
		if e != nil {
			return facts, e
		}
		if reason != "" {
			facts.releases[value.IntentID] = reason
			continue
		}
		if value.Commit == nil {
			var current, bound int64
			if err = tx.QueryRowContext(ctx, `SELECT h.generation,COALESCE((SELECT max(r.generation+1) FROM task_action_conflict_resolutions r WHERE r.intent_id=b.intent_id),b.generation) FROM task_action_heads h JOIN task_action_confirmation_bindings b ON b.task_id=h.task_id WHERE h.task_id=? AND b.intent_id=?`, taskID, value.IntentID).Scan(&current, &bound); err != nil {
				return facts, err
			}
			if current != bound {
				facts.releases[value.IntentID] = "UNCONSUMED_GENERATION_SUPERSEDED"
				continue
			}
			if !time.Now().Before(value.ExpiresAt) {
				facts.releases[value.IntentID] = "UNCONSUMED_EXPIRED"
				continue
			}
		}
		facts.active = append(facts.active, value)
	}
	return facts, nil
}

func saveTaskActionConflictFactsTx(ctx context.Context, tx *sql.Tx, taskID string, facts taskActionConflictFacts) error {
	for _, value := range facts.settled {
		if err := confirmationSave(ctx, tx, value); err != nil {
			return err
		}
	}
	for id, reason := range facts.releases {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_action_releases(intent_id,task_id,generation,reason)
 SELECT b.intent_id,b.task_id,h.generation,? FROM task_action_confirmation_bindings b JOIN task_action_heads h ON h.task_id=b.task_id
 WHERE b.intent_id=? AND b.task_id=? ON CONFLICT(intent_id) DO NOTHING`, reason, id, taskID); err != nil {
			return err
		}
	}
	return nil
}

func reconcileTaskActionConflictTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	var conflict bool
	if err := tx.QueryRowContext(ctx, `SELECT conflict FROM task_action_heads WHERE task_id=?`, taskID).Scan(&conflict); err != nil {
		return err
	}
	if !conflict {
		return nil
	}
	facts, err := taskActionConflictFactsTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if err = saveTaskActionConflictFactsTx(ctx, tx, taskID, facts); err != nil {
		return err
	}
	if len(facts.active) > 1 {
		return nil // Multiple actual choices still require explicit resolution.
	}
	owner, advance := "", 1
	if len(facts.active) == 1 {
		// No user choice is being made: all other candidates are proven settled.
		// Keep this original binding and its generation, including its hash.
		owner, advance = facts.active[0].IntentID, 0
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_action_heads SET conflict=0,owner_intent_id=?,generation=generation+? WHERE task_id=?`, nullString(owner), advance, taskID)
	return err
}

func migrateTaskActionReconciliationV134(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM task_action_heads WHERE conflict=1 ORDER BY task_id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = reconcileTaskActionConflictTx(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
