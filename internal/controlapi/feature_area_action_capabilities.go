package controlapi

import (
	"context"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

// enrichFeatureAreaActionCapabilities adds only durable, exact action facts to
// a completed workflow projection. It is a read path: action owners still
// revalidate every target when a command is submitted.
func (f *Facade) enrichFeatureAreaActionCapabilities(ctx context.Context, sessionID string, current CurrentWorkflow, op agentconversation.State, out *presentation.Projection) error {
	if out == nil || current.Topic == nil {
		return nil
	}
	// Receipt truth is independently readable even if an older runtime lacks the
	// optional eligibility seam.
	if err := f.projectCancellationReceipt(ctx, sessionID, current, op, out); err != nil {
		return err
	}
	if err := f.projectRecoveredOperation(ctx, current, op, out); err != nil {
		return err
	}
	preWork, err := f.preWorkTaskCancellationCapability(ctx, current.Task)
	if err != nil {
		return err
	}
	if preWork != nil {
		setFeatureAreaCapability(out, preWork.Action, true, "", preWork.Target)
		if reader, ok := f.deps.AgentConversations.(agentconversation.EligibilityReader); ok {
			return enrichObservedRecoveryCapability(ctx, current, op, out, reader, true)
		}
		return nil
	}
	reader, ok := f.deps.AgentConversations.(agentconversation.EligibilityReader)
	if !ok {
		setFeatureAreaCapability(out, "CANCEL_EXECUTION", false, "Exact cancellation eligibility is unavailable.", nil)
		if out.Recovery != nil {
			setFeatureAreaCapability(out, "RECOVER", false, "Exact recovery eligibility is unavailable.", nil)
		}
		return nil
	}
	cancel, err := reader.CancelEligibility(ctx, current.Topic.ID)
	if err != nil {
		return MapError(err)
	}
	// Review cancellation already belongs to the canonical operation owner.
	// Its projection uses the same exact Run/claim facts as operation details;
	// the legacy Execution-only eligibility reader cannot advertise this path.
	if current.CurrentReview != nil && current.Task != nil && current.CurrentReview.TaskID == current.Task.ID {
		reviewCancel, e := f.reviewCancellationEligibility(ctx, sessionID, current, op)
		if e != nil {
			return e
		}
		cancel = reviewCancel
	}
	exactCancelOwner := cancel.Target != nil && ((current.Execution != nil && cancel.Target.OperationKind == agentconversation.Execution && cancel.Target.ExecutionID == current.Execution.ID) || (current.CurrentReview != nil && cancel.Target.OperationKind == agentconversation.Review && cancel.Target.OperationID == current.CurrentReview.ID))
	if cancel.Available && (!sameObservedActionTarget(current, op, cancel.Target) || !exactCancelOwner) {
		cancel = agentconversation.Eligibility{Reason: "The operation changed during projection; refresh its exact identity."}
	}
	if cancel.Available && !(current.CurrentReview != nil && current.CurrentReview.ExecutorKind == review.ExecutorUpperLLM && cancel.Target.OperationKind == agentconversation.Review) {
		live, ok := f.deps.Runner.(interface{ ExecutionCancellationAvailable(string) bool })
		if !ok || !live.ExecutionCancellationAvailable(cancel.Target.RunID) {
			cancel = agentconversation.Eligibility{Reason: "The exact Run has no live cancellation owner."}
		}
	}
	cancelTarget := presentationActionTarget(cancel.Target)
	if cancelTarget != nil && current.Task != nil {
		cancelTarget.TaskID = current.Task.ID
	}
	setFeatureAreaCapability(out, "CANCEL_EXECUTION", cancel.Available, cancel.Reason, cancelTarget)
	return enrichObservedRecoveryCapability(ctx, current, op, out, reader, false)
}

// Area recovery facts remain relevant when a different selected Task has its
// own pre-work cancellation capability. That capability must not short-circuit
// the interrupted area owner's authoritative recovery refusal.
func enrichObservedRecoveryCapability(ctx context.Context, current CurrentWorkflow, op agentconversation.State, out *presentation.Projection, reader agentconversation.EligibilityReader, workerOnly bool) error {
	if out.Recovery == nil {
		return nil
	}
	recover, err := reader.RecoveryEligibility(ctx, current.Topic.ID)
	if err != nil {
		return MapError(err)
	}
	if workerOnly && recover.ReasonCode != agentconversation.ReasonUpperWorkerContinuationUnsupported {
		return nil
	}
	if recover.Available && !sameObservedActionTarget(current, op, recover.Target) {
		recover = agentconversation.Eligibility{Reason: "The recovery target changed during projection; refresh its exact identity."}
	}
	out.Recovery.Prerequisite = recover.Prerequisite
	setFeatureAreaCapability(out, "RECOVER", recover.Available, recover.Reason, presentationActionTarget(recover.Target))
	// Required attention is not evidence that same-Run recovery is supported.
	// Preserve Core's structured worker refusal regardless of which Task is
	// selected, rather than the generic Upper reauthorization copy.
	if !recover.Available && recover.ReasonCode == agentconversation.ReasonUpperWorkerContinuationUnsupported {
		out.Recovery.Reason = recover.ReasonCode
		out.Recovery.Summary = recover.Reason
		out.Recovery.Prerequisite = ""
	}

	return nil
}

func (f *Facade) preWorkTaskCancellationCapability(ctx context.Context, value *task.Task) (*presentation.Capability, error) {
	if value == nil || value.Type != task.TypeDevelopment || value.LegacyReadOnly {
		return nil, nil
	}
	if failed, ok := f.deps.ExecutionCancellations.(interface {
		FailedUpperPreparationTaskCancellation(context.Context, string, string) (agentconversation.Eligibility, error)
	}); ok {
		available, err := failed.FailedUpperPreparationTaskCancellation(ctx, value.ProjectID, value.ID)
		if err != nil {
			return nil, MapError(err)
		}
		if available.Available && available.Target != nil {
			target := presentationActionTarget(available.Target)
			target.TaskID = value.ID
			return &presentation.Capability{Action: "CANCEL_EXECUTION", Available: true, Reason: available.Reason, Target: target}, nil
		}
	}
	reader, ok := f.deps.ExecutionCancellations.(interface {
		PreWorkTaskCancellationEligible(context.Context, string, string) (bool, error)
	})
	if !ok {
		return nil, nil
	}
	available, err := reader.PreWorkTaskCancellationEligible(ctx, value.ProjectID, value.ID)
	if err != nil {
		return nil, MapError(err)
	}
	if !available {
		return nil, nil
	}
	return &presentation.Capability{Action: "CANCEL_EXECUTION", Available: true, Target: &presentation.ActionTarget{FeatureAreaID: value.FeatureAreaID, TaskID: value.ID, OperationKind: "TASK", OperationID: value.ID}}, nil
}

func sameObservedActionTarget(current CurrentWorkflow, op agentconversation.State, target *agentconversation.ActionTarget) bool {
	// Upper has no Coding conversation. The durable executor selects this rule;
	// an empty conversation alone never changes an operation's contract.
	conversationMatches := target != nil && target.AgentConversationID == op.AgentConversationID
	if current.CurrentReview != nil && current.CurrentReview.ExecutorKind == review.ExecutorUpperLLM && target != nil && target.OperationKind == agentconversation.Review && target.OperationID == current.CurrentReview.ID {
		conversationMatches = target.AgentConversationID == ""
	}
	return target != nil && current.Topic != nil && target.FeatureAreaID == current.Topic.ID && op.Claim != nil &&
		target.OperationKind == op.Claim.Kind && target.OperationID == op.Claim.OperationID && target.RunID == op.Claim.RunID &&
		conversationMatches
}

func presentationActionTarget(value *agentconversation.ActionTarget) *presentation.ActionTarget {
	if value == nil {
		return nil
	}
	return &presentation.ActionTarget{ExpectedUpperVersion: value.ExpectedUpperVersion, FeatureAreaID: value.FeatureAreaID, DecisionID: value.DecisionID, SubjectID: value.SubjectID, ExecutionID: value.ExecutionID, OperationKind: string(value.OperationKind), OperationID: value.OperationID, RunID: value.RunID, AgentConversationID: value.AgentConversationID}
}

func setFeatureAreaCapability(out *presentation.Projection, action string, available bool, reason string, target *presentation.ActionTarget) {
	for i := range out.Capabilities {
		if out.Capabilities[i].Action == action {
			out.Capabilities[i].Available, out.Capabilities[i].Reason, out.Capabilities[i].Target = available, reason, target
			return
		}
	}
	out.Capabilities = append(out.Capabilities, presentation.Capability{Action: action, Available: available, Reason: reason, Target: target})
}

// projectCancellationReceipt preserves receipt truth: a request or unknown
// outcome is never rendered as CANCELED. Old resolved UNKNOWN receipts remain
// historical detail and do not suppress eligibility for a successor operation.
func (f *Facade) projectCancellationReceipt(ctx context.Context, sessionID string, current CurrentWorkflow, op agentconversation.State, out *presentation.Projection) error {
	if current.Execution == nil {
		return nil
	}
	receipts, err := f.ListExecutionCancellationsForSession(ctx, sessionID, current.Execution.ID)
	if err != nil {
		return err
	}
	var selected *executioncancel.Receipt
	for i := range receipts {
		receipt := &receipts[i]
		if op.Claim != nil && op.Claim.Kind == agentconversation.Execution && receipt.RunID != op.Claim.RunID {
			continue
		}
		if selected == nil || receipt.RequestedAt.After(selected.RequestedAt) {
			selected = receipt
		}
	}
	if selected == nil {
		return nil
	}
	// Explicit recovery resolves the current requirement, while the receipt's
	// original unknown outcome remains immutable in Details history.
	if selected.ResolvedAt != nil && selected.RecoveryRequired {
		return nil
	}
	state := selected.State
	if selected.RecoveryRequired || state == "RECOVERY_REQUIRED" || selected.BackendInterruptState == "OUTCOME_UNKNOWN" {
		state = "OUTCOME_UNKNOWN"
	}
	out.Cancellation = &presentation.Cancellation{State: state, RunID: selected.RunID, RecoveryRequired: selected.RecoveryRequired || state == "OUTCOME_UNKNOWN"}
	return nil
}

func (f *Facade) projectRecoveredOperation(ctx context.Context, current CurrentWorkflow, op agentconversation.State, out *presentation.Projection) error {
	if op.Status != "READY" || op.Claim != nil || op.AgentConversationID == "" || out.PendingDecision != nil || len(out.Blockers) != 0 {
		return nil
	}
	reader, ok := f.deps.AgentConversations.(agentconversation.HistoryReader)
	if !ok {
		return nil
	}
	receipts, err := reader.ListRecoveryReceipts(ctx, current.Topic.ID)
	if err != nil {
		return MapError(err)
	}
	for _, receipt := range receipts {
		if receipt.FeatureAreaID != current.Topic.ID || receipt.NewConversationID != op.AgentConversationID {
			continue
		}
		if !sameRecoveredCurrentOperation(current, out, receipt) {
			continue
		}
		out.Recovery = &presentation.Recovery{Status: "RECOVERED", Required: false, Summary: "The exact unsafe context was abandoned. Fresh work is allowed; historical operation facts and the UNKNOWN boundary remain unchanged."}
		out.WorkflowStatus = "READY"
		if receipt.CreatedAt.After(out.UpdatedAt) {
			out.UpdatedAt = receipt.CreatedAt
		}
		return nil
	}
	return nil
}

// sameRecoveredCurrentOperation prevents an old recovery receipt from changing
// a newly selected workflow. The historical terminal product status is allowed:
// it belongs to the abandoned operation and remains visible in Progress.
func sameRecoveredCurrentOperation(current CurrentWorkflow, out *presentation.Projection, receipt agentconversation.RecoveryReceipt) bool {
	switch receipt.OperationKind {
	case agentconversation.Execution:
		e := current.Execution
		if e == nil || (out.CurrentPhase != "IMPLEMENTATION" && out.CurrentPhase != "VERIFICATION") || out.Details.ExecutionID != e.ID || receipt.OperationID != e.ID {
			return false
		}
		latest := e.ImplementationRunID
		if e.VerificationRunID != "" {
			latest = e.VerificationRunID
		}
		return receipt.RunID != "" && receipt.RunID == latest
	case agentconversation.Review:
		r := current.CurrentReview
		return r != nil && out.CurrentPhase == "REVIEW" && out.Details.ReviewID == r.ID && receipt.OperationID == r.ID && receipt.RunID != "" && receipt.RunID == r.RunID
	default:
		return false
	}
}

func (f *Facade) reviewCancellationEligibility(ctx context.Context, sessionID string, current CurrentWorkflow, op agentconversation.State) (agentconversation.Eligibility, error) {
	out := agentconversation.Eligibility{Reason: "No exact active Review Run is eligible for cancellation."}
	r := current.CurrentReview
	if r != nil && r.ExecutorKind == review.ExecutorUpperLLM {
		if current.Task == nil || current.Topic == nil || r.ProjectID != current.Task.ProjectID || r.FeatureAreaID != current.Topic.ID || r.TaskID != current.Task.ID {
			out.Reason = "The Upper Review does not belong to the current Task and FeatureArea."
			return out, nil
		}
		reader, ok := f.deps.ExecutionCancellations.(interface {
			ReviewCancelEligibility(context.Context, string) (agentconversation.Eligibility, error)
		})
		if !ok {
			out.Reason = "Exact Upper Review cancellation eligibility is unavailable."
			return out, nil
		}
		out, err := reader.ReviewCancelEligibility(ctx, r.ID)
		if err != nil {
			return out, MapError(err)
		}
		if out.Available {
			available, ok := f.deps.Runner.(interface{ NoBackendCancellationAvailable() bool })
			if !ok || !available.NoBackendCancellationAvailable() {
				return agentconversation.Eligibility{Reason: "The canonical no-backend cancellation service is unavailable."}, nil
			}
		}
		return out, nil
	}
	if r != nil && r.ExecutorKind != review.ExecutorCodingAgent {
		out.Reason = "The Review executor is unknown."
		return out, nil
	}
	if r == nil || current.Task == nil || r.TaskID != current.Task.ID || op.Status != "BUSY" || op.Claim == nil || op.Claim.Kind != agentconversation.Review || op.Claim.OperationID != r.ID || op.Claim.RunID != r.RunID || r.RunID == "" {
		return out, nil
	}
	value, err := f.deps.Runs.Get(ctx, r.RunID)
	if err != nil {
		return out, MapError(err)
	}
	if value.ProjectID != r.ProjectID || value.FeatureAreaID != r.FeatureAreaID || value.TaskID != r.TaskID || value.ReviewID != r.ID || value.OperationKind != run.OperationReview || value.OperationID != r.ID || value.RuntimeEpoch == "" || value.RuntimeEpoch != op.Claim.RuntimeEpoch || value.ThreadID == "" || value.TurnID == "" || (value.Status != run.StatusRunning && value.Status != run.StatusWaitingApproval) {
		return out, nil
	}
	receipts, err := f.ListExecutionCancellationsForSession(ctx, sessionID, r.ID)
	if err != nil {
		return out, err
	}
	for _, receipt := range receipts {
		if receipt.RunID == r.RunID && (receipt.OwnerType == "" || receipt.OwnerType == "REVIEW") && receipt.ResolvedAt == nil {
			return out, nil
		}
	}
	live, ok := f.deps.Runner.(interface{ ExecutionCancellationAvailable(string) bool })
	if !ok || !live.ExecutionCancellationAvailable(value.ID) {
		return out, nil
	}
	out.Available = true
	out.Reason = ""
	out.Target = &agentconversation.ActionTarget{FeatureAreaID: r.FeatureAreaID, OperationKind: agentconversation.Review, OperationID: r.ID, RunID: r.RunID, AgentConversationID: op.AgentConversationID}
	return out, nil
}
