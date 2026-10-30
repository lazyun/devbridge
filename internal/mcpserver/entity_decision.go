package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/lazyun/devbridge/internal/approval"
)

// EntityDecisionOutput is an observation of the authoritative subject, not a
// receipt for a caller request. It deliberately has no requestId, accepted,
// replay, or adapter-generated timestamp. A recorded authorization says
// nothing about successful runtime handoff or Approval delivery.
type EntityDecisionOutput struct {
	DecisionID      string `json:"decisionId"`
	SubjectState    string `json:"subjectState"`
	RecordedOption  string `json:"recordedOption,omitempty"`
	AlreadyResolved bool   `json:"alreadyResolved"`
	Conflict        bool   `json:"conflict"`
	TransitionError string `json:"transitionError,omitempty"`
}

const EntityDecisionOutputSchema = `{"type":"object","description":"Authoritative entity state only; no durable caller request binding or exact request receipt. Runtime handoff and delivery are separate facts.","properties":{"decisionId":{"type":"string"},"subjectState":{"type":"string"},"recordedOption":{"type":"string"},"alreadyResolved":{"type":"boolean"},"conflict":{"type":"boolean"},"transitionError":{"type":"string"}},"required":["decisionId","subjectState","alreadyResolved","conflict"],"additionalProperties":false}`

func approvalDecisionState(value approval.Approval, desired approval.Decision, already bool) (EntityDecisionOutput, error) {
	// Validate the payload against the immutable request even for readback.
	// In particular, a changed permissions set must not match an existing
	// GRANT_REQUESTED_PERMISSIONS merely because the option string matches.
	pending := approval.Clone(value)
	pending.Status = approval.StatusPending
	normalized, err := approval.NormalizeDecision(pending, desired)
	if err != nil {
		return EntityDecisionOutput{}, err
	}
	out := EntityDecisionOutput{DecisionID: "RUNTIME_APPROVAL:" + value.ID, SubjectState: string(value.Status), AlreadyResolved: already}
	if value.Decision != nil {
		out.RecordedOption = string(value.Decision.Type)
		out.Conflict = value.Decision.Type != normalized.Type
	} else if value.Status != approval.StatusPending {
		// Superseded/expired approvals have no human choice to attribute to
		// the caller, including when the enclosing Run was canceled.
		out.Conflict = true
	}
	return out, nil
}

// ResolveApprovalDecisionCore never redelivers a durably decided Approval.
// DECIDED, DELIVERED, and expired/superseded remain distinct on readback.
func ResolveApprovalDecisionCore(ctx context.Context, service BridgeService, sessionID, approvalID string, decision approval.Decision) (approval.Approval, EntityDecisionOutput, error) {
	if strings.TrimSpace(approvalID) == "" {
		return approval.Approval{}, EntityDecisionOutput{}, fmt.Errorf("Approval decision requires exact approvalId")
	}
	current, err := service.GetApprovalForSession(ctx, sessionID, approvalID)
	if err != nil {
		return approval.Approval{}, EntityDecisionOutput{}, err
	}
	state, err := approvalDecisionState(current, decision, current.Status != approval.StatusPending)
	if err != nil {
		return approval.Approval{}, EntityDecisionOutput{}, err
	}
	if current.Status != approval.StatusPending {
		return current, state, nil
	}
	value, err := service.DecideApprovalForSession(ctx, sessionID, approvalID, decision)
	if err != nil {
		current, readErr := service.GetApprovalForSession(context.WithoutCancel(ctx), sessionID, approvalID)
		if readErr != nil || current.Status == approval.StatusPending {
			return approval.Approval{}, EntityDecisionOutput{}, err
		}
		state, stateErr := approvalDecisionState(current, decision, true)
		if stateErr != nil {
			return approval.Approval{}, EntityDecisionOutput{}, stateErr
		}
		state.TransitionError = publicToolError(err).Error()
		return current, state, nil
	}
	state, err = approvalDecisionState(value, decision, false)
	return value, state, err
}
