package consoleapi

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

// RuntimeSession is a serializable read composition. Domain authorities retain
// all decisions; this facade has no process, client, claim or dispatch owner.
type RuntimeSession struct {
	Steering               agentruntime.SteeringView    `json:"steering"`
	RecoveryStatus         *recoverystatus.Projection   `json:"recoveryStatus,omitempty"`
	Detail                 RunDetail                    `json:"detail"`
	Session                agentruntime.SessionSnapshot `json:"session"`
	TaskPhase              string                       `json:"task_phase,omitempty"`
	AgentConversationID    string                       `json:"agent_conversation_id,omitempty"`
	CancellationCapability p.Capability                 `json:"cancellation_capability"`
	Cancellation           *p.Cancellation              `json:"cancellation,omitempty"`
}

func (r *Reader) sessionRun(ctx context.Context, projectID, taskID, runID string) (run.Run, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return run.Run{}, err
	}
	if err := validID("run_id", runID); err != nil {
		return run.Run{}, err
	}
	if taskID != "" {
		if err := r.requireTaskRun(ctx, projectID, taskID, runID); err != nil {
			return run.Run{}, err
		}
	}
	v, err := r.plane.Runs.Get(ctx, runID)
	if err != nil {
		return run.Run{}, controlapi.MapError(err)
	}
	if _, err = r.ownerForRun(ctx, projectID, v); err != nil {
		return run.Run{}, err
	}
	return v, nil
}

func (r *Reader) GetRuntimeSession(ctx context.Context, projectID, taskID, runID string) (RuntimeSession, error) {
	v, err := r.sessionRun(ctx, projectID, taskID, runID)
	if err != nil {
		return RuntimeSession{}, err
	}
	out := RuntimeSession{CancellationCapability: p.Capability{Action: "CANCEL_EXECUTION", Reason: "Exact current cancellation capability unavailable"}}
	out.Detail, err = r.GetRun(ctx, projectID, runID)
	if err != nil {
		return out, err
	}
	if r.plane.Runs.AgentRuntime == nil {
		return out, errors.New("runtime session reader unavailable")
	}
	out.Session, err = r.plane.Runs.AgentRuntime.SessionSnapshot(ctx, runID)
	if err != nil {
		return out, err
	}
	out.Steering, err = r.plane.Runs.AgentRuntime.Steering(ctx, runID)
	if err != nil {
		return out, err
	}
	out.Session.Runtime = safeSessionRuntime(v, out.Session.Runtime)
	if projection, e := r.control.RecoveryStatusForSourceForOperator(ctx, projectID, runID); e == nil {
		out.RecoveryStatus = &projection
	}
	out.Session.Coverage = sessionDisplayCoverage(out.Session.Coverage, out.Session.Runtime)
	if v.TaskID != "" {
		t, e := r.plane.Tasks.Get(ctx, v.TaskID)
		if e == nil && t.ProjectID == projectID {
			out.TaskPhase = string(t.Phase)
			projection, e := r.control.TaskPresentationForOperator(ctx, projectID, v.FeatureAreaID, v.TaskID)
			if e == nil {
				if projection.Cancellation != nil && projection.Cancellation.RunID == runID {
					out.Cancellation = projection.Cancellation
				}
				for _, cap := range projection.Capabilities {
					if cap.Action == "CANCEL_EXECUTION" && cap.Target != nil && cap.Target.RunID == runID && cap.Target.OperationID == v.OperationID && cap.Target.OperationKind == string(v.OperationKind) && v.AgentExecutionID != "" {
						out.CancellationCapability = cap
					}
				}
			}
		}
	}
	if r.plane.AgentConversations != nil {
		boundary, e := r.plane.AgentConversations.Boundary(ctx, runID)
		if e == nil && boundary.RunID == runID {
			out.AgentConversationID = boundary.AgentConversationID
		} else if e != nil && !errors.Is(e, agentconversation.ErrNotFound) {
			out.Session.Coverage.Reasons = append(out.Session.Coverage.Reasons, "CONVERSATION_BOUNDARY_UNAVAILABLE")
		}
	}
	return out, nil
}

func safeSessionRuntime(v run.Run, state agentruntime.Snapshot) agentruntime.Snapshot {
	v.AgentRuntime = &state
	if safe := boundedRunRuntime(v); safe != nil {
		// Keep the original owner checks and request redaction, but do not run
		// transcript text or protocol identities through the legacy summary's
		// TrimSpace/16 KiB blanket string limit.
		safe.Identity = state.Identity
		for i := range safe.Items {
			original := state.Items[len(state.Items)-len(safe.Items)+i]
			safe.Items[i] = sessionDisplayItem(original)
		}
		if state.CurrentItem != nil {
			item := sessionDisplayItem(*state.CurrentItem)
			safe.CurrentItem = &item
		}
		for i := range safe.Requests {
			original := state.Requests[len(state.Requests)-len(safe.Requests)+i]
			target := &safe.Requests[i]
			target.Identity, target.RequestID, target.ItemID = original.Identity, original.RequestID, original.ItemID
			target.Payload, _ = approval.SafePresentationPayload(original.Payload)
			if len(original.Resolution) > 0 {
				target.Resolution, _ = approval.SafePresentationPayload(original.Resolution)
			}
			for j := range target.AvailableResponses {
				index := len(original.AvailableResponses) - len(target.AvailableResponses) + j
				target.AvailableResponses[j], _ = approval.SafePresentationPayload(original.AvailableResponses[index])
			}
		}
		var cut bool
		safe.TurnDiff, cut = sessionDisplayText(state.TurnDiff, agentruntime.MaxOutput)
		if cut {
			safe.CaptureCoverage.Reasons = append(safe.CaptureCoverage.Reasons, "DISPLAY_DIFF_TRUNCATED")
		}
		safe.CaptureCoverage = sessionDisplayCoverage(safe.CaptureCoverage, *safe)
		return *safe
	}
	return agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: v.ID}, Availability: "UNAVAILABLE", ConnectionState: "UNKNOWN"}
}

func sessionDisplayText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true
}

func sessionDisplayItem(item agentruntime.Item) agentruntime.Item {
	var textCut, outputCut, metadataCut bool
	item.Text, textCut = sessionDisplayText(item.Text, agentruntime.MaxSessionItemBytes)
	item.OutputTail, outputCut = sessionDisplayText(item.OutputTail, agentruntime.MaxOutput)
	if len(item.Metadata) > 0 {
		item.Metadata, metadataCut = approval.SafePresentationPayload(item.Metadata)
	}
	item.Truncated = item.Truncated || textCut || outputCut || metadataCut
	return item
}

func sessionDisplayCoverage(coverage agentruntime.Coverage, state agentruntime.Snapshot) agentruntime.Coverage {
	truncated := state.CurrentItem != nil && state.CurrentItem.Truncated
	for _, item := range state.Items {
		truncated = truncated || item.Truncated
	}
	for _, reason := range state.CaptureCoverage.Reasons {
		if reason == "DISPLAY_DIFF_TRUNCATED" {
			truncated = true
		}
	}
	if truncated {
		if coverage.State != "GAP" && coverage.State != "UNAVAILABLE" {
			coverage.State = "TRUNCATED"
		}
		for _, reason := range coverage.Reasons {
			if reason == "DISPLAY_CONTENT_TRUNCATED" {
				return coverage
			}
		}
		coverage.Reasons = append(coverage.Reasons, "DISPLAY_CONTENT_TRUNCATED")
	}
	return coverage
}

func (r *Reader) RuntimeSessionHistory(ctx context.Context, projectID, taskID, runID string, before uint64, limit int) (agentruntime.HistoryPage, error) {
	v, err := r.sessionRun(ctx, projectID, taskID, runID)
	if err != nil {
		return agentruntime.HistoryPage{}, err
	}
	if r.plane.Runs.AgentRuntime == nil {
		return agentruntime.HistoryPage{}, errors.New("runtime session history unavailable")
	}
	page, err := r.plane.Runs.AgentRuntime.History(ctx, runID, before, limit)
	if err != nil {
		return page, err
	}
	for i := range page.Items {
		item := page.Items[i].Item
		state := agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: runID}, Items: []agentruntime.Item{item}}
		safe := safeSessionRuntime(v, state)
		if len(safe.Items) == 1 {
			page.Items[i].Item = safe.Items[0]
			page.Coverage = sessionDisplayCoverage(page.Coverage, safe)
		} else {
			page.Items[i].Item = agentruntime.Item{ID: item.ID, Unsupported: true}
		}
	}
	return page, nil
}

func (r *Reader) RuntimeSessionUpdates(ctx context.Context, projectID, taskID, runID, generation string, cursor uint64) (agentruntime.UpdatePage, error) {
	// Re-prove Project and exact immutable owner on every subscription poll.
	v, err := r.sessionRun(ctx, projectID, taskID, runID)
	if err != nil {
		return agentruntime.UpdatePage{}, err
	}
	if r.plane.Runs.AgentRuntime == nil {
		return agentruntime.UpdatePage{}, errors.New("runtime session stream unavailable")
	}
	page, err := r.plane.Runs.AgentRuntime.Updates(ctx, runID, generation, cursor)
	if err == nil && page.Snapshot != nil {
		page.Snapshot.Runtime = safeSessionRuntime(v, page.Snapshot.Runtime)
		page.Snapshot.Coverage = sessionDisplayCoverage(page.Snapshot.Coverage, page.Snapshot.Runtime)
	}
	return page, err
}

type SessionInstruction struct {
	Source     string `json:"source"`
	Available  bool   `json:"available"`
	Text       string `json:"text"`
	Dispatched bool   `json:"dispatched"`
}

func (r *Reader) RuntimeSessionInstructions(ctx context.Context, projectID, taskID, runID string) ([]SessionInstruction, error) {
	v, err := r.sessionRun(ctx, projectID, taskID, runID)
	if err != nil {
		return nil, err
	}
	text := v.CodexInstruction
	if v.ExecutionProposalID != "" {
		e, err := r.plane.ExecutionProposals.GetForProject(ctx, projectID, v.ExecutionProposalID)
		if err != nil {
			return nil, err
		}
		switch v.Purpose {
		case run.PurposeImplementation:
			text = e.WorkSpec.CodexInstruction
		case run.PurposeVerification:
			text = e.VerificationSpec.CodexInstruction
		}
	}
	return []SessionInstruction{{Source: "FROZEN_AUTHOR_INSTRUCTION", Available: text != "", Text: text}, {Source: "RECORDED_DISPATCH_INPUT", Available: v.FinalCodexPrompt != "", Text: v.FinalCodexPrompt, Dispatched: v.FinalCodexPrompt != "" && v.PromptSentAt != nil && v.ThreadID != "" && v.TurnID != ""}}, nil
}

func (r *Reader) DecideRuntimeApproval(ctx context.Context, projectID, runID, approvalID string, decision approval.Decision) (ApprovalSummary, error) {
	v, err := r.sessionRun(ctx, projectID, "", runID)
	if err != nil {
		return ApprovalSummary{}, err
	}
	value, err := r.control.DecideRuntimeApprovalForOperator(ctx, projectID, runID, approvalID, decision)
	return makeApprovalSummary(value, v, ""), err
}
