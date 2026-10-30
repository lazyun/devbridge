package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// OperatorApprovedTaskStartInput starts a v2 definition-only Coding approval.
// The browser supplies selection and observed identity, never actor/source facts.
type OperatorApprovedTaskStartInput struct {
	ProjectID, TaskID, RequestID string
	Expected                     wp.TaskExecutionExpectation
	RuntimeConfig                codexruntime.NextRunRuntimeConfig
	BackendSelection             codexconfig.BackendSelectionInput
}

func operatorApprovedStartRequest(ctx context.Context, in OperatorApprovedTaskStartInput) (context.Context, wp.OperatorExecutionStartRequest, error) {
	ctx, err := localoperator.BindTask(ctx, in.ProjectID, in.TaskID, "START_EXECUTION")
	if err != nil {
		return ctx, wp.OperatorExecutionStartRequest{}, MapError(err)
	}
	request, err := localoperator.RequestID(ctx, in.ProjectID, in.TaskID, "START_EXECUTION", in.RequestID)
	if err != nil {
		return ctx, wp.OperatorExecutionStartRequest{}, MapError(err)
	}
	return ctx, wp.OperatorExecutionStartRequest{ProjectID: in.ProjectID, TaskID: in.TaskID, RequestID: request, Expected: in.Expected, RuntimeConfig: in.RuntimeConfig, BackendSelection: in.BackendSelection}, nil
}

// ReadApprovedTaskStartForOperator reads only this exact historical command.
// It never expands current model options, reads the live Task or dispatches.
func (f *Facade) ReadApprovedTaskStartForOperator(ctx context.Context, in OperatorApprovedTaskStartInput) (executionproposal.StartResult, bool, error) {
	ctx, request, err := operatorApprovedStartRequest(ctx, in)
	if err != nil {
		return executionproposal.StartResult{}, false, err
	}
	store, ok := f.deps.WorkflowProgression.(wp.OperatorExecutionStartStore)
	if !ok {
		return executionproposal.StartResult{}, false, errors.New("Operator explicit start persistence unavailable")
	}
	v, r, found, err := store.ReadOperatorSelectedTaskExecution(ctx, request)
	if err != nil {
		return executionproposal.StartResult{}, found, MapError(err)
	}
	if !found {
		return executionproposal.StartResult{}, false, nil
	}
	out, _, err := f.operatorStartHandoff(ctx, v, r)
	return out, true, err
}

// StartApprovedTaskForOperator is independent of confirmation.Intent. Its
// durable Core command is not a runtime-platform formal issuance source.
func (f *Facade) StartApprovedTaskForOperator(ctx context.Context, in OperatorApprovedTaskStartInput) (executionproposal.StartResult, error) {
	ctx, request, err := operatorApprovedStartRequest(ctx, in)
	if err != nil {
		return executionproposal.StartResult{}, err
	}
	store, ok := f.deps.WorkflowProgression.(wp.OperatorExecutionStartStore)
	if !ok {
		return executionproposal.StartResult{}, errors.New("Operator explicit start persistence unavailable")
	}
	v, r, found, err := store.ReadOperatorSelectedTaskExecution(ctx, request)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	if found && r.ID != "" {
		out, settled, readErr := f.operatorStartHandoff(ctx, v, r)
		if readErr != nil || settled {
			return out, readErr
		}
	}
	var backend codexconfig.BackendSelection
	if !found {
		// Fresh selections are independently resolved under the actual operator
		// scope; inherited Review runtime does not participate in this choice.
		if _, err = in.RuntimeConfig.Normalize(f.ExecutionAllowlist()); err != nil {
			return executionproposal.StartResult{}, MapError(err)
		}
		backend, err = f.resolveBackendSelection(ctx, "", in.ProjectID, in.Expected.FeatureAreaID, &in.BackendSelection)
		if err != nil {
			return executionproposal.StartResult{}, invalid(err.Error())
		}
	}
	v, r, err = store.ReserveOperatorSelectedTaskExecution(ctx, request, backend)
	if err != nil {
		// An error return may contain a staged candidate from a rolled-back
		// transaction. Only an independent exact receipt read proves durability.
		return operatorStartReservationFailure(ctx, store, request, err)
	}
	out, settled, err := f.operatorStartHandoff(ctx, v, r)
	if err != nil || settled {
		return out, err
	}
	starter, ok := f.deps.ExecutionProposals.(interface {
		StartAuthorizedTurnForProject(context.Context, string, executionproposal.Proposal, run.Run) (executionproposal.StartResult, error)
	})
	if !ok {
		return out, errors.New("reserved operator Execution handoff unavailable")
	}
	started, startErr := starter.StartAuthorizedTurnForProject(ctx, in.ProjectID, v, r)
	if operatorStartDisclosureDenied(startErr) {
		return executionproposal.StartResult{}, MapError(startErr)
	}
	started.Receipt = out.Receipt
	if started.Proposal.ID == "" {
		started.Proposal = v
	}
	if started.Run.ID == "" {
		started.Run = r
	}
	return started, startErr
}

func (f *Facade) operatorStartHandoff(ctx context.Context, v executionproposal.Proposal, r run.Run) (executionproposal.StartResult, bool, error) {
	out := operatorStartReceiptResult(v, r)
	if r.ID == "" {
		return out, false, nil
	}
	reader, ok := f.deps.ExecutionProposals.(interface {
		ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error)
	})
	if !ok {
		return out, true, errors.New("operator Execution handoff reader unavailable")
	}
	h, err := reader.ReadHandoff(ctx, v.ProjectID, r.ID)
	if err != nil {
		if operatorStartDisclosureDenied(err) {
			return executionproposal.StartResult{}, true, MapError(err)
		}
		return out, true, MapError(err)
	}
	if h.RunID != r.ID || h.ExecutionID != v.ID || h.TaskID != v.TaskID {
		return executionproposal.StartResult{}, true, MapError(executionproposal.ErrConflict)
	}
	out.Handoff = h
	if h.RecordedAccepted() {
		return out, true, nil
	}
	// Only an exact current REGISTERED/CREATED receipt has not crossed the
	// dispatch boundary. UNKNOWN, rejection, stale epoch and terminal evidence
	// remain observations; none of them permits reissuing a runtime request.
	if h.RecordExists && h.RecordedState == executionproposal.HandoffRegistered && h.State == executionproposal.HandoffRegistered && h.RunStatus == run.StatusCreated && h.RunEpochCurrent && h.RecordEpochMatchesRun {
		return out, false, nil
	}
	return out, true, &executionproposal.HandoffError{Handoff: h}
}

// Call only with a successful exact Store read/reservation. These IDs describe
// durable command progress independently of a later handoff read or dispatch.
func operatorStartReceiptResult(v executionproposal.Proposal, r run.Run) executionproposal.StartResult {
	out := executionproposal.StartResult{Proposal: v, Run: r}
	if v.ID == "" || v.ProjectID == "" || v.TaskID == "" || v.TaskDefinitionRevisionID == "" {
		return out
	}
	out.Receipt = &executionproposal.StartReceiptIdentity{ProjectID: v.ProjectID, TaskID: v.TaskID, TaskDefinitionRevisionID: v.TaskDefinitionRevisionID, OriginalExecutionID: v.ID}
	if r.ID != "" && r.ProjectID == v.ProjectID && r.TaskID == v.TaskID && r.ExecutionProposalID == v.ID {
		out.Receipt.OriginalRunID = r.ID
	}
	return out
}

func operatorStartReservationFailure(ctx context.Context, store wp.OperatorExecutionStartStore, request wp.OperatorExecutionStartRequest, failure error) (executionproposal.StartResult, error) {
	if operatorStartDisclosureDenied(failure) {
		return executionproposal.StartResult{}, MapError(failure)
	}
	v, r, found, readErr := store.ReadOperatorSelectedTaskExecution(context.WithoutCancel(ctx), request)
	if readErr != nil || !found {
		return executionproposal.StartResult{}, MapError(failure)
	}
	return operatorStartReceiptResult(v, r), MapError(failure)
}

func operatorStartDisclosureDenied(err error) bool {
	if err == nil {
		return false
	}
	code := MapError(err).Code
	return code == CodePermissionDenied || code == CodeReviewScopeForbidden || code == CodeNotFound
}
