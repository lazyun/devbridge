package mcpserver

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
	"sort"
	"strings"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowstate"
)

type externalProjectSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func projectContextProductFor(value projectcontext.ProjectContext) externalProjectContextProduct {
	result := externalProjectContextProduct{Status: "PREPARING", RefreshStatus: "IDLE", Guidance: "UNKNOWN", Modules: []externalProjectModule{}, Usable: value.SnapshotUsable()}
	if value.SnapshotUsable() {
		result.Status = "READY"
		result.Summary = "A usable Project Context snapshot is available."
		if value.RootGuidanceIdentity != "" {
			result.Guidance = "FOUND"
		} else {
			result.Guidance = "NOT_FOUND"
		}
	} else {
		result.Summary = "Project Context is being established."
	}
	if value.RefreshInProgress() {
		result.RefreshStatus = "RUNNING"
		result.Summary = "The current Project Context snapshot remains usable while Bridge refreshes it."
	} else if value.RefreshFailed() {
		result.RefreshStatus = "FAILED"
		result.Summary = "The current Project Context snapshot remains usable; the latest refresh failed."
	} else if value.Status == projectcontext.StatusRunning {
		result.RefreshStatus = "RUNNING"
	} else if value.Status == projectcontext.StatusFailed {
		result.Status, result.RefreshStatus, result.Summary = "BLOCKED", "FAILED", "No usable Project Context snapshot is available."
	}
	return result
}

func applyProductWorkflow(projection *workflowProjection, current controlapi.CurrentWorkflow, _ workflowstate.Input, contextValue projectcontext.ProjectContext) {
	out := &projection.output
	out.State, out.Summary = "PROJECT_READY", "Create or select a FeatureArea for the engineering intent."
	if projection.productReview != nil {
		out.Current.Review = reviewSummaryFor(*projection.productReview, projection.review)
	}
	out.PendingDecision = pendingDecision(*projection, current)
	idle := out.Current.Topic == nil || out.Current.Topic.AgentOperation == nil || out.Current.Topic.AgentOperation.OperationID == ""
	usable := projection.featureArea != nil && featurearea.IsUsable(*projection.featureArea)
	out.Capabilities = externalCapabilities{CanCreateFeatureArea: true, CanResetAgentContext: usable && idle, HasPendingDecision: out.PendingDecision != nil, CanResolveDecision: out.PendingDecision != nil}
	if projection.featureArea != nil {
		out.Capabilities.CanActivateFeatureArea = projection.featureArea.Status == featurearea.StatusDiscovering && idle
		out.Capabilities.CanDecomposeFeatureArea = out.Capabilities.CanActivateFeatureArea
		out.Capabilities.CanRetireFeatureArea = featurearea.CanRetire(*projection.featureArea)
	}
	if g := current.Guidance; g != nil {
		out.WorkflowGuidance = g
		out.NextAction = &g.NextAction
		out.CurrentState = g.CurrentState
		out.State = g.CurrentState
		out.Summary = g.Message
		out.Guidance = []string{g.Message}
		out.Current.LatestTask = g.LatestTask
		out.Capabilities.CanCreateTask = contextValue.SnapshotUsable() && g.CanCreateTask()
		out.Capabilities.CanStartReview = contextValue.SnapshotUsable() && idle && g.NextAction.Code == "START_TASK_REVIEW"
		out.Capabilities.CanRequestAnotherReview = out.Capabilities.CanStartReview
		out.Capabilities.CanExecute = contextValue.SnapshotUsable() && idle && (g.NextAction.Code == "START_EXECUTION" || g.NextAction.Code == "START_VERIFICATION")
	}
	if !contextValue.SnapshotUsable() {
		out.Capabilities.CanCreateTask = false
		out.Capabilities.CanStartReview = false
		out.Capabilities.CanExecute = false
		out.Blockers = append(out.Blockers, externalBlocker{Code: "PROJECT_CONTEXT_UNAVAILABLE", Category: "PROJECT_CONTEXT", Summary: "A usable Project Context is required.", ObservedFacts: []string{}, Retryable: true})
	}
	if contextValue.RefreshFailed() {
		out.Recovery = append(out.Recovery, externalRecovery{Code: "PROJECT_CONTEXT_REFRESH_RETRY", Summary: "The previous snapshot remains usable while refresh is retried.", Condition: "A later refresh succeeds."})
	}
}

func reviewSummaryFor(value productreview.Review, internalRun *run.Run) *externalReviewSummary {
	result := &externalReviewSummary{ReviewDepth: value.ReviewDepth, ReviewDisposition: value.ReviewDisposition, ID: value.ID, RequestID: value.StartRequestID, RunID: value.RunID, ReviewBaseline: value.BaseCommit, State: "STARTING", Summary: "The Feature Area Review is being prepared."}
	if value.TerminalStatus == "CANCELED" {
		result.State, result.Summary = "CANCELED", "Review authority is closed; inspect Task work for backend disposition."
		return result
	}
	if value.StartFailure != nil {
		result.State, result.Summary = "BLOCKED", "The Feature Area Review could not start."
		return result
	}
	if internalRun == nil {
		return result
	}
	result.State = externalReviewStatus(internalRun.Status)
	result.ResultAvailable = internalRun.Status == run.StatusSucceeded && internalRun.Result != nil && internalRun.Result.AgentClaim.Message != ""
	switch internalRun.Status {
	case run.StatusSucceeded:
		result.Summary = "The Feature Area Review completed and its findings are available."
	case run.StatusFailed, run.StatusInfrastructureFailed:
		result.Summary = "The Feature Area Review reached a terminal failure."
	case run.StatusCanceled:
		result.Summary = "The Feature Area Review was canceled."
	default:
		result.Summary = "The Feature Area Review is inspecting relevant implementation."
	}
	return result
}

func addReviewFailure(projection *workflowProjection) {
	code := "REVIEW_RUNTIME_FAILED"
	if projection.productReview != nil && projection.productReview.StartFailure != nil && projection.productReview.StartFailure.Code != "" {
		code = projection.productReview.StartFailure.Code
	}
	if projection.review != nil && projection.review.Failure != nil && projection.review.Failure.Code != "" {
		code = projection.review.Failure.Code
	}
	summary := reviewBlockerFor(code)
	observed := []string{}
	if projection.review != nil {
		observed = append(observed, "Review terminal state: "+externalReviewStatus(projection.review.Status))
	}
	projection.output.Blockers = append(projection.output.Blockers, externalBlocker{Code: code, Category: "REVIEW", Summary: summary, Cause: reviewFailureCause(code), ObservedFacts: observed, Retryable: code != "REVIEW_RESULT_INVALID"})
	projection.output.Recovery = append(projection.output.Recovery, externalRecovery{Code: "RETRY_REVIEW", Summary: "Retrying Review continues under the current Feature Area.", Condition: "The reported Review cause is resolved or the Review focus is revised; a new Feature Area represents a different engineering goal."})
}

func reviewFailureCause(code string) string {
	switch code {
	case "REVIEW_CONTEXT_UNAVAILABLE":
		return "The authorized Repository context was unavailable to the Review."
	case "REVIEW_EVIDENCE_INSUFFICIENT":
		return "No successful authorized Repository inspection was observed."
	case "REVIEW_RESULT_INVALID":
		return "The terminal Review result was missing or structurally invalid."
	default:
		return "The Review runtime ended before producing usable engineering findings."
	}
}

type externalProjectModule struct {
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	Responsibility  string   `json:"responsibility"`
	Repository      string   `json:"repository"`
	RepositoryID    string   `json:"repositoryId"`
	ProductionRoots []string `json:"productionRoots"`
	Entrypoints     []string `json:"entrypoints"`
	RelatedModules  []string `json:"relatedModules"`
}

type externalProjectUnderstanding struct {
	Guidance    string                  `json:"guidance"`
	Status      string                  `json:"status"`
	Summary     string                  `json:"summary"`
	Modules     []externalProjectModule `json:"modules"`
	Diagnostics []string                `json:"diagnostics"`
}

type externalResolutionCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type externalResolutionValue struct {
	Status     string                        `json:"status"`
	Name       string                        `json:"name,omitempty"`
	Candidates []externalResolutionCandidate `json:"candidates"`
}

type externalResolutionOutput struct {
	Repository  externalResolutionValue `json:"repository"`
	CodexConfig externalResolutionValue `json:"codexConfig"`
	Module      externalResolutionValue `json:"module"`
}

type externalWorkflowDiagnostics struct {
	AISessionID         string `json:"aiSessionId"`
	InternalTaskID      string `json:"internalTaskId,omitempty"`
	TaskPlanID          string `json:"taskPlanId,omitempty"`
	ExecutionProposalID string `json:"executionProposalId,omitempty"`
	ImplementationRunID string `json:"implementationRunId,omitempty"`
	VerificationRunID   string `json:"verificationRunId,omitempty"`
}

type externalTopicSummary struct {
	CurrentWorkspaceID    string                       `json:"currentWorkspaceId,omitempty"`
	ID                    string                       `json:"id"`
	Title                 string                       `json:"title"`
	Status                string                       `json:"status"`
	CurrentReviewID       string                       `json:"currentReviewId,omitempty"`
	ActiveReviewID        string                       `json:"activeReviewId,omitempty"`
	LatestReviewID        string                       `json:"latestReviewId,omitempty"`
	ReviewStatus          string                       `json:"reviewStatus"`
	ReviewResultAvailable bool                         `json:"reviewResultAvailable"`
	AgentOperation        *externalAgentOperationState `json:"agentOperation,omitempty"`
}

type externalAgentOperationState struct {
	State               string `json:"state"`
	Message             string `json:"message"`
	OperationKind       string `json:"operationKind,omitempty"`
	OperationID         string `json:"operationId,omitempty"`
	RunID               string `json:"runId,omitempty"`
	AgentConversationID string `json:"agentConversationId,omitempty"`
}

type externalTopicReference struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type externalTaskSummary struct {
	RequiredReviewDepth  *task.ReviewDepth  `json:"requiredReviewDepth"`
	AllowedReviewDepths  []task.ReviewDepth `json:"allowedReviewDepths"`
	Phase                task.Phase         `json:"phase"`
	ID                   string             `json:"id"`
	Title                string             `json:"title"`
	WorkKind             string             `json:"workKind,omitempty"`
	Model                string             `json:"model,omitempty"`
	ReasoningEffort      string             `json:"reasoningEffort,omitempty"`
	Status               string             `json:"status"`
	ConfirmationRequired bool               `json:"confirmationRequired"`
}

type externalExecutionSummary struct {
	Cancellation              *executionCancellationProjection `json:"cancellation,omitempty"`
	ID                        string                           `json:"id"`
	Status                    string                           `json:"status"`
	UserAuthorizationRequired bool                             `json:"userAuthorizationRequired"`
}

type externalRepositoryReference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type externalProjectContextProduct struct {
	Status        string                  `json:"status"`
	Usable        bool                    `json:"usable"`
	RefreshStatus string                  `json:"refreshStatus"`
	Summary       string                  `json:"summary"`
	Guidance      string                  `json:"guidance"`
	Modules       []externalProjectModule `json:"modules"`
}

type externalProjectProduct struct {
	DevelopmentContext *developmentcycle.Context     `json:"developmentContext,omitempty"`
	ID                 string                        `json:"id"`
	Name               string                        `json:"name"`
	Repositories       []externalRepositoryReference `json:"repositories"`
	Context            externalProjectContextProduct `json:"context"`
}

type externalReviewSummary struct {
	ReviewDepth       task.ReviewDepth          `json:"reviewDepth"`
	ReviewDisposition productreview.Disposition `json:"reviewDisposition,omitempty"`
	ID                string                    `json:"id"`
	RequestID         string                    `json:"requestId,omitempty"`
	RunID             string                    `json:"runId,omitempty"`
	ReviewBaseline    string                    `json:"reviewBaseline,omitempty"`
	State             string                    `json:"state"`
	Summary           string                    `json:"summary"`
	ResultAvailable   bool                      `json:"resultAvailable"`
}

type externalResultSummary struct {
	ExecutionID string `json:"executionId"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	Inspectable bool   `json:"inspectable"`
}

type externalCurrentObjects struct {
	LatestTask *wp.TaskFact              `json:"latestTask,omitempty"`
	Project    externalProjectProduct    `json:"project"`
	Topic      *externalTopicSummary     `json:"featureArea"`
	Review     *externalReviewSummary    `json:"review"`
	Task       *externalTaskSummary      `json:"task"`
	Execution  *externalExecutionSummary `json:"execution"`
	Result     *externalResultSummary    `json:"result"`
}

type externalCapabilities struct {
	CanCancelExecution           bool `json:"canCancelExecution"`
	CanActivateFeatureArea       bool `json:"canActivateFeatureArea"`
	CanDecomposeFeatureArea      bool `json:"canDecomposeFeatureArea"`
	CanRetireFeatureArea         bool `json:"canRetireFeatureArea"`
	CanResetAgentContext         bool `json:"canResetAgentContext"`
	CanCreateFeatureArea         bool `json:"canCreateFeatureArea"`
	CanStartReview               bool `json:"canStartReview"`
	CanCreateTask                bool `json:"canCreateTask"`
	CanExecute                   bool `json:"canExecute"`
	CanRequestAnotherReview      bool `json:"canRequestAnotherReview"`
	CanInspectResult             bool `json:"canInspectResult"`
	CanResolveDecision           bool `json:"canResolveDecision"`
	HasPendingDecision           bool `json:"hasPendingDecision"`
	DecisionInteractionAvailable bool `json:"decisionInteractionAvailable"`
}

type externalDecisionOption struct {
	Option  string `json:"option"`
	Meaning string `json:"meaning"`
}

type externalDecisionOutput struct {
	DecisionID   string                   `json:"decisionId"`
	DecisionKind string                   `json:"decisionKind"`
	SubjectKind  string                   `json:"subjectKind"`
	SubjectID    string                   `json:"subjectId"`
	SubjectName  string                   `json:"subjectName"`
	Summary      string                   `json:"summary"`
	Options      []externalDecisionOption `json:"options"`
}

type externalBlocker struct {
	Code          string   `json:"code"`
	Category      string   `json:"category"`
	Summary       string   `json:"summary"`
	Cause         string   `json:"cause"`
	ObservedFacts []string `json:"observedFacts"`
	Retryable     bool     `json:"retryable"`
}

type externalRecovery struct {
	Code      string `json:"code"`
	Summary   string `json:"summary"`
	Condition string `json:"condition"`
}

// externalWorkflowStateOutput is the closed, concise GPT-facing product
// snapshot. Internal lifecycle identifiers belong to diagnostic surfaces.
type externalWorkflowStateOutput struct {
	DirectFile                *directfile.State          `json:"directFile,omitempty"`
	AgentExecution            *agentexecution.Projection `json:"agentExecution,omitempty"`
	WorkflowGuidance          *wp.Guidance               `json:"workflowGuidance,omitempty"`
	CurrentState              string                     `json:"currentState,omitempty"`
	NextAction                *wp.Action                 `json:"nextAction,omitempty"`
	State                     string                     `json:"state"`
	Summary                   string                     `json:"summary"`
	Current                   externalCurrentObjects     `json:"current"`
	AvailableFeatureAreas     []externalTopicReference   `json:"availableFeatureAreas"`
	AvailableModels           []string                   `json:"availableModels"`
	AvailableReasoningEfforts []string                   `json:"availableReasoningEfforts"`
	Capabilities              externalCapabilities       `json:"capabilities"`
	PendingDecision           *externalDecisionOutput    `json:"pendingDecision"`
	Blockers                  []externalBlocker          `json:"blockers"`
	Guidance                  []string                   `json:"guidance"`
	Recovery                  []externalRecovery         `json:"recovery"`
}

type externalProjectBindingResult struct {
	aicontext.ProjectBindingResult
	Project externalProjectSummary `json:"project"`
}

type externalProjectOpenOutput struct {
	Result           externalProjectBindingResult `json:"result"`
	Context          SessionBindingContext        `json:"context"`
	Workflow         sessionIdentityWorkflow      `json:"workflow"`
	PresentationHint SurfacePresentationHint      `json:"presentationHint"`
}

type externalTaskDiagnostics struct {
	InternalTaskID string `json:"internalTaskId"`
	TaskPlanID     string `json:"taskPlanId"`
}

type externalTaskOutput struct {
	ID                        string                    `json:"id"`
	FeatureAreaID             string                    `json:"featureAreaId"`
	Title                     string                    `json:"title"`
	WorkKind                  string                    `json:"workKind,omitempty"`
	Model                     string                    `json:"model,omitempty"`
	ReasoningEffort           string                    `json:"reasoningEffort,omitempty"`
	Goal                      string                    `json:"goal"`
	Constraints               []string                  `json:"constraints"`
	ExecutionScope            task.ExecutionScope       `json:"executionScope"`
	ImplementationInstruction string                    `json:"implementationInstruction"`
	VerificationSpec          VerificationSpecOutput    `json:"verificationSpec"`
	Status                    string                    `json:"status"`
	ConfirmationRequired      bool                      `json:"confirmationRequired"`
	Execution                 *externalExecutionSummary `json:"execution"`
	CreatedAt                 string                    `json:"createdAt"`
	UpdatedAt                 string                    `json:"updatedAt"`
}

type externalTaskListOutput struct {
	Tasks []externalTaskOutput `json:"tasks"`
}

type externalAgentClaimOutput struct {
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
}

type externalRunSummary struct {
	Stage          string  `json:"stage"`
	Status         string  `json:"status"`
	StartedAt      *string `json:"startedAt,omitempty"`
	FinishedAt     *string `json:"finishedAt,omitempty"`
	DurationMillis *int64  `json:"durationMillis,omitempty"`
}

type externalEvidenceOutput struct {
	Stage string            `json:"stage"`
	Git   GitEvidenceOutput `json:"git"`
}

type externalFailureOutput struct {
	Stage   string `json:"stage"`
	Kind    string `json:"kind"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type externalExecutionDiagnostics struct {
	ExecutionProposalID string   `json:"executionProposalId"`
	InternalTaskID      string   `json:"internalTaskId"`
	WorkspaceIDs        []string `json:"workspaceIds"`
	WorktreeIDs         []string `json:"worktreeIds"`
	RunIDs              []string `json:"runIds"`
}

type externalExecutionOutput struct {
	agentexecution.Projection
	Cancellation              *executionCancellationProjection `json:"cancellation,omitempty"`
	TaskDefinitionRevisionID  string                           `json:"taskDefinitionRevisionId,omitempty"`
	ProvenanceKind            string                           `json:"provenanceKind"`
	ID                        string                           `json:"id"`
	TaskID                    string                           `json:"taskId"`
	Status                    string                           `json:"status"`
	UserAuthorizationRequired bool                             `json:"userAuthorizationRequired"`
	GoalProgress              []externalGoalProgressOutput     `json:"goalProgress"`
	Runs                      []externalRunSummary             `json:"stages"`
	ChangedFiles              []string                         `json:"changedFiles"`
	Evidence                  []externalEvidenceOutput         `json:"evidence"`
	AgentClaim                externalAgentClaimOutput         `json:"agentClaim"`
	Failures                  []externalFailureOutput          `json:"failures"`
	TokenUsage                TokenUsageOutput                 `json:"tokenUsage"`
	VerificationReport        *VerificationReportOutput        `json:"verificationReport,omitempty"`
	CreatedAt                 string                           `json:"createdAt"`
	UpdatedAt                 string                           `json:"updatedAt"`
}

// externalGoalProgressOutput is one observable Task Goal status inside an
// Execution. An empty goalProgress means the Task declared no Goals.
type externalGoalProgressOutput struct {
	GoalID    string `json:"goalId"`
	Position  int    `json:"position"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updatedAt"`
}

type externalReviewFocus struct {
	Mode              string   `json:"mode"`
	Summary           string   `json:"summary"`
	PrimaryModules    []string `json:"primaryModules"`
	SupportingModules []string `json:"supportingModules"`
	ProductionRoots   []string `json:"productionRoots"`
}

type externalReviewProduct struct {
	ExecutorKind        productreview.ExecutorKind      `json:"executorKind"`
	ExecutorMetadata    *productreview.ExecutorMetadata `json:"executorMetadata,omitempty"`
	UpperAccess         *productreview.UpperAccess      `json:"upperAccess,omitempty"`
	ReportID            string                          `json:"reportId,omitempty"`
	StageLabel          string                          `json:"stageLabel,omitempty"`
	ReviewDepth         productreview.ReviewDepth       `json:"reviewDepth"`
	ReviewDisposition   productreview.Disposition       `json:"reviewDisposition,omitempty"`
	TaskID              string                          `json:"taskId"`
	Ordinal             int                             `json:"ordinal"`
	ID                  string                          `json:"id"`
	RequestID           string                          `json:"requestId,omitempty"`
	ProjectID           string                          `json:"projectId,omitempty"`
	RepositoryID        string                          `json:"repositoryId,omitempty"`
	RunID               string                          `json:"runId,omitempty"`
	OperationKind       string                          `json:"operationKind,omitempty"`
	Topic               externalTopicReference          `json:"featureArea"`
	State               string                          `json:"state"`
	Summary             string                          `json:"summary"`
	ResultAvailable     bool                            `json:"resultAvailable"`
	Model               string                          `json:"model,omitempty"`
	ReasoningEffort     string                          `json:"reasoningEffort,omitempty"`
	ReviewInstruction   string                          `json:"reviewInstruction,omitempty"`
	AdmittedAt          string                          `json:"admittedAt,omitempty"`
	CompletedAt         string                          `json:"completedAt,omitempty"`
	CompletedTransition bool                            `json:"completedTransition"`
	Usage               TokenUsageOutput                `json:"usage"`
	Focus               externalReviewFocus             `json:"focus"`
	Blockers            []string                        `json:"blockers"`
	Failure             *externalBlocker                `json:"failure,omitempty"`
	Recovery            string                          `json:"recovery,omitempty"`
}

type externalReviewObservedEvidence struct {
	AuthorizationStatus          string `json:"authorizationStatus"`
	RepositoryInspectionObserved bool   `json:"repositoryInspectionObserved"`
	ObservationBoundary          string `json:"observationBoundary"`
	SuccessfulHostToolOperations int    `json:"successfulHostToolOperations"`
	ForbiddenHostToolOperations  int    `json:"forbiddenHostToolOperations"`
	FailedHostToolOperations     int    `json:"failedHostToolOperations"`
}

type externalReviewResult struct {
	ReviewDisposition productreview.Disposition `json:"reviewDisposition,omitempty"`
	EngineeringReview string                    `json:"engineeringReview"`
	AgentClaim        externalAgentClaimOutput  `json:"agentClaim"`
}

type externalReviewRepositoryProvenance struct {
	RepositoryID                string `json:"repositoryId"`
	ReviewBaseline              string `json:"reviewBaseline"`
	RepositoryHEADAtAdmission   string `json:"repositoryHEADAtAdmission"`
	BranchKind                  string `json:"branchKind"`
	BranchRef                   string `json:"branchRef,omitempty"`
	WorkingTreeDirtyAtAdmission *bool  `json:"workingTreeDirtyAtAdmission"`
	InspectionMode              string `json:"inspectionMode"`
	CapturedAt                  string `json:"capturedAt"`
}

type externalReviewOutput struct {
	Review               externalReviewProduct               `json:"review"`
	Result               *externalReviewResult               `json:"result,omitempty"`
	ObservedEvidence     externalReviewObservedEvidence      `json:"observedEvidence"`
	RepositoryProvenance *externalReviewRepositoryProvenance `json:"repositoryProvenance,omitempty"`
	// RepositoryDrift carries bounded, objective Git facts between the
	// repository state this Review was based on and the current repository
	// state. Bridge derives no semantic judgement from them.
	RepositoryDrift *RepositoryDriftOutput `json:"repositoryDrift,omitempty"`
}

type externalTopicDetailsOutput struct {
	Topic      externalTopicSummary       `json:"featureArea"`
	Review     *externalReviewOutput      `json:"review"`
	Tasks      []externalTaskOutput       `json:"tasks"`
	Executions []externalExecutionSummary `json:"executions"`
}

type externalContextOutput struct {
	Status          string                   `json:"status"`
	ResultAvailable bool                     `json:"resultAvailable"`
	Result          string                   `json:"result,omitempty"`
	Workflow        workflowstate.Workflow   `json:"workflow"`
	Resolution      externalResolutionOutput `json:"resolution"`
	Blockers        []string                 `json:"blockers"`
	Diagnostics     map[string]string        `json:"diagnostics"`
}

type externalScopeExpansionOutput struct {
	ID               string            `json:"id"`
	TargetKind       string            `json:"targetKind"`
	TargetID         string            `json:"targetId"`
	CurrentScope     ReviewScopeOutput `json:"currentScope"`
	AdditionalScope  ReviewScopeOutput `json:"additionalScope"`
	Reason           string            `json:"reason"`
	Status           string            `json:"status"`
	DecisionRequired bool              `json:"decisionRequired"`
}

type workflowProjection struct {
	output        externalWorkflowStateOutput
	current       controlapi.CurrentWorkflow
	featureArea   *featurearea.FeatureArea
	internal      *task.Task
	plan          *taskplan.Plan
	proposal      *executionproposal.Proposal
	review        *run.Run
	productReview *productreview.Review
	runs          []run.Run
	resolution    resolvedPrerequisites
}

type resolvedPrerequisites struct {
	repository aicontext.RepositoryContext
	config     codexconfig.Config
	module     module.Module
	output     externalResolutionOutput
	blockers   []string
}

func buildWorkflowProjection(ctx context.Context, service BridgeService, aiSessionID, featureAreaID string) (workflowProjection, error) {
	session, err := service.GetSession(ctx, aiSessionID)
	if err != nil {
		return workflowProjection{}, err
	}
	projection := workflowProjection{}
	projection.output.AvailableFeatureAreas = []externalTopicReference{}
	projection.output.Blockers = []externalBlocker{}
	projection.output.Guidance = []string{}
	projection.output.Recovery = []externalRecovery{}
	projection.output.Current.Project = externalProjectProduct{
		ID: session.Project.ID, Name: boundedUTF8(session.Project.Name, maxNameBytes),
		Repositories: []externalRepositoryReference{}, Context: projectContextProductFor(session.ProjectContext),
	}
	if provider, ok := service.(developmentBranchService); ok {
		development, readErr := provider.DevelopmentContextForSession(ctx, aiSessionID)
		if readErr != nil && !errors.Is(readErr, errDevelopmentContextUnavailable) {
			return workflowProjection{}, readErr
		}
		if readErr == nil {
			projection.output.Current.Project.DevelopmentContext = &development
		}
	}
	repositories, err := service.ListRepositories(ctx, aiSessionID, aicontext.ListOptions{Limit: 100})
	if err != nil {
		return workflowProjection{}, err
	}
	for _, value := range repositories {
		projection.output.Current.Project.Repositories = append(projection.output.Current.Project.Repositories, externalRepositoryReference{ID: value.ID, Name: boundedUTF8(value.Name, 256)})
	}
	contextModules, err := service.ListProjectModuleMap(ctx, aiSessionID)
	if err != nil {
		return workflowProjection{}, err
	}
moduleProjection:
	for _, value := range contextModules {
		for _, facts := range value.ProductionFacts {
			if len(projection.output.Current.Project.Context.Modules) == 100 {
				break moduleProjection
			}
			projection.output.Current.Project.Context.Modules = append(projection.output.Current.Project.Context.Modules, externalProjectModule{Key: value.Key, Name: boundedUTF8(value.Name, 256), Responsibility: boundedUTF8(facts.Responsibility, 4096), Repository: boundedUTF8(facts.RepositoryName, 256), RepositoryID: facts.RepositoryID, ProductionRoots: append([]string{}, facts.ProductionRoots...), Entrypoints: append([]string{}, facts.Entrypoints...), RelatedModules: append([]string{}, facts.RelatedModules...)})
		}
	}

	current, err := service.CurrentWorkflow(ctx, aiSessionID, featureAreaID)
	if err != nil {
		return workflowProjection{}, err
	}
	projection.current = current
	projection.output.DirectFile = current.DirectFile
	projection.output.AgentExecution = current.AgentExecution
	projection.output.AvailableModels = append([]string(nil), current.AvailableModels...)
	projection.output.AvailableReasoningEfforts = append([]string(nil), current.AvailableReasoningEfforts...)
	for _, value := range current.AvailableFeatureAreas {
		projection.output.AvailableFeatureAreas = append(projection.output.AvailableFeatureAreas, externalTopicReference{ID: value.ID, Title: boundedUTF8(value.Title, 256)})
	}
	var featureAreaValue featurearea.FeatureArea
	if current.Topic != nil {
		featureAreaValue = *current.Topic
		projection.featureArea = current.Topic
		projection.output.Current.Topic = &externalTopicSummary{ID: featureAreaValue.ID, CurrentWorkspaceID: featureAreaValue.CurrentWorkspaceID, Title: boundedUTF8(featureAreaValue.Title, 256), Status: string(featureAreaValue.Status), ReviewStatus: "NOT_STARTED"}
		state := current.AgentOperation
		if state == nil {
			if recovery, ok := service.(agentOperationRecoveryService); ok {
				value, stateErr := recovery.FeatureAreaAgentOperationState(ctx, aiSessionID, featureAreaValue.ID)
				if stateErr == nil {
					state = &value
				}
			}
		}
		if state != nil {
			projected := &externalAgentOperationState{State: state.Status, Message: boundedUTF8(state.Message, 4096), AgentConversationID: state.AgentConversationID}
			if state.Claim != nil {
				projected.OperationKind = string(state.Claim.Kind)
				projected.OperationID = state.Claim.OperationID
				projected.RunID = state.Claim.RunID
			}
			projection.output.Current.Topic.AgentOperation = projected
		}
	}
	if current.CurrentReview != nil {
		value := *current.CurrentReview
		projection.productReview = &value
		if projection.output.Current.Topic != nil {
			projection.output.Current.Topic.CurrentReviewID = value.ID
			if current.ActiveReview != nil {
				projection.output.Current.Topic.ActiveReviewID = current.ActiveReview.ID
			}
			if current.LatestReview != nil {
				projection.output.Current.Topic.LatestReviewID = current.LatestReview.ID
			}
			projection.output.Current.Topic.ReviewStatus = "STARTING"
			if value.StartFailure != nil {
				projection.output.Current.Topic.ReviewStatus = "BLOCKED"
			}
		}
	}
	if current.Task != nil {
		projection.internal = current.Task
		value := current.Task
		projection.output.Current.Task = &externalTaskSummary{RequiredReviewDepth: value.RequiredReviewDepth, AllowedReviewDepths: task.AllowedReviewDepths(value.RequiredReviewDepth), ID: value.ID, Title: value.Title, Phase: value.Phase, Status: string(value.Status), WorkKind: string(value.WorkKind), Model: value.Model, ReasoningEffort: value.ReasoningEffort}
	}

	if current.TaskPlan != nil {
		projection.plan = current.TaskPlan

	}
	if current.Execution != nil {
		projection.proposal = current.Execution
	}
	projection.runs = append(projection.runs, current.Runs...)
	for i := range projection.runs {
		value := projection.runs[i]
		if value.Purpose == run.PurposeScopeReview {
			if projection.review == nil && (projection.productReview == nil || value.ReviewID == projection.productReview.ID) {
				projection.review = &projection.runs[i]
			}
		}
	}
	if projection.review != nil && projection.output.Current.Topic != nil {
		projection.output.Current.Topic.ReviewStatus = currentReviewCompatibilityStatus(projection, current)
		projection.output.Current.Topic.ReviewResultAvailable = projection.review.Result != nil
	}
	if projection.proposal != nil {
		projection.output.Current.Execution = executionSummaryFor(current.AgentExecution)
		cancellation, err := projectExecutionCancellation(ctx, service, aiSessionID, current.AgentExecution, projection.runs)
		if err != nil {
			return workflowProjection{}, err
		}
		if projection.output.Current.Execution != nil {
			projection.output.Current.Execution.Cancellation = &cancellation
		}
	}

	applyProductWorkflow(&projection, current, workflowstate.Input{}, session.ProjectContext)
	if execution := projection.output.Current.Execution; execution != nil && execution.Cancellation != nil {
		projection.output.Capabilities.CanCancelExecution = execution.Cancellation.Available
	}
	if development := projection.output.Current.Project.DevelopmentContext; development != nil {
		if development.DevelopmentBranchSelectionRequired {
			projection.output.Capabilities.CanCreateTask = false
			projection.output.Capabilities.CanStartReview = false
			projection.output.Capabilities.CanRequestAnotherReview = false
			projection.output.Guidance = append([]string{"Explicitly select a Development Branch with development_branch_list and development_branch_select before entering normal development flow. Selection updates Dev Bridge metadata and does not perform Git checkout."}, projection.output.Guidance...)
		} else {
			for _, repository := range development.Repositories {
				projection.output.Guidance = append(projection.output.Guidance, "Current development branch for "+repository.RepositoryName+": "+repository.BranchShortName+" ("+repository.DevelopmentRef+"). Repository checkout is independent.")
			}
		}
		if len(projection.output.Guidance) > 8 {
			projection.output.Guidance = projection.output.Guidance[:8]
		}
	}
	return projection, nil
}

func resolvePrerequisites(ctx context.Context, service BridgeService, aiSessionID, repositoryID, configID, moduleID string, createModule bool) (resolvedPrerequisites, error) {
	result := resolvedPrerequisites{output: externalResolutionOutput{
		Repository:  externalResolutionValue{Status: "UNAVAILABLE", Candidates: []externalResolutionCandidate{}},
		CodexConfig: externalResolutionValue{Status: "UNAVAILABLE", Candidates: []externalResolutionCandidate{}},
		Module:      externalResolutionValue{Status: "UNAVAILABLE", Candidates: []externalResolutionCandidate{}},
	}}
	repositories, err := service.ListRepositories(ctx, aiSessionID, aicontext.ListOptions{Limit: 100})
	if err != nil {
		return result, err
	}
	for _, value := range repositories {
		result.output.Repository.Candidates = append(result.output.Repository.Candidates, externalResolutionCandidate{ID: value.ID, Name: boundedUTF8(value.Name, 256)})
		if value.ID == repositoryID {
			result.repository = value
		}
	}
	if repositoryID != "" && result.repository.ID == "" {
		result.output.Repository.Status = "INVALID_SELECTION"
		result.blockers = append(result.blockers, "The selected Repository is not available to this Project.")
	} else if result.repository.ID != "" {
		result.output.Repository.Status, result.output.Repository.Name = "RESOLVED", result.repository.Name
	} else if len(repositories) > 0 {
		result.output.Repository.Status = "AMBIGUOUS"
		result.blockers = append(result.blockers, "An explicit Repository identity is required for this operation.")
	} else {
		result.blockers = append(result.blockers, "No Repository is configured for this Project.")
	}

	configs, err := service.ListCodexConfigs(ctx, aiSessionID, aicontext.ListOptions{Limit: 100})
	if err != nil {
		return result, err
	}
	usable := make([]codexconfig.Config, 0)
	for _, value := range configs {
		if !value.Enabled {
			continue
		}
		usable = append(usable, value)
		result.output.CodexConfig.Candidates = append(result.output.CodexConfig.Candidates, externalResolutionCandidate{ID: value.ID, Name: boundedUTF8(value.Name, 256)})
		if value.ID == configID {
			result.config = value
		}
	}
	invalidConfigSelection := configID != "" && result.config.ID == ""
	if invalidConfigSelection {
		result.output.CodexConfig.Status = "INVALID_SELECTION"
		result.blockers = append(result.blockers, "The selected Codex configuration is not enabled for this Project.")
	} else if result.config.ID == "" {
		sort.Slice(usable, func(i, j int) bool { return usable[i].ID < usable[j].ID })
		for _, value := range usable {
			if value.ID == codexconfig.DefaultConfigID {
				result.config = value
				break
			}
		}
		if result.config.ID == "" && len(usable) != 0 {
			// Codex configuration is a Bridge execution prerequisite, not a
			// user-semantic selection. Resolve it mechanically and stably.
			result.config = usable[0]
		}
	}
	if result.config.ID != "" {
		result.output.CodexConfig.Status, result.output.CodexConfig.Name = "RESOLVED", result.config.Name
	} else if !invalidConfigSelection {
		result.blockers = append(result.blockers, "No enabled Codex configuration is available.")
	}

	modules, err := service.ListModules(ctx, aiSessionID, aicontext.ListOptions{Limit: 100})
	if err != nil {
		return result, err
	}
	for _, value := range modules {
		result.output.Module.Candidates = append(result.output.Module.Candidates, externalResolutionCandidate{ID: value.ID, Name: boundedUTF8(value.Name, 256)})
		if value.ID == moduleID || (moduleID == "" && value.Key == module.UnclassifiedKey) {
			result.module = value
			if value.ID == moduleID {
				break
			}
		}
	}
	if moduleID != "" && result.module.ID == "" {
		result.output.Module.Status = "INVALID_SELECTION"
		result.blockers = append(result.blockers, "The selected Module is not available to this Project.")
	} else if result.module.ID == "" && moduleID == "" && createModule {
		if extended, ok := service.(m5ContextService); ok {
			value, createErr := extended.CreateModuleForSession(ctx, aiSessionID, module.UnclassifiedKey, module.UnclassifiedName, "Fallback ownership for work not yet classified.", nil)
			if createErr == nil {
				result.module = value
			} else {
				refreshed, listErr := service.ListModules(ctx, aiSessionID, aicontext.ListOptions{Limit: 100})
				if listErr != nil {
					return result, createErr
				}
				for _, candidate := range refreshed {
					if candidate.Key == module.UnclassifiedKey {
						result.module = candidate
						break
					}
				}
				if result.module.ID == "" {
					return result, createErr
				}
			}
		}
	}
	if result.module.ID != "" {
		result.output.Module.Status, result.output.Module.Name = "RESOLVED", result.module.Name
	} else if moduleID == "" {
		result.output.Module.Status, result.output.Module.Name = "FALLBACK", module.UnclassifiedName
	}
	if result.output.Repository.Status == "RESOLVED" {
		result.output.Repository.Candidates = []externalResolutionCandidate{}
	}
	if result.output.CodexConfig.Status == "RESOLVED" {
		result.output.CodexConfig.Candidates = []externalResolutionCandidate{}
	}
	if result.output.Module.Status == "RESOLVED" || result.output.Module.Status == "FALLBACK" {
		result.output.Module.Candidates = []externalResolutionCandidate{}
	}
	return result, nil
}

func pinnedResolution(value task.Task, candidates resolvedPrerequisites) externalResolutionOutput {
	result := candidates.output
	for _, candidate := range candidates.output.Repository.Candidates {
		if candidate.ID == value.RepositoryID {
			result.Repository = externalResolutionValue{Status: "RESOLVED", Name: candidate.Name, Candidates: []externalResolutionCandidate{}}
		}
	}
	for _, candidate := range candidates.output.CodexConfig.Candidates {
		if candidate.ID == value.CodexConfigID {
			result.CodexConfig = externalResolutionValue{Status: "RESOLVED", Name: candidate.Name, Candidates: []externalResolutionCandidate{}}
		}
	}
	for _, candidate := range candidates.output.Module.Candidates {
		if candidate.ID == value.ModuleID {
			result.Module = externalResolutionValue{Status: "RESOLVED", Name: candidate.Name, Candidates: []externalResolutionCandidate{}}
			break
		}
	}
	return result
}

func taskSummaryFor(value taskplan.Plan, internal task.Task) *externalTaskSummary {
	return &externalTaskSummary{RequiredReviewDepth: internal.RequiredReviewDepth, AllowedReviewDepths: task.AllowedReviewDepths(internal.RequiredReviewDepth), ID: internal.ID, Title: boundedUTF8(value.Title, 256), WorkKind: string(internal.WorkKind), Model: internal.Model, ReasoningEffort: internal.ReasoningEffort, Status: externalTaskStatus(value.Status), ConfirmationRequired: value.Status == taskplan.StatusPendingConfirmation}
}

func externalTaskStatus(status taskplan.Status) string {
	switch status {
	case taskplan.StatusPendingConfirmation:
		return "READY_FOR_CONFIRMATION"
	case taskplan.StatusRejected:
		return "REJECTED"
	case taskplan.StatusApplied:
		return "READY"
	default:
		return "CONFIRMED"
	}
}

func currentExecutionStatus(p *agentexecution.Projection) string {
	if p == nil {
		return "UNAVAILABLE"
	}
	return p.Status
}
func executionSummaryFor(p *agentexecution.Projection) *externalExecutionSummary {
	if p == nil {
		return nil
	}
	return &externalExecutionSummary{ID: p.ExecutionID, Status: p.Status, UserAuthorizationRequired: p.UserAuthorizationRequired}
}

func externalReviewStatus(status run.Status) string {
	switch status {
	case run.StatusSucceeded:
		return "SUCCEEDED"
	case run.StatusFailed:
		return "FAILED"
	case run.StatusInfrastructureFailed:
		return "BLOCKED"
	case run.StatusCanceled:
		return "CANCELED"
	default:
		return "RUNNING"
	}
}

func pendingDecision(projection workflowProjection, current controlapi.CurrentWorkflow) *externalDecisionOutput {
	if g := current.Guidance; g != nil && !g.Legacy && current.PendingApproval == nil {
		if a := g.Approval; a != nil && g.TaskPhase == task.PhaseReviewApprovalPending {
			options := []externalDecisionOption{}
			for _, option := range a.AllowedActions {
				options = append(options, externalDecisionOption{Option: option, Meaning: option + " the exact Review implementation proposal."})
			}
			return &externalDecisionOutput{DecisionID: a.DecisionID, DecisionKind: wp.KindReviewApproval, SubjectKind: "TASK_REVIEW_GATE", SubjectID: a.TaskID, SubjectName: "Review implementation proposal", Summary: a.Summary, Options: options}
		}
		if g.NextAction.Code == "RESOLVE_TASK_ACCEPTANCE" && g.ExecutionID != "" {
			return &externalDecisionOutput{DecisionID: decisionresolution.TaskAcceptanceDecisionID(g.ExecutionID), DecisionKind: "TASK_ACCEPTANCE", SubjectKind: "EXECUTION_PROPOSAL", SubjectID: g.ExecutionID, SubjectName: "Current terminal Execution", Summary: "GPT assesses the exact verification evidence; the user chooses to continue, complete with commit/merge, or defer this same Task.", Options: taskAcceptanceDecisionOptions(projection)}
		}
		return nil
	}

	if current.PendingApproval != nil {
		options := make([]externalDecisionOption, 0, len(current.PendingApproval.AllowedDecisions))
		for _, decision := range current.PendingApproval.AllowedDecisions {
			options = append(options, externalDecisionOption{Option: string(decision), Meaning: runtimeApprovalMeaning(decision, current.PendingApproval.WorkflowOwnedRun)})
		}
		subjectID := ""
		if projection.proposal != nil {
			subjectID = projection.proposal.ID
		}
		return &externalDecisionOutput{DecisionID: "RUNTIME_APPROVAL:" + current.PendingApproval.ID, DecisionKind: "RUNTIME_APPROVAL", SubjectKind: "EXECUTION", SubjectID: subjectID, SubjectName: "Current Execution", Summary: "Execution is paused for a runtime permission decision.", Options: options}
	}
	return nil
}

func taskAcceptanceDecisionOptions(projection workflowProjection) []externalDecisionOption {
	options := []externalDecisionOption{{Option: "CONTINUE_TASK", Meaning: "Continue this same Task at its verification checkpoint; do not create a replacement Task."}, {Option: "DEFER_TASK", Meaning: "Save the checkpoint and remaining work, keeping this Task open."}}
	if proposal := projection.proposal; proposal != nil {
		runs := map[string]run.Run{}
		for _, value := range projection.runs {
			runs[value.ID] = value
		}
		acceptAllowed := false
		if proposal.VerificationSpec.Mode == task.VerificationModeNone {
			acceptAllowed = proposal.VerificationSpec.Waiver != nil && proposal.VerificationSpec.Waiver.Source == task.VerificationWaiverUser && strings.TrimSpace(proposal.VerificationSpec.Waiver.Reason) != ""
		} else if verification, ok := runs[proposal.VerificationRunID]; ok {
			acceptAllowed = (verification.Failure == nil || verification.Failure.Code != "VERIFICATION_MUTATED_WORKTREE") && verification.VerificationReport != nil && run.VerificationEvidenceComplete(*verification.VerificationReport, proposal.VerificationSpec, verification.RuntimeWorkset) && run.VerificationAssessmentPassed(*verification.VerificationReport, proposal.VerificationSpec)
		}
		if acceptAllowed {
			options = append(options[:1], append([]externalDecisionOption{{Option: "COMPLETE_TASK", Meaning: "Prepare explicit completion with the exact per-repository commit and merge plan; completion requires successful integration."}}, options[1:]...)...)
		}
	}
	return options
}

func externalVerificationReportMatchesSpec(report run.VerificationReport, spec task.VerificationSpec) bool {
	if run.ValidateVerificationReportV2(report) != nil || spec.Mode != task.VerificationModeRun || len(report.Checks) != len(spec.Checks) || len(report.AcceptanceCriteria) != len(spec.AcceptanceCriteria) {
		return false
	}
	for index, check := range report.Checks {
		declared := spec.Checks[index]
		if check.Index != index || check.Kind != string(declared.Kind) || check.Required != declared.Required || check.DeclaredCommand != declared.Command {
			return false
		}
	}
	for index, criterion := range report.AcceptanceCriteria {
		if criterion.Index != index || criterion.Criterion != spec.AcceptanceCriteria[index] {
			return false
		}
	}
	return true
}

func runtimeApprovalMeaning(value approval.DecisionType, workflowOwned bool) string {
	switch value {
	case approval.DecisionDecline:
		return "Decline the requested runtime action."
	case approval.DecisionCancel:
		if workflowOwned {
			return "Decline this runtime action only. This does not cancel the whole operation; use operation_cancel to cancel an Execution."
		}
		return "Cancel this standalone legacy Run."
	case approval.DecisionApproveOnce:
		return "Allow the requested runtime operation once."
	case approval.DecisionApproveForSession:
		return "Allow the requested runtime operation for this execution session."
	default:
		return "Apply this exact runtime permission decision."
	}
}

func externalTaskFor(value taskplan.Plan, internal task.Task, proposal *executionproposal.Proposal, runs []run.Run) externalTaskOutput {
	constraints := append([]string(nil), value.WorkSpec.Constraints...)
	if constraints == nil {
		constraints = []string{}
	}
	result := externalTaskOutput{
		ID: internal.ID, FeatureAreaID: internal.FeatureAreaID, WorkKind: string(internal.WorkKind), Model: internal.Model, ReasoningEffort: internal.ReasoningEffort,
		Title: boundedUTF8(value.Title, 256), Goal: boundedUTF8(value.WorkSpec.Goal, 16<<10), Constraints: constraints,
		ExecutionScope: value.WorkSpec.ExecutionScope, ImplementationInstruction: boundedUTF8(value.WorkSpec.CodexInstruction, 64<<10),
		VerificationSpec: verificationSpecOutput(value.VerificationSpec, true), Status: externalTaskStatus(value.Status),
		ConfirmationRequired: value.Status == taskplan.StatusPendingConfirmation,
		CreatedAt:            value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat),
	}
	if internal.Status == task.StatusCanceled {
		result.Status = "CANCELED"
		result.ConfirmationRequired = false
	}
	if proposal != nil && proposal.TaskPlanID == value.ID && internal.Work.ExecutionID == proposal.ID {
		result.Execution = executionSummaryFor(&agentexecution.Projection{ExecutionID: internal.Work.ExecutionID, Status: internal.Work.ExecutionStatus})
	}
	return result
}

func externalReviewFor(value productreview.Review, featureAreaValue featurearea.FeatureArea, internalRun *run.Run, evidence productreview.EvidenceSummary) externalReviewOutput {
	// Admission returns the durable Review before the Upper access preparation
	// projection has been re-read.  Do not serialize that zero-value placeholder
	// as an access object: the external schema only permits established protocol
	// versions, and consumers must use review_access_get for the authoritative
	// current grant.
	upperAccess := value.UpperAccess
	if upperAccess != nil && upperAccess.ProtocolVersion != 1 && upperAccess.ProtocolVersion != 2 {
		upperAccess = nil
	}
	focus := externalReviewFocus{Mode: string(value.Focus.Kind), Summary: boundedUTF8(value.Focus.Summary, 4096), PrimaryModules: []string{}, SupportingModules: []string{}, ProductionRoots: append([]string{}, value.Focus.Paths...)}
	if internalRun != nil {
		for _, selected := range internalRun.ReviewPlan.SelectedModules {
			switch selected.Role {
			case run.ReviewModulePrimary:
				focus.PrimaryModules = append(focus.PrimaryModules, selected.Key)
			case run.ReviewModuleSupporting:
				focus.SupportingModules = append(focus.SupportingModules, selected.Key)
			}
		}
	}
	authorizationStatus := "UNKNOWN"
	if evidence.AuthorizationGranted {
		authorizationStatus = "GRANTED"
	}
	result := externalReviewOutput{Review: externalReviewProduct{
		ExecutorKind: value.ExecutorKind.Effective(), ExecutorMetadata: value.ExecutorMetadata, UpperAccess: upperAccess, ReportID: value.ReportID, ReviewDepth: value.ReviewDepth, ReviewDisposition: value.ReviewDisposition, TaskID: value.TaskID, Ordinal: value.Ordinal, ID: value.ID, RequestID: value.StartRequestID, ProjectID: value.ProjectID, RepositoryID: value.RepositoryID, RunID: value.RunID, OperationKind: "REVIEW", Topic: externalTopicReference{ID: featureAreaValue.ID, Title: boundedUTF8(featureAreaValue.Title, 256)}, State: "STARTING", Model: value.Model, ReasoningEffort: value.ReasoningEffort,
		ReviewInstruction: boundedUTF8(value.Instruction, 64<<10), AdmittedAt: value.CreatedAt.UTC().Format(timeFormat), Summary: "The Feature Area Review is being prepared.", ResultAvailable: false, Focus: focus, Blockers: []string{}, Usage: TokenUsageOutput{Status: string(run.TokenUsageUnavailable)},
	}, ObservedEvidence: externalReviewObservedEvidence{AuthorizationStatus: authorizationStatus, RepositoryInspectionObserved: evidence.Successes > 0, ObservationBoundary: "BRIDGE_HOST_TOOLS_ONLY", SuccessfulHostToolOperations: evidence.Successes, ForbiddenHostToolOperations: evidence.Forbidden, FailedHostToolOperations: evidence.Failures}}
	if value.ExecutorKind == productreview.ExecutorUpperLLM {
		_, label, _ := workflowpresentation.UpperReviewStage(value)
		result.Review.StageLabel = label
	}
	if value.RepositoryProvenance != nil {
		p := value.RepositoryProvenance
		result.RepositoryProvenance = &externalReviewRepositoryProvenance{RepositoryID: p.RepositoryID, ReviewBaseline: p.Commit, RepositoryHEADAtAdmission: p.RepositoryHEAD, BranchKind: p.BranchKind, BranchRef: p.BranchRef, WorkingTreeDirtyAtAdmission: p.WorkingTreeDirtyAtAdmission, InspectionMode: p.InspectionMode, CapturedAt: p.CapturedAt.UTC().Format(timeFormat)}
	}
	if value.StartFailure != nil {
		result.Review.State = "BLOCKED"
		result.Review.Summary = "The Feature Area Review could not start with its required Repository context."
		result.Review.Blockers = []string{boundedUTF8(value.StartFailure.Blocker, 2048)}
		result.Review.Failure = &externalBlocker{Code: value.StartFailure.Code, Category: "REVIEW", Summary: reviewBlockerFor(value.StartFailure.Code), Cause: reviewFailureCause(value.StartFailure.Code), ObservedFacts: []string{}, Retryable: true}
		result.Review.Recovery = "The same Feature Area can be reviewed again after the Review runtime context is repaired."
		return result
	}
	if internalRun == nil {
		return result
	}
	result.Review.State = externalReviewStatus(internalRun.Status)
	result.Review.Usage = TokenUsageOutput{Status: string(internalRun.TokenUsage.Status), ModelContextWindow: internalRun.TokenUsage.ModelContextWindow}
	if internalRun.TokenUsage.Usage != nil {
		usage := tokenCountsOutput(*internalRun.TokenUsage.Usage)
		result.Review.Usage.Usage = &usage
	}
	if internalRun.FinishedAt != nil {
		result.Review.CompletedAt = internalRun.FinishedAt.UTC().Format(timeFormat)
	}
	result.Review.CompletedTransition = terminalRunStatus(internalRun.Status)
	switch internalRun.Status {
	case run.StatusSucceeded:
		result.Review.Summary = "The Feature Area Review completed successfully."
		if evidence.Successes > 0 {
			result.Review.Summary += " Repository inspection was observed through successful Bridge Host Tool operations."
		} else {
			result.Review.Summary += " Repository inspection through Bridge Host Tools was not observed."
		}
	case run.StatusInfrastructureFailed:
		result.Review.State = "BLOCKED"
		result.Review.Summary = "The Feature Area Review could not access its authorized Repository context."
		result.Review.Recovery = "The same Feature Area can be reviewed again after the Review runtime context is repaired."
	case run.StatusFailed:
		result.Review.Summary = "The Feature Area Review did not produce a usable result."
		result.Review.Recovery = "The same Feature Area can be reviewed again with repaired context or a revised focus."
	case run.StatusCanceled:
		result.Review.Summary = "The Feature Area Review was canceled."
	default:
		result.Review.Summary = "The Feature Area Review is inspecting relevant Repository implementation."
	}
	if internalRun.Failure != nil {
		result.Review.Blockers = []string{reviewBlockerFor(internalRun.Failure.Code)}
		result.Review.Failure = &externalBlocker{Code: internalRun.Failure.Code, Category: "REVIEW", Summary: reviewBlockerFor(internalRun.Failure.Code), Cause: reviewFailureCause(internalRun.Failure.Code), ObservedFacts: []string{"Review terminal state: " + externalReviewStatus(internalRun.Status)}, Retryable: internalRun.Failure.Code != "REVIEW_RESULT_INVALID"}
		if internalRun.Failure.Code == "REVIEW_CONTEXT_UNAVAILABLE" || internalRun.Failure.Code == "REVIEW_EVIDENCE_INSUFFICIENT" || internalRun.Failure.Code == "REVIEW_RUNTIME_FAILED" {
			result.Review.State = "BLOCKED"
		}
	}
	if internalRun.Status == run.StatusSucceeded && internalRun.Result != nil && internalRun.Result.AgentClaim.Message != "" {
		claim := boundedUTF8(internalRun.Result.AgentClaim.Message, 64<<10)
		result.Review.ResultAvailable = true
		result.Result = &externalReviewResult{
			ReviewDisposition: value.ReviewDisposition, EngineeringReview: claim,
			AgentClaim: externalAgentClaimOutput{Available: true, Message: claim},
		}
	}
	if value.ExecutorKind == productreview.ExecutorUpperLLM {
		result.Review.Usage = TokenUsageOutput{Status: string(run.TokenUsageUnavailable)}
		result.Review.ResultAvailable = value.ReportID != ""
		result.Review.Model = ""
		result.Review.ReasoningEffort = ""
		result.Review.Summary = result.Review.StageLabel
		if value.UpperAccess != nil && value.UpperAccess.Blocker != "" && value.UpperAccess.State == "PREPARING" {
			result.Review.State = "BLOCKED"
			result.Review.Blockers = []string{value.UpperAccess.Blocker}
			result.Review.Recovery = "Resolve the preparation blocker, then replay the original task_review_start request. The existing Review/Run and claim remain reserved."
		}
	}
	return result
}

func reviewBlockerFor(code string) string {
	switch code {
	case "REVIEW_EVIDENCE_INSUFFICIENT":
		return "No successful authorized repository inspection was observed."
	case "REVIEW_RESULT_INVALID":
		return "The Review result was missing or structurally unusable."
	case "REVIEW_CONTEXT_UNAVAILABLE":
		return "The authorized Repository context was unavailable to the Feature Area Review."
	case "REVIEW_RUNTIME_FAILED":
		return "The Codex Review runtime failed before completing normally."
	default:
		return "The Review reached a terminal failure before producing reliable engineering findings."
	}
}

func projectUnderstandingFor(value projectcontext.ProjectContext) externalProjectUnderstanding {
	status := "PREPARING"
	if value.Status == projectcontext.StatusReady || value.Status == projectcontext.StatusNoGuidance {
		status = "READY"
	} else if value.Status == projectcontext.StatusFailed {
		status = "BLOCKED"
	}
	result := externalProjectUnderstanding{Status: status, Modules: []externalProjectModule{}, Diagnostics: append([]string{}, value.Diagnostics...)}
	if value.Status == projectcontext.StatusNoGuidance {
		result.Guidance = "NOT_FOUND"
		result.Summary = "No project-level guidance was found. Dev Bridge created a lightweight Project Module Map from production structure."
	} else if value.RootGuidanceIdentity != "" {
		result.Guidance = "FOUND"
		result.Summary = "Applicable project guidance was preserved and reconciled with bounded production structure."
	} else {
		result.Guidance = "UNKNOWN"
		result.Summary = "Project Context is being established."
	}
	return result
}

func externalExecutionFor(p agentexecution.Projection, runs []run.Run) externalExecutionOutput {
	result := externalExecutionOutput{Projection: p, ID: p.ExecutionID, TaskID: p.TaskID, Status: p.Status, ProvenanceKind: string(p.ProvenanceKind), UpdatedAt: p.UpdatedAt.UTC().Format(timeFormat), UserAuthorizationRequired: p.UserAuthorizationRequired,
		GoalProgress: []externalGoalProgressOutput{}, Runs: []externalRunSummary{}, ChangedFiles: []string{}, Evidence: []externalEvidenceOutput{}, Failures: []externalFailureOutput{}, AgentClaim: externalAgentClaimOutput{}}
	fileSet := map[string]struct{}{}
	usageRuns := []run.Run{}
	for _, candidate := range runs {
		if candidate.AgentExecutionID != p.ExecutionID || candidate.TaskID != p.TaskID || candidate.ProjectID != p.ProjectID {
			continue
		}
		valueOut := runOutput(candidate, true)
		stage := string(candidate.Purpose)
		result.Runs = append(result.Runs, externalRunSummary{Stage: stage, Status: valueOut.Status, StartedAt: valueOut.StartedAt, FinishedAt: valueOut.FinishedAt, DurationMillis: valueOut.DurationMillis})
		if valueOut.GitEvidence != nil {
			result.Evidence = append(result.Evidence, externalEvidenceOutput{Stage: stage, Git: *valueOut.GitEvidence})
			for _, file := range valueOut.GitEvidence.ChangedFiles {
				fileSet[file] = struct{}{}
			}
		}
		if valueOut.AgentClaim != "" {
			result.AgentClaim.Available = true
			result.AgentClaim.Message = valueOut.AgentClaim
		}
		if valueOut.Failure != nil {
			result.Failures = append(result.Failures, externalFailureOutput{Stage: stage, Kind: valueOut.Failure.Kind, Code: valueOut.Failure.Code, Message: valueOut.Failure.Message})
		}
		usageRuns = append(usageRuns, candidate)
		if candidate.Purpose == run.PurposeVerification && valueOut.VerificationReport != nil {
			result.VerificationReport = valueOut.VerificationReport
		}
	}
	result.TokenUsage = aggregateExecutionUsage(usageRuns)
	result.ChangedFiles = sortedKeys(fileSet)
	return result
}

// externalGoalProgressFor projects durable goal progress rows onto the
// external Execution output. A read failure degrades to an empty list: goal
// progress is observational and must never block Execution reads.
func externalGoalProgressFor(ctx context.Context, service BridgeService, aiSessionID, executionID string) []externalGoalProgressOutput {
	reader, ok := service.(interface {
		ExecutionGoalProgress(context.Context, string, string) ([]taskgoal.Progress, error)
	})
	if !ok {
		return []externalGoalProgressOutput{}
	}
	values, err := reader.ExecutionGoalProgress(ctx, aiSessionID, executionID)
	if err != nil {
		return []externalGoalProgressOutput{}
	}
	result := make([]externalGoalProgressOutput, 0, len(values))
	for _, value := range values {
		result = append(result, externalGoalProgressOutput{GoalID: value.GoalID, Position: value.Position, Title: value.Title, Status: string(value.Status), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)})
	}
	return result
}

func externalRunStage(purpose run.Purpose) string {
	switch purpose {
	case run.PurposeScopeReview:
		return "REVIEW"
	case run.PurposeVerification:
		return "VERIFICATION"
	default:
		return "IMPLEMENTATION"
	}
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// assignWorkKind records the optional organization-only work kind on the
// durable Task and returns the updated Task. It is a pure metadata update and
// never affects runtime setup, scope, authorization, or runtime behavior.
func assignWorkKind(ctx context.Context, service BridgeService, aiSessionID, internalTaskID, workKind string) (task.Task, error) {
	if workKind == "" {
		return service.GetTask(ctx, aiSessionID, internalTaskID)
	}
	if !task.ValidWorkKind(task.WorkKind(workKind)) {
		return task.Task{}, errors.New("workKind must be null or one of the closed work-kind enum")
	}
	classifier, ok := service.(interface {
		ClassifyTaskWorkKind(context.Context, string, string, task.WorkKind) (task.Task, error)
	})
	if !ok {
		return task.Task{}, errors.New("work kind classification is unavailable")
	}
	return classifier.ClassifyTaskWorkKind(ctx, aiSessionID, internalTaskID, task.WorkKind(workKind))
}

// assignExecutionSelection freezes the GPT-selected Codex model and reasoning
// effort on the durable Task. The runtime allowlist is validated structurally;
// Bridge never infers a fallback.
func assignExecutionSelection(ctx context.Context, service BridgeService, aiSessionID, internalTaskID, model, reasoningEffort string) (task.Task, error) {
	classifier, ok := service.(interface {
		ClassifyTaskExecutionSelection(context.Context, string, string, string, string) (task.Task, error)
	})
	if !ok {
		return task.Task{}, errors.New("execution selection is unavailable")
	}
	return classifier.ClassifyTaskExecutionSelection(ctx, aiSessionID, internalTaskID, model, reasoningEffort)
}

// validateModelSelection validates the GPT-selected model and reasoning effort
// against the runtime allowlist exposed by workspace_get include RUNTIME_CONFIG. The preferred
// provider path reads the same current Core snapshot directly; the workflow
// projection fallback is retained for compatible BridgeService implementations.
func validateModelSelection(ctx context.Context, service BridgeService, aiSessionID, model, reasoningEffort string) (string, error) {
	allowlistProvider, ok := service.(interface {
		ExecutionAllowlist() codexruntime.Allowlist
	})
	var allowlist codexruntime.Allowlist
	if ok {
		allowlist = allowlistProvider.ExecutionAllowlist()
	} else {
		current, err := service.CurrentWorkflow(ctx, aiSessionID, "")
		if err != nil {
			return "", err
		}
		allowlist = codexruntime.Allowlist{Models: current.AvailableModels, ReasoningEfforts: current.AvailableReasoningEfforts}
	}
	effort, err := allowlist.ResolveSelection(model, reasoningEffort)
	if err != nil {
		return "", &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: err.Error()}
	}
	return effort, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func latestScopeReview(ctx context.Context, service BridgeService, aiSessionID, internalTaskID string) (*run.Run, error) {
	value, err := service.LatestRunForTask(ctx, aiSessionID, internalTaskID, run.PurposeScopeReview, "")
	if err != nil {
		return nil, err
	}
	detailed, getErr := service.GetRunForSession(ctx, aiSessionID, value.ID)
	return &detailed, getErr
}

func classifyPlanningTask(ctx context.Context, service BridgeService, aiSessionID string, internal task.Task, moduleID string) (task.Task, error) {
	if moduleID == "" || internal.ModuleID == moduleID {
		return internal, nil
	}
	extended, ok := service.(m5ContextService)
	if !ok {
		return task.Task{}, errors.New("Module classification unavailable")
	}
	return extended.ClassifyTaskForSession(ctx, aiSessionID, internal.ID, moduleID)
}

func contextOutputFor(probe contextprobe.Probe, projection workflowProjection, resolution resolvedPrerequisites) externalContextOutput {
	return externalContextOutput{
		Status: string(probe.Status), ResultAvailable: probe.Result != "", Result: boundedUTF8(probe.Result, 64<<10),
		Workflow: workflowstate.Workflow{}, Resolution: resolution.output, Blockers: append([]string(nil), resolution.blockers...),
		Diagnostics: map[string]string{"contextAcquisitionId": probe.ID},
	}
}

func scopeExpansionTarget(ctx context.Context, service BridgeService, aiSessionID, featureAreaID, reviewID string) (productreview.Review, string, string, error) {
	value, err := service.GetReview(ctx, aiSessionID, reviewID)
	if err != nil {
		return productreview.Review{}, "", "", err
	}
	if featureAreaID != "" && featureAreaID != value.FeatureAreaID {
		return productreview.Review{}, "", "", productreview.ErrNotFound
	}
	return value, "REVIEW", value.ID, nil
}

func externalScopeExpansionFor(value scopeexpansion.Expansion, targetKind, targetID string) externalScopeExpansionOutput {
	return externalScopeExpansionOutput{
		ID: value.ID, TargetKind: targetKind, TargetID: targetID,
		CurrentScope:    ReviewScopeOutput{Kind: string(value.CurrentScope.Kind), Paths: append([]string(nil), value.CurrentScope.Paths...)},
		AdditionalScope: ReviewScopeOutput{Kind: string(value.AdditionalScope.Kind), Paths: append([]string(nil), value.AdditionalScope.Paths...)},
		Reason:          boundedUTF8(value.Reason, 4096), Status: string(value.Status), DecisionRequired: value.Status == scopeexpansion.StatusPendingConfirmation,
	}
}
