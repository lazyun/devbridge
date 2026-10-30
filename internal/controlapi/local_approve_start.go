package controlapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// LocalApproveAndStartInput is the single trusted browser business command.
// Target and raw request are both persisted before the approval reducer runs.
type LocalApproveAndStartInput struct {
	TaskID, FeatureAreaID, ReviewID, DecisionID, RequestID, PayloadSHA256 string
	Target                                                                *p.ActionTarget
	ExecutionInstructions                                                 *task.ExecutionInstructions
	NextRunRuntimeConfig                                                  *codexruntime.NextRunRuntimeConfig
	BackendSelection                                                      *codexconfig.BackendSelectionInput
}

type LocalApproveAndStartResult struct {
	// Command is only ever a successfully read or committed parent snapshot.
	// A later staged update must not make an older durable receipt disappear.
	Command                   wp.LocalApproveAndStart        `json:"command"`
	Approval                  wp.Resolution                  `json:"approval"`
	Start                     *executionproposal.StartResult `json:"start,omitempty"`
	HandoffApplicability      string                         `json:"handoffApplicability,omitempty"`
	HandoffInapplicableReason string                         `json:"handoffInapplicableReason,omitempty"`
	FailureCode               ErrorCode                      `json:"failureCode,omitempty"`
	FailureStage              string                         `json:"failureStage,omitempty"`
	FailureSource             string                         `json:"failureSource,omitempty"`
}

const (
	handoffApplicable    = "APPLICABLE"
	handoffNotApplicable = "NOT_APPLICABLE"
)

func localApproveAndStartRequiresConfirmationFailure() *Error {
	return &Error{Code: ErrorCode(operatorStartRequiresConfirmation), Message: "This Coding Review requires formal APPROVE_AND_START Confirmation from its authorized AI Session. The local operator command cannot create the required formal runtime source."}
}

// readLocalApproveAndStartEvidence projects facts belonging to an already
// durable parent. It performs no reducer, reservation, handoff claim, wake or
// parent write. A legacy parent without run_id may use only its immutable
// START_IMPLEMENTATION receipt; it never falls back to a Task's latest Run.
func terminalRunStatus(status run.Status) bool {
	return status == run.StatusSucceeded || status == run.StatusFailed || status == run.StatusInfrastructureFailed || status == run.StatusCanceled
}

// readLocalApproveAndStartRun resolves only the parent command's immutable
// START_IMPLEMENTATION receipt. A missing receipt is not proof that a Run was
// never reserved; callers use it only where the durable parent state itself
// proves that Run admission did not occur.
func (f *Facade) readLocalApproveAndStartRun(ctx context.Context, projectID, actorID string, command wp.LocalApproveAndStart) (string, bool, error) {
	store, ok := f.deps.WorkflowProgression.(interface {
		ReadLocalApproveAndStartRun(context.Context, string, string, string) (string, bool, error)
	})
	if !ok {
		return "", false, errors.New("local approve-and-start Run receipt reader unavailable")
	}
	return store.ReadLocalApproveAndStartRun(context.WithoutCancel(ctx), projectID, actorID, command.RequestID)
}

func (f *Facade) readLocalApproveAndStartEvidence(ctx context.Context, projectID, actorID string, command wp.LocalApproveAndStart, out *LocalApproveAndStartResult) (bool, error) {
	runID := command.RunID
	if runID == "" {
		var found bool
		var err error
		runID, found, err = f.readLocalApproveAndStartRun(ctx, projectID, actorID, command)
		if err != nil {
			out.FailureStage, out.FailureSource = "HANDOFF_EVIDENCE_READ", "LOCAL_APPROVE_AND_START_STORE"
			return false, MapError(err)
		}
		if !found {
			out.FailureStage, out.FailureSource = "HANDOFF_EVIDENCE", "LOCAL_APPROVE_AND_START_STORE"
			out.FailureCode = ErrorCode("HANDOFF_ACCEPTANCE_UNCONFIRMED")
			return false, nil
		}
	}
	reader, ok := f.deps.ExecutionProposals.(interface {
		ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error)
	})
	if !ok {
		out.FailureStage, out.FailureSource = "HANDOFF_EVIDENCE", "EXECUTION_DISPATCHER"
		out.FailureCode = ErrorCode("HANDOFF_ACCEPTANCE_UNCONFIRMED")
		return false, nil
	}
	handoff, err := reader.ReadHandoff(context.WithoutCancel(ctx), projectID, runID)
	if err != nil {
		out.FailureStage, out.FailureSource = "HANDOFF_READ", "EXECUTION_DISPATCHER"
		return false, MapError(err)
	}
	if handoff.TaskID != command.TaskID || command.ExecutionID != "" && handoff.ExecutionID != command.ExecutionID {
		out.FailureStage, out.FailureSource = "HANDOFF_IDENTITY", "EXECUTION_DISPATCHER"
		return false, MapError(wp.Reject("LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT", wp.Guidance{}))
	}
	out.Start = &executionproposal.StartResult{Run: run.Run{ID: runID, ProjectID: projectID, TaskID: handoff.TaskID, ExecutionProposalID: handoff.ExecutionID, Attempt: handoff.Attempt, Status: handoff.RunStatus}, Handoff: handoff}
	if !handoff.RecordedAccepted() && !(handoff.RecordExists && handoff.RecordedState == executionproposal.HandoffRejected) {
		out.FailureStage, out.FailureSource = "HANDOFF_EVIDENCE", "EXECUTION_DISPATCHER"
		out.FailureCode = ErrorCode("HANDOFF_ACCEPTANCE_UNCONFIRMED")
	}
	return terminalRunStatus(handoff.RunStatus), nil
}

func localApproveAndStartChildRequest(parent, step string) string {
	b, _ := json.Marshal([]string{"devbridge.local-approve-and-start.v1", parent, step})
	h := sha256.Sum256(b)
	return "local_approve_start_" + hex.EncodeToString(h[:])
}

// ReadLocalApproveAndStartForOperator is the narrow, read-only receipt route
// for a local operator's own durable command. It intentionally does not
// require a currently allowed model/runtime configuration: old receipts stay
// readable after the allowlist changes.
func (f *Facade) ReadLocalApproveAndStartForOperator(ctx context.Context, projectID string, in LocalApproveAndStartInput) (out LocalApproveAndStartResult, err error) {
	ctx, err = localoperator.BindTask(ctx, projectID, in.TaskID, "APPROVE_AND_START")
	if err != nil {
		return out, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return out, err
	}
	if in.TaskID == "" || in.FeatureAreaID == "" || in.ReviewID == "" || in.DecisionID != wp.DecisionID(in.ReviewID) || in.RequestID == "" || len(in.PayloadSHA256) != 64 || in.Target == nil || in.Target.TaskID != in.TaskID || in.Target.SubjectID != in.TaskID || in.Target.FeatureAreaID != in.FeatureAreaID || in.Target.DecisionID != in.DecisionID {
		return out, invalid("Exact local APPROVE_AND_START receipt target is required")
	}
	actor, _ := localoperator.FromContext(ctx)
	targetJSON, err := json.Marshal(in.Target)
	if err != nil {
		return out, invalid("Exact local APPROVE_AND_START receipt target is required")
	}
	parentRequest, err := localoperator.RequestID(ctx, projectID, in.TaskID, "APPROVE_AND_START", in.RequestID)
	if err != nil {
		return out, MapError(err)
	}
	store, ok := reader.deps.WorkflowProgression.(wp.LocalApproveAndStartStore)
	if !ok {
		return out, errors.New("Local approve-and-start persistence unavailable")
	}
	command, found, err := store.ReadLocalApproveAndStart(ctx, projectID, actor.PrincipalID(), parentRequest)
	if err != nil {
		out.FailureStage, out.FailureSource = "PARENT_READ", "LOCAL_APPROVE_AND_START_STORE"
		return out, MapError(err)
	}
	if !found {
		out.FailureStage, out.FailureSource = "PARENT_READ", "LOCAL_APPROVE_AND_START_STORE"
		out.FailureCode = ErrorCode("LOCAL_APPROVE_AND_START_RECEIPT_NOT_FOUND")
		return out, nil
	}
	approvalRequest := localApproveAndStartChildRequest(parentRequest, "APPROVE")
	startRequest := localApproveAndStartChildRequest(parentRequest, "START_IMPLEMENTATION")
	if command.TaskID != in.TaskID || command.FeatureAreaID != in.FeatureAreaID || command.ReviewID != in.ReviewID || command.DecisionID != in.DecisionID || command.RawRequestID != in.RequestID || command.PayloadSHA256 != in.PayloadSHA256 || command.TargetJSON != string(targetJSON) || command.ApprovalRequestID != approvalRequest || command.StartRequestID != startRequest {
		out.FailureStage, out.FailureSource = "PARENT_IDENTITY", "LOCAL_APPROVE_AND_START_STORE"
		return out, MapError(wp.Reject("LOCAL_APPROVE_AND_START_REQUEST_CONFLICT", wp.Guidance{}))
	}
	out.Command = command
	if command.State == "DIRECT_FILE_READY" {
		if command.RunID != "" {
			return out, MapError(wp.Reject("LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT", wp.Guidance{}))
		}
		_, foundRun, receiptErr := f.readLocalApproveAndStartRun(ctx, projectID, actor.PrincipalID(), command)
		if receiptErr != nil {
			out.FailureStage, out.FailureSource = "HANDOFF_EVIDENCE_READ", "LOCAL_APPROVE_AND_START_STORE"
			out.FailureCode = MapError(receiptErr).Code
			return out, nil
		}
		if foundRun {
			return out, MapError(wp.Reject("LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT", wp.Guidance{}))
		}
		out.HandoffApplicability = handoffNotApplicable
		out.HandoffInapplicableReason = "DIRECT_FILE_NO_CODEX_RUN"
		return out, nil
	}
	if command.State == "REJECTED" {
		// REJECTED is durably recorded by the approval-failure path, before
		// reservation. Still check the immutable child receipt: a discovered
		// Run would be contradictory evidence, never a reason to silently call
		// handoff inapplicable.
		if command.RunID != "" {
			return out, MapError(wp.Reject("LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT", wp.Guidance{}))
		}
		_, foundRun, receiptErr := f.readLocalApproveAndStartRun(ctx, projectID, actor.PrincipalID(), command)
		if receiptErr != nil {
			out.FailureStage, out.FailureSource = "HANDOFF_EVIDENCE_READ", "LOCAL_APPROVE_AND_START_STORE"
			out.FailureCode = MapError(receiptErr).Code
			return out, nil
		}
		if foundRun {
			return out, MapError(wp.Reject("LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT", wp.Guidance{}))
		}
		out.HandoffApplicability = handoffNotApplicable
		out.HandoffInapplicableReason = "REJECTED_BEFORE_RUN_ADMISSION"
		return out, nil
	}
	out.HandoffApplicability = handoffApplicable
	_, evidenceErr := f.readLocalApproveAndStartEvidence(ctx, projectID, actor.PrincipalID(), command, &out)
	if evidenceErr == nil {
		return out, nil
	}
	// The command above is already an authorized, exact durable snapshot.
	// Only evidence transport/storage reads may be downgraded into its result;
	// identity conflicts remain ordinary errors and expose no foreign evidence.
	if out.FailureStage == "HANDOFF_EVIDENCE_READ" || out.FailureStage == "HANDOFF_READ" {
		out.FailureCode = MapError(evidenceErr).Code
		return out, nil
	}
	return out, evidenceErr
}

// ApproveAndStartForOperator preserves the same order as the GPT confirmation
// Core: a durable parent, exact approval, then the exact result's reservation.
// It never invents a ConfirmationIntent or an AI Session.
func (f *Facade) ApproveAndStartForOperator(ctx context.Context, projectID string, in LocalApproveAndStartInput) (out LocalApproveAndStartResult, err error) {
	var lastDurableParent wp.LocalApproveAndStart
	hasDurableParent := false
	setDurableParent := func(value wp.LocalApproveAndStart) {
		lastDurableParent = value
		hasDurableParent = true
		out.Command = value
	}
	setFailure := func(stage, source string) {
		out.FailureStage = stage
		out.FailureSource = source
	}
	defer func() {
		// Once the durable parent exists, transport the exact saved phase even
		// when a later Core substep reports an error. A generic HTTP error would
		// discard the parent receipt and make PARTIAL/UNKNOWN indistinguishable.
		if err != nil && hasDurableParent {
			out.Command = lastDurableParent
			out.FailureCode = MapError(err).Code
			err = nil
		}
	}()
	ingressCtx := ctx
	ctx, err = localoperator.BindTask(ctx, projectID, in.TaskID, "APPROVE_AND_START")
	if err != nil {
		return out, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return out, err
	}
	if in.TaskID == "" || in.FeatureAreaID == "" || in.ReviewID == "" || in.DecisionID != wp.DecisionID(in.ReviewID) || in.RequestID == "" || len(in.PayloadSHA256) != 64 || in.Target == nil || in.Target.TaskID != in.TaskID || in.Target.SubjectID != in.TaskID || in.Target.FeatureAreaID != in.FeatureAreaID || in.Target.DecisionID != in.DecisionID {
		return out, invalid("Exact local APPROVE_AND_START target is required")
	}
	actor, _ := localoperator.FromContext(ctx)
	targetJSON, err := json.Marshal(in.Target)
	if err != nil {
		return out, invalid("Exact local APPROVE_AND_START target is required")
	}
	parentRequest, err := localoperator.RequestID(ctx, projectID, in.TaskID, "APPROVE_AND_START", in.RequestID)
	if err != nil {
		return out, MapError(err)
	}
	store, ok := reader.deps.WorkflowProgression.(wp.LocalApproveAndStartStore)
	if !ok {
		return out, errors.New("Local approve-and-start persistence unavailable")
	}
	ctx = taskaction.WithCommand(ctx, parentRequest)
	command, found, err := store.ReadLocalApproveAndStart(ctx, projectID, actor.PrincipalID(), parentRequest)
	if err != nil {
		setFailure("PARENT_READ", "LOCAL_APPROVE_AND_START_STORE")
		return out, MapError(err)
	}
	approvalRequest := localApproveAndStartChildRequest(parentRequest, "APPROVE")
	startRequest := localApproveAndStartChildRequest(parentRequest, "START_IMPLEMENTATION")
	codingImplementation := false
	if found {
		setDurableParent(command)
		if command.TaskID != in.TaskID || command.FeatureAreaID != in.FeatureAreaID || command.ReviewID != in.ReviewID || command.DecisionID != in.DecisionID || command.RawRequestID != in.RequestID || command.PayloadSHA256 != in.PayloadSHA256 || command.TargetJSON != string(targetJSON) || command.ApprovalRequestID != approvalRequest || command.StartRequestID != startRequest {
			setFailure("PARENT_IDENTITY", "LOCAL_APPROVE_AND_START_STORE")
			return out, MapError(wp.Reject("LOCAL_APPROVE_AND_START_REQUEST_CONFLICT", wp.Guidance{}))
		}
		codingImplementation = command.BackendSelection != nil
	} else {
		taskValue, taskErr := reader.deps.Tasks.Get(ctx, in.TaskID)
		if taskErr != nil {
			return out, MapError(taskErr)
		}
		// Before approval the Task deliberately contains only the user's intent.
		// The exact pending Review proposal, not that draft or caller input, owns
		// the implementation mode which the approval reducer will materialize.
		if taskValue.CurrentReviewID != in.ReviewID || taskValue.ProjectID != projectID || taskValue.FeatureAreaID != in.FeatureAreaID {
			return out, MapError(wp.Reject("DECISION_OWNER_CHANGED", wp.Guidance{}))
		}
		guidanceReader, ok := reader.deps.WorkflowProgression.(interface {
			ReadTask(context.Context, string, string, string) (wp.Guidance, error)
		})
		if !ok {
			return out, errors.New("Review approval facts unavailable")
		}
		guidance, readErr := guidanceReader.ReadTask(ctx, projectID, in.FeatureAreaID, in.TaskID)
		if readErr != nil {
			return out, MapError(readErr)
		}
		approval := guidance.Approval
		if approval == nil || approval.Proposal == nil || approval.DecisionID != in.DecisionID || approval.ReviewID != in.ReviewID || approval.TaskID != in.TaskID || approval.Status != "PENDING" {
			return out, MapError(wp.Reject("DECISION_NOT_PENDING", guidance))
		}
		mode := approval.Proposal.WorkSpec.ImplementationMode
		if mode != task.ImplementationCodingAgent && mode != task.ImplementationDirectFile {
			return out, MapError(wp.Reject("IMPLEMENTATION_MODE_REQUIRED", guidance))
		}
		codingImplementation = mode == task.ImplementationCodingAgent
		if codingImplementation {
			// A local command cannot manufacture the AI Session/Confirmation source
			// required by the formal runtime binding. Reject before the parent or
			// approval reducer writes anything; persisted parents replay above.
			return out, localApproveAndStartRequiresConfirmationFailure()
		}
		if !codingImplementation && (in.NextRunRuntimeConfig != nil || in.BackendSelection != nil) {
			return out, invalid("Backend and runtime selections do not apply to Direct File implementation")
		}
		var frozen codexruntime.NextRunRuntimeConfig
		var backend *codexconfig.BackendSelection
		if codingImplementation {
			frozen, err = in.NextRunRuntimeConfig.Normalize(reader.ExecutionAllowlist())
			if err != nil {
				return out, MapError(err)
			}
			selected, selectionErr := reader.resolveBackendSelection(ctx, "", projectID, in.FeatureAreaID, in.BackendSelection)
			if selectionErr != nil {
				return out, invalid(selectionErr.Error())
			}
			backend = &selected
		}
		command = wp.LocalApproveAndStart{ProjectID: projectID, TaskID: in.TaskID, FeatureAreaID: in.FeatureAreaID, ReviewID: in.ReviewID, DecisionID: in.DecisionID, ActorID: actor.PrincipalID(), RawRequestID: in.RequestID, RequestID: parentRequest, PayloadSHA256: in.PayloadSHA256, TargetJSON: string(targetJSON), ApprovalRequestID: approvalRequest, StartRequestID: startRequest, ExecutionInstructions: in.ExecutionInstructions, RuntimeConfig: frozen, BackendSelection: backend}
		stagedParent, _, beginErr := store.BeginLocalApproveAndStart(ctx, command)
		if beginErr != nil {
			setFailure("PARENT_BEGIN", "LOCAL_APPROVE_AND_START_STORE")
			return out, MapError(beginErr)
		}
		command = stagedParent
	}
	setDurableParent(command)
	if command.State == "REJECTED" || command.State == "DIRECT_FILE_READY" {
		return out, nil
	}
	if command.State == "PARTIAL" || command.State == "STARTED" {
		_, readErr := f.readLocalApproveAndStartEvidence(ctx, projectID, actor.PrincipalID(), command, &out)
		return out, readErr
	}
	if command.State == "UNKNOWN" && command.RunID != "" {
		terminal, readErr := f.readLocalApproveAndStartEvidence(ctx, projectID, actor.PrincipalID(), command, &out)
		if readErr != nil || terminal {
			return out, readErr
		}
		out.Start, out.FailureCode, out.FailureStage, out.FailureSource = nil, "", "", ""
	}
	if command.State == "UNKNOWN" {
		// A parent can have missed its run_id write after the immutable child
		// reservation committed. If that receipt already exists, this is a
		// historical observation only; never re-enter reserve/resume.
		if receiptStore, ok := reader.deps.WorkflowProgression.(interface {
			ReadLocalApproveAndStartRun(context.Context, string, string, string) (string, bool, error)
		}); ok {
			_, receiptFound, receiptErr := receiptStore.ReadLocalApproveAndStartRun(context.WithoutCancel(ctx), projectID, actor.PrincipalID(), command.RequestID)
			if receiptErr != nil {
				setFailure("HANDOFF_EVIDENCE_READ", "LOCAL_APPROVE_AND_START_STORE")
				return out, MapError(receiptErr)
			}
			if receiptFound {
				terminal, readErr := f.readLocalApproveAndStartEvidence(ctx, projectID, actor.PrincipalID(), command, &out)
				if readErr != nil || terminal {
					return out, readErr
				}
				out.Start, out.FailureCode, out.FailureStage, out.FailureSource = nil, "", "", ""
			}
		}
	}
	commandCtx := context.WithoutCancel(ctx)
	// From this point replay uses only the immutable parent payload.
	in.ExecutionInstructions = command.ExecutionInstructions
	in.NextRunRuntimeConfig = &command.RuntimeConfig
	if command.State == "PENDING_APPROVAL" {
		// The parent context remains APPROVE_AND_START. Calling the public
		// APPROVE wrapper would attempt to re-bind it as a different browser
		// command; use the shared canonical reducer directly with its durable
		// child request and the already-trusted local actor.
		resolveInput := wp.ResolveInput{TaskID: command.TaskID, FeatureAreaID: command.FeatureAreaID, ReviewID: command.ReviewID, DecisionID: command.DecisionID, Option: "APPROVE", RequestID: command.ApprovalRequestID, OperatorPrincipalID: actor.PrincipalID(), ExecutionInstructions: command.ExecutionInstructions, StartEffect: wp.StartEffectStartAuthorized, StartRequestID: command.StartRequestID}
		if codingImplementation {
			resolveInput.NextRunRuntimeConfig = &command.RuntimeConfig
			resolveInput.BackendSelection = command.BackendSelection
		}
		approval, e := reader.deps.WorkflowProgression.Resolve(commandCtx, resolveInput)
		out.Approval = approval
		if e != nil {
			mapped := MapError(e)
			var rejected *wp.Error
			isRejected := (errors.As(e, &rejected) && rejected.Status == "REJECTED") || errors.Is(e, executionproposal.ErrConflict) || errors.Is(e, decisionresolution.ErrInvalidArgument)
			saved, saveErr := store.RecordLocalApproveAndStartApprovalFailure(context.WithoutCancel(ctx), projectID, command.RequestID, string(mapped.Code), isRejected)
			if saveErr == nil {
				setDurableParent(saved)
				out.FailureCode = mapped.Code
				setFailure("APPROVAL_REDUCER", "WORKFLOW_PROGRESSION")
				return out, nil
			}
			setFailure("PARENT_APPROVAL_FAILURE_RECORD", "LOCAL_APPROVE_AND_START_STORE")
			return out, MapError(saveErr)
		}
		if approval.Task == nil {
			setFailure("APPROVAL_RESULT", "WORKFLOW_PROGRESSION")
			return out, errors.New("Local approve-and-start approval did not return its exact Task")
		}
		stagedParent, recordErr := store.RecordLocalApproveAndStartApproval(context.WithoutCancel(ctx), projectID, command.RequestID, approval.Task.CurrentDefinitionRevisionID, approval.Task.CurrentExecutionID)
		if recordErr == nil {
			command = stagedParent
			setDurableParent(command)
		}
		if recordErr != nil {
			setFailure("PARENT_APPROVAL_RECORD", "LOCAL_APPROVE_AND_START_STORE")
			return out, MapError(recordErr)
		}
		if approval.DirectFile != nil || approval.Task.WorkSpec.ImplementationMode == task.ImplementationDirectFile {
			stagedParent, outcomeErr := store.RecordLocalApproveAndStartOutcome(context.WithoutCancel(ctx), projectID, command.RequestID, "", "DIRECT_FILE_READY", "", false)
			if outcomeErr != nil {
				setFailure("PARENT_DIRECT_FILE_OUTCOME_RECORD", "LOCAL_APPROVE_AND_START_STORE")
				return out, MapError(outcomeErr)
			}
			command = stagedParent
			setDurableParent(command)
			return out, nil
		}
	}
	if command.State == "APPROVED" || command.State == "RUN_RESERVED" || command.State == "UNKNOWN" {
		if command.RevisionID == "" || command.ExecutionID == "" {
			setFailure("PARENT_APPROVED_FACTS", "LOCAL_APPROVE_AND_START_STORE")
			return out, invalid("Local approve-and-start parent lacks approved revision")
		}
		expected := wp.TaskExecutionExpectation{FeatureAreaID: command.FeatureAreaID, TaskDefinitionRevisionID: command.RevisionID, ExecutionID: command.ExecutionID, Phase: task.PhaseImplementationReady}
		startCtx, bindErr := localoperator.BindTask(context.WithoutCancel(ingressCtx), projectID, command.TaskID, "START_EXECUTION")
		if bindErr != nil {
			setFailure("EXECUTION_START_BIND", "LOCAL_OPERATOR")
			return out, MapError(bindErr)
		}
		starterStore, ok := reader.deps.WorkflowProgression.(interface {
			ReserveTaskExecutionExpectedForOperator(context.Context, string, string, string, string, string, *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error)
		})
		if !ok {
			setFailure("EXECUTION_RESERVATION", "WORKFLOW_PROGRESSION")
			return out, errors.New("Exact local execution reservation unavailable")
		}
		proposal, reserved, e := starterStore.ReserveTaskExecutionExpectedForOperator(startCtx, projectID, command.TaskID, "START_EXECUTION", actor.PrincipalID(), command.StartRequestID, &expected)
		started := executionproposal.StartResult{}
		if e == nil {
			started = executionproposal.StartResult{Proposal: proposal, Run: reserved}
			out.Start = &started
			stagedParent, recordErr := store.RecordLocalApproveAndStartReservation(context.WithoutCancel(ctx), projectID, command.RequestID, proposal.ID, reserved.ID)
			if recordErr != nil {
				setFailure("PARENT_RESERVATION_RECORD", "LOCAL_APPROVE_AND_START_STORE")
				return out, MapError(recordErr)
			}
			command = stagedParent
			setDurableParent(command)
		}
		if e != nil {
			setFailure("EXECUTION_RESERVATION", "WORKFLOW_PROGRESSION")
		}
		if e == nil {
			resumer, available := f.deps.ExecutionProposals.(interface {
				StartAuthorizedTurnForProject(context.Context, string, executionproposal.Proposal, run.Run) (executionproposal.StartResult, error)
			})
			if !available {
				setFailure("RUNTIME_HANDOFF", "EXECUTION_DISPATCHER")
				e = errors.New("reserved Execution handoff unavailable")
			} else {
				started, e = resumer.StartAuthorizedTurnForProject(startCtx, projectID, proposal, reserved)
			}
		}
		if e == nil || started.Run.ID != "" || started.Handoff.RunID != "" {
			out.Start = &started
		}
		if e != nil {
			state, code := string(started.Handoff.State), started.Handoff.Code
			if state == "" {
				state, code = "UNKNOWN", "HANDOFF_RESULT_UNKNOWN"
			} else if code == "" {
				code = "HANDOFF_" + state
			}
			saved, saveErr := store.RecordLocalApproveAndStartOutcome(context.WithoutCancel(ctx), projectID, command.RequestID, command.RunID, state, code, started.Handoff.WasAccepted())
			if saveErr == nil {
				command = saved
				setDurableParent(saved)
				out.FailureCode = MapError(e).Code
				setFailure("RUNTIME_HANDOFF", "EXECUTION_DISPATCHER")
				return out, nil
			}
			setFailure("PARENT_OUTCOME_RECORD", "LOCAL_APPROVE_AND_START_STORE")
			return out, MapError(saveErr)
		}
		outcomeCode := started.Handoff.Code
		if started.Handoff.State == executionproposal.HandoffTerminal && !started.Handoff.WasAccepted() && outcomeCode == "" {
			outcomeCode = "RUN_TERMINAL_WITHOUT_HANDOFF_ACCEPTANCE"
		}
		stagedParent, outcomeErr := store.RecordLocalApproveAndStartOutcome(context.WithoutCancel(ctx), projectID, command.RequestID, command.RunID, string(started.Handoff.State), outcomeCode, started.Handoff.WasAccepted())
		if outcomeErr != nil {
			setFailure("PARENT_OUTCOME_RECORD", "LOCAL_APPROVE_AND_START_STORE")
			return out, MapError(outcomeErr)
		}
		command = stagedParent
		setDurableParent(command)
	}
	return out, nil
}
