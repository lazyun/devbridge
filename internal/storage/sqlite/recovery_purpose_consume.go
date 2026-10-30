package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

type RecoveryPurposeStore struct{ db *DB }

func purposeFenceError(code string) error {
	switch code {
	case "SOURCE_REVISION_CHANGED":
		return rs.ErrPurposeSourceRevisionChanged
	case "SOURCE_EPOCH_CHANGED":
		return rs.ErrPurposeSourceEpochChanged
	case "SOURCE_WORKSET_CHANGED":
		return rs.ErrPurposeSourceWorksetChanged
	case "EFFECTIVE_CANCELLATION":
		return rs.ErrPurposeEffectiveCancellation
	case "SUCCESSOR_ALREADY_EXISTS":
		return rs.ErrPurposeSuccessorExists
	case "PURPOSE_INTENT_INCOMPLETE", "PURPOSE_PROPOSAL_CHANGED", "RECOVERY_PURPOSE_LINK_NOT_CURRENT":
		return rs.ErrPurposeReceiptMismatch
	default:
		return rs.ErrIneligible
	}
}

func NewRecoveryPurposeStore(db *DB) *RecoveryPurposeStore { return &RecoveryPurposeStore{db: db} }

var _ rs.PurposeConsumer = (*RecoveryPurposeStore)(nil)

func (s *RecoveryPurposeStore) ApplyConfirmedImplementationPurpose(ctx context.Context, in rs.PurposeConsumeInput) (rs.PurposeConsumption, error) {
	return s.consume(ctx, in, "IMPLEMENTATION", rs.ApplyConfirmedImplementationPurpose)
}
func (s *RecoveryPurposeStore) ApplyConfirmedVerificationPurpose(ctx context.Context, in rs.PurposeConsumeInput) (rs.PurposeConsumption, error) {
	return s.consume(ctx, in, "VERIFICATION", rs.ApplyConfirmedVerificationPurpose)
}
func (s *RecoveryPurposeStore) ApplyConfirmedReviewPurpose(ctx context.Context, in rs.PurposeConsumeInput) (rs.PurposeConsumption, error) {
	return s.consume(ctx, in, "REVIEW", rs.ApplyConfirmedReviewPurpose)
}

func (s *RecoveryPurposeStore) consume(ctx context.Context, in rs.PurposeConsumeInput, owner, action string) (rs.PurposeConsumption, error) {
	return s.consumeConfirmed(ctx, in, owner, action, false)
}

// Only the write-side receipt scanner may use Core authority. Public commands
// still authenticate their caller; both paths validate the same immutable
// confirmation and all live consumption fences in this transaction.
func (s *RecoveryPurposeStore) consumeConfirmed(ctx context.Context, in rs.PurposeConsumeInput, owner, action string, core bool) (rs.PurposeConsumption, error) {
	var out rs.PurposeConsumption
	if in.AISessionID == "" || in.IntentID == "" || in.Version < 1 || in.ContentHash == "" || in.RequestID == "" || in.Action != action || in.PayloadHash == "" || in.PayloadHash != in.ComputePayloadHash() {
		return out, rs.ErrPurposeRequestConflict
	}
	tx, err := s.db.beginWrite(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if !core {
		if err = instructionCaller(ctx, tx, in.AISessionID); err != nil {
			return out, err
		}
	}
	var savedIntent, savedHash, savedOwner, savedAction, savedRevision, savedExecution, savedReview, savedRun string
	err = tx.QueryRowContext(ctx, `SELECT intent_id,consume_payload_hash,source_owner_kind,consume_action,successor_task_definition_revision_id,successor_execution_id,successor_review_id,successor_run_id FROM recovery_purpose_confirmation_consumptions WHERE intent_ai_session_id=? AND consume_request_id=?`, in.AISessionID, in.RequestID).Scan(&savedIntent, &savedHash, &savedOwner, &savedAction, &savedRevision, &savedExecution, &savedReview, &savedRun)
	if err == nil {
		if savedIntent != in.IntentID || savedHash != in.PayloadHash || savedOwner != owner || savedAction != action {
			return out, rs.ErrPurposeRequestConflict
		}
		var taskID string
		if err = tx.QueryRowContext(ctx, `SELECT task_id FROM recovery_purpose_link_history WHERE intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=?`, in.AISessionID, in.IntentID, in.Version, in.ContentHash).Scan(&taskID); err != nil {
			return out, err
		}
		phase := "IMPLEMENTATION_READY"
		if owner == "REVIEW" {
			phase = "REVIEWING"
		}
		return rs.PurposeConsumption{IntentID: in.IntentID, Action: action, RequestID: in.RequestID, OwnerKind: owner, TaskID: taskID, TaskDefinitionRevisionID: savedRevision, ExecutionID: savedExecution, ReviewID: savedReview, RunID: savedRun, TaskPhase: phase, Replay: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	intent, err := formalConfirmationTx(ctx, tx, in.AISessionID, in.IntentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, rs.ErrPurposeReceiptMismatch
		}
		return out, err
	}
	if intent.Version != in.Version || intent.ContentHash != in.ContentHash || intent.Purpose != confirmation.RecoveryPurpose || intent.State != "COMPLETED" || intent.Commit == nil || intent.Content.RecoveryPurpose == nil {
		return out, rs.ErrPurposeReceiptMismatch
	}
	if err = validateCommittedPurposeReceiptTx(ctx, tx, intent); err != nil {
		return out, err
	}
	p := intent.Content.RecoveryPurpose.Proposal
	if p.OwnerKind != owner {
		return out, rs.ErrPurposeOwnerMismatch
	}
	if p.SubmittedHash != p.ComputeSubmittedHash() {
		return out, rs.ErrPurposeReceiptMismatch
	}
	var selectedVariant, receiptOwner, receiptProposal, receiptOutcome, receiptLink string
	var receiptOrdinal int
	err = tx.QueryRowContext(ctx, `SELECT selected_variant_id,source_owner_kind,proposal_hash,outcome,purpose_link_id,purpose_link_ordinal FROM recovery_purpose_confirmation_receipts WHERE intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=?`, in.AISessionID, in.IntentID, in.Version, in.ContentHash).Scan(&selectedVariant, &receiptOwner, &receiptProposal, &receiptOutcome, &receiptLink, &receiptOrdinal)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, rs.ErrPurposeReceiptMismatch
		}
		return out, err
	}
	if receiptOwner != owner {
		return out, rs.ErrPurposeOwnerMismatch
	}
	if receiptProposal != p.SubmittedHash || receiptOutcome != "COMMITTED" || selectedVariant != intent.Commit.VariantID {
		return out, rs.ErrPurposeReceiptMismatch
	}
	var already int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recovery_purpose_confirmation_consumptions WHERE intent_ai_session_id=? AND intent_id=? AND intent_version=? AND intent_content_hash=?)`, in.AISessionID, in.IntentID, in.Version, in.ContentHash).Scan(&already); err != nil {
		return out, err
	}
	if already != 0 {
		return out, rs.ErrPurposeAlreadyConsumed
	}
	if failure, fenceErr := recoveryPurposeCommitFenceTx(ctx, tx, intent); fenceErr != nil {
		return out, fenceErr
	} else if failure != nil {
		return out, purposeFenceError(failure.Code)
	}
	link, err := currentPurposeLinkTx(ctx, tx, intent)
	if err != nil {
		return out, err
	}
	if link.ID != receiptLink || link.Ordinal != receiptOrdinal {
		return out, rs.ErrPurposeReceiptMismatch
	}
	ctx = taskaction.WithIntent(ctx, intent.IntentID)
	if err = taskActionAdmissionTx(ctx, tx, p.TaskID); err != nil {
		return out, err
	}
	var variant *rs.PurposeVariant
	for i := range p.Variants {
		if p.Variants[i].ID == selectedVariant {
			variant = &p.Variants[i]
			break
		}
	}
	if variant == nil {
		return out, rs.ErrPurposeReceiptMismatch
	}
	var quiesced int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(quiesced,0) FROM agent_operation_boundaries WHERE run_id=?`, p.SourceRunID).Scan(&quiesced); err != nil || quiesced != 1 {
		return out, rs.ErrIneligible
	}
	// Read the exact source authority before any semantic or lifecycle write.
	// The purpose receipt authorizes this successor; it never creates a formal
	// Review approval or copies a revision binding from the predecessor.
	var sourceAuthority *runtimeselection.CurrentAuthority
	if owner != "REVIEW" {
		authority, authorityErr := recoveryPurposeSourceAuthorityTx(ctx, tx, intent)
		if authorityErr != nil {
			return out, authorityErr
		}
		sourceAuthority = &authority
	}
	now := time.Now().UTC()
	out = rs.PurposeConsumption{IntentID: in.IntentID, Action: action, RequestID: in.RequestID, OwnerKind: owner, TaskID: p.TaskID}
	identity := workflowHash(in.IntentID + "\x00" + in.RequestID)[:32]
	if owner == "REVIEW" {
		out.ReviewID = "review_recovery_purpose_" + identity
	} else {
		out.ExecutionID = "execution_recovery_purpose_" + identity
		out.TaskDefinitionRevisionID, err = plannedPurposeRevisionIDTx(ctx, tx, p, *variant, in)
		if err != nil {
			return rs.PurposeConsumption{}, err
		}
	}
	// The immutable consumption is inserted before Task mutations so the SQL
	// guards can prove this exact receipt-first authority. Every successor and
	// the receipt are still one transaction; any later failure rolls both back.
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_purpose_confirmation_consumptions(intent_ai_session_id,intent_id,intent_version,intent_content_hash,consume_action,consume_request_id,consume_payload_hash,source_owner_kind,selected_variant_id,proposal_hash,successor_task_definition_revision_id,successor_execution_id,successor_review_id,successor_run_id,consumed_at,purpose_link_id,purpose_link_ordinal) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.AISessionID, in.IntentID, in.Version, in.ContentHash, action, in.RequestID, in.PayloadHash, owner, selectedVariant, p.SubmittedHash, out.TaskDefinitionRevisionID, out.ExecutionID, out.ReviewID, out.RunID, formatTime(now), link.ID, link.Ordinal)
	if err != nil {
		return rs.PurposeConsumption{}, err
	}
	claim, err := tx.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE feature_area_id=? AND run_id=? AND operation_kind=? AND operation_id=?`, p.FeatureAreaID, p.SourceRunID, map[bool]string{true: "REVIEW", false: "EXECUTION"}[owner == "REVIEW"], p.OwnerID)
	if err != nil {
		return rs.PurposeConsumption{}, err
	}
	if n, _ := claim.RowsAffected(); n != 1 {
		return rs.PurposeConsumption{}, rs.ErrPurposeSuccessorExists
	}
	terminal := formatTime(now)
	terminalized, err := tx.ExecContext(ctx, `UPDATE runs SET status='FAILED',failure_class=COALESCE(failure_class,'TASK_FAILED'),failure_code=COALESCE(failure_code,'RECOVERY_REVISION_REQUIRED'),failure_stage=COALESCE(failure_stage,'engineering recovery'),failure_message=COALESCE(failure_message,'RECOVERY_REVISION_REQUIRED'),finished_at=COALESCE(finished_at,?),updated_at=? WHERE id=? AND turn_id=? AND status IN ('RUNNING','COLLECTING','FAILED','INFRASTRUCTURE_FAILED')`, terminal, terminal, p.SourceRunID, p.SourceTurnID)
	if err != nil {
		return rs.PurposeConsumption{}, err
	}
	if n, _ := terminalized.RowsAffected(); n != 1 {
		return rs.PurposeConsumption{}, rs.ErrIneligible
	}
	if owner != "REVIEW" {
		if _, err = tx.ExecContext(ctx, `UPDATE execution_proposals SET outcome='FAILED',failure_code=COALESCE(failure_code,'RECOVERY_REVISION_REQUIRED'),failure_message=COALESCE(failure_message,'RECOVERY_REVISION_REQUIRED'),updated_at=? WHERE id=? AND task_id=? AND outcome IN ('RUNNING','FAILED')`, terminal, p.SourceExecutionID, p.TaskID); err != nil {
			return rs.PurposeConsumption{}, err
		}
	}
	switch owner {
	case "IMPLEMENTATION", "VERIFICATION":
		err = applyConfirmedRevisionPurposeTx(ctx, tx, intent, *variant, in, &out, *sourceAuthority, now)
	case "REVIEW":
		err = applyConfirmedReviewPurposeTx(ctx, tx, intent, *variant, in, &out, now)
	}
	if err != nil {
		return rs.PurposeConsumption{}, err
	}
	if err = freezeRecoveryPurposeScopeTx(ctx, tx, intent, *variant, in, out, sourceAuthority, now); err != nil {
		return rs.PurposeConsumption{}, err
	}
	if err = tx.Commit(); err != nil {
		return rs.PurposeConsumption{}, err
	}
	return out, nil
}

func applyConfirmedRevisionPurposeTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, variant rs.PurposeVariant, in rs.PurposeConsumeInput, out *rs.PurposeConsumption, authority runtimeselection.CurrentAuthority, now time.Time) error {
	p := intent.Content.RecoveryPurpose.Proposal
	var work task.WorkSpec
	var verification task.VerificationSpec
	var targets []workset.Target
	var primary string
	if p.OwnerKind == "IMPLEMENTATION" {
		if variant.Implementation == nil {
			return rs.ErrIneligible
		}
		work, verification, targets, primary = variant.Implementation.WorkSpec, variant.Implementation.VerificationSpec, variant.Implementation.RepositoryTargets, variant.Implementation.PrimaryMemberID
	} else {
		if variant.Verification == nil {
			return rs.ErrIneligible
		}
		verification, targets, primary = variant.Verification.VerificationSpec, variant.Verification.RepositoryTargets, variant.Verification.PrimaryMemberID
		if variant.Verification.WorkSpec != nil {
			work = *variant.Verification.WorkSpec
		} else {
			current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, p.TaskID))
			if err != nil {
				return err
			}
			work = current.WorkSpec
		}
	}
	if err := task.ValidateTargetWorkSpec(work); err != nil {
		return err
	}
	if err := task.ValidateTargetVerification(verification, targets); err != nil {
		return err
	}
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, p.TaskID))
	if err != nil {
		return err
	}
	predecessor, err := scanExecutionProposal(tx.QueryRowContext(ctx, selectExecutionProposal+` WHERE id=?`, p.SourceExecutionID))
	if err != nil {
		return err
	}
	if predecessor.TaskID != current.ID || predecessor.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID {
		return rs.ErrIneligible
	}
	current.WorkSpec, current.VerificationSpec, current.RepositoryTargets, current.PrimaryMemberID = work, verification, targets, primary
	current.Goal, current.Constraints = work.Goal, append([]string(nil), work.Constraints...)
	current.RepositoryID, current.BaseCommit = "", ""
	planID := "task_plan_recovery_purpose_" + workflowHash(in.IntentID + "\x00" + in.RequestID)[:32]
	executionID := "execution_recovery_purpose_" + workflowHash(in.IntentID + "\x00" + in.RequestID)[:32]
	workJSON, err := encodeWorkSpec(work)
	if err != nil {
		return err
	}
	verifyJSON, err := encodeVerificationSpec(verification)
	if err != nil {
		return err
	}
	constraintsJSON, err := encodeStringItems(work.Constraints)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_plans(id,ai_session_id,content_actor_kind,content_actor_id,project_id,task_id,title,work_spec_json,verification_spec_json,status,created_at,updated_at,confirmed_at,applied_at) VALUES(?,?,?,?,?,?,?,?,?,'APPLIED',?,?,?,?)`, planID, in.AISessionID, "AI_SESSION", in.AISessionID, current.ProjectID, current.ID, work.Goal, workJSON, verifyJSON, formatTime(now), formatTime(now), formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("purpose task plan insert: %w", err)
	}
	current.ConfirmedTaskPlanID = planID
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET repository_id=NULL,base_commit='',goal=?,constraints_json=?,work_spec_json=?,verification_spec_json=?,confirmed_task_plan_id=?,phase='IMPLEMENTATION_READY',updated_at=? WHERE id=? AND status='ACTIVE' AND phase='RECOVERY_REQUIRED' AND current_definition_revision_id=? AND current_execution_id=?`, work.Goal, constraintsJSON, workJSON, verifyJSON, planID, formatTime(now), current.ID, p.TaskDefinitionRevisionID, p.SourceExecutionID)
	if err != nil {
		return fmt.Errorf("purpose task semantics update: %w", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return rs.ErrConflict
	}
	current.Phase = task.PhaseImplementationReady
	revision, err := ensureTaskDefinitionTx(ctx, tx, &current, now)
	if err != nil {
		return fmt.Errorf("purpose definition revision: %w", err)
	}
	if revision.ID == p.TaskDefinitionRevisionID || revision.ID == "" {
		return rs.ErrIneligible
	}
	if err = insertRecoveryPurposeExecutionTx(ctx, tx, intent, in, current, predecessor, *out, authority, now); err != nil {
		return fmt.Errorf("purpose successor Execution: %w", err)
	}
	if err = insertExecutionStartEffectTx(ctx, tx, executionStartEffectRecord{ProjectID: current.ProjectID, ActorKind: "AI_SESSION", ActorID: in.AISessionID, RequestID: in.RequestID, Action: "START_EXECUTION", Effect: wp.StartEffectStartAuthorized, TaskID: current.ID, FeatureAreaID: current.FeatureAreaID, RevisionID: revision.ID, ExecutionID: executionID, Phase: task.PhaseImplementationReady}, formatTime(now)); err != nil {
		return err
	}
	if revision.ID != out.TaskDefinitionRevisionID || executionID != out.ExecutionID {
		return rs.ErrConflict
	}
	out.TaskPhase = "IMPLEMENTATION_READY"
	return nil
}

func applyConfirmedReviewPurposeTx(ctx context.Context, tx *sql.Tx, intent confirmation.Intent, variant rs.PurposeVariant, in rs.PurposeConsumeInput, out *rs.PurposeConsumption, now time.Time) error {
	p := intent.Content.RecoveryPurpose.Proposal
	b := variant.Review
	if b == nil || b.SourceReviewID != p.SourceReviewID || b.ReviewInstruction == "" || b.ReviewDepth == "" || b.ReviewPlan.Mode == "" {
		return rs.ErrIneligible
	}
	previous, err := scanReview(tx.QueryRowContext(ctx, reviewSelect+` WHERE r.id=? AND r.task_id=? AND r.project_id=?`, p.SourceReviewID, p.TaskID, intent.ProjectID))
	if err != nil {
		return err
	}
	if previous.ID != p.SourceReviewID || previous.TaskID != p.TaskID || previous.Ordinal < 1 || previous.RunID != p.SourceRunID {
		return rs.ErrIneligible
	}
	var latest string
	if err = tx.QueryRowContext(ctx, `SELECT id FROM reviews WHERE task_id=? ORDER BY ordinal DESC LIMIT 1`, p.TaskID).Scan(&latest); err != nil || latest != previous.ID {
		return rs.ErrIneligible
	}
	identity := workflowHash(in.IntentID + "\x00" + in.RequestID)
	reviewID := "review_recovery_purpose_" + identity[:32]
	changed, err := tx.ExecContext(ctx, `UPDATE tasks SET phase='REVIEW_REQUIRED',required_review_depth=?,updated_at=? WHERE id=? AND status='ACTIVE' AND phase='RECOVERY_REQUIRED' AND current_review_id=?`, string(b.ReviewDepth), formatTime(now), p.TaskID, p.SourceReviewID)
	if err != nil {
		return err
	}
	if n, _ := changed.RowsAffected(); n != 1 {
		return rs.ErrConflict
	}
	successor := previous
	successor.ID, successor.RunID, successor.Ordinal = reviewID, "", 0
	successor.ReviewDisposition, successor.TerminalStatus, successor.ReportID = "", "", ""
	successor.StartRequestID = ""
	successor.FinalCodexPrompt, successor.PromptSHA256, successor.PromptBytes, successor.PromptSentAt = "", "", 0, nil
	successor.StartFailure, successor.ContinuationAttemptedAt = nil, nil
	successor.CreatedAt, successor.ReviewDepth = now.UTC(), b.ReviewDepth
	successor.Instruction = b.ReviewInstruction
	if previous.RepositoryID == "" {
		successor.RepositoryTargets = workset.CloneTargets(b.RepositoryTargets)
	} else {
		if len(b.RepositoryTargets) != 1 || b.RepositoryTargets[0].RepositoryID != previous.RepositoryID || b.RepositoryTargets[0].Write.Kind != "NONE" {
			return rs.ErrIneligible
		}
		successor.RepositoryTargets = nil
	}
	if successor.RepositoryProvenance != nil {
		provenance := *successor.RepositoryProvenance
		provenance.ReviewID = successor.ID
		successor.RepositoryProvenance = &provenance
	}
	if err = insertReviewTx(ctx, tx, successor); err != nil {
		return fmt.Errorf("purpose successor Review: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT ordinal FROM reviews WHERE id=? AND task_id=?`, reviewID, p.TaskID).Scan(&successor.Ordinal); err != nil {
		return err
	}
	if successor.Ordinal != previous.Ordinal+1 {
		return rs.ErrConflict
	}
	if len(successor.RepositoryTargets) == 0 {
		if successor.RepositoryProvenance == nil {
			return rs.ErrIneligible
		}
		provenance := successor.RepositoryProvenance
		var dirty any
		if provenance.WorkingTreeDirtyAtAdmission != nil {
			dirty = *provenance.WorkingTreeDirtyAtAdmission
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO review_repository_provenance(review_id,repository_id,reviewed_commit,captured_at,repository_head,branch_kind,branch_ref,working_tree_dirty,inspection_mode) VALUES(?,?,?,?,?,?,?,?,?)`, reviewID, provenance.RepositoryID, provenance.Commit, formatTime(now), provenance.RepositoryHEAD, provenance.BranchKind, nullString(provenance.BranchRef), dirty, provenance.InspectionMode); err != nil {
			return err
		}
	}
	scope := b.ReviewPlan.InitialScope
	grantMode := reviewgrant.ModeRepository
	if successor.ExecutorKind == review.ExecutorUpperLLM && scope.Kind == run.ReviewScopePaths {
		grantMode = reviewgrant.ModeFocused
	}
	paths, err := encodeReviewScopePaths(scope.Paths)
	if err != nil {
		return err
	}
	selected, err := json.Marshal(struct {
		Version int   `json:"version"`
		Items   []any `json:"items"`
	}{Version: 1, Items: []any{}})
	if err != nil {
		return err
	}
	forbidden, err := json.Marshal(append([]string{}, successor.ForbiddenPaths...))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO review_grants(review_id,feature_area_id,project_id,repository_id,mode,initial_scope_kind,initial_scope_paths_json,selected_modules_json,created_at,forbidden_paths_json,protocol_version) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, reviewID, successor.FeatureAreaID, successor.ProjectID, nullString(successor.RepositoryID), grantMode, scope.Kind, paths, string(selected), formatTime(now), string(forbidden), reviewProtocol(successor)); err != nil {
		return err
	}
	if err = captureReviewWorkflowTx(ctx, tx, successor); err != nil {
		return err
	}
	if reviewID != out.ReviewID || out.RunID != "" {
		return rs.ErrConflict
	}
	out.TaskPhase = "REVIEWING"
	return nil
}

func plannedPurposeRevisionIDTx(ctx context.Context, tx *sql.Tx, p rs.PurposeProposal, variant rs.PurposeVariant, in rs.PurposeConsumeInput) (string, error) {
	current, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=?`, p.TaskID))
	if err != nil {
		return "", err
	}
	var work task.WorkSpec
	var verification task.VerificationSpec
	var targets []workset.Target
	var primary string
	if p.OwnerKind == "IMPLEMENTATION" {
		b := variant.Implementation
		if b == nil {
			return "", rs.ErrIneligible
		}
		work, verification, targets, primary = b.WorkSpec, b.VerificationSpec, b.RepositoryTargets, b.PrimaryMemberID
	} else {
		b := variant.Verification
		if b == nil {
			return "", rs.ErrIneligible
		}
		work, verification, targets, primary = current.WorkSpec, b.VerificationSpec, b.RepositoryTargets, b.PrimaryMemberID
		if b.WorkSpec != nil {
			work = *b.WorkSpec
		}
		if work.ImplementationMode != b.ImplementationMode {
			return "", rs.ErrIneligible
		}
	}
	current.WorkSpec, current.VerificationSpec, current.RepositoryTargets, current.PrimaryMemberID = work, verification, targets, primary
	current.Goal, current.Constraints = work.Goal, append([]string(nil), work.Constraints...)
	current.ConfirmedTaskPlanID = "task_plan_recovery_purpose_" + workflowHash(in.IntentID + "\x00" + in.RequestID)[:32]
	current.Phase = task.PhaseImplementationReady
	goals, err := definitionGoalsTx(ctx, tx, current.ID)
	if err != nil {
		return "", err
	}
	definition := task.DefinitionFromTask(current, goals)
	old, err := getDefinitionRevision(ctx, tx, current.CurrentDefinitionRevisionID)
	if err != nil {
		return "", err
	}
	if old.Definition.ReviewApprovalID == definition.ReviewApprovalID {
		definition.ContinuationNormalizationVersion = old.Definition.ContinuationNormalizationVersion
	}
	_, hash, err := task.EncodeDefinition(definition)
	if err != nil {
		return "", err
	}
	if old.PayloadSHA256 == hash {
		return "", rs.ErrIneligible
	}
	var ordinal int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal),0)+1 FROM task_definition_revisions WHERE task_id=?`, current.ID).Scan(&ordinal); err != nil {
		return "", err
	}
	return fmt.Sprintf("task_definition_%s_%d_%s", current.ID, ordinal, hash[:16]), nil
}
