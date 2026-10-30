package consoleapi

import (
	"context"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
)

func (r *Reader) universalProductExecutions(ctx context.Context, projectID string, legacy []ProductExecutionSummary, tasks []ProductTaskSummary) ([]ProductExecutionSummary, error) {
	projections, err := r.control.ListAgentExecutions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byID := map[string]ProductExecutionSummary{}
	for _, v := range legacy {
		byID[v.ID] = v
	}
	taskIndex := map[string]int{}
	for i := range tasks {
		taskIndex[tasks[i].ID] = i
		tasks[i].Executions = []ProductExecutionSummary{}
		tasks[i].LatestExecution = nil
	}
	out := make([]ProductExecutionSummary, 0, len(projections))
	for _, p := range projections {
		summary, exists := byID[p.ExecutionID]
		title := ""
		if i, ok := taskIndex[p.TaskID]; ok {
			title = tasks[i].Title
		}
		if !exists && p.RunID != "" {
			value, err := r.control.GetRun(ctx, p.RunID)
			if err != nil {
				return nil, err
			}
			summary, err = r.makeProductExecution(ctx, value, title)
			if err != nil {
				return nil, err
			}
		}
		if summary.GoalProgress == nil {
			summary.GoalProgress = []ProductGoalProgress{}
		}
		if p.Kind == agentexecution.KindReview && p.SemanticOwnerKind == "REVIEW" {
			owner, err := r.plane.Reviews.Get(ctx, p.SemanticOwnerID)
			if err != nil {
				return nil, err
			}
			if owner.ProjectID != projectID || owner.TaskID != p.TaskID || owner.RunID != p.RunID {
				return nil, controlapi.MapError(review.ErrNotFound)
			}
			summary.ExecutorKind = owner.ExecutorKind.Effective()
			summary.ReviewActivity = workflowpresentation.PresentReviewActivity(owner, time.Now())
		}
		summary.ID = p.ExecutionID
		summary.Execution = &p
		summary.Kind = string(p.Kind)
		summary.TaskID = p.TaskID
		summary.RunID = p.RunID
		summary.Phase = p.Stage
		summary.State = p.Status
		summary.DomainStatus = p.DomainStatus
		summary.DomainResult = p.DomainResult
		summary.SemanticOwnerKind = p.SemanticOwnerKind
		summary.SemanticOwnerID = p.SemanticOwnerID
		summary.ExecutionProposalID = p.ExecutionProposalID
		summary.ProvenanceKind = string(p.ProvenanceKind)
		summary.BackendDisposition = p.BackendDisposition
		summary.UpdatedAt = p.UpdatedAt
		if summary.TaskTitle == "" {
			summary.TaskTitle = title
		}
		if p.Kind == agentexecution.KindDevelopment && !p.ReadOnly && p.RunID == "" {
			cap, cancellation, err := r.control.ExecutionCancellationPresentationForOperator(ctx, projectID, p.ExecutionID)
			if err != nil {
				cap.Available = false
				cap.Reason = "Cancellation eligibility facts are unavailable; refresh required."
			}
			summary.CancellationCapability, summary.Cancellation = &cap, cancellation
		}
		if p.ReadOnly {
			summary.CancellationCapability = nil
		}
		out = append(out, summary)
		if i, ok := taskIndex[p.TaskID]; ok {
			if p.Kind == agentexecution.KindDevelopment {
				tasks[i].Executions = append(tasks[i].Executions, summary)
			}
			if tasks[i].Work.ExecutionID == p.ExecutionID || tasks[i].Work.RunID == p.RunID && p.RunID != "" {
				if p.Kind == agentexecution.KindDevelopment {
					tasks[i].LatestExecution = &ProductExecutionRef{ID: p.ExecutionID, Kind: string(p.Kind), RunID: p.RunID, State: p.Status, Verification: summary.Verification}
				}
				tasks[i].Work.ExecutionID = p.ExecutionID
				tasks[i].Work.ExecutionProposalID = p.ExecutionProposalID
				tasks[i].Work.ExecutionKind = string(p.Kind)
				tasks[i].Work.ExecutionStatus = p.Status
				tasks[i].Work.DomainStatus = p.DomainStatus
				tasks[i].Work.DomainResult = p.DomainResult
				tasks[i].Work.CancellationState = p.CancellationState
				tasks[i].Work.BackendDisposition = p.BackendDisposition
			}
		}
	}
	return out, nil
}
