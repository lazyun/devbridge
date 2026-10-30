package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// ReserveTaskExecution records the Task-owned start effect. Modern admitted
// Implementation returns without a Run; the caller prepares it after commit.
// Historical scalar and Verification paths still use their legacy reservation.
func (s *WorkflowProgressionStore) ReserveTaskExecution(ctx context.Context, sessionID, taskID string) (executionproposal.Proposal, run.Run, error) {
	return s.ReserveTaskExecutionExpected(ctx, sessionID, taskID, nil)
}

func (s *WorkflowProgressionStore) ReserveTaskExecutionExpected(ctx context.Context, sessionID, taskID string, expected *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error) {
	action := "START_EXECUTION"
	if expected != nil && expected.Phase == task.PhaseVerificationReady {
		action = "START_VERIFICATION"
	}
	requestID := "execution_start_" + workflowHash(sessionID + "\x00" + taskID + "\x00" + action + "\x00" + executionStartExpectationKey(expected))[:32]
	return s.ReserveTaskExecutionExpectedCommand(ctx, sessionID, taskID, action, requestID, expected)
}

// ReserveTaskExecutionExpectedCommand binds the runtime effect to the durable
// parent/substep request instead of deriving authority from candidate status.
func (s *WorkflowProgressionStore) ReserveTaskExecutionExpectedCommand(ctx context.Context, sessionID, taskID, action, requestID string, expected *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error) {
	if sessionID == "" || taskID == "" || requestID == "" || action != "START_EXECUTION" && action != "START_VERIFICATION" {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrInvalidArgument
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=(SELECT project_id FROM ai_sessions WHERE id=?)`, taskID, sessionID))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	phase := t.Phase
	if expected != nil {
		phase = expected.Phase
	}
	if phase == task.PhaseImplementationReady && action != "START_EXECUTION" || phase == task.PhaseVerificationReady && action != "START_VERIFICATION" {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrInvalidArgument
	}
	value, reserved, err := s.reserveTaskExecutionExpectedTx(ctx, tx, t, sessionID, expected, executionStartActor{Kind: "AI_SESSION", ID: sessionID, RequestID: requestID, Action: action})
	if err != nil {
		return value, reserved, err
	}
	if err = tx.Commit(); err != nil {
		return value, reserved, err
	}
	return value, reserved, nil
}

// ReserveTaskExecutionExpectedForOperator is the project-scoped counterpart
// for an already-admitted local operator. It shares the exact same writer
// transaction and reducer as the AI Session entry point; it does not derive or
// install an AI Session from Task creation provenance.
func (s *WorkflowProgressionStore) ReserveTaskExecutionExpectedForOperator(ctx context.Context, projectID, taskID, action, actorID, requestID string, expected *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error) {
	actor, ok := localoperator.FromContext(ctx)
	if !ok || actor.PrincipalID() != actorID || actor.Check(projectID, taskID, action) != nil || localoperator.RequireProject(ctx, projectID) != nil {
		return executionproposal.Proposal{}, run.Run{}, localoperator.ErrAccessDenied
	}
	if expected == nil || requestID == "" {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrInvalidArgument
	}
	payload, err := operatorExecutionStartPayload(action, *expected)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	defer tx.Rollback()
	// The explicit-selection command has a stronger original payload than
	// this legacy API can express. It must be replayed through its own reader;
	// substituting the newly current pointer here cannot consume that command.
	var selectedCommand bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM operator_execution_start_commands WHERE project_id=? AND actor_id=? AND request_id=?)`, projectID, actorID, requestID).Scan(&selectedCommand); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if selectedCommand {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrStartAuthorizationUnavailable
	}
	if value, reserved, found, readErr := readOperatorTaskExecutionStartReceipt(ctx, tx, projectID, actorID, requestID, taskID, action, *expected, payload); readErr != nil || found {
		return value, reserved, readErr
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=?`, taskID, projectID))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	ownerSessionID := ""
	revising, err := noRunRevisionApprovedTx(ctx, tx, t.ID)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if t.CurrentExecutionID == "" || revising {
		ownerSessionID, err = executionReservationOwnerSessionTx(ctx, tx, t)
		if err != nil {
			return executionproposal.Proposal{}, run.Run{}, err
		}
	}
	value, reserved, err := s.reserveTaskExecutionExpectedTx(ctx, tx, t, ownerSessionID, expected, executionStartActor{Kind: "LOCAL_OPERATOR", ID: actorID, RequestID: requestID, Action: action})
	if err != nil {
		return value, reserved, err
	}
	if value.Status == executionproposal.StatusAdmitted {
		if err = tx.Commit(); err != nil {
			return value, reserved, err
		}
		value, reserved, err = continueAdmittedExecution(ctx, s.db, value.ID, s.now())
		if err != nil {
			return value, reserved, err
		}
		// The exact start effect is already committed. This projection receipt
		// is written only after canonical Run creation, so response loss can
		// recover through that effect and the preparation receipt.
		tx, err = s.db.beginWrite(ctx)
		if err != nil {
			return value, reserved, err
		}
		defer tx.Rollback()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operator_task_execution_start_receipts(project_id,actor_kind,actor_id,request_id,action,task_id,feature_area_id,task_definition_revision_id,expected_execution_id,expected_phase,payload_sha256,execution_id,run_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, projectID, "LOCAL_OPERATOR", actorID, requestID, action, taskID, expected.FeatureAreaID, expected.TaskDefinitionRevisionID, expected.ExecutionID, expected.Phase, payload, value.ID, reserved.ID, formatTime(s.now()))
	if err != nil {
		return value, reserved, conflict(err)
	}
	return value, reserved, tx.Commit()
}

func operatorExecutionStartPayload(action string, expected wp.TaskExecutionExpectation) (string, error) {
	data, err := json.Marshal(struct {
		Action   string                      `json:"action"`
		Expected wp.TaskExecutionExpectation `json:"expected"`
	}{Action: action, Expected: expected})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func readOperatorTaskExecutionStartReceipt(ctx context.Context, q definitionQuerier, projectID, actorID, requestID, taskID, action string, expected wp.TaskExecutionExpectation, payload string) (executionproposal.Proposal, run.Run, bool, error) {
	var savedAction, savedTaskID, featureAreaID, revisionID, executionID, phase, savedPayload, resultExecutionID, runID string
	err := q.QueryRowContext(ctx, `SELECT action,task_id,feature_area_id,task_definition_revision_id,expected_execution_id,expected_phase,payload_sha256,execution_id,run_id FROM operator_task_execution_start_receipts WHERE project_id=? AND actor_kind='LOCAL_OPERATOR' AND actor_id=? AND request_id=?`, projectID, actorID, requestID).Scan(&savedAction, &savedTaskID, &featureAreaID, &revisionID, &executionID, &phase, &savedPayload, &resultExecutionID, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		return executionproposal.Proposal{}, run.Run{}, false, nil
	}
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	if savedAction != action || savedTaskID != taskID || featureAreaID != expected.FeatureAreaID || revisionID != expected.TaskDefinitionRevisionID || executionID != expected.ExecutionID || phase != string(expected.Phase) || savedPayload != payload {
		return executionproposal.Proposal{}, run.Run{}, true, executionproposal.ErrConflict
	}
	value, err := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND project_id=? AND task_id=?`, resultExecutionID, projectID, taskID))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, true, err
	}
	reserved, err := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.project_id=? AND r.task_id=? AND r.execution_proposal_id=?`, runID, projectID, taskID, resultExecutionID))
	return value, reserved, true, err
}

func (s *WorkflowProgressionStore) TaskExecutionStartReplayForOperator(ctx context.Context, projectID, taskID, action, actorID, requestID string, expected wp.TaskExecutionExpectation) (bool, error) {
	actor, ok := localoperator.FromContext(ctx)
	if !ok || actor.PrincipalID() != actorID || actor.Check(projectID, taskID, action) != nil || localoperator.RequireProject(ctx, projectID) != nil {
		return false, localoperator.ErrAccessDenied
	}
	payload, err := operatorExecutionStartPayload(action, expected)
	if err != nil {
		return false, err
	}
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	_, _, found, err := readOperatorTaskExecutionStartReceipt(ctx, tx, projectID, actorID, requestID, taskID, action, expected, payload)
	return found, err
}

// Initial candidate creation keeps the authority captured by the approval
// materialization. A Task creator is not necessarily the approval's owner.
func executionReservationOwnerSessionTx(ctx context.Context, q definitionQuerier, t task.Task) (string, error) {
	var owner sql.NullString
	err := q.QueryRowContext(ctx, `SELECT ai_session_id FROM review_approval_materializations WHERE decision_id=? AND task_id=? AND initial_revision_id=?`, t.ReviewApprovalID, t.ID, t.CurrentDefinitionRevisionID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", executionproposal.ErrConflict
	}
	if err != nil {
		return "", err
	}
	if !owner.Valid || owner.String == "" {
		return "", executionproposal.ErrConflict
	}
	return owner.String, nil
}

type executionStartActor struct {
	Kind, ID, RequestID, Action string
	SelectionHash               string // present only in the exact explicit operator command transaction
}

func executionStartExpectationKey(expected *wp.TaskExecutionExpectation) string {
	if expected == nil {
		return "CURRENT"
	}
	return expected.FeatureAreaID + "\x00" + expected.TaskDefinitionRevisionID + "\x00" + expected.ExecutionID + "\x00" + string(expected.Phase) + "\x00" + expected.PrimaryMemberID + "\x00" + expected.ApprovedWorksetHash
}

// matchesExecutionLayout keeps historical empty expectations compatible while
// requiring every v77 multi-member Coding start to echo the exact proposal
// layout that was displayed during confirmation.
func matchesExecutionLayout(expected wp.TaskExecutionExpectation, value executionproposal.Proposal) bool {
	if value.ApprovedWorksetHash == "" {
		return true
	}
	return expected.PrimaryMemberID == value.PrimaryMemberID && expected.ApprovedWorksetHash == value.ApprovedWorksetHash
}

func (s *WorkflowProgressionStore) reserveTaskExecutionExpectedTx(ctx context.Context, tx *sql.Tx, t task.Task, ownerSessionID string, expected *wp.TaskExecutionExpectation, actor executionStartActor) (executionproposal.Proposal, run.Run, error) {
	if t.Status != task.StatusActive {
		return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrConflict
	}
	if expected != nil {
		if expected.FeatureAreaID != t.FeatureAreaID || expected.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
			return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrConflict
		}
		changed := expected.ExecutionID != t.CurrentExecutionID || expected.Phase != t.Phase
		// Validate a known request even when it substitutes the newly current
		// candidate before preparation advances phase. A retained predecessor
		// awaiting its first start still enters candidate creation below.
		if changed || t.CurrentExecutionID == approvedExecutionID(t) {
			value, reserved, found, readErr := existingTaskExecutionReservationCommand(ctx, tx, t, *expected, actor)
			if readErr != nil {
				return value, reserved, readErr
			}
			if found && reserved.ID != "" {
				return value, reserved, nil
			}
			if changed {
				if !found {
					return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrConflict
				}
				return value, reserved, nil
			}
		}
	}
	// A fresh v2 Coding start must already have exact revision authority.
	// Adoption creates its qualified candidate through its private producer;
	// a raw/operator selection cannot manufacture that source here.
	if err := requireTaskExecutionRuntimeBindingTx(ctx, tx, t); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if err := taskActionAdmissionTx(ctx, tx, t.ID); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	now := s.now()
	revising, err := noRunRevisionApprovedTx(ctx, tx, t.ID)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if t.CurrentExecutionID == "" || revising {
		if t.Phase != task.PhaseImplementationReady || t.CurrentDefinitionRevisionID == "" {
			return executionproposal.Proposal{}, run.Run{}, executionproposal.ErrConflict
		}
		if err := createApprovedExecutionForStartTx(ctx, tx, t, ownerSessionID, now, actor, expected); err != nil {
			return executionproposal.Proposal{}, run.Run{}, err
		}
		t.CurrentExecutionID = approvedExecutionID(t)
	}
	value, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND task_id=? AND task_definition_revision_id=?`, t.CurrentExecutionID, t.ID, t.CurrentDefinitionRevisionID))
	if err != nil {
		return value, run.Run{}, err
	}
	if value.ApprovedWorksetHash != "" && (expected == nil || !matchesExecutionLayout(*expected, value)) {
		return value, run.Run{}, executionproposal.ErrConflict
	}
	startPhase := t.Phase
	if expected != nil {
		startPhase = expected.Phase
	}
	if startPhase == task.PhaseImplementationReady || startPhase == task.PhaseVerificationReady {
		effect := executionStartEffectRecord{ProjectID: t.ProjectID, ActorKind: actor.Kind, ActorID: actor.ID, RequestID: actor.RequestID, Action: actor.Action, Effect: wp.StartEffectStartAuthorized, TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, RevisionID: value.TaskDefinitionRevisionID, ExecutionID: value.ID, Phase: startPhase}
		// A composed approval already sealed this exact start request with
		// its Review decision. Preserve that source identity when consuming it.
		var decision sql.NullString
		readErr := tx.QueryRowContext(ctx, `SELECT decision_id FROM execution_start_effects WHERE project_id=? AND actor_kind=? AND actor_id=? AND request_id=?`, effect.ProjectID, effect.ActorKind, effect.ActorID, effect.RequestID).Scan(&decision)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return value, run.Run{}, readErr
		}
		if decision.Valid && decision.String != "" {
			if decision.String != t.ReviewApprovalID {
				return value, run.Run{}, executionproposal.ErrConflict
			}
			effect.DecisionID = decision.String
		}
		if err = insertExecutionStartEffectTx(ctx, tx, effect, formatTime(now)); err != nil {
			return value, run.Run{}, err
		}
		if startPhase == task.PhaseImplementationReady && value.Status == executionproposal.StatusAdmitted {
			if formal, reuseErr := reuseFormalExecutionAdmissionTx(ctx, tx, value); reuseErr != nil {
				return value, run.Run{}, reuseErr
			} else if formal {
				return value, run.Run{}, nil
			}
		}
		if startPhase == task.PhaseImplementationReady && len(t.RepositoryTargets) > 0 && value.ApprovedWorksetHash != "" {
			payload, payloadErr := executionStartEffectPayload(effect)
			if payloadErr != nil {
				return value, run.Run{}, payloadErr
			}
			admission, admissionErr := admitExecutionTx(ctx, tx, value.ID, actor.RequestID, payload, now)
			if admissionErr != nil {
				return value, run.Run{}, admissionErr
			}
			return admission.Proposal, run.Run{}, nil
		}
	}
	var reserved run.Run
	switch t.Phase {
	case task.PhaseImplementationReady:
		// Scalar-layout rows predate the frozen multi-repository Workset
		// contract. They now use the same admission path as multi-repo tasks.
		if len(t.RepositoryTargets) != 0 || value.ApprovedWorksetHash != "" {
			return value, run.Run{}, executionproposal.ErrConflict
		}
		var payload string
		payloadErr := tx.QueryRowContext(ctx, `SELECT payload_sha256 FROM execution_start_effects WHERE project_id=? AND actor_kind=? AND actor_id=? AND request_id=? AND effect='START_AUTHORIZED' AND execution_id=? AND task_definition_revision_id=?`, t.ProjectID, actor.Kind, actor.ID, actor.RequestID, value.ID, value.TaskDefinitionRevisionID).Scan(&payload)
		if payloadErr != nil {
			return value, run.Run{}, payloadErr
		}
		admission, admissionErr := admitExecutionTx(ctx, tx, value.ID, actor.RequestID, payload, now)
		if admissionErr != nil {
			return value, run.Run{}, admissionErr
		}
		return admission.Proposal, run.Run{}, nil
	case task.PhaseVerificationReady:
		if value.ImplementationRunID == "" {
			value, reserved, err = consumeAndCreateVerificationRunTx(ctx, s.db, tx, value.ID, "run_verify_"+value.ID, now)
		} else {
			value, reserved, err = reserveVerificationRunTx(ctx, s.db, tx, value.ID, "run_verify_"+value.ID, now)
		}
	case task.PhaseImplementing, task.PhaseVerifying, task.PhaseAcceptancePending:
		runID := value.ImplementationRunID
		if value.VerificationRunID != "" {
			runID = value.VerificationRunID
		}
		reserved, err = scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.task_id=? AND r.execution_proposal_id=?`, runID, t.ID, value.ID))
	default:
		err = executionproposal.ErrConflict
	}
	if err != nil {
		return value, reserved, err
	}
	return value, reserved, nil
}

// continueAdmittedExecution crosses the committed business/Core transaction
// boundary. A crash here leaves ADMITTED and its immutable receipt readable;
// replay uses the exact deterministic Run identity and request payload.
func continueAdmittedExecution(ctx context.Context, db *DB, executionID string, now time.Time) (executionproposal.Proposal, run.Run, error) {
	// Original already-reserved Run readback remains historical. Only a fresh
	// Run needs current qualification/binding; CreatePreparationRun repeats the
	// guard under the writer lock before creating any new attempt.
	var priorRun string
	if err := db.sql.QueryRowContext(ctx, `SELECT COALESCE(run_id,'') FROM execution_proposals WHERE id=?`, executionID).Scan(&priorRun); err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	if priorRun == "" {
		if _, err := formalExecutionStartReadyTx(ctx, db.sql, executionID); err != nil {
			return executionproposal.Proposal{}, run.Run{}, err
		}
	}

	var planID, requestID, payload string
	if err := db.sql.QueryRowContext(ctx, `SELECT runtime_scope_plan_id,admission_request_id,payload_sha256 FROM execution_proposal_admissions WHERE proposal_id=?`, executionID).Scan(&planID, &requestID, &payload); err != nil {
		return executionproposal.Proposal{}, run.Run{}, executionProposalConflict(err)
	}
	reserved, err := NewRunStore(db).CreatePreparationRun(ctx, planID, "run_impl_"+executionID, requestID, payload, now)
	if err != nil {
		value, readErr := NewExecutionProposalStore(db).Get(ctx, executionID)
		if readErr != nil {
			return executionproposal.Proposal{}, run.Run{}, errors.Join(err, readErr)
		}
		return value, run.Run{}, err
	}
	value, err := NewExecutionProposalStore(db).Get(ctx, executionID)
	return value, reserved, err
}

// ReadTaskExecutionReservation is a receipt-free, read-only observation of the
// exact existing candidate/Run. It never calls reservation or backend dispatch.
func (s *WorkflowProgressionStore) ReadTaskExecutionReservation(ctx context.Context, sessionID, taskID string, expected wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, bool, error) {
	tx, err := s.db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	defer tx.Rollback()
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=(SELECT project_id FROM ai_sessions WHERE id=?)`, taskID, sessionID))
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, false, err
	}
	return existingTaskExecutionReservation(ctx, tx, t, expected)
}

func existingTaskExecutionReservation(ctx context.Context, q definitionQuerier, t task.Task, expected wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, bool, error) {
	if expected.FeatureAreaID != t.FeatureAreaID || expected.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
		return executionproposal.Proposal{}, run.Run{}, false, nil
	}
	// Reuse the same initial identity as createApprovedExecutionTx; no client
	// guesses a candidate and no latest/history selection replaces the target.
	candidateID := expected.ExecutionID
	if candidateID == "" && expected.Phase == task.PhaseImplementationReady {
		// Only the initial candidate owned by this exact approval can satisfy
		// an observation of no Execution. A same-revision retry successor must
		// never be selected by a delayed initial start request.
		candidateID = approvedExecutionID(t)
		legacyID := "execution_approved_" + t.ID
		if t.CurrentExecutionID == legacyID {
			candidateID = legacyID
		}
	}
	if candidateID == "" || candidateID != t.CurrentExecutionID {
		return executionproposal.Proposal{}, run.Run{}, false, nil
	}
	value, err := scanExecutionProposal(q.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=? AND task_id=? AND task_definition_revision_id=?`, candidateID, t.ID, expected.TaskDefinitionRevisionID))
	if err != nil {
		return value, run.Run{}, false, err
	}
	if !matchesExecutionLayout(expected, value) {
		return value, run.Run{}, false, executionproposal.ErrConflict
	}
	runID := value.ImplementationRunID
	if expected.Phase == task.PhaseVerificationReady {
		runID = value.VerificationRunID
	} else if expected.Phase != task.PhaseImplementationReady {
		return executionproposal.Proposal{}, run.Run{}, false, executionproposal.ErrConflict
	}
	if runID == "" {
		if expected.Phase == task.PhaseImplementationReady && value.Status == executionproposal.StatusAdmitted {
			var admitted int
			err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposal_admissions WHERE proposal_id=? AND task_id=? AND task_definition_revision_id=?)`, value.ID, t.ID, expected.TaskDefinitionRevisionID).Scan(&admitted)
			if err != nil {
				return value, run.Run{}, false, err
			}
			if admitted == 1 {
				return value, run.Run{}, true, nil
			}
		}
		return executionproposal.Proposal{}, run.Run{}, false, nil
	}
	reserved, err := scanRun(q.QueryRowContext(ctx, selectRun+` WHERE r.id=? AND r.task_id=? AND r.execution_proposal_id=?`, runID, t.ID, value.ID))
	return value, reserved, err == nil, err
}
