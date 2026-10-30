package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The original START_APPROVED expected the canceled predecessor, while its
// committed start effect names the new approval-owned Execution. Only that
// same actor/request may recover the gap before its orchestration step receipt.
func existingTaskExecutionReservationCommand(ctx context.Context, q definitionQuerier, t task.Task, expected wp.TaskExecutionExpectation, actor executionStartActor) (executionproposal.Proposal, run.Run, bool, error) {
	// A known revision start keeps its original full expectation even when a
	// caller substitutes the now-current candidate and would otherwise match
	// the ordinary current-owner read path below.
	if actor.Kind == "AI_SESSION" && actor.Action == "START_EXECUTION" && actor.RequestID != "" && t.CurrentExecutionID == approvedExecutionID(t) {
		var raw string
		err := q.QueryRowContext(ctx, `SELECT json_extract(original.intent_json,'$.content.execution.expectedTarget') FROM confirmation_intents original,json_each(original.intent_json,'$.steps') step
 JOIN execution_start_effects effect ON effect.actor_kind='AI_SESSION' AND effect.actor_id=original.ai_session_id AND effect.request_id=json_extract(step.value,'$.requestId')
 WHERE original.ai_session_id=? AND original.project_id=? AND json_extract(original.intent_json,'$.action')='START_APPROVED' AND json_type(original.intent_json,'$.commit')='object'
 AND json_extract(original.intent_json,'$.content.execution.taskId')=? AND json_extract(original.intent_json,'$.content.execution.expectedTarget.taskDefinitionRevisionId')=?
 AND COALESCE(json_extract(original.intent_json,'$.content.execution.expectedTarget.executionId'),'')<>'' AND json_extract(original.intent_json,'$.content.execution.expectedTarget.executionId')<>?
 AND json_extract(step.value,'$.kind')='START_IMPLEMENTATION' AND effect.request_id=? AND effect.action='START_EXECUTION' AND effect.effect='START_AUTHORIZED' AND effect.project_id=? AND effect.task_id=? AND effect.execution_id=?`, actor.ID, t.ProjectID, t.ID, t.CurrentDefinitionRevisionID, t.CurrentExecutionID, actor.RequestID, t.ProjectID, t.ID, t.CurrentExecutionID).Scan(&raw)
		if err == nil {
			var original wp.TaskExecutionExpectation
			if err = json.Unmarshal([]byte(raw), &original); err != nil {
				return executionproposal.Proposal{}, run.Run{}, false, err
			}
			if original != expected {
				return executionproposal.Proposal{}, run.Run{}, false, executionproposal.ErrConflict
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return executionproposal.Proposal{}, run.Run{}, false, err
		}
	}
	value, reserved, found, err := existingTaskExecutionReservation(ctx, q, t, expected)
	if err != nil || found {
		return value, reserved, found, err
	}
	if actor.Kind != "AI_SESSION" || actor.RequestID == "" || actor.Action != "START_EXECUTION" || expected.Phase != task.PhaseImplementationReady || expected.ExecutionID == "" || expected.ExecutionID == t.CurrentExecutionID || t.CurrentExecutionID != approvedExecutionID(t) || expected.FeatureAreaID != t.FeatureAreaID || expected.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
		return value, reserved, false, nil
	}
	// Re-evaluate the immutable predecessor evidence against the explicitly
	// expected historical pointer. The Task's live pointer must independently
	// equal the new candidate named by the exact committed start effect.
	authority := noRunRevisionPredecessorSQL + ` AND ` + noRunRevisionReviewSQL + ` AND ` + strings.TrimPrefix(noRunRevisionApprovedSQL, "t.phase='IMPLEMENTATION_READY' AND ")
	authority = strings.ReplaceAll(authority, "t.current_execution_id", "history.predecessor_execution_id")
	var exact bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t
 JOIN no_run_successor_eligibilities history ON history.task_id=t.id AND history.predecessor_execution_id=?
 JOIN execution_start_effects effect ON effect.project_id=t.project_id AND effect.task_id=t.id AND effect.feature_area_id=t.feature_area_id AND effect.task_definition_revision_id=t.current_definition_revision_id AND effect.execution_id=t.current_execution_id
 WHERE t.id=? AND t.project_id=? AND t.current_execution_id=? AND effect.actor_kind=? AND effect.actor_id=? AND effect.request_id=? AND effect.action=? AND effect.effect='START_AUTHORIZED' AND effect.expected_phase='IMPLEMENTATION_READY'
 AND EXISTS(SELECT 1 FROM confirmation_intents original,json_each(original.intent_json,'$.steps') step
  WHERE original.ai_session_id=effect.actor_id AND original.project_id=t.project_id AND json_extract(original.intent_json,'$.purpose')='EXECUTION' AND json_extract(original.intent_json,'$.action')='START_APPROVED' AND json_type(original.intent_json,'$.commit')='object'
  AND json_extract(original.intent_json,'$.content.execution.taskId')=t.id AND json_extract(step.value,'$.kind')='START_IMPLEMENTATION' AND json_extract(step.value,'$.requestId')=effect.request_id
  AND json_extract(original.intent_json,'$.content.execution.expectedTarget.featureAreaId')=?
  AND json_extract(original.intent_json,'$.content.execution.expectedTarget.taskDefinitionRevisionId')=?
  AND json_extract(original.intent_json,'$.content.execution.expectedTarget.executionId')=?
  AND json_extract(original.intent_json,'$.content.execution.expectedTarget.phase')=?
  AND COALESCE(json_extract(original.intent_json,'$.content.execution.expectedTarget.primaryMemberId'),'')=?
  AND COALESCE(json_extract(original.intent_json,'$.content.execution.expectedTarget.approvedWorksetHash'),'')=?)
 AND `+authority+`)`, expected.ExecutionID, t.ID, t.ProjectID, t.CurrentExecutionID, actor.Kind, actor.ID, actor.RequestID, actor.Action, expected.FeatureAreaID, expected.TaskDefinitionRevisionID, expected.ExecutionID, string(expected.Phase), expected.PrimaryMemberID, expected.ApprovedWorksetHash).Scan(&exact)
	if err != nil || !exact {
		return value, reserved, false, err
	}
	mapped := expected
	mapped.ExecutionID = t.CurrentExecutionID
	return existingTaskExecutionReservation(ctx, q, t, mapped)
}

// ReadTaskExecutionReservationCommand is receipt-only observation: it never
// reserves, prepares or dispatches. Identity changes require the exact original
// start effect; a new request cannot borrow an earlier canceled predecessor.
func (s *WorkflowProgressionStore) ReadTaskExecutionReservationCommand(ctx context.Context, sessionID, taskID, action, requestID string, expected wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, bool, error) {
	if sessionID == "" || taskID == "" || requestID == "" || action != "START_EXECUTION" && action != "START_VERIFICATION" {
		return executionproposal.Proposal{}, run.Run{}, false, executionproposal.ErrInvalidArgument
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=(SELECT project_id FROM ai_sessions WHERE id=?)`, taskID, sessionID))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	return existingTaskExecutionReservationCommand(ctx, tx, t, expected, executionStartActor{Kind: "AI_SESSION", ID: sessionID, Action: action, RequestID: requestID})
}
