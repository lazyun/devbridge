package mcpserver

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

type ownedUIService struct {
	BridgeService
	areaStatus     featurearea.Status
	reviewClaims   int
	owner          task.Task
	current        controlapi.CurrentWorkflow
	currentErr     error
	currentCalls   int
	plans          map[string]taskplan.Plan
	executions     map[string]executionproposal.Proposal
	runs           map[string]run.Run
	review         review.Review
	approvals      []approval.Approval
	approvalFilter controlapi.ApprovalFilter
	claims         []string
}

func (s *ownedUIService) GetAgentExecutionForSession(ctx context.Context, session, id string) (agentexecution.Projection, error) {
	e, err := s.GetExecutionProposal(ctx, session, id)
	if err != nil {
		return agentexecution.Projection{}, err
	}
	var runs []run.Run
	if e.RunID != "" {
		r := s.runs[e.RunID]
		r.AgentExecutionID = e.ID
		r.FeatureAreaID = s.owner.FeatureAreaID
		r.OperationKind, r.OperationID = run.OperationExecution, e.ID
		runs = append(runs, r)
	}
	return agentexecution.Reduce(agentexecution.Execution{ID: e.ID, ExecutionProposalID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, FeatureAreaID: s.owner.FeatureAreaID, Kind: agentexecution.KindDevelopment, SemanticOwnerKind: "EXECUTION", SemanticOwnerID: e.ID, ProvenanceKind: agentexecution.ProvenanceKind(e.ProvenanceKind)}, agentexecution.Facts{Runs: runs, DomainStatus: string(e.Status), DevelopmentOutcome: string(e.Outcome), AuthorizationRequired: e.Outcome == executionproposal.OutcomePending}), nil
}

func (s *ownedUIService) GetFeatureArea(context.Context, string, string) (featurearea.FeatureArea, error) {
	status := s.areaStatus
	if status == "" {
		status = featurearea.StatusActive
	}
	return featurearea.FeatureArea{ID: s.owner.FeatureAreaID, ProjectID: s.owner.ProjectID, Status: status}, nil
}
func (s *ownedUIService) ClaimReviewContinuation(context.Context, string, string) (bool, error) {
	s.reviewClaims++
	return true, nil
}
func (s *ownedUIService) GetTask(context.Context, string, string) (task.Task, error) {
	return s.owner, nil
}
func (s *ownedUIService) CurrentTaskWorkflow(context.Context, string, string) (controlapi.CurrentWorkflow, error) {
	s.currentCalls++
	return s.current, s.currentErr
}
func (s *ownedUIService) GetTaskPlan(_ context.Context, _, id string) (taskplan.Plan, error) {
	v, ok := s.plans[id]
	if !ok {
		return v, taskplan.ErrNotFound
	}
	return v, nil
}
func (s *ownedUIService) GetExecutionProposal(_ context.Context, _, id string) (executionproposal.Proposal, error) {
	v, ok := s.executions[id]
	if !ok {
		return v, executionproposal.ErrNotFound
	}
	return v, nil
}
func (s *ownedUIService) GetRunForSession(_ context.Context, _, id string) (run.Run, error) {
	v, ok := s.runs[id]
	if !ok {
		return v, run.ErrNotFound
	}
	return v, nil
}
func (s *ownedUIService) GetReview(context.Context, string, string) (review.Review, error) {
	return s.review, nil
}
func (s *ownedUIService) ListApprovalsForSession(_ context.Context, _ string, filter controlapi.ApprovalFilter) ([]approval.Approval, error) {
	s.approvalFilter = filter
	return s.approvals, nil
}

func newOwnedUIService() *ownedUIService {
	owner := task.Task{ID: "task-a", ProjectID: "project-a", FeatureAreaID: "feature-a", Status: task.StatusActive, Phase: task.PhaseAcceptancePending, Title: "Intent"}
	plan := taskplan.Plan{ID: "plan-1", ProjectID: owner.ProjectID, TaskID: owner.ID, Status: taskplan.StatusApplied}
	execution := executionproposal.Proposal{ID: "execution-1", ProjectID: owner.ProjectID, TaskID: owner.ID, TaskDefinitionRevisionID: "revision-1", TaskPlanID: plan.ID, Status: executionproposal.StatusConsumed, Outcome: executionproposal.OutcomeSucceeded, RunID: "run-1"}
	return &ownedUIService{owner: owner, current: controlapi.CurrentWorkflow{Task: &owner, Execution: &execution}, plans: map[string]taskplan.Plan{plan.ID: plan}, executions: map[string]executionproposal.Proposal{execution.ID: execution}, runs: map[string]run.Run{
		"run-2": {ID: "run-2", TaskID: owner.ID, ProjectID: owner.ProjectID, AgentExecutionID: "other-execution", ExecutionProposalID: "other-execution", Purpose: run.PurposeImplementation},
		"run-1": {ID: "run-1", AgentExecutionID: execution.ID, ProjectID: owner.ProjectID, TaskID: owner.ID, ExecutionProposalID: execution.ID, Purpose: run.PurposeImplementation, Status: run.StatusSucceeded},
	}}
}

func TestUIWorkflowUsesCanonicalExecutionAndExactHistoricalSelectors(t *testing.T) {
	ctx := context.Background()
	service := newOwnedUIService()
	current, err := uiWorkflowState(ctx, service, uiWorkflowStateInput{AISessionID: "session-a", TaskID: service.owner.ID})
	if err != nil || current.Phase != "ACCEPTANCE_PENDING" || current.TaskPlanID != "plan-1" || current.ExecutionProposalID != "execution-1" || current.CurrentRun == nil || current.CurrentRun.ID != "run-1" {
		t.Fatalf("mixed current projection=%+v err=%v", current, err)
	}
	// A retired FeatureArea must not prevent explicit historical object reads.
	service.currentErr = featurearea.ErrUnavailable
	for _, input := range []uiWorkflowStateInput{
		{AISessionID: "session-a", TaskID: service.owner.ID, ExecutionProposalID: "execution-1"},
		{AISessionID: "session-a", TaskID: service.owner.ID, RunID: "run-1"},
	} {
		value, err := uiWorkflowState(ctx, service, input)
		if err != nil || value.Phase != string(service.owner.Phase) || value.TaskPlanID != "plan-1" || value.ExecutionProposalID != "execution-1" || value.CurrentRun == nil || value.CurrentRun.ID != "run-1" || value.Continuation != nil {
			t.Fatalf("history adopted newer definition=%+v err=%v", value, err)
		}
	}
	if service.currentCalls != 1 || len(service.claims) != 0 {
		t.Fatalf("historical read consulted unavailable current state or claimed continuation: current=%d claims=%v", service.currentCalls, service.claims)
	}
	// Selecting a plan resolves its exact immutable Task contract.
	selected, err := resolveUITaskWorkflow(ctx, service, uiWorkflowStateInput{AISessionID: "session-a", TaskID: service.owner.ID, TaskPlanID: "plan-1"}, service.owner)
	if err != nil {
		t.Fatalf("exact plan history=%+v err=%v", selected, err)
	}
	for _, input := range []uiWorkflowStateInput{
		{AISessionID: "session-a", TaskID: service.owner.ID, ExecutionProposalID: "execution-1", RunID: "run-2"},
		{AISessionID: "session-a", TaskID: service.owner.ID, TaskPlanID: "wrong-plan", RunID: "run-1"},
	} {
		if _, err := uiWorkflowState(ctx, service, input); !errors.Is(err, task.ErrNotFound) {
			t.Fatalf("mixed explicit selectors accepted: %+v err=%v", input, err)
		}
	}
}

func TestUIWorkflowKeepsExactTaskOwnedReviewApproval(t *testing.T) {
	for _, kind := range []string{"REVIEW"} {
		t.Run(kind, func(t *testing.T) {
			service := newOwnedUIService()
			input := uiWorkflowStateInput{AISessionID: "session-a", TaskID: service.owner.ID}
			current := service.runs["run-2"]
			if kind == "REVIEW" {
				current = run.Run{ID: "review-run", TaskID: service.owner.ID, ProjectID: service.owner.ProjectID, ReviewID: "review-a", Purpose: run.PurposeScopeReview}
				service.review = review.Review{ReviewDepth: "DEEP", ID: "review-a", TaskID: service.owner.ID, ProjectID: service.owner.ProjectID, FeatureAreaID: service.owner.FeatureAreaID, RunID: current.ID}
				input.TaskID = ""
				input.ReviewID = service.review.ID
				service.owner.Phase = task.PhaseReviewing
			}
			current.Status = run.StatusWaitingApproval
			service.runs[current.ID] = current
			service.approvals = []approval.Approval{{ID: "approval-a", RunID: current.ID, Kind: approval.KindCommandExecution, Status: approval.StatusPending, AllowedDecisions: []approval.DecisionType{approval.DecisionApproveOnce}}}
			value, err := uiWorkflowState(context.Background(), service, input)
			if err != nil || value.Phase != "REVIEWING" || value.WorkflowPhase != "REVIEWING" || value.WorkflowObjective != "RESOLVE_USER_DECISION" || value.Terminal || value.Approval == nil || !value.Approval.Pending || value.Approval.RunID != current.ID {
				t.Fatalf("pending approval lost=%+v err=%v", value, err)
			}
			if service.approvalFilter.RunID != current.ID || !service.approvalFilter.PendingOnly || service.approvalFilter.Limit != 1 {
				t.Fatalf("approval filter=%+v", service.approvalFilter)
			}
			if kind == "REVIEW" && (value.Task == nil || value.Task.ID != service.owner.ID) {
				t.Fatalf("Review acquired hidden Task=%+v", value)
			}
		})
	}
}

func TestUIWorkflowTerminalFeatureAreaHistoryHasNoActions(t *testing.T) {
	for _, status := range []featurearea.Status{featurearea.StatusRetired, featurearea.StatusDecomposed} {
		for _, kind := range []string{"TASK_PLAN", "EXECUTION", "REVIEW"} {
			t.Run(string(status)+"/"+kind, func(t *testing.T) {
				service := newOwnedUIService()
				service.areaStatus = status
				service.currentErr = featurearea.ErrUnavailable
				input := uiWorkflowStateInput{AISessionID: "session-a", TaskID: service.owner.ID}
				switch kind {
				case "TASK_PLAN":
					plan := service.plans["plan-1"]
					plan.Status = taskplan.StatusPendingConfirmation
					service.plans[plan.ID] = plan
					input.TaskPlanID = plan.ID
				case "EXECUTION":
					execution := service.executions["execution-1"]
					execution.Status = executionproposal.StatusPendingConfirmation
					execution.Outcome = executionproposal.OutcomePending
					execution.RunID = ""
					service.executions[execution.ID] = execution
					input.ExecutionProposalID = execution.ID
				case "REVIEW":
					service.review = review.Review{ReviewDepth: "DEEP", ID: "review-a", TaskID: service.owner.ID, ProjectID: service.owner.ProjectID, FeatureAreaID: service.owner.FeatureAreaID, RunID: "review-run"}
					service.runs["review-run"] = run.Run{ID: "review-run", TaskID: service.owner.ID, ProjectID: service.owner.ProjectID, ReviewID: service.review.ID, Purpose: run.PurposeScopeReview, Status: run.StatusSucceeded}
					input.TaskID = ""
					input.ReviewID = service.review.ID
				}
				value, err := uiWorkflowState(context.Background(), service, input)
				if err != nil || value.Terminal != (service.owner.Status != task.StatusActive) || value.WorkflowPhase != string(service.owner.Phase) || value.WorkflowObjective != "INSPECT_HISTORY" || value.TaskPlan != nil || value.ExecutionProposal != nil || value.Approval != nil || value.ScopeExpansion != nil || value.Continuation != nil || len(service.claims) != 0 || service.reviewClaims != 0 {
					t.Fatalf("terminal history offered new action=%+v err=%v prepClaims=%v reviewClaims=%d", value, err, service.claims, service.reviewClaims)
				}
				if kind == "REVIEW" && (value.Phase != string(service.owner.Phase) || value.CurrentRun == nil || value.CurrentRun.ID != "review-run") {
					t.Fatalf("historical Review facts missing=%+v", value)
				}
			})
		}
	}
}

func TestUIWorkflowLegacyUnknownObjectsStayReadOnlyInActiveFeatureArea(t *testing.T) {
	for _, kind := range []string{"EXECUTION"} {
		t.Run(kind, func(t *testing.T) {
			service := newOwnedUIService()
			input := uiWorkflowStateInput{AISessionID: "session-a", TaskID: service.owner.ID}
			{
				execution := service.executions["execution-1"]
				execution.TaskDefinitionRevisionID = ""
				execution.ProvenanceKind = "LEGACY_UNKNOWN"
				execution.Status = executionproposal.StatusPendingConfirmation
				execution.Outcome = executionproposal.OutcomePending
				execution.RunID = ""
				service.executions[execution.ID] = execution
				input.ExecutionProposalID = execution.ID
			}
			value, err := uiWorkflowState(context.Background(), service, input)
			if err != nil || value.Terminal != (service.owner.Status != task.StatusActive) || value.TaskPlan != nil || value.ExecutionProposal != nil || value.Continuation != nil || len(service.claims) != 0 {
				t.Fatalf("legacy history interpreted as current authorization=%+v err=%v claims=%v", value, err, service.claims)
			}
		})
	}
}
