package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/workflowevents"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/projectselection"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
	"github.com/lazyun/devbridge/internal/worktreeinventory"
)

const (
	DefaultListLimit         = 50
	MaxListLimit             = 100
	MaxOffset                = 1_000_000
	MaxIDBytes               = 128
	MaxGoalBytes             = 16 << 10
	MaxConstraintCount       = 32
	MaxConstraintBytes       = 2 << 10
	MaxBaseRevisionBytes     = 256
	DefaultArtifactReadBytes = 16 << 10
	MaxArtifactReadBytes     = 64 << 10
)

type ProjectService interface {
	Get(context.Context, string) (project.Project, error)
	List(context.Context) ([]project.Project, error)
}
type ProjectRegistrationService interface {
	Register(context.Context, projectregistration.Input) (projectregistration.Registration, error)
}
type AISessionService interface {
	Get(context.Context, string) (aisession.Session, error)
}
type ProjectSelectionService interface {
	Select(context.Context, string) (projectselection.Selection, error)
	Current(context.Context) (projectselection.Selection, error)
}
type RepositoryService interface {
	Get(context.Context, string) (repository.Repository, error)
	List(context.Context) ([]repository.Repository, error)
	Search(context.Context, string, string, []string, int) (repository.SearchResult, error)
}

// RepositoryUnlinkService is the narrow authoritative lifecycle port. It owns
// eligibility and atomic commit; controlapi only binds it to transport-safe
// request shapes and never reimplements a blocker query.
type RepositoryUnlinkService interface {
	Prepare(context.Context, string, string) (repositoryunlink.Eligibility, error)
	Commit(context.Context, repositoryunlink.CommitInput) (repositoryunlink.Eligibility, error)
}
type ModuleService interface {
	Create(context.Context, module.CreateInput) (module.Module, error)
	Get(context.Context, string) (module.Module, error)
	EnsureUnclassified(context.Context, string) (module.Module, error)
}
type ProjectContextService interface {
	Get(context.Context, string) (projectcontext.ProjectContext, error)
}
type ContextProbeService interface {
	Create(context.Context, contextprobe.CreateInput) (contextprobe.Probe, error)
	GetForSession(context.Context, string, string) (contextprobe.Probe, error)
	ListForSession(context.Context, string) ([]contextprobe.Probe, error)
}
type canonicalContextProbeService interface {
	PrepareInspection(context.Context, contextprobe.PrepareInspectionInput) (contextprobe.InspectionConfirmationPlan, error)
	ConfirmInspection(context.Context, contextprobe.ConfirmInspectionInput) (contextprobe.Probe, error)
}
type ContextProbeRunner interface {
	Start(context.Context, string, string) (contextprobe.Probe, error)
}
type canonicalContextProbeRunner interface {
	StartConfirmed(context.Context, string, string, string, string) (contextprobe.Probe, error)
}
type ClassificationService interface {
	Classify(context.Context, string, string, string) (task.Task, error)
}
type TaskService interface {
	Create(context.Context, task.CreateInput) (task.Task, error)
	Get(context.Context, string) (task.Task, error)
	List(context.Context) ([]task.Task, error)
}
type workKindTaskService interface {
	ClassifyWorkKind(context.Context, string, task.WorkKind) (task.Task, error)
}
type selectionTaskService interface {
	ClassifyExecutionSelection(context.Context, string, string, string) (task.Task, error)
}
type FeatureAreaService interface {
	Create(context.Context, featurearea.CreateInput) (featurearea.FeatureArea, error)
	GetForSession(context.Context, string, string) (featurearea.FeatureArea, error)
	ListForSession(context.Context, string) ([]featurearea.FeatureArea, error)
	CurrentForSession(context.Context, string) (featurearea.FeatureArea, error)
	IsTaskActionableFor(featurearea.FeatureArea) bool
}

// AgentConversationResetService rotates the current FeatureArea conversation.
// It is optional while adapters using older test doubles remain supported.
type AgentConversationResetService interface {
	Reset(context.Context, string, string) (agentconversation.Conversation, error)
}

// AgentConversationRecoveryService is the optional durable recovery seam.
// Recovery is session-authorized at the facade and never accepts runtime epoch.
type AgentConversationRecoveryService interface {
	AgentConversationResetService
	State(context.Context, string) (agentconversation.State, error)
	Recover(context.Context, agentconversation.RecoverInput) (agentconversation.RecoveryResult, error)
}
type DocumentService interface {
	Create(context.Context, document.CreateInput) (document.Document, error)
}

// ReviewDriftService derives objective Git drift facts between a Review's
// recorded repository state and the current repository state. It is optional:
// when absent, drift reads report a bounded factual unavailability.
type ReviewDriftService interface {
	RepositoryDrift(ctx context.Context, reviewID string, limit int) (review.RepositoryDrift, error)
}
type ReviewService interface {
	Create(context.Context, review.Review) (review.Review, error)
	Get(context.Context, string) (review.Review, error)
	ListByFeatureArea(context.Context, string) ([]review.Review, error)
	LinkRun(context.Context, string, string) error
	RecordStartFailure(context.Context, string, string, string) error
	SummarizeEvidence(context.Context, string) (review.EvidenceSummary, error)
	ClaimContinuation(context.Context, string) (bool, error)
}
type TaskPlanService interface {
	Create(context.Context, taskplan.CreateInput) (taskplan.Plan, error)
	Get(context.Context, string, string) (taskplan.Plan, error)
	List(context.Context, string, taskplan.ListOptions) ([]taskplan.Plan, error)
	Confirm(context.Context, string, string) (taskplan.Plan, error)
	Reject(context.Context, string, string) (taskplan.Plan, error)
	Apply(context.Context, string, string) (taskplan.ApplyResult, error)
}
type ScopeReviewService interface {
	Start(context.Context, scopereview.StartInput) (run.Run, error)
}
type ScopeExpansionService interface {
	Create(context.Context, scopeexpansion.CreateInput) (scopeexpansion.Expansion, error)
	Get(context.Context, string, string) (scopeexpansion.Expansion, error)
	List(context.Context, string, scopeexpansion.ListOptions) ([]scopeexpansion.Expansion, error)
	Allow(context.Context, string, string) (scopeexpansion.Expansion, error)
	Reject(context.Context, string, string) (scopeexpansion.Expansion, error)
	CurrentAuthorizedScope(context.Context, string) (run.ReviewScope, bool, error)
}
type ExecutionProposalService interface {
	Create(context.Context, executionproposal.CreateInput) (executionproposal.Proposal, error)
	ConfirmTask(context.Context, string, string) (executionproposal.TaskConfirmationResult, error)
	Get(context.Context, string, string) (executionproposal.Proposal, error)
	List(context.Context, string, executionproposal.ListOptions) ([]executionproposal.Proposal, error)
	Confirm(context.Context, string, string) (executionproposal.Proposal, error)
	Reject(context.Context, string, string) (executionproposal.Proposal, error)
	Start(context.Context, string, string) (executionproposal.StartResult, error)
	AuthorizeAndStart(context.Context, string, string) (executionproposal.StartResult, error)
}
type DecisionResolutionService interface {
	Get(context.Context, string, string) (decisionresolution.Receipt, error)
	GetByRequest(context.Context, string, string) (decisionresolution.Receipt, error)
}

// ExecutionGoalService owns observable Goal progress per Execution. It is
// optional: goal reads report unavailability when absent.
type ExecutionGoalService interface {
	ListForExecution(ctx context.Context, executionID string) ([]taskgoal.Progress, error)
}
type RunService interface {
	Get(context.Context, string) (run.Run, error)
	List(context.Context) ([]run.Run, error)
}
type EvidenceStore interface {
	Get(context.Context, string) (evidence.GitEvidence, error)
}
type ArtifactStore interface {
	Get(context.Context, string) (artifact.Artifact, error)
	ListByRun(context.Context, string) ([]artifact.Artifact, error)
}
type EventStore interface {
	List(context.Context, event.Filter) ([]event.Event, error)
}
type ApprovalService interface {
	Get(context.Context, string) (approval.Approval, error)
	ListByRun(context.Context, string) ([]approval.Approval, error)
	Decide(context.Context, string, approval.Decision) (approval.Approval, error)
}
type Runner interface {
	StartRun(context.Context, string) (run.Run, error)
	CancelRun(context.Context, string) (run.Run, error)
}

type Dependencies struct {
	SteeringRuntime          *agentruntime.Service
	CodexHistoryReader       CodexHistoryReader
	WorkflowEvents           *workflowevents.Service
	RepositoryUnlink         RepositoryUnlinkService
	RecoveryStatus           RecoveryStatusService
	RecoveryPurposeConsumers recoverystatus.PurposeConsumer
	Confirmations            confirmation.Store
	ConfirmationSessions     *aicontext.Service
	UpperReviewAccess        UpperReviewAccessService
	UpperReviewPreparer      interface {
		PrepareUpperReview(context.Context, review.UpperTarget) (review.UpperAccess, error)
	}
	UpperReviewWorkers   UpperReviewWorkerService
	DirectFileWorkers    DirectFileWorkerService
	UpperReviewResults   UpperReviewResultService
	RunAnalyses          *runanalysis.Service
	StartupContext       StartupContext
	WorktreeInventory    *worktreeinventory.Service
	DirectFiles          *directfile.Service
	AgentExecutions      agentexecution.Store
	RuntimeQualification interface {
		ResolvePlatformReference(context.Context, codexconfig.BackendSelection, string) (runtimeselection.PlatformReference, error)
	}
	RuntimeSelections   runtimeselection.Store
	WorkflowProgression wp.Store
	DevelopmentCycles   DevelopmentCycleService
	Projects            ProjectService
	ProjectRegistration ProjectRegistrationService
	ProjectSelection    ProjectSelectionService
	AISessions          AISessionService
	Repositories        RepositoryService
	CodexConfigs        interface {
		List(context.Context) ([]codexconfig.Config, error)
		Get(context.Context, string) (codexconfig.Config, error)
	}
	Modules                ModuleService
	ProjectContexts        ProjectContextService
	ContextProbes          ContextProbeService
	ContextProbeRunner     ContextProbeRunner
	Classifications        ClassificationService
	Tasks                  TaskService
	FeatureAreas           FeatureAreaService
	AgentConversations     AgentConversationResetService
	Documents              DocumentService
	DecisionResolutions    DecisionResolutionService
	Reviews                ReviewService
	ReviewDrift            ReviewDriftService
	ExecutionGoals         ExecutionGoalService
	TaskPlans              TaskPlanService
	ScopeReviews           ScopeReviewService
	ScopeExpansions        ScopeExpansionService
	ExecutionProposals     ExecutionProposalService
	ExecutionCancellations executioncancel.Store
	Runs                   RunService
	Workspaces             WorkspaceReadService
	Evidence               EvidenceStore
	Artifacts              ArtifactStore
	Events                 EventStore
	Approvals              ApprovalService
	Runner                 Runner
	ExecutionAllowlist     interface{ ExecutionAllowlist() codexruntime.Allowlist }
	// PiAgentEnabled reports whether the PI Agent review executor runtime is
	// configured. Nil means unavailable.
	PiAgentEnabled        func() bool
	CodexRuntimeInventory interface {
		Snapshot() codexruntime.Snapshot
		Apply(uint64, codexruntime.Allowlist) (codexruntime.Snapshot, error)
	}
}

type Facade struct {
	// Set only on a private, project-authorized local read adapter. No Session is synthesized.
	operatorProject     string
	workspaceSelections *workspaceSelections
	deps                Dependencies
	drift               ReviewDriftService
	allowlistProvider   interface{ ExecutionAllowlist() codexruntime.Allowlist }
	runtimeInventory    interface {
		Snapshot() codexruntime.Snapshot
		Apply(uint64, codexruntime.Allowlist) (codexruntime.Snapshot, error)
	}
	// allowlist remains as a compatibility seam for package-local focused tests.
	allowlist codexruntime.Allowlist
}

func New(deps Dependencies) (*Facade, error) {
	if deps.Projects == nil || deps.ProjectRegistration == nil || deps.ProjectSelection == nil || deps.AISessions == nil || deps.Repositories == nil || deps.Modules == nil || deps.ProjectContexts == nil || deps.ContextProbes == nil || deps.ContextProbeRunner == nil || deps.Classifications == nil || deps.FeatureAreas == nil || deps.Documents == nil || deps.Reviews == nil || deps.Tasks == nil || deps.TaskPlans == nil || deps.ScopeReviews == nil || deps.ScopeExpansions == nil || deps.ExecutionProposals == nil || deps.ExecutionGoals == nil || deps.Runs == nil || deps.Evidence == nil || deps.Artifacts == nil || deps.Events == nil || deps.Approvals == nil || deps.Runner == nil {
		return nil, errors.New("control API dependencies are incomplete")
	}
	allowlist := codexruntime.Default()
	provider := deps.ExecutionAllowlist
	if provider != nil {
		allowlist = provider.ExecutionAllowlist()
	}
	var err error
	if allowlist, err = allowlist.Normalize(); err != nil {
		return nil, errors.New("control API execution allowlist is invalid")
	}
	return &Facade{workspaceSelections: &workspaceSelections{values: make(map[string]workspaceSelection)}, deps: deps, drift: deps.ReviewDrift, allowlistProvider: provider, runtimeInventory: deps.CodexRuntimeInventory, allowlist: allowlist.Clone()}, nil
}

func NewFromControlPlane(plane *controlplane.ControlPlane) (*Facade, error) {
	if plane == nil || !plane.Ready {
		return nil, errors.New("control plane is not ready")
	}
	return New(Dependencies{RunAnalyses: &runanalysis.Service{Store: sqlite.NewRunAnalysisStore(plane.DB), Analyze: execution.AnalyzeRunReport}, SteeringRuntime: plane.Runs.AgentRuntime, CodexHistoryReader: nativeHistoryReader(plane), WorkflowEvents: plane.WorkflowEvents, RuntimeQualification: plane.RuntimeQualification, RuntimeSelections: sqlite.NewRuntimeSelectionStore(plane.DB), RecoveryStatus: plane.RecoveryStatus, RecoveryPurposeConsumers: plane.RecoveryPurposeConsumers, RepositoryUnlink: plane.RepositoryUnlink, Confirmations: plane.Confirmations, ConfirmationSessions: plane.AIContext, UpperReviewAccess: plane.UpperReviewAccess, UpperReviewPreparer: plane.Coordinator, UpperReviewResults: plane.UpperReviewResults, UpperReviewWorkers: plane.UpperReviewWorkers, DirectFileWorkers: plane.DirectFileWorkers, StartupContext: plane.AIContext, CodexConfigs: plane.CodexConfigs, WorktreeInventory: plane.WorktreeInventory, DirectFiles: plane.DirectFiles, AgentExecutions: plane.AgentExecutions, WorkflowProgression: plane.WorkflowProgression, DevelopmentCycles: plane.DevelopmentCycles, Projects: plane.Projects, ProjectRegistration: plane.ProjectRegistration, ProjectSelection: plane.ProjectSelection, AISessions: plane.AISessions, Repositories: plane.Repositories, Modules: plane.Modules, ProjectContexts: plane.ProjectContexts, ContextProbes: plane.ContextProbes, ContextProbeRunner: plane.ContextProbeRunner, Classifications: plane.Classifications, FeatureAreas: plane.FeatureAreas, AgentConversations: plane.AgentConversations, Documents: plane.Documents, DecisionResolutions: plane.DecisionResolutions, Reviews: plane.Reviews, Tasks: plane.Tasks, TaskPlans: plane.TaskPlans, ScopeReviews: plane.ScopeReviews, ScopeExpansions: plane.ScopeExpansions, ExecutionProposals: plane.ExecutionProposals, ExecutionCancellations: plane.ExecutionCancellations, ExecutionGoals: plane.ExecutionGoals, Runs: plane.Runs, Workspaces: plane.Workspaces, Evidence: plane.Evidence, Artifacts: plane.Artifacts, Events: plane.Events, Approvals: plane.Approvals, Runner: plane.Supervisor, ExecutionAllowlist: plane.CodexRuntime, PiAgentEnabled: func() bool { return plane.Coordinator != nil && plane.Coordinator.PiAgent != nil }, CodexRuntimeInventory: plane.CodexRuntime, ReviewDrift: plane.ReviewDrift})
}

// ExecutionAllowlist exposes the runtime-authoritative Codex model and
// reasoning-effort allowlist without requiring a Project-bound session.
func (f *Facade) ExecutionAllowlist() codexruntime.Allowlist {
	value := f.allowlist.Clone()
	if f.allowlistProvider != nil {
		value = f.allowlistProvider.ExecutionAllowlist()
	}
	normalized, err := value.Normalize()
	if err != nil {
		// This accessor cannot return an error. An empty inventory keeps request
		// admission closed if a provider ever publishes an invalid generation.
		return codexruntime.Allowlist{}
	}
	return normalized
}

// CodexRuntimeInventory returns a detached process-authoritative snapshot for
// local control clients. It is not part of either MCP adapter surface.
func (f *Facade) CodexRuntimeInventory() (codexruntime.Snapshot, error) {
	if f.runtimeInventory == nil {
		return codexruntime.Snapshot{}, errors.New("Codex runtime inventory is unavailable")
	}
	return f.runtimeInventory.Snapshot(), nil
}

// ApplyCodexRuntimeInventory is the application operation for a complete
// compare-and-swap replacement. Callers cannot partially update a generation.
func (f *Facade) ApplyCodexRuntimeInventory(expectedRevision uint64, next codexruntime.Allowlist) (codexruntime.Snapshot, error) {
	if f.runtimeInventory == nil {
		return codexruntime.Snapshot{}, errors.New("Codex runtime inventory is unavailable")
	}
	return f.runtimeInventory.Apply(expectedRevision, next)
}

type ListOptions struct{ Limit, Offset int }
type ProjectSummary struct {
	Project         project.Project
	RepositoryCount int
}
type ProjectDetails struct {
	Project      project.Project
	Repositories []repository.Repository
}

// RegisterProject is the production application entry point for creating the
// deterministic Project -> local Repository facts required at first use.
func (f *Facade) RegisterProject(ctx context.Context, input projectregistration.Input) (projectregistration.Registration, error) {
	value, err := f.deps.ProjectRegistration.Register(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

type TaskFilter struct {
	ProjectID, RepositoryID string
	Status                  task.Status
	ListOptions
}
type RunFilter struct {
	TaskID string
	Status run.Status
	ListOptions
}
type ApprovalFilter struct {
	RunID       string
	PendingOnly bool
	ListOptions
}
type EventFilter struct {
	ProjectID, TaskID, RunID string
	AfterSequence            int64
	Limit                    int
}
type EvidenceDetails struct {
	Evidence  evidence.GitEvidence
	Artifacts []artifact.Artifact
}
type CreateTaskRequest struct {
	ProjectID, FeatureAreaID, ModuleID, RepositoryID, CodexConfigID, Goal string
	Constraints                                                           []string
	BaseRevision                                                          string
}

type TaskClassificationContext struct {
	Task                  task.Task
	Module                module.Module
	LatestRun             *run.Run
	ChangedFiles          []string
	DiffArtifactID        string
	AgentReportArtifactID string
	ProjectContext        projectcontext.ProjectContext
}

// CurrentWorkflow is the durable, relationship-based slice needed to derive
// startup/current state. It intentionally contains no bounded history page.
type CurrentWorkflow struct {
	DirectFile            *directfile.State
	LatestTask            *task.Task
	AgentExecution        *agentexecution.Projection
	Guidance              *wp.Guidance
	Topic                 *featurearea.FeatureArea
	AvailableFeatureAreas []featurearea.FeatureArea
	ActiveReview          *review.Review
	LatestReview          *review.Review
	// AgentOperation is the ownership observation used to select ActiveReview.
	// Presentation must not combine that selection with a later operation read.
	AgentOperation *agentconversation.State
	// CurrentReview is the compatibility projection: active Review when one
	// owns the Feature Area, otherwise the latest admitted Review.
	CurrentReview             *review.Review
	Reviews                   []review.Review
	Task                      *task.Task
	TaskPlan                  *taskplan.Plan
	Execution                 *executionproposal.Proposal
	Runs                      []run.Run
	ApprovalPending           bool
	PendingApproval           *approval.Approval
	PendingScopeExpansion     *scopeexpansion.Expansion
	AvailableModels           []string
	AvailableReasoningEfforts []string
}

func (f *Facade) CurrentWorkflowForSession(ctx context.Context, aiSessionID, featureAreaID string) (CurrentWorkflow, error) {
	if f.deps.WorkflowProgression == nil {
		return f.currentWorkflowForSession(ctx, aiSessionID, featureAreaID, "")
	}
	if featureAreaID != "" {
		if _, err := (historicalFeatureAreaRead{f.deps.FeatureAreas}).GetForSession(ctx, aiSessionID, featureAreaID); err != nil {
			return CurrentWorkflow{}, MapError(err)
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		g, err := f.WorkflowGuidance(ctx, aiSessionID, featureAreaID)
		if err != nil {
			return CurrentWorkflow{}, err
		}
		value, err := f.currentWorkflowForSession(ctx, aiSessionID, featureAreaID, g.TaskID)
		if err != nil {
			return value, err
		}
		if value.Topic == nil {
			return value, nil
		}
		if featureAreaID == "" {
			return f.CurrentWorkflowForSession(ctx, aiSessionID, value.Topic.ID)
		}
		after, err := f.WorkflowGuidance(ctx, aiSessionID, value.Topic.ID)
		if err != nil {
			return value, err
		}
		if featureAreaID != "" && g.Generation != after.Generation {
			continue
		}
		value.Guidance = &after
		if after.CurrentWorkflowReview != nil && value.ActiveReview == nil {
			for i := range value.Reviews {
				if value.Reviews[i].ID == after.CurrentWorkflowReview.ID {
					value.CurrentReview = &value.Reviews[i]
					break
				}
			}
		}
		if after.LatestTask != nil {
			latest, e := f.deps.Tasks.Get(ctx, after.LatestTask.ID)
			if e != nil {
				return CurrentWorkflow{}, MapError(e)
			}
			if latest.ProjectID != after.ProjectID || latest.FeatureAreaID != value.Topic.ID {
				return CurrentWorkflow{}, notFound()
			}
			value.LatestTask = &latest
		}
		if err := f.enrichCurrentExecution(ctx, &value); err != nil {
			return CurrentWorkflow{}, err
		}
		final, err := f.WorkflowGuidance(ctx, aiSessionID, value.Topic.ID)
		if err != nil {
			return CurrentWorkflow{}, err
		}
		if final.Generation != after.Generation {
			continue
		}
		if after.CurrentState == "FEATURE_AREA_IDLE" {
			value.Task = nil
			value.TaskPlan = nil
			value.Execution = nil
			value.AgentExecution = nil
		}
		return value, nil
	}
	return CurrentWorkflow{}, errors.New("workflow snapshot changed; refresh required")
}

// CurrentWorkflowForTaskForSession keeps a selected Task's exact current
// operation graph, including when another Task is newer in the FeatureArea.
func (f *Facade) CurrentWorkflowForTaskForSession(ctx context.Context, aiSessionID, taskID string) (CurrentWorkflow, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return CurrentWorkflow{}, err
	}
	value, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || value.ProjectID != projectID {
		return CurrentWorkflow{}, notFound()
	}
	return f.currentWorkflowForSession(ctx, aiSessionID, value.FeatureAreaID, taskID)
}

func terminalWorkflowRun(status run.Status) bool {
	switch status {
	case run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
		return true
	default:
		return false
	}
}

func (f *Facade) currentWorkflowForSession(ctx context.Context, aiSessionID, featureAreaID, selectedTaskID string) (result CurrentWorkflow, resultErr error) {
	defer func() {
		if resultErr == nil {
			resultErr = f.enrichCurrentExecution(ctx, &result)
		}
	}()
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return CurrentWorkflow{}, err
	}
	result = CurrentWorkflow{AvailableFeatureAreas: []featurearea.FeatureArea{}, Reviews: []review.Review{}, Runs: []run.Run{}}
	allowlist := f.ExecutionAllowlist()
	result.AvailableModels = append([]string(nil), allowlist.Models...)
	result.AvailableReasoningEfforts = append([]string(nil), allowlist.ReasoningEfforts...)
	var featureAreaValue featurearea.FeatureArea
	if featureAreaID != "" {
		if history, ok := f.deps.FeatureAreas.(interface {
			GetHistoryForSession(context.Context, string, string) (featurearea.FeatureArea, error)
		}); selectedTaskID != "" && ok {
			featureAreaValue, err = history.GetHistoryForSession(ctx, aiSessionID, featureAreaID)
		} else {
			featureAreaValue, err = f.deps.FeatureAreas.GetForSession(ctx, aiSessionID, featureAreaID)
		}
	} else {
		values, listErr := f.deps.FeatureAreas.ListForSession(ctx, aiSessionID)
		if listErr != nil {
			err = listErr
		} else {
			activeIDs := []string{}
			if active, ok := f.deps.Tasks.(interface {
				ActiveFeatureAreaIDsForProject(context.Context, string) ([]string, error)
			}); ok {
				activeIDs, err = active.ActiveFeatureAreaIDsForProject(ctx, projectID)
			}
			usableActive := make([]string, 0, len(activeIDs))
			for _, id := range activeIDs {
				if _, activeErr := f.deps.FeatureAreas.GetForSession(ctx, aiSessionID, id); activeErr == nil {
					usableActive = append(usableActive, id)
				} else if !errors.Is(activeErr, featurearea.ErrNotFound) && !errors.Is(activeErr, featurearea.ErrUnavailable) {
					err = activeErr
					break
				}
			}
			if err == nil && len(usableActive) == 1 {
				featureAreaValue, err = f.deps.FeatureAreas.GetForSession(ctx, aiSessionID, usableActive[0])
			} else if err == nil && len(usableActive) == 0 && len(values) == 1 {
				featureAreaValue = values[0]
			} else if err == nil && len(values) > 1 {
				result.AvailableFeatureAreas = append(result.AvailableFeatureAreas, values...)
				return result, nil
			}
			if err == nil && featureAreaValue.ID == "" {
				err = featurearea.ErrNotFound
			}
		}
	}
	if errors.Is(err, featurearea.ErrNotFound) {
		if featureAreaID != "" {
			return CurrentWorkflow{}, MapError(err)
		}
		return result, nil
	}
	if err != nil {
		return CurrentWorkflow{}, MapError(err)
	}
	if featureAreaValue.ProjectID != projectID {
		return CurrentWorkflow{}, notFound()
	}
	result.Topic = &featureAreaValue
	reviewValues, reviewErr := f.deps.Reviews.ListByFeatureArea(ctx, featureAreaValue.ID)
	if reviewErr != nil {
		return CurrentWorkflow{}, MapError(reviewErr)
	}
	for _, reviewValue := range reviewValues {
		if reviewValue.ProjectID != projectID {
			return CurrentWorkflow{}, notFound()
		}
		result.Reviews = append(result.Reviews, reviewValue)
	}
	reviewRuns := make(map[string]run.Run, len(result.Reviews))
	for index := len(result.Reviews) - 1; index >= 0; index-- {
		reviewValue := result.Reviews[index]
		if reviewValue.RunID == "" {
			// Runtimeless start-failure rows are legacy admission history. They
			// remain listable but are not latest admitted workflow state.
			continue
		}
		runValue, getErr := f.deps.Runs.Get(ctx, reviewValue.RunID)
		if getErr != nil || runValue.ReviewID != reviewValue.ID {
			continue
		}
		reviewRuns[reviewValue.ID] = runValue
		if result.LatestReview == nil {
			copy := reviewValue
			result.LatestReview = &copy
		}
	}
	if f.deps.DirectFiles != nil {
		state, err := f.DirectFileStatus(ctx, aiSessionID, featureAreaValue.ID)
		if err != nil {
			return CurrentWorkflow{}, err
		}
		result.DirectFile = &state
	}
	stateReader, authoritativeAgentState := f.deps.AgentConversations.(interface {
		State(context.Context, string) (agentconversation.State, error)
	})
	if authoritativeAgentState {
		state, stateErr := stateReader.State(ctx, featureAreaValue.ID)
		if stateErr != nil {
			return CurrentWorkflow{}, MapError(stateErr)
		}
		result.AgentOperation = &state
		if state.Claim != nil && state.Claim.Kind == agentconversation.Review {
			for index := range result.Reviews {
				if result.Reviews[index].ID == state.Claim.OperationID && result.Reviews[index].RunID == state.Claim.RunID {
					copy := result.Reviews[index]
					result.ActiveReview = &copy
					break
				}
			}
			if result.ActiveReview == nil {
				// Admission may have committed after the history list was read.
				// Resolve the exact owner rather than falling back to old success.
				value, getErr := f.deps.Reviews.Get(ctx, state.Claim.OperationID)
				if getErr != nil {
					return CurrentWorkflow{}, MapError(getErr)
				}
				if value.ProjectID != projectID || value.FeatureAreaID != featureAreaValue.ID || value.RunID != state.Claim.RunID {
					return CurrentWorkflow{}, notFound()
				}
				result.ActiveReview = &value
				result.Reviews = append(result.Reviews, value)
				if result.LatestReview == nil || value.CreatedAt.After(result.LatestReview.CreatedAt) || value.CreatedAt.Equal(result.LatestReview.CreatedAt) && value.ID > result.LatestReview.ID {
					result.LatestReview = &value
				}
			}
			value, getErr := f.deps.Runs.Get(ctx, state.Claim.RunID)
			if getErr != nil {
				return CurrentWorkflow{}, MapError(getErr)
			}
			if value.ReviewID != result.ActiveReview.ID || value.ProjectID != projectID || value.FeatureAreaID != featureAreaValue.ID {
				return CurrentWorkflow{}, notFound()
			}
			reviewRuns[result.ActiveReview.ID] = value
		}
	}
	if result.ActiveReview == nil && !authoritativeAgentState {
		for index := len(result.Reviews) - 1; index >= 0; index-- {
			reviewValue := result.Reviews[index]
			runValue, ok := reviewRuns[reviewValue.ID]
			if !ok || terminalWorkflowRun(runValue.Status) {
				continue
			}
			copy := reviewValue
			result.ActiveReview = &copy
			break
		}
	}
	result.CurrentReview = result.ActiveReview
	if result.CurrentReview == nil {
		result.CurrentReview = result.LatestReview
	}

	seenRuns := map[string]bool{}
	if result.CurrentReview != nil {
		if value, ok := reviewRuns[result.CurrentReview.ID]; ok {
			result.Runs = append(result.Runs, value)
			seenRuns[value.ID] = true
		}
	}

	// scope_expansions are legacy history, never an active workflow gate.

	var taskValue task.Task
	if selectedTaskID != "" {
		taskValue, err = f.deps.Tasks.Get(ctx, selectedTaskID)
	} else {
		currentTask, ok := f.deps.Tasks.(interface {
			LatestForFeatureArea(context.Context, string) (task.Task, error)
		})
		if !ok {
			return CurrentWorkflow{}, errors.New("phase-aware Task read unavailable")
		}
		taskValue, err = currentTask.LatestForFeatureArea(ctx, featureAreaValue.ID)
	}
	if errors.Is(err, task.ErrNotFound) {
		approvalErr := f.populateCurrentWorkflowApprovals(ctx, &result)
		return result, approvalErr
	}
	if err != nil {
		return CurrentWorkflow{}, MapError(err)
	}
	if taskValue.ProjectID != projectID {
		return CurrentWorkflow{}, notFound()
	}
	result.Task = &taskValue
	selectTaskPresentationReview(&result)
	result.Runs = nil
	seenRuns = map[string]bool{}
	if result.CurrentReview != nil {
		if r, ok := reviewRuns[result.CurrentReview.ID]; ok {
			result.Runs = append(result.Runs, r)
			seenRuns[r.ID] = true
		}
	}
	var executionValue executionproposal.Proposal
	executionErr := executionproposal.ErrNotFound
	if taskValue.CurrentExecutionID != "" {
		executionValue, executionErr = f.deps.ExecutionProposals.Get(ctx, aiSessionID, taskValue.CurrentExecutionID)
	}
	if executionErr != nil && !errors.Is(executionErr, executionproposal.ErrNotFound) {
		return CurrentWorkflow{}, MapError(executionErr)
	}
	planID := ""

	if executionErr == nil {
		planID = executionValue.TaskPlanID
	}
	if planID != "" {
		planValue, err := f.deps.TaskPlans.Get(ctx, aiSessionID, planID)
		if err != nil {
			return CurrentWorkflow{}, MapError(err)
		}
		if planValue.TaskID != taskValue.ID || planValue.ProjectID != projectID {
			return CurrentWorkflow{}, notFound()
		}
		result.TaskPlan = &planValue
	}
	if executionErr == nil {
		result.Execution = &executionValue
	}

	currentRun, ok := f.deps.Runs.(interface {
		LatestForTask(context.Context, string, run.Purpose, string) (run.Run, error)
	})
	if !ok {
		return CurrentWorkflow{}, errors.New("phase-aware Run read unavailable")
	}

	queries := []struct {
		purpose     run.Purpose
		executionID string
	}{}
	if result.CurrentReview == nil {
		queries = append(queries, struct {
			purpose     run.Purpose
			executionID string
		}{run.PurposeScopeReview, ""})
	}
	if result.Execution != nil {
		queries = append(queries, struct {
			purpose     run.Purpose
			executionID string
		}{run.PurposeImplementation, result.Execution.ID}, struct {
			purpose     run.Purpose
			executionID string
		}{run.PurposeVerification, result.Execution.ID})
	}
	for _, query := range queries {
		runValue, runErr := currentRun.LatestForTask(ctx, taskValue.ID, query.purpose, query.executionID)
		if errors.Is(runErr, run.ErrNotFound) {
			continue
		}
		if runErr != nil {
			return CurrentWorkflow{}, MapError(runErr)
		}
		if !seenRuns[runValue.ID] {
			result.Runs = append(result.Runs, runValue)
			seenRuns[runValue.ID] = true
		}

	}
	approvalErr := f.populateCurrentWorkflowApprovals(ctx, &result)
	return result, approvalErr
}

// Every selected engineering Run can require an operator decision, including
// Review and Development Runs.
func (f *Facade) populateCurrentWorkflowApprovals(ctx context.Context, result *CurrentWorkflow) error {
	for _, runValue := range result.Runs {
		values, err := f.deps.Approvals.ListByRun(ctx, runValue.ID)
		if err != nil {
			return MapError(err)
		}
		for _, value := range values {
			if value.Status == approval.StatusPending {
				result.ApprovalPending = true
				pending := value
				result.PendingApproval = &pending
				return nil
			}
		}
	}
	return nil
}

func (f *Facade) LatestRunForTaskForSession(ctx context.Context, aiSessionID, taskID string, purpose run.Purpose, executionID string) (run.Run, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return run.Run{}, err
	}
	taskValue, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || taskValue.ProjectID != projectID {
		return run.Run{}, notFound()
	}
	current, ok := f.deps.Runs.(interface {
		LatestForTask(context.Context, string, run.Purpose, string) (run.Run, error)
	})
	if !ok {
		return run.Run{}, errors.New("phase-aware Run read unavailable")
	}
	value, err := current.LatestForTask(ctx, taskID, purpose, executionID)
	if err != nil {
		return run.Run{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) LatestExecutionForTaskForSession(ctx context.Context, aiSessionID, taskID string) (executionproposal.Proposal, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return executionproposal.Proposal{}, err
	}
	taskValue, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || taskValue.ProjectID != projectID {
		return executionproposal.Proposal{}, notFound()
	}
	current, ok := f.deps.ExecutionProposals.(interface {
		LatestForTask(context.Context, string, string) (executionproposal.Proposal, error)
	})
	if !ok {
		return executionproposal.Proposal{}, errors.New("phase-aware Execution read unavailable")
	}
	value, err := current.LatestForTask(ctx, aiSessionID, taskID)
	if err != nil {
		return executionproposal.Proposal{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetProjectContextForSession(ctx context.Context, aiSessionID string) (projectcontext.ProjectContext, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return projectcontext.ProjectContext{}, err
	}
	value, err := f.deps.ProjectContexts.Get(ctx, projectID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) CreateModuleForSession(ctx context.Context, aiSessionID, key, name, description string, parentID *string) (module.Module, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return module.Module{}, err
	}
	if parentID != nil {
		parent, getErr := f.deps.Modules.Get(ctx, *parentID)
		if getErr != nil || parent.ProjectID != projectID {
			return module.Module{}, notFound()
		}
	}
	value, err := f.deps.Modules.Create(ctx, module.CreateInput{ProjectID: projectID, Key: key, Name: name, Description: description, ParentID: parentID})
	if err != nil {
		return module.Module{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) CreateContextProbe(ctx context.Context, input contextprobe.CreateInput) (contextprobe.Probe, error) {
	value, err := f.deps.ContextProbes.Create(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) PrepareProjectInspection(ctx context.Context, input contextprobe.PrepareInspectionInput) (contextprobe.InspectionConfirmationPlan, error) {
	service, ok := f.deps.ContextProbes.(canonicalContextProbeService)
	if !ok {
		return contextprobe.InspectionConfirmationPlan{}, &Error{Code: CodeConflict, Message: "canonical Project Inspection is unavailable"}
	}
	effort, err := f.ExecutionAllowlist().ResolveSelection(input.Model, input.ReasoningEffort)
	if err != nil {
		return contextprobe.InspectionConfirmationPlan{}, MapError(err)
	}
	input.ReasoningEffort = effort
	value, err := service.PrepareInspection(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) ConfirmProjectInspection(ctx context.Context, input contextprobe.ConfirmInspectionInput) (contextprobe.Probe, error) {
	service, ok := f.deps.ContextProbes.(canonicalContextProbeService)
	if !ok {
		return contextprobe.Probe{}, &Error{Code: CodeConflict, Message: "canonical Project Inspection is unavailable"}
	}
	if err := f.ExecutionAllowlist().ValidateSelection(input.Plan.Model, input.Plan.ReasoningEffort); err != nil {
		return contextprobe.Probe{}, MapError(err)
	}
	value, err := service.ConfirmInspection(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetContextProbeForSession(ctx context.Context, aiSessionID, probeID string) (contextprobe.Probe, error) {
	value, err := f.deps.ContextProbes.GetForSession(ctx, aiSessionID, probeID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) ListContextProbesForSession(ctx context.Context, aiSessionID string) ([]contextprobe.Probe, error) {
	values, err := f.deps.ContextProbes.ListForSession(ctx, aiSessionID)
	if err != nil {
		return nil, MapError(err)
	}
	return values, nil
}

func (f *Facade) StartContextProbe(ctx context.Context, aiSessionID, probeID string) (contextprobe.Probe, error) {
	value, err := f.deps.ContextProbeRunner.Start(ctx, aiSessionID, probeID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) StartConfirmedProjectInspection(ctx context.Context, input contextprobe.StartInspectionInput) (contextprobe.Probe, error) {
	runner, ok := f.deps.ContextProbeRunner.(canonicalContextProbeRunner)
	if !ok {
		return contextprobe.Probe{}, &Error{Code: CodeConflict, Message: "canonical Project Inspection runner is unavailable"}
	}
	if input.AISessionID == "" || input.ProjectID == "" || input.InspectionOwnerTaskID == "" || input.InspectionOperationID == "" || input.ExpectedOwnerState == "" || input.RequestID == "" || input.PayloadSHA256 == "" {
		return contextprobe.Probe{}, &Error{Code: CodeInvalidArgument, Message: "confirmed Project Inspection identity is required"}
	}
	current, err := f.deps.ContextProbes.GetForSession(ctx, input.AISessionID, input.InspectionOperationID)
	if err != nil {
		return contextprobe.Probe{}, MapError(err)
	}
	if current.ProjectID != input.ProjectID || current.TaskID != input.InspectionOwnerTaskID || current.Status != input.ExpectedOwnerState || (input.ExpectedRuntimeEpoch != "" && current.RuntimeEpoch != input.ExpectedRuntimeEpoch) || (input.ExpectedWorksetSHA256 != "" && current.RuntimeWorksetSHA256 != input.ExpectedWorksetSHA256) {
		return contextprobe.Probe{}, &Error{Code: CodeConflict, Message: "Project Inspection expected owner state changed"}
	}
	value, err := runner.StartConfirmed(ctx, input.AISessionID, input.InspectionOperationID, input.RequestID, input.PayloadSHA256)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) ClassifyTaskForSession(ctx context.Context, aiSessionID, taskID, moduleID string) (task.Task, error) {
	value, err := f.deps.Classifications.Classify(ctx, aiSessionID, taskID, moduleID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetTaskClassificationContextForSession(ctx context.Context, aiSessionID, taskID string) (TaskClassificationContext, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return TaskClassificationContext{}, err
	}
	taskValue, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || taskValue.ProjectID != projectID {
		return TaskClassificationContext{}, notFound()
	}
	moduleValue, err := f.deps.Modules.Get(ctx, taskValue.ModuleID)
	if err != nil {
		return TaskClassificationContext{}, MapError(err)
	}
	contextValue, err := f.deps.ProjectContexts.Get(ctx, projectID)
	if err != nil {
		return TaskClassificationContext{}, MapError(err)
	}
	result := TaskClassificationContext{Task: taskValue, Module: moduleValue, ProjectContext: contextValue, ChangedFiles: []string{}}
	runs, err := f.deps.Runs.List(ctx)
	if err != nil {
		return TaskClassificationContext{}, MapError(err)
	}
	for _, value := range runs {
		if value.TaskID != taskID {
			continue
		}
		if result.LatestRun == nil || value.CreatedAt.After(result.LatestRun.CreatedAt) {
			copy := value
			result.LatestRun = &copy
		}
	}
	if result.LatestRun == nil {
		return result, nil
	}
	evidenceValue, evidenceErr := f.deps.Evidence.Get(ctx, result.LatestRun.ID)
	if evidenceErr == nil {
		result.ChangedFiles, result.DiffArtifactID = append([]string(nil), evidenceValue.ChangedFiles...), evidenceValue.DiffArtifactID
	}
	artifacts, artifactErr := f.deps.Artifacts.ListByRun(ctx, result.LatestRun.ID)
	if artifactErr == nil {
		for _, value := range artifacts {
			if value.Kind == artifact.KindAgentMessage {
				result.AgentReportArtifactID = value.ID
			}
		}
	}
	return result, nil
}

// SelectProject changes only local Console view state. It does not create the
// explicit single-use record required by session_open(selectionId).
func (f *Facade) SelectProject(ctx context.Context, projectID string) (projectselection.Selection, error) {
	if err := validID("projectId", projectID); err != nil {
		return projectselection.Selection{}, err
	}
	value, err := f.deps.ProjectSelection.Select(ctx, projectID)
	if err != nil {
		return projectselection.Selection{}, MapError(err)
	}
	return value, nil
}

type CreatePlanningTaskInput struct {
	RequestID, Title                                                                                  string
	Constraints                                                                                       []string
	AISessionID, FeatureAreaID, RepositoryID, CodexConfigID, ModuleID, ProblemStatement, BaseRevision string
	SkipReview                                                                                        bool
}

func (f *Facade) ReplayPlanningTaskForSession(ctx context.Context, input CreatePlanningTaskInput) (task.Task, error) {
	project, err := f.sessionProject(ctx, input.AISessionID)
	if err != nil {
		return task.Task{}, err
	}
	reader, ok := f.deps.Tasks.(interface {
		ReplayCreate(context.Context, task.CreateInput) (task.Task, error)
	})
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	return reader.ReplayCreate(ctx, task.CreateInput{ProjectID: project, FeatureAreaID: input.FeatureAreaID, RepositoryID: input.RepositoryID, Goal: input.Title, ProblemStatement: input.ProblemStatement, Constraints: input.Constraints, RequestID: input.RequestID, SkipReview: input.SkipReview})
}

func (f *Facade) CreatePlanningTaskForSession(ctx context.Context, input CreatePlanningTaskInput) (task.Task, error) {
	if !observability.ValidRequestID(input.RequestID) {
		return task.Task{}, invalid("requestId is required")
	}
	projectID, err := f.sessionProject(ctx, input.AISessionID)
	if err != nil {
		return task.Task{}, err
	}
	if input.RequestID != "" {
		if existing, e := f.ReplayPlanningTaskForSession(ctx, input); e == nil {
			return existing, nil
		} else if !errors.Is(e, task.ErrNotFound) {
			return task.Task{}, MapError(e)
		}
	}
	if input.RepositoryID != "" {
		repositoryValue, err := f.deps.Repositories.Get(ctx, input.RepositoryID)
		if err != nil || repositoryValue.ProjectID != projectID {
			return task.Task{}, notFound()
		}
	} else if input.BaseRevision != "" {
		return task.Task{}, invalid("project Task intent cannot supply a repository baseline")
	}
	if input.FeatureAreaID == "" {
		return task.Task{}, invalid("featureAreaId is required")
	}
	{
		featureAreaValue, featureAreaErr := f.deps.FeatureAreas.GetForSession(ctx, input.AISessionID, input.FeatureAreaID)
		if featureAreaErr != nil || featureAreaValue.ProjectID != projectID || !featurearea.IsUsable(featureAreaValue) {
			return task.Task{}, notFound()
		}
	}
	value, err := f.deps.Tasks.Create(ctx, task.CreateInput{AISessionID: input.AISessionID, RequestID: input.RequestID, Goal: input.Title, Constraints: input.Constraints, ProjectID: projectID, FeatureAreaID: input.FeatureAreaID, RepositoryID: input.RepositoryID, CodexConfigID: input.CodexConfigID, ModuleID: input.ModuleID, ProblemStatement: input.ProblemStatement, BaseRevision: input.BaseRevision, Planning: true, SkipReview: input.SkipReview})
	if err != nil {
		return task.Task{}, MapError(err)
	}
	return value, nil
}

// ClassifyTaskWorkKind records the optional organization-only work kind on an
// existing Task within the caller's Project boundary. It is a pure metadata
// update: it never changes ExecutionScope, authorization,
// model/reasoning selection, Verification, or runtime behavior.
func (f *Facade) ClassifyTaskWorkKind(ctx context.Context, aiSessionID, taskID string, kind task.WorkKind) (task.Task, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return task.Task{}, err
	}
	if err := validID("taskId", taskID); err != nil {
		return task.Task{}, err
	}
	value, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || value.ProjectID != projectID {
		return task.Task{}, notFound()
	}
	classifier, ok := f.deps.Tasks.(workKindTaskService)
	if !ok {
		return task.Task{}, errors.New("work kind classification is unavailable")
	}
	updated, err := classifier.ClassifyWorkKind(ctx, taskID, kind)
	if err != nil {
		return task.Task{}, MapError(err)
	}
	return updated, nil
}

// ClassifyTaskExecutionSelection freezes the GPT-selected model and reasoning
// effort on a Task within the caller's Project boundary. The selection is
// validated against the closed runtime universe; Bridge never imputes one.
func (f *Facade) ClassifyTaskExecutionSelection(ctx context.Context, aiSessionID, taskID, model, reasoningEffort string) (task.Task, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return task.Task{}, err
	}
	if err := validID("taskId", taskID); err != nil {
		return task.Task{}, err
	}
	value, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || value.ProjectID != projectID {
		return task.Task{}, notFound()
	}
	reasoningEffort, err = f.resolveCurrentExecutionSelection(model, reasoningEffort)
	if err != nil {
		return task.Task{}, err
	}
	if err := validateExecutionSelection(model, reasoningEffort); err != nil {
		return task.Task{}, err
	}
	classifier, ok := f.deps.Tasks.(selectionTaskService)
	if !ok {
		return task.Task{}, errors.New("execution selection is unavailable")
	}
	updated, err := classifier.ClassifyExecutionSelection(ctx, taskID, model, reasoningEffort)
	if err != nil {
		return task.Task{}, MapError(err)
	}
	return updated, nil
}

func validateExecutionSelection(model, reasoningEffort string) error {
	if err := codexruntime.ValidateModelID(model); err != nil {
		return &Error{Code: CodeInvalidArgument, Message: "MODEL_NOT_ALLOWED: invalid Codex model ID", cause: err}
	}
	if !containsValue(codexruntime.AllReasoningEfforts, reasoningEffort) {
		return &Error{Code: CodeInvalidArgument, Message: "REASONING_EFFORT_NOT_ALLOWED: reasoning effort is not in the closed allowlist", cause: &codexruntime.SelectionError{Kind: codexruntime.ReasoningEffortNotAllowed, Value: reasoningEffort, Candidates: append([]string(nil), codexruntime.AllReasoningEfforts...)}}
	}
	return nil
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (f *Facade) CreateFeatureArea(ctx context.Context, input featurearea.CreateInput) (featurearea.FeatureArea, error) {
	if f.deps.FeatureAreas == nil {
		return featurearea.FeatureArea{}, errors.New("Feature Area service unavailable")
	}
	value, err := f.deps.FeatureAreas.Create(ctx, input)
	if err != nil {
		return featurearea.FeatureArea{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) CreateDocument(ctx context.Context, input document.CreateInput) (document.Document, error) {
	if f.deps.Documents == nil {
		return document.Document{}, errors.New("Document service unavailable")
	}
	value, err := f.deps.Documents.Create(ctx, input)
	if err != nil {
		return document.Document{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetFeatureAreaForSession(ctx context.Context, aiSessionID, featureAreaID string) (featurearea.FeatureArea, error) {
	if _, err := f.sessionProject(ctx, aiSessionID); err != nil {
		return featurearea.FeatureArea{}, err
	}
	if f.deps.FeatureAreas == nil {
		return featurearea.FeatureArea{}, errors.New("Feature Area service unavailable")
	}
	if history, ok := f.deps.FeatureAreas.(interface {
		GetHistoryForSession(context.Context, string, string) (featurearea.FeatureArea, error)
	}); ok {
		value, err := history.GetHistoryForSession(ctx, aiSessionID, featureAreaID)
		if err != nil {
			return value, MapError(err)
		}
		return value, nil
	}
	value, err := f.deps.FeatureAreas.GetForSession(ctx, aiSessionID, featureAreaID)
	if err != nil {
		return featurearea.FeatureArea{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) ListFeatureAreasForSession(ctx context.Context, aiSessionID string) ([]featurearea.FeatureArea, error) {
	if _, err := f.sessionProject(ctx, aiSessionID); err != nil {
		return nil, err
	}
	if f.deps.FeatureAreas == nil {
		return nil, errors.New("Feature Area service unavailable")
	}
	if history, ok := f.deps.FeatureAreas.(interface {
		ListHistoryForSession(context.Context, string) ([]featurearea.FeatureArea, error)
	}); ok {
		values, err := history.ListHistoryForSession(ctx, aiSessionID)
		if err != nil {
			return nil, MapError(err)
		}
		return values, nil
	}
	values, err := f.deps.FeatureAreas.ListForSession(ctx, aiSessionID)
	if err != nil {
		return nil, MapError(err)
	}
	return values, nil
}

// ResetFeatureAreaAgentContext rotates the FeatureArea-owned coding-agent
// conversation after proving the caller's session can access that FeatureArea.
func (f *Facade) ResetFeatureAreaAgentContext(ctx context.Context, aiSessionID, featureAreaID, requestID string) (agentconversation.Conversation, error) {
	if err := validID("featureAreaId", featureAreaID); err != nil {
		return agentconversation.Conversation{}, err
	}
	if err := validID("requestId", requestID); err != nil {
		return agentconversation.Conversation{}, err
	}
	if _, err := f.sessionProject(ctx, aiSessionID); err != nil {
		return agentconversation.Conversation{}, err
	}
	if _, err := f.deps.FeatureAreas.GetForSession(ctx, aiSessionID, featureAreaID); err != nil {
		return agentconversation.Conversation{}, MapError(err)
	}
	if f.deps.AgentConversations == nil {
		return agentconversation.Conversation{}, &Error{Code: CodeInvalidState, Message: "Feature Area agent context reset is unavailable"}
	}
	value, err := f.deps.AgentConversations.Reset(ctx, featureAreaID, requestID)
	if err != nil {
		return agentconversation.Conversation{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) FeatureAreaAgentOperationState(ctx context.Context, aiSessionID, featureAreaID string) (agentconversation.State, error) {
	if err := validID("featureAreaId", featureAreaID); err != nil {
		return agentconversation.State{}, err
	}
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return agentconversation.State{}, err
	}
	if _, err := f.GetFeatureAreaForSession(ctx, aiSessionID, featureAreaID); err != nil {
		return agentconversation.State{}, MapError(err)
	}
	service, ok := f.deps.AgentConversations.(AgentConversationRecoveryService)
	if !ok {
		return agentconversation.State{}, &Error{Code: CodeInvalidState, Message: "Feature Area agent operation recovery is unavailable"}
	}
	value, err := service.State(ctx, featureAreaID)
	if err != nil {
		return agentconversation.State{}, MapError(err)
	}
	return f.ExecutionCancellationState(ctx, projectID, value)
}

// RecoverFeatureAreaAgentOperation authorizes access to the exact FeatureArea
// before the original recovery owner checks receipts and durable fencing.
// Coding recovery abandons unsafe context; Upper recovery reauthorizes the same
// Review/Run into PREPARING, without preparing a workspace or restoring access.
func (f *Facade) RecoverFeatureAreaAgentOperation(ctx context.Context, aiSessionID string, input agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	for name, value := range map[string]string{"featureAreaId": input.FeatureAreaID, "operationId": input.OperationID, "runId": input.RunID, "requestId": input.RequestID} {
		if err := validID(name, value); err != nil {
			return agentconversation.RecoveryResult{}, err
		}
	}
	if input.OperationKind != agentconversation.Review && input.OperationKind != agentconversation.Execution {
		return agentconversation.RecoveryResult{}, &Error{Code: CodeInvalidArgument, Message: "invalid operationKind"}
	}
	if input.AgentConversationID == "" && input.OperationKind != agentconversation.Review {
		return agentconversation.RecoveryResult{}, &Error{Code: CodeInvalidArgument, Message: "agentConversationId is required for this operation"}
	}
	if input.AgentConversationID != "" {
		if err := validID("agentConversationId", input.AgentConversationID); err != nil {
			return agentconversation.RecoveryResult{}, err
		}
	}
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return agentconversation.RecoveryResult{}, err
	}
	if input.ProjectID != "" && input.ProjectID != projectID {
		return agentconversation.RecoveryResult{}, notFound()
	}
	input.ProjectID = projectID
	if _, err := f.GetFeatureAreaForSession(ctx, aiSessionID, input.FeatureAreaID); err != nil {
		return agentconversation.RecoveryResult{}, MapError(err)
	}
	service, ok := f.deps.AgentConversations.(AgentConversationRecoveryService)
	if !ok {
		return agentconversation.RecoveryResult{}, &Error{Code: CodeInvalidState, Message: "Feature Area agent operation recovery is unavailable"}
	}
	value, err := service.Recover(ctx, input)
	if err != nil {
		return agentconversation.RecoveryResult{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) SearchRepositoryForSession(ctx context.Context, aiSessionID, repositoryID, query string, paths []string, limit int) (repository.SearchResult, error) {
	// Unscoped source search is not an alternate entrance around an active
	// Upper Review's immutable read boundary. Use its controlled file tools.
	if guard, ok := f.deps.UpperReviewAccess.(interface {
		CheckUnscopedRepositorySearch(context.Context, string, string) error
	}); ok {
		if err := guard.CheckUnscopedRepositorySearch(ctx, aiSessionID, repositoryID); err != nil {
			return repository.SearchResult{}, MapError(err)
		}
	}

	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return repository.SearchResult{}, err
	}
	value, err := f.deps.Repositories.Get(ctx, repositoryID)
	if err != nil || value.ProjectID != projectID {
		return repository.SearchResult{}, notFound()
	}
	reviewSearch, ok := f.deps.Repositories.(interface {
		SearchReview(context.Context, string, string, []string, int) (repository.SearchResult, error)
	})
	if !ok {
		return repository.SearchResult{}, errors.New("Review repository search unavailable")
	}
	result, err := reviewSearch.SearchReview(ctx, repositoryID, query, paths, limit)
	if err != nil {
		return repository.SearchResult{}, MapError(err)
	}
	return result, nil
}

func (f *Facade) CreateTaskPlan(ctx context.Context, input taskplan.CreateInput) (taskplan.Plan, error) {
	value, err := f.deps.TaskPlans.Create(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetTaskPlan(ctx context.Context, sessionID, id string) (taskplan.Plan, error) {
	value, err := f.deps.TaskPlans.Get(ctx, sessionID, id)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) ListTaskPlans(ctx context.Context, sessionID string, options ListOptions) ([]taskplan.Plan, error) {
	values, err := f.deps.TaskPlans.List(ctx, sessionID, taskplan.ListOptions{Limit: options.Limit, Offset: options.Offset})
	if err != nil {
		return nil, MapError(err)
	}
	return values, nil
}
func (f *Facade) RejectTaskPlan(ctx context.Context, sessionID, id string) (taskplan.Plan, error) {
	value, err := f.deps.TaskPlans.Reject(ctx, sessionID, id)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) StartScopeReview(ctx context.Context, input scopereview.StartInput) (run.Run, error) {
	value, err := f.deps.ScopeReviews.Start(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

// StartAdmittedReview is the admission-aware product entry point. All
// semantic and ownership validation happens before the Scope Review service
// commits an idempotent command receipt and, only when free, its Review/Run.
func (f *Facade) StartAdmittedReview(ctx context.Context, input scopereview.AdmittedStartInput) (review.StartAdmission, error) {
	return f.startAdmittedReview(ctx, input, false)
}
func (f *Facade) startAdmittedReview(ctx context.Context, input scopereview.AdmittedStartInput, frozen bool) (review.StartAdmission, error) {
	projectID := ""
	var err error
	if input.AISessionID != "" {
		projectID, err = f.sessionProject(ctx, input.AISessionID)
		if err != nil {
			return review.StartAdmission{}, err
		}
	} else {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || input.OperatorPrincipalID == "" || actor.PrincipalID() != input.OperatorPrincipalID {
			return review.StartAdmission{}, notFound()
		}
		projectID = actor.ProjectID()
	}
	featureAreaValue, err := f.deps.FeatureAreas.GetForSession(ctx, input.AISessionID, input.Review.FeatureAreaID)
	if err != nil || featureAreaValue.ProjectID != projectID || input.Review.ProjectID != projectID {
		return review.StartAdmission{}, notFound()
	}
	if !featurearea.IsUsable(featureAreaValue) {
		return review.StartAdmission{}, MapError(featurearea.ErrUnavailable)
	}
	input.Review.ExecutorKind = input.Review.ExecutorKind.Effective()
	if !input.Review.ExecutorKind.Valid() {
		return review.StartAdmission{}, MapError(review.ErrExecutorMismatch)
	}
	if input.Review.ExecutorKind == review.ExecutorUpperLLM {
		if input.Review.Model != "" || input.Review.ReasoningEffort != "" || input.Review.CodexConfigID != "" || input.Review.BackendSelection != nil {
			return review.StartAdmission{}, MapError(review.ErrExecutorMismatch)
		}
	} else {
		if input.Review.BackendSelection == nil || input.Review.CodexConfigID != input.Review.BackendSelection.ConfigID {
			return review.StartAdmission{}, invalid("Frozen Coding Review backend selection is required")
		}
		if err := f.validateFrozenBackendSelection(ctx, input.AISessionID, projectID, input.Review.FeatureAreaID, input.Review.BackendSelection); err != nil {
			return review.StartAdmission{}, invalid(err.Error())
		}
		input.Review.ReasoningEffort, err = f.resolveCurrentExecutionSelection(input.Review.Model, input.Review.ReasoningEffort)
		if err != nil {
			return review.StartAdmission{}, err
		}
	}
	if len(input.Review.RepositoryTargets) == 0 {
		repositoryValue, err := f.deps.Repositories.Get(ctx, input.Review.RepositoryID)
		if err != nil || repositoryValue.ProjectID != projectID {
			return review.StartAdmission{}, notFound()
		}
		// Admission captures the selected integration branch, never the repository
		// checkout or the evolving FeatureArea working branch. Workspace origin is
		// immutable; later Review Runs record the actual shared-workspace snapshots.
		if f.deps.DevelopmentCycles != nil {
			development, e := f.deps.DevelopmentCycles.Context(ctx, projectID)
			if e != nil {
				return review.StartAdmission{}, MapError(e)
			}
			var selected bool
			for _, repository := range development.Repositories {
				if repository.RepositoryID != input.Review.RepositoryID {
					continue
				}
				if repository.DevelopmentBranchSelectionRequired {
					return review.StartAdmission{}, MapError(developmentcycle.ErrNotSelected)
				}
				if repository.CurrentHeadSHA == "" {
					return review.StartAdmission{}, MapError(developmentcycle.ErrRefMissing)
				}
				if frozen {
					p := input.Review.RepositoryProvenance
					if p == nil || input.Review.BaseCommit != repository.CurrentHeadSHA || p.BranchRef != repository.DevelopmentRef {
						return review.StartAdmission{}, invalid("Frozen development context changed; this request cannot select a new baseline")
					}
					selected = true
					continue
				}
				input.Review.BaseCommit = repository.CurrentHeadSHA
				input.Review.RepositoryProvenance = &review.RepositoryProvenance{RepositoryID: repository.RepositoryID, Commit: repository.CurrentHeadSHA, RepositoryHEAD: repository.CurrentHeadSHA, BranchKind: review.ProvenanceBranch, BranchRef: repository.DevelopmentRef, InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: time.Now().UTC()}
				selected = true
			}
			if !selected {
				return review.StartAdmission{}, MapError(developmentcycle.ErrNotSelected)
			}
		}
	} else {
		if input.Review.WorkspaceID == "" || input.Review.RepositoryID != "" || input.Review.BaseCommit != "" {
			return review.StartAdmission{}, invalid("exact read-only Review workset required")
		}
	}
	admissions, ok := f.deps.ScopeReviews.(interface {
		StartAdmitted(context.Context, scopereview.AdmittedStartInput) (review.StartAdmission, error)
	})
	if !ok {
		return review.StartAdmission{}, errors.New("Review admission is unavailable")
	}
	value, err := admissions.StartAdmitted(ctx, input)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) CreateReview(ctx context.Context, value review.Review) (review.Review, error) {
	projectID, err := f.sessionProject(ctx, value.CreatedByAISessionID)
	if err != nil {
		return review.Review{}, err
	}
	featureAreaValue, err := f.deps.FeatureAreas.GetForSession(ctx, value.CreatedByAISessionID, value.FeatureAreaID)
	if err != nil || featureAreaValue.ProjectID != projectID || value.ProjectID != projectID {
		return review.Review{}, notFound()
	}
	if !featurearea.IsUsable(featureAreaValue) {
		return review.Review{}, MapError(featurearea.ErrUnavailable)
	}
	value.ReasoningEffort, err = f.resolveCurrentExecutionSelection(value.Model, value.ReasoningEffort)
	if err != nil {
		return review.Review{}, err
	}
	repositoryValue, err := f.deps.Repositories.Get(ctx, value.RepositoryID)
	if err != nil || repositoryValue.ProjectID != projectID {
		return review.Review{}, notFound()
	}
	created, err := f.deps.Reviews.Create(ctx, value)
	if err != nil {
		return review.Review{}, MapError(err)
	}
	return created, nil
}

// resolveCurrentExecutionSelection only supplies an omitted effort when the
// operation explicitly selects a model. The resulting concrete pair is frozen
// by the existing operation authority, never reread at execution start.
func (f *Facade) resolveCurrentExecutionSelection(model, effort string) (string, error) {
	allowlist := f.ExecutionAllowlist()
	if model == "" {
		if effort == "" {
			return "", nil
		}
		return "", &Error{Code: CodeInvalidArgument, Message: "MODEL_REQUIRED: reasoning effort requires a selected model"}
	}
	resolved, err := allowlist.ResolveSelection(model, effort)
	if err != nil {
		return "", &Error{Code: CodeInvalidArgument, Message: err.Error(), cause: err}
	}
	return resolved, nil
}

func (f *Facade) validateCurrentExecutionSelection(model, effort string) error {
	_, err := f.resolveCurrentExecutionSelection(model, effort)
	return err
}

func (f *Facade) GetReviewForSession(ctx context.Context, aiSessionID, reviewID string) (review.Review, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return review.Review{}, err
	}
	value, err := f.deps.Reviews.Get(ctx, reviewID)
	if err != nil || value.ProjectID != projectID {
		return review.Review{}, notFound()
	}
	if _, err := f.GetFeatureAreaForSession(ctx, aiSessionID, value.FeatureAreaID); err != nil {
		return review.Review{}, notFound()
	}
	return value, nil
}

// ReviewRepositoryDriftForSession exposes objective Git drift facts between
// the repository state one Project Review was based on and the current
// repository state. It is factual: Bridge never derives staleness, validity,
// or rerun requirements from it.
func (f *Facade) ReviewRepositoryDriftForSession(ctx context.Context, aiSessionID, reviewID string, limit int) (review.RepositoryDrift, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return review.RepositoryDrift{}, err
	}
	return f.ReviewRepositoryDrift(ctx, projectID, reviewID, limit)
}

// ReviewRepositoryDrift is the Project-scoped form used by the Console.
func (f *Facade) ReviewRepositoryDrift(ctx context.Context, projectID, reviewID string, limit int) (review.RepositoryDrift, error) {
	reviewValue, err := f.deps.Reviews.Get(ctx, reviewID)
	if err != nil {
		return review.RepositoryDrift{}, MapError(err)
	}
	if reviewValue.ProjectID != projectID {
		return review.RepositoryDrift{}, notFound()
	}
	if f.drift == nil {
		return review.RepositoryDrift{Available: false, UnavailableReason: "review drift facts are unavailable in this deployment"}, nil
	}
	return f.drift.RepositoryDrift(ctx, reviewID, limit)
}

func (f *Facade) ListReviewsForFeatureArea(ctx context.Context, aiSessionID, featureAreaID string) ([]review.Review, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return nil, err
	}
	if _, err := f.GetFeatureAreaForSession(ctx, aiSessionID, featureAreaID); err != nil {
		return nil, notFound()
	}
	values, err := f.deps.Reviews.ListByFeatureArea(ctx, featureAreaID)
	if err != nil {
		return nil, MapError(err)
	}
	for _, value := range values {
		if value.ProjectID != projectID {
			return nil, notFound()
		}
	}
	return values, nil
}

func (f *Facade) LinkReviewRun(ctx context.Context, reviewID, runID string) error {
	if err := f.deps.Reviews.LinkRun(ctx, reviewID, runID); err != nil {
		return MapError(err)
	}
	return nil
}

func (f *Facade) RecordReviewStartFailure(ctx context.Context, reviewID, code, blocker string) error {
	if err := f.deps.Reviews.RecordStartFailure(ctx, reviewID, code, blocker); err != nil {
		return MapError(err)
	}
	return nil
}

func (f *Facade) ReviewEvidenceForSession(ctx context.Context, aiSessionID, reviewID string) (review.EvidenceSummary, error) {
	if _, err := f.GetReviewForSession(ctx, aiSessionID, reviewID); err != nil {
		return review.EvidenceSummary{}, err
	}
	value, err := f.deps.Reviews.SummarizeEvidence(ctx, reviewID)
	if err != nil {
		return review.EvidenceSummary{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) ClaimReviewContinuationForSession(ctx context.Context, aiSessionID, reviewID string) (bool, error) {
	v, err := f.GetReviewForSession(ctx, aiSessionID, reviewID)
	if err != nil {
		return false, err
	}
	if v.ExecutorKind == review.ExecutorUpperLLM {
		return false, nil
	}
	claimed, err := f.deps.Reviews.ClaimContinuation(ctx, reviewID)
	if err != nil {
		return false, MapError(err)
	}
	return claimed, nil
}

func (f *Facade) CreateScopeExpansion(ctx context.Context, input scopeexpansion.CreateInput) (scopeexpansion.Expansion, error) {
	return scopeexpansion.Expansion{}, &Error{Code: CodeConflict, Message: "SCOPE_EXPANSION_LEGACY_ONLY: engineering scope decisions belong to the Upper AI"}
}

func (f *Facade) GetScopeExpansion(ctx context.Context, aiSessionID, expansionID string) (scopeexpansion.Expansion, error) {
	value, err := f.deps.ScopeExpansions.Get(ctx, aiSessionID, expansionID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) ListScopeExpansions(ctx context.Context, aiSessionID string, options ListOptions) ([]scopeexpansion.Expansion, error) {
	values, err := f.deps.ScopeExpansions.List(ctx, aiSessionID, scopeexpansion.ListOptions{Limit: options.Limit, Offset: options.Offset})
	if err != nil {
		return nil, MapError(err)
	}
	return values, nil
}

func (f *Facade) AllowScopeExpansion(ctx context.Context, aiSessionID, expansionID string) (scopeexpansion.Expansion, error) {
	return scopeexpansion.Expansion{}, &Error{Code: CodeConflict, Message: "SCOPE_EXPANSION_LEGACY_ONLY: engineering scope decisions belong to the Upper AI"}
}

func (f *Facade) RejectScopeExpansion(ctx context.Context, aiSessionID, expansionID string) (scopeexpansion.Expansion, error) {
	return scopeexpansion.Expansion{}, &Error{Code: CodeConflict, Message: "SCOPE_EXPANSION_LEGACY_ONLY: engineering scope decisions belong to the Upper AI"}
}

func (f *Facade) CurrentReviewScope(ctx context.Context, aiSessionID, reviewID string) (run.ReviewScope, bool, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return run.ReviewScope{}, false, err
	}
	reviewValue, err := f.deps.Reviews.Get(ctx, reviewID)
	if err != nil || reviewValue.ProjectID != projectID {
		return run.ReviewScope{}, false, notFound()
	}
	value, ok, err := f.deps.ScopeExpansions.CurrentAuthorizedScope(ctx, reviewID)
	if err != nil {
		return run.ReviewScope{}, false, MapError(err)
	}
	return value, ok, nil
}

func (f *Facade) CurrentSelectedProject(ctx context.Context) (projectselection.Selection, error) {
	value, err := f.deps.ProjectSelection.Current(ctx)
	if err != nil {
		return projectselection.Selection{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) CreateExecutionProposal(ctx context.Context, input executionproposal.CreateInput) (executionproposal.Proposal, error) {
	value, err := f.deps.ExecutionProposals.Create(ctx, input)
	if err != nil {
		return executionproposal.Proposal{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) ConfirmTask(ctx context.Context, aiSessionID, taskPlanID string) (executionproposal.TaskConfirmationResult, error) {
	value, err := f.deps.ExecutionProposals.ConfirmTask(ctx, aiSessionID, taskPlanID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetDecisionResolution(ctx context.Context, aiSessionID, decisionID string) (decisionresolution.Receipt, error) {
	if f.deps.DecisionResolutions == nil {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	return f.deps.DecisionResolutions.Get(ctx, aiSessionID, decisionID)
}

func (f *Facade) GetDecisionResolutionByRequest(ctx context.Context, aiSessionID, requestID string) (decisionresolution.Receipt, error) {
	if f.deps.DecisionResolutions == nil {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	return f.deps.DecisionResolutions.GetByRequest(ctx, aiSessionID, requestID)
}

func (f *Facade) GetDecisionResolutionByRequestForOperator(ctx context.Context, projectID, requestID string) (decisionresolution.Receipt, error) {
	if err := localoperator.RequireProject(ctx, projectID); err != nil || f.deps.DecisionResolutions == nil {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	reader, ok := f.deps.DecisionResolutions.(interface {
		GetByProjectRequest(context.Context, string, string) (decisionresolution.Receipt, error)
	})
	if !ok {
		return decisionresolution.Receipt{}, decisionresolution.ErrNotFound
	}
	return reader.GetByProjectRequest(ctx, projectID, requestID)
}

func (f *Facade) GetExecutionProposal(ctx context.Context, aiSessionID, proposalID string) (executionproposal.Proposal, error) {
	value, err := f.deps.ExecutionProposals.Get(ctx, aiSessionID, proposalID)
	if err != nil {
		return executionproposal.Proposal{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) ListExecutionProposals(ctx context.Context, aiSessionID string, options ListOptions) ([]executionproposal.Proposal, error) {
	values, err := f.deps.ExecutionProposals.List(ctx, aiSessionID, executionproposal.ListOptions{Limit: options.Limit, Offset: options.Offset})
	if err != nil {
		return nil, MapError(err)
	}
	return values, nil
}

func (f *Facade) RejectExecutionProposal(ctx context.Context, aiSessionID, proposalID string) (executionproposal.Proposal, error) {
	value, err := f.deps.ExecutionProposals.Reject(ctx, aiSessionID, proposalID)
	if err != nil {
		return executionproposal.Proposal{}, MapError(err)
	}
	return value, nil
}

func (f *Facade) AuthorizeAndStartExecution(ctx context.Context, aiSessionID, proposalID string) (executionproposal.StartResult, error) {
	value, err := f.deps.ExecutionProposals.AuthorizeAndStart(ctx, aiSessionID, proposalID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

// DefineTaskGoalsForSession stores the optional ordered Task Goals exactly
// once for a Task within the caller's Project boundary. Definition is a pure
// authoring operation: it never changes Task lifecycle or execution state.
func (f *Facade) DefineTaskGoalsForSession(ctx context.Context, aiSessionID, taskID string, titles []string) ([]task.Goal, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return nil, err
	}
	if err := validID("taskId", taskID); err != nil {
		return nil, err
	}
	taskValue, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || taskValue.ProjectID != projectID {
		return nil, notFound()
	}
	goalSetter, ok := f.deps.Tasks.(interface {
		DefineGoals(context.Context, string, []task.GoalInput) ([]task.Goal, error)
		ListGoals(context.Context, string) ([]task.Goal, error)
	})
	if !ok {
		return nil, errors.New("task goal definitions are unavailable")
	}
	inputs := make([]task.GoalInput, 0, len(titles))
	for _, title := range titles {
		inputs = append(inputs, task.GoalInput{Title: title})
	}
	values, err := goalSetter.DefineGoals(ctx, taskID, inputs)
	if err != nil {
		return nil, MapError(err)
	}
	return values, nil
}

// ExecutionGoalProgressForSession returns the observable Goal progress for an
// Execution visible in the caller's Project boundary.
func (f *Facade) ExecutionGoalProgressForSession(ctx context.Context, aiSessionID, executionID string) ([]taskgoal.Progress, error) {
	if err := validID("executionId", executionID); err != nil {
		return nil, err
	}
	// The session-scoped proposal read enforces the Project boundary.
	if _, err := f.deps.ExecutionProposals.Get(ctx, aiSessionID, executionID); err != nil {
		return nil, MapError(err)
	}
	goals, err := f.deps.ExecutionGoals.ListForExecution(ctx, executionID)
	if err != nil {
		return nil, MapError(err)
	}
	return goals, nil
}

func (f *Facade) ListProjects(ctx context.Context, options ListOptions) ([]ProjectSummary, error) {
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	projects, err := f.deps.Projects.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	repositories, err := f.deps.Repositories.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	counts := map[string]int{}
	for _, value := range repositories {
		counts[value.ProjectID]++
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	projects = page(projects, offset, limit)
	result := make([]ProjectSummary, 0, len(projects))
	for _, value := range projects {
		result = append(result, ProjectSummary{Project: value, RepositoryCount: counts[value.ID]})
	}
	return result, nil
}
func (f *Facade) GetProject(ctx context.Context, id string) (ProjectDetails, error) {
	if err := validID("projectId", id); err != nil {
		return ProjectDetails{}, err
	}
	value, err := f.deps.Projects.Get(ctx, id)
	if err != nil {
		return ProjectDetails{}, MapError(err)
	}
	repositories, err := f.deps.Repositories.List(ctx)
	if err != nil {
		return ProjectDetails{}, MapError(err)
	}
	filtered := repositories[:0]
	for _, repositoryValue := range repositories {
		if repositoryValue.ProjectID == id {
			filtered = append(filtered, repositoryValue)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	return ProjectDetails{Project: value, Repositories: filtered}, nil
}
func (f *Facade) ListRepositories(ctx context.Context, projectID string, options ListOptions) ([]repository.Repository, error) {
	if err := validID("projectId", projectID); err != nil {
		return nil, err
	}
	if _, err := f.deps.Projects.Get(ctx, projectID); err != nil {
		return nil, MapError(err)
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := f.deps.Repositories.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	filtered := values[:0]
	for _, value := range values {
		if value.ProjectID == projectID {
			filtered = append(filtered, value)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	return page(filtered, offset, limit), nil
}

// CreateTask is the retired pre-receipt API. Production Task creation must use
// CreatePlanningTaskForSession, which requires a durable requestId receipt.
func (f *Facade) CreateTask(context.Context, CreateTaskRequest) (task.Task, error) {
	return task.Task{}, invalid("legacy Task creation is retired; use the canonical Task-create command")
}
func (f *Facade) GetTask(ctx context.Context, id string) (task.Task, error) {
	if err := validID("taskId", id); err != nil {
		return task.Task{}, err
	}
	value, err := f.deps.Tasks.Get(ctx, id)
	if err != nil {
		return task.Task{}, MapError(err)
	}
	if err := f.EnrichTaskExecution(ctx, &value); err != nil {
		return task.Task{}, err
	}
	return value, nil
}
func (f *Facade) ListTasks(ctx context.Context, filter TaskFilter) ([]task.Task, error) {
	limit, offset, err := boundedList(filter.ListOptions)
	if err != nil {
		return nil, err
	}
	if filter.ProjectID != "" {
		if err := validID("projectId", filter.ProjectID); err != nil {
			return nil, err
		}
	}
	if filter.RepositoryID != "" {
		if err := validID("repositoryId", filter.RepositoryID); err != nil {
			return nil, err
		}
	}
	if filter.Status != "" && !validTaskStatus(filter.Status) {
		return nil, invalid("unknown task status")
	}
	values, err := f.deps.Tasks.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	filtered := values[:0]
	for _, value := range values {
		if filter.ProjectID != "" && value.ProjectID != filter.ProjectID {
			continue
		}
		if filter.RepositoryID != "" && value.RepositoryID != filter.RepositoryID {
			continue
		}
		if filter.Status != "" && value.Status != filter.Status {
			continue
		}
		if err := f.EnrichTaskExecution(ctx, &value); err != nil {
			return nil, err
		}
		filtered = append(filtered, value)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	return page(filtered, offset, limit), nil
}
func (f *Facade) StartRun(ctx context.Context, taskID string) (run.Run, error) {
	if err := validID("taskId", taskID); err != nil {
		return run.Run{}, err
	}
	value, err := f.deps.Runner.StartRun(ctx, taskID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) CancelRun(ctx context.Context, runID string) (run.Run, error) {
	value, err := f.GetRun(ctx, runID)
	if err != nil {
		return run.Run{}, err
	}
	return f.cancelLegacyRun(ctx, value)
}

// Workflow-owned Runs must use their exact durable application operation.
// Keep this compatibility seam for legacy standalone Runs only; old HTTP/MCP
// clients must not bypass execution_cancel by supplying just a Run ID.
func (f *Facade) cancelLegacyRun(ctx context.Context, target run.Run) (run.Run, error) {
	if target.ExecutionProposalID != "" || target.OperationKind == run.OperationExecution {
		return run.Run{}, invalid("Execution-owned Runs require execution_cancel with aiSessionId, executionId, runId, and requestId")
	}
	if target.OperationKind != "" || target.OperationID != "" || target.ReviewID != "" {
		return run.Run{}, invalid("legacy run cancellation does not support workflow-owned Runs")
	}
	value, err := f.deps.Runner.CancelRun(ctx, target.ID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}
func (f *Facade) GetRun(ctx context.Context, id string) (run.Run, error) {
	if err := validID("runId", id); err != nil {
		return run.Run{}, err
	}
	value, err := f.deps.Runs.Get(ctx, id)
	if err != nil {
		return run.Run{}, MapError(err)
	}
	return value, nil
}
func (f *Facade) ListRuns(ctx context.Context, filter RunFilter) ([]run.Run, error) {
	limit, offset, err := boundedList(filter.ListOptions)
	if err != nil {
		return nil, err
	}
	if filter.TaskID != "" {
		if err := validID("taskId", filter.TaskID); err != nil {
			return nil, err
		}
	}
	if filter.Status != "" && !validRunStatus(filter.Status) {
		return nil, invalid("unknown run status")
	}
	values, err := f.deps.Runs.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	filtered := values[:0]
	for _, value := range values {
		if filter.TaskID != "" && value.TaskID != filter.TaskID {
			continue
		}
		if filter.Status != "" && value.Status != filter.Status {
			continue
		}
		filtered = append(filtered, value)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].TaskID == filtered[j].TaskID {
			return filtered[i].Attempt < filtered[j].Attempt
		}
		return filtered[i].TaskID < filtered[j].TaskID
	})
	return page(filtered, offset, limit), nil
}

// GetRunForSession returns a Run only when its Task belongs to the Project
// immutably bound to aiSessionID. A foreign Run is deliberately indistinguishable
// from a missing Run.
func (f *Facade) GetRunForSession(ctx context.Context, aiSessionID, runID string) (run.Run, error) {
	value, _, err := f.scopedRun(ctx, aiSessionID, runID)
	if err == nil && value.Result != nil {
		if observed, evidenceErr := f.deps.Evidence.Get(ctx, value.ID); evidenceErr == nil {
			value.Result.GitEvidence = observed
		}
		if value.Result.GitEvidence.ChangedFiles == nil {
			value.Result.GitEvidence.ChangedFiles = []string{}
		}
	}
	return value, err
}

func (f *Facade) ListRunsForSession(ctx context.Context, aiSessionID string, filter RunFilter) ([]run.Run, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(filter.ListOptions)
	if err != nil {
		return nil, err
	}
	if filter.TaskID != "" {
		if err := validID("taskId", filter.TaskID); err != nil {
			return nil, err
		}
		taskValue, getErr := f.deps.Tasks.Get(ctx, filter.TaskID)
		if getErr != nil || taskValue.ProjectID != projectID {
			return nil, notFound()
		}
	}
	if filter.Status != "" && !validRunStatus(filter.Status) {
		return nil, invalid("unknown run status")
	}
	tasks, err := f.deps.Tasks.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	allowedTasks := make(map[string]struct{}, len(tasks))
	for _, value := range tasks {
		if value.ProjectID == projectID {
			allowedTasks[value.ID] = struct{}{}
		}
	}
	values, err := f.deps.Runs.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	filtered := make([]run.Run, 0, len(values))
	for _, value := range values {
		if _, ok := allowedTasks[value.TaskID]; !ok && value.ProjectID != projectID {
			continue
		}
		if filter.TaskID != "" && value.TaskID != filter.TaskID {
			continue
		}
		if filter.Status != "" && value.Status != filter.Status {
			continue
		}
		filtered = append(filtered, value)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	return page(filtered, offset, limit), nil
}

func (f *Facade) CancelRunForSession(ctx context.Context, aiSessionID, runID string) (run.Run, error) {
	value, _, err := f.scopedRun(ctx, aiSessionID, runID)
	if err != nil {
		return run.Run{}, err
	}
	return f.cancelLegacyRun(ctx, value)
}

func (f *Facade) GetApproval(ctx context.Context, id string) (approval.Approval, error) {
	if err := validID("approvalId", id); err != nil {
		return approval.Approval{}, err
	}
	value, err := f.deps.Approvals.Get(ctx, id)
	if err != nil {
		return approval.Approval{}, MapError(err)
	}
	return value, nil
}
func (f *Facade) ListApprovals(ctx context.Context, runID string, options ListOptions) ([]approval.Approval, error) {
	if err := validID("runId", runID); err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	if _, err := f.deps.Runs.Get(ctx, runID); err != nil {
		return nil, MapError(err)
	}
	values, err := f.deps.Approvals.ListByRun(ctx, runID)
	if err != nil {
		return nil, MapError(err)
	}
	return page(values, offset, limit), nil
}
func (f *Facade) ListPendingApprovals(ctx context.Context, runID string, options ListOptions) ([]approval.Approval, error) {
	if err := validID("runId", runID); err != nil {
		return nil, err
	}
	if _, err := f.deps.Runs.Get(ctx, runID); err != nil {
		return nil, MapError(err)
	}
	values, err := f.deps.Approvals.ListByRun(ctx, runID)
	if err != nil {
		return nil, MapError(err)
	}
	pending := values[:0]
	for _, value := range values {
		if value.Status == approval.StatusPending {
			pending = append(pending, value)
		}
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	return page(pending, offset, limit), nil
}
func (f *Facade) DecideApproval(ctx context.Context, id string, decision approval.Decision) (approval.Approval, error) {
	if err := validID("approvalId", id); err != nil {
		return approval.Approval{}, err
	}
	value, err := f.deps.Approvals.Decide(ctx, id, decision)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) GetApprovalForSession(ctx context.Context, aiSessionID, approvalID string) (approval.Approval, error) {
	if err := validID("approvalId", approvalID); err != nil {
		return approval.Approval{}, err
	}
	value, err := f.deps.Approvals.Get(ctx, approvalID)
	if err != nil {
		return approval.Approval{}, MapError(err)
	}
	if _, _, err := f.scopedRun(ctx, aiSessionID, value.RunID); err != nil {
		return approval.Approval{}, err
	}
	return value, nil
}

func (f *Facade) ListApprovalsForSession(ctx context.Context, aiSessionID string, filter ApprovalFilter) ([]approval.Approval, error) {
	limit, offset, err := boundedList(filter.ListOptions)
	if err != nil {
		return nil, err
	}
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return nil, err
	}
	var runs []run.Run
	if filter.RunID != "" {
		value, _, scopedErr := f.scopedRun(ctx, aiSessionID, filter.RunID)
		if scopedErr != nil {
			return nil, scopedErr
		}
		runs = []run.Run{value}
	} else {
		tasks, listErr := f.deps.Tasks.List(ctx)
		if listErr != nil {
			return nil, MapError(listErr)
		}
		allowedTasks := make(map[string]struct{}, len(tasks))
		for _, value := range tasks {
			if value.ProjectID == projectID {
				allowedTasks[value.ID] = struct{}{}
			}
		}
		allRuns, listErr := f.deps.Runs.List(ctx)
		if listErr != nil {
			return nil, MapError(listErr)
		}
		for _, value := range allRuns {
			if value.ProjectID != "" {
				if value.ProjectID == projectID {
					runs = append(runs, value)
				}
				continue
			}
			if _, ok := allowedTasks[value.TaskID]; ok {
				runs = append(runs, value)
			}
		}
	}
	values := make([]approval.Approval, 0)
	for _, runValue := range runs {
		byRun, listErr := f.deps.Approvals.ListByRun(ctx, runValue.ID)
		if listErr != nil {
			return nil, MapError(listErr)
		}
		for _, value := range byRun {
			if filter.PendingOnly && value.Status != approval.StatusPending {
				continue
			}
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	return page(values, offset, limit), nil
}

func (f *Facade) DecideApprovalForSession(ctx context.Context, aiSessionID, approvalID string, decision approval.Decision) (approval.Approval, error) {
	if _, err := f.GetApprovalForSession(ctx, aiSessionID, approvalID); err != nil {
		return approval.Approval{}, err
	}
	value, err := f.deps.Approvals.Decide(ctx, approvalID, decision)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func (f *Facade) sessionProject(ctx context.Context, aiSessionID string) (string, error) {
	if f.operatorProject != "" {
		if aiSessionID != "" {
			return "", invalid("Operator access cannot impersonate an AI Session")
		}
		if err := localoperator.RequireProject(ctx, f.operatorProject); err != nil {
			return "", err
		}
		return f.operatorProject, nil
	}
	if err := validID("aiSessionId", aiSessionID); err != nil {
		return "", err
	}
	var value aisession.Session
	var err error
	if owned, ok := f.deps.AISessions.(interface {
		GetOwned(context.Context, string) (aisession.Session, error)
	}); ok {
		value, err = owned.GetOwned(ctx, aiSessionID)
	} else {
		return "", &Error{Code: CodePermissionDenied, Message: "Authenticated Session ownership verification is unavailable"}
	}
	if err != nil {
		return "", MapError(err)
	}
	if value.ProjectID == "" {
		return "", MapError(aisession.ErrProjectUnbound)
	}
	return value.ProjectID, nil
}

func (f *Facade) scopedRun(ctx context.Context, aiSessionID, runID string) (run.Run, task.Task, error) {
	projectID, err := f.sessionProject(ctx, aiSessionID)
	if err != nil {
		return run.Run{}, task.Task{}, err
	}
	if err := validID("runId", runID); err != nil {
		return run.Run{}, task.Task{}, err
	}
	runValue, err := f.deps.Runs.Get(ctx, runID)
	if err != nil {
		return run.Run{}, task.Task{}, MapError(err)
	}
	if runValue.ProjectID != "" {
		if runValue.ProjectID != projectID {
			return run.Run{}, task.Task{}, notFound()
		}
		return runValue, task.Task{}, nil
	}
	taskValue, err := f.deps.Tasks.Get(ctx, runValue.TaskID)
	if err != nil || taskValue.ProjectID != projectID {
		return run.Run{}, task.Task{}, notFound()
	}
	return runValue, taskValue, nil
}

func notFound() *Error {
	return &Error{Code: CodeNotFound, Message: "requested control-plane entity was not found"}
}

func (f *Facade) GetEvidence(ctx context.Context, runID string) (EvidenceDetails, error) {
	if err := validID("runId", runID); err != nil {
		return EvidenceDetails{}, err
	}
	if _, err := f.deps.Runs.Get(ctx, runID); err != nil {
		return EvidenceDetails{}, MapError(err)
	}
	value, err := f.deps.Evidence.Get(ctx, runID)
	if err != nil {
		return EvidenceDetails{}, MapError(err)
	}
	artifacts, err := f.deps.Artifacts.ListByRun(ctx, runID)
	if err != nil {
		return EvidenceDetails{}, MapError(err)
	}
	return EvidenceDetails{Evidence: value, Artifacts: artifacts}, nil
}
func (f *Facade) ListEvents(ctx context.Context, filter EventFilter) ([]event.Event, error) {
	if filter.AfterSequence < 0 {
		return nil, invalid("afterSequence must be non-negative")
	}
	limit, _, err := boundedList(ListOptions{Limit: filter.Limit})
	if err != nil {
		return nil, err
	}
	values, err := f.deps.Events.List(ctx, event.Filter{ProjectID: filter.ProjectID, TaskID: filter.TaskID, RunID: filter.RunID})
	if err != nil {
		return nil, MapError(err)
	}
	filtered := values[:0]
	for _, value := range values {
		if value.Sequence > filter.AfterSequence {
			filtered = append(filtered, value)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Sequence < filtered[j].Sequence })
	return page(filtered, 0, limit), nil
}

func boundedList(options ListOptions) (int, int, error) {
	limit := options.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, 0, invalid("limit must be between 1 and 100")
	}
	if options.Offset < 0 || options.Offset > MaxOffset {
		return 0, 0, invalid("offset must be between 0 and 1000000")
	}
	return limit, options.Offset, nil
}
func validID(name, value string) error {
	if value == "" {
		return invalid(name + " is required")
	}
	if len(value) > MaxIDBytes {
		return invalid(name + " exceeds 128 bytes")
	}
	return nil
}
func page[T any](values []T, offset, limit int) []T {
	if offset >= len(values) {
		return []T{}
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	return values[offset:end]
}
func validTaskStatus(value task.Status) bool {
	switch value {
	case task.StatusActive, task.StatusSucceeded, task.StatusFailed, task.StatusCanceled:
		return true
	}
	return false
}
func validRunStatus(value run.Status) bool {
	switch value {
	case run.StatusCreated, run.StatusPreparing, run.StatusRunning, run.StatusWaitingApproval, run.StatusCollecting, run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
		return true
	}
	return false
}

func (f *Facade) TaskDefinitionRevisionForSession(ctx context.Context, sessionID, revisionID string) (task.DefinitionRevision, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return task.DefinitionRevision{}, err
	}
	lookup, ok := f.deps.Tasks.(interface {
		GetDefinitionRevision(context.Context, string) (task.DefinitionRevision, error)
	})
	if !ok {
		return task.DefinitionRevision{}, errors.New("definition lookup unavailable")
	}
	value, err := lookup.GetDefinitionRevision(ctx, revisionID)
	if err != nil || value.Definition.ProjectID != projectID {
		return task.DefinitionRevision{}, notFound()
	}
	return value, nil
}
