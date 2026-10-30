package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/approval"
)

// DecideRuntimeApprovalForOperator preserves the original durable decision and
// delivery protocol. The exact Run check prevents a form from changing targets.
func (f *Facade) DecideRuntimeApprovalForOperator(ctx context.Context, projectID, runID, approvalID string, decision approval.Decision) (approval.Approval, error) {
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return approval.Approval{}, err
	}
	v, err := reader.GetApprovalForSession(ctx, "", approvalID)
	if err != nil {
		return approval.Approval{}, err
	}
	if v.RunID != runID {
		return approval.Approval{}, notFound()
	}
	current, _, err := reader.scopedRun(ctx, "", runID)
	if err != nil {
		return approval.Approval{}, err
	}
	if v.ThreadID == "" || v.TurnID == "" || current.ThreadID != v.ThreadID || current.TurnID != v.TurnID || (current.Status != "RUNNING" && current.Status != "WAITING_APPROVAL") {
		return approval.Approval{}, MapError(approval.ErrSessionNotLive)
	}
	runtime := current.AgentRuntime
	if runtime == nil || runtime.Availability != "AVAILABLE" || runtime.Sequence == 0 || runtime.ConnectionState != "CONNECTED" || runtime.ConnectionID == "" || runtime.RunID != runID || runtime.ProjectID != projectID || runtime.ThreadID != v.ThreadID || runtime.TurnID != v.TurnID {
		return approval.Approval{}, MapError(approval.ErrSessionNotLive)
	}
	return reader.DecideApprovalForSession(ctx, "", approvalID, decision)
}
