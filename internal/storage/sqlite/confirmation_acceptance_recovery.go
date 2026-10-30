package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// recoverConfirmationAcceptanceTx observes the exact original atomic Decision
// result. It never calls the acceptance writer, delivery, or a runtime starter,
// and current Task pointers and qualification lifecycle do not replace history.
func recoverConfirmationAcceptanceTx(ctx context.Context, q definitionQuerier, out confirmation.Intent, step *confirmation.Step) error {
	if out.Purpose != confirmation.Execution || out.Commit == nil || len(out.Steps) != 1 || step.Kind != "RESOLVE_ACCEPTANCE_DECISION" || step.RequestID != "confirmation_"+confirmation.ValueHash([]string{out.IntentID, step.Kind})[:32] {
		return ErrConflict
	}
	switch out.Action {
	case wp.AcceptanceAccept, wp.AcceptanceReject, wp.AcceptanceRequestReverification, "COMPLETE_TASK":
	default:
		return ErrConflict
	}
	strict, err := formalConfirmationTx(ctx, q, out.AISessionID, out.IntentID)
	if err != nil {
		return err
	}
	if strict.Commit == nil || strict.ContentHash != out.ContentHash || strict.Version != out.Version || confirmation.ValueHash(*strict.Commit) != confirmation.ValueHash(*out.Commit) {
		return ErrConflict
	}
	sub, err := confirmationSubmissionTx(ctx, q, strict, strict.Commit.RequestID)
	if err != nil {
		return err
	}
	if !sub.Recorded || sub.Outcome != confirmation.Consumed || sub.ReceiptRequestID != strict.Commit.RequestID {
		return sql.ErrNoRows
	}
	b := strict.Content.Execution
	if b == nil || b.Action != strict.Action || b.TaskID == "" || b.FeatureAreaID == "" || b.ExecutionID == "" || b.TaskDefinitionRevisionID == "" || b.DecisionID != wp.TaskAcceptanceDecisionID(b.ExecutionID) {
		return ErrConflict
	}
	receipt, err := scanDecisionReceipt(q.QueryRowContext(ctx, selectDecisionReceipt+` WHERE decision_id=?`, b.DecisionID))
	if err != nil {
		return err
	}
	if receipt.ProjectID != strict.ProjectID || receipt.Kind != decisionresolution.KindTaskAcceptance || receipt.SubjectKind != decisionresolution.SubjectExecution || receipt.SubjectID != b.ExecutionID || receipt.Option != func() string {
		if strict.Action == "COMPLETE_TASK" {
			return wp.AcceptanceAccept
		}
		return strict.Action
	}() || receipt.RequestID != step.RequestID || receipt.ActorKind != "AI_SESSION" || receipt.ActorID != strict.AISessionID {
		// Another actor or request's decision cannot complete this command.
		return sql.ErrNoRows
	}
	if strict.Action == "COMPLETE_TASK" {
		tx, ok := q.(*sql.Tx)
		if !ok {
			return ErrConflict
		}
		if err = validateCompletionReplay(ctx, tx, b.TaskID, step.RequestID, b.CompletionPlan); err != nil {
			return err
		}
	}
	var exactOwner bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e JOIN tasks t ON t.id=e.task_id
 WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=? AND t.project_id=e.project_id AND t.feature_area_id=?)`, b.ExecutionID, strict.ProjectID, b.TaskID, b.TaskDefinitionRevisionID, b.FeatureAreaID).Scan(&exactOwner)
	if err != nil {
		return err
	}
	if !exactOwner {
		return ErrConflict
	}
	revision, err := getDefinitionRevision(ctx, q, b.TaskDefinitionRevisionID)
	if err != nil {
		return err
	}
	if revision.TaskID != b.TaskID || revision.Definition.ProjectID != strict.ProjectID {
		return ErrConflict
	}
	executionID := ""
	if strict.Action == wp.AcceptanceRequestReverification {
		_, executionID = continuationIDs(b.ExecutionID, strict.Action)
		if receipt.ResultKind != decisionresolution.ResultExecution || receipt.ResultID != executionID {
			return ErrConflict
		}
		effect := executionStartEffectRecord{ProjectID: strict.ProjectID, ActorKind: "AI_SESSION", ActorID: strict.AISessionID, RequestID: step.RequestID, Action: strict.Action, Effect: wp.StartEffectNoStart, TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, RevisionID: b.TaskDefinitionRevisionID, ExecutionID: executionID, Phase: task.PhaseVerificationReady, DecisionID: b.DecisionID}
		hash, err := executionStartEffectPayload(effect)
		if err != nil {
			return err
		}
		var complete bool
		err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_proposals e JOIN execution_start_effects s ON s.execution_id=e.id
 WHERE e.id=? AND e.project_id=? AND e.task_id=? AND e.task_definition_revision_id=? AND e.content_actor_kind='AI_SESSION' AND e.content_actor_id=?
 AND s.project_id=e.project_id AND s.task_id=e.task_id AND s.task_definition_revision_id=e.task_definition_revision_id
 AND s.actor_kind='AI_SESSION' AND s.actor_id=? AND s.request_id=? AND s.action=? AND s.effect='NO_START' AND s.expected_phase='VERIFICATION_READY'
 AND s.feature_area_id=? AND s.decision_id=? AND s.payload_sha256=?)`, executionID, strict.ProjectID, b.TaskID, b.TaskDefinitionRevisionID, strict.AISessionID, strict.AISessionID, step.RequestID, strict.Action, b.FeatureAreaID, b.DecisionID, hash).Scan(&complete)
		if err != nil {
			return err
		}
		if !complete {
			return ErrConflict
		}
	} else if receipt.ResultKind != decisionresolution.ResultTask || receipt.ResultID != b.TaskID {
		return ErrConflict
	}
	if step.ExecutionID != "" && step.ExecutionID != executionID || step.RevisionID != "" && step.RevisionID != revision.ID || step.RunID != "" || step.WorkflowID != "" {
		return ErrConflict
	}
	step.ExecutionID, step.RevisionID = executionID, revision.ID
	step.Effect = "COMMAND_COMPLETED"
	return nil
}
