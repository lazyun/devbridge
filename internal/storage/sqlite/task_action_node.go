package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/taskaction"
)

func taskActionFailure(code string) *confirmation.Failure {
	message := "当前任务已有待处理操作，请先核对原操作；不能另起确认。"
	if code == "TASK_ACTION_STALE" {
		message = "任务的当前步骤已变化，此旧确认不能再提交；请刷新当前操作。"
	}
	if code == "TASK_ACTION_CONFLICT" {
		message = "任务存在多个历史未决操作，需先核对并明确保留的链路。"
	}
	return confirmation.NewFailure(code, "CORE", "UNIQUE_CONSUMPTION", message, confirmation.ReadCurrentTarget, nil)
}

func taskActionOperatorTx(ctx context.Context, q definitionQuerier, taskID string) (string, string, error) {
	var id, state string
	err := q.QueryRowContext(ctx, `SELECT request_id,state FROM local_approve_and_start_commands WHERE task_id=? AND state NOT IN ('REJECTED','DIRECT_FILE_READY','STARTED','FINISHED') ORDER BY request_id LIMIT 1`, taskID).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return id, state, err
}

// A canceled admission without a Run remains the same chain. Its exact T2
// and cancellation eligibility permit the explicit stop-and-revise exit;
// they do not prove approval preparation completed or erase its history.
func taskActionNoRunParentTx(ctx context.Context, q definitionQuerier, taskID string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT h.owner_intent_id FROM task_action_heads h JOIN tasks t ON t.id=h.task_id JOIN formal_execution_t1 a ON a.intent_id=h.owner_intent_id AND a.task_id=t.id JOIN formal_execution_t2 b ON b.ai_session_id=a.ai_session_id AND b.request_id=a.request_id AND b.execution_id=t.current_execution_id JOIN no_run_successor_eligibilities e ON e.task_id=t.id AND e.predecessor_execution_id=b.execution_id AND e.task_definition_revision_id=t.current_definition_revision_id WHERE t.id=? AND t.status='ACTIVE' AND t.phase='NO_RUN_SUCCESSOR_REQUIRED' AND h.conflict=0 AND e.consumed_successor_execution_id IS NULL AND NOT (EXISTS(SELECT 1 FROM formal_execution_receipts z WHERE z.ai_session_id=a.ai_session_id AND z.request_id=a.request_id) AND EXISTS(SELECT 1 FROM runtime_selection_bindings v WHERE v.source_id=a.source_origin_id AND v.issuance_request_id='runtime_binding_'||a.source_origin_id))`, taskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func releaseTaskActionTx(ctx context.Context, tx *sql.Tx, taskID, parent, reason string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_action_releases(intent_id,task_id,generation,reason) SELECT owner_intent_id,task_id,generation,? FROM task_action_heads WHERE task_id=? AND owner_intent_id=?`, reason, taskID, parent); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE task_action_heads SET owner_intent_id=NULL,generation=generation+1 WHERE task_id=? AND owner_intent_id=?`, taskID, parent)
	return err
}

// The old immutable depth can never pass admission. The supplemental request
// is already committed, while its exact start has no Review/Run receipt. This
// proves the remaining step cannot execute even if a late caller retries it.
func taskActionShallowStartClosedTx(ctx context.Context, q definitionQuerier, i confirmation.Intent) (bool, error) {
	if i.Commit == nil || i.Purpose != confirmation.Review || i.Action != "REQUEST_SUPPLEMENTAL_REVIEW" || i.State != "PARTIAL" || i.Content.Review == nil || len(i.Steps) != 2 {
		return false, nil
	}
	b, request, start := i.Content.Review, i.Steps[0], i.Steps[1]
	if request.Kind != "REQUEST_REVIEW" || request.State != "COMPLETED" || request.TaskID != b.TaskID || start.Kind != "START_REVIEW" || start.Code != "REVIEW_DEPTH_TOO_SHALLOW" || start.ReviewID != "" || start.RunID != "" || b.ExpectedTarget == nil {
		return false, nil
	}
	var proven bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN task_supplemental_review_current c ON c.task_id=t.id JOIN task_supplemental_review_requests r ON r.id=c.request_id AND r.task_id=t.id WHERE t.id=? AND t.project_id=? AND t.status='ACTIVE' AND t.phase='REVIEW_REQUIRED' AND t.current_definition_revision_id=? AND t.current_review_id=? AND t.current_execution_id IS NULL AND t.required_review_depth='DEEP' AND ?='FAST' AND r.ai_session_id=? AND r.request_id=? AND NOT EXISTS(SELECT 1 FROM task_supplemental_review_starts s WHERE s.request_id=r.id) AND NOT EXISTS(SELECT 1 FROM feature_area_review_start_requests a WHERE a.feature_area_id=t.feature_area_id AND a.request_id=?))`, b.TaskID, i.ProjectID, b.ExpectedTarget.TaskDefinitionRevisionID, b.ExpectedTarget.CurrentReviewID, b.ReviewDepth, i.AISessionID, request.RequestID, start.RequestID).Scan(&proven)
	return proven, err
}

// Workspace preparation failed after its exact reservation was recorded. The
// grant is still disabled, and there are no unresolved file mutations. Keep
// those business facts and permit the existing explicit recovery contract.
func taskActionFilePreparationClosedTx(ctx context.Context, q definitionQuerier, i confirmation.Intent) (bool, error) {
	if i.Commit == nil || i.Purpose != confirmation.Execution || (i.Action != "START_DIRECT_FILE" && i.Action != "RECOVER_DIRECT_FILE") || i.Content.Execution == nil || len(i.Steps) != 1 {
		return false, nil
	}
	step := i.Steps[0]
	if step.Kind != i.Action || step.RequestID == "" {
		return false, nil
	}
	knownFailure := step.Effect == "RESERVED" && step.Failure != nil && (step.Failure.Code == "DIRECT_FILE_WORKTREE_UNAVAILABLE" || step.Failure.Code == "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
	var proven bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_start_receipts r JOIN direct_file_task_authorizations a ON a.workflow_id=r.workflow_id JOIN tasks t ON t.id=a.task_id WHERE r.ai_session_id=? AND r.request_id=? AND r.action=? AND json_extract(r.payload_json,'$.confirmationIntentId')=? AND a.task_id=? AND a.task_definition_revision_id=t.current_definition_revision_id AND a.generation=r.generation AND a.runtime_epoch=r.runtime_epoch AND a.state='PREPARING' AND (a.failure_code='DIRECT_FILE_WORKSPACE_PREPARATION_FAILED' OR ?) AND NOT EXISTS(SELECT 1 FROM direct_file_operation_receipts o WHERE o.workflow_id=a.workflow_id AND o.state IN ('PENDING','UNKNOWN')))`, i.AISessionID, step.RequestID, i.Action, i.IntentID, i.Content.Execution.TaskID, knownFailure).Scan(&proven)
	return proven, err
}

func taskActionDeferredPurposeTx(ctx context.Context, q definitionQuerier, taskID string) (string, string, error) {
	var id, session string
	err := q.QueryRowContext(ctx, `SELECT r.intent_id,r.intent_ai_session_id FROM recovery_purpose_confirmation_receipts r JOIN recovery_purpose_link_history l ON l.link_id=r.purpose_link_id AND l.link_ordinal=r.purpose_link_ordinal AND l.intent_id=r.intent_id AND l.intent_ai_session_id=r.intent_ai_session_id AND l.intent_version=r.intent_version AND l.intent_content_hash=r.intent_content_hash JOIN recovery_purpose_current_links p ON p.recovery_id=l.recovery_id AND p.recovery_generation=l.recovery_generation AND p.link_id=l.link_id AND p.link_ordinal=l.link_ordinal JOIN recovery_current_generations g ON g.recovery_id=l.recovery_id AND g.generation=l.recovery_generation AND g.owner_kind=l.source_owner_kind AND g.owner_id=l.source_owner_id WHERE l.task_id=? AND r.outcome='COMMITTED' AND r.consumption_disposition='UNCONSUMED' AND NOT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions c WHERE c.intent_id=r.intent_id AND c.intent_ai_session_id=r.intent_ai_session_id) ORDER BY r.intent_id LIMIT 1`, taskID).Scan(&id, &session)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return id, session, err
}

func confirmationTaskID(in confirmation.PrepareInput) string {
	if in.Execution != nil {
		return in.Execution.TaskID
	}
	if in.Review != nil {
		return in.Review.TaskID
	}
	if in.FeatureArea != nil {
		return in.FeatureArea.CompletionTaskID
	}
	if in.RecoveryPurpose != nil {
		return in.RecoveryPurpose.Proposal.TaskID
	}
	return ""
}

func bindCreatedTaskActionTx(ctx context.Context, tx *sql.Tx, taskID, project string) error {
	parent := taskaction.Intent(ctx)
	if parent == "" {
		return nil
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND project_id=?`, parent, project).Scan(&raw); err != nil {
		return err
	}
	var intent confirmation.Intent
	if err := json.Unmarshal([]byte(raw), &intent); err != nil {
		return err
	}
	if intent.Commit == nil || intent.Content.Task == nil && (intent.Content.FeatureArea == nil || intent.Content.FeatureArea.InitialTask == nil) {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_action_confirmation_bindings(intent_id,task_id,generation) SELECT ?,task_id,generation FROM task_action_heads WHERE task_id=?`, parent, taskID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE task_action_heads SET owner_intent_id=? WHERE task_id=?`, parent, taskID)
	return err
}

// Reconciliation happens only under the writer lock. Canonical readback may
// settle a consumed parent; absence of a receipt never releases an unknown one.
func reconcileTaskActionTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	if err := reconcileTaskActionConflictTx(ctx, tx, taskID); err != nil {
		return err
	}
	var owner sql.NullString
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation,owner_intent_id FROM task_action_heads WHERE task_id=?`, taskID).Scan(&generation, &owner); err != nil {
		return err
	}
	if !owner.Valid {
		return nil
	}
	if parent, err := taskActionNoRunParentTx(ctx, tx, taskID); err != nil {
		return err
	} else if parent == owner.String {
		return nil
	}
	var raw string
	var bound int64
	if err := tx.QueryRowContext(ctx, `SELECT c.intent_json,COALESCE((SELECT max(r.generation+1) FROM task_action_conflict_resolutions r WHERE r.intent_id=c.intent_id),b.generation) FROM confirmation_intents c JOIN task_action_confirmation_bindings b ON b.intent_id=c.intent_id WHERE c.intent_id=? AND b.task_id=?`, owner.String, taskID).Scan(&raw, &bound); err != nil {
		return err
	}
	var intent confirmation.Intent
	if err := json.Unmarshal([]byte(raw), &intent); err != nil {
		return err
	}
	if reason, err := taskActionReleaseReasonTx(ctx, tx, intent); err != nil {
		return err
	} else if reason != "" {
		return releaseTaskActionTx(ctx, tx, taskID, intent.IntentID, reason)
	}
	if intent.Commit != nil && intent.State != "COMPLETED" && intent.State != "REJECTED" {
		// Use the original Session's validated caller identity only when it is
		// available. The reducer itself reads exact canonical receipts, not ACLs.
		var epoch string
		if err := tx.QueryRowContext(ctx, `SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&epoch); err != nil {
			return err
		}
		recovered, err := recoverConfirmationTx(ctx, tx, intent, epoch)
		if err != nil {
			return err
		}
		if recovered.State == "COMPLETED" || recovered.State == "REJECTED" {
			return confirmationSave(ctx, tx, recovered)
		}
		return nil
	}
	if intent.State == "PENDING" && bound == generation && time.Now().Before(intent.ExpiresAt) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE task_action_heads SET owner_intent_id=NULL WHERE task_id=? AND owner_intent_id=?`, taskID, owner.String)
	return err
}

func bindTaskActionConfirmationTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) error {
	taskID := confirmationTaskID(intent.Content)
	if taskID == "" {
		return nil
	}
	if key, _, err := taskActionOperatorTx(ctx, tx, taskID); err != nil {
		return err
	} else if key != "" {
		return taskActionFailure("TASK_ACTION_PENDING")
	}
	if id, _, err := taskActionDeferredPurposeTx(ctx, tx, taskID); err != nil {
		return err
	} else if id != "" {
		return taskActionFailure("TASK_ACTION_PENDING")
	}
	if err := reconcileTaskActionTx(ctx, tx, taskID); err != nil {
		return err
	}
	var generation int64
	var owner sql.NullString
	var conflict bool
	if err := tx.QueryRowContext(ctx, `SELECT h.generation,h.owner_intent_id,h.conflict FROM task_action_heads h JOIN tasks t ON t.id=h.task_id WHERE h.task_id=? AND t.project_id=?`, taskID, intent.ProjectID).Scan(&generation, &owner, &conflict); err != nil {
		return err
	}
	if conflict {
		return taskActionFailure("TASK_ACTION_CONFLICT")
	}
	if intent.Content.SourceIntentID == "" && intent.Content.TaskActionGeneration != 0 && intent.Content.TaskActionGeneration != generation {
		return taskActionFailure("TASK_ACTION_STALE")
	}
	if expected, ok := taskaction.Expected(ctx); ok && (expected.TaskID != taskID || expected.Generation != generation) {
		return fmt.Errorf("%w: expected generation %d, current %d", taskActionFailure("TASK_ACTION_STALE"), expected.Generation, generation)
	}
	if owner.Valid && owner.String != intent.IntentID && owner.String != intent.Content.SourceIntentID {
		return taskActionFailure("TASK_ACTION_PENDING")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_action_confirmation_bindings(intent_id,task_id,generation) VALUES(?,?,?)`, intent.IntentID, taskID, generation); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE task_action_heads SET owner_intent_id=? WHERE task_id=?`, intent.IntentID, taskID)
	return err
}

func taskActionConfirmationFenceTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent) error {
	taskID := confirmationTaskID(intent.Content)
	if taskID == "" {
		return nil
	}
	if err := reconcileTaskActionConflictTx(ctx, tx, taskID); err != nil {
		return err
	}
	var current, bound int64
	var owner sql.NullString
	var conflict bool
	err := tx.QueryRowContext(ctx, `SELECT h.generation,COALESCE((SELECT max(r.generation+1) FROM task_action_conflict_resolutions r WHERE r.intent_id=b.intent_id),b.generation),h.owner_intent_id,h.conflict FROM task_action_heads h JOIN task_action_confirmation_bindings b ON b.task_id=h.task_id AND b.intent_id=? WHERE h.task_id=?`, intent.IntentID, taskID).Scan(&current, &bound, &owner, &conflict)
	if errors.Is(err, sql.ErrNoRows) {
		return taskActionFailure("TASK_ACTION_STALE")
	}
	if err != nil {
		return err
	}
	if conflict {
		return taskActionFailure("TASK_ACTION_CONFLICT")
	}
	if current != bound || !owner.Valid || owner.String != intent.IntentID {
		return taskActionFailure("TASK_ACTION_STALE")
	}
	return nil
}

// taskActionAdmissionTx is called after immutable receipt replay and before a
// canonical command's first mutation, under the same transaction as that write.
func taskActionAdmissionTx(ctx context.Context, tx *sql.Tx, taskID string) error {
	if taskID == "" {
		return nil
	}
	if id, _, err := taskActionDeferredPurposeTx(ctx, tx, taskID); err != nil {
		return err
	} else if id != "" && id != taskaction.Intent(ctx) {
		return taskActionFailure("TASK_ACTION_PENDING")
	}
	// Do not reconcile a parent's in-progress subtransaction against partially
	// written receipts. Its durable consumption and exact reservation already
	// prove ownership; nested canonical writers stay inside that same chain.
	if parent := taskaction.Intent(ctx); parent != "" {
		var own bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_action_heads h JOIN confirmation_intents c ON c.intent_id=h.owner_intent_id WHERE h.task_id=? AND h.conflict=0 AND h.owner_intent_id=? AND json_extract(c.intent_json,'$.commit') IS NOT NULL)`, taskID, parent).Scan(&own); err != nil {
			return err
		}
		if own {
			return nil
		}
	}
	if err := reconcileTaskActionTx(ctx, tx, taskID); err != nil {
		return err
	}
	var generation int64
	var owner sql.NullString
	var conflict bool
	if err := tx.QueryRowContext(ctx, `SELECT generation,owner_intent_id,conflict FROM task_action_heads WHERE task_id=?`, taskID).Scan(&generation, &owner, &conflict); err != nil {
		return err
	}
	if conflict {
		return taskActionFailure("TASK_ACTION_CONFLICT")
	}
	command, _, err := taskActionOperatorTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if command != "" && command != taskaction.Command(ctx) {
		return taskActionFailure("TASK_ACTION_PENDING")
	}
	if expected, ok := taskaction.Expected(ctx); ok && command == "" && (expected.TaskID != taskID || expected.Generation != generation) {
		return taskActionFailure("TASK_ACTION_STALE")
	}
	if owner.Valid {
		if owner.String != taskaction.Intent(ctx) {
			return taskActionFailure("TASK_ACTION_PENDING")
		}
		var consumed bool
		if err := tx.QueryRowContext(ctx, `SELECT json_extract(intent_json,'$.commit') IS NOT NULL FROM confirmation_intents WHERE intent_id=?`, owner.String).Scan(&consumed); err != nil {
			return err
		}
		if !consumed {
			return taskActionFailure("TASK_ACTION_PENDING")
		}
	}
	return nil
}

// ReadTaskActionNode returns a safe task-wide summary. A foreign Session never
// receives the owning Session identity, request, variant, or confirmation ID.
func (s *ConfirmationStore) ReadTaskActionNode(ctx context.Context, project, taskID, session string) (taskaction.Node, error) {
	var out taskaction.Node
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if session != "" {
		if err = instructionCaller(ctx, tx, session); err != nil {
			return out, err
		}
		var allowed bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_sessions WHERE id=? AND project_id=?)`, session, project).Scan(&allowed); err != nil {
			return out, err
		}
		if !allowed {
			return out, ErrNotFound
		}
	} else if actor, ok := localoperator.FromContext(ctx); !ok || actor.ProjectID() != project {
		return out, ErrNotFound
	}
	var owner sql.NullString
	var conflict bool
	err = tx.QueryRowContext(ctx, `SELECT h.generation,h.owner_intent_id,h.conflict,t.phase FROM task_action_heads h JOIN tasks t ON t.id=h.task_id WHERE h.task_id=? AND t.project_id=?`, taskID, project).Scan(&out.Generation, &owner, &conflict, &out.Kind)
	if err != nil {
		return out, err
	}
	out.TaskID, out.ID, out.State = taskID, fmt.Sprintf("%s:%d", taskID, out.Generation), "AVAILABLE"
	if conflict {
		facts, e := taskActionConflictFactsTx(ctx, tx, taskID)
		if e != nil {
			return out, e
		}
		values := facts.active
		if len(values) > 1 {
			out.State, out.Blocking, out.Reason = "CONFLICT", true, taskActionFailure("TASK_ACTION_CONFLICT").Message
			consumed, count := "", 0
			for _, value := range values {
				if value.Commit != nil {
					consumed = value.IntentID
					count++
				}
			}
			for _, value := range values {
				if value.AISessionID == session {
					out.Candidates = append(out.Candidates, taskaction.Candidate{IntentID: value.IntentID, Version: value.Version, Purpose: string(value.Purpose), ContentHash: value.ContentHash, Action: value.Action, Consumed: value.Commit != nil, Selectable: (count == 0 || count == 1 && consumed == value.IntentID) && (value.Commit != nil || time.Now().Before(value.ExpiresAt))})
				}
			}
			return out, nil
		}
		owner = sql.NullString{}
		if len(values) == 1 {
			owner = sql.NullString{String: values[0].IntentID, Valid: true}
		} else {
			out.Generation++
			out.ID = fmt.Sprintf("%s:%d", taskID, out.Generation)
		}
	}
	if key, state, e := taskActionOperatorTx(ctx, tx, taskID); e != nil {
		return out, e
	} else if key != "" {
		out.Kind, out.State, out.Blocking, out.Reason = "LOCAL_APPROVE_AND_START", state, true, "原 WebUI 操作尚未完成，请核对或恢复该原请求。"
		return out, nil
	}
	if id, owner, e := taskActionDeferredPurposeTx(ctx, tx, taskID); e != nil {
		return out, e
	} else if id != "" {
		out.Kind, out.State, out.Blocking, out.Owned, out.Reason = "RECOVERY_PURPOSE", "AWAITING_CORE_CONSUMPTION", true, owner == session, "原恢复方案已确认，等待 Core 完成原链路处理。"
		if out.Owned {
			out.IntentID = id
		}
		return out, nil
	}
	if !owner.Valid {
		return out, nil
	}
	var raw string
	var bound int64
	err = tx.QueryRowContext(ctx, `SELECT c.intent_json,COALESCE((SELECT max(r.generation+1) FROM task_action_conflict_resolutions r WHERE r.intent_id=c.intent_id),b.generation) FROM confirmation_intents c JOIN task_action_confirmation_bindings b ON b.intent_id=c.intent_id WHERE c.intent_id=?`, owner.String).Scan(&raw, &bound)
	if err != nil {
		return out, err
	}
	var intent confirmation.Intent
	if err = json.Unmarshal([]byte(raw), &intent); err != nil {
		return out, err
	}
	if reason, e := taskActionReleaseReasonTx(ctx, tx, intent); e != nil {
		return out, e
	} else if reason != "" {
		out.Generation++
		out.ID = fmt.Sprintf("%s:%d", taskID, out.Generation)
		return out, nil
	}
	if intent.Commit == nil && (intent.State != "PENDING" || bound != out.Generation || !time.Now().Before(intent.ExpiresAt)) {
		return out, nil
	}
	if intent.Commit != nil {
		if parent, e := taskActionNoRunParentTx(ctx, tx, taskID); e != nil {
			return out, e
		} else if parent == intent.IntentID {
			out.Kind, out.State, out.Blocking, out.Owned, out.Reason = "NO_RUN_RECOVERY", "PARTIAL", true, session != "" && session == intent.AISessionID, "原执行已在派发前取消，请沿当前链路停止并修订；原审批及部分步骤保留。"
			if out.Owned {
				out.IntentID = parent
			}
			return out, nil
		}
		intent, err = recoverConfirmationTx(ctx, tx, intent, s.db.epoch)
		if err != nil {
			return out, err
		}
		if intent.State == "COMPLETED" || intent.State == "REJECTED" {
			out.Generation++
			out.ID = fmt.Sprintf("%s:%d", taskID, out.Generation)
			return out, nil
		}
	}
	out.State, out.Blocking, out.Owned, out.Action = intent.State, true, session != "" && intent.AISessionID == session, intent.Action
	out.Reason = taskActionFailure("TASK_ACTION_PENDING").Message
	if out.Owned {
		out.IntentID = intent.IntentID
		out.Reason = "请处理当前准确确认；其他操作在此链路完成后开放。"
	}
	return out, nil
}

func (s *ConfirmationStore) TaskActionConfirmationCurrent(ctx context.Context, session string, intent confirmation.Intent) (bool, error) {
	if confirmationTaskID(intent.Content) == "" || intent.Commit != nil {
		return true, nil
	}
	n, err := s.ReadTaskActionNode(ctx, intent.ProjectID, confirmationTaskID(intent.Content), session)
	if err != nil {
		return false, err
	}
	return n.State != "CONFLICT" && n.IntentID == intent.IntentID, nil
}

func (s *ConfirmationStore) DiscardProposal(ctx context.Context, in confirmation.ReadInput) (confirmation.Intent, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	out, err := confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return out, err
	}
	if out.Version != in.Version || out.Purpose != in.Purpose || out.ContentHash != in.ContentHash {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	if out.State == "SUPERSEDED" {
		return out, nil
	}
	if out.Commit != nil {
		return recoverConfirmationTx(ctx, tx, out, s.db.epoch)
	}
	if out.State != "PENDING" && out.State != "STALE" {
		return out, taskActionFailure("TASK_ACTION_STALE")
	}
	out.State = "SUPERSEDED"
	if err = confirmationSave(ctx, tx, out); err != nil {
		return out, err
	}
	if id := confirmationTaskID(out.Content); id != "" {
		var conflict bool
		if err = tx.QueryRowContext(ctx, `SELECT conflict FROM task_action_heads WHERE task_id=?`, id).Scan(&conflict); err != nil {
			return out, err
		}
		if conflict {
			if err = reconcileTaskActionConflictTx(ctx, tx, id); err != nil {
				return out, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return confirmation.Intent{}, err
	}
	return out, nil
}

func taskActionConflictCandidates(ctx context.Context, q *sql.Tx, taskID string) ([]confirmation.Intent, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.intent_json FROM task_action_confirmation_bindings b JOIN confirmation_intents c ON c.intent_id=b.intent_id WHERE b.task_id=? AND NOT EXISTS(SELECT 1 FROM task_action_releases r WHERE r.intent_id=b.intent_id AND r.task_id=b.task_id) AND json_extract(c.intent_json,'$.state') IN ('PENDING','CONFIRMING','UNKNOWN','PARTIAL') ORDER BY c.intent_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []confirmation.Intent
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var i confirmation.Intent
		if err = json.Unmarshal([]byte(raw), &i); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *ConfirmationStore) ResolveTaskActionConflict(ctx context.Context, in taskaction.ConflictSelection) (confirmation.Intent, error) {
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return confirmation.Intent{}, err
	}
	defer tx.Rollback()
	selected, err := confirmationRead(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		return selected, err
	}
	if selected.Version != in.Version || string(selected.Purpose) != in.Purpose || selected.ContentHash != in.ContentHash || confirmationTaskID(selected.Content) != in.TaskID {
		return confirmation.Intent{}, confirmation.ErrRequestConflict
	}
	var authorized bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t JOIN ai_sessions s ON s.project_id=t.project_id WHERE t.id=? AND t.project_id=? AND s.id=?)`, in.TaskID, selected.ProjectID, in.AISessionID).Scan(&authorized); err != nil {
		return selected, err
	}
	if !authorized {
		return confirmation.Intent{}, sql.ErrNoRows
	}
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT intent_id FROM task_action_conflict_resolutions WHERE task_id=? AND generation=?`, in.TaskID, in.Generation).Scan(&prior)
	if err == nil {
		if prior != in.IntentID {
			return confirmation.Intent{}, confirmation.ErrRequestConflict
		}
		return selected, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return selected, err
	}
	var generation int64
	var conflict bool
	if err = tx.QueryRowContext(ctx, `SELECT generation,conflict FROM task_action_heads WHERE task_id=?`, in.TaskID).Scan(&generation, &conflict); err != nil {
		return selected, err
	}
	if !conflict || generation != in.Generation {
		return selected, taskActionFailure("TASK_ACTION_STALE")
	}
	facts, err := taskActionConflictFactsTx(ctx, tx, in.TaskID)
	if err != nil {
		return selected, err
	}
	if err = saveTaskActionConflictFactsTx(ctx, tx, in.TaskID, facts); err != nil {
		return selected, err
	}
	values := facts.active
	found := false
	for _, v := range values {
		if v.IntentID == selected.IntentID {
			found = true
			continue
		}
		// A consumed sibling must be reconciled through its own canonical
		// receipts. Explicit selection never erases an uncertain effect.
		if v.Commit != nil {
			return selected, taskActionFailure("TASK_ACTION_CONFLICT")
		}
	}
	if !found || selected.Commit == nil && !time.Now().Before(selected.ExpiresAt) {
		return selected, taskActionFailure("TASK_ACTION_STALE")
	}
	for _, v := range values {
		if v.IntentID != selected.IntentID {
			v.State = "SUPERSEDED"
			if err = confirmationSave(ctx, tx, v); err != nil {
				return selected, err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_action_conflict_resolutions(task_id,generation,intent_id) VALUES(?,?,?)`, in.TaskID, in.Generation, in.IntentID); err != nil {
		return selected, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE task_action_heads SET generation=generation+1,conflict=0,owner_intent_id=? WHERE task_id=? AND generation=?`, in.IntentID, in.TaskID, in.Generation); err != nil {
		return selected, err
	}
	if err = tx.Commit(); err != nil {
		return confirmation.Intent{}, err
	}
	return selected, nil
}
