package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeselection"
)

func recoverApprovalTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent, step *confirmation.Step) (bool, error) {
	if armed, err := recoverFormalExecutionApprovalTx(ctx, q, intent, step); armed || err != nil {
		return armed, err
	}
	return recoverDefinitionApprovalTx(ctx, q, intent, step)
}

// T1's approval receipt proves only the revision commit. A formal execution
// confirmation needs the exact T3 archive and its subsequent binding receipt.
// These are historical reads; current Task pointers and qualification state
// cannot replace, repair, or revoke an already committed confirmation result.
func recoverFormalExecutionApprovalTx(ctx context.Context, q definitionQuerier, intent confirmation.Intent, step *confirmation.Step) (bool, error) {
	var armed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM formal_execution_fences WHERE ai_session_id=? AND intent_id=?) OR EXISTS(SELECT 1 FROM formal_execution_t1 WHERE ai_session_id=? AND intent_id=?)`, intent.AISessionID, intent.IntentID, intent.AISessionID, intent.IntentID).Scan(&armed)
	if err != nil || !armed {
		return armed, err
	}
	if intent.Commit == nil || intent.Purpose != confirmation.Execution || intent.Action != "APPROVE_AND_START" || len(intent.Steps) != 1 || step.Kind != "APPROVE" || step.RequestID != "confirmation_"+fe.Hash([]string{intent.IntentID, "APPROVE"})[:32] {
		return true, fe.ErrConflict
	}
	id := fe.ReadIdentity{AISessionID: intent.AISessionID, IntentID: intent.IntentID, IntentVersion: intent.Version, RequestID: intent.Commit.RequestID, ContentHash: intent.ContentHash, TypedPayloadHash: fe.HashCommit(*intent.Commit)}
	var typed string
	err = q.QueryRowContext(ctx, `SELECT submitted_sha256,payload_sha256 FROM confirmation_commit_requests WHERE ai_session_id=? AND request_id=? AND intent_id=? AND outcome='CONSUMED'`, id.AISessionID, id.RequestID, id.IntentID).Scan(&id.SubmittedHash, &typed)
	if err != nil {
		return true, err
	}
	if typed != id.TypedPayloadHash {
		return true, fe.ErrCorrupt
	}
	if _, err = readFormalExecutionFenceTx(ctx, q, id); err != nil {
		return true, err
	}
	receipt, err := readFormalExecutionReceiptTx(ctx, q, id)
	if errors.Is(err, fe.ErrMissing) {
		if step.State == "COMPLETED" {
			return true, fe.ErrCorrupt
		}
		progress, readErr := readFormalExecutionProgressTx(ctx, q, id)
		if errors.Is(readErr, fe.ErrMissing) {
			return true, sql.ErrNoRows
		}
		if readErr != nil {
			return true, readErr
		}
		execution := ""
		if progress.T2 != nil {
			execution = progress.T2.ExecutionID
		}
		if err = formalApprovalStepIdentity(step, progress.Archive.Fence.TaskID, progress.Archive.RevisionID, execution); err != nil {
			return true, err
		}
		formalApprovalStepPending(step)
		return true, sql.ErrNoRows
	}
	if err != nil {
		return true, err
	}
	if err = formalApprovalStepIdentity(step, receipt.Archive.Fence.TaskID, receipt.Archive.RevisionID, receipt.T2.ExecutionID); err != nil {
		return true, err
	}
	source, err := readRuntimeSelectionSourceTx(ctx, q, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		return true, err
	}
	_, err = replayRuntimeSelectionBindingTx(ctx, q, runtimeselection.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: source.Platform.LineageVersion})
	if errors.Is(err, runtimeselection.ErrBindingMissing) && step.State != "COMPLETED" {
		formalApprovalStepPending(step)
		return true, sql.ErrNoRows
	}
	if err != nil {
		return true, err
	}
	if step.State == "COMPLETED" && step.Effect != "PREPARATION_READY" {
		return true, fe.ErrCorrupt
	}
	step.Effect = "PREPARATION_READY"
	return true, nil
}

func formalApprovalStepIdentity(step *confirmation.Step, taskID, revisionID, executionID string) error {
	if step.RunID != "" || step.WorkflowID != "" || (step.TaskID != "" && step.TaskID != taskID) || (step.RevisionID != "" && step.RevisionID != revisionID) || (step.ExecutionID != "" && step.ExecutionID != executionID) {
		return fe.ErrCorrupt
	}
	if step.State == "COMPLETED" && (step.TaskID != taskID || step.RevisionID != revisionID || step.ExecutionID != executionID) {
		return fe.ErrCorrupt
	}
	step.TaskID, step.RevisionID, step.ExecutionID = taskID, revisionID, executionID
	return nil
}

func formalApprovalStepPending(step *confirmation.Step) {
	step.State, step.Effect = "PENDING", "COMMAND_COMPLETED"
	step.Failure, step.Code, step.Message = nil, "", ""
}
