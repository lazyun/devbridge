package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type executionStartEffectRecord struct {
	ProjectID, ActorKind, ActorID, RequestID, Action string
	Effect                                           wp.StartEffect
	TaskID, FeatureAreaID, RevisionID, ExecutionID   string
	Phase                                            task.Phase
	DecisionID                                       string
}

func executionStartEffectPayload(v executionStartEffectRecord) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func insertExecutionStartEffectTx(ctx context.Context, tx *sql.Tx, v executionStartEffectRecord, now string) error {
	if v.ProjectID == "" || v.ActorKind == "" || v.ActorID == "" || v.RequestID == "" || v.Action == "" || !v.Effect.Valid() || v.TaskID == "" || v.FeatureAreaID == "" || v.RevisionID == "" || v.ExecutionID == "" || v.Phase != task.PhaseImplementationReady && v.Phase != task.PhaseVerificationReady {
		return executionproposal.ErrInvalidArgument
	}
	payload, err := executionStartEffectPayload(v)
	if err != nil {
		return err
	}
	var saved executionStartEffectRecord
	var savedEffect string
	var savedPhase string
	var savedPayload string
	err = tx.QueryRowContext(ctx, `SELECT project_id,actor_kind,actor_id,request_id,action,effect,task_id,feature_area_id,task_definition_revision_id,execution_id,expected_phase,COALESCE(decision_id,''),payload_sha256 FROM execution_start_effects WHERE project_id=? AND actor_kind=? AND actor_id=? AND request_id=?`, v.ProjectID, v.ActorKind, v.ActorID, v.RequestID).Scan(&saved.ProjectID, &saved.ActorKind, &saved.ActorID, &saved.RequestID, &saved.Action, &savedEffect, &saved.TaskID, &saved.FeatureAreaID, &saved.RevisionID, &saved.ExecutionID, &savedPhase, &saved.DecisionID, &savedPayload)
	if err == nil {
		saved.Effect, saved.Phase = wp.StartEffect(savedEffect), task.Phase(savedPhase)
		if saved != v || savedPayload != payload {
			return executionproposal.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var decisionID any
	if v.DecisionID != "" {
		decisionID = v.DecisionID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_start_effects(project_id,actor_kind,actor_id,request_id,action,effect,task_id,feature_area_id,task_definition_revision_id,execution_id,expected_phase,decision_id,payload_sha256,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ProjectID, v.ActorKind, v.ActorID, v.RequestID, v.Action, string(v.Effect), v.TaskID, v.FeatureAreaID, v.RevisionID, v.ExecutionID, string(v.Phase), decisionID, payload, now)
	return conflict(err)
}

func checkDecisionStartEffectReplayTx(ctx context.Context, tx *sql.Tx, v executionStartEffectRecord) error {
	payload, err := executionStartEffectPayload(v)
	if err != nil {
		return err
	}
	var effect, savedPayload string
	err = tx.QueryRowContext(ctx, `SELECT effect,payload_sha256 FROM execution_start_effects WHERE project_id=? AND actor_kind=? AND actor_id=? AND request_id=?`, v.ProjectID, v.ActorKind, v.ActorID, v.RequestID).Scan(&effect, &savedPayload)
	if errors.Is(err, sql.ErrNoRows) {
		// Historical receipts have no V67 fact. They can be observed as
		// non-starting decisions, but the same request cannot be upgraded.
		if v.Effect == wp.StartEffectNoStart {
			return nil
		}
		return executionproposal.ErrStartAuthorizationUnavailable
	}
	if err != nil {
		return err
	}
	if wp.StartEffect(effect) != v.Effect || savedPayload != payload {
		return executionproposal.ErrStartEffectConflict
	}
	return nil
}

func requireExecutionStartAuthorizationTx(ctx context.Context, tx *sql.Tx, v executionproposal.Proposal) error {
	return requireExecutionRunStartAuthorizationTx(ctx, tx, v, v.RunID)
}

func requireExecutionRunStartAuthorizationTx(ctx context.Context, tx *sql.Tx, v executionproposal.Proposal, runID string) error {
	var authorized, recorded bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM execution_start_effects s
 JOIN execution_proposals e ON e.id=s.execution_id
 JOIN tasks t ON t.id=s.task_id
 JOIN feature_areas f ON f.id=s.feature_area_id
 WHERE s.effect='START_AUTHORIZED' AND s.execution_id=? AND s.project_id=? AND s.task_id=? AND s.task_definition_revision_id=?
 AND e.project_id=s.project_id AND e.task_id=s.task_id AND e.task_definition_revision_id=s.task_definition_revision_id
 AND t.feature_area_id=s.feature_area_id AND t.current_execution_id=e.id AND t.current_definition_revision_id=e.task_definition_revision_id
 AND t.project_id=s.project_id AND t.status='ACTIVE' AND t.legacy_read_only=0 AND f.status='ACTIVE'
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(e.id,t.id))
 AND ((e.status='CONFIRMED' AND e.run_id IS NULL AND s.expected_phase=t.phase)
  OR (e.status='CONSUMED' AND EXISTS(SELECT 1 FROM runtime_handoffs h JOIN runs r ON r.id=h.run_id
    WHERE h.run_id=? AND r.id=? AND r.execution_proposal_id=e.id AND h.state='REGISTERED' AND r.status='CREATED'
    AND h.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
    AND ((s.expected_phase='IMPLEMENTATION_READY' AND r.purpose='IMPLEMENTATION')
      OR (s.expected_phase='VERIFICATION_READY' AND r.purpose='VERIFICATION')
      OR (s.expected_phase='IMPLEMENTATION_READY' AND r.purpose='VERIFICATION' AND EXISTS(
        SELECT 1 FROM runs implementation WHERE implementation.id=e.run_id
        AND implementation.execution_proposal_id=e.id AND implementation.purpose='IMPLEMENTATION' AND implementation.status='SUCCEEDED'))))))
 ), EXISTS(SELECT 1 FROM execution_start_effects s WHERE s.execution_id=? AND s.project_id=? AND s.task_id=? AND s.task_definition_revision_id=?)`, v.ID, v.ProjectID, v.TaskID, v.TaskDefinitionRevisionID, runID, runID, v.ID, v.ProjectID, v.TaskID, v.TaskDefinitionRevisionID).Scan(&authorized, &recorded)
	if err != nil {
		return err
	}
	if !authorized {
		if recorded {
			return executionproposal.ErrNoStartRecorded
		}
		successor, successorErr := noRunSuccessorRunStartAuthorizedTx(ctx, tx, v, runID)
		if successorErr != nil {
			return successorErr
		}
		if successor {
			return nil
		}
		legacy, legacyErr := legacyExecutionRunStartAuthorizedTx(ctx, tx, v, runID)
		if legacyErr != nil {
			return legacyErr
		}
		if legacy {
			return nil
		}
		return executionproposal.ErrStartAuthorizationUnavailable
	}
	return nil
}

// V67 is deliberately not backfilled. A pre-V67 Run may cross the dispatch
// boundary only when an older immutable command receipt proves the exact
// already-reserved Run and every current ownership, epoch, phase, and
// cancellation invariant still holds. Candidate status or author alone is
// never sufficient historical evidence.
func legacyExecutionRunStartAuthorizedTx(ctx context.Context, tx *sql.Tx, v executionproposal.Proposal, runID string) (bool, error) {
	var authorized bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM execution_proposals e
 JOIN tasks t ON t.id=e.task_id
 JOIN feature_areas f ON f.id=t.feature_area_id
 JOIN runs r ON r.id=? AND r.execution_proposal_id=e.id
 JOIN runtime_handoffs h ON h.run_id=r.id
 WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=?
 AND t.project_id=e.project_id AND t.current_execution_id=e.id AND t.current_definition_revision_id=e.task_definition_revision_id
 AND t.status='ACTIVE' AND t.legacy_read_only=0 AND f.status='ACTIVE'
 AND e.status='CONSUMED' AND r.status='CREATED' AND h.state='REGISTERED'
 AND h.runtime_epoch=(SELECT epoch_id FROM runtime_authority WHERE singleton=1)
 AND NOT EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(e.id,t.id))
 AND ((r.purpose='IMPLEMENTATION') OR (r.purpose='VERIFICATION' AND
      (r.id=e.verification_run_id OR r.id=e.run_id) AND
      (EXISTS(SELECT 1 FROM runs implementation WHERE implementation.id=e.run_id AND implementation.execution_proposal_id=e.id AND implementation.purpose='IMPLEMENTATION' AND implementation.status='SUCCEEDED'))))
 AND (
  EXISTS(SELECT 1 FROM operator_task_execution_start_receipts o
   WHERE o.project_id=e.project_id AND o.actor_kind='LOCAL_OPERATOR' AND length(o.actor_id)>0 AND length(o.request_id)>0
   AND o.task_id=e.task_id AND o.feature_area_id=t.feature_area_id
   AND o.task_definition_revision_id=e.task_definition_revision_id
   AND o.expected_execution_id=e.id AND o.execution_id=e.id AND o.run_id=r.id
   AND length(o.payload_sha256)=64
   AND ((o.action='START_EXECUTION' AND o.expected_phase='IMPLEMENTATION_READY' AND r.purpose='IMPLEMENTATION')
     OR (o.action='START_VERIFICATION' AND o.expected_phase='VERIFICATION_READY' AND r.purpose='VERIFICATION')))
  OR EXISTS(SELECT 1 FROM confirmation_intents ci, json_each(ci.intent_json,'$.steps') step
   WHERE ci.project_id=e.project_id AND ci.ai_session_id=e.ai_session_id
   AND json_type(ci.intent_json,'$.commit')='object'
   AND json_extract(ci.intent_json,'$.state') IN ('CONFIRMING','PARTIAL','UNKNOWN','COMPLETED')
   AND json_extract(ci.intent_json,'$.action') IN ('APPROVE_AND_START','START_APPROVED','CONFIRM_AND_START_IMPLEMENTATION','RETRY_IMPLEMENTATION','REQUEST_CHANGES')
   AND json_extract(ci.intent_json,'$.content.execution.taskId')=e.task_id
   AND json_extract(ci.intent_json,'$.content.execution.featureAreaId')=t.feature_area_id
   AND json_extract(step.value,'$.kind')='START_IMPLEMENTATION'
   AND length(json_extract(step.value,'$.requestId'))>0
   AND json_extract(step.value,'$.executionId')=e.id
   AND json_extract(step.value,'$.revisionId')=e.task_definition_revision_id
   AND json_extract(step.value,'$.runId')=r.id
   AND json_extract(step.value,'$.effect')='RESERVED')
 )
)`, runID, v.ID, v.ProjectID, v.TaskID, v.TaskDefinitionRevisionID).Scan(&authorized)
	return authorized, err
}
