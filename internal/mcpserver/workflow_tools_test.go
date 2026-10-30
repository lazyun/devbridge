package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type workflowToolService struct {
	*mcpserver.StaticService
	now                  time.Time
	cancelCalls          int
	proposalCreateCalls  int
	executionStarts      int
	executed             bool
	approvalDecided      bool
	approvalDecision     *approval.Decision
	approvalResumed      bool
	approvalPayload      json.RawMessage
	continuationClaimed  bool
	reconstructPlan      bool
	reconstructExecution bool
	reconstructScope     bool
	decisionReceipt      *decisionresolution.Receipt
}

type terminalWorkflowToolService struct{ *workflowToolService }

type scopeReviewWorkflowToolService struct {
	*workflowToolService
	status run.Status
	plans  []taskplan.Plan
}

func (s *terminalWorkflowToolService) GetTask(context.Context, string, string) (task.Task, error) {
	return task.Task{ID: "task-a", ProjectID: "project-a", FeatureAreaID: "featureArea-a", Title: "Intent", Phase: task.PhaseAcceptancePending, Status: task.StatusSucceeded, CreatedAt: s.now, UpdatedAt: s.now}, nil
}
func (s *terminalWorkflowToolService) GetExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error) {
	value := s.proposal(executionproposal.StatusConsumed)
	value.Outcome = executionproposal.OutcomeSucceeded
	return value, nil
}
func (s *terminalWorkflowToolService) GetRunForSession(context.Context, string, string) (run.Run, error) {
	value := s.run(run.StatusSucceeded)
	value.Result = &run.Result{GitEvidence: evidence.GitEvidence{ChangedFiles: []string{"one.go", "two.go"}}}
	return value, nil
}
func (s *terminalWorkflowToolService) ListApprovalsForSession(context.Context, string, controlapi.ApprovalFilter) ([]approval.Approval, error) {
	return nil, nil
}

func (s *scopeReviewWorkflowToolService) GetTask(context.Context, string, string) (task.Task, error) {
	return task.Task{ID: "task-a", ProjectID: "project-a", FeatureAreaID: "featureArea-a", Title: "Intent", Phase: task.PhaseReviewing, Status: task.StatusActive, CurrentReviewID: "review_product", CreatedAt: s.now, UpdatedAt: s.now}, nil
}

func (s *scopeReviewWorkflowToolService) GetRunForSession(context.Context, string, string) (run.Run, error) {
	value := s.run(s.status)
	value.AgentExecutionID = "review:review_product"
	value.OperationKind, value.OperationID = run.OperationReview, "review_product"
	value.ReviewID = "review_product"
	value.TaskID = "task-a"
	value.Purpose = run.PurposeScopeReview
	value.ExecutionProposalID = ""
	if s.status == run.StatusFailed || s.status == run.StatusInfrastructureFailed {
		value.Failure = &run.Failure{Kind: run.FailureInfrastructure, Code: "review_failed", Message: "review failed"}
	}
	return value, nil
}

func (s *scopeReviewWorkflowToolService) GetReview(context.Context, string, string) (review.Review, error) {
	return review.Review{ReviewDepth: "DEEP", TaskID: "task-a", ID: "review_product", ProjectID: "project-a", FeatureAreaID: "featureArea-a", RunID: "run-a"}, nil
}

func (s *scopeReviewWorkflowToolService) ListTaskPlans(context.Context, string, controlapi.ListOptions) ([]taskplan.Plan, error) {
	return append([]taskplan.Plan(nil), s.plans...), nil
}

func (s *workflowToolService) ClaimReviewContinuation(context.Context, string, string) (bool, error) {
	if s.continuationClaimed {
		return false, nil
	}
	s.continuationClaimed = true
	return true, nil
}

func (s *workflowToolService) GetSession(_ context.Context, id string) (aicontext.SessionContext, error) {
	if id != "session-a" {
		return aicontext.SessionContext{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "session not found"}
	}
	return aicontext.SessionContext{Session: aisession.Session{ID: id, ProjectID: "project-a"}, Project: project.Project{ID: "project-a"}}, nil
}

func (s *workflowToolService) InitializeSession(_ context.Context, _ aiprojectselection.ClientBindingReference, _ string, id string) (aicontext.InitializationResult, error) {
	if id != "session-a" {
		return aicontext.InitializationResult{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "session not found"}
	}
	projectID := "project-a"
	return aicontext.InitializationResult{AISessionID: id, State: "PROJECT_BOUND", BoundProjectID: &projectID, AvailableProjects: []aicontext.ProjectIdentity{}, AvailableProjectsStatus: "NOT_APPLICABLE", AvailableProjectsComplete: true}, nil
}
func (s *workflowToolService) BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aicontext.ProjectBindingResult, error) {
	return aicontext.ProjectBindingResult{}, &controlapi.Error{Code: controlapi.CodeConflict, Message: "fixture binding is already established"}
}

func (s *workflowToolService) GetFeatureArea(context.Context, string, string) (featurearea.FeatureArea, error) {
	return featurearea.FeatureArea{ID: "featureArea-a", ProjectID: "project-a", Status: featurearea.StatusActive}, nil
}
func (s *workflowToolService) CurrentTaskWorkflow(ctx context.Context, sessionID, taskID string) (controlapi.CurrentWorkflow, error) {
	owner, err := s.GetTask(ctx, sessionID, taskID)
	if err != nil {
		return controlapi.CurrentWorkflow{}, err
	}
	value := controlapi.CurrentWorkflow{Task: &owner}
	if s.reconstructPlan {
		plan := s.taskPlan(taskplan.StatusPendingConfirmation)
		value.TaskPlan = &plan
	}
	if s.reconstructExecution {
		execution := s.proposal(executionproposal.StatusConfirmed)
		value.Execution = &execution
		p, _ := s.GetAgentExecutionForSession(ctx, sessionID, execution.ID)
		value.AgentExecution = &p
	}
	return value, nil
}
func (s *workflowToolService) ListTaskPlans(context.Context, string, controlapi.ListOptions) ([]taskplan.Plan, error) {
	if !s.reconstructPlan {
		return []taskplan.Plan{}, nil
	}
	return []taskplan.Plan{s.taskPlan(taskplan.StatusPendingConfirmation)}, nil
}

func (s *workflowToolService) proposal(status executionproposal.Status) executionproposal.Proposal {
	value := executionproposal.Proposal{
		ID: "execution-1", AISessionID: "session-a", ProjectID: "project-a", TaskID: "task-a", TaskPlanID: "task-plan-a", TaskDefinitionRevisionID: "revision-a", ProvenanceKind: "EXACT",
		WorkSpec:         task.WorkSpec{Goal: "Implement the change", CodexInstruction: "Implement the confirmed change.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}},
		VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "User waived verification."}},
		Status:           status, Outcome: executionproposal.OutcomePending, CreatedAt: s.now, UpdatedAt: s.now,
	}
	if status == executionproposal.StatusConfirmed || status == executionproposal.StatusConsumed {
		value.ConfirmedAt = &s.now
	}
	if status == executionproposal.StatusConsumed {
		value.RunID = "run-a"
		value.ImplementationRunID = "run-a"
		value.Outcome = executionproposal.OutcomeRunning
		value.ConsumedAt = &s.now
	}
	if status == executionproposal.StatusRejected {
		value.RejectedAt = &s.now
	}
	return value
}

func (s *workflowToolService) run(status run.Status) run.Run {
	started := s.now.Add(time.Second)
	return run.Run{ID: "run-a", AgentExecutionID: "execution-1", ProjectID: "project-a", FeatureAreaID: "featureArea-a", OperationKind: run.OperationExecution, OperationID: "execution-1", TaskID: "task-a", Purpose: run.PurposeImplementation, ExecutionProposalID: "execution-1", WorkspaceID: "workspace-a", WorktreeID: "worktree-a", ThreadID: "thread-a", TurnID: "turn-a", Attempt: 1, Status: status, CreatedAt: s.now, UpdatedAt: started, StartedAt: &started, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable, Usage: &run.TokenCounts{InputTokens: 11, OutputTokens: 7, TotalTokens: 18}}}
}

func (s *workflowToolService) runtimeApproval() approval.Approval {
	payload := s.approvalPayload
	if len(payload) == 0 {
		payload = json.RawMessage(`{"command":"go test ./..."}`)
	}
	return approval.Approval{ID: "approval-a", RunID: "run-a", Kind: approval.KindCommandExecution, Status: approval.StatusPending, RequestPayload: payload, AllowedDecisions: []approval.DecisionType{approval.DecisionApproveOnce, approval.DecisionApproveForSession, approval.DecisionDecline}, CreatedAt: s.now}
}

func (s *workflowToolService) GetTask(context.Context, string, string) (task.Task, error) {
	return task.Task{ID: "task-a", ProjectID: "project-a", FeatureAreaID: "featureArea-a", Title: "Intent", Phase: task.PhaseImplementing, Status: task.StatusActive, CreatedAt: s.now, UpdatedAt: s.now}, nil
}
func (s *workflowToolService) GetTaskPlan(context.Context, string, string) (taskplan.Plan, error) {
	return s.taskPlan(taskplan.StatusApplied), nil
}

func (s *workflowToolService) CreateExecutionProposal(context.Context, executionproposal.CreateInput) (executionproposal.Proposal, error) {
	s.proposalCreateCalls++
	return s.proposal(executionproposal.StatusPendingConfirmation), nil
}
func (s *workflowToolService) ConfirmTask(ctx context.Context, _, planID string) (executionproposal.TaskConfirmationResult, error) {
	if s.proposalCreateCalls == 0 {
		s.proposalCreateCalls++
	}
	plan := s.taskPlan(taskplan.StatusApplied)
	plan.ID = planID
	taskValue := task.Task{ID: plan.TaskID, ProjectID: plan.ProjectID, ConfirmedTaskPlanID: plan.ID, Goal: plan.WorkSpec.Goal, WorkSpec: plan.WorkSpec, VerificationSpec: plan.VerificationSpec, Status: task.StatusReady, CreatedAt: s.now, UpdatedAt: s.now}
	proposal := s.proposal(executionproposal.StatusPendingConfirmation)
	if attempt, ok := decisionresolution.FromContext(ctx); ok {
		receipt := decisionresolution.Receipt{DecisionID: attempt.Request.DecisionID, ProjectID: plan.ProjectID, Kind: attempt.Request.Kind, SubjectKind: attempt.Request.SubjectKind, SubjectID: planID, Option: attempt.Request.Option, RequestID: attempt.Request.RequestID, ResultKind: decisionresolution.ResultExecution, ResultID: proposal.ID, ResolvedAt: s.now}
		s.decisionReceipt = &receipt
		attempt.Record(receipt, false, false, false)
	}
	return executionproposal.TaskConfirmationResult{TaskPlan: plan, Task: taskValue, Proposal: proposal}, nil
}
func (s *workflowToolService) GetDecisionResolution(_ context.Context, _, decisionID string) (decisionresolution.Receipt, error) {
	if s.decisionReceipt == nil || s.decisionReceipt.DecisionID != decisionID {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	return *s.decisionReceipt, nil
}
func (s *workflowToolService) GetDecisionResolutionByRequest(_ context.Context, _, requestID string) (decisionresolution.Receipt, error) {
	if s.decisionReceipt == nil || s.decisionReceipt.RequestID != requestID {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	return *s.decisionReceipt, nil
}
func (s *workflowToolService) GetExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error) {
	if s.decisionReceipt != nil {
		return s.proposal(executionproposal.StatusPendingConfirmation), nil
	}
	if s.executed {
		return s.proposal(executionproposal.StatusConsumed), nil
	}
	return s.proposal(executionproposal.StatusConfirmed), nil
}
func (s *workflowToolService) ListExecutionProposals(context.Context, string, controlapi.ListOptions) ([]executionproposal.Proposal, error) {
	return []executionproposal.Proposal{s.proposal(executionproposal.StatusConfirmed)}, nil
}
func (s *workflowToolService) LatestExecutionForTask(ctx context.Context, sessionID, taskID string) (executionproposal.Proposal, error) {
	if !s.reconstructExecution {
		return executionproposal.Proposal{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "requested control-plane entity was not found"}
	}
	return s.GetExecutionProposal(ctx, sessionID, "execution-1")
}
func (s *workflowToolService) RejectExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error) {
	return s.proposal(executionproposal.StatusRejected), nil
}

func (s *workflowToolService) AuthorizeAndStartExecution(_ context.Context, _ string, proposalID string) (executionproposal.StartResult, error) {
	if proposalID == "execution-active" {
		return executionproposal.StartResult{}, &controlapi.Error{Code: controlapi.CodeTaskAlreadyHasActiveRun, Message: "the task already has an active run"}
	}
	if s.executionStarts == 0 {
		s.executionStarts++
	}
	s.executed = true
	return executionproposal.StartResult{Proposal: s.proposal(executionproposal.StatusConsumed), Run: s.run(run.StatusPreparing)}, nil
}
func (s *workflowToolService) GetRunForSession(_ context.Context, sessionID, runID string) (run.Run, error) {
	if sessionID != "session-a" || runID != "run-a" {
		return run.Run{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "requested control-plane entity was not found"}
	}
	status := run.StatusWaitingApproval
	if s.approvalResumed {
		status = run.StatusRunning
	}
	value := s.run(status)
	value.Result = &run.Result{
		AgentClaim:  evidence.AgentClaim{Message: "Codex says the implementation is complete."},
		GitEvidence: evidence.GitEvidence{BaseCommit: "base", HeadCommit: "head", StartSnapshot: "start", EndSnapshot: "end", ChangedFiles: []string{"changed.go"}},
	}
	return value, nil
}
func (s *workflowToolService) ListRunsForSession(context.Context, string, controlapi.RunFilter) ([]run.Run, error) {
	value := s.run(run.StatusWaitingApproval)
	value.Result = &run.Result{
		AgentClaim:  evidence.AgentClaim{Message: "Codex says the implementation is complete."},
		GitEvidence: evidence.GitEvidence{BaseCommit: "base", HeadCommit: "head", StartSnapshot: "start", EndSnapshot: "end", ChangedFiles: []string{"changed.go"}},
	}
	return []run.Run{value}, nil
}
func (s *workflowToolService) LatestRunForTask(ctx context.Context, sessionID, taskID string, purpose run.Purpose, executionID string) (run.Run, error) {
	if purpose != run.PurposeImplementation {
		return run.Run{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "requested control-plane entity was not found"}
	}
	return s.GetRunForSession(ctx, sessionID, "run-a")
}
func (s *workflowToolService) CancelRunForSession(context.Context, string, string) (run.Run, error) {
	s.cancelCalls++
	value := s.run(run.StatusCanceled)
	finished := s.now.Add(2 * time.Second)
	value.FinishedAt = &finished
	return value, nil
}
func (s *workflowToolService) GetApprovalForSession(context.Context, string, string) (approval.Approval, error) {
	value := s.runtimeApproval()
	if s.approvalDecision != nil {
		value.Status = approval.StatusDelivered
		value.Decision = s.approvalDecision
		value.DecidedAt, value.DeliveredAt = &s.now, &s.now
	}
	return value, nil
}
func (s *workflowToolService) ListApprovalsForSession(context.Context, string, controlapi.ApprovalFilter) ([]approval.Approval, error) {
	large := s.runtimeApproval()
	large.ID = "approval-large"
	large.RequestPayload = json.RawMessage(`{"data":"` + strings.Repeat("x", 20<<10) + `"}`)
	return []approval.Approval{s.runtimeApproval(), large}, nil
}
func (s *workflowToolService) DecideApprovalForSession(_ context.Context, _ string, _ string, decision approval.Decision) (approval.Approval, error) {
	if s.approvalDecided {
		return approval.Approval{}, approval.ErrAlreadyResolved
	}
	s.approvalDecided = true
	s.approvalDecision = &decision
	if decision.Type == approval.DecisionApproveOnce || decision.Type == approval.DecisionApproveForSession {
		s.approvalResumed = true
	}
	value := s.runtimeApproval()
	value.Status = approval.StatusDelivered
	value.Decision = &decision
	value.DecidedAt = &s.now
	value.DeliveredAt = &s.now
	return value, nil
}

func (s *workflowToolService) taskPlan(status taskplan.Status) taskplan.Plan {
	value := taskplan.Plan{ID: "task-plan-a", AISessionID: "session-a", ProjectID: "project-a", TaskID: "task-a", WorkSpec: task.WorkSpec{Goal: "Implement the change", Constraints: []string{"preserve behavior"}, CodexInstruction: "Implement the confirmed change.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "User waived verification."}}, Status: status, CreatedAt: s.now, UpdatedAt: s.now}
	if status == taskplan.StatusConfirmed || status == taskplan.StatusApplied {
		value.ConfirmedAt = &s.now
	}
	if status == taskplan.StatusApplied {
		value.AppliedAt = &s.now
	}
	return value
}

func (s *workflowToolService) GetReview(context.Context, string, string) (review.Review, error) {
	return review.Review{ReviewDepth: "DEEP", TaskID: "task-a", ID: "review_product", ProjectID: "project-a", FeatureAreaID: "featureArea-a", RunID: "run-a"}, nil
}

func (s *workflowToolService) scopeExpansion(status scopeexpansion.Status) scopeexpansion.Expansion {
	value := scopeexpansion.Expansion{ID: "scope-expansion-a", AISessionID: "session-a", ProjectID: "project-a", ReviewID: "review_product", FeatureAreaID: "featureArea-a", RepositoryID: "repository-a", CurrentScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"current"}}, AdditionalScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: []string{"additional"}}, Reason: "Inspect the adjacent package", Status: status, CreatedAt: s.now, UpdatedAt: s.now}
	if status != scopeexpansion.StatusPendingConfirmation {
		value.DecidedAt = &s.now
	}
	return value
}

func (s *workflowToolService) CreateScopeExpansion(context.Context, scopeexpansion.CreateInput) (scopeexpansion.Expansion, error) {
	return s.scopeExpansion(scopeexpansion.StatusPendingConfirmation), nil
}
func (s *workflowToolService) GetScopeExpansion(context.Context, string, string) (scopeexpansion.Expansion, error) {
	return s.scopeExpansion(scopeexpansion.StatusPendingConfirmation), nil
}
func (s *workflowToolService) ListScopeExpansions(context.Context, string, controlapi.ListOptions) ([]scopeexpansion.Expansion, error) {
	if !s.reconstructScope {
		return []scopeexpansion.Expansion{}, nil
	}
	return []scopeexpansion.Expansion{s.scopeExpansion(scopeexpansion.StatusPendingConfirmation)}, nil
}
func (s *workflowToolService) AllowScopeExpansion(context.Context, string, string) (scopeexpansion.Expansion, error) {
	return s.scopeExpansion(scopeexpansion.StatusAllowed), nil
}
func (s *workflowToolService) RejectScopeExpansion(context.Context, string, string) (scopeexpansion.Expansion, error) {
	return s.scopeExpansion(scopeexpansion.StatusRejected), nil
}

func TestAppPrivateWorkflowProjectionIsBoundedAndPresentationOnly(t *testing.T) {
	static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	service := &workflowToolService{
		StaticService:   static,
		executed:        true,
		now:             time.Date(2026, 9, 3, 13, 14, 15, 0, time.UTC),
		approvalPayload: json.RawMessage(`{"command":"API_TOKEN=top-secret go test /Users/private/repository/... C:\\private\\repository Authorization: Bearer hidden-value","cwd":"/private/tmp/repository","password":"top-secret"}`),
	}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "workflow-projection-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	projection := callToolMap(t, ctx, session, "ui_workflow_state_get", map[string]any{
		"aiSessionId": "session-a", "taskId": "task-a", "taskPlanId": "task-plan-a", "executionProposalId": "execution-1", "runId": "run-a",
	})
	if projection["phase"] != "IMPLEMENTING" || projection["terminal"] != false {
		t.Fatalf("workflow projection=%+v", projection)
	}
	currentRun := projection["currentRun"].(map[string]any)
	if currentRun["id"] != "run-a" || currentRun["purpose"] != "IMPLEMENTATION" || currentRun["status"] != "WAITING_APPROVAL" {
		t.Fatalf("current run projection=%+v", currentRun)
	}
	approvalValue := projection["approval"].(map[string]any)
	if approvalValue["pending"] != true || approvalValue["id"] != "approval-a" || len(approvalValue["allowedDecisions"].([]any)) != 3 {
		t.Fatalf("approval projection=%+v", approvalValue)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("workflow projection grew beyond presentation bound: %d bytes", len(encoded))
	}
	for _, forbidden := range []string{"agentClaim", "gitEvidence", "evidence", "tokenUsage", "workSpec", "verificationSpec", "changedFiles", "/Users/", "/private/", `C:\\private`, "top-secret", "hidden-value"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("workflow projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestAppPrivateWorkflowProjectionMarksTerminalResult(t *testing.T) {
	static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	base := &workflowToolService{StaticService: static, now: time.Date(2026, 9, 3, 13, 14, 15, 0, time.UTC)}
	service := &terminalWorkflowToolService{workflowToolService: base}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "workflow-terminal-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	projection := callToolMap(t, ctx, session, "ui_workflow_state_get", map[string]any{
		"aiSessionId": "session-a", "taskId": "task-a", "taskPlanId": "task-plan-a", "executionProposalId": "execution-1", "runId": "run-a",
	})
	if projection["phase"] != "ACCEPTANCE_PENDING" || projection["terminal"] != true || projection["changedFileCount"] != float64(2) {
		t.Fatalf("terminal workflow projection=%+v", projection)
	}
	if projection["verification"].(map[string]any)["status"] != "WAITING" {
		t.Fatalf("terminal verification projection=%+v", projection["verification"])
	}
}

func TestScopeReviewProjectionIsIndependentOfTaskPlan(t *testing.T) {
	for _, test := range []struct {
		name  string
		plans []taskplan.Plan
		want  bool
	}{
		{name: "no task plan", want: true},
		{name: "task plan already exists", plans: []taskplan.Plan{{ID: "plan-a", TaskID: "task-a"}}, want: false},
		{name: "only rejected task plan", plans: []taskplan.Plan{{ID: "plan-rejected", TaskID: "task-a", Status: taskplan.StatusRejected}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
			if err != nil {
				t.Fatal(err)
			}
			base := &workflowToolService{StaticService: static, now: time.Date(2026, 9, 3, 13, 14, 15, 0, time.UTC)}
			service := &scopeReviewWorkflowToolService{workflowToolService: base, status: run.StatusSucceeded, plans: test.plans}
			_, _, baseURL := startTestServer(t, service)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "scope-review-continuation-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			projection := callToolMap(t, ctx, session, "ui_workflow_state_get", map[string]any{
				"aiSessionId": "session-a", "reviewId": "review_product",
			})
			if projection["phase"] != "REVIEWING" || projection["terminal"] != false || projection["planningContinuationRequired"] != false || projection["task"] == nil {
				t.Fatalf("Scope Review planning continuation projection=%+v, want continuation=%t", projection, test.want)
			}
			if projection["workflowPhase"] != "REVIEWING" || projection["workflowObjective"] != "WAIT" {
				t.Fatalf("Scope Review product workflow projection=%+v", projection)
			}
			continuation, ok := projection["continuation"].(map[string]any)
			if !ok || continuation["kind"] != "REVIEW_SUCCEEDED" || continuation["featureAreaId"] != "featureArea-a" || continuation["reviewId"] != "review_product" {
				t.Fatalf("Review semantic continuation=%+v", projection["continuation"])
			}
			byReview := callToolMap(t, ctx, session, "ui_workflow_state_get", map[string]any{
				"aiSessionId": "session-a", "reviewId": "review_product",
			})
			if byReview["reviewId"] != "review_product" || byReview["task"] == nil || byReview["currentRun"].(map[string]any)["id"] != "run-a" {
				t.Fatalf("Review-owned app polling projection=%+v", byReview)
			}
			if _, duplicated := byReview["continuation"]; duplicated {
				t.Fatalf("duplicate Review continuation=%+v", byReview)
			}
		})
	}
}

func TestAppPrivateWorkflowReconstructsEachDurableDecisionCard(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(*workflowToolService)
		args   map[string]any
		field  string
		status string
	}{
		{name: "Task confirmation", setup: func(s *workflowToolService) { s.reconstructPlan = true }, args: map[string]any{"aiSessionId": "session-a", "taskId": "task-a"}, field: "taskPlan", status: "PENDING_CONFIRMATION"},
		{name: "Execution authorization", setup: func(s *workflowToolService) { s.reconstructExecution = true }, args: map[string]any{"aiSessionId": "session-a", "taskId": "task-a"}, field: "executionProposal", status: "CONFIRMED"},
		{name: "runtime Approval", args: map[string]any{"aiSessionId": "session-a", "taskId": "task-a", "runId": "run-a"}, field: "approval", status: "PENDING"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var operationalLog bytes.Buffer
			previousLogger := observability.Default()
			observability.SetDefault(observability.New(observability.Options{Process: "devbridge-mcp", Writer: &operationalLog}))
			t.Cleanup(func() { observability.SetDefault(previousLogger) })
			static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
			if err != nil {
				t.Fatal(err)
			}
			service := &workflowToolService{StaticService: static, now: time.Date(2026, 9, 3, 13, 14, 15, 0, time.UTC)}
			if test.setup != nil {
				test.setup(service)
			}
			_, _, baseURL := startTestServer(t, service)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "decision-reconstruction-test", Version: "1"}, nil)
			session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			projection := callToolMap(t, ctx, session, "ui_workflow_state_get", test.args)
			card, ok := projection[test.field].(map[string]any)
			if test.field != "approval" {
				if ok || projection["phase"] != "IMPLEMENTING" {
					t.Fatalf("obsolete child decision overrides Task: %+v", projection)
				}
				return
			}
			if !ok || card["status"] != test.status {
				t.Fatalf("reconstructed %s=%+v projection=%+v", test.field, card, projection)
			}
			for _, other := range []string{"taskPlan", "executionProposal", "approval", "scopeExpansion"} {
				if other != test.field {
					if _, exists := projection[other]; exists {
						t.Fatalf("lower-priority card %s also projected: %+v", other, projection)
					}
				}
			}
			if !bytes.Contains(operationalLog.Bytes(), []byte(`"event":"decision.reconstructed"`)) {
				t.Fatalf("missing reconstruction log: %s", operationalLog.Bytes())
			}
		})
	}
}

func TestFailedReviewContinuationIsAttemptedOnce(t *testing.T) {
	var operationalLog bytes.Buffer
	previousLogger := observability.Default()
	observability.SetDefault(observability.New(observability.Options{Process: "devbridge-mcp", Writer: &operationalLog}))
	t.Cleanup(func() { observability.SetDefault(previousLogger) })
	static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	base := &workflowToolService{StaticService: static, now: time.Date(2026, 9, 3, 13, 14, 15, 0, time.UTC)}
	service := &scopeReviewWorkflowToolService{workflowToolService: base, status: run.StatusFailed}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "review-failure-continuation-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	args := map[string]any{"aiSessionId": "session-a", "reviewId": "review_product"}
	first := callToolMap(t, ctx, session, "ui_workflow_state_get", args)
	continuation, ok := first["continuation"].(map[string]any)
	if !ok || continuation["kind"] != "REVIEW_FAILED" || continuation["featureAreaId"] != "featureArea-a" || continuation["reviewId"] != "review_product" {
		t.Fatalf("failure continuation=%+v", first)
	}
	message, _ := continuation["message"].(string)
	for _, semantic := range []string{"closes the identified Review", "Inspect its durable failure result", "does not request or authorize another Review", "No implementation has been authorized", "Review review_product", "Run run-a", "FeatureArea featureArea-a"} {
		if !strings.Contains(message, semantic) {
			t.Fatalf("failure continuation omits %q: %q", semantic, message)
		}
	}
	if continuation["status"] != "FAILED" || continuation["resultAvailable"] != false || continuation["completedTransition"] != true {
		t.Fatalf("failure continuation semantics=%+v", continuation)
	}
	for _, forbidden := range []string{"threadId", "turnId", "featureArea_create", "task_create"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("failure continuation exposes workflow script or runtime identity %q: %q", forbidden, message)
		}
	}
	if base.proposalCreateCalls != 0 || base.executionStarts != 0 {
		t.Fatalf("failure continuation created or authorized implementation: proposals=%d starts=%d", base.proposalCreateCalls, base.executionStarts)
	}
	second := callToolMap(t, ctx, session, "ui_workflow_state_get", args)
	if _, duplicated := second["continuation"]; duplicated {
		t.Fatalf("duplicate failure continuation=%+v", second)
	}
	for _, event := range []string{"continuation.requested", "continuation.emitted", "continuation.delivery_accepted", "continuation.skipped_duplicate"} {
		if !bytes.Contains(operationalLog.Bytes(), []byte(`"event":"`+event+`"`)) {
			t.Fatalf("missing %s in %s", event, operationalLog.Bytes())
		}
	}
}

func TestOperationGetSeparatesEvidenceFromAgentClaim(t *testing.T) {
	static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	service := &workflowToolService{StaticService: static, now: time.Date(2026, 9, 3, 13, 14, 15, 0, time.UTC)}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "execution-decision-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	service.executed = true
	response, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "operation_get", Arguments: map[string]any{
		"aiSessionId": "session-a", "target": map[string]any{"kind": "EXECUTION", "operationId": "execution-1", "ownerTaskId": "task-a", "runId": "run-a"}, "include": []string{"RESULT", "EVIDENCE"},
	}})
	if err != nil || response.IsError {
		t.Fatalf("operation_get: response=%+v err=%v", response, err)
	}
	encoded, err := json.Marshal(response.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output mcpserver.OperationGetOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	operation := output.Result
	if output.Context.AISessionID != "session-a" || output.Context.ProjectID != "project-a" || output.Context.TaskID != "task-a" || output.Context.Target == nil || output.Context.Target.OperationID != "execution-1" {
		t.Fatalf("exact operation context=%+v", output.Context)
	}
	if len(operation.AgentClaims) != 1 || len(operation.Evidence) != 1 || len(operation.RunResults) != 1 {
		t.Fatalf("typed result partitions=%+v", operation)
	}
	claim, proof, runResult := operation.AgentClaims[0], operation.Evidence[0], operation.RunResults[0]
	if operation.Operation.Status != "EXECUTING" || runResult.Status != "WAITING_APPROVAL" || runResult.Source != "CORE_RUN" || runResult.RunID != "run-a" || claim.Source != "AGENT_CLAIM" || claim.RunID != "run-a" || claim.Claim.Message != "Codex says the implementation is complete." || proof.Git == nil || proof.Git.HeadCommit != "head" || len(proof.Git.ChangedFiles) != 1 || proof.Git.ChangedFiles[0] != "changed.go" {
		t.Fatalf("operation/evidence/claim projection=%+v", operation)
	}
	gitJSON, _ := json.Marshal(proof.Git)
	if bytes.Contains(gitJSON, []byte("Codex says")) || bytes.Contains(gitJSON, []byte(`"message"`)) {
		t.Fatalf("AgentClaim leaked into Evidence: %s", gitJSON)
	}
	if output.Result.Atomic || len(operation.Activity) != 0 || len(operation.RuntimeRequests) != 0 {
		t.Fatalf("unrequested result partitions or false atomic snapshot: %+v", operation)
	}

}

func TestAppPrivateApprovalDecisionResumesExecutionWithoutModelTurn(t *testing.T) {
	var operationalLog bytes.Buffer
	previousLogger := observability.Default()
	observability.SetDefault(observability.New(observability.Options{Process: "devbridge-mcp", Writer: &operationalLog}))
	t.Cleanup(func() { observability.SetDefault(previousLogger) })
	static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	service := &workflowToolService{StaticService: static, now: time.Date(2026, 9, 3, 13, 14, 15, 0, time.UTC)}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "approval-decision-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	continued := callToolMap(t, ctx, session, "ui_approval_decide", map[string]any{"aiSessionId": "session-a", "approvalId": "approval-a", "decision": "APPROVE_FOR_SESSION"})
	decision := continued["approval"].(map[string]any)
	runValue := continued["run"].(map[string]any)
	if decision["status"] != "DELIVERED" || decision["decision"].(map[string]any)["type"] != "APPROVE_FOR_SESSION" || runValue["status"] != "RUNNING" || !service.approvalResumed {
		t.Fatalf("Approval continuation=%+v resumed=%v", continued, service.approvalResumed)
	}
	repeated, repeatErr := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "ui_approval_decide", Arguments: map[string]any{"aiSessionId": "session-a", "approvalId": "approval-a", "decision": "APPROVE_FOR_SESSION"}})
	if repeatErr != nil || repeated == nil || repeated.IsError {
		t.Fatalf("repeated continuation result=%+v err=%v", repeated, repeatErr)
	}
	for _, event := range []string{"continuation.requested", "continuation.emitted", "continuation.delivery_accepted", "continuation.skipped_duplicate"} {
		if !bytes.Contains(operationalLog.Bytes(), []byte(`"event":"`+event+`"`)) {
			t.Fatalf("missing %s in %s", event, operationalLog.Bytes())
		}
	}
}

func (s *workflowToolService) GetAgentExecutionForSession(ctx context.Context, session, id string) (agentexecution.Projection, error) {
	e, err := s.GetExecutionProposal(ctx, session, id)
	if err != nil {
		return agentexecution.Projection{}, err
	}
	var runs []run.Run
	if e.RunID != "" {
		r, _ := s.GetRunForSession(ctx, session, e.RunID)
		runs = append(runs, r)
	}
	return fixtureExecutionProjection(e, runs), nil
}
func (s *terminalWorkflowToolService) GetAgentExecutionForSession(ctx context.Context, session, id string) (agentexecution.Projection, error) {
	e, _ := s.GetExecutionProposal(ctx, session, id)
	r, _ := s.GetRunForSession(ctx, session, e.RunID)
	return fixtureExecutionProjection(e, []run.Run{r}), nil
}
func fixtureExecutionProjection(e executionproposal.Proposal, runs []run.Run) agentexecution.Projection {
	return agentexecution.Reduce(agentexecution.Execution{ID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, FeatureAreaID: "featureArea-a", Kind: agentexecution.KindDevelopment, SemanticOwnerKind: "EXECUTION", SemanticOwnerID: e.ID, ExecutionProposalID: e.ID, ProvenanceKind: agentexecution.ProvenanceNative, CreatedAt: e.CreatedAt}, agentexecution.Facts{Runs: runs, DomainStatus: string(e.Status), DevelopmentOutcome: string(e.Outcome), AuthorizationRequired: e.Outcome == executionproposal.OutcomePending})
}

func (s *scopeReviewWorkflowToolService) GetAgentExecutionForSession(ctx context.Context, session, id string) (agentexecution.Projection, error) {
	r, err := s.GetRunForSession(ctx, session, "run-a")
	if err != nil {
		return agentexecution.Projection{}, err
	}
	return agentexecution.Reduce(agentexecution.Execution{ID: r.AgentExecutionID, ProjectID: r.ProjectID, TaskID: r.TaskID, FeatureAreaID: r.FeatureAreaID, Kind: agentexecution.KindReview, SemanticOwnerKind: "REVIEW", SemanticOwnerID: r.ReviewID}, agentexecution.Facts{Runs: []run.Run{r}, CurrentRunID: r.ID}), nil
}

func (s *workflowToolService) WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error) {
	return wp.Guidance{TaskID: "task-a", TaskPhase: task.PhaseImplementing, CurrentState: "IMPLEMENTING", NextAction: wp.Action{Code: "WAIT"}}, nil
}
