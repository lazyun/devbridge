package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func reviewRequestRejected(reason string) error { return wp.Reject(reason, wp.Guidance{}) }

type supplementalReviewFacts struct {
	task          task.Task
	target        wp.TaskReviewRequestTarget
	closeWorkflow string
}

// Both qualification and the write reducer call this same pure predicate while
// holding the original Authority's operation gate and grant mutex. No file I/O.
func supplementalReviewFactsTx(ctx context.Context, tx *sql.Tx, db *DB, session, taskID string) (f supplementalReviewFacts, err error) {
	if err = instructionCaller(ctx, tx, session); err != nil {
		return
	}
	var project string
	if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, session).Scan(&project); err != nil {
		return
	}
	return supplementalReviewProjectFactsTx(ctx, tx, db, project, session, taskID)
}

func supplementalReviewProjectFactsTx(ctx context.Context, tx *sql.Tx, db *DB, project, session, taskID string) (f supplementalReviewFacts, err error) {
	f.task, err = scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, taskID, project))
	if err != nil {
		return
	}
	t := f.task
	f.target = wp.TaskReviewRequestTarget{Phase: t.Phase, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, CurrentReviewID: t.CurrentReviewID, CurrentExecutionID: t.CurrentExecutionID}
	if t.Status != task.StatusActive || t.LegacyReadOnly || t.Type != task.TypeDevelopment {
		return f, reviewRequestRejected("TASK_NOT_ACTIVE")
	}
	var held bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_holds WHERE task_id=?)`, t.ID).Scan(&held); err != nil {
		return f, err
	}
	if held {
		return f, reviewRequestRejected("TASK_DEFERRED")
	}
	if t.Phase == task.PhaseReviewRequired {
		return f, reviewRequestRejected("REVIEW_ALREADY_REQUIRED")
	}
	if t.Phase == task.PhaseReviewing {
		return f, reviewRequestRejected("REVIEW_IN_PROGRESS")
	}
	if t.Phase == task.PhaseReviewApprovalPending {
		var pending bool
		decision := wp.DecisionID(t.CurrentReviewID)
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_approval_decisions d WHERE d.decision_id=? AND d.task_id=? AND d.project_id=? AND NOT EXISTS(SELECT 1 FROM decision_resolution_receipts r WHERE r.decision_id=d.decision_id))`, decision, t.ID, t.ProjectID).Scan(&pending); err != nil {
			return
		}
		if pending {
			f.target.PendingDecisionID = decision
		}
		return f, reviewRequestRejected("USE_CURRENT_REVIEW_REVISION")
	}
	switch t.Phase {
	case task.PhaseImplementationReady, task.PhaseImplementing, task.PhaseVerifying, task.PhaseVerificationReady, task.PhaseAcceptancePending:
	case task.PhaseRecoveryRequired:
		// An explicit same-Task continuation decision may request a revised
		// Review after verification. Every native writer/claim and finalization
		// fence below still applies; an unknown old result is never fabricated.
		var continued bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_hold_receipts c WHERE c.task_id=? AND c.execution_id=? AND c.revision_id=? AND c.action='CONTINUE_TASK') AND NOT EXISTS(SELECT 1 FROM task_holds WHERE task_id=?)`, t.ID, t.CurrentExecutionID, t.CurrentDefinitionRevisionID, t.ID).Scan(&continued); err != nil {
			return f, err
		}
		if !continued || t.RecoveryOrigin == nil || t.RecoveryOrigin.Phase != task.PhaseVerifying {
			return f, reviewRequestRejected("TASK_CONTINUATION_CONFIRMATION_REQUIRED")
		}
	default:
		return f, reviewRequestRejected("TASK_PHASE_NOT_ELIGIBLE")
	}
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND project_id=? AND status='ACTIVE' AND legacy_read_only=0)`, t.FeatureAreaID, t.ProjectID).Scan(&active)
	if err != nil {
		return
	}
	if !active {
		return f, reviewRequestRejected("FEATURE_AREA_NOT_ACTIVE")
	}
	if t.CurrentDefinitionRevisionID == "" || t.ReviewApprovalID == "" {
		return f, reviewRequestRejected("APPROVED_DEFINITION_REQUIRED")
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_definition_revisions r JOIN review_approval_decisions d ON d.decision_id=? JOIN decision_resolution_receipts a ON a.decision_id=d.decision_id WHERE r.id=? AND r.task_id=? AND d.task_id=r.task_id AND d.review_id=? AND d.project_id=? AND a.option='APPROVE' AND a.project_id=d.project_id)`, t.ReviewApprovalID, t.CurrentDefinitionRevisionID, t.ID, t.CurrentReviewID, t.ProjectID).Scan(&active)
	if err != nil {
		return
	}
	if !active {
		return f, reviewRequestRejected("APPROVED_DEFINITION_MISMATCH")
	}
	authority, e := readRuntimeAuthority(ctx, tx, t.FeatureAreaID)
	if e != nil {
		return f, e
	}
	if authority.Busy {
		return f, reviewRequestRejected("RUNTIME_AUTHORITY_BUSY")
	}
	var finalizing bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_commit_receipts WHERE feature_area_id=? AND completed_at IS NULL) OR EXISTS(SELECT 1 FROM pending_feature_area_commits WHERE feature_area_id=? AND completed_at IS NULL)`, t.FeatureAreaID, t.FeatureAreaID).Scan(&finalizing)
	if err != nil {
		return
	}
	if finalizing {
		return f, reviewRequestRejected("GIT_FINALIZATION_PENDING")
	}
	if directFileGrants.byDB[db] != nil && directFileGrants.byDB[db][t.FeatureAreaID] != nil {
		return f, reviewRequestRejected("UNRELATED_FILE_WORKFLOW_ACTIVE")
	}
	var workflow, owner, revision, grantSession, state string
	err = tx.QueryRowContext(ctx, `SELECT workflow_id,task_id,task_definition_revision_id,ai_session_id,state FROM direct_file_task_authorizations WHERE feature_area_id=? AND state IN ('ACTIVE','PREPARING','RECOVERY_REQUIRED')`, t.FeatureAreaID).Scan(&workflow, &owner, &revision, &grantSession, &state)
	if err == nil {
		var protocol int
		if err = tx.QueryRowContext(ctx, `SELECT protocol_version FROM direct_file_task_authorizations WHERE workflow_id=?`, workflow).Scan(&protocol); err != nil {
			return
		}
		if protocol == 2 {
			return f, reviewRequestRejected("DIRECT_FILE_EXPLICIT_CANCEL_REQUIRED")
		}
		// A local operator is never a Direct File grant owner. It may request a
		// Review only if no active grant needs closing; a Session source may
		// close only its own exact Task/revision grant below.
		if session == "" {
			return f, reviewRequestRejected("UNRELATED_FILE_WORKFLOW_ACTIVE")
		}
		if owner != t.ID || revision != t.CurrentDefinitionRevisionID || grantSession != session {
			return f, reviewRequestRejected("UNRELATED_FILE_WORKFLOW_ACTIVE")
		}
		if state == "PREPARING" {
			return f, reviewRequestRejected("DIRECT_FILE_PREPARATION_IN_PROGRESS")
		}
		var sameAuthority bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM direct_file_task_authorizations WHERE workflow_id=? AND project_id=? AND caller_scope IS (SELECT caller_scope FROM ai_sessions WHERE id=?))`, workflow, t.ProjectID, session).Scan(&sameAuthority); err != nil {
			return
		}
		if !sameAuthority {
			return f, reviewRequestRejected("FILE_WORKFLOW_AUTHORITY_MISMATCH")
		}
		f.closeWorkflow = workflow
	} else if !errors.Is(err, sql.ErrNoRows) {
		return
	}
	err = nil
	if t.CurrentExecutionID == "" {
		if t.Phase != task.PhaseImplementationReady && !(t.WorkSpec.ImplementationMode == task.ImplementationDirectFile && t.Phase == task.PhaseImplementing) {
			return f, reviewRequestRejected("CURRENT_EXECUTION_REQUIRED")
		}
		return f, nil
	}
	predecessor, e := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, t.CurrentExecutionID))
	if e != nil {
		return f, e
	}
	if predecessor.TaskID != t.ID || predecessor.ProjectID != t.ProjectID || predecessor.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
		return f, reviewRequestRejected("EXECUTION_TARGET_MISMATCH")
	}
	var hasRun bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE execution_proposal_id=? OR agent_execution_id=?)`, predecessor.ID, predecessor.ID).Scan(&hasRun)
	if err != nil {
		return
	}
	if !hasRun {
		if predecessor.Status != executionproposal.StatusConfirmed || predecessor.Outcome != executionproposal.OutcomePending || predecessor.RunID != "" || predecessor.ImplementationRunID != "" || predecessor.VerificationRunID != "" {
			return f, reviewRequestRejected("EXECUTION_NOT_UNDISPATCHED")
		}
		return f, nil
	}
	// Between implementation completion and verification reservation the
	// product Execution is still RUNNING. This is not itself a live writer.
	verificationUnreserved := t.Phase == task.PhaseVerificationReady && predecessor.Status == executionproposal.StatusConsumed && predecessor.Outcome == executionproposal.OutcomeRunning && predecessor.VerificationSpec.Mode == task.VerificationModeRun && predecessor.ImplementationRunID != "" && predecessor.RunID == predecessor.ImplementationRunID && predecessor.VerificationRunID == ""
	if verificationUnreserved {
		var exactGap bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs implementation WHERE implementation.id=? AND implementation.execution_proposal_id=? AND implementation.task_id=? AND implementation.project_id=? AND implementation.feature_area_id=? AND implementation.purpose='IMPLEMENTATION' AND implementation.status='SUCCEEDED'
 AND NOT EXISTS(SELECT 1 FROM runs other WHERE other.execution_proposal_id=implementation.execution_proposal_id AND other.id<>implementation.id))`, predecessor.ImplementationRunID, predecessor.ID, t.ID, t.ProjectID, t.FeatureAreaID).Scan(&exactGap)
		if err != nil {
			return
		}
		if !exactGap {
			return f, reviewRequestRejected("VERIFICATION_ALREADY_RESERVED_OR_IMPLEMENTATION_INCOMPLETE")
		}
	}
	if !verificationUnreserved && predecessor.Outcome != executionproposal.OutcomeSucceeded && predecessor.Outcome != executionproposal.OutcomeFailed && predecessor.Outcome != executionproposal.OutcomeCanceled {
		return f, reviewRequestRejected("EXECUTION_NOT_TERMINAL")
	}
	// Multi-repository workspaces intentionally have no scalar repository_id.
	// Match the Run's immutable primary-member binding to that workspace;
	// single-repository records retain their original repository check.
	var closed bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r WHERE r.id=? AND r.task_id=? AND r.project_id=? AND r.feature_area_id=? AND r.execution_proposal_id=? AND r.operation_kind='EXECUTION' AND r.operation_id=? AND r.status IN ('SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED')
 AND (r.runtime_epoch IS NULL OR EXISTS(SELECT 1 FROM runtime_epochs ep WHERE ep.id=r.runtime_epoch))
 AND (r.workspace_id IS NULL OR EXISTS(SELECT 1 FROM workspaces w WHERE w.id=r.workspace_id AND w.project_id=r.project_id AND w.feature_area_id=r.feature_area_id AND ((w.repository_id=? AND ?<>'') OR (w.repository_id IS NULL AND ? > 0 AND EXISTS(SELECT 1 FROM run_repository_bindings member JOIN workspace_members m ON m.id=member.member_id AND m.workspace_id=member.workspace_id AND m.repository_id=member.repository_id AND m.identity_version=member.member_version AND m.origin_commit=member.origin_commit WHERE member.run_id=r.id AND member.execution_id=r.execution_proposal_id AND member.project_id=r.project_id AND member.feature_area_id=r.feature_area_id AND member.workspace_id=w.id AND member.member_id=?)))))
 AND (EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id) OR (r.thread_id IS NULL AND r.turn_id IS NULL AND r.prompt_sent_at IS NULL AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id)))
 AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=r.id AND i.advance_state<>'UNKNOWN' AND i.applied_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM agent_operation_boundaries b WHERE b.run_id=r.id AND b.advance_state<>'UNKNOWN' AND NOT EXISTS(SELECT 1 FROM agent_finalization_intents i WHERE i.run_id=b.run_id AND i.runtime_epoch=r.runtime_epoch AND i.applied_at IS NOT NULL)))`, terminalExecutionRunID(predecessor), t.ID, t.ProjectID, t.FeatureAreaID, predecessor.ID, predecessor.ID, t.RepositoryID, t.RepositoryID, len(t.RepositoryTargets), predecessor.PrimaryMemberID).Scan(&closed)
	if err != nil {
		return
	}
	if !closed {
		return f, reviewRequestRejected("EXECUTION_FINALIZATION_REQUIRED")
	}
	if verificationUnreserved {
		// No Task acceptance decision exists yet. Supersession preserves the
		// old product outcome and creates neither a failure nor a fake gate.
		return f, nil
	}
	var resolved bool
	decision := decisionresolution.TaskAcceptanceDecisionID(predecessor.ID)
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decision_resolution_receipts WHERE decision_id=?)`, decision).Scan(&resolved)
	if err != nil {
		return
	}
	if !resolved {
		f.target.PendingDecisionID = decision
	}
	return f, nil
}

const supplementalRequestSelect = `SELECT id,project_id,task_id,feature_area_id,request_id,purpose,source_revision_id,source_review_id,source_execution_id,source_decision_id,source_phase,created_at,payload_sha256 FROM task_supplemental_review_requests`

func scanSupplementalRequest(row *sql.Row) (out wp.TaskReviewRequestResolution, hash string, err error) {
	var phase task.Phase
	var stamp string
	err = row.Scan(&out.ID, &out.ProjectID, &out.TaskID, &out.FeatureAreaID, &out.RequestID, &out.Purpose, &out.SourceRevisionID, &out.SourceReviewID, &out.SourceExecutionID, &out.SupersededDecisionID, &phase, &stamp, &hash)
	if err != nil {
		return
	}
	out.ExpectedTarget = &wp.TaskReviewRequestTarget{Phase: phase, TaskDefinitionRevisionID: out.SourceRevisionID, CurrentReviewID: out.SourceReviewID, CurrentExecutionID: out.SourceExecutionID, PendingDecisionID: out.SupersededDecisionID}
	out.Phase = task.PhaseReviewRequired
	out.RequestedAt, err = parseTime(stamp)
	return
}

func (s *WorkflowProgressionStore) TaskReviewRequestEligibility(ctx context.Context, session, taskID string) (out wp.TaskReviewRequestEligibility, err error) {
	return s.taskReviewRequestEligibility(ctx, session, "", taskID)
}

func (s *WorkflowProgressionStore) TaskReviewRequestEligibilityForOperator(ctx context.Context, project, taskID string) (wp.TaskReviewRequestEligibility, error) {
	if err := localoperator.RequireProject(ctx, project); err != nil {
		return wp.TaskReviewRequestEligibility{}, err
	}
	return s.taskReviewRequestEligibility(ctx, "", project, taskID)
}
func (s *WorkflowProgressionStore) taskReviewRequestEligibility(ctx context.Context, session, project, taskID string) (out wp.TaskReviewRequestEligibility, err error) {
	if !s.db.directFileOperations.TryLock() {
		out.Reason = "DIRECT_FILE_OPERATION_IN_PROGRESS"
		return
	}
	defer s.db.directFileOperations.Unlock()
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return
	}
	defer tx.Rollback()
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	var facts supplementalReviewFacts
	var e error
	if project != "" {
		facts, e = supplementalReviewProjectFactsTx(ctx, tx, s.db, project, "", taskID)
	} else {
		facts, e = supplementalReviewFactsTx(ctx, tx, s.db, session, taskID)
	}
	out.Target = &facts.target
	if facts.task.ID != "" {
		// Historical replacement is durable provenance, independent of the
		// current supplemental request and of today's execution eligibility.
		rows, historyErr := tx.QueryContext(ctx, `SELECT x.execution_id,x.request_id FROM task_review_superseded_executions x JOIN task_supplemental_review_requests req ON req.id=x.request_id JOIN execution_proposals execution ON execution.id=x.execution_id WHERE req.task_id=? AND req.project_id=? AND execution.task_id=req.task_id AND execution.project_id=req.project_id AND req.source_execution_id=execution.id`, facts.task.ID, facts.task.ProjectID)
		if historyErr != nil {
			return out, historyErr
		}
		out.SupersededExecutions = map[string]string{}
		for rows.Next() {
			var executionID, requestID string
			if historyErr = rows.Scan(&executionID, &requestID); historyErr != nil {
				rows.Close()
				return out, historyErr
			}
			out.SupersededExecutions[executionID] = requestID
		}
		historyErr = rows.Err()
		rows.Close()
		if historyErr != nil {
			return out, historyErr
		}
		value, _, readErr := scanSupplementalRequest(tx.QueryRowContext(ctx, supplementalRequestSelect+` WHERE id=(SELECT request_id FROM task_supplemental_review_current WHERE task_id=?)`, facts.task.ID))
		if readErr == nil {
			out.CurrentRequest = &value
		} else if !errors.Is(readErr, sql.ErrNoRows) {
			return out, readErr
		}
	}
	if e != nil {
		var rejected *wp.Error
		if errors.Is(e, aisession.ErrScopeMismatch) {
			out.Target = nil
			out.Reason = "SESSION_SCOPE_MISMATCH"
			return
		}
		if errors.As(e, &rejected) {
			out.Reason = rejected.Code
			return
		}
		return out, e
	}
	out.Available = true
	return
}

func supplementalRequestSource(ctx context.Context, in wp.TaskReviewRequestInput) (projectID, actorKind, actorID string, operator bool, err error) {
	in.AISessionID, in.OperatorPrincipalID = strings.TrimSpace(in.AISessionID), strings.TrimSpace(in.OperatorPrincipalID)
	operator = in.OperatorPrincipalID != ""
	if (in.AISessionID == "") == (in.OperatorPrincipalID == "") {
		return "", "", "", false, reviewRequestRejected("INVALID_REVIEW_REQUEST")
	}
	if !operator {
		if !aisession.ValidRequestID(in.AISessionID) {
			return "", "", "", false, reviewRequestRejected("INVALID_REVIEW_REQUEST")
		}
		return "", "AI_SESSION", in.AISessionID, false, nil
	}
	actor, ok := localoperator.FromContext(ctx)
	if !ok || actor.ProjectID() == "" || actor.PrincipalID() != in.OperatorPrincipalID || actor.Check(actor.ProjectID(), in.TaskID, wp.ActionRequestTaskReview) != nil {
		return "", "", "", false, reviewRequestRejected("OPERATOR_ACCESS_REQUIRED")
	}
	return actor.ProjectID(), "LOCAL_OPERATOR", actor.PrincipalID(), true, nil
}

func supplementalSessionValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *WorkflowProgressionStore) TaskReviewRequestReplayForOperator(ctx context.Context, projectID string, in wp.TaskReviewRequestInput) (bool, error) {
	if in.ExpectedTarget == nil || !aisession.ValidRequestID(in.TaskID) || !aisession.ValidRequestID(in.FeatureAreaID) || !aisession.ValidRequestID(in.RequestID) || strings.TrimSpace(in.Purpose) == "" || len(in.Purpose) > 65536 {
		return false, reviewRequestRejected("INVALID_REVIEW_REQUEST")
	}
	actualProject, kind, actorID, operator, err := supplementalRequestSource(ctx, in)
	if err != nil || !operator || actualProject != projectID {
		if err != nil {
			return false, err
		}
		return false, reviewRequestRejected("OPERATOR_ACCESS_REQUIRED")
	}
	bytes, err := json.Marshal(in)
	if err != nil {
		return false, err
	}
	read, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer read.Rollback()
	replay, saved, err := scanSupplementalRequest(read.QueryRowContext(ctx, supplementalRequestSelect+` WHERE project_id=? AND actor_kind=? AND actor_id=? AND request_id=?`, projectID, kind, actorID, in.RequestID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if saved != workflowHash(string(bytes)) || replay.TaskID != in.TaskID || replay.FeatureAreaID != in.FeatureAreaID || replay.Purpose != in.Purpose || replay.ExpectedTarget == nil || *replay.ExpectedTarget != *in.ExpectedTarget {
		return false, executionproposal.ErrConflict
	}
	return true, nil
}

func (s *WorkflowProgressionStore) RequestTaskReview(ctx context.Context, in wp.TaskReviewRequestInput) (out wp.TaskReviewRequestResolution, err error) {
	if in.ExpectedTarget == nil || !aisession.ValidRequestID(in.TaskID) || !aisession.ValidRequestID(in.FeatureAreaID) || !aisession.ValidRequestID(in.RequestID) || strings.TrimSpace(in.Purpose) == "" || len(in.Purpose) > 65536 {
		return out, reviewRequestRejected("INVALID_REVIEW_REQUEST")
	}
	projectID, actorKind, actorID, operator, err := supplementalRequestSource(ctx, in)
	if err != nil {
		return out, err
	}
	bytes, e := json.Marshal(in)
	if e != nil {
		return out, e
	}
	hash := workflowHash(string(bytes))
	// Receipt lookup precedes currentness and the nonwaiting operation guard.
	read, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	if !operator {
		err = instructionCaller(ctx, read, in.AISessionID)
	}
	if err != nil {
		read.Rollback()
		return out, err
	}
	if !operator {
		if err = read.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, in.AISessionID).Scan(&projectID); err != nil {
			read.Rollback()
			return out, err
		}
	}
	replay, saved, e := scanSupplementalRequest(read.QueryRowContext(ctx, supplementalRequestSelect+` WHERE project_id=? AND actor_kind=? AND actor_id=? AND request_id=?`, projectID, actorKind, actorID, in.RequestID))
	read.Rollback()
	if e == nil {
		if saved != hash {
			return out, executionproposal.ErrConflict
		}
		replay.Replay = true
		return replay, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if !s.db.directFileOperations.TryLock() {
		return out, reviewRequestRejected("DIRECT_FILE_OPERATION_IN_PROGRESS")
	}
	defer s.db.directFileOperations.Unlock()
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	if !operator {
		if err = tx.QueryRowContext(ctx, `SELECT project_id FROM ai_sessions WHERE id=?`, in.AISessionID).Scan(&projectID); err != nil {
			return out, err
		}
	}
	defer tx.Rollback()
	directFileGrants.Lock()
	defer directFileGrants.Unlock()
	if !operator {
		err = instructionCaller(ctx, tx, in.AISessionID)
	}
	if err != nil {
		return out, err
	}
	replay, saved, e = scanSupplementalRequest(tx.QueryRowContext(ctx, supplementalRequestSelect+` WHERE project_id=? AND actor_kind=? AND actor_id=? AND request_id=?`, projectID, actorKind, actorID, in.RequestID))
	if e == nil {
		if saved != hash {
			return out, executionproposal.ErrConflict
		}
		replay.Replay = true
		return replay, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if err = taskActionAdmissionTx(ctx, tx, in.TaskID); err != nil {
		return out, err
	}
	var facts supplementalReviewFacts
	if operator {
		facts, err = supplementalReviewProjectFactsTx(ctx, tx, s.db, projectID, "", in.TaskID)
	} else {
		facts, err = supplementalReviewFactsTx(ctx, tx, s.db, in.AISessionID, in.TaskID)
	}
	if err != nil {
		return out, err
	}
	if facts.task.FeatureAreaID != in.FeatureAreaID || facts.target != *in.ExpectedTarget {
		return out, reviewRequestRejected("REVIEW_REQUEST_TARGET_CHANGED")
	}
	t := facts.task
	now := s.now()
	id := "task_review_request_" + workflowHash(actorKind + "\x00" + actorID + "\x00" + in.RequestID)[:32]
	_, err = tx.ExecContext(ctx, `INSERT INTO task_supplemental_review_requests(id,ai_session_id,actor_kind,actor_id,project_id,task_id,feature_area_id,request_id,payload_sha256,purpose,source_phase,source_revision_id,source_review_id,source_execution_id,source_decision_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, supplementalSessionValue(in.AISessionID), actorKind, actorID, t.ProjectID, t.ID, t.FeatureAreaID, in.RequestID, hash, in.Purpose, t.Phase, t.CurrentDefinitionRevisionID, t.CurrentReviewID, t.CurrentExecutionID, facts.target.PendingDecisionID, formatTime(now))
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_supplemental_review_current(task_id,request_id) VALUES(?,?) ON CONFLICT(task_id) DO UPDATE SET request_id=excluded.request_id`, t.ID, id); err != nil {
		return out, err
	}
	if t.CurrentExecutionID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_review_superseded_executions(execution_id,request_id) VALUES(?,?)`, t.CurrentExecutionID, id); err != nil {
			return out, err
		}
	}
	if facts.target.PendingDecisionID != "" {
		request := decisionresolution.Request{DecisionID: facts.target.PendingDecisionID, Kind: decisionresolution.KindTaskAcceptance, SubjectKind: decisionresolution.SubjectExecution, SubjectID: t.CurrentExecutionID, Option: "REQUEST_REVIEW", RequestID: in.RequestID, ActorKind: actorKind, ActorID: actorID}
		if _, err = insertDecisionResolutionTx(ctx, tx, request, t.ProjectID, decisionresolution.ResultTask, t.ID, now); err != nil {
			return out, err
		}
	}
	if facts.closeWorkflow != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE direct_file_task_authorizations SET state='CLOSED',failure_code='',failure_reason='',updated_at=? WHERE workflow_id=? AND task_id=? AND task_definition_revision_id=? AND state='ACTIVE'`, formatTime(now), facts.closeWorkflow, t.ID, t.CurrentDefinitionRevisionID); err != nil {
			return out, err
		}
	}
	changed, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth=COALESCE(required_review_depth,(SELECT review_depth FROM reviews WHERE id=tasks.current_review_id),'FAST'),current_execution_id=NULL,confirmed_task_plan_id=NULL,updated_at=? WHERE id=? AND status='ACTIVE' AND phase=? AND current_definition_revision_id=? AND COALESCE(current_review_id,'')=? AND COALESCE(current_execution_id,'')=?`, formatTime(now), t.ID, t.Phase, t.CurrentDefinitionRevisionID, t.CurrentReviewID, t.CurrentExecutionID)
	if err != nil {
		return out, err
	}
	count, e := changed.RowsAffected()
	if e != nil {
		return out, e
	}
	if count != 1 {
		return out, executionproposal.ErrConflict
	}
	payload, e := event.Payload(map[string]any{"supplemental_request_id": id, "purpose": in.Purpose, "source_revision_id": t.CurrentDefinitionRevisionID, "source_review_id": t.CurrentReviewID, "source_execution_id": t.CurrentExecutionID, "superseded_decision_id": facts.target.PendingDecisionID})
	if e != nil {
		return out, e
	}
	if err = appendEvent(ctx, tx, event.Event{Type: "TASK_SUPPLEMENTAL_REVIEW_REQUESTED", ProjectID: t.ProjectID, TaskID: t.ID, EntityType: "task", EntityID: t.ID, Payload: payload, CreatedAt: now}); err != nil {
		return out, err
	}
	out, _, err = scanSupplementalRequest(tx.QueryRowContext(ctx, supplementalRequestSelect+` WHERE id=?`, id))
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
