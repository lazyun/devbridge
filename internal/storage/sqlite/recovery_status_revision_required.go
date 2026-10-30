package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

// resolveRevisionRequiredTx fails closed until the resolve command carries a
// complete, typed revised-purpose proposal and durable source AI-session
// provenance. A recovery assessment alone cannot authorize a confirmation
// intent. Returning before any write preserves the ASSESSED recovery and its
// source owner when the caller has not supplied those facts.
func (s *RecoveryStatusStore) resolveRevisionRequiredTx(ctx context.Context, tx *sql.Tx, recovery *rs.Record, source run.Run, in rs.ResolveInput) error {
	p := in.Purpose
	if p != nil && p.SourceAISessionID == "" {
		return rs.ErrPurposeSourceSessionUnavailable
	}
	if p == nil || recovery.State != "ASSESSED" || recovery.Spec.Generation < 1 || p.Validate(recovery.Spec.Source, recovery.ID, recovery.Spec.Generation, recovery.AssessmentHash) != nil {
		return rs.ErrIneligible
	}
	currentGeneration, err := recoveryGenerationCurrentTx(ctx, tx, recovery.ID, recovery.Spec.Generation)
	if err != nil {
		return err
	}
	if !currentGeneration {
		return rs.ErrConflict
	}
	owner := ""
	switch source.Purpose {
	case run.PurposeImplementation:
		owner = "IMPLEMENTATION"
	case run.PurposeVerification:
		owner = "VERIFICATION"
	case run.PurposeScopeReview:
		owner = "REVIEW"
	}
	if owner == "" || owner != p.OwnerKind || p.RuntimeEpoch != source.RuntimeEpoch || p.SourceRunID != source.ID || p.SourceOperationID != source.OperationID {
		return rs.ErrIneligible
	}
	var session, project string
	query := `SELECT COALESCE(ai_session_id,''),project_id FROM execution_proposals WHERE id=? AND task_id=?`
	if owner == "REVIEW" {
		query = `SELECT COALESCE(created_by_ai_session_id,''),project_id FROM reviews WHERE id=? AND task_id=?`
	}
	if err := tx.QueryRowContext(ctx, query, p.OwnerID, p.TaskID).Scan(&session, &project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rs.ErrPurposeSourceSessionUnavailable
		}
		return err
	}
	if session == "" {
		return rs.ErrPurposeSourceSessionUnavailable
	}
	if session != p.SourceAISessionID || project != recovery.Spec.ProjectID {
		return rs.ErrPurposeSourceSessionMismatch
	}
	var sessionProject string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(project_id,'') FROM ai_sessions WHERE id=?`, session).Scan(&sessionProject); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rs.ErrPurposeSourceSessionUnavailable
		}
		return err
	}
	if sessionProject != project {
		return rs.ErrPurposeSourceSessionMismatch
	}
	var status, phase, currentExecution, currentReview, currentRevision string
	if err := tx.QueryRowContext(ctx, `SELECT status,phase,COALESCE(current_execution_id,''),COALESCE(current_review_id,''),COALESCE(current_definition_revision_id,'') FROM tasks WHERE id=? AND project_id=? AND feature_area_id=?`, p.TaskID, project, p.FeatureAreaID).Scan(&status, &phase, &currentExecution, &currentReview, &currentRevision); err != nil {
		return err
	}
	if status != "ACTIVE" || phase != "RECOVERY_REQUIRED" || currentRevision != p.TaskDefinitionRevisionID || (owner == "REVIEW" && currentReview != p.SourceReviewID) || (owner != "REVIEW" && currentExecution != p.SourceExecutionID) {
		return rs.ErrIneligible
	}
	if err := validatePurposeTargetsTx(ctx, tx, *p, source); err != nil {
		return err
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, project)
	if err != nil {
		return err
	}
	variants := make([]confirmation.Variant, 0, len(p.Variants))
	for _, variant := range p.Variants {
		v := confirmation.Variant{ID: variant.ID, Label: variant.Label, Summary: variant.Summary}
		switch owner {
		case "IMPLEMENTATION":
			b := variant.Implementation
			v.WorkSpec, v.VerificationSpec = &b.WorkSpec, &b.VerificationSpec
			v.RepositoryTargets, v.PrimaryMemberID, v.ImplementationMode = b.RepositoryTargets, b.PrimaryMemberID, b.ImplementationMode
		case "VERIFICATION":
			b := variant.Verification
			v.WorkSpec, v.VerificationSpec = b.WorkSpec, &b.VerificationSpec
			v.RepositoryTargets, v.PrimaryMemberID, v.ImplementationMode = b.RepositoryTargets, b.PrimaryMemberID, b.ImplementationMode
		case "REVIEW":
			b := variant.Review
			v.ReviewInstruction, v.RepositoryTargets = b.ReviewInstruction, b.RepositoryTargets
		}
		variants = append(variants, v)
	}
	prepare := confirmation.PrepareInput{AISessionID: session, RequestID: fmt.Sprintf("recovery-purpose:%s:%d", recovery.ID, recovery.Spec.Generation), Purpose: confirmation.RecoveryPurpose, Action: confirmation.ConfirmRecoveryPurpose,
		RecoveryPurpose: &confirmation.RecoveryPurposeBody{Proposal: *p, ExpectedTaskStatus: status, ExpectedTaskPhase: phase, PreparedTopologyRevision: topology, PreparedDevelopmentContextHash: development}, Variants: variants, RecommendedRuntimeConfig: p.RecommendedRuntimeConfig}
	prepare.SubmittedHash = confirmation.ValueHash(prepare)
	intent, err := prepareConfirmationIntentTx(ctx, tx, project, prepare, time.Now().UTC(), s.db.epoch)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_purpose_confirmation_links(recovery_id,recovery_generation,intent_ai_session_id,intent_id,intent_version,intent_content_hash,proposal_hash,source_owner_kind,source_owner_id,task_id,feature_area_id,source_execution_id,source_review_id,task_definition_revision_id,source_operation_id,source_run_id,source_turn_id,runtime_epoch,runtime_workset_hash,assessment_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		recovery.ID, recovery.Spec.Generation, intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash, p.SubmittedHash, p.OwnerKind, p.OwnerID, p.TaskID, p.FeatureAreaID, p.SourceExecutionID, p.SourceReviewID, p.TaskDefinitionRevisionID, p.SourceOperationID, p.SourceRunID, p.SourceTurnID, p.RuntimeEpoch, p.RuntimeWorksetHash, p.AssessmentHash, formatTime(time.Now().UTC()))
	if err != nil {
		return err
	}
	if err = appendInitialPurposeLinkTx(ctx, tx, intent); err != nil {
		return err
	}
	return finishRecoveryPurposeDiagnosticTx(ctx, tx, recovery, time.Now().UTC())
}

// A revision-required assessment has finished its independent diagnostic Turn.
// Close that child in the Resolve transaction, without claiming that the
// historical source Turn completed or granting the successor runtime authority.
func finishRecoveryPurposeDiagnosticTx(ctx context.Context, tx *sql.Tx, recovery *rs.Record, now time.Time) error {
	if recovery.RecoveryRunID == "" {
		// Historical assessments can precede the independent-child protocol.
		return nil
	}
	return finishRecoveryDiagnosticTx(ctx, tx, recovery, now)
}

func validatePurposeTargetsTx(ctx context.Context, tx *sql.Tx, p rs.PurposeProposal, source run.Run) error {
	if source.RuntimeWorkset == nil || source.RuntimeWorkset.ContentHash != p.RuntimeWorksetHash {
		return rs.ErrIneligible
	}
	allowed := map[string]workset.RuntimeMemberBinding{}
	for _, member := range source.RuntimeWorkset.Members {
		allowed[member.RepositoryID] = member
	}
	for _, variant := range p.Variants {
		var targets []workset.Target
		switch p.OwnerKind {
		case "IMPLEMENTATION":
			targets = variant.Implementation.RepositoryTargets
		case "VERIFICATION":
			targets = variant.Verification.RepositoryTargets
		case "REVIEW":
			targets = variant.Review.RepositoryTargets
		}
		if len(targets) == 0 {
			return rs.ErrIneligible
		}
		for _, target := range targets {
			member, ok := allowed[target.RepositoryID]
			if !ok || target.MemberID != member.MemberID || target.MemberVersion != member.MemberVersion || target.OriginCommit != member.OriginCommit || !target.Read || (p.OwnerKind == "REVIEW" && target.Write.Kind != "NONE") {
				return rs.ErrIneligible
			}
			if err := ensureRepositoryActiveTx(ctx, tx, source.ProjectID, target.RepositoryID); err != nil {
				return err
			}
		}
	}
	return nil
}
