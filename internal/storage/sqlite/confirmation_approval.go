package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func confirmedApprovalResultTx(ctx context.Context, tx *sql.Tx, raw string, in wp.ResolveInput) (review.ImplementationResult, error) {
	original, err := parsePersistedApprovalResult(raw)
	if err != nil || in.ConfirmationIntentID == "" {
		return original, err
	}
	intent, err := confirmationRead(ctx, tx, in.AISessionID, in.ConfirmationIntentID)
	if err != nil {
		return original, err
	}
	b := intent.Content.Execution
	if intent.State != "CONFIRMING" || intent.Purpose != confirmation.Execution || (intent.Action != "APPROVE_AND_START" && intent.Action != "APPROVE") || b == nil || b.TaskID != in.TaskID || b.FeatureAreaID != in.FeatureAreaID || b.ReviewID != in.ReviewID || b.DecisionID != in.DecisionID || intent.Commit == nil {
		return original, ErrConflict
	}
	stepOK := false
	for _, s := range intent.Steps {
		if s.Kind == "APPROVE" && s.RequestID == in.RequestID && s.State == "UNKNOWN" {
			stepOK = true
		}
	}
	if !stepOK {
		return original, ErrConflict
	}
	// A v2 Coding approval may omit the caller selection and inherit the exact
	// frozen Review runtime facts. A supplied value is still compared against
	// the confirmation record; omission never permits substitution.
	if (original.Version != 2 || in.NextRunRuntimeConfig != nil) && ((intent.Commit.Selection.RuntimeConfig == nil) != (in.NextRunRuntimeConfig == nil) || intent.Commit.Selection.RuntimeConfig != nil && *intent.Commit.Selection.RuntimeConfig != *in.NextRunRuntimeConfig) {
		return original, ErrConflict
	}
	committedBackend := intent.Commit.Selection.Backend
	if (original.Version != 2 || in.BackendSelection != nil) && ((committedBackend == nil) != (in.BackendSelection == nil) || committedBackend != nil && (committedBackend.ConfigID != in.BackendSelection.ConfigID || committedBackend.ConfigFingerprint != in.BackendSelection.ConfigFingerprint)) {
		return original, ErrConflict
	}
	for _, v := range intent.Content.Variants {
		if v.ID == intent.Commit.VariantID {
			if v.WorkSpec == nil || v.VerificationSpec == nil || v.ImplementationMode != v.WorkSpec.ImplementationMode {
				return original, ErrConflict
			}
			if original.Version == 2 {
				if !reflect.DeepEqual(original.RepositoryTargets, v.RepositoryTargets) || original.PrimaryMemberID != v.PrimaryMemberID || !reflect.DeepEqual(original.WorkSpec, *v.WorkSpec) || !reflect.DeepEqual(original.VerificationSpec, *v.VerificationSpec) {
					return original, ErrConflict
				}
				definition := task.Definition{Version: 2, RepositoryTargets: v.RepositoryTargets, PrimaryMemberID: v.PrimaryMemberID, WorkSpec: *v.WorkSpec, VerificationSpec: *v.VerificationSpec}
				err = task.ValidateDefinitionTargets(definition)
				if err == nil {
					err = task.ValidateExecutablePrimary(definition)
				}
			} else {
				if len(v.RepositoryTargets) > 0 {
					return original, ErrConflict
				}
				err = task.ValidateWorkSpec(*v.WorkSpec)
			}
			if err != nil {
				return original, err
			}
			if err = task.ValidateExecutableVerificationSpec(*v.VerificationSpec); err != nil {
				return original, err
			}
			if v.VerificationSpec.Mode != task.VerificationModeRun && !(v.ImplementationMode == task.ImplementationDirectFile && v.VerificationSpec.Mode == task.VerificationModeNone) {
				return original, ErrConflict
			}
			original.WorkSpec = *v.WorkSpec
			original.VerificationSpec = *v.VerificationSpec
			original.PrimaryMemberID = v.PrimaryMemberID
			if waiver := intent.Commit.Selection.VerificationWaiver; waiver != nil {
				if original.Version != 2 || intent.Action != "APPROVE" || v.ImplementationMode != task.ImplementationDirectFile {
					return original, ErrConflict
				}
				waived := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: waiver}
				if err := task.ValidateVerificationSpec(waived); err != nil {
					return original, err
				}
				original.VerificationSpec = waived
			}
			return original, nil
		}
	}
	return original, ErrConflict
}
func confirmationApprovalReplayTx(ctx context.Context, tx *sql.Tx, in wp.ResolveInput) error {
	var saved string
	err := tx.QueryRowContext(ctx, `SELECT intent_id FROM confirmation_approval_sources WHERE decision_id=?`, in.DecisionID).Scan(&saved)
	if errors.Is(err, sql.ErrNoRows) {
		if in.ConfirmationIntentID == "" {
			return nil
		}
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if saved != in.ConfirmationIntentID {
		return ErrConflict
	}
	return nil
}

// The same transaction that admits a new Review fences its consumed Card's
// exact observed lifecycle; canonical historical admission replay stays first.
func confirmationReviewAdmissionTx(ctx context.Context, tx *sql.Tx, candidate review.Review, requestID string) error {
	if candidate.RuntimeConfirmationID == "" {
		return nil
	}
	intent, err := confirmationRead(ctx, tx, candidate.CreatedByAISessionID, candidate.RuntimeConfirmationID)
	if err != nil {
		return err
	}
	if intent.State != "CONFIRMING" || intent.Commit == nil || intent.ProjectID != candidate.ProjectID {
		return ErrConflict
	}
	active := false
	taskCreated := false
	createdRevision := ""
	requestReview := ""
	for _, s := range intent.Steps {
		if s.Kind == "START_REVIEW" && s.State == "UNKNOWN" && s.RequestID == requestID {
			active = true
		}
		if s.Kind == "CREATE_TASK" && s.State == "COMPLETED" && s.TaskID == candidate.TaskID {
			taskCreated = true
			createdRevision = s.RevisionID
		}
		if s.Kind == "REQUEST_REVIEW" && s.State == "COMPLETED" {
			requestReview = s.RequestID
		}
	}
	if !active {
		return ErrConflict
	}
	selected := false
	for _, v := range intent.Content.Variants {
		if v.ID == intent.Commit.VariantID {
			selected = v.ExecutorKind == string(candidate.ExecutorKind) && v.ReviewInstruction == candidate.Instruction
		}
	}
	if !selected {
		return ErrConflict
	}
	if candidate.ExecutorKind == review.ExecutorCodingAgent {
		if intent.Commit.Selection.RuntimeConfig == nil || *intent.Commit.Selection.RuntimeConfig != candidate.RuntimeConfig || intent.Commit.Selection.Backend == nil || intent.Commit.Selection.Backend.ConfigID != candidate.CodexConfigID {
			return ErrConflict
		}
		var id, name, path string
		var enabled int
		if err := tx.QueryRowContext(ctx, `SELECT id,name,codex_path,enabled FROM codex_configs WHERE id=?`, candidate.CodexConfigID).Scan(&id, &name, &path, &enabled); err != nil || enabled == 0 || (codexconfig.Config{ID: id, Name: name, CodexPath: path}).Fingerprint() != intent.Commit.Selection.Backend.ConfigFingerprint {
			return ErrConflict
		}
	} else if intent.Commit.Selection.RuntimeConfig != nil || intent.Commit.Selection.Backend != nil || intent.Commit.BackendSelection != nil {
		return ErrConflict
	}
	t, err := scanTask(tx.QueryRowContext(ctx, selectTask+` WHERE id=? AND project_id=? AND feature_area_id=?`, candidate.TaskID, candidate.ProjectID, candidate.FeatureAreaID))
	if err != nil {
		return err
	}
	if taskCreated {
		if t.Phase != task.PhaseReviewRequired || t.CurrentReviewID != "" || t.CurrentDefinitionRevisionID != createdRevision || t.CurrentExecutionID != "" {
			return ErrConflict
		}
		return nil
	}
	b := intent.Content.Review
	if b == nil || b.TaskID != candidate.TaskID || b.FeatureAreaID != candidate.FeatureAreaID || b.RepositoryID != candidate.RepositoryID || b.WorkspaceID != candidate.WorkspaceID || b.ReviewDepth != candidate.ReviewDepth || !reflect.DeepEqual(b.RepositoryTargets, candidate.RepositoryTargets) || b.ExpectedTarget == nil {
		return ErrConflict
	}
	expected := b.ExpectedTarget
	if intent.Action == "REQUEST_SUPPLEMENTAL_REVIEW" {
		var exact bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_supplemental_review_current c JOIN task_supplemental_review_requests r ON r.id=c.request_id WHERE c.task_id=? AND r.task_id=c.task_id AND r.project_id=? AND r.ai_session_id=? AND ((r.actor_kind='AI_SESSION' AND r.actor_id=?) OR (r.actor_kind IS NULL AND r.actor_id IS NULL)) AND r.request_id=? AND r.source_revision_id=? AND r.source_review_id=? AND r.source_execution_id=?)`, t.ID, t.ProjectID, intent.AISessionID, intent.AISessionID, requestReview, expected.TaskDefinitionRevisionID, expected.CurrentReviewID, expected.CurrentExecutionID).Scan(&exact)
		if err != nil {
			return err
		}
		if !exact || t.Phase != task.PhaseReviewRequired || t.CurrentDefinitionRevisionID != expected.TaskDefinitionRevisionID || t.CurrentReviewID != expected.CurrentReviewID || t.CurrentExecutionID != "" {
			return ErrConflict
		}
		return nil
	}
	if intent.Action != "START_REVIEW" || t.Phase != expected.Phase || t.CurrentReviewID != expected.CurrentReviewID || t.CurrentDefinitionRevisionID != expected.TaskDefinitionRevisionID || t.CurrentExecutionID != expected.CurrentExecutionID || expected.PendingDecisionID != "" {
		return ErrConflict
	}
	return nil
}
