package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Only this atomic continuation is eligible for negative recovery. Its original
// writer must be retired, and the original identity and every mandatory durable
// result must independently prove that it never committed. An error code or a
// missing receipt alone is not proof. This is a detached read projection; the
// consumed submission remains immutable and never becomes NOT_CONSUMED.
func recoverRejectedContinuationTx(ctx context.Context, q definitionQuerier, out confirmation.Intent, worker, epoch string) (confirmation.Intent, bool, error) {
	if out.State != "UNKNOWN" || out.Purpose != confirmation.Execution || out.Action != wp.AcceptanceRequestChanges || out.Commit == nil || worker == "" || worker == epoch || len(out.Steps) != 2 {
		return out, false, nil
	}
	first, next := out.Steps[0], out.Steps[1]
	if first.Kind != "RESOLVE_CONTINUATION" || first.State != "UNKNOWN" || first.Effect != "UNKNOWN" || first.Failure == nil || first.Failure.Code != runtimeselection.ErrBindingMissing.Error() || next.Kind != "START_IMPLEMENTATION" || next.State != "PENDING" {
		return out, false, nil
	}
	for _, step := range out.Steps {
		if step.RequestID != "confirmation_"+confirmation.ValueHash([]string{out.IntentID, step.Kind})[:32] || step.ExecutionID != "" || step.RunID != "" || step.RevisionID != "" || step.WorkflowID != "" {
			return out, false, nil
		}
	}
	var fenced bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runtime_authority a JOIN runtime_epochs old ON old.id=? WHERE a.singleton=1 AND a.epoch_id=? AND a.epoch_id<>old.id)`, worker, epoch).Scan(&fenced); err != nil || !fenced {
		return out, false, err
	}
	strict, err := formalConfirmationTx(ctx, q, out.AISessionID, out.IntentID)
	if err != nil {
		return out, false, err
	}
	if strict.Commit == nil || strict.State != out.State || confirmation.ValueHash(strict.Steps) != confirmation.ValueHash(out.Steps) {
		return out, false, ErrConflict
	}
	sub, err := confirmationSubmissionTx(ctx, q, strict, strict.Commit.RequestID)
	if err != nil {
		return out, false, err
	}
	// SubmittedHash is intentionally excluded from intent JSON. Its authority
	// is the exact original submission row, never a reconstructed projection.
	if !sub.Recorded || sub.Outcome != confirmation.Consumed || sub.PayloadSHA256 == "" || sub.ReceiptRequestID != strict.Commit.RequestID {
		return out, false, nil
	}
	b := strict.Content.Execution
	if b == nil || b.TaskID == "" || b.ExecutionID == "" || b.TaskDefinitionRevisionID == "" || b.DecisionID != wp.TaskAcceptanceDecisionID(b.ExecutionID) {
		return out, false, nil
	}
	var raw, hash string
	err = q.QueryRowContext(ctx, `SELECT d.payload_json,d.payload_sha256 FROM tasks t
 JOIN execution_proposals e ON e.id=t.current_execution_id AND e.task_id=t.id AND e.project_id=t.project_id AND e.task_definition_revision_id=t.current_definition_revision_id
 JOIN task_definition_revisions d ON d.id=e.task_definition_revision_id AND d.task_id=t.id
 WHERE t.id=? AND t.project_id=? AND t.feature_area_id=? AND t.current_execution_id=? AND t.current_definition_revision_id=?`, b.TaskID, strict.ProjectID, b.FeatureAreaID, b.ExecutionID, b.TaskDefinitionRevisionID).Scan(&raw, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	d, err := task.DecodeDefinition(raw, hash)
	if err != nil {
		return out, false, err
	}
	if d.Version != 2 || d.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return out, false, nil
	}
	planID, successorID := continuationIDs(b.ExecutionID, strict.Action)
	var effects bool
	err = q.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM decision_resolution_receipts WHERE decision_id=? OR (actor_kind='AI_SESSION' AND actor_id=? AND request_id=?))
 OR EXISTS(SELECT 1 FROM execution_proposals WHERE id=?)
 OR EXISTS(SELECT 1 FROM task_plans WHERE id=?)
 OR EXISTS(SELECT 1 FROM execution_start_effects WHERE actor_kind='AI_SESSION' AND actor_id=? AND request_id=?)`, b.DecisionID, strict.AISessionID, first.RequestID, successorID, planID, strict.AISessionID, first.RequestID).Scan(&effects)
	if err != nil || effects {
		return out, false, err
	}
	out.Steps = append([]confirmation.Step(nil), out.Steps...)
	first.State, first.Effect = "REJECTED", "NO_EFFECT_PROVEN"
	first.Failure = confirmation.NewFailure("REQUEST_CHANGES_REQUIRES_FORMAL_REVIEW", "CORE", "RECEIPT_READBACK", "The retired continuation writer committed no business result. Request a supplemental Review and a new formal approval for this Coding definition.", confirmation.ReadCurrentTarget, nil)
	first.Code, first.Message = first.Failure.Code, first.Failure.Message
	out.Steps[0] = first
	out.State, out.CanResume = "REJECTED", false
	if out.Submission != nil {
		copy := *out.Submission
		copy.NextAction = confirmation.ReadCurrentTarget
		out.Submission = &copy
	}
	return out, true, nil
}
