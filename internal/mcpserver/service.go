package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
	workflowpresentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	workflowprogression "github.com/lazyun/devbridge/internal/workflowprogression"
)

const maxMetadataLength = 256

// BridgeInfo is the narrow, read-only view of Dev Bridge exposed to the MCP
// adapter. Domain and persistence implementations can satisfy BridgeService
// later without being imported by this package.
type BridgeInfo struct {
	ApplicationName    string `json:"applicationName"`
	ApplicationVersion string `json:"applicationVersion"`
	Ready              bool   `json:"ready"`
}

// BridgeService is the control-plane port consumed by the MCP adapter.
// Implementations must honor context cancellation.
type BridgeService interface {
	BridgeInfo(context.Context) (BridgeInfo, error)
	ListProjects(context.Context, aicontext.ListOptions) ([]aicontext.ProjectContext, error)
	RecordProjectSelection(context.Context, string) (aiprojectselection.Selection, error)
	OpenSession(context.Context, string) (aicontext.SessionContext, error)
	OpenProject(context.Context, string, string) (aicontext.SessionContext, error)
	GetSession(context.Context, string) (aicontext.SessionContext, error)
	CurrentWorkflow(context.Context, string, string) (controlapi.CurrentWorkflow, error)
	LatestExecutionForTask(context.Context, string, string) (executionproposal.Proposal, error)
	LatestRunForTask(context.Context, string, string, run.Purpose, string) (run.Run, error)
	ListModules(context.Context, string, aicontext.ListOptions) ([]module.Module, error)
	ListProjectModuleMap(context.Context, string) ([]module.Module, error)
	ListRepositories(context.Context, string, aicontext.ListOptions) ([]aicontext.RepositoryContext, error)
	GetRepository(context.Context, string, string) (aicontext.RepositoryContext, error)
	ListTasks(context.Context, string, aicontext.ListOptions) ([]task.Task, error)
	GetTask(context.Context, string, string) (task.Task, error)
	DefineTaskGoals(context.Context, string, string, []string) ([]task.Goal, error)
	ResetFeatureAreaAgentContext(context.Context, string, string, string) error
	ListCodexConfigs(context.Context, string, aicontext.ListOptions) ([]codexconfig.Config, error)
	CreatePlanningTask(context.Context, controlapi.CreatePlanningTaskInput) (task.Task, error)
	CreateFeatureArea(context.Context, featurearea.CreateInput) (featurearea.FeatureArea, error)
	CreateDocument(context.Context, document.CreateInput) (document.Document, error)
	GetFeatureArea(context.Context, string, string) (featurearea.FeatureArea, error)
	ListFeatureAreas(context.Context, string) ([]featurearea.FeatureArea, error)
	CreateReview(context.Context, review.Review) (review.Review, error)
	GetReview(context.Context, string, string) (review.Review, error)
	ListReviews(context.Context, string, string) ([]review.Review, error)
	LinkReviewRun(context.Context, string, string) error
	RecordReviewStartFailure(context.Context, string, string, string) error
	ReviewEvidence(context.Context, string, string) (review.EvidenceSummary, error)
	ClaimReviewContinuation(context.Context, string, string) (bool, error)
	SearchRepository(context.Context, string, string, string, []string, int) (repository.SearchResult, error)
	CreateTaskPlan(context.Context, taskplan.CreateInput) (taskplan.Plan, error)
	GetTaskPlan(context.Context, string, string) (taskplan.Plan, error)
	ListTaskPlans(context.Context, string, controlapi.ListOptions) ([]taskplan.Plan, error)
	RejectTaskPlan(context.Context, string, string) (taskplan.Plan, error)
	StartScopeReview(context.Context, scopereview.StartInput) (run.Run, error)
	CreateScopeExpansion(context.Context, scopeexpansion.CreateInput) (scopeexpansion.Expansion, error)
	GetScopeExpansion(context.Context, string, string) (scopeexpansion.Expansion, error)
	ListScopeExpansions(context.Context, string, controlapi.ListOptions) ([]scopeexpansion.Expansion, error)
	AllowScopeExpansion(context.Context, string, string) (scopeexpansion.Expansion, error)
	RejectScopeExpansion(context.Context, string, string) (scopeexpansion.Expansion, error)
	CurrentReviewScope(context.Context, string, string) (run.ReviewScope, bool, error)
	CreateExecutionProposal(context.Context, executionproposal.CreateInput) (executionproposal.Proposal, error)
	ConfirmTask(context.Context, string, string) (executionproposal.TaskConfirmationResult, error)
	GetDecisionResolution(context.Context, string, string) (decisionresolution.Receipt, error)
	GetDecisionResolutionByRequest(context.Context, string, string) (decisionresolution.Receipt, error)
	GetExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error)
	ListExecutionProposals(context.Context, string, controlapi.ListOptions) ([]executionproposal.Proposal, error)
	RejectExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error)
	AuthorizeAndStartExecution(context.Context, string, string) (executionproposal.StartResult, error)
	ExecutionGoalProgress(context.Context, string, string) ([]taskgoal.Progress, error)
	GetRunForSession(context.Context, string, string) (run.Run, error)
	ListRunsForSession(context.Context, string, controlapi.RunFilter) ([]run.Run, error)
	CancelRunForSession(context.Context, string, string) (run.Run, error)
	GetApprovalForSession(context.Context, string, string) (approval.Approval, error)
	ListApprovalsForSession(context.Context, string, controlapi.ApprovalFilter) ([]approval.Approval, error)
	DecideApprovalForSession(context.Context, string, string, approval.Decision) (approval.Approval, error)
}

// StaticService supplies bootstrap metadata before the production control
// plane is wired to the adapter.
type StaticService struct {
	info    BridgeInfo
	context *aicontext.Service
	control *controlapi.Facade
}

// NewService constructs the production adapter service over the typed
// transport-neutral AI context port.
func NewService(info BridgeInfo, contextService *aicontext.Service, control *controlapi.Facade) (*StaticService, error) {
	if contextService == nil || control == nil {
		return nil, errors.New("AI context and control API services are required")
	}
	if err := validateBridgeInfo(info); err != nil {
		return nil, err
	}
	return &StaticService{info: info, context: contextService, control: control}, nil
}

// NewStaticService constructs a deterministic bootstrap service.
func NewStaticService(info BridgeInfo) (*StaticService, error) {
	if err := validateBridgeInfo(info); err != nil {
		return nil, err
	}
	return &StaticService{info: info}, nil
}

// BridgeInfo returns immutable bootstrap metadata.
func (s *StaticService) BridgeInfo(context.Context) (BridgeInfo, error) {
	return s.info, nil
}

func (s *StaticService) DirectFileEnable(ctx context.Context, scope directfile.Scope) (directfile.State, error) {
	if s.control == nil {
		return directfile.State{}, errors.New("control API unavailable")
	}
	return s.control.DirectFileEnable(ctx, scope)
}
func (s *StaticService) DirectFileDisable(ctx context.Context, scope directfile.Scope) (directfile.State, error) {
	if s.control == nil {
		return directfile.State{}, errors.New("control API unavailable")
	}
	return s.control.DirectFileDisable(ctx, scope)
}
func (s *StaticService) DirectFileFileList(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if s.control == nil {
		return directfile.Result{}, errors.New("control API unavailable")
	}
	if in.ReviewTarget != nil {
		return s.control.UpperReviewList(ctx, in)
	}
	return s.control.FileList(ctx, in)
}
func (s *StaticService) DirectFileFileRead(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if s.control == nil {
		return directfile.Result{}, errors.New("control API unavailable")
	}
	if in.ReviewTarget != nil {
		return s.control.UpperReviewRead(ctx, in)
	}
	return s.control.FileRead(ctx, in)
}
func (s *StaticService) DirectFileFileCreate(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if s.control == nil {
		return directfile.Result{}, errors.New("control API unavailable")
	}
	return s.control.FileCreate(ctx, in)
}
func (s *StaticService) DirectFileFileReplace(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if s.control == nil {
		return directfile.Result{}, errors.New("control API unavailable")
	}
	return s.control.FileReplace(ctx, in)
}
func (s *StaticService) DirectFileFileDelete(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if s.control == nil {
		return directfile.Result{}, errors.New("control API unavailable")
	}
	return s.control.FileDelete(ctx, in)
}
func (s *StaticService) DirectFileFileMove(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	if s.control == nil {
		return directfile.Result{}, errors.New("control API unavailable")
	}
	return s.control.FileMove(ctx, in)
}

func (s *StaticService) ListProjects(ctx context.Context, options aicontext.ListOptions) ([]aicontext.ProjectContext, error) {
	if s.context == nil {
		return nil, errors.New("AI context unavailable")
	}
	return s.context.ListProjects(ctx, options)
}

// Legacy creation ports fail closed even when called outside tool registration.
// New creation and binding use InitializeSession and BindProject respectively.
func (s *StaticService) RecordProjectSelection(context.Context, string) (aiprojectselection.Selection, error) {
	return aiprojectselection.Selection{}, aisession.ErrInitializationRequired
}

func (s *StaticService) OpenSession(context.Context, string) (aicontext.SessionContext, error) {
	return aicontext.SessionContext{}, aisession.ErrInitializationRequired
}

func (s *StaticService) OpenProject(context.Context, string, string) (aicontext.SessionContext, error) {
	return aicontext.SessionContext{}, aisession.ErrInitializationRequired
}

func (s *StaticService) GetSession(ctx context.Context, sessionID string) (aicontext.SessionContext, error) {
	if s.context == nil {
		return aicontext.SessionContext{}, errors.New("AI context unavailable")
	}
	return s.context.GetSession(ctx, sessionID)
}

func (s *StaticService) CurrentWorkflow(ctx context.Context, sessionID, featureAreaID string) (controlapi.CurrentWorkflow, error) {
	if s.control == nil {
		return controlapi.CurrentWorkflow{}, errors.New("control API unavailable")
	}
	return s.control.CurrentWorkflowForSession(ctx, sessionID, featureAreaID)
}

// ExecutionAllowlist exposes the runtime-authoritative Codex model and
// reasoning-effort allowlist. The allowlist is runtime configuration, not
// Project/Feature Area/Task ownership, so it is available even before a session is
// bound to a Project.
// ReviewRepositoryDriftForSession exposes objective Git drift facts between
// the repository state one Project Review was based on and the current
// repository state. It delegates to the shared application service.
func (s *StaticService) ReviewRepositoryDriftForSession(ctx context.Context, aiSessionID, reviewID string, limit int) (review.RepositoryDrift, error) {
	if s.control == nil {
		return review.RepositoryDrift{}, errors.New("control API unavailable")
	}
	return s.control.ReviewRepositoryDriftForSession(ctx, aiSessionID, reviewID, limit)
}

func (s *StaticService) ExecutionAllowlist() codexruntime.Allowlist {
	if s.control == nil {
		return codexruntime.Default()
	}
	return s.control.ExecutionAllowlist()
}

func (s *StaticService) CreateReview(ctx context.Context, value review.Review) (review.Review, error) {
	if s.control == nil {
		return review.Review{}, errors.New("Review service unavailable")
	}
	return s.control.CreateReview(ctx, value)
}

func (s *StaticService) StartAdmittedReview(ctx context.Context, input scopereview.AdmittedStartInput) (review.StartAdmission, error) {
	if s.control == nil {
		return review.StartAdmission{}, errors.New("Review admission service unavailable")
	}
	return s.control.StartAdmittedReview(ctx, input)
}

func (s *StaticService) GetReview(ctx context.Context, sessionID, reviewID string) (review.Review, error) {
	if s.control == nil {
		return review.Review{}, errors.New("Review service unavailable")
	}
	return s.control.GetReviewForSession(ctx, sessionID, reviewID)
}

func (s *StaticService) ListReviews(ctx context.Context, sessionID, featureAreaID string) ([]review.Review, error) {
	if s.control == nil {
		return nil, errors.New("Review service unavailable")
	}
	return s.control.ListReviewsForFeatureArea(ctx, sessionID, featureAreaID)
}

func (s *StaticService) LinkReviewRun(ctx context.Context, reviewID, runID string) error {
	if s.control == nil {
		return errors.New("Review service unavailable")
	}
	return s.control.LinkReviewRun(ctx, reviewID, runID)
}

func (s *StaticService) RecordReviewStartFailure(ctx context.Context, reviewID, code, blocker string) error {
	if s.control == nil {
		return errors.New("Review service unavailable")
	}
	return s.control.RecordReviewStartFailure(ctx, reviewID, code, blocker)
}

func (s *StaticService) ReviewEvidence(ctx context.Context, sessionID, reviewID string) (review.EvidenceSummary, error) {
	if s.control == nil {
		return review.EvidenceSummary{}, errors.New("Review service unavailable")
	}
	return s.control.ReviewEvidenceForSession(ctx, sessionID, reviewID)
}

func (s *StaticService) ClaimReviewContinuation(ctx context.Context, sessionID, reviewID string) (bool, error) {
	if s.control == nil {
		return false, errors.New("Review service unavailable")
	}
	return s.control.ClaimReviewContinuationForSession(ctx, sessionID, reviewID)
}

func (s *StaticService) LatestRunForTask(ctx context.Context, sessionID, taskID string, purpose run.Purpose, executionID string) (run.Run, error) {
	if s.control == nil {
		return run.Run{}, errors.New("control API unavailable")
	}
	return s.control.LatestRunForTaskForSession(ctx, sessionID, taskID, purpose, executionID)
}

func (s *StaticService) LatestExecutionForTask(ctx context.Context, sessionID, taskID string) (executionproposal.Proposal, error) {
	if s.control == nil {
		return executionproposal.Proposal{}, errors.New("control API unavailable")
	}
	return s.control.LatestExecutionForTaskForSession(ctx, sessionID, taskID)
}

func (s *StaticService) ListModules(ctx context.Context, sessionID string, options aicontext.ListOptions) ([]module.Module, error) {
	if s.context == nil {
		return nil, errors.New("AI context unavailable")
	}
	return s.context.ListModules(ctx, sessionID, options)
}

func (s *StaticService) ListProjectModuleMap(ctx context.Context, sessionID string) ([]module.Module, error) {
	if s.context == nil {
		return nil, errors.New("AI context unavailable")
	}
	return s.context.ListProjectModuleMap(ctx, sessionID)
}

func (s *StaticService) ListRepositories(ctx context.Context, sessionID string, options aicontext.ListOptions) ([]aicontext.RepositoryContext, error) {
	if s.context == nil {
		return nil, errors.New("AI context unavailable")
	}
	return s.context.ListRepositories(ctx, sessionID, options)
}

func (s *StaticService) GetRepository(ctx context.Context, sessionID, repositoryID string) (aicontext.RepositoryContext, error) {
	if s.context == nil {
		return aicontext.RepositoryContext{}, errors.New("AI context unavailable")
	}
	return s.context.GetRepository(ctx, sessionID, repositoryID)
}

func (s *StaticService) ListTasks(ctx context.Context, sessionID string, options aicontext.ListOptions) ([]task.Task, error) {
	if s.context == nil {
		return nil, errors.New("AI context unavailable")
	}
	values, err := s.context.ListTasks(ctx, sessionID, options)
	if err != nil || s.control == nil {
		return values, err
	}
	for i := range values {
		values[i], err = s.control.GetTask(ctx, values[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *StaticService) GetTask(ctx context.Context, sessionID, taskID string) (task.Task, error) {
	if s.context == nil {
		return task.Task{}, errors.New("AI context unavailable")
	}
	value, err := s.context.GetTask(ctx, sessionID, taskID)
	if err != nil || s.control == nil {
		return value, err
	}
	return s.control.GetTask(ctx, value.ID)
}

func (s *StaticService) DefineTaskGoals(ctx context.Context, sessionID, taskID string, titles []string) ([]task.Goal, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.DefineTaskGoalsForSession(ctx, sessionID, taskID, titles)
}

func (s *StaticService) ResetFeatureAreaAgentContext(ctx context.Context, sessionID, featureAreaID, requestID string) error {
	if s.control == nil {
		return errors.New("control API unavailable")
	}
	_, err := s.control.ResetFeatureAreaAgentContext(ctx, sessionID, featureAreaID, requestID)
	return err
}

func (s *StaticService) FeatureAreaAgentOperationState(ctx context.Context, sessionID, featureAreaID string) (agentconversation.State, error) {
	if s.control == nil {
		return agentconversation.State{}, errors.New("control API unavailable")
	}
	return s.control.FeatureAreaAgentOperationState(ctx, sessionID, featureAreaID)
}

func (s *StaticService) RecoverFeatureAreaAgentOperation(ctx context.Context, sessionID string, input agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	if s.control == nil {
		return agentconversation.RecoveryResult{}, errors.New("control API unavailable")
	}
	return s.control.RecoverFeatureAreaAgentOperation(ctx, sessionID, input)
}

func (s *StaticService) ListCodexConfigs(ctx context.Context, sessionID string, options aicontext.ListOptions) ([]codexconfig.Config, error) {
	if s.context == nil {
		return nil, errors.New("AI context unavailable")
	}
	return s.context.ListCodexConfigs(ctx, sessionID, options)
}

func (s *StaticService) ReplayPlanningTask(ctx context.Context, input controlapi.CreatePlanningTaskInput) (task.Task, error) {
	if s.control == nil {
		return task.Task{}, task.ErrNotFound
	}
	return s.control.ReplayPlanningTaskForSession(ctx, input)
}
func (s *StaticService) CreatePlanningTask(ctx context.Context, input controlapi.CreatePlanningTaskInput) (task.Task, error) {
	if s.control == nil {
		return task.Task{}, errors.New("control API unavailable")
	}
	return s.control.CreatePlanningTaskForSession(ctx, input)
}
func (s *StaticService) ClassifyTaskWorkKind(ctx context.Context, aiSessionID, taskID string, kind task.WorkKind) (task.Task, error) {
	if s.control == nil {
		return task.Task{}, errors.New("control API unavailable")
	}
	return s.control.ClassifyTaskWorkKind(ctx, aiSessionID, taskID, kind)
}
func (s *StaticService) ClassifyTaskExecutionSelection(ctx context.Context, aiSessionID, taskID, model, reasoningEffort string) (task.Task, error) {
	if s.control == nil {
		return task.Task{}, errors.New("control API unavailable")
	}
	return s.control.ClassifyTaskExecutionSelection(ctx, aiSessionID, taskID, model, reasoningEffort)
}
func (s *StaticService) CreateFeatureArea(ctx context.Context, input featurearea.CreateInput) (featurearea.FeatureArea, error) {
	if s.control == nil {
		return featurearea.FeatureArea{}, errors.New("control API unavailable")
	}
	return s.control.CreateFeatureArea(ctx, input)
}

// CreateFeatureAreaWithInitialReview exposes the single application-owned
// composition to both MCP adapters. The adapter never chains MCP tools.
func (s *StaticService) CreateFeatureAreaWithInitialReview(ctx context.Context, input controlapi.FeatureAreaCreateInput) (controlapi.FeatureAreaCreateResult, error) {
	if s.control == nil {
		return controlapi.FeatureAreaCreateResult{}, errors.New("control API unavailable")
	}
	return s.control.CreateFeatureAreaWithInitialReview(ctx, input)
}
func (s *StaticService) CreateDocument(ctx context.Context, input document.CreateInput) (document.Document, error) {
	if s.control == nil {
		return document.Document{}, errors.New("control API unavailable")
	}
	return s.control.CreateDocument(ctx, input)
}
func (s *StaticService) GetFeatureArea(ctx context.Context, aiSessionID, featureAreaID string) (featurearea.FeatureArea, error) {
	if s.control == nil {
		return featurearea.FeatureArea{}, errors.New("control API unavailable")
	}
	return s.control.GetFeatureAreaForSession(ctx, aiSessionID, featureAreaID)
}
func (s *StaticService) ListFeatureAreas(ctx context.Context, aiSessionID string) ([]featurearea.FeatureArea, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ListFeatureAreasForSession(ctx, aiSessionID)
}
func (s *StaticService) SearchRepository(ctx context.Context, sessionID, repositoryID, query string, paths []string, limit int) (repository.SearchResult, error) {
	if s.control == nil {
		return repository.SearchResult{}, errors.New("control API unavailable")
	}
	return s.control.SearchRepositoryForSession(ctx, sessionID, repositoryID, query, paths, limit)
}
func (s *StaticService) CreateTaskPlan(ctx context.Context, input taskplan.CreateInput) (taskplan.Plan, error) {
	if s.control == nil {
		return taskplan.Plan{}, errors.New("control API unavailable")
	}
	return s.control.CreateTaskPlan(ctx, input)
}

func (s *StaticService) GetTaskPlan(ctx context.Context, sessionID, id string) (taskplan.Plan, error) {
	if s.control == nil {
		return taskplan.Plan{}, errors.New("control API unavailable")
	}
	return s.control.GetTaskPlan(ctx, sessionID, id)
}
func (s *StaticService) ListTaskPlans(ctx context.Context, sessionID string, options controlapi.ListOptions) ([]taskplan.Plan, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ListTaskPlans(ctx, sessionID, options)
}
func (s *StaticService) RejectTaskPlan(ctx context.Context, sessionID, id string) (taskplan.Plan, error) {
	if s.control == nil {
		return taskplan.Plan{}, errors.New("control API unavailable")
	}
	return s.control.RejectTaskPlan(ctx, sessionID, id)
}
func (s *StaticService) StartScopeReview(ctx context.Context, input scopereview.StartInput) (run.Run, error) {
	if s.control == nil {
		return run.Run{}, errors.New("control API unavailable")
	}
	return s.control.StartScopeReview(ctx, input)
}

func (s *StaticService) CreateScopeExpansion(ctx context.Context, input scopeexpansion.CreateInput) (scopeexpansion.Expansion, error) {
	if s.control == nil {
		return scopeexpansion.Expansion{}, errors.New("control API unavailable")
	}
	return s.control.CreateScopeExpansion(ctx, input)
}

func (s *StaticService) GetScopeExpansion(ctx context.Context, sessionID, expansionID string) (scopeexpansion.Expansion, error) {
	if s.control == nil {
		return scopeexpansion.Expansion{}, errors.New("control API unavailable")
	}
	return s.control.GetScopeExpansion(ctx, sessionID, expansionID)
}

func (s *StaticService) ListScopeExpansions(ctx context.Context, sessionID string, options controlapi.ListOptions) ([]scopeexpansion.Expansion, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ListScopeExpansions(ctx, sessionID, options)
}

func (s *StaticService) AllowScopeExpansion(ctx context.Context, sessionID, expansionID string) (scopeexpansion.Expansion, error) {
	if s.control == nil {
		return scopeexpansion.Expansion{}, errors.New("control API unavailable")
	}
	return s.control.AllowScopeExpansion(ctx, sessionID, expansionID)
}

func (s *StaticService) RejectScopeExpansion(ctx context.Context, sessionID, expansionID string) (scopeexpansion.Expansion, error) {
	if s.control == nil {
		return scopeexpansion.Expansion{}, errors.New("control API unavailable")
	}
	return s.control.RejectScopeExpansion(ctx, sessionID, expansionID)
}

func (s *StaticService) CurrentReviewScope(ctx context.Context, sessionID, reviewID string) (run.ReviewScope, bool, error) {
	if s.control == nil {
		return run.ReviewScope{}, false, errors.New("control API unavailable")
	}
	return s.control.CurrentReviewScope(ctx, sessionID, reviewID)
}

func (s *StaticService) GetProjectContextForSession(ctx context.Context, sessionID string) (projectcontext.ProjectContext, error) {
	if s.control == nil {
		return projectcontext.ProjectContext{}, errors.New("control API unavailable")
	}
	return s.control.GetProjectContextForSession(ctx, sessionID)
}

func (s *StaticService) CreateModuleForSession(ctx context.Context, sessionID, key, name, description string, parentID *string) (module.Module, error) {
	if s.control == nil {
		return module.Module{}, errors.New("control API unavailable")
	}
	return s.control.CreateModuleForSession(ctx, sessionID, key, name, description, parentID)
}

func (s *StaticService) CreateContextProbe(ctx context.Context, input contextprobe.CreateInput) (contextprobe.Probe, error) {
	if s.control == nil {
		return contextprobe.Probe{}, errors.New("control API unavailable")
	}
	return s.control.CreateContextProbe(ctx, input)
}

func (s *StaticService) GetContextProbeForSession(ctx context.Context, sessionID, probeID string) (contextprobe.Probe, error) {
	if s.control == nil {
		return contextprobe.Probe{}, errors.New("control API unavailable")
	}
	return s.control.GetContextProbeForSession(ctx, sessionID, probeID)
}

func (s *StaticService) ListContextProbesForSession(ctx context.Context, sessionID string) ([]contextprobe.Probe, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ListContextProbesForSession(ctx, sessionID)
}

func (s *StaticService) StartContextProbe(ctx context.Context, sessionID, probeID string) (contextprobe.Probe, error) {
	if s.control == nil {
		return contextprobe.Probe{}, errors.New("control API unavailable")
	}
	return s.control.StartContextProbe(ctx, sessionID, probeID)
}

func (s *StaticService) ClassifyTaskForSession(ctx context.Context, sessionID, taskID, moduleID string) (task.Task, error) {
	if s.control == nil {
		return task.Task{}, errors.New("control API unavailable")
	}
	return s.control.ClassifyTaskForSession(ctx, sessionID, taskID, moduleID)
}

func (s *StaticService) GetTaskClassificationContextForSession(ctx context.Context, sessionID, taskID string) (controlapi.TaskClassificationContext, error) {
	if s.control == nil {
		return controlapi.TaskClassificationContext{}, errors.New("control API unavailable")
	}
	return s.control.GetTaskClassificationContextForSession(ctx, sessionID, taskID)
}

func (s *StaticService) CreateExecutionProposal(ctx context.Context, input executionproposal.CreateInput) (executionproposal.Proposal, error) {
	if s.control == nil {
		return executionproposal.Proposal{}, errors.New("control API unavailable")
	}
	return s.control.CreateExecutionProposal(ctx, input)
}

func (s *StaticService) ConfirmTask(ctx context.Context, sessionID, taskPlanID string) (executionproposal.TaskConfirmationResult, error) {
	if s.control == nil {
		return executionproposal.TaskConfirmationResult{}, errors.New("control API unavailable")
	}
	return s.control.ConfirmTask(ctx, sessionID, taskPlanID)
}

func (s *StaticService) GetDecisionResolution(ctx context.Context, sessionID, decisionID string) (decisionresolution.Receipt, error) {
	if s.control == nil {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	return s.control.GetDecisionResolution(ctx, sessionID, decisionID)
}

func (s *StaticService) GetDecisionResolutionByRequest(ctx context.Context, sessionID, requestID string) (decisionresolution.Receipt, error) {
	if s.control == nil {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	return s.control.GetDecisionResolutionByRequest(ctx, sessionID, requestID)
}

func (s *StaticService) ResolveTaskAcceptance(ctx context.Context, in workflowprogression.TaskAcceptanceInput) (workflowprogression.TaskAcceptanceResolution, error) {
	if s.control == nil {
		return workflowprogression.TaskAcceptanceResolution{}, errors.New("control API unavailable")
	}
	return s.control.ResolveTaskAcceptance(ctx, in)
}

func (s *StaticService) GetExecutionProposal(ctx context.Context, sessionID, proposalID string) (executionproposal.Proposal, error) {
	if s.control == nil {
		return executionproposal.Proposal{}, errors.New("control API unavailable")
	}
	return s.control.GetExecutionProposal(ctx, sessionID, proposalID)
}

func (s *StaticService) ExecutionGoalProgress(ctx context.Context, sessionID, executionID string) ([]taskgoal.Progress, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ExecutionGoalProgressForSession(ctx, sessionID, executionID)
}

func (s *StaticService) ListExecutionProposals(ctx context.Context, sessionID string, options controlapi.ListOptions) ([]executionproposal.Proposal, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ListExecutionProposals(ctx, sessionID, options)
}

func (s *StaticService) RejectExecutionProposal(ctx context.Context, sessionID, proposalID string) (executionproposal.Proposal, error) {
	if s.control == nil {
		return executionproposal.Proposal{}, errors.New("control API unavailable")
	}
	return s.control.RejectExecutionProposal(ctx, sessionID, proposalID)
}

func (s *StaticService) AuthorizeAndStartExecution(ctx context.Context, sessionID, proposalID string) (executionproposal.StartResult, error) {
	if s.control == nil {
		return executionproposal.StartResult{}, errors.New("control API unavailable")
	}
	return s.control.AuthorizeAndStartExecution(ctx, sessionID, proposalID)
}

func (s *StaticService) GetRunForSession(ctx context.Context, sessionID, runID string) (run.Run, error) {
	if s.control == nil {
		return run.Run{}, errors.New("control API unavailable")
	}
	return s.control.GetRunForSession(ctx, sessionID, runID)
}

func (s *StaticService) ListRunsForSession(ctx context.Context, sessionID string, filter controlapi.RunFilter) ([]run.Run, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ListRunsForSession(ctx, sessionID, filter)
}

func (s *StaticService) CancelRunForSession(ctx context.Context, sessionID, runID string) (run.Run, error) {
	if s.control == nil {
		return run.Run{}, errors.New("control API unavailable")
	}
	return s.control.CancelRunForSession(ctx, sessionID, runID)
}

func (s *StaticService) GetApprovalForSession(ctx context.Context, sessionID, approvalID string) (approval.Approval, error) {
	if s.control == nil {
		return approval.Approval{}, errors.New("control API unavailable")
	}
	return s.control.GetApprovalForSession(ctx, sessionID, approvalID)
}

func (s *StaticService) ListApprovalsForSession(ctx context.Context, sessionID string, filter controlapi.ApprovalFilter) ([]approval.Approval, error) {
	if s.control == nil {
		return nil, errors.New("control API unavailable")
	}
	return s.control.ListApprovalsForSession(ctx, sessionID, filter)
}

func (s *StaticService) DecideApprovalForSession(ctx context.Context, sessionID, approvalID string, decision approval.Decision) (approval.Approval, error) {
	if s.control == nil {
		return approval.Approval{}, errors.New("control API unavailable")
	}
	return s.control.DecideApprovalForSession(ctx, sessionID, approvalID, decision)
}

func validateBridgeInfo(info BridgeInfo) error {
	if info.ApplicationName == "" {
		return errors.New("application name is required")
	}
	if info.ApplicationVersion == "" {
		return errors.New("application version is required")
	}
	if len(info.ApplicationName) > maxMetadataLength {
		return fmt.Errorf("application name exceeds %d bytes", maxMetadataLength)
	}
	if len(info.ApplicationVersion) > maxMetadataLength {
		return fmt.Errorf("application version exceeds %d bytes", maxMetadataLength)
	}
	return nil
}

func (s *StaticService) CurrentTaskWorkflow(ctx context.Context, sessionID, taskID string) (controlapi.CurrentWorkflow, error) {
	if s.control == nil {
		return controlapi.CurrentWorkflow{}, errors.New("control API unavailable")
	}
	return s.control.CurrentWorkflowForTaskForSession(ctx, sessionID, taskID)
}

func (s *StaticService) TaskPresentationForSession(ctx context.Context, sessionID, featureAreaID, taskID string) (workflowpresentation.Projection, error) {
	if s.control == nil {
		return workflowpresentation.Projection{}, errors.New("Task presentation unavailable")
	}
	return s.control.TaskPresentationForSession(ctx, sessionID, featureAreaID, taskID)
}

func (s *StaticService) GetTaskDefinitionRevision(ctx context.Context, sessionID, revisionID string) (task.DefinitionRevision, error) {
	if s.control == nil {
		return task.DefinitionRevision{}, errors.New("control API unavailable")
	}
	return s.control.TaskDefinitionRevisionForSession(ctx, sessionID, revisionID)
}

func (s *StaticService) AgentRuntimeStateForSession(ctx context.Context, sessionID, runID string) (agentruntime.Snapshot, error) {
	if s.control == nil {
		return agentruntime.Snapshot{}, errors.New("control API unavailable")
	}
	return s.control.AgentRuntimeStateForSession(ctx, sessionID, runID)
}
func (s *StaticService) ResolveAgentRequestForSession(ctx context.Context, sessionID string, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	if s.control == nil {
		return agentruntime.Snapshot{}, errors.New("control API unavailable")
	}
	return s.control.ResolveAgentRequestForSession(ctx, sessionID, in)
}

func (s *StaticService) DirectFileReconcileTaskAuthorization(ctx context.Context, in directfile.Registration) (directfile.State, error) {
	if s.control == nil {
		return directfile.State{}, errors.New("control API unavailable")
	}
	return s.control.DirectFileReconcileTaskAuthorization(ctx, in)
}
