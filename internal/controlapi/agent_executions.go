package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// ListAgentExecutions is a trusted project-scoped read; all clients consume the
// same Core reducer. It enumerates durable envelopes, never invents Run owners.
func (f *Facade) ListAgentExecutions(ctx context.Context, projectID string) ([]agentexecution.Projection, error) {
	out := []agentexecution.Projection{}
	if f.deps.AgentExecutions == nil {
		return out, nil
	}
	values, err := f.deps.AgentExecutions.ListByProject(ctx, projectID)
	if err != nil {
		return nil, MapError(err)
	}
	for _, e := range values {
		p, err := f.projectAgentExecution(ctx, e)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
func (f *Facade) ListAgentExecutionsForSession(ctx context.Context, sessionID string) ([]agentexecution.Projection, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return f.ListAgentExecutions(ctx, projectID)
}
func (f *Facade) projectAgentExecution(ctx context.Context, e agentexecution.Execution) (agentexecution.Projection, error) {
	facts := agentexecution.Facts{}
	var runs []run.Run
	var err error
	if reader, ok := f.deps.AgentExecutions.(interface {
		ExactRuns(context.Context, string) ([]run.Run, error)
	}); ok {
		runs, err = reader.ExactRuns(ctx, e.ID)
	} else {
		runs, err = f.deps.Runs.List(ctx)
	}
	if err != nil {
		return agentexecution.Projection{}, MapError(err)
	}
	for _, r := range runs {
		if r.AgentExecutionID == e.ID {
			facts.Runs = append(facts.Runs, r)
		}
	}
	var owner task.Task
	if string(e.ProvenanceKind) != "LEGACY_UNKNOWN" {
		owner, err = f.deps.Tasks.Get(ctx, e.TaskID)
		if err != nil {
			return agentexecution.Projection{}, MapError(err)
		}
		if owner.ProjectID != e.ProjectID || owner.FeatureAreaID != e.FeatureAreaID {
			return agentexecution.Projection{}, notFound()
		}
	}
	if reader, ok := f.deps.AgentExecutions.(interface {
		RuntimeFacts(context.Context, string) (map[string]agentexecution.RuntimeFacts, error)
	}); ok {
		facts.Runtime, err = reader.RuntimeFacts(ctx, e.ID)
		if err != nil {
			return agentexecution.Projection{}, MapError(err)
		}
	}
	if string(e.ProvenanceKind) != "LEGACY_UNKNOWN" {
		switch string(e.Kind) {
		case "REVIEW":
			v, err := f.deps.Reviews.Get(ctx, e.SemanticOwnerID)
			if err != nil {
				return agentexecution.Projection{}, MapError(err)
			}
			if v.TaskID != e.TaskID || v.ProjectID != e.ProjectID || v.FeatureAreaID != e.FeatureAreaID {
				return agentexecution.Projection{}, notFound()
			}
			facts.CurrentRunID = v.RunID
			facts.DomainStatus = v.TerminalStatus
			if facts.DomainStatus == "" {
				facts.DomainStatus = string(owner.Status)
			}
			if v.StartFailure != nil {
				facts.DomainStatus = "FAILED"
				facts.DomainResult = v.StartFailure.Code
			}
		case "DEVELOPMENT":
			reader, ok := f.deps.ExecutionProposals.(interface {
				GetForProject(context.Context, string, string) (executionproposal.Proposal, error)
			})
			if !ok {
				return agentexecution.Projection{}, errors.New("exact authorization reader unavailable")
			}
			v, err := reader.GetForProject(ctx, e.ProjectID, e.ExecutionProposalID)
			if err != nil {
				return agentexecution.Projection{}, MapError(err)
			}
			if v.TaskID != e.TaskID || v.ID != e.SemanticOwnerID {
				return agentexecution.Projection{}, notFound()
			}
			facts.CurrentRunID = v.VerificationRunID
			if facts.CurrentRunID == "" {
				facts.CurrentRunID = v.ImplementationRunID
			}
			if facts.CurrentRunID == "" {
				facts.CurrentRunID = v.RunID
			}
			facts.DomainStatus = string(v.Status)
			facts.DomainResult = string(v.Outcome)
			if v.FailureCode == "SCOPE_CONFLICT" {
				facts.DomainResult = v.FailureCode
			}
			facts.DevelopmentOutcome = string(v.Outcome)
			facts.AuthorizationRequired = v.ReviewApprovalID == "" && v.Outcome == executionproposal.OutcomePending && (v.Status == executionproposal.StatusPendingConfirmation || v.Status == executionproposal.StatusConfirmed)
			facts.VerificationRequired = v.VerificationSpec.Mode != "" && v.VerificationSpec.Mode != "NONE"
		case "INSPECTION":
			reader, ok := f.deps.ContextProbes.(interface {
				Get(context.Context, string) (contextprobe.Probe, error)
			})
			if !ok {
				return agentexecution.Projection{}, errors.New("exact inspection reader unavailable")
			}
			v, err := reader.Get(ctx, e.SemanticOwnerID)
			if err != nil {
				return agentexecution.Projection{}, MapError(err)
			}
			if v.TaskID != e.TaskID || v.ProjectID != e.ProjectID {
				return agentexecution.Projection{}, notFound()
			}
			facts.CurrentRunID = v.RunID
			facts.DomainStatus = string(v.Status)
		}
	}
	if owner.Work.RunID != "" && owner.Work.RunID == facts.CurrentRunID {
		facts.CancellationState = owner.Work.CancellationState
		facts.BackendDisposition = owner.Work.BackendDisposition
	}
	if reader, ok := f.deps.AgentExecutions.(interface {
		CancellationFacts(context.Context, string, string) (agentexecution.RuntimeFacts, error)
	}); ok && e.ProvenanceKind != agentexecution.ProvenanceLegacyUnknown {
		cancellation, err := reader.CancellationFacts(ctx, e.ID, facts.CurrentRunID)
		if err != nil {
			return agentexecution.Projection{}, MapError(err)
		}
		facts.CancellationState = cancellation.CancellationState
		// RuntimeFacts retains precedence for UNKNOWN/orphan disposition in Reduce.
		facts.BackendDisposition = cancellation.BackendDisposition
	}
	return agentexecution.Reduce(e, facts), nil
}
func terminalSucceeded(runs []run.Run, id string) bool {
	for _, r := range runs {
		if r.ID == id {
			return r.Status == run.StatusSucceeded
		}
	}
	return false
}

// EnrichTaskExecution replaces a storage Task snapshot's Execution fields with
// the shared authoritative projection. Callers must authorize the Task first;
// enrichment is a read and grants no workflow or mutation authority.
func (f *Facade) EnrichTaskExecution(ctx context.Context, v *task.Task) error {
	if f.deps.AgentExecutions == nil {
		return nil
	}
	id := v.Work.ExecutionID
	if v.Work.RunID != "" {
		r, err := f.deps.Runs.Get(ctx, v.Work.RunID)
		if err != nil {
			return MapError(err)
		}
		id = r.AgentExecutionID
	}
	if id == "" {
		return nil
	}
	e, err := f.deps.AgentExecutions.Get(ctx, id)
	if err != nil {
		return MapError(err)
	}
	if e.TaskID != v.ID || e.ProjectID != v.ProjectID {
		return notFound()
	}
	p, err := f.projectAgentExecution(ctx, e)
	if err != nil {
		return err
	}
	v.Work.ExecutionID = p.ExecutionID
	v.Work.ExecutionProposalID = p.ExecutionProposalID
	v.Work.ExecutionKind = string(p.Kind)
	v.Work.ExecutionStatus = p.Status
	v.Work.DomainStatus = p.DomainStatus
	v.Work.DomainResult = p.DomainResult
	v.Work.CancellationState = p.CancellationState
	v.Work.BackendDisposition = p.BackendDisposition
	return nil
}
func (f *Facade) enrichCurrentExecution(ctx context.Context, v *CurrentWorkflow) error {
	if f.deps.AgentExecutions == nil {
		return nil
	}
	v.AgentExecution = nil
	selected, id := "", ""
	if v.AgentOperation != nil && v.AgentOperation.Claim != nil {
		selected = v.AgentOperation.Claim.RunID
	}
	if selected == "" && v.CurrentReview != nil && presentationReviewCurrent(*v) {
		selected = v.CurrentReview.RunID
	}
	if selected == "" && v.Task != nil {
		selected = v.Task.Work.RunID
		id = v.Task.Work.ExecutionID
	}
	if selected == "" && id == "" && v.Execution != nil {
		id = v.Execution.ID
	}
	if selected == "" && id == "" && v.Task == nil && v.CurrentReview != nil {
		selected = v.CurrentReview.RunID
	}
	if selected != "" {
		r, err := f.deps.Runs.Get(ctx, selected)
		if err != nil {
			return MapError(err)
		}
		id = r.AgentExecutionID
	}
	if id == "" {
		return nil
	}
	e, err := f.deps.AgentExecutions.Get(ctx, id)
	if err != nil {
		return MapError(err)
	}
	p, err := f.projectAgentExecution(ctx, e)
	if err != nil {
		return err
	}
	v.AgentExecution = &p
	if v.Task != nil {
		return f.EnrichTaskExecution(ctx, v.Task)
	}
	return nil
}
