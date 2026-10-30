package controlapi

import (
	"context"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// ApprovedTaskStartInput selects the runtime for an exact approved definition.
// The command cannot replace its authored content or inherit a Review's model.
type ApprovedTaskStartInput struct {
	AISessionID    string                            `json:"aiSessionId"`
	TaskID         string                            `json:"taskId"`
	RequestID      string                            `json:"requestId"`
	ExpectedTarget wp.TaskExecutionExpectation       `json:"expectedTarget"`
	RuntimeConfig  codexruntime.NextRunRuntimeConfig `json:"runtimeConfig"`
	Backend        codexconfig.BackendSelectionInput `json:"backend"`
}

// StartApprovedTaskExecution composes the existing durable START_APPROVED
// confirmation. Its stable prepare and commit identities preserve the original
// selection across response loss and Core restart, including before a Run exists.
func (f *Facade) StartApprovedTaskExecution(ctx context.Context, in ApprovedTaskStartInput) (executionproposal.StartResult, error) {
	if strings.TrimSpace(in.AISessionID) == "" || strings.TrimSpace(in.TaskID) == "" || strings.TrimSpace(in.RequestID) == "" ||
		!validTaskExecutionExpectation(in.ExpectedTarget) || in.ExpectedTarget.Phase != task.PhaseImplementationReady {
		return executionproposal.StartResult{}, invalid("Explicit implementation start requires a Session, Task, requestId and exact approved target")
	}
	if _, err := f.confirmationSession(ctx, in.AISessionID); err != nil {
		return executionproposal.StartResult{}, err
	}
	// Deliberately exclude target/selection from the request key: reusing a caller
	// request with changed content must conflict, not create another intent.
	requestKey := confirmationHash([]string{"devbridge.approved-task-start.v1", in.AISessionID, in.RequestID})
	prepareRequest := "approved_task_start_prepare_" + requestKey
	submittedHash := confirmation.ValueHash(in)
	prepared, err := f.deps.Confirmations.ReplayPrepare(ctx, in.AISessionID, prepareRequest, submittedHash)
	if errors.Is(err, confirmation.ErrRequestNotObserved) {
		if result, found, startErr := f.startExistingApprovedAdoption(ctx, in); found || startErr != nil {
			return result, startErr
		}
		prepared, err = f.prepareApprovedTaskStart(ctx, in, prepareRequest, submittedHash)
	}
	if err != nil {
		// An error may accompany a partially decoded, unverified Intent. Never
		// publish IDs from it. A successful independent replay can still prove
		// the original receipt without executing a command or consulting live
		// model options.
		var result executionproposal.StartResult
		if prepared.IntentID != "" && !approvedStartIdentityDenied(err) {
			if verified, readErr := f.deps.Confirmations.ReplayPrepare(context.WithoutCancel(ctx), in.AISessionID, prepareRequest, submittedHash); readErr == nil {
				result, _ = f.readApprovedTaskStartResult(context.WithoutCancel(ctx), verified)
			}
		}
		return result, ConfirmationFailure(err, "RECEIPT_READBACK")
	}
	committed, commitErr := f.CommitConfirmation(ctx, confirmation.CommitInput{
		AISessionID: in.AISessionID, IntentID: prepared.IntentID, Version: prepared.Version,
		Purpose: confirmation.Execution, Action: "START_APPROVED", RequestID: "approved_task_start_commit_" + requestKey,
		ContentHash: prepared.ContentHash, VariantID: "start",
		Selection: confirmation.Selection{RuntimeConfig: &in.RuntimeConfig, Backend: &in.Backend},
	})
	// The immutable step supplies identity. Reading a Task's current reservation
	// here could return a later successor after this original start completed.
	verified := committed
	if commitErr != nil {
		if approvedStartIdentityDenied(commitErr) {
			return executionproposal.StartResult{}, commitErr
		}
		// prepared was successfully checked before CommitConfirmation. Keep
		// that observation if the second read fails; do not trust the possibly
		// incomplete Intent returned alongside commitErr.
		verified = prepared
		if latest, receiptErr := f.deps.Confirmations.ReplayPrepare(context.WithoutCancel(ctx), in.AISessionID, prepareRequest, submittedHash); receiptErr == nil {
			verified = latest
		}
	}
	result, readErr := f.readApprovedTaskStartResult(context.WithoutCancel(ctx), verified)
	if commitErr != nil {
		return result, commitErr
	}
	if readErr != nil {
		return result, readErr
	}
	if committed.State != "COMPLETED" {
		for _, step := range committed.Steps {
			if step.Failure != nil {
				return result, step.Failure
			}
		}
		return result, confirmation.NewFailure("APPROVED_TASK_START_UNCONFIRMED", "CORE", "RECEIPT_READBACK", "Read the original start request again; its runtime start has not been confirmed.", confirmation.ReadOriginalReceipt, nil)
	}
	if len(committed.Steps) == 1 && committed.Steps[0].Effect == "PREPARATION_READY" {
		return f.startConfirmedAdoption(ctx, committed, result)
	}
	return result, nil
}

func (f *Facade) startExistingApprovedAdoption(ctx context.Context, in ApprovedTaskStartInput) (executionproposal.StartResult, bool, error) {
	store, ok := f.deps.WorkflowProgression.(formalexecution.AdoptionStartStore)
	if !ok {
		return executionproposal.StartResult{}, false, nil
	}
	request := formalexecution.AdoptionStartRequest(in)
	intent, found, err := store.ReplayAdoptionStart(ctx, request)
	if err == nil && !found {
		intent, found, err = store.BindAdoptionStart(ctx, request)
	}
	if err != nil {
		return executionproposal.StartResult{}, found, ConfirmationFailure(err, "RECEIPT_READBACK")
	}
	if !found {
		return executionproposal.StartResult{}, false, nil
	}
	result, err := f.readApprovedTaskStartResult(ctx, intent)
	if err != nil {
		return result, true, err
	}
	result, err = f.startConfirmedAdoption(ctx, intent, result)
	return result, true, err
}

// Confirmation seals only pre-Run authority. This explicit start command is a
// separate consumer; failures here never mutate the completed confirmation.
func (f *Facade) startConfirmedAdoption(ctx context.Context, intent confirmation.Intent, result executionproposal.StartResult) (executionproposal.StartResult, error) {
	receipt, err := f.finishFormalAdoption(ctx, intent)
	if err != nil {
		return result, err
	}
	if result.Proposal.ImplementationRunID != "" {
		return f.resumeConfirmedAdoption(ctx, intent, result)
	}
	if f.deps.RuntimeSelections == nil {
		return result, runtimeselection.ErrBindingMissing
	}
	sourceID := receipt.SourceID()
	if prior := receipt.Archive.PriorRuntimeApproval; prior != nil {
		sourceID = prior.T1.Identity.SourceOriginID
	}
	source, err := f.deps.RuntimeSelections.ReadSource(ctx, sourceID)
	if err != nil {
		return result, err
	}
	if err = f.materializeRuntimeSource(ctx, source, false); err != nil {
		return result, err
	}
	proposal, reserved := result.Proposal, result.Run
	if receipt.Archive.PriorRuntimeApproval != nil {
		// APPROVE_AND_START sealed the original admission without a start
		// effect. Only this explicit consumer may record that effect, using
		// its immutable composed command and the archived exact target.
		// The original admission request and ScopePlan remain unchanged.
		store, ok := f.deps.WorkflowProgression.(interface {
			ReserveTaskExecutionExpectedCommand(context.Context, string, string, string, string, *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error)
		})
		if !ok {
			return result, errors.New("Persistent Task progression unavailable")
		}
		expected := receipt.Archive.ExpectedTarget
		proposal, reserved, err = store.ReserveTaskExecutionExpectedCommand(ctx, intent.AISessionID, receipt.Archive.TaskID, "START_EXECUTION", intent.Steps[0].RequestID, &expected)
		if err != nil {
			return result, MapError(err)
		}
		a := receipt.Archive
		if proposal.ID != a.ExecutionID || proposal.ProjectID != a.ProjectID || proposal.TaskID != a.TaskID || proposal.TaskDefinitionRevisionID != a.RevisionID || reserved.ID != "" && (reserved.ID != proposal.ImplementationRunID || reserved.ExecutionProposalID != a.ExecutionID || reserved.ProjectID != a.ProjectID || reserved.TaskID != a.TaskID || reserved.Purpose != run.PurposeImplementation) {
			return result, MapError(executionproposal.ErrConflict)
		}
		result.Proposal, result.Run = proposal, reserved
		if reserved.ID != "" {
			return f.resumeConfirmedAdoption(ctx, intent, result)
		}
	}
	proposal, reserved, err = f.prepareAdmittedTaskExecution(ctx, intent.AISessionID, proposal, reserved)
	if err != nil {
		// Preparation may commit its Run before a subsequent read fails. Recover
		// identity from the original Execution, never from the Task pointer.
		if observed, readErr := f.readApprovedTaskStartResult(context.WithoutCancel(ctx), intent); readErr == nil {
			result = observed
			if observed.Proposal.ImplementationRunID != "" {
				replay := intent
				replay.Steps = append([]confirmation.Step(nil), intent.Steps...)
				replay.Steps[0].RunID = observed.Proposal.ImplementationRunID
				if observed, readErr = f.readApprovedTaskStartResult(context.WithoutCancel(ctx), replay); readErr == nil {
					result = observed
				}
			}
		}
		return result, MapError(err)
	}
	return f.dispatchApprovedAdoption(ctx, intent, result, proposal, reserved)
}

func (f *Facade) resumeConfirmedAdoption(ctx context.Context, intent confirmation.Intent, result executionproposal.StartResult) (executionproposal.StartResult, error) {
	replay := intent
	replay.Steps = append([]confirmation.Step(nil), intent.Steps...)
	replay.Steps[0].RunID = result.Proposal.ImplementationRunID
	result, err := f.readApprovedTaskStartResult(ctx, replay)
	if err != nil {
		return result, err
	}
	if result.Handoff.RecordedAccepted() {
		return result, nil
	}
	// A Run can commit before the explicit caller dispatches it. Only the
	// still-registered current-epoch handoff may resume; accepted, terminal
	// and unknown receipts remain historical observation.
	h := result.Handoff
	if h.RecordExists && h.RecordedState == executionproposal.HandoffRegistered && h.State == executionproposal.HandoffRegistered && h.RunStatus == run.StatusCreated && h.RunEpochCurrent && h.RecordEpochMatchesRun {
		return f.dispatchApprovedAdoption(ctx, intent, result, result.Proposal, result.Run)
	}
	return result, &executionproposal.HandoffError{Handoff: h}
}

func (f *Facade) dispatchApprovedAdoption(ctx context.Context, intent confirmation.Intent, result executionproposal.StartResult, proposal executionproposal.Proposal, reserved run.Run) (executionproposal.StartResult, error) {
	starter, ok := f.deps.ExecutionProposals.(interface {
		StartAuthorizedTurn(context.Context, string, executionproposal.Proposal, run.Run) (executionproposal.StartResult, error)
	})
	if !ok {
		return result, errors.New("reserved Execution handoff unavailable")
	}
	launched, err := starter.StartAuthorizedTurn(ctx, intent.AISessionID, proposal, reserved)
	launched.Receipt = result.Receipt
	if launched.Receipt != nil {
		copy := *launched.Receipt
		copy.OriginalRunID = reserved.ID
		launched.Receipt = &copy
	}
	return launched, err
}

func (f *Facade) prepareApprovedTaskStart(ctx context.Context, in ApprovedTaskStartInput, requestID, submittedHash string) (confirmation.Intent, error) {
	projectID, err := f.sessionProject(ctx, in.AISessionID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	current, err := f.deps.Tasks.Get(ctx, in.TaskID)
	if err != nil {
		return confirmation.Intent{}, MapError(err)
	}
	if current.ProjectID != projectID || current.FeatureAreaID != in.ExpectedTarget.FeatureAreaID {
		return confirmation.Intent{}, notFound()
	}
	revision, err := f.TaskDefinitionRevisionForSession(ctx, in.AISessionID, in.ExpectedTarget.TaskDefinitionRevisionID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if revision.TaskID != in.TaskID || revision.ID != current.CurrentDefinitionRevisionID || revision.Definition.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return confirmation.Intent{}, invalid("Explicit runtime selection requires the current approved Coding Agent definition")
	}
	definition := revision.Definition
	return f.PrepareConfirmation(ctx, confirmation.PrepareInput{
		SubmittedHash: submittedHash, AISessionID: in.AISessionID, RequestID: requestID,
		Purpose: confirmation.Execution, Action: "START_APPROVED",
		Execution: &confirmation.ExecutionBody{
			Action: "START_APPROVED", TaskID: in.TaskID, FeatureAreaID: current.FeatureAreaID,
			ReviewID: current.CurrentReviewID, TaskDefinitionRevisionID: revision.ID,
			ExecutionID: in.ExpectedTarget.ExecutionID, ExpectedTarget: &in.ExpectedTarget,
		},
		Variants: []confirmation.Variant{{
			ID: "start", Label: "Start approved implementation", Summary: "Start the approved definition with the explicitly selected model and backend",
			ImplementationMode: definition.WorkSpec.ImplementationMode,
			RepositoryTargets:  definition.RepositoryTargets, PrimaryMemberID: definition.PrimaryMemberID,
			WorkSpec: &definition.WorkSpec, VerificationSpec: &definition.VerificationSpec,
		}},
	})
}

// approvedStartIdentityDenied separates failures that cannot disclose a scoped
// receipt from a child-read outage after the original identity was verified.
func approvedStartIdentityDenied(err error) bool {
	if err == nil {
		return false
	}
	var failure *Error
	if errors.As(MapError(err), &failure) {
		return failure.Code == CodeNotFound || failure.Code == CodePermissionDenied
	}
	return false
}

// intent must come from a successful canonical receipt read/commit. This narrow
// projection does not turn an unverified partial decode into durable authority.
func approvedTaskStartReceipt(intent confirmation.Intent) (*executionproposal.StartReceiptIdentity, error) {
	if intent.IntentID == "" || intent.Commit == nil {
		return nil, nil
	}
	b, c := intent.Content.Execution, intent.Commit
	if intent.Purpose != confirmation.Execution || intent.Action != "START_APPROVED" || b == nil ||
		intent.AISessionID == "" || intent.ProjectID == "" || b.TaskID == "" || b.FeatureAreaID == "" || b.TaskDefinitionRevisionID == "" ||
		c.AISessionID != intent.AISessionID || c.IntentID != intent.IntentID || c.Version != intent.Version || c.Purpose != intent.Purpose || c.Action != intent.Action || c.ContentHash != intent.ContentHash || c.RequestID == "" || len(intent.Steps) != 1 {
		return nil, confirmation.ErrRequestConflict
	}
	step := intent.Steps[0]
	if step.Kind != "START_IMPLEMENTATION" || step.RequestID != "confirmation_"+confirmationHash([]string{intent.IntentID, step.Kind})[:32] {
		return nil, confirmation.ErrRequestConflict
	}
	if step.ExecutionID == "" {
		if step.RunID != "" {
			return nil, confirmation.ErrRequestConflict
		}
		return nil, nil
	}
	if step.ProjectID != intent.ProjectID || step.TaskID != b.TaskID || step.FeatureAreaID != b.FeatureAreaID || step.RevisionID != b.TaskDefinitionRevisionID {
		return nil, confirmation.ErrRequestConflict
	}
	return &executionproposal.StartReceiptIdentity{ProjectID: intent.ProjectID, TaskID: b.TaskID, TaskDefinitionRevisionID: b.TaskDefinitionRevisionID, OriginalExecutionID: step.ExecutionID, OriginalRunID: step.RunID}, nil
}

func (f *Facade) readApprovedTaskStartResult(ctx context.Context, intent confirmation.Intent) (executionproposal.StartResult, error) {
	receipt, err := approvedTaskStartReceipt(intent)
	if err != nil {
		return executionproposal.StartResult{}, err
	}
	result := executionproposal.StartResult{Receipt: receipt}
	if receipt == nil {
		return result, nil
	}
	proposal, err := f.deps.ExecutionProposals.Get(ctx, intent.AISessionID, receipt.OriginalExecutionID)
	if err != nil {
		if approvedStartIdentityDenied(err) {
			return executionproposal.StartResult{}, MapError(err)
		}
		return result, MapError(err)
	}
	if proposal.ID != receipt.OriginalExecutionID || proposal.ProjectID != receipt.ProjectID || proposal.TaskID != receipt.TaskID || proposal.TaskDefinitionRevisionID != receipt.TaskDefinitionRevisionID || receipt.OriginalRunID != "" && proposal.ImplementationRunID != receipt.OriginalRunID {
		return executionproposal.StartResult{}, confirmation.ErrRequestConflict
	}
	result.Proposal = proposal
	if receipt.OriginalRunID == "" {
		return result, nil
	}
	reserved, err := f.deps.Runs.Get(ctx, receipt.OriginalRunID)
	if err != nil {
		if approvedStartIdentityDenied(err) {
			return executionproposal.StartResult{}, MapError(err)
		}
		return result, MapError(err)
	}
	if reserved.ProjectID != receipt.ProjectID || reserved.TaskID != receipt.TaskID || reserved.ExecutionProposalID != proposal.ID || reserved.Purpose != run.PurposeImplementation || reserved.ID != receipt.OriginalRunID {
		return executionproposal.StartResult{}, confirmation.ErrRequestConflict
	}
	result.Run = reserved
	reader, ok := f.deps.ExecutionProposals.(interface {
		ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error)
	})
	if !ok {
		return result, errors.New("Execution handoff read unavailable")
	}
	handoff, err := reader.ReadHandoff(ctx, receipt.ProjectID, reserved.ID)
	if err != nil {
		if approvedStartIdentityDenied(err) {
			return executionproposal.StartResult{}, MapError(err)
		}
		return result, err
	}
	if handoff.ExecutionID != proposal.ID || handoff.RunID != reserved.ID || handoff.TaskID != receipt.TaskID {
		return executionproposal.StartResult{}, confirmation.ErrRequestConflict
	}
	result.Handoff = handoff
	return result, nil
}
