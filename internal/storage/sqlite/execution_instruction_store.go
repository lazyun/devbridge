package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Check authenticated Session ownership before revealing or replaying a receipt.
func instructionCaller(ctx context.Context, q definitionQuerier, sessionID string) error {
	var caller sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT caller_scope FROM ai_sessions WHERE id=?`, sessionID).Scan(&caller); err != nil {
		return err
	}
	if caller.Valid {
		scope, ok := aisession.CallerScopeFromContext(ctx)
		if !ok || scope != caller.String {
			return aisession.ErrScopeMismatch
		}
	}
	return nil
}

// Payload hashes are identities, not substitute prompt fields. Exact submitted
// strings live only in the immutable definition's canonical instruction fields.
func instructionPayloadHash(in *task.ExecutionInstructions) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return workflowHash(string(b)), nil
}

func checkApprovalInstructionReplay(ctx context.Context, q definitionQuerier, decisionID string, in *task.ExecutionInstructions) error {
	hash, err := instructionPayloadHash(in)
	if err != nil {
		return err
	}
	var saved string
	err = q.QueryRowContext(ctx, `SELECT instructions_sha256 FROM review_approval_instruction_payloads WHERE decision_id=?`, decisionID).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) {
		// An older receipt predates this input and can only replay its absent input.
		if in == nil {
			return nil
		}
		return executionproposal.ErrConflict
	}
	if err != nil {
		return err
	}
	if saved != hash {
		return executionproposal.ErrConflict
	}
	return nil
}
func recordApprovalInstructionsTx(ctx context.Context, tx *sql.Tx, t task.Task, in wp.ResolveInput, now time.Time) error {
	hash, err := instructionPayloadHash(in.ExecutionInstructions)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_approval_instruction_payloads(decision_id,instructions_sha256) VALUES(?,?)`, in.DecisionID, hash); err != nil {
		return err
	}
	var sourceRevision sql.NullString
	e := tx.QueryRowContext(ctx, `SELECT req.source_revision_id FROM task_supplemental_review_current cur JOIN task_supplemental_review_requests req ON req.id=cur.request_id JOIN task_supplemental_review_starts started ON started.request_id=req.id WHERE cur.task_id=? AND req.task_id=cur.task_id AND started.review_id=?`, t.ID, in.ReviewID).Scan(&sourceRevision)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	actorKind, actorID := "AI_SESSION", in.AISessionID
	if in.OperatorPrincipalID != "" {
		actorKind, actorID = "LOCAL_OPERATOR", in.OperatorPrincipalID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO execution_instruction_sources(revision_id,source_revision_id,decision_id,task_id,ai_session_id,actor_kind,actor_id,request_id,source_kind,instructions_sha256,created_at) VALUES(?,?,?,?,?,?,?,?, 'REVIEW_APPROVAL',?,?)`, t.CurrentDefinitionRevisionID, sourceRevision, in.DecisionID, t.ID, nullString(in.AISessionID), actorKind, actorID, in.RequestID, hash, formatTime(now))
	return err
}

// A start materialization carries the original approval, never a new authority.
// The source revision remains immutable and the source Review must stay current.
func instructionRevisionAuthorizedTx(ctx context.Context, q definitionQuerier, t task.Task) (bool, error) {
	var exact bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_instruction_sources s JOIN review_approval_materializations m ON m.initial_revision_id=s.source_revision_id AND m.decision_id=s.decision_id AND m.task_id=s.task_id JOIN review_approval_decisions d ON d.decision_id=m.decision_id JOIN decision_resolution_receipts r ON r.decision_id=m.decision_id WHERE s.revision_id=? AND s.task_id=? AND s.decision_id=? AND s.source_kind='EXECUTION_START' AND d.review_id=? AND d.task_id=s.task_id AND d.project_id=? AND r.option='APPROVE' AND r.subject_id=s.task_id AND r.project_id=d.project_id)`, t.CurrentDefinitionRevisionID, t.ID, t.ReviewApprovalID, t.CurrentReviewID, t.ProjectID).Scan(&exact)
	return exact, err
}
func taskInstructionStartHash(taskID string, expected wp.TaskExecutionExpectation, in *task.ExecutionInstructions) (string, error) {
	b, err := json.Marshal(struct {
		TaskID       string                      `json:"taskId"`
		Target       wp.TaskExecutionExpectation `json:"target"`
		Instructions *task.ExecutionInstructions `json:"executionInstructions"`
	}{taskID, expected, in})
	if err != nil {
		return "", err
	}
	return workflowHash(string(b)), nil
}

// ReadTaskExecutionInstructionsReservation restores only the original durable
// reservation. It performs no materialization, authorization or dispatch.
func (s *WorkflowProgressionStore) ReadTaskExecutionInstructionsReservation(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation, requestID string, in *task.ExecutionInstructions) (executionproposal.Proposal, run.Run, bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	defer tx.Rollback()
	hash, err := taskInstructionStartHash(taskID, expected, in)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	return readTaskInstructionReceipt(ctx, tx, sessionID, taskID, requestID, hash)
}
func readTaskInstructionReceipt(ctx context.Context, q definitionQuerier, sessionID, taskID, requestID, hash string) (executionproposal.Proposal, run.Run, bool, error) {
	var saved, owner, revision, executionID string
	var runID sql.NullString
	err := q.QueryRowContext(ctx, `SELECT r.payload_sha256,r.task_id,r.revision_id,r.execution_id,r.run_id FROM task_execution_instruction_receipts r WHERE r.ai_session_id=? AND r.request_id=? AND EXISTS(SELECT 1 FROM tasks t JOIN ai_sessions s ON s.project_id=t.project_id WHERE t.id=r.task_id AND s.id=r.ai_session_id)`, sessionID, requestID).Scan(&saved, &owner, &revision, &executionID, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, run.Run{}, false, nil
	}
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	if err := instructionCaller(ctx, q, sessionID); err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	if saved != hash || owner != taskID {
		return executionproposal.Proposal{}, run.Run{}, false, executionproposal.ErrConflict
	}
	value, err := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND task_id=? AND task_definition_revision_id=?`, executionID, taskID, revision))
	if err != nil {
		return value, run.Run{}, false, err
	}
	if !runID.Valid {
		if value.Status != executionproposal.StatusAdmitted && value.Status != executionproposal.StatusConsumed {
			return value, run.Run{}, true, executionproposal.ErrConflict
		}
		if value.RunID == "" {
			return value, run.Run{}, true, nil
		}
		runID.String = value.RunID
	}
	reserved, err := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.task_id=? AND r.execution_proposal_id=?`, runID.String, taskID, executionID))
	return value, reserved, err == nil, err
}

// ReserveTaskExecutionInstructions is the narrow instruction-only successor
// path for an approved initial candidate that has never been dispatched. The
// original definition, Review result and approval hashes remain unchanged.
// Definition + source + Task pointer + candidate + admission receipt commit
// together. Core creates the preparation Run only after that business commit.
func (s *WorkflowProgressionStore) ReserveTaskExecutionInstructions(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation, requestID string, in task.ExecutionInstructions) (executionproposal.Proposal, run.Run, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 128 || expected.TaskDefinitionRevisionID == "" || expected.FeatureAreaID == "" {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrInvalidArgument
	}
	hash, err := taskInstructionStartHash(taskID, expected, &in)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	if err := instructionCaller(ctx, tx, sessionID); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	value, reserved, found, err := readTaskInstructionReceipt(ctx, tx, sessionID, taskID, requestID, hash)
	if err != nil || found {
		if err != nil || value.Status != executionproposal.StatusAdmitted {
			return value, reserved, err
		}
		if err = tx.Rollback(); err != nil {
			return value, reserved, err
		}
		return continueAdmittedExecution(ctx, s.db, value.ID, s.now())
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=(SELECT project_id FROM ai_sessions WHERE id=?)`, taskID, sessionID))
	if err != nil {
		return value, reserved, err
	}
	// Do not mutate an existing candidate, a dispatched/failed Execution or a
	// Verification continuation whose Implementation has already run.
	if t.Status != task.StatusActive || t.Phase != task.PhaseImplementationReady || t.CurrentExecutionID != "" || expected.ExecutionID != "" || expected.Phase != t.Phase || expected.FeatureAreaID != t.FeatureAreaID || expected.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || t.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return value, reserved, executionproposal.ErrConflict
	}
	if err = validateApprovedTaskTx(ctx, tx, t, false); err != nil {
		return value, reserved, err
	}
	source, err := getDefinitionRevision(ctx, tx, t.CurrentDefinitionRevisionID)
	if err != nil {
		return value, reserved, err
	}
	var initial bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_approval_materializations WHERE decision_id=? AND task_id=? AND initial_revision_id=?)`, t.ReviewApprovalID, t.ID, source.ID).Scan(&initial); err != nil {
		return value, reserved, err
	}
	if !initial || source.TaskID != t.ID {
		return value, reserved, executionproposal.ErrConflict
	}
	work, verification, err := task.ApplyExecutionInstructions(source.Definition.WorkSpec, source.Definition.VerificationSpec, &in)
	if err != nil {
		return value, reserved, err
	}
	definition := source.Definition
	definition.WorkSpec = work
	definition.VerificationSpec = verification
	payload, definitionHash, err := task.EncodeDefinition(definition)
	if err != nil {
		return value, reserved, err
	}
	// Exact no-op submissions reserve the existing revision, recording their
	// original payload in the start receipt without a meaningless revision.
	now := s.now()
	revisionID := source.ID
	if definitionHash != source.PayloadSHA256 {
		var ordinal int
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM task_definition_revisions WHERE task_id=?`, t.ID).Scan(&ordinal); err != nil {
			return value, reserved, err
		}
		revisionID = fmt.Sprintf("task_definition_%s_%d_%s", t.ID, ordinal, definitionHash[:16])
		if _, err = tx.ExecContext(ctx, `INSERT INTO task_definition_revisions(id,task_id,ordinal,payload_json,payload_sha256,created_at) VALUES(?,?,?,?,?,?)`, revisionID, t.ID, ordinal, payload, definitionHash, formatTime(now)); err != nil {
			return value, reserved, err
		}
		workJSON, err := encodeWorkSpec(work)
		if err != nil {
			return value, reserved, err
		}
		verificationJSON, err := encodeVerificationSpec(verification)
		if err != nil {
			return value, reserved, err
		}
		instructionHash, err := instructionPayloadHash(&in)
		if err != nil {
			return value, reserved, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO execution_instruction_sources(revision_id,source_revision_id,decision_id,task_id,ai_session_id,actor_kind,actor_id,request_id,source_kind,instructions_sha256,created_at) VALUES(?,?,?,?,?,?,?,?, 'EXECUTION_START',?,?)`, revisionID, source.ID, t.ReviewApprovalID, t.ID, sessionID, "AI_SESSION", sessionID, requestID, instructionHash, formatTime(now)); err != nil {
			return value, reserved, err
		}
		changed, err := tx.ExecContext(ctx, `UPDATE tasks SET current_definition_revision_id=?,work_spec_json=?,verification_spec_json=?,updated_at=? WHERE id=? AND current_definition_revision_id=? AND current_execution_id IS NULL AND phase='IMPLEMENTATION_READY' AND status='ACTIVE'`, revisionID, workJSON, verificationJSON, formatTime(now), t.ID, source.ID)
		if err != nil {
			return value, reserved, err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return value, reserved, err
		}
		if n != 1 {
			return value, reserved, executionproposal.ErrConflict
		}

	}
	t.CurrentDefinitionRevisionID = revisionID
	t.WorkSpec = work
	t.VerificationSpec = verification
	if err = createApprovedExecutionTx(ctx, tx, t, sessionID, now); err != nil {
		return value, reserved, err
	}
	if err = insertExecutionStartEffectTx(ctx, tx, executionStartEffectRecord{ProjectID: t.ProjectID, ActorKind: "AI_SESSION", ActorID: sessionID, RequestID: requestID, Action: "START_EXECUTION", Effect: wp.StartEffectStartAuthorized, TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, RevisionID: revisionID, ExecutionID: approvedExecutionID(t), Phase: task.PhaseImplementationReady}, formatTime(now)); err != nil {
		return value, reserved, err
	}
	var effectPayload string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM execution_start_effects WHERE execution_id=? AND request_id=? AND effect='START_AUTHORIZED'`, approvedExecutionID(t), requestID).Scan(&effectPayload)
	if err != nil {
		return value, reserved, err
	}
	admission, err := admitExecutionTx(ctx, tx, approvedExecutionID(t), requestID, effectPayload, now)
	if err != nil {
		return value, reserved, err
	}
	value = admission.Proposal
	if _, err = tx.ExecContext(ctx, `INSERT INTO task_execution_instruction_receipts(ai_session_id,request_id,task_id,payload_sha256,source_revision_id,revision_id,execution_id,run_id,created_at) VALUES(?,?,?,?,?,?,?,NULL,?)`, sessionID, requestID, t.ID, hash, source.ID, revisionID, value.ID, formatTime(now)); err != nil {
		return value, reserved, err
	}
	if err = tx.Commit(); err != nil {
		return value, reserved, err
	}
	return continueAdmittedExecution(ctx, s.db, value.ID, s.now())
}
