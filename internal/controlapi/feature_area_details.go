package controlapi

import (
	"context"
	"errors"
	"sort"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	"github.com/lazyun/devbridge/internal/workspace"
)

// FeatureAreaDetailsForSession composes durable, project-scoped read APIs for
// one FeatureArea. It deliberately does not use CurrentWorkflow: history must
// not be collapsed to a current operation or cause lazy materialization.
func (f *Facade) FeatureAreaDetailsForSession(ctx context.Context, sessionID, areaID string, options ListOptions) (presentation.DetailsPage, error) {
	if err := validID("featureAreaId", areaID); err != nil {
		return presentation.DetailsPage{}, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return presentation.DetailsPage{}, err
	}
	area, err := f.GetFeatureAreaForSession(ctx, sessionID, areaID)
	if err != nil {
		return presentation.DetailsPage{}, err
	}
	contextValue, err := f.DevelopmentContextPresentationForSession(ctx, sessionID)
	if err != nil {
		return presentation.DetailsPage{}, err
	}
	out := presentation.DetailsPage{SchemaVersion: "1", FeatureArea: area, DevelopmentContext: contextValue, Reviews: []review.Review{}, Tasks: []task.Task{}, TaskPlans: []taskplan.Plan{}, TaskDefinitionRevisions: []task.DefinitionRevision{}, Executions: []executionproposal.Proposal{}, Runs: []run.Run{}, Workspaces: []workspace.Workspace{}, Approvals: []approval.Approval{}, TaskDecisionReceipts: []decisionresolution.Receipt{}, CancellationReceipts: []executioncancel.Receipt{}, RecoveryConversations: []agentconversation.Conversation{}, RecoveryReceipts: []agentconversation.RecoveryReceipt{}, ScopeExpansions: []scopeexpansion.Expansion{}, SectionTotals: map[string]int{}, SectionHasMore: map[string]bool{}, Limit: limit, Offset: offset, HasPrevious: offset > 0}
	reviews, err := f.ListReviewsForFeatureArea(ctx, sessionID, areaID)
	if err != nil {
		return out, err
	}
	allAgentExecutions, err := f.ListAgentExecutions(ctx, area.ProjectID)
	if err != nil {
		return out, err
	}
	for _, e := range allAgentExecutions {
		if e.FeatureAreaID == areaID {
			out.AgentExecutions = append(out.AgentExecutions, e)
		}
	}
	var areaTasks []task.Task
	if err := scanDetailsPages(func(scanOffset int) (int, error) {
		tasks, readErr := f.ListTasks(ctx, TaskFilter{ProjectID: area.ProjectID, ListOptions: ListOptions{Limit: MaxListLimit, Offset: scanOffset}})
		if readErr == nil {
			for _, value := range tasks {
				if value.FeatureAreaID == areaID {
					areaTasks = append(areaTasks, value)
				}
			}
		}
		return len(tasks), readErr
	}); err != nil {
		return out, err
	}
	taskIDs := make(map[string]struct{}, len(areaTasks))
	for _, value := range areaTasks {
		taskIDs[value.ID] = struct{}{}
	}
	var plans []taskplan.Plan
	if err := scanDetailsPages(func(scanOffset int) (int, error) {
		values, readErr := f.ListTaskPlans(ctx, sessionID, ListOptions{Limit: MaxListLimit, Offset: scanOffset})
		if readErr == nil {
			for _, value := range values {
				if _, ok := taskIDs[value.TaskID]; ok {
					plans = append(plans, value)
				}
			}
		}
		return len(values), readErr
	}); err != nil {
		return out, err
	}
	var executions []executionproposal.Proposal
	if reader, ok := f.deps.ExecutionProposals.(interface {
		HistoryForTask(context.Context, string) ([]executionproposal.Proposal, error)
	}); ok {
		for _, taskValue := range areaTasks {
			values, readErr := reader.HistoryForTask(ctx, taskValue.ID)
			if readErr != nil {
				return out, MapError(readErr)
			}
			executions = append(executions, values...)
		}
	} else if err := scanDetailsPages(func(scanOffset int) (int, error) {
		values, readErr := f.ListExecutionProposals(ctx, sessionID, ListOptions{Limit: MaxListLimit, Offset: scanOffset})
		if readErr == nil {
			for _, value := range values {
				if _, ok := taskIDs[value.TaskID]; ok {
					executions = append(executions, value)
				}
			}
		}
		return len(values), readErr
	}); err != nil {
		return out, err
	}
	var runs []run.Run
	if err := scanDetailsPages(func(scanOffset int) (int, error) {
		values, readErr := f.ListRunsForSession(ctx, sessionID, RunFilter{ListOptions: ListOptions{Limit: MaxListLimit, Offset: scanOffset}})
		if readErr == nil {
			for _, value := range values {
				if value.FeatureAreaID == areaID {
					runs = append(runs, value)
					continue
				}
				if _, ok := taskIDs[value.TaskID]; ok {
					runs = append(runs, value)
				}
			}
		}
		return len(values), readErr
	}); err != nil {
		return out, err
	}
	for index, value := range runs {
		exact, readErr := f.GetRunForSession(ctx, sessionID, value.ID)
		if readErr != nil {
			return out, readErr
		}
		runs[index] = exact
	}
	executionByID := make(map[string]executionproposal.Proposal, len(executions))
	for _, value := range executions {
		executionByID[value.ID] = value
	}
	var workspaces []workspace.Workspace
	seenWorkspaces := make(map[string]struct{})
	for _, runValue := range runs {
		if runValue.WorkspaceID == "" {
			continue
		}
		if _, ok := seenWorkspaces[runValue.WorkspaceID]; ok {
			continue
		}
		if f.deps.Workspaces == nil {
			return out, errors.New("workspace history unavailable")
		}
		workspaceValue, readErr := f.deps.Workspaces.Get(ctx, runValue.WorkspaceID)
		if errors.Is(readErr, workspace.ErrNotFound) {
			continue
		}
		if readErr != nil {
			return out, MapError(readErr)
		}
		if !detailsWorkspaceMatchesRun(workspaceValue, runValue, executionByID) {
			return out, notFound()
		}
		seenWorkspaces[workspaceValue.ID] = struct{}{}
		workspaces = append(workspaces, workspaceValue)
	}
	runIDs := make(map[string]struct{}, len(runs))
	for _, value := range runs {
		runIDs[value.ID] = struct{}{}
	}
	var approvals []approval.Approval
	if err := scanDetailsPages(func(scanOffset int) (int, error) {
		values, readErr := f.ListApprovalsForSession(ctx, sessionID, ApprovalFilter{ListOptions: ListOptions{Limit: MaxListLimit, Offset: scanOffset}})
		if readErr == nil {
			for _, value := range values {
				if _, ok := runIDs[value.RunID]; ok {
					approvals = append(approvals, value)
				}
			}
		}
		return len(values), readErr
	}); err != nil {
		return out, err
	}
	var cancellationReceipts []executioncancel.Receipt
	for _, value := range executions {
		receipts, readErr := f.ListExecutionCancellationsForSession(ctx, sessionID, value.ID)
		if readErr != nil {
			return out, readErr
		}
		cancellationReceipts = append(cancellationReceipts, receipts...)
	}
	var recoveryConversations []agentconversation.Conversation
	var recoveryReceipts []agentconversation.RecoveryReceipt
	if reader, ok := f.deps.AgentConversations.(agentconversation.HistoryReader); ok {
		conversations, readErr := reader.ListByFeatureArea(ctx, areaID)
		if readErr != nil {
			return out, MapError(readErr)
		}
		for _, conversation := range conversations {
			if conversation.ProjectID != area.ProjectID || conversation.FeatureAreaID != areaID {
				return out, notFound()
			}
		}
		recoveryConversations = conversations
		receipts, readErr := reader.ListRecoveryReceipts(ctx, areaID)
		if readErr != nil {
			return out, MapError(readErr)
		}
		for _, receipt := range receipts {
			if receipt.FeatureAreaID != areaID {
				return out, notFound()
			}
		}
		recoveryReceipts = receipts
	}
	revisionIDs := make(map[string]struct{})
	for _, value := range areaTasks {
		if value.CurrentDefinitionRevisionID != "" {
			revisionIDs[value.CurrentDefinitionRevisionID] = struct{}{}
		}
	}
	for _, value := range executions {
		if value.TaskDefinitionRevisionID != "" {
			revisionIDs[value.TaskDefinitionRevisionID] = struct{}{}
		}
	}
	var revisions []task.DefinitionRevision
	if len(revisionIDs) != 0 {
		reader, ok := f.deps.Tasks.(interface {
			GetDefinitionRevision(context.Context, string) (task.DefinitionRevision, error)
		})
		if !ok {
			return out, errors.New("definition lookup unavailable")
		}
		for revisionID := range revisionIDs {
			revision, readErr := reader.GetDefinitionRevision(ctx, revisionID)
			if errors.Is(readErr, task.ErrNotFound) {
				continue
			}
			if readErr != nil {
				return out, MapError(readErr)
			}
			if _, ok := taskIDs[revision.TaskID]; !ok || revision.Definition.ProjectID != area.ProjectID {
				return out, notFound()
			}
			revisions = append(revisions, revision)
		}
		sort.Slice(revisions, func(i, j int) bool {
			if revisions[i].CreatedAt.Equal(revisions[j].CreatedAt) {
				return revisions[i].ID < revisions[j].ID
			}
			return revisions[i].CreatedAt.After(revisions[j].CreatedAt)
		})
	}
	var taskDecisionReceipts []decisionresolution.Receipt
	for _, value := range plans {
		receipt, readErr := f.GetDecisionResolution(ctx, sessionID, decisionresolution.TaskDecisionID(value.ID))
		if readErr == nil {
			taskDecisionReceipts = append(taskDecisionReceipts, receipt)
		} else if !errors.Is(readErr, decisionresolution.ErrNotFound) {
			return out, readErr
		}
	}
	var expansions []scopeexpansion.Expansion
	if err := scanDetailsPages(func(scanOffset int) (int, error) {
		values, readErr := f.ListScopeExpansions(ctx, sessionID, ListOptions{Limit: MaxListLimit, Offset: scanOffset})
		if readErr == nil {
			for _, value := range values {
				if value.FeatureAreaID == areaID {
					expansions = append(expansions, value)
				}
			}
		}
		return len(values), readErr
	}); err != nil {
		return out, err
	}
	sections := []struct {
		name   string
		length int
	}{
		{"reviews", len(reviews)}, {"tasks", len(areaTasks)}, {"taskPlans", len(plans)}, {"taskDefinitionRevisions", len(revisions)},
		{"executions", len(executions)}, {"agentExecutions", len(out.AgentExecutions)}, {"runs", len(runs)}, {"workspaces", len(workspaces)}, {"approvals", len(approvals)},
		{"taskDecisionReceipts", len(taskDecisionReceipts)}, {"cancellationReceipts", len(cancellationReceipts)},
		{"recoveryConversations", len(recoveryConversations)}, {"recoveryReceipts", len(recoveryReceipts)}, {"scopeExpansions", len(expansions)},
	}
	for _, section := range sections {
		out.SectionTotals[section.name] = section.length
		out.SectionHasMore[section.name] = section.length > offset+limit
		if out.SectionHasMore[section.name] {
			out.HasNext = true
		}
	}
	out.AgentExecutions = page(out.AgentExecutions, offset, limit)
	out.Reviews = page(reviews, offset, limit)
	out.Tasks = page(areaTasks, offset, limit)
	out.TaskPlans = page(plans, offset, limit)
	out.TaskDefinitionRevisions = page(revisions, offset, limit)
	out.Executions = page(executions, offset, limit)
	out.Runs = page(runs, offset, limit)
	out.Workspaces = page(workspaces, offset, limit)
	out.Approvals = page(approvals, offset, limit)
	out.TaskDecisionReceipts = page(taskDecisionReceipts, offset, limit)
	out.CancellationReceipts = page(cancellationReceipts, offset, limit)
	out.RecoveryConversations = page(recoveryConversations, offset, limit)
	out.RecoveryReceipts = page(recoveryReceipts, offset, limit)
	out.ScopeExpansions = page(expansions, offset, limit)
	return out, nil
}

func detailsWorkspaceMatchesRun(value workspace.Workspace, owner run.Run, executions map[string]executionproposal.Proposal) bool {
	if value.ID == "" || value.ID != owner.WorkspaceID {
		return false
	}
	return value.ProjectID == owner.ProjectID && value.FeatureAreaID == owner.FeatureAreaID

}

func scanDetailsPages(read func(int) (int, error)) error {
	for offset := 0; ; offset += MaxListLimit {
		count, err := read(offset)
		if err != nil {
			return err
		}
		if count < MaxListLimit {
			return nil
		}
	}
}
