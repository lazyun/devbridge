package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

// CreatePreparationRun is the single intended Core Run-preparation entry for
// Task Review and Execution owners. It commits the durable Run and attempt
// before any managed-workspace operation. Legacy admission writers are not
// permitted to call createRunAttempt after their migration to this protocol.
func (s *RunStore) CreatePreparationRun(ctx context.Context, planID, runID, requestID, payloadHash string, now time.Time) (run.Run, error) {
	if planID == "" || runID == "" || requestID == "" || len(payloadHash) != 64 {
		return run.Run{}, ErrConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return run.Run{}, err
	}
	defer tx.Rollback()
	var ownerKind, ownerID, projectID, taskID, planRequest, planPayload, authorityKind, authorityID, authorityDisposition, developmentContext string
	var generation, authorityGeneration int
	var topologyRevision int64
	err = tx.QueryRowContext(ctx, `SELECT p.owner_kind,p.owner_id,p.project_id,COALESCE(p.task_id,''),p.generation,p.preparation_request_id,p.preparation_payload_hash,p.topology_revision,p.development_context_hash,a.owner_kind,a.owner_id,a.generation,a.disposition FROM runtime_scope_plans p JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id WHERE p.plan_id=?`, planID).Scan(&ownerKind, &ownerID, &projectID, &taskID, &generation, &planRequest, &planPayload, &topologyRevision, &developmentContext, &authorityKind, &authorityID, &authorityGeneration, &authorityDisposition)
	if err != nil {
		return run.Run{}, err
	}
	if planRequest != requestID || planPayload != payloadHash || authorityKind != ownerKind || authorityID != ownerID || authorityGeneration != generation || authorityDisposition != "CURRENT" {
		return run.Run{}, ErrConflict
	}
	currentTopology, currentContext, err := purposeProjectContextTx(ctx, tx, projectID)
	if err != nil {
		return run.Run{}, err
	}
	if currentTopology != topologyRevision || currentContext != developmentContext {
		return run.Run{}, ErrConflict
	}
	var savedRun, savedPayload string
	err = tx.QueryRowContext(ctx, `SELECT run_id,payload_hash FROM runtime_preparation_attempts WHERE plan_id=? AND request_id=?`, planID, requestID).Scan(&savedRun, &savedPayload)
	if err == nil {
		if savedRun != runID || savedPayload != payloadHash {
			return run.Run{}, ErrConflict
		}
		existing, getErr := scanRun(tx.QueryRowContext(ctx, selectRun+` WHERE r.id=?`, savedRun))
		if getErr != nil {
			return run.Run{}, getErr
		}
		if err = tx.Commit(); err != nil {
			return run.Run{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run.Run{}, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM runtime_preparation_attempts WHERE plan_id=?`, planID).Scan(&existing); err != nil {
		return run.Run{}, err
	}
	if existing != 0 {
		return run.Run{}, ErrConflict
	}
	metadata := runMetadata{}
	switch runtimepreparation.OwnerKind(ownerKind) {
	case runtimepreparation.OwnerExecution, runtimepreparation.OwnerImplementation:
		if formal, readErr := formalExecutionStartReadyTx(ctx, tx, ownerID); readErr != nil {
			return run.Run{}, readErr
		} else if formal {
			var authorized bool
			if readErr = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_start_effects s JOIN tasks t ON t.id=s.task_id WHERE s.execution_id=? AND s.effect='START_AUTHORIZED' AND s.expected_phase='IMPLEMENTATION_READY' AND t.current_execution_id=s.execution_id AND t.current_definition_revision_id=s.task_definition_revision_id AND t.phase='IMPLEMENTATION_READY' AND t.status='ACTIVE')`, ownerID).Scan(&authorized); readErr != nil {
				return run.Run{}, readErr
			}
			if !authorized {
				// A no-Run successor has its own preparation effect, rather
				// than a second START_APPROVED confirmation for the revision.
				if readErr = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id JOIN feature_areas f ON f.id=t.feature_area_id WHERE e.id=? AND e.status='ADMITTED' AND e.outcome='PENDING' AND e.run_id IS NULL AND t.current_execution_id=e.id AND t.current_definition_revision_id=e.task_definition_revision_id AND t.project_id=e.project_id AND t.phase='IMPLEMENTATION_PREPARING' AND t.status='ACTIVE' AND t.legacy_read_only=0 AND f.status='ACTIVE' AND `+noRunSuccessorContinuationSQL+`)`, ownerID).Scan(&authorized); readErr != nil {
					return run.Run{}, readErr
				}
			}
			if !authorized {
				return run.Run{}, ErrConflict
			}
		}
		var status, currentExecution, revision, expectedRevision string
		var canceled bool
		err = tx.QueryRowContext(ctx, `SELECT e.status,COALESCE(t.current_execution_id,''),e.task_definition_revision_id,COALESCE(t.current_definition_revision_id,''),EXISTS(SELECT 1 FROM execution_cancellations c WHERE c.owner_id IN(e.id,t.id)) FROM execution_proposals e JOIN tasks t ON t.id=e.task_id WHERE e.id=? AND e.task_id=? AND e.project_id=?`, ownerID, taskID, projectID).Scan(&status, &currentExecution, &revision, &expectedRevision, &canceled)
		if err != nil || status != "ADMITTED" || currentExecution != ownerID || revision != expectedRevision || canceled {
			return run.Run{}, ErrConflict
		}
		var admissionPlan string
		if err = tx.QueryRowContext(ctx, `SELECT runtime_scope_plan_id FROM execution_proposal_admissions WHERE proposal_id=? AND admission_request_id=? AND payload_sha256=?`, ownerID, requestID, payloadHash).Scan(&admissionPlan); err != nil || admissionPlan != planID {
			return run.Run{}, ErrConflict
		}
		metadata = runMetadata{Purpose: run.PurposeImplementation, ExecutionProposalID: ownerID}
	case runtimepreparation.OwnerVerification:
		if _, readErr := formalExecutionStartReadyTx(ctx, tx, ownerID); readErr != nil {
			return run.Run{}, readErr
		}
		metadata = runMetadata{Purpose: run.PurposeVerification, ExecutionProposalID: ownerID}
	case runtimepreparation.OwnerReview:
		var sourceCommand string
		if readErr := tx.QueryRowContext(ctx, `SELECT source_command FROM runtime_scope_plans WHERE plan_id=?`, planID).Scan(&sourceCommand); readErr != nil {
			return run.Run{}, readErr
		}
		if sourceCommand == "APPLY_CONFIRMED_REVIEW_PURPOSE" {
			metadata, err = recoveryReviewPreparationMetadataTx(ctx, tx, planID, ownerID)
			if err != nil {
				return run.Run{}, err
			}
			taskID = ""
			break
		}
		current, readErr := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, ownerID))
		if readErr != nil {
			return run.Run{}, readErr
		}
		scope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
		initial := scope
		if len(current.Focus.Paths) > 0 {
			initial = run.ReviewScope{Kind: run.ReviewScopePaths, Paths: append([]string(nil), current.Focus.Paths...)}
		}
		metadata = runMetadata{Purpose: run.PurposeScopeReview, ReviewID: ownerID, CodexInstruction: current.Instruction, ReviewScope: scope,
			ReviewPlan: run.ReviewPlan{Mode: run.ReviewMode(current.Focus.Kind), InitialScope: initial, SelectedModules: append([]run.ReviewModuleSelection(nil), current.Focus.Modules...)}}
		taskID = "" // Review ownership is resolved by createRunAttempt from the Review row.
	case runtimepreparation.OwnerRecoveryStatus:
		// Recovery owns a distinct diagnostic child Run. Its source Run is only
		// provenance; no source workspace, worktree, Thread or Workset is copied.
	default:
		return run.Run{}, ErrConflict
	}
	var created run.Run
	if runtimepreparation.OwnerKind(ownerKind) == runtimepreparation.OwnerRecoveryStatus {
		created, err = createRecoveryChildPreparationRunTx(ctx, s.db, tx, ownerID, planID, runID, requestID, payloadHash, now)
	} else {
		created, err = createRunAttempt(ctx, s.db, tx, taskID, runID, now, metadata)
	}
	if err != nil {
		return run.Run{}, err
	}
	if created.ProjectID == "" || runtimepreparation.OwnerKind(ownerKind) != runtimepreparation.OwnerRecoveryStatus && created.OperationID != ownerID {
		return run.Run{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_attempts(attempt_id,plan_id,run_id,request_id,payload_hash,stage,outcome,created_at) VALUES(?,?,?,?,?,'RUN_CREATED','PENDING',?)`, "attempt_"+runID, planID, runID, requestID, payloadHash, formatTime(now))
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if runtimepreparation.OwnerKind(ownerKind) == runtimepreparation.OwnerExecution || runtimepreparation.OwnerKind(ownerKind) == runtimepreparation.OwnerImplementation {
		result, updateErr := tx.ExecContext(ctx, `UPDATE execution_proposals SET status='CONSUMED',run_id=?,outcome='RUNNING',consumed_at=?,updated_at=? WHERE id=? AND status='ADMITTED' AND run_id IS NULL AND outcome='PENDING'`, runID, formatTime(now), formatTime(now), ownerID)
		if updateErr != nil {
			return run.Run{}, conflict(updateErr)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return run.Run{}, ErrConflict
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_receipts(request_id,payload_hash,plan_id,run_id,outcome,recorded_at) VALUES(?,?,?,?,'RUN_CREATED',?)`, requestID, payloadHash, planID, runID, formatTime(now))
	if err != nil {
		return run.Run{}, conflict(err)
	}
	if runtimepreparation.OwnerKind(ownerKind) == runtimepreparation.OwnerReview {
		if err = completeRecoveryReviewPreparationTx(ctx, tx, planID, created, requestID, payloadHash, now); err != nil {
			return run.Run{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return run.Run{}, err
	}
	return created, nil
}

func (s *RunStore) PreparationIdentity(ctx context.Context, runID string) (string, string, string, error) {
	var planID, requestID, payloadHash string
	err := s.db.sql.QueryRowContext(ctx, `SELECT plan_id,request_id,payload_hash FROM runtime_preparation_attempts WHERE run_id=?`, runID).Scan(&planID, &requestID, &payloadHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", run.ErrNotFound
	}
	return planID, requestID, payloadHash, err
}
