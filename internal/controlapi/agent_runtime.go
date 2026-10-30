package controlapi

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
)

func (f *Facade) AgentRuntimeStateForSession(ctx context.Context, sessionID, runID string) (agentruntime.Snapshot, error) {
	value, _, err := f.scopedRun(ctx, sessionID, runID)
	if err != nil {
		return agentruntime.Snapshot{}, err
	}
	if value.AgentRuntime == nil {
		return agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: runID}, Availability: "UNAVAILABLE", WaitingState: "RUNNING"}, nil
	}
	return *value.AgentRuntime, nil
}
func (f *Facade) ResolveAgentRequestForSession(ctx context.Context, sessionID string, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	value, _, err := f.scopedRun(ctx, sessionID, in.RunID)
	if err != nil {
		return agentruntime.Snapshot{}, err
	}
	if value.ProjectID != in.ProjectID || value.FeatureAreaID != in.FeatureAreaID || value.TaskID != in.TaskID || value.ThreadID != in.ThreadID || value.TurnID != in.TurnID {
		return agentruntime.Snapshot{}, MapError(agentruntime.ErrStale)
	}
	if err := rejectUnknownResponse(value, in.RuntimeRequestID, in.PayloadSHA256); err != nil {
		return agentruntime.Snapshot{}, err
	}
	resolver, ok := f.deps.Runs.(interface {
		ResolveAgentRequest(context.Context, agentruntime.Resolution) (agentruntime.Snapshot, error)
	})
	if !ok {
		return agentruntime.Snapshot{}, MapError(agentruntime.ErrStale)
	}
	ctx = agentruntime.WithCanonicalCancellation(ctx, func() error {
		id := fmt.Sprintf("agent-cancel-%x", sha256.Sum256(append([]byte(in.ConnectionID), in.RequestID...)))
		_, err := f.CancelExecutionForSession(ctx, executioncancel.Input{AISessionID: sessionID, ProjectID: value.ProjectID, OwnerType: string(value.OperationKind), OwnerID: value.OperationID, RunID: value.ID, RequestID: id, Reason: "User canceled a Codex approval"})
		return err
	})
	result, err := resolver.ResolveAgentRequest(ctx, in)
	if err != nil {
		return result, MapError(err)
	}
	return result, nil
}

// AgentUserDecisionResult keeps the durable user choice distinct from backend
// delivery and canonical cancellation. A recorded answer is never user-pending.
type AgentUserDecisionResult struct {
	Decision     agentruntime.UserDecision `json:"decision"`
	Runtime      agentruntime.Snapshot     `json:"runtime"`
	Cancellation *executioncancel.Receipt  `json:"cancellation,omitempty"`
}

type userDecisionRunner interface {
	SubmitUserDecision(context.Context, agentruntime.UserDecisionInput) (agentruntime.UserDecision, error)
}

func (f *Facade) SubmitAgentUserDecisionForSession(ctx context.Context, sessionID string, in agentruntime.UserDecisionInput) (AgentUserDecisionResult, error) {
	if in.SubmittedBy != "" {
		return AgentUserDecisionResult{}, invalid("User decision actor is derived from authenticated caller scope")
	}
	value, _, err := f.scopedRun(ctx, sessionID, in.RunID)
	if err != nil {
		return AgentUserDecisionResult{}, err
	}
	in.SubmittedBy = "session:" + sessionID
	return f.submitAgentUserDecision(ctx, in, value, func(res agentruntime.Resolution) (agentruntime.Snapshot, error) {
		return f.ResolveAgentRequestForSession(ctx, sessionID, res)
	}, func(input executioncancel.Input) (executioncancel.Receipt, error) {
		input.AISessionID = sessionID
		return f.CancelExecutionForSession(ctx, input)
	})
}

func (f *Facade) SubmitAgentUserDecisionForOperator(ctx context.Context, in agentruntime.UserDecisionInput) (AgentUserDecisionResult, error) {
	if in.SubmittedBy != "" {
		return AgentUserDecisionResult{}, invalid("Operator cannot supply a decision actor")
	}
	ctx, err := localoperator.BindProject(ctx, in.ProjectID)
	if err != nil {
		return AgentUserDecisionResult{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, in.ProjectID)
	if err != nil {
		return AgentUserDecisionResult{}, err
	}
	value, _, err := reader.scopedRun(ctx, "", in.RunID)
	if err != nil {
		return AgentUserDecisionResult{}, err
	}
	actor, _ := localoperator.FromContext(ctx)
	in.SubmittedBy = "operator:" + actor.PrincipalID()
	return f.submitAgentUserDecision(ctx, in, value, func(res agentruntime.Resolution) (agentruntime.Snapshot, error) {
		return f.ResolveAgentRequestForOperator(ctx, res)
	}, func(input executioncancel.Input) (executioncancel.Receipt, error) {
		return f.CancelExecutionForOperator(ctx, in.ProjectID, in.TaskID, input)
	})
}

func (f *Facade) submitAgentUserDecision(ctx context.Context, in agentruntime.UserDecisionInput, value run.Run, respond func(agentruntime.Resolution) (agentruntime.Snapshot, error), cancel func(executioncancel.Input) (executioncancel.Receipt, error)) (AgentUserDecisionResult, error) {
	out := AgentUserDecisionResult{}
	// Adoption changes the Run epoch, but cannot rewrite a previously recorded
	// cancellation choice. Only that exact choice may cross the epoch check;
	// the canonical cancellation service still owns observation and dispatch.
	exactCancellationReplay := false
	if value.AgentRuntime != nil && (in.Action == "CANCEL_OPERATION" || in.Action == "STOP_AND_REVISE") {
		for _, q := range value.AgentRuntime.Requests {
			if q.ID == in.RuntimeRequestID && q.UserDecision != nil {
				d := q.UserDecision
				exactCancellationReplay = d.Identity == in.Identity && d.SubmissionRequestID == in.SubmissionRequestID && d.SubmittedBy == in.SubmittedBy && d.PayloadSHA256 == in.PayloadSHA256 && in.PayloadSHA256 == agentruntime.DecisionPayloadSHA256(in)
				break
			}
		}
	}
	if value.ProjectID != in.ProjectID || value.FeatureAreaID != in.FeatureAreaID || value.TaskID != in.TaskID || string(value.OperationKind) != in.OperationKind || value.OperationID != in.OperationID || value.ThreadID != in.ThreadID || value.TurnID != in.TurnID {
		return out, MapError(agentruntime.ErrStale)
	}
	if value.AgentRuntime != nil {
		for _, q := range value.AgentRuntime.Requests {
			if q.ID == in.RuntimeRequestID && q.UserDecision != nil && q.UserDecision.SubmissionRequestID == in.SubmissionRequestID && in.SubmissionRequestID != "" && (q.UserDecision.PayloadSHA256 != in.PayloadSHA256 || in.PayloadSHA256 != agentruntime.DecisionPayloadSHA256(in)) {
				return out, MapError(agentruntime.ErrRequestConflict)
			}
		}
	}
	if value.RuntimeEpoch != in.RuntimeEpoch && !exactCancellationReplay {
		return out, MapError(agentruntime.ErrStale)
	}
	if err := rejectUnknownResponse(value, in.RuntimeRequestID, in.PayloadSHA256); err != nil {
		return out, err
	}
	writer, ok := f.deps.Runs.(userDecisionRunner)
	if !ok {
		return out, MapError(agentruntime.ErrUnsupported)
	}
	decision, err := writer.SubmitUserDecision(ctx, in)
	if err != nil {
		return out, MapError(err)
	}
	out.Decision = decision
	cancellationID := fmt.Sprintf("user-decision-%x", sha256.Sum256([]byte(decision.ID)))
	cancellationReceiptID := cancellationID
	if _, operator := localoperator.FromContext(ctx); operator && decision.Action != "RESPOND" {
		bound, bindErr := localoperator.BindTask(ctx, in.ProjectID, in.TaskID, "CANCEL_EXECUTION")
		if bindErr != nil {
			return out, MapError(bindErr)
		}
		cancellationReceiptID, bindErr = localoperator.RequestID(bound, in.ProjectID, in.TaskID, "CANCEL_EXECUTION", cancellationID)
		if bindErr != nil {
			return out, MapError(bindErr)
		}
	}
	if decision.Action != "RESPOND" && f.deps.ExecutionCancellations != nil {
		receipts, readErr := f.deps.ExecutionCancellations.List(ctx, in.ProjectID, in.OperationID)
		if readErr != nil {
			return out, MapError(readErr)
		}
		for _, receipt := range receipts {
			if receipt.RunID == in.RunID && receipt.RequestID == cancellationReceiptID {
				copy := receipt
				out.Cancellation = &copy
			}
		}
	}
	current, err := f.deps.Runs.Get(ctx, in.RunID)
	if err != nil {
		return out, MapError(err)
	}
	if current.AgentRuntime == nil {
		return out, MapError(agentruntime.ErrStale)
	}
	out.Runtime = *current.AgentRuntime
	var exact *agentruntime.Request
	for i := range out.Runtime.Requests {
		if out.Runtime.Requests[i].ID == in.RuntimeRequestID {
			exact = &out.Runtime.Requests[i]
			break
		}
	}
	if exact == nil {
		return out, MapError(agentruntime.ErrStale)
	}
	// An unfinished cancellation must reach the canonical cancellation service
	// again so it can observe quiescence. The durable request ID prevents another
	// interrupt even if the request projection has already become stale.
	pendingCancellation := out.Cancellation != nil && (out.Cancellation.State == "CANCEL_REQUESTED" || out.Cancellation.State == "RECOVERY_REQUIRED")
	// Completed cancellation and response replays remain pure reads.
	if !pendingCancellation && (out.Cancellation != nil || exact.State == "UNKNOWN" || exact.State == "RESPONDING" || exact.State == "RESPONDED" || exact.State == "CANCELED" || exact.State == "STALE") {
		return out, nil
	}
	if decision.Action == "RESPOND" {
		out.Runtime, err = respond(agentruntime.Resolution{Identity: in.Identity, RuntimeRequestID: in.RuntimeRequestID, UserDecisionID: decision.ID, ExpectedState: exact.State, PayloadSHA256: decision.PayloadSHA256, RequestID: exact.RequestID, ItemID: exact.ItemID})
		if err != nil {
			if updated, readErr := f.deps.Runs.Get(ctx, in.RunID); readErr == nil && updated.AgentRuntime != nil {
				for _, r := range updated.AgentRuntime.Requests {
					if r.ID == in.RuntimeRequestID && r.UserDecision != nil && r.UserDecision.ID == decision.ID && (r.State == "RESPONDING" || r.State == "RESPONDED" || r.State == "UNKNOWN" || r.State == "USER_DECISION_RECORDED" && len(r.Resolution) > 0) {
						out.Runtime = *updated.AgentRuntime
						return out, nil
					}
				}
			}
		}
		return out, err
	}
	if decision.Action != "STOP_AND_REVISE" && decision.Action != "CANCEL_OPERATION" {
		return out, MapError(agentruntime.ErrInvalid)
	}
	receipt, err := cancel(executioncancel.Input{ProjectID: in.ProjectID, OwnerType: in.OperationKind, OwnerID: in.OperationID, RunID: in.RunID, RequestID: cancellationID, Reason: "Explicit user decision: " + decision.Action})
	out.Cancellation = &receipt
	if updated, readErr := f.deps.Runs.Get(ctx, in.RunID); readErr == nil && updated.AgentRuntime != nil {
		out.Runtime = *updated.AgentRuntime
	}
	return out, err
}

// Ordinary submissions never act as an internal, evidence-backed recovery.
func rejectUnknownResponse(value run.Run, requestID string, payloadHash ...string) error {
	if value.AgentRuntime != nil {
		for _, request := range value.AgentRuntime.Requests {
			if request.ID == requestID && request.UserDecision != nil && len(payloadHash) > 0 && payloadHash[0] != "" && payloadHash[0] != request.UserDecision.PayloadSHA256 {
				return MapError(agentruntime.ErrRequestConflict)
			}
			if request.ID == requestID && agentruntime.ResponseDisposition(request) == "UNKNOWN" {
				return MapError(agentruntime.ErrResponseDispositionUnknown)
			}
		}
	}
	return nil
}
