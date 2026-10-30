package consoleapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"sort"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// ProductTopicSummary is a user-facing read projection. The Feature Area identity is
// retained for object-scoped log filtering, while AI-session provenance and
// planning identifiers never cross this boundary. Status is the canonical
// FeatureArea lifecycle; the compatibility State field is workflow presentation
// state and must never source a FeatureArea lifecycle badge.
type ProductTopicSummary struct {
	Product                 presentation.FeatureAreaProductProjection `json:"product"`
	CurrentWorkspaceID      string                                    `json:"current_workspace_id,omitempty"`
	Presentation            *presentation.Projection                  `json:"presentation,omitempty"`
	WorkflowGuidance        *wp.Guidance                              `json:"workflowGuidance,omitempty"`
	Retirement              *featurearea.Retirement                   `json:"retirement,omitempty"`
	ID                      string                                    `json:"id"`
	Title                   string                                    `json:"title"`
	Summary                 string                                    `json:"summary"`
	Status                  string                                    `json:"status"`
	LegacyReadOnly          bool                                      `json:"legacy_read_only,omitempty"`
	State                   string                                    `json:"state"`
	AgentOperationState     string                                    `json:"agent_operation_state,omitempty"`
	AgentOperationMessage   string                                    `json:"agent_operation_message,omitempty"`
	AgentOperationKind      string                                    `json:"agent_operation_kind,omitempty"`
	AgentOperationID        string                                    `json:"agent_operation_id,omitempty"`
	AgentOperationRunID     string                                    `json:"agent_operation_run_id,omitempty"`
	AgentConversationID     string                                    `json:"agent_conversation_id,omitempty"`
	AgentOperationStartedAt *time.Time                                `json:"agent_operation_started_at,omitempty"`
	ActiveReview            *ProductReviewSummary                     `json:"active_review,omitempty"`
	LatestReview            *ProductReviewSummary                     `json:"latest_review,omitempty"`
	Reviews                 []ProductReviewSummary                    `json:"reviews"`
	TaskCount               int                                       `json:"task_count"`
	CanRetire               bool                                      `json:"can_retire"`
	RetireBlockedReason     string                                    `json:"retire_blocked_reason,omitempty"`
	CreatedAt               time.Time                                 `json:"created_at"`
	UpdatedAt               time.Time                                 `json:"updated_at"`
}

type ProductDocumentSummary struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	FeatureAreaID  string    `json:"feature_area_id,omitempty"`
	TopicTitle     string    `json:"feature_area_title,omitempty"`
	ContentPreview string    `json:"content_preview"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ProductDocumentDetail struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	TopicTitle string    `json:"feature_area_title,omitempty"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ProductReviewSummary exposes Review identity and frozen execution
// configuration, plus the durable blocker reason, to support the Console and
// the existing exact prompt viewer. Run and AI-session provenance remain
// diagnostic data.
type ProductReviewSummary struct {
	Activity          *workflowpresentation.ReviewActivity `json:"activity,omitempty"`
	ExecutorKind      review.ExecutorKind                  `json:"executorKind"`
	AuthorKind        string                               `json:"authorKind,omitempty"`
	UpperAccess       *review.UpperAccess                  `json:"upperAccess,omitempty"`
	ReportID          string                               `json:"reportId,omitempty"`
	ReviewDepth       task.ReviewDepth                     `json:"reviewDepth"`
	ReviewDisposition review.Disposition                   `json:"reviewDisposition,omitempty"`
	AgentRuntime      *agentruntime.Snapshot               `json:"agent_runtime,omitempty"`
	ID                string                               `json:"id"`
	RequestID         string                               `json:"request_id,omitempty"`
	RunID             string                               `json:"run_id,omitempty"`
	RepositoryID      string                               `json:"repository_id,omitempty"`
	FeatureAreaID     string                               `json:"feature_area_id"`
	State             string                               `json:"state"`
	// BlockerReason is the durable Run failure message or Review start
	// blocker, projected verbatim. It is never translated, rewritten, or
	// derived from logs; empty means no durable reason was recorded.
	BlockerReason   string          `json:"blocker_reason,omitempty"`
	Model           string          `json:"model,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Usage           *OperationUsage `json:"usage,omitempty"`
	// Result is the durable AgentClaim message of the Review's backing Run,
	// projected verbatim. It is frozen operation output, never derived from
	// current Feature Area state or logs.
	Result               string                             `json:"result,omitempty"`
	ResultTruncated      bool                               `json:"result_truncated"`
	Provenance           string                             `json:"provenance,omitempty"`
	RepositoryProvenance *ProductReviewRepositoryProvenance `json:"repository_provenance,omitempty"`
	ObservedEvidence     ProductReviewObservedEvidence      `json:"observed_evidence"`
	Focus                ReviewFocusSummary                 `json:"focus"`
	PromptAvailable      bool                               `json:"prompt_available"`
	CreatedAt            time.Time                          `json:"created_at"`
}

type ProductReviewRepositoryProvenance struct {
	ReviewBaseline              string    `json:"review_baseline"`
	RepositoryHEADAtAdmission   string    `json:"repository_head_at_admission"`
	BranchKind                  string    `json:"branch_kind"`
	BranchRef                   string    `json:"branch_ref,omitempty"`
	WorkingTreeDirtyAtAdmission *bool     `json:"working_tree_dirty_at_admission"`
	InspectionMode              string    `json:"inspection_mode"`
	CapturedAt                  time.Time `json:"captured_at"`
}

type ProductReviewObservedEvidence struct {
	AuthorizationStatus          string `json:"authorization_status"`
	RepositoryInspectionObserved bool   `json:"repository_inspection_observed"`
	ObservationBoundary          string `json:"observation_boundary"`
	SuccessfulHostToolOperations int    `json:"successful_host_tool_operations"`
	ForbiddenHostToolOperations  int    `json:"forbidden_host_tool_operations"`
	FailedHostToolOperations     int    `json:"failed_host_tool_operations"`
}

// OperationUsage is a reusable product-level projection of one backend
// operation's token and duration facts. Usage exists only for AVAILABLE,
// including exact zero. Duration is independent of token availability.
type OperationUsage struct {
	ModelContextWindow   *int64                `json:"model_context_window,omitempty"`
	Status               run.TokenUsageStatus  `json:"status"`
	Usage                *OperationTokenCounts `json:"usage,omitempty"`
	DurationMilliseconds *int64                `json:"duration_ms,omitempty"`
}

// OperationTokenCounts retains unknown optional breakdowns as absent fields;
// input, output and total are exact Run deltas, never a partial Thread total.
type OperationTokenCounts struct {
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
}

type ProductExecutionRef struct {
	ID           string `json:"id,omitempty"`
	Kind         string `json:"kind,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	State        string `json:"state"`
	Verification string `json:"verification,omitempty"`
}

type ProductWorkRun struct {
	AnalysisState string                 `json:"analysisState,omitempty"`
	Usage         *OperationUsage        `json:"usage,omitempty"`
	AgentRuntime  *agentruntime.Snapshot `json:"agent_runtime,omitempty"`
	ID            string                 `json:"id"`
	Purpose       run.Purpose            `json:"purpose"`
	Status        run.Status             `json:"status"`
}

type ProductTaskSummary struct {
	VerificationDecisions *presentation.VerificationDecisions `json:"verificationDecisions,omitempty"`
	NextStep              *presentation.TaskNextStep          `json:"nextStep"`
	RunRecovery           *presentation.TaskRunRecovery       `json:"runRecovery,omitempty"`
	BusinessPhase         task.Phase                          `json:"businessPhase,omitempty"`
	// OperatorActions is the Console channel projection layered over the shared
	// domain Action list. Presentation.TaskView.Actions.Available remains a
	// domain fact; this field says whether the trusted local operator has a
	// canonical command adapter for that exact action.
	OperatorActions             []ProductTaskActionCapability `json:"operatorActions"`
	CurrentActivity             presentation.TaskActivity     `json:"currentActivity"`
	CurrentReviewID             string                        `json:"currentReviewId,omitempty"`
	CurrentExecutionID          string                        `json:"currentExecutionId,omitempty"`
	RequiredReviewDepth         *task.ReviewDepth             `json:"requiredReviewDepth"`
	AllowedReviewDepths         []task.ReviewDepth            `json:"allowedReviewDepths"`
	CommitActivity              string                        `json:"commitActivity,omitempty"`
	CommitErrorSummary          string                        `json:"commitErrorSummary,omitempty"`
	CommitSummary               string                        `json:"commitSummary,omitempty"`
	DevSummary                  string                        `json:"devSummary,omitempty"`
	CurrentAction               wp.Action                     `json:"currentAction"`
	Recovery                    *presentation.Recovery        `json:"recovery,omitempty"`
	PhaseLabel                  string                        `json:"phaseLabel"`
	PhaseDescription            string                        `json:"phaseDescription"`
	CommitStatus                string                        `json:"commitStatus"`
	DevIntegrationStatus        string                        `json:"devIntegrationStatus"`
	Phase                       task.Phase                    `json:"phase"`
	Status                      task.Status                   `json:"status"`
	CommitSHA                   string                        `json:"commitSha,omitempty"`
	MergedCommit                string                        `json:"mergedCommit,omitempty"`
	Presentation                *presentation.Projection      `json:"presentation,omitempty"`
	Executions                  []ProductExecutionSummary     `json:"executions"`
	Work                        task.Work                     `json:"work"`
	LatestRun                   *ProductWorkRun               `json:"latest_run,omitempty"`
	CurrentDefinitionRevisionID string                        `json:"current_definition_revision_id,omitempty"`
	CurrentDefinitionVersion    int                           `json:"current_definition_version,omitempty"`
	CurrentDefinitionSHA256     string                        `json:"current_definition_sha256,omitempty"`
	ID                          string                        `json:"id"`
	Title                       string                        `json:"title"`
	WorkKind                    string                        `json:"work_kind,omitempty"`
	Model                       string                        `json:"model,omitempty"`
	ReasoningEffort             string                        `json:"reasoning_effort,omitempty"`
	FeatureAreaID               string                        `json:"feature_area_id,omitempty"`
	TopicTitle                  string                        `json:"feature_area_title,omitempty"`
	State                       string                        `json:"state"`
	DevelopmentBranches         []string                      `json:"development_branches"`
	DevelopmentBranchProvenance string                        `json:"development_branch_provenance"`
	Goals                       []ProductTaskGoal             `json:"goals"`
	LatestExecution             *ProductExecutionRef          `json:"latest_execution,omitempty"`
	CreatedAt                   time.Time                     `json:"created_at"`
	UpdatedAt                   time.Time                     `json:"updated_at"`
}

type ProductTaskActionCapability struct {
	Target           *presentation.ActionTarget `json:"target,omitempty"`
	Action           string                     `json:"action"`
	DomainAvailable  bool                       `json:"domainAvailable"`
	ChannelAvailable bool                       `json:"channelAvailable"`
	Reason           string                     `json:"reason,omitempty"`
}

// ProductReviewResult is an explicitly requested exact-content projection.
// Result is the durable backing Run AgentClaim message without summarization.
type ProductReviewResult struct {
	Analysis          *runanalysis.RecordedReport `json:"analysis,omitempty"`
	AnalysisReadError string                      `json:"analysis_read_error,omitempty"`
	Report            *review.StoredReport        `json:"report,omitempty"`
	ReviewID          string                      `json:"review_id"`
	Result            string                      `json:"result"`
}

type ProductFeatureAreaCloseResult struct {
	Retirement *featurearea.Retirement `json:"retirement,omitempty"`
	ID         string                  `json:"id"`
	Status     string                  `json:"status"`
}

// ProductTaskGoal is one ordered Task Goal in the user-facing read model.
type ProductTaskGoal struct {
	Position int    `json:"position"`
	Title    string `json:"title"`
}

type ProductExecutionSummary struct {
	ExecutorKind             review.ExecutorKind                  `json:"executorKind,omitempty"`
	ReviewActivity           *workflowpresentation.ReviewActivity `json:"reviewActivity,omitempty"`
	Execution                *agentexecution.Projection           `json:"execution,omitempty"`
	Kind                     string                               `json:"kind"`
	SemanticOwnerKind        string                               `json:"semantic_owner_kind"`
	SemanticOwnerID          string                               `json:"semantic_owner_id"`
	ExecutionProposalID      string                               `json:"-"`
	DomainStatus             string                               `json:"domain_status,omitempty"`
	DomainResult             string                               `json:"domain_result,omitempty"`
	BackendDisposition       string                               `json:"backend_disposition,omitempty"`
	CancellationCapability   *presentation.Capability             `json:"cancellation_capability,omitempty"`
	Cancellation             *presentation.Cancellation           `json:"cancellation,omitempty"`
	RunID                    string                               `json:"run_id"`
	Phase                    string                               `json:"phase"`
	AgentRuntime             *agentruntime.Snapshot               `json:"agent_runtime,omitempty"`
	ID                       string                               `json:"id,omitempty"`
	TaskDefinitionRevisionID string                               `json:"task_definition_revision_id,omitempty"`
	TaskDefinitionVersion    int                                  `json:"task_definition_version,omitempty"`
	TaskDefinitionSHA256     string                               `json:"task_definition_sha256,omitempty"`
	ProvenanceKind           string                               `json:"provenance_kind"`
	TaskID                   string                               `json:"task_id"`
	TaskTitle                string                               `json:"task_title"`
	State                    string                               `json:"state"`
	Verification             string                               `json:"verification,omitempty"`
	CommitRef                string                               `json:"commit_ref,omitempty"`
	Result                   string                               `json:"result,omitempty"`
	Failure                  string                               `json:"failure,omitempty"`
	Usage                    *OperationUsage                      `json:"usage,omitempty"`
	GoalProgress             []ProductGoalProgress                `json:"goal_progress"`
	UpdatedAt                time.Time                            `json:"updated_at"`
}

// ProductGoalProgress is the observable status of one Task Goal inside an
// Execution. An empty goal_progress list means the Task declared no Goals.
type ProductGoalProgress struct {
	Position int    `json:"position"`
	Title    string `json:"title"`
	Status   string `json:"status"`
}

type ProductContextSummary struct {
	Available     bool       `json:"available"`
	State         string     `json:"state"`
	Refreshing    bool       `json:"refreshing"`
	RefreshFailed bool       `json:"refresh_failed"`
	ModuleCount   int        `json:"module_count"`
	RefreshedAt   *time.Time `json:"refreshed_at,omitempty"`
}

type ProductWorkspaceSummary struct {
	ID               string    `json:"id"`
	FeatureAreaID    string    `json:"feature_area_id"`
	FeatureAreaTitle string    `json:"feature_area_title"`
	State            string    `json:"state"`
	Branch           string    `json:"branch,omitempty"`
	Path             string    `json:"path,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ProductProjectWorkspace is a bounded, Project-scoped read model for the
// local Console. It deliberately contains product summaries rather than raw
// control-plane records.
type ProductProjectWorkspace struct {
	TasksTotal            int                       `json:"tasks_total"`
	TasksTruncated        bool                      `json:"tasks_truncated"`
	FeatureAreas          []ProductTopicSummary     `json:"featureAreas"`
	Documents             []ProductDocumentSummary  `json:"documents"`
	Tasks                 []ProductTaskSummary      `json:"tasks"`
	Executions            []ProductExecutionSummary `json:"executions"`
	Context               ProductContextSummary     `json:"project_context"`
	Workspaces            []ProductWorkspaceSummary `json:"workspaces"`
	ActiveAgentOperations []ProductAgentOperation   `json:"active_agent_operations"`
}

// ProductAgentOperation is a durable Project-level projection. It is built
// from FeatureArea claims/state and never inferred from process activity.
type ProductAgentOperation struct {
	FeatureAreaID       string     `json:"feature_area_id"`
	FeatureAreaTitle    string     `json:"feature_area_title"`
	Kind                string     `json:"kind,omitempty"`
	OperationID         string     `json:"operation_id,omitempty"`
	RunID               string     `json:"run_id,omitempty"`
	AgentConversationID string     `json:"agent_conversation_id,omitempty"`
	State               string     `json:"state"`
	Message             string     `json:"message,omitempty"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
}

type ProductModuleFacts struct {
	RepositoryName  string   `json:"repository_name"`
	Responsibility  string   `json:"responsibility"`
	ProductionRoots []string `json:"production_roots"`
	Entrypoints     []string `json:"entrypoints"`
	RelatedAreas    []string `json:"related_areas"`
}

// ProductModuleSummary intentionally omits Module IDs, storage provenance,
// and raw classification fields.
type ProductModuleSummary struct {
	Name        string               `json:"name"`
	Description string               `json:"description,omitempty"`
	Facts       []ProductModuleFacts `json:"facts"`
}

func (r *Reader) ListProductFeatureAreas(ctx context.Context, projectID string) ([]ProductTopicSummary, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	values, err := r.plane.FeatureAreas.ListHistoryByProject(ctx, projectID, MaxLimit)
	if err != nil {
		return nil, controlapi.MapError(err)
	}
	items := make([]ProductTopicSummary, 0, len(values))
	for _, value := range values {
		item := ProductTopicSummary{CurrentWorkspaceID: value.CurrentWorkspaceID, CanRetire: featurearea.CanRetire(value), Retirement: value.Retirement, Product: presentation.NewFeatureAreaProductProjection(value.ID, summarizeOnly(value.Title, 256), string(value.Status), value.LegacyReadOnly),
			ID: value.ID, Title: summarizeOnly(value.Title, 256), Summary: summarizeOnly(value.ProblemStatement, MaxSummaryBytes),
			Status: string(value.Status), State: string(value.Status), LegacyReadOnly: value.LegacyReadOnly, Reviews: []ProductReviewSummary{}, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		}
		if r.plane.WorkflowProgression != nil {
			g, e := r.plane.WorkflowProgression.Read(ctx, projectID, value.ID)
			if e != nil {
				return nil, controlapi.MapError(e)
			}
			item.WorkflowGuidance = &g
		}
		if r.plane.AgentConversations != nil {
			if state, stateErr := r.plane.AgentConversations.State(ctx, value.ID); stateErr == nil {
				if annotated, annotateErr := r.control.ExecutionCancellationState(ctx, projectID, state); annotateErr == nil {
					state = annotated
				}
				item.AgentOperationState, item.AgentOperationMessage, item.AgentConversationID = state.Status, summarizeOnly(state.Message, MaxSummaryBytes), state.AgentConversationID
				if state.Claim != nil {
					item.AgentOperationKind, item.AgentOperationID, item.AgentOperationRunID = string(state.Claim.Kind), state.Claim.OperationID, state.Claim.RunID
					started := state.Claim.CreatedAt
					item.AgentOperationStartedAt = &started
				}
			}
		}
		capability, capabilityErr := r.control.FeatureAreaRetireCapabilityForOperator(ctx, projectID, value.ID)
		item.CanRetire = capabilityErr == nil && capability.Available
		if capabilityErr != nil {
			item.RetireBlockedReason = "Authoritative lifecycle capability is unavailable."
		} else {
			item.RetireBlockedReason = capability.Reason
		}
		item.State = productTopicState(item)
		if item.WorkflowGuidance != nil && item.WorkflowGuidance.Approval != nil {
			item.State = item.WorkflowGuidance.CurrentState
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *Reader) GetProductProjectWorkspace(ctx context.Context, projectID string) (ProductProjectWorkspace, error) {
	return r.productWorkspace(ctx, projectID, "")
}
func (r *Reader) GetProductWorkTab(ctx context.Context, projectID, tab string) (ProductProjectWorkspace, error) {
	if tab != "tasks" && tab != "executions" {
		return ProductProjectWorkspace{}, errors.New("invalid work tab")
	}
	return r.productWorkspace(ctx, projectID, tab)
}
func (r *Reader) productWorkspace(ctx context.Context, projectID, tab string) (ProductProjectWorkspace, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ProductProjectWorkspace{}, err
	}
	featureAreaValues, err := r.plane.FeatureAreas.ListHistoryByProject(ctx, projectID, MaxLimit)
	if err != nil {
		return ProductProjectWorkspace{}, controlapi.MapError(err)
	}
	var documentValues []document.Document
	if tab == "" {
		documentValues, err = r.plane.Documents.ListByProject(ctx, projectID, document.MaxDocuments)
	}
	if err != nil {
		return ProductProjectWorkspace{}, controlapi.MapError(err)
	}
	taskValues, err := r.plane.Tasks.List(ctx)
	if err != nil {
		return ProductProjectWorkspace{}, controlapi.MapError(err)
	}
	runValues, err := r.plane.Runs.List(ctx)
	if err != nil {
		return ProductProjectWorkspace{}, controlapi.MapError(err)
	}

	featureAreas := make([]ProductTopicSummary, 0, len(featureAreaValues))
	featureAreaIndex := make(map[string]int, len(featureAreaValues))
	featureAreaTitles := make(map[string]string, len(featureAreaValues))
	for _, value := range featureAreaValues {
		featureAreaIndex[value.ID] = len(featureAreas)
		featureAreaTitles[value.ID] = summarizeOnly(value.Title, 256)
		item := ProductTopicSummary{CurrentWorkspaceID: value.CurrentWorkspaceID, CanRetire: featurearea.CanRetire(value), Retirement: value.Retirement,
			Product: presentation.NewFeatureAreaProductProjection(value.ID, summarizeOnly(value.Title, 256), string(value.Status), value.LegacyReadOnly),
			ID:      value.ID, Title: summarizeOnly(value.Title, 256), Summary: summarizeOnly(value.ProblemStatement, MaxSummaryBytes),
			Status: string(value.Status), State: string(value.Status), LegacyReadOnly: value.LegacyReadOnly, Reviews: []ProductReviewSummary{}, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		}
		if tab == "" && r.plane.WorkflowProgression != nil {
			g, e := r.plane.WorkflowProgression.Read(ctx, projectID, value.ID)
			if e != nil {
				return ProductProjectWorkspace{}, controlapi.MapError(e)
			}
			item.WorkflowGuidance = &g
		}
		if tab == "" && r.plane.AgentConversations != nil {
			if state, stateErr := r.plane.AgentConversations.State(ctx, value.ID); stateErr == nil {
				if annotated, annotateErr := r.control.ExecutionCancellationState(ctx, projectID, state); annotateErr == nil {
					state = annotated
				}
				item.AgentOperationState, item.AgentOperationMessage, item.AgentConversationID = state.Status, summarizeOnly(state.Message, MaxSummaryBytes), state.AgentConversationID
				if state.Claim != nil {
					item.AgentOperationKind, item.AgentOperationID, item.AgentOperationRunID = string(state.Claim.Kind), state.Claim.OperationID, state.Claim.RunID
					started := state.Claim.CreatedAt
					item.AgentOperationStartedAt = &started
				}
			}
		}
		featureAreas = append(featureAreas, item)
	}
	documents := make([]ProductDocumentSummary, 0, len(documentValues))
	for _, value := range documentValues {
		documents = append(documents, ProductDocumentSummary{ID: value.ID, Title: summarizeOnly(value.Title, 256), FeatureAreaID: value.FeatureAreaID, TopicTitle: featureAreaTitles[value.FeatureAreaID], ContentPreview: summarizeOnly(value.Content, MaxSummaryBytes), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt})
	}
	taskByID := make(map[string]task.Task)
	productTaskIDs := make(map[string]bool)
	runsByTask := make(map[string][]run.Run)
	runsByReview := make(map[string][]run.Run)
	for _, value := range taskValues {
		if value.ProjectID != projectID {
			continue
		}
		taskByID[value.ID] = value

		if !semanticProductTask(value) {
			continue
		}
		productTaskIDs[value.ID] = true
		if index, ok := featureAreaIndex[value.FeatureAreaID]; ok {
			featureAreas[index].TaskCount++
			updatedAt := value.UpdatedAt

			if updatedAt.After(featureAreas[index].UpdatedAt) {
				featureAreas[index].UpdatedAt = updatedAt
			}
		}
	}
	for _, value := range runValues {
		if value.ReviewID != "" {
			runsByReview[value.ReviewID] = append(runsByReview[value.ReviewID], value)
		}
		if _, ok := taskByID[value.TaskID]; ok {
			runsByTask[value.TaskID] = append(runsByTask[value.TaskID], value)
		}
	}

	reviewBranches := make(map[string]string)
	for index := range featureAreas {
		if tab != "" {
			continue
		}
		reviewValues, listErr := r.plane.Reviews.ListByFeatureArea(ctx, featureAreas[index].ID)
		if listErr != nil {
			return ProductProjectWorkspace{}, controlapi.MapError(listErr)
		}
		sort.Slice(reviewValues, func(i, j int) bool {
			if reviewValues[i].CreatedAt.Equal(reviewValues[j].CreatedAt) {
				return reviewValues[i].ID < reviewValues[j].ID
			}
			return reviewValues[i].CreatedAt.After(reviewValues[j].CreatedAt)
		})
		for _, value := range reviewValues {
			if value.ProjectID != projectID {
				continue
			}
			state, usage := productReviewPresentation(value, runsByReview[value.ID])
			authorization := "UNKNOWN"
			evidence, evidenceErr := r.plane.Reviews.SummarizeEvidence(ctx, value.ID)
			if evidenceErr != nil {
				return ProductProjectWorkspace{}, controlapi.MapError(evidenceErr)
			}
			if evidence.AuthorizationGranted {
				authorization = "GRANTED"
			}
			summary := ProductReviewSummary{Activity: workflowpresentation.PresentReviewActivity(value, time.Now()), UpperAccess: value.UpperAccess, ExecutorKind: value.ExecutorKind.Effective(), AuthorKind: value.AuthorKind, ReportID: value.ReportID, ReviewDepth: value.ReviewDepth, ReviewDisposition: value.ReviewDisposition, ID: value.ID, RequestID: value.StartRequestID, RunID: value.RunID, RepositoryID: value.RepositoryID, FeatureAreaID: value.FeatureAreaID, Focus: reviewFocusSummary(value.Focus), State: state, BlockerReason: productReviewBlockerReason(value, runsByReview[value.ID]), Model: value.Model, ReasoningEffort: value.ReasoningEffort, Usage: usage, PromptAvailable: value.FinalCodexPrompt != "", CreatedAt: value.CreatedAt, ObservedEvidence: ProductReviewObservedEvidence{AuthorizationStatus: authorization, RepositoryInspectionObserved: evidence.Successes > 0, ObservationBoundary: "BRIDGE_HOST_TOOLS_ONLY", SuccessfulHostToolOperations: evidence.Successes, ForbiddenHostToolOperations: evidence.Forbidden, FailedHostToolOperations: evidence.Failures}}
			for _, candidate := range runsByReview[value.ID] {
				if candidate.ID == value.RunID {
					summary.AgentRuntime = candidate.AgentRuntime
					break
				}
			}
			if value.RepositoryProvenance != nil {
				p := value.RepositoryProvenance
				summary.RepositoryProvenance = &ProductReviewRepositoryProvenance{ReviewBaseline: p.Commit, RepositoryHEADAtAdmission: p.RepositoryHEAD, BranchKind: p.BranchKind, BranchRef: p.BranchRef, WorkingTreeDirtyAtAdmission: p.WorkingTreeDirtyAtAdmission, InspectionMode: p.InspectionMode, CapturedAt: p.CapturedAt}
			}
			result, resultTruncated, provenance, provenanceErr := r.productReviewResult(ctx, value, runsByReview[value.ID])
			if provenanceErr != nil {
				return ProductProjectWorkspace{}, controlapi.MapError(provenanceErr)
			}
			summary.Result, summary.ResultTruncated, summary.Provenance = result, resultTruncated, provenance
			if summary.RepositoryProvenance != nil && summary.RepositoryProvenance.BranchKind == review.ProvenanceBranch && summary.RepositoryProvenance.BranchRef != "" {
				reviewBranches[value.ID] = summary.RepositoryProvenance.BranchRef
			}
			featureAreas[index].Reviews = append(featureAreas[index].Reviews, summary)
			if featureAreas[index].LatestReview == nil && value.RunID != "" {
				copy := summary
				featureAreas[index].LatestReview = &copy
			}
			if featureAreas[index].AgentOperationKind == "REVIEW" && featureAreas[index].AgentOperationID == value.ID && featureAreas[index].AgentOperationRunID == value.RunID {
				copy := summary
				copy.State, _ = productReviewPresentationWithOwnership(value, runsByReview[value.ID], true)
				featureAreas[index].ActiveReview = &copy
			}
			if value.CreatedAt.After(featureAreas[index].UpdatedAt) {
				featureAreas[index].UpdatedAt = value.CreatedAt
			}
		}
		if len(featureAreas[index].Reviews) > MaxLimit {
			featureAreas[index].Reviews = featureAreas[index].Reviews[:MaxLimit]
		}
		featureAreas[index].State = productTopicState(featureAreas[index])
		if g := featureAreas[index].WorkflowGuidance; g != nil && g.Approval != nil {
			featureAreas[index].State = g.CurrentState
		}
	}
	sort.Slice(featureAreas, func(i, j int) bool {
		if featureAreas[i].UpdatedAt.Equal(featureAreas[j].UpdatedAt) {
			return featureAreas[i].ID < featureAreas[j].ID
		}
		return featureAreas[i].UpdatedAt.After(featureAreas[j].UpdatedAt)
	})
	featureAreaIndex = make(map[string]int, len(featureAreas))
	for index := range featureAreas {
		featureAreaIndex[featureAreas[index].ID] = index
	}

	tasks := make([]ProductTaskSummary, 0, len(productTaskIDs))
	executions := make([]ProductExecutionSummary, 0)
	workspaces := make([]ProductWorkspaceSummary, 0)
	for _, value := range taskByID {
		if !productTaskIDs[value.ID] {
			continue
		}
		title := value.Goal
		if title == "" {
			title = value.ProblemStatement
		}

		featureAreaTitle := ""
		if index, ok := featureAreaIndex[value.FeatureAreaID]; ok {
			featureAreaTitle = featureAreas[index].Title
		}
		executionRuns := make([]run.Run, 0)
		for _, runValue := range runsByTask[value.ID] {
			if runValue.Purpose == run.PurposeImplementation || runValue.Purpose == run.PurposeVerification {
				executionRuns = append(executionRuns, runValue)
			}
		}
		owners := make(map[string]productExecutionOwner)
		for _, runValue := range executionRuns {
			if runValue.ExecutionProposalID == "" {
				continue
			}
			if _, exists := owners[runValue.ExecutionProposalID]; exists {
				continue
			}
			owner, err := r.plane.ExecutionProposals.GetForProject(ctx, projectID, runValue.ExecutionProposalID)
			if err != nil {
				return ProductProjectWorkspace{}, controlapi.MapError(err)
			}
			ownerTitle := ""

			if ownerTitle == "" && owner.TaskDefinitionRevisionID != "" {
				revision, err := r.plane.Tasks.GetDefinitionRevision(ctx, owner.TaskDefinitionRevisionID)
				if err != nil || revision.TaskID != value.ID {
					return ProductProjectWorkspace{}, controlapi.MapError(errors.Join(err, executionproposal.ErrConflict))
				}
				ownerTitle = revision.Definition.WorkSpec.Goal
			}
			if ownerTitle == "" {
				ownerTitle = value.ProblemStatement
			}
			owners[owner.ID] = productExecutionOwner{proposal: owner, title: ownerTitle}
		}
		entries, err := r.productTaskExecutions(ctx, value.ID, value.ProblemStatement, executionRuns, owners)
		if err != nil {
			return ProductProjectWorkspace{}, controlapi.MapError(err)
		}
		// Recovery may quarantine a Run without an Execution cancellation
		// receipt. Task.Work carries that same durable disposition.
		for i := range entries {
			if entries[i].run.ID == value.Work.RunID && value.Work.BackendDisposition == "UNKNOWN_ORPHANED" {
				entries[i].summary.BackendDisposition = value.Work.BackendDisposition
				entries[i].summary.State = "UNKNOWN_ORPHANED"
				entries[i].active = false
			}
		}
		var latest *run.Run
		if len(entries) > 0 {
			latest = &entries[0].run
		}
		goals := []ProductTaskGoal{}
		if r.plane.ExecutionGoals != nil {
			goalValues, goalErr := r.plane.Tasks.ListGoals(ctx, value.ID)
			if goalErr == nil {
				for _, goalValue := range goalValues {
					goals = append(goals, ProductTaskGoal{Position: goalValue.Position, Title: summarizeOnly(goalValue.Title, 512)})
				}
			} else if !errors.Is(goalErr, task.ErrNotFound) {
				return ProductProjectWorkspace{}, controlapi.MapError(goalErr)
			}
		}
		taskSummary := ProductTaskSummary{Phase: value.Phase, Status: value.Status, CommitSHA: value.CommitSHA, MergedCommit: value.MergedCommit, Work: value.Work, ID: value.ID, CurrentDefinitionRevisionID: value.CurrentDefinitionRevisionID, Title: summarizeOnly(value.Title, 2048), WorkKind: string(value.WorkKind), Model: value.Model, ReasoningEffort: value.ReasoningEffort, FeatureAreaID: value.FeatureAreaID, TopicTitle: featureAreaTitle, State: string(value.Status), DevelopmentBranches: []string{}, DevelopmentBranchProvenance: "UNKNOWN_HISTORICAL", Goals: goals, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
		for _, candidate := range runsByTask[value.ID] {
			if candidate.ID == value.Work.RunID {
				summary := ProductWorkRun{AnalysisState: candidate.AnalysisState, Usage: operationUsage(candidate), AgentRuntime: candidate.AgentRuntime, ID: candidate.ID, Purpose: candidate.Purpose, Status: candidate.Status}
				taskSummary.LatestRun = &summary
				if candidate.UpdatedAt.After(taskSummary.UpdatedAt) {
					taskSummary.UpdatedAt = candidate.UpdatedAt
				}
			}
		}
		sourceReviewIDs := make([]string, 0)

		if value.CurrentDefinitionRevisionID != "" {
			revision, revisionErr := r.plane.Tasks.GetDefinitionRevision(ctx, value.CurrentDefinitionRevisionID)
			if revisionErr != nil || revision.TaskID != value.ID {
				return ProductProjectWorkspace{}, controlapi.MapError(errors.Join(revisionErr, task.ErrNotFound))
			}
			taskSummary.CurrentDefinitionVersion, taskSummary.CurrentDefinitionSHA256 = revision.Ordinal, revision.PayloadSHA256
		}

		taskSummary.DevelopmentBranches = append(taskSummary.DevelopmentBranches, productTaskDevelopmentBranches(sourceReviewIDs, reviewBranches)...)
		if len(taskSummary.DevelopmentBranches) != 0 {
			taskSummary.DevelopmentBranchProvenance = "SOURCE_REVIEW"
		}

		if value.Work.DevelopmentBranches != nil {
			taskSummary.DevelopmentBranches = value.Work.DevelopmentBranches
			taskSummary.DevelopmentBranchProvenance = value.Work.DevelopmentBranchProvenance
		}

		if latest != nil {
			latestSummary := entries[0].summary
			ref := ProductExecutionRef{State: latestSummary.State, Verification: latestSummary.Verification}
			taskSummary.LatestExecution = &ref
			if latestSummary.UpdatedAt.After(taskSummary.UpdatedAt) {
				taskSummary.UpdatedAt = latestSummary.UpdatedAt
			}
		}

		exact, e := r.control.TaskPresentationForOperator(ctx, projectID, value.FeatureAreaID, value.ID)
		if e != nil {
			if isNotFound(e) {
				continue
			}
			return ProductProjectWorkspace{}, e
		}
		if exact.TaskView == nil {
			return ProductProjectWorkspace{}, errors.New("Task presentation unavailable")
		}
		applyProductTaskFacts(&taskSummary, exact.TaskView)
		tasks = append(tasks, taskSummary)
		for _, entry := range entries {
			executionSummary := entry.summary
			if executionSummary.TaskDefinitionRevisionID != "" {
				revision, revisionErr := r.plane.Tasks.GetDefinitionRevision(ctx, executionSummary.TaskDefinitionRevisionID)
				if revisionErr == nil && revision.TaskID == value.ID {
					executionSummary.TaskDefinitionVersion, executionSummary.TaskDefinitionSHA256 = revision.Ordinal, revision.PayloadSHA256
				}
			}
			if r.plane.ExecutionGoals != nil && executionSummary.ID != "" {
				progress, progressErr := r.plane.ExecutionGoals.ListForExecution(ctx, executionSummary.ID)
				if progressErr == nil {
					executionSummary.GoalProgress = make([]ProductGoalProgress, 0, len(progress))
					for _, progressValue := range progress {
						executionSummary.GoalProgress = append(executionSummary.GoalProgress, ProductGoalProgress{Position: progressValue.Position, Title: summarizeOnly(progressValue.Title, 512), Status: string(progressValue.Status)})
					}
				}
			}
			executions = append(executions, executionSummary)
			tasks[len(tasks)-1].Executions = append(tasks[len(tasks)-1].Executions, executionSummary)
		}
		if tab != "" {
			continue
		}

	}
	workspaceValues, err := r.plane.Workspaces.List(ctx)
	if err != nil {
		return ProductProjectWorkspace{}, err
	}
	for _, w := range workspaceValues {
		if w.ProjectID != projectID || w.RuntimeQuarantined {
			continue
		}
		var area featurearea.FeatureArea
		for _, a := range featureAreaValues {
			if a.ID == w.FeatureAreaID {
				area = a
				break
			}
		}
		v, err := r.workspaceSummary(ctx, w)
		if err != nil {
			return ProductProjectWorkspace{}, err
		}
		workspaces = append(workspaces, ProductWorkspaceSummary{ID: w.ID, FeatureAreaID: w.FeatureAreaID, FeatureAreaTitle: area.Title, State: v.State, Branch: v.WorktreeBranch, Path: v.WorktreePath, UpdatedAt: v.UpdatedAt})
	}

	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})
	if r.plane.AgentExecutions != nil {
		var err error
		executions, err = r.universalProductExecutions(ctx, projectID, executions, tasks)
		if err != nil {
			return ProductProjectWorkspace{}, err
		}
	}
	sort.Slice(executions, func(i, j int) bool {
		if executions[i].UpdatedAt.Equal(executions[j].UpdatedAt) {
			if executions[i].TaskID == executions[j].TaskID {
				return executions[i].State < executions[j].State
			}
			return executions[i].TaskID < executions[j].TaskID
		}
		return executions[i].UpdatedAt.After(executions[j].UpdatedAt)
	})
	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].UpdatedAt.Equal(workspaces[j].UpdatedAt) {
			return workspaces[i].FeatureAreaID < workspaces[j].FeatureAreaID
		}
		return workspaces[i].UpdatedAt.After(workspaces[j].UpdatedAt)
	})
	if len(executions) > MaxLimit {
		executions = executions[:MaxLimit]
	}
	if len(workspaces) > MaxLimit {
		workspaces = workspaces[:MaxLimit]
	}

	if tab != "" {
		return ProductProjectWorkspace{Tasks: tasks, Executions: executions}, nil
	}
	modules, err := r.plane.Modules.ListContextMapByProject(ctx, projectID)
	if err != nil {
		return ProductProjectWorkspace{}, controlapi.MapError(err)
	}
	contextSummary := ProductContextSummary{State: "UNAVAILABLE", ModuleCount: len(modules)}
	if value, getErr := r.plane.ProjectContexts.Get(ctx, projectID); getErr == nil {
		contextSummary.Available = value.SnapshotUsable()
		contextSummary.State, contextSummary.Refreshing, contextSummary.RefreshFailed = productProjectContextState(value)
		updated := value.UpdatedAt
		contextSummary.RefreshedAt = &updated
	} else if !errors.Is(getErr, projectcontext.ErrNotFound) {
		return ProductProjectWorkspace{}, controlapi.MapError(getErr)
	}
	probes, err := r.plane.ContextProbes.ListByProject(ctx, projectID)
	if err != nil {
		return ProductProjectWorkspace{}, controlapi.MapError(err)
	}
	for _, value := range probes {
		if string(value.Status) == "CREATED" || string(value.Status) == "RUNNING" {
			contextSummary.Refreshing = true
			break
		}
	}
	for index := range featureAreas {
		capability, capabilityErr := r.control.FeatureAreaRetireCapabilityForOperator(ctx, projectID, featureAreas[index].ID)
		if capabilityErr != nil {
			featureAreas[index].CanRetire = false
			featureAreas[index].RetireBlockedReason = "Authoritative lifecycle capability is unavailable."
			continue
		}
		featureAreas[index].CanRetire, featureAreas[index].RetireBlockedReason = capability.Available, capability.Reason
		if r.plane.WorkflowProgression != nil {
			p, e := r.control.FeatureAreaPresentationForOperator(ctx, projectID, featureAreas[index].ID)
			if e != nil {
				return ProductProjectWorkspace{}, e
			}
			item := &featureAreas[index]
			item.Presentation = &p
			item.Product = p.Product
			item.AgentOperationState = p.AgentOperation.Status
			item.AgentOperationMessage = p.AgentOperation.Message
			item.AgentConversationID = p.AgentOperation.ConversationID
			item.AgentOperationStartedAt = p.AgentOperation.StartedAt
			for _, cap := range p.Capabilities {
				if cap.Action == "RETIRE" {
					item.CanRetire = cap.Available
					item.RetireBlockedReason = cap.Reason
				}
			}
			item.ActiveReview = nil
			if p.CurrentWorkflowReview != nil && p.CurrentState == "REVIEW_RUNNING" {
				for i := range item.Reviews {
					if item.Reviews[i].ID == p.CurrentWorkflowReview.ID {
						item.ActiveReview = &item.Reviews[i]
						break
					}
				}
			}
			item.WorkflowGuidance = p.Guidance
			if p.CurrentState != "" {
				item.State = p.CurrentState
			}
			item.AgentOperationKind = ""
			item.AgentOperationID = ""
			item.AgentOperationRunID = ""
			if p.CurrentOperation != nil {
				item.AgentOperationKind = p.CurrentOperation.Kind
				item.AgentOperationID = p.CurrentOperation.ID
				item.AgentOperationRunID = p.CurrentOperation.RunID
			}
		}
	}
	activeAgentOperations := make([]ProductAgentOperation, 0)
	for _, featureArea := range featureAreas {
		if featureArea.AgentOperationState != "BUSY" && featureArea.AgentOperationState != "RECOVERY_REQUIRED" {
			continue
		}
		activeAgentOperations = append(activeAgentOperations, ProductAgentOperation{FeatureAreaID: featureArea.ID, FeatureAreaTitle: featureArea.Title, Kind: featureArea.AgentOperationKind, OperationID: featureArea.AgentOperationID, RunID: featureArea.AgentOperationRunID, AgentConversationID: featureArea.AgentConversationID, State: featureArea.AgentOperationState, Message: featureArea.AgentOperationMessage, StartedAt: featureArea.AgentOperationStartedAt})
	}
	taskTotal := len(tasks)
	if len(tasks) > MaxLimit {
		tasks = tasks[:MaxLimit]
	}
	return ProductProjectWorkspace{TasksTotal: taskTotal, TasksTruncated: taskTotal > len(tasks), FeatureAreas: featureAreas, Documents: documents, Tasks: tasks, Executions: executions, Context: contextSummary, Workspaces: workspaces, ActiveAgentOperations: activeAgentOperations}, nil
}

// productTaskDevelopmentBranches deliberately accepts only durable
func productTaskDevelopmentBranches(sourceReviewIDs []string, reviewBranches map[string]string) []string {
	set := make(map[string]bool)
	for _, reviewID := range sourceReviewIDs {
		if branch := reviewBranches[reviewID]; branch != "" {
			set[branch] = true
		}
	}
	branches := make([]string, 0, len(set))
	for branch := range set {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	return branches
}

func (r *Reader) GetProductDocument(ctx context.Context, projectID, documentID string) (ProductDocumentDetail, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ProductDocumentDetail{}, err
	}
	value, err := r.plane.Documents.GetForProject(ctx, projectID, documentID)
	if err != nil {
		return ProductDocumentDetail{}, controlapi.MapError(err)
	}
	featureAreaTitle := ""
	if value.FeatureAreaID != "" {
		if featureAreas, listErr := r.plane.FeatureAreas.ListHistoryByProject(ctx, projectID, MaxLimit); listErr == nil {
			for _, featureAreaValue := range featureAreas {
				if featureAreaValue.ID == value.FeatureAreaID {
					featureAreaTitle = summarizeOnly(featureAreaValue.Title, 256)
					break
				}
			}
		}
	}
	return ProductDocumentDetail{ID: value.ID, Title: value.Title, TopicTitle: featureAreaTitle, Content: value.Content, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}, nil
}

func productReviewState(value review.Review, runs []run.Run) string {
	state, _ := productReviewPresentation(value, runs)
	return state
}

func productReviewPresentation(value review.Review, runs []run.Run) (string, *OperationUsage) {
	return productReviewPresentationWithOwnership(value, runs, false)
}

func productReviewPresentationWithOwnership(value review.Review, runs []run.Run, active bool) (string, *OperationUsage) {
	usage := operationUsage(run.Run{})
	for _, candidate := range runs {
		if candidate.ID == value.RunID {
			usage = operationUsage(candidate)
			break
		}
	}
	if value.TerminalStatus == "CANCELED" {
		return "CANCELED", usage
	}
	if value.StartFailure != nil {
		return "BLOCKED", usage
	}
	for _, runValue := range runs {
		if runValue.ID != value.RunID {
			continue
		}
		if runValue.Status == run.StatusWaitingApproval {
			return "AWAITING_AUTHORIZATION", usage
		}
		switch workflowpresentation.ReviewProgressStatus(value, &runValue, active) {
		case "RUNNING":
			return "REVIEWING", usage
		case "COMPLETE":
			return "COMPLETE", usage
		case "FAILED", "BLOCKED":
			return "BLOCKED", usage
		case "CANCELED":
			return "CANCELED", usage
		}
	}
	return "STARTING", usage
}

// productReviewBlockerReason returns the durable blocker reason for a blocked
// or failed Review: the associated Run's recorded failure message, or the
// Review's recorded start blocker. The stored text is projected verbatim; the
// reason is never inferred from logs or rewritten by error code.
func productReviewBlockerReason(value review.Review, runs []run.Run) string {
	if value.StartFailure != nil {
		return value.StartFailure.Blocker
	}
	for _, runValue := range runs {
		if runValue.ID != value.RunID || runValue.Failure == nil {
			continue
		}
		return runValue.Failure.Message
	}
	return ""
}

// operationUsage projects one Run back to authoritative product-level
// operation usage. Only the already-derived per-operation Usage delta is
// considered; cumulative Thread snapshots are never exposed as operation
// totals. The upstream telemetry cannot distinguish an unknown cached-input
// field from a confirmed zero, so a zero cached-input delta is left nil
// instead of being presented as 0.
func operationUsage(value run.Run) *OperationUsage {
	result := &OperationUsage{ModelContextWindow: value.TokenUsage.ModelContextWindow, Status: run.TokenUsageUnavailable, DurationMilliseconds: duration(value.StartedAt, value.FinishedAt)}
	if value.TokenUsage.Status == run.TokenUsagePending {
		result.Status = run.TokenUsagePending
	}
	if value.TokenUsage.Status == run.TokenUsageAvailable && value.TokenUsage.Usage != nil {
		counts := *value.TokenUsage.Usage
		result.Status = run.TokenUsageAvailable
		result.Usage = &OperationTokenCounts{InputTokens: int64Pointer(counts.InputTokens), OutputTokens: int64Pointer(counts.OutputTokens), TotalTokens: int64Pointer(counts.TotalTokens)}
		if counts.CachedInputTokens != 0 {
			result.Usage.CachedInputTokens = int64Pointer(counts.CachedInputTokens)
		}
		if counts.ReasoningOutputTokens != 0 {
			result.Usage.ReasoningOutputTokens = int64Pointer(counts.ReasoningOutputTokens)
		}
	}
	return result
}

func int64Pointer(value int64) *int64 {
	return &value
}

func productTopicState(value ProductTopicSummary) string {
	if value.WorkflowGuidance != nil {
		return value.WorkflowGuidance.CurrentState
	}
	return value.Status
}

type productExecutionOwner struct {
	proposal executionproposal.Proposal
	title    string
}

type productExecutionEntry struct {
	summary   ProductExecutionSummary
	run       run.Run
	createdAt time.Time
	active    bool
}

// productTaskExecutions emits one row per durable Execution. Its selected
// phase comes only from that Execution's exact references, never Run recency.
func (r *Reader) productTaskExecutions(ctx context.Context, taskID, legacyTitle string, runs []run.Run, owners map[string]productExecutionOwner) ([]productExecutionEntry, error) {
	byID := make(map[string]run.Run, len(runs))
	for _, value := range runs {
		byID[value.ID] = value
	}
	entries := make([]productExecutionEntry, 0, len(runs))
	seen := make(map[string]bool)
	for _, value := range runs {
		if value.Purpose == run.PurposeScopeReview {
			continue
		}
		if value.TaskID != taskID {
			return nil, executionproposal.ErrConflict
		}
		if value.ExecutionProposalID == "" {
			summary, err := r.makeProductExecution(ctx, value, legacyTitle)
			if err != nil {
				return nil, err
			}
			entries = append(entries, productExecutionEntry{summary: summary, run: value, createdAt: value.CreatedAt})
			continue
		}
		if seen[value.ExecutionProposalID] {
			continue
		}
		seen[value.ExecutionProposalID] = true
		owner, ok := owners[value.ExecutionProposalID]
		proposal := owner.proposal
		if !ok || proposal.ID != value.ExecutionProposalID || proposal.TaskID != taskID {
			return nil, executionproposal.ErrConflict
		}
		runID, purpose := proposal.VerificationRunID, run.PurposeVerification
		if runID == "" {
			runID, purpose = proposal.ImplementationRunID, run.PurposeImplementation
			if runID == "" {
				runID = proposal.RunID
			}
		}
		selected, ok := byID[runID]
		if !ok || selected.TaskID != taskID || selected.ExecutionProposalID != proposal.ID || selected.Purpose != purpose || selected.ProjectID != "" && selected.ProjectID != proposal.ProjectID {
			return nil, executionproposal.ErrConflict
		}
		summary, err := r.makeProductExecution(ctx, selected, owner.title)
		if err != nil {
			return nil, err
		}
		summary.RunID = selected.ID
		summary.Phase = string(selected.Purpose)
		{
			cap, cancellation, e := r.control.ExecutionCancellationPresentationForOperator(ctx, proposal.ProjectID, proposal.ID)
			if e != nil {
				cap.Available = false
				cap.Reason = "Cancellation eligibility or receipt facts are unavailable; refresh required."
			}
			summary.CancellationCapability = &cap
			summary.Cancellation = cancellation
			receipts, e := r.control.ListExecutionCancellationsForOperator(ctx, proposal.ProjectID, proposal.ID)
			if e != nil {
				cap.Available = false
				cap.Reason = "Cancellation eligibility or receipt facts are unavailable; refresh required."
			}
			for _, receipt := range receipts {
				if receipt.RunID == selected.ID && receipt.BackendDisposition == "UNKNOWN_ORPHANED" {
					summary.BackendDisposition = receipt.BackendDisposition
					break
				}
			}
		}
		summary.ID, summary.TaskDefinitionRevisionID = proposal.ID, proposal.TaskDefinitionRevisionID
		if proposal.TaskDefinitionRevisionID != "" {
			summary.ProvenanceKind = "EXACT"
		}
		active := proposal.Status == executionproposal.StatusConsumed && proposal.Outcome == executionproposal.OutcomeRunning
		switch proposal.Outcome {
		case executionproposal.OutcomeRunning:
			summary.State = "EXECUTING"
			if purpose == run.PurposeVerification {
				summary.State = "VERIFYING"
			}
		case executionproposal.OutcomeSucceeded:
			summary.State = "COMPLETED"
		case executionproposal.OutcomeFailed:
			summary.State = "FAILED"
		case executionproposal.OutcomeCanceled:
			summary.State = "CANCELED"
		}
		if summary.BackendDisposition == "UNKNOWN_ORPHANED" {
			summary.State = "UNKNOWN_ORPHANED"
			active = false
		}
		if proposal.FailureMessage != "" {
			summary.Failure = summarizeOnly(proposal.FailureMessage, 2048)
		}
		if proposal.UpdatedAt.After(summary.UpdatedAt) {
			summary.UpdatedAt = proposal.UpdatedAt
		}
		entries = append(entries, productExecutionEntry{summary: summary, run: selected, createdAt: proposal.CreatedAt, active: active})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].active != entries[j].active {
			return entries[i].active
		}
		if entries[i].createdAt.Equal(entries[j].createdAt) {
			return entries[i].run.ID < entries[j].run.ID
		}
		return entries[i].createdAt.After(entries[j].createdAt)
	})
	return entries, nil
}

func (r *Reader) makeProductExecution(ctx context.Context, value run.Run, title string) (ProductExecutionSummary, error) {
	result := ProductExecutionSummary{AgentRuntime: value.AgentRuntime, ProvenanceKind: "LEGACY_UNKNOWN", TaskID: value.TaskID, TaskTitle: summarizeOnly(title, 2048), State: productExecutionState(value), Verification: productVerification(value), GoalProgress: []ProductGoalProgress{}, UpdatedAt: value.UpdatedAt}
	if value.Result != nil {
		result.Result = summarizeOnly(value.Result.AgentClaim.Message, 2048)
	}
	if r.plane != nil && r.plane.Evidence != nil {
		evidenceValue, evidenceErr := r.plane.Evidence.Get(ctx, value.ID)
		if evidenceErr == nil {
			result.CommitRef = evidenceValue.HeadCommit
		} else if !isNotFound(evidenceErr) {
			return ProductExecutionSummary{}, controlapi.MapError(evidenceErr)
		}
	}
	result.Usage = operationUsage(value)
	if value.Failure != nil {
		result.Failure = summarizeOnly(value.Failure.Message, 2048)
	}
	return result, nil
}

// productReviewResult returns the durable AgentClaim message and repository
// provenance of the Review's backing Run. Both come only from the frozen Run
// result; empty values mean no completed Run result exists.
func (r *Reader) productReviewResult(ctx context.Context, value review.Review, runs []run.Run) (string, bool, string, error) {
	for _, runValue := range runs {
		if runValue.ID != value.RunID || runValue.Result == nil {
			continue
		}
		provenance := ""
		if r.plane != nil && r.plane.Evidence != nil {
			evidenceValue, evidenceErr := r.plane.Evidence.Get(ctx, runValue.ID)
			if evidenceErr == nil {
				provenance = evidenceValue.BaseCommit
			} else if !isNotFound(evidenceErr) {
				return "", false, "", controlapi.MapError(evidenceErr)
			}
		}
		result, truncated := summarize(runValue.Result.AgentClaim.Message, 2048)
		return result, truncated, provenance, nil
	}
	return "", false, "", nil
}

// GetProductReviewResult returns the exact durable Review result only after
// Project ownership is proven. It never reads logs or regenerates content.
func (r *Reader) GetProductReviewResult(ctx context.Context, projectID, reviewID string) (ProductReviewResult, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ProductReviewResult{}, err
	}
	value, err := r.plane.Reviews.Get(ctx, reviewID)
	if err != nil {
		if !isNotFound(err) {
			return ProductReviewResult{}, controlapi.MapError(err)
		}
		return ProductReviewResult{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "Project Review result not found"}
	}
	if value.ProjectID != projectID || value.RunID == "" {
		return ProductReviewResult{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "Project Review result not found"}
	}
	if value.ExecutorKind == review.ExecutorUpperLLM {
		report, err := r.control.ReviewReportForOperator(ctx, projectID, value.ID)
		if err != nil {
			return ProductReviewResult{}, err
		}
		raw, _ := json.Marshal(report.Report)
		return ProductReviewResult{ReviewID: reviewID, Result: string(raw), Report: &report}, nil
	}
	runValue, err := r.plane.Runs.Get(ctx, value.RunID)
	if err != nil {
		if !isNotFound(err) {
			return ProductReviewResult{}, controlapi.MapError(err)
		}
		return ProductReviewResult{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "Durable Review result not found"}
	}
	if runValue.ProjectID != projectID || runValue.ReviewID != reviewID || runValue.Result == nil {
		return ProductReviewResult{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "Durable Review result not found"}
	}
	out := ProductReviewResult{ReviewID: reviewID, Result: runValue.Result.AgentClaim.Message}
	if runValue.ResultProtocol == run.NaturalResultProtocol {
		analysis, err := r.control.RecordedRunAnalysisForOperator(ctx, projectID, runValue.ID)
		if err != nil {
			out.AnalysisReadError = "GPT 分析读取失败，请刷新重试。"
		} else {
			out.Analysis = &analysis
		}
	}
	return out, nil
}

// CloseProductFeatureArea is the Console's thin authoritative action adapter.
// It re-reads RETIRE capability immediately before delegating to the existing
// lifecycle service; the service/storage transaction rechecks status and claim.
func (r *Reader) CloseProductFeatureArea(ctx context.Context, projectID, featureAreaID, requestID string) (ProductFeatureAreaCloseResult, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ProductFeatureAreaCloseResult{}, err
	}
	if requestID == "" || len(requestID) > maxIdentifierSize {
		return ProductFeatureAreaCloseResult{}, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Close FeatureArea requestId is required"}
	}
	return ProductFeatureAreaCloseResult{}, operatorActionUnavailable("RETIRE")
}

func productExecutionState(value run.Run) string {
	switch value.Status {
	case run.StatusCreated, run.StatusPreparing, run.StatusWaitingApproval, run.StatusRunning, run.StatusCollecting:
		if value.Purpose == run.PurposeVerification {
			return "VERIFYING"
		}
		return "EXECUTING"
	case run.StatusSucceeded:
		return "COMPLETED"
	case run.StatusFailed, run.StatusInfrastructureFailed:
		return "FAILED"
	case run.StatusCanceled:
		return "CANCELED"
	default:
		return "UNAVAILABLE"
	}
}

func productVerification(value run.Run) string {
	if value.VerificationReport != nil && value.VerificationReport.Availability == run.VerificationReportInsufficient {
		return "VERIFICATION_EVIDENCE_INSUFFICIENT"
	}
	if value.VerificationReport == nil || value.VerificationReport.Availability != run.VerificationReportAvailable {
		return ""
	}
	return "REPORT_AVAILABLE"
}

func (r *Reader) ListProductModules(ctx context.Context, projectID string) ([]ProductModuleSummary, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return nil, err
	}
	values, err := r.plane.Modules.ListContextMapByProject(ctx, projectID)
	if err != nil {
		return nil, controlapi.MapError(err)
	}
	items := make([]ProductModuleSummary, 0, len(values))
	for _, value := range values {
		facts := make([]ProductModuleFacts, 0, len(value.ProductionFacts))
		for _, fact := range value.ProductionFacts {
			facts = append(facts, ProductModuleFacts{
				RepositoryName:  summarizeOnly(fact.RepositoryName, 256),
				Responsibility:  summarizeOnly(fact.Responsibility, 4096),
				ProductionRoots: append([]string(nil), fact.ProductionRoots...),
				Entrypoints:     append([]string(nil), fact.Entrypoints...),
				RelatedAreas:    append([]string(nil), fact.RelatedModules...),
			})
		}
		items = append(items, ProductModuleSummary{Name: summarizeOnly(value.Name, 256), Description: summarizeOnly(value.Description, 4096), Facts: facts})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

// ResolveProductAgentRequest authorizes the operator and the exact Run.
// Runtime protocol validation remains with its original owner.
func (r *Reader) ResolveProductAgentRequest(ctx context.Context, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	if err := r.requireProject(ctx, in.ProjectID); err != nil {
		return agentruntime.Snapshot{}, err
	}
	return r.control.ResolveAgentRequestForOperator(ctx, in)
}

// GetProductTask reads only the opened Task and its exact presentation graph.
func (r *Reader) GetProductTask(ctx context.Context, projectID, taskID string) (ProductTaskSummary, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ProductTaskSummary{}, err
	}
	t, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil || t.ProjectID != projectID || !semanticProductTask(t) {
		return ProductTaskSummary{}, controlapi.MapError(task.ErrNotFound)
	}
	p, err := r.control.TaskPresentationForOperator(ctx, projectID, t.FeatureAreaID, t.ID)
	if err != nil {
		return ProductTaskSummary{}, err
	}
	if p.TaskView == nil {
		return ProductTaskSummary{}, errors.New("Task presentation unavailable")
	}
	v := ProductTaskSummary{Phase: t.Phase, Status: t.Status, CurrentDefinitionRevisionID: t.CurrentDefinitionRevisionID, ID: t.ID, Title: t.Title, FeatureAreaID: t.FeatureAreaID, Work: t.Work, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, Presentation: &p, OperatorActions: productTaskOperatorActions(p.TaskView.Actions)}
	applyProductTaskFacts(&v, p.TaskView)
	return v, nil
}

func productTaskOperatorActions(actions []presentation.Capability) []ProductTaskActionCapability {
	out := make([]ProductTaskActionCapability, 0, len(actions))
	for _, action := range actions {
		item := ProductTaskActionCapability{Action: action.Action, DomainAvailable: action.Available, ChannelAvailable: operatorTaskActionSupportedTarget(action.Action, action.Target)}
		if action.Target != nil {
			target := *action.Target
			item.Target = &target
		}
		if !item.ChannelAvailable {
			item.Reason = "OPERATOR_ACTION_IDENTITY_UNAVAILABLE"
		} else if !item.DomainAvailable {
			item.Reason = action.Reason
		}
		out = append(out, item)
	}
	return out
}

// Product navigation contains only durable semantic FeatureArea Tasks.
func semanticProductTask(t task.Task) bool {
	return t.ID != "" && t.Type == task.TypeDevelopment && t.FeatureAreaID != "" && !t.LegacyReadOnly
}

// Both inventory and detail copy the shared client-neutral Task facts.
func applyProductTaskFacts(item *ProductTaskSummary, v *presentation.TaskView) {
	item.CurrentActivity = cloneProductTaskActivity(v.Activity)
	if v.CurrentReview != nil {
		item.CurrentReviewID = v.CurrentReview.ID
	}
	if v.CurrentExecution != nil {
		item.CurrentExecutionID = v.CurrentExecution.ID
	}
	item.RequiredReviewDepth, item.AllowedReviewDepths = v.RequiredReviewDepth, v.AllowedReviewDepths
	item.Title, item.UpdatedAt = v.Title, v.UpdatedAt
	item.Phase, item.Status, item.State = v.Phase, task.Status(v.Status), v.Status
	item.BusinessPhase = v.BusinessPhase
	item.NextStep = v.NextStep
	item.VerificationDecisions = v.VerificationDecisions
	item.RunRecovery = v.RunRecovery
	item.CurrentDefinitionRevisionID = v.Details.TaskDefinitionRevisionID
	item.PhaseLabel, item.PhaseDescription = v.PhaseLabel, v.PhaseDescription
	item.CommitStatus, item.CommitSHA = v.CommitStatus, v.CommitSHA
	item.DevIntegrationStatus, item.MergedCommit = v.DevIntegrationStatus, v.MergedCommit
	item.CommitActivity, item.CommitErrorSummary = v.CommitActivity, v.CommitErrorSummary
	item.CommitSummary, item.DevSummary = v.CommitSummary, v.DevSummary
	item.CurrentAction, item.Recovery = v.CurrentAction, v.Recovery
}

func cloneProductTaskActivity(value presentation.TaskActivity) presentation.TaskActivity {
	out := value
	if value.ElapsedMS != nil {
		elapsed := *value.ElapsedMS
		out.ElapsedMS = &elapsed
	}
	if value.NativePlan != nil {
		plan := *value.NativePlan
		plan.Steps = append([]agentruntime.PlanStep(nil), value.NativePlan.Steps...)
		out.NativePlan = &plan
	}
	if value.Runtime != nil {
		runtime := *value.Runtime
		runtime.Items = append([]agentruntime.Item(nil), value.Runtime.Items...)
		runtime.Requests = append([]agentruntime.Request(nil), value.Runtime.Requests...)
		runtime.Diagnostics = append([]agentruntime.Diagnostic(nil), value.Runtime.Diagnostics...)
		if value.Runtime.LatestPlan != nil {
			plan := *value.Runtime.LatestPlan
			plan.Steps = append([]agentruntime.PlanStep(nil), value.Runtime.LatestPlan.Steps...)
			runtime.LatestPlan = &plan
		}
		out.Runtime = &runtime
	}
	return out
}
