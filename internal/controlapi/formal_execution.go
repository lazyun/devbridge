package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
)

func formalApprovalIntent(intent confirmation.Intent) bool {
	if intent.Purpose != confirmation.Execution || intent.Action != "APPROVE_AND_START" || intent.Content.Execution == nil {
		return false
	}
	for _, v := range intent.Content.Variants {
		if (intent.Commit == nil || v.ID == intent.Commit.VariantID) && v.ImplementationMode == task.ImplementationCodingAgent && len(v.RepositoryTargets) > 0 {
			return true
		}
	}
	return false
}

func (f *Facade) formalExecutionIdentity(ctx context.Context, intent confirmation.Intent) (formalexecution.ReadIdentity, error) {
	if intent.Commit == nil {
		return formalexecution.ReadIdentity{}, formalexecution.ErrConflict
	}
	c := intent.Commit
	submission, err := f.deps.Confirmations.ReadSubmission(ctx, confirmation.ReadInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, ContentHash: intent.ContentHash, RequestID: c.RequestID})
	if err != nil {
		return formalexecution.ReadIdentity{}, err
	}
	if !submission.Recorded || submission.Outcome != confirmation.Consumed || submission.ReceiptRequestID != c.RequestID {
		return formalexecution.ReadIdentity{}, formalexecution.ErrConflict
	}
	return formalexecution.ReadIdentity{AISessionID: intent.AISessionID, IntentID: intent.IntentID, IntentVersion: intent.Version, RequestID: c.RequestID, ContentHash: intent.ContentHash, TypedPayloadHash: formalexecution.HashCommit(*c), SubmittedHash: submission.PayloadSHA256}, nil
}

// The final business archive is read before live platform or partial progress.
// Binding is deliberately a subsequent transaction and can be retried without
// replaying any of the three business transactions or creating a Run.
func (f *Facade) finishFormalExecution(ctx context.Context, intent confirmation.Intent) (formalexecution.Receipt, error) {
	var zero formalexecution.Receipt
	store, ok := f.deps.WorkflowProgression.(formalexecution.Store)
	if !ok {
		return zero, formalexecution.ErrMissing
	}
	id, err := f.formalExecutionIdentity(ctx, intent)
	if err != nil {
		return zero, err
	}
	receipt, err := store.ReadApproveAndStart(ctx, id)
	if errors.Is(err, formalexecution.ErrMissing) {
		required, e := store.ReadApproveAndStartRequired(ctx, id)
		if e != nil {
			return zero, e
		}
		if !required {
			return zero, formalexecution.ErrMissing
		}
		if intent.State == "COMPLETED" {
			return zero, formalexecution.ErrCorrupt
		}
		var platform formalexecution.PlatformReference
		_, progressErr := store.ReadApproveAndStartProgress(ctx, id)
		if errors.Is(progressErr, formalexecution.ErrMissing) {
			if f.deps.RuntimeQualification == nil || intent.Commit.BackendSelection == nil {
				return zero, formalexecution.ErrMissing
			}
			platform, err = f.deps.RuntimeQualification.ResolvePlatformReference(ctx, *intent.Commit.BackendSelection, "IMPLEMENTATION")
			if err != nil {
				return zero, err
			}
			if len(intent.Steps) != 1 || intent.Steps[0].Kind != "APPROVE" {
				return zero, formalexecution.ErrConflict
			}
			step := intent.Steps[0]
			step.State = "UNKNOWN"
			step.Effect = ""
			step.Failure = nil
			step.Code = ""
			step.Message = ""
			if _, err = f.deps.Confirmations.RecordStep(ctx, intent.AISessionID, intent.IntentID, intent.Version, step, "CONFIRMING"); err != nil {
				return zero, err
			}
		} else if progressErr != nil {
			return zero, progressErr
		}
		receipt, err = store.CommitApproveAndStart(ctx, id, platform)
	}
	if err != nil {
		return receipt, err
	}
	if f.deps.RuntimeSelections == nil {
		return receipt, runtimeselection.ErrBindingMissing
	}
	source, err := f.deps.RuntimeSelections.ReadSource(ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		return receipt, err
	}
	err = f.materializeRuntimeSource(ctx, source, intent.State == "COMPLETED")
	return receipt, err
}

func (f *Facade) materializeRuntimeSource(ctx context.Context, source runtimeselection.ApprovedSource, replayOnly bool) error {
	in := runtimeselection.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: source.Platform.LineageVersion}
	if _, err := f.deps.RuntimeSelections.ReplayBinding(ctx, in); err == nil {
		return nil
	} else if !errors.Is(err, runtimeselection.ErrBindingMissing) {
		return err
	}
	if replayOnly {
		return runtimeselection.ErrBindingMissing
	}
	reader, ok := f.deps.RuntimeSelections.(interface {
		ReadBindingLineageVersion(context.Context, string) (int, error)
	})
	if !ok {
		return runtimeplatform.ErrSourceConflict
	}
	version, err := reader.ReadBindingLineageVersion(ctx, source.SourceID)
	if err != nil {
		return err
	}
	in.ExpectedLineageVersion = version
	_, err = f.deps.RuntimeSelections.MaterializeBinding(ctx, in)
	return err
}

func (f *Facade) runFormalExecutionConfirmation(ctx context.Context, intent confirmation.Intent) (confirmation.Intent, error) {
	if intent.State == "COMPLETED" {
		store, ok := f.deps.WorkflowProgression.(formalexecution.Store)
		if !ok {
			return intent, formalexecution.ErrMissing
		}
		id, e := f.formalExecutionIdentity(ctx, intent)
		if e != nil {
			return intent, e
		}
		if _, e = store.ReadApproveAndStart(ctx, id); errors.Is(e, formalexecution.ErrMissing) {
			required, readErr := store.ReadApproveAndStartRequired(ctx, id)
			if readErr != nil {
				return intent, readErr
			}
			if !required {
				return f.confirmationAccessReadback(ctx, intent)
			}
			return intent, formalexecution.ErrCorrupt
		} else if e != nil {
			return intent, e
		}
	}
	if len(intent.Steps) != 1 || intent.Steps[0].Kind != "APPROVE" {
		return intent, formalexecution.ErrConflict
	}
	step := intent.Steps[0]
	receipt, err := f.finishFormalExecution(ctx, intent)
	if intent.State == "COMPLETED" {
		if err != nil {
			return intent, err
		}
		if step.TaskID != receipt.Archive.Fence.TaskID || step.RevisionID != receipt.Archive.RevisionID || step.ExecutionID != receipt.T2.ExecutionID || step.RunID != "" {
			return intent, formalexecution.ErrCorrupt
		}
		return f.confirmationAccessReadback(ctx, intent)
	}
	state := "COMPLETED"
	if receipt.Archive.RevisionID != "" {
		step.TaskID = receipt.Archive.Fence.TaskID
		step.RevisionID = receipt.Archive.RevisionID
		step.ExecutionID = receipt.T2.ExecutionID
	}
	if err != nil {
		state = "UNKNOWN"
		if receipt.Archive.RevisionID != "" {
			state = "PARTIAL"
		}
		// Return only identities re-read from the exact durable prefix. An
		// interrupted T1/T2 must not look like a rejected, effect-free request.
		if store, ok := f.deps.WorkflowProgression.(formalexecution.Store); ok && receipt.Archive.RevisionID == "" {
			if id, identityErr := f.formalExecutionIdentity(context.WithoutCancel(ctx), intent); identityErr == nil {
				if progress, readErr := store.ReadApproveAndStartProgress(context.WithoutCancel(ctx), id); readErr == nil {
					state = "PARTIAL"
					step.TaskID = progress.Archive.Fence.TaskID
					step.RevisionID = progress.Archive.RevisionID
					if progress.T2 != nil {
						step.ExecutionID = progress.T2.ExecutionID
					}
				}
			}
		}
		step.State = "UNKNOWN"
		step.Effect = "UNKNOWN"
		step.Failure = ConfirmationFailure(err, "FORMAL_EXECUTION")
		step.Code = step.Failure.Code
		step.Message = step.Failure.Message
	} else {
		step.State = "COMPLETED"
		step.Effect = "PREPARATION_READY"
		step.Failure = nil
		step.Code = ""
		step.Message = ""
	}
	out, saveErr := f.deps.Confirmations.RecordStep(context.WithoutCancel(ctx), intent.AISessionID, intent.IntentID, intent.Version, step, state)
	if saveErr != nil {
		return intent, saveErr
	}
	if err != nil {
		return out, err
	}
	return f.confirmationAccessReadback(ctx, out)
}
