// Package consoleapi provides bounded, local-operator read projections for the
// embedded Bridge Console. It composes typed control/domain services and never
// reads SQLite or repository files directly.
package consoleapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/projectselection"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

const (
	DefaultLimit      = 50
	MaxLimit          = 100
	MaxChangedFiles   = 256
	MaxArtifactBytes  = 64 << 10
	MaxPayloadBytes   = 16 << 10
	MaxSummaryBytes   = 512
	MaxEventCount     = 100
	MaxArtifactCount  = 100
	MaxRunHistory     = 100
	maxIdentifierSize = 128
)

// Reader is the typed read/control surface used by HTTP handlers for the
// console. The control plane remains authoritative for all selection and
// lifecycle behavior.
type Reader struct {
	plane        *controlplane.ControlPlane
	control      *controlapi.Facade
	logDirectory string
}

func New(plane *controlplane.ControlPlane, control *controlapi.Facade) (*Reader, error) {
	return NewWithLogDirectory(plane, control, "")
}

// NewWithLogDirectory constructs a Console reader with an optional explicit
// shared diagnostics log directory. An empty directory leaves log retrieval
// available but returns no entries; it never discovers paths from a request.
func NewWithLogDirectory(plane *controlplane.ControlPlane, control *controlapi.Facade, logDirectory string) (*Reader, error) {
	if plane == nil || !plane.Ready || control == nil {
		return nil, errors.New("ready control plane and control API are required")
	}
	if logDirectory != "" {
		absolute, err := filepath.Abs(logDirectory)
		if err != nil {
			return nil, errors.New("resolve diagnostics log directory")
		}
		logDirectory = absolute
	}
	return &Reader{plane: plane, control: control, logDirectory: logDirectory}, nil
}

type PageRequest struct {
	Limit  int
	Offset int
}

type Page[T any] struct {
	Items   []T  `json:"items"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

type Selection struct {
	Selected          bool       `json:"selected"`
	SelectedProjectID string     `json:"selected_project_id,omitempty"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
}

type ProjectSummary struct {
	ID                          string    `json:"id"`
	Name                        string    `json:"name"`
	Selected                    bool      `json:"selected"`
	ProjectContextStatus        string    `json:"project_context_status,omitempty"`
	ProjectContextRefreshing    bool      `json:"project_context_refreshing"`
	ProjectContextRefreshFailed bool      `json:"project_context_refresh_failed"`
	RepositoryCount             int       `json:"repository_count"`
	CreatedAt                   time.Time `json:"created_at"`
	UpdatedAt                   time.Time `json:"updated_at"`
}

type ContextSummary struct {
	Available            bool      `json:"available"`
	ProjectID            string    `json:"project_id"`
	Status               string    `json:"status,omitempty"`
	LatestProbeID        string    `json:"latest_probe_id,omitempty"`
	ResultSummary        string    `json:"result_summary,omitempty"`
	ResultSummaryCut     bool      `json:"result_summary_truncated"`
	RootGuidanceIdentity string    `json:"root_guidance_identity,omitempty"`
	CreatedAt            time.Time `json:"created_at,omitempty"`
	UpdatedAt            time.Time `json:"updated_at,omitempty"`
}

type ProjectOverview struct {
	Project     ProjectSummary       `json:"project"`
	Context     ContextSummary       `json:"project_context"`
	LatestProbe *ContextProbeSummary `json:"latest_context_probe,omitempty"`
}

type RepositorySummary struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	CurrentHEAD string `json:"current_head,omitempty"`
	HEADStatus  string `json:"head_status"`
}

type ModuleSummary struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Key          string    `json:"key"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	SystemOwned  bool      `json:"system_owned"`
	Unclassified bool      `json:"unclassified"`
	ParentID     *string   `json:"parent_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AISessionSummary struct {
	ID               string    `json:"id"`
	ProjectID        string    `json:"project_id"`
	CreatedAt        time.Time `json:"created_at"`
	RecentActivityAt time.Time `json:"recent_activity_at"`
}

type TokenCounts struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type TokenUsage struct {
	Status             string       `json:"status"`
	Counts             *TokenCounts `json:"counts,omitempty"`
	ModelContextWindow *int64       `json:"model_context_window,omitempty"`
}

type RunSummary struct {
	ReviewAuthorizationRequired bool                   `json:"review_authorization_required,omitempty"`
	ExecutorKind                string                 `json:"executor_kind,omitempty"`
	ResultProtocol              string                 `json:"result_protocol,omitempty"`
	AnalysisState               string                 `json:"analysis_state,omitempty"`
	TaskTitle                   string                 `json:"task_title,omitempty"`
	TaskPhase                   string                 `json:"task_phase,omitempty"`
	PendingRequestCount         int                    `json:"pending_request_count"`
	AgentExecutionID            string                 `json:"agent_execution_id"`
	AgentRuntime                *agentruntime.Snapshot `json:"agent_runtime,omitempty"`
	ProjectID                   string                 `json:"project_id,omitempty"`
	FeatureAreaID               string                 `json:"feature_area_id,omitempty"`
	OperationKind               string                 `json:"operation_kind,omitempty"`
	OperationID                 string                 `json:"operation_id,omitempty"`
	ReviewID                    string                 `json:"review_id,omitempty"`
	ID                          string                 `json:"id"`
	TaskID                      string                 `json:"task_id"`
	TaskGoal                    string                 `json:"task_goal,omitempty"`
	Purpose                     string                 `json:"purpose"`
	ExecutionProposalID         string                 `json:"execution_proposal_id,omitempty"`
	Attempt                     int                    `json:"attempt"`
	Status                      string                 `json:"status"`
	WorkspaceID                 string                 `json:"workspace_id,omitempty"`
	WorktreeID                  string                 `json:"worktree_id,omitempty"`
	ThreadID                    string                 `json:"thread_id,omitempty"`
	TurnID                      string                 `json:"turn_id,omitempty"`
	CreatedAt                   time.Time              `json:"created_at"`
	StartedAt                   *time.Time             `json:"started_at,omitempty"`
	FinishedAt                  *time.Time             `json:"finished_at,omitempty"`
	DurationMilliseconds        *int64                 `json:"duration_ms,omitempty"`
	TokenUsage                  TokenUsage             `json:"token_usage"`
	PendingApprovalCount        int                    `json:"pending_approval_count"`
	FailureSummary              string                 `json:"failure_summary,omitempty"`
	VerificationReport          string                 `json:"verification_report,omitempty"`
	VerificationCheckCount      int                    `json:"verification_check_count,omitempty"`
	PromptAvailable             bool                   `json:"prompt_available"`
	PromptSHA256                string                 `json:"prompt_sha256,omitempty"`
	PromptBytes                 int64                  `json:"prompt_bytes,omitempty"`
	PromptSentAt                *time.Time             `json:"prompt_sent_at,omitempty"`
}

type TaskSummary struct {
	RequiredReviewDepth *task.ReviewDepth  `json:"requiredReviewDepth"`
	AllowedReviewDepths []task.ReviewDepth `json:"allowedReviewDepths"`
	Title               string             `json:"title"`
	Phase               task.Phase         `json:"phase"`
	CurrentReviewID     string             `json:"current_review_id,omitempty"`
	CurrentExecutionID  string             `json:"current_execution_id,omitempty"`
	Work                task.Work          `json:"work"`
	ID                  string             `json:"id"`
	ProjectID           string             `json:"project_id"`
	WorkKind            string             `json:"work_kind,omitempty"`
	Model               string             `json:"model,omitempty"`
	ReasoningEffort     string             `json:"reasoning_effort,omitempty"`
	Goal                string             `json:"goal"`
	GoalTruncated       bool               `json:"goal_truncated"`
	ProblemStatement    string             `json:"problem_statement"`
	ProblemTruncated    bool               `json:"problem_truncated"`
	TaskPlanStatus      string             `json:"task_plan_status"`
	ConfirmedTaskPlanID string             `json:"confirmed_task_plan_id,omitempty"`
	Status              string             `json:"status"`
	ModuleID            string             `json:"module_id"`
	ModuleName          string             `json:"module_name"`
	ModuleKey           string             `json:"module_key"`
	Unclassified        bool               `json:"unclassified"`
	RepositoryID        string             `json:"repository_id"`
	RepositoryName      string             `json:"repository_name"`
	CodexConfigID       string             `json:"codex_config_id"`
	CodexConfigName     string             `json:"codex_config_name"`
	VerificationMode    string             `json:"verification_mode"`
	VerificationWaiver  string             `json:"verification_waiver,omitempty"`
	LatestRun           *RunSummary        `json:"latest_run,omitempty"`
	WorkspaceID         string             `json:"workspace_id,omitempty"`
	RunCount            int                `json:"run_count"`
	CreatedAt           time.Time          `json:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at"`
}

type WorkspaceSummary struct {
	FeatureAreaID  string    `json:"feature_area_id"`
	ProjectID      string    `json:"project_id"`
	RepositoryID   string    `json:"repository_id"`
	BaseCommit     string    `json:"base_commit"`
	ID             string    `json:"id"`
	WorktreeID     string    `json:"worktree_id,omitempty"`
	WorktreePath   string    `json:"worktree_path,omitempty"`
	WorktreeBranch string    `json:"worktree_branch,omitempty"`
	WorktreeStatus string    `json:"worktree_status,omitempty"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AccountingValue struct {
	Status       string       `json:"status"`
	Milliseconds *int64       `json:"milliseconds,omitempty"`
	Tokens       *TokenCounts `json:"tokens,omitempty"`
}

type Accounting struct {
	Disposition string          `json:"disposition"`
	Duration    AccountingValue `json:"duration"`
	TokenUsage  AccountingValue `json:"token_usage"`
}

type TaskAccounting struct {
	TopicReview    Accounting `json:"featureArea_review"`
	Implementation Accounting `json:"implementation"`
	Verification   Accounting `json:"verification"`
	ContextProbe   Accounting `json:"context_probe"`
}

type TaskDetail struct {
	ExecutionHistory            []ExecutionHistorySummary `json:"execution_history"`
	CurrentDefinitionRevisionID string                    `json:"current_definition_revision_id,omitempty"`
	Task                        TaskSummary               `json:"task"`
	BaseCommit                  string                    `json:"base_commit"`
	WorkSpec                    task.WorkSpec             `json:"work_spec"`
	VerificationSpec            task.VerificationSpec     `json:"verification_spec"`
	TaskPlans                   []taskplan.Plan           `json:"task_plans"`
	ScopeExpansions             []ScopeExpansionSummary   `json:"scope_expansions"`
	Reviews                     []ReviewSummary           `json:"reviews"`
	Workspace                   *WorkspaceSummary         `json:"workspace,omitempty"`
	RunHistory                  []RunSummary              `json:"run_history"`
	RunHistoryCut               bool                      `json:"run_history_truncated"`
	Accounting                  TaskAccounting            `json:"accounting"`
}

type ExecutionHistorySummary struct {
	ID                       string `json:"id"`
	TaskDefinitionRevisionID string `json:"task_definition_revision_id,omitempty"`
	ProvenanceKind           string `json:"provenance_kind"`
	Status                   string `json:"status"`
	Outcome                  string `json:"outcome"`
}

type ReviewSummary struct {
	ExecutorKind        productreview.ExecutorKind `json:"executorKind"`
	UpperAccess         *productreview.UpperAccess `json:"upperAccess,omitempty"`
	ReportID            string                     `json:"reportId,omitempty"`
	ReviewDepth         task.ReviewDepth           `json:"reviewDepth"`
	ReviewDisposition   productreview.Disposition  `json:"reviewDisposition,omitempty"`
	ID                  string                     `json:"id"`
	FeatureAreaID       string                     `json:"feature_area_id"`
	Focus               ReviewFocusSummary         `json:"focus"`
	State               string                     `json:"state"`
	Usage               *OperationUsage            `json:"usage,omitempty"`
	EngineeringReview   string                     `json:"engineering_review,omitempty"`
	ResultTruncated     bool                       `json:"result_truncated"`
	AgentClaim          string                     `json:"agent_claim,omitempty"`
	AgentClaimTruncated bool                       `json:"agent_claim_truncated"`
	Evidence            ReviewEvidenceSummary      `json:"observed_evidence"`
	TokenUsage          TokenUsage                 `json:"token_usage"`
	Diagnostics         ReviewDiagnostics          `json:"diagnostics"`
	PromptAvailable     bool                       `json:"prompt_available"`
	PromptSHA256        string                     `json:"prompt_sha256,omitempty"`
	PromptBytes         int64                      `json:"prompt_bytes,omitempty"`
	PromptSentAt        *time.Time                 `json:"prompt_sent_at,omitempty"`
	CreatedAt           time.Time                  `json:"created_at"`
}

type ReviewEvidenceSummary struct {
	SuccessfulOperations int        `json:"successful_operations"`
	ForbiddenOperations  int        `json:"forbidden_operations"`
	FailedOperations     int        `json:"failed_operations"`
	LastObservedAt       *time.Time `json:"last_observed_at,omitempty"`
}

type ReviewDiagnostics struct {
	RunID       string `json:"run_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	WorktreeID  string `json:"worktree_id,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
	TurnID      string `json:"turn_id,omitempty"`
}

type ReviewFocusSummary struct {
	Mode              string   `json:"mode"`
	Summary           string   `json:"summary"`
	Paths             []string `json:"production_roots"`
	PrimaryModules    []string `json:"primary_modules"`
	SupportingModules []string `json:"supporting_modules"`
}

// reviewFocusSummary projects only the immutable focus captured by this Review.
// Current module catalog entries and later Run traversal are not focus authority.
func reviewFocusSummary(focus productreview.Focus) ReviewFocusSummary {
	out := ReviewFocusSummary{Mode: string(focus.Kind), Summary: focus.Summary, Paths: append([]string{}, focus.Paths...), PrimaryModules: []string{}, SupportingModules: []string{}}
	for _, selected := range focus.Modules {
		switch selected.Role {
		case run.ReviewModulePrimary:
			out.PrimaryModules = append(out.PrimaryModules, selected.Key)
		case run.ReviewModuleSupporting:
			out.SupportingModules = append(out.SupportingModules, selected.Key)
		}
	}
	return out
}

type ReviewPromptDiagnostic struct {
	ReviewID  string     `json:"review_id"`
	Available bool       `json:"available"`
	Prompt    string     `json:"prompt,omitempty"`
	SHA256    string     `json:"sha256,omitempty"`
	Bytes     int64      `json:"bytes,omitempty"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type RunPromptDiagnostic struct {
	RunID     string     `json:"run_id"`
	Purpose   string     `json:"purpose"`
	Available bool       `json:"available"`
	Prompt    string     `json:"prompt,omitempty"`
	SHA256    string     `json:"sha256,omitempty"`
	Bytes     int64      `json:"bytes,omitempty"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type ContextProbePromptDiagnostic struct {
	ContextProbeID string     `json:"context_probe_id"`
	Available      bool       `json:"available"`
	Prompt         string     `json:"prompt,omitempty"`
	SHA256         string     `json:"sha256,omitempty"`
	Bytes          int64      `json:"bytes,omitempty"`
	SentAt         *time.Time `json:"sent_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type ScopeExpansionSummary struct {
	ID              string          `json:"id"`
	CurrentScope    run.ReviewScope `json:"current_scope"`
	AdditionalScope run.ReviewScope `json:"additional_scope"`
	Reason          string          `json:"reason"`
	ReasonTruncated bool            `json:"reason_truncated"`
	Status          string          `json:"status"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	DecidedAt       *time.Time      `json:"decided_at,omitempty"`
}

type ContextProbeSummary struct {
	ID                    string     `json:"id"`
	OwnerKind             string     `json:"owner_kind,omitempty"`
	TaskID                string     `json:"inspection_owner_task_id,omitempty"`
	RunID                 string     `json:"run_id,omitempty"`
	ProjectID             string     `json:"project_id"`
	AISessionID           string     `json:"ai_session_id"`
	RepositoryID          string     `json:"repository_id"`
	SelectedRepositoryIDs []string   `json:"selected_repository_ids"`
	PrimaryMemberID       string     `json:"primary_member_id,omitempty"`
	WorkspaceID           string     `json:"workspace_id,omitempty"`
	RuntimeWorksetSHA256  string     `json:"runtime_workset_sha256,omitempty"`
	ReportSHA256          string     `json:"report_sha256,omitempty"`
	ResultSHA256          string     `json:"result_sha256,omitempty"`
	RuntimeLimitation     string     `json:"runtime_limitation,omitempty"`
	CodexConfigID         string     `json:"codex_config_id"`
	ScopeKind             string     `json:"scope_kind"`
	ScopePaths            []string   `json:"scope_paths"`
	Status                string     `json:"status"`
	ThreadID              string     `json:"thread_id,omitempty"`
	TurnID                string     `json:"turn_id,omitempty"`
	StartedAt             *time.Time `json:"started_at,omitempty"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
	DurationMilliseconds  *int64     `json:"duration_ms,omitempty"`
	TokenUsage            TokenUsage `json:"token_usage"`
	ResultSummary         string     `json:"result_summary,omitempty"`
	ResultSummaryCut      bool       `json:"result_summary_truncated"`
	FailureSummary        string     `json:"failure_summary,omitempty"`
	PromptAvailable       bool       `json:"prompt_available"`
	PromptSHA256          string     `json:"prompt_sha256,omitempty"`
	PromptBytes           int64      `json:"prompt_bytes,omitempty"`
	PromptSentAt          *time.Time `json:"prompt_sent_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type ApprovalSummary struct {
	ID                      string                  `json:"id"`
	TaskID                  string                  `json:"task_id"`
	TaskGoal                string                  `json:"task_goal,omitempty"`
	RunID                   string                  `json:"run_id"`
	RunPurpose              string                  `json:"run_purpose"`
	RunStatus               string                  `json:"run_status"`
	ThreadID                string                  `json:"thread_id,omitempty"`
	TurnID                  string                  `json:"turn_id,omitempty"`
	ItemID                  string                  `json:"item_id,omitempty"`
	Kind                    string                  `json:"kind"`
	Status                  string                  `json:"status"`
	AllowedDecisions        []approval.DecisionType `json:"allowed_decisions"`
	RequestPayload          string                  `json:"request_payload"`
	RequestPayloadTruncated bool                    `json:"request_payload_truncated"`
	Decision                *approval.Decision      `json:"decision,omitempty"`
	CreatedAt               time.Time               `json:"created_at"`
	DecidedAt               *time.Time              `json:"decided_at,omitempty"`
	DeliveredAt             *time.Time              `json:"delivered_at,omitempty"`
}

type FailureDetails struct {
	Kind    string `json:"kind"`
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type EvidenceDetails struct {
	Available             bool     `json:"available"`
	BaseCommit            string   `json:"base_commit,omitempty"`
	HeadCommit            string   `json:"head_commit,omitempty"`
	StartSnapshot         string   `json:"start_snapshot,omitempty"`
	EndSnapshot           string   `json:"end_snapshot,omitempty"`
	Dirty                 bool     `json:"dirty"`
	ChangedFiles          []string `json:"changed_files"`
	ChangedFilesTruncated bool     `json:"changed_files_truncated"`
	DiffArtifactID        string   `json:"diff_artifact_id,omitempty"`
	Diff                  string   `json:"diff,omitempty"`
	DiffTruncated         bool     `json:"diff_truncated"`
	AgentClaim            string   `json:"agent_claim,omitempty"`
	AgentClaimTruncated   bool     `json:"agent_claim_truncated"`
}

type EventSummary struct {
	Sequence     int64     `json:"sequence"`
	Type         string    `json:"type"`
	From         string    `json:"from,omitempty"`
	To           string    `json:"to,omitempty"`
	ReasonCode   string    `json:"reason_code,omitempty"`
	DecisionType string    `json:"decision_type,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type RunFrozenConfig struct {
	Model                    string `json:"model"`
	ReasoningEffort          string `json:"reasoning_effort"`
	Source                   string `json:"source"`
	TaskDefinitionRevisionID string `json:"task_definition_revision_id"`
}
type RunDetail struct {
	NativeOutput          string                      `json:"native_output,omitempty"`
	NativeOutputTruncated bool                        `json:"native_output_truncated"`
	Analysis              *runanalysis.RecordedReport `json:"analysis,omitempty"`
	AnalysisReadError     string                      `json:"analysis_read_error,omitempty"`
	SessionState          SessionState                `json:"session_state"`
	Handoff               executionproposal.Handoff   `json:"handoff"`
	ReviewEvidenceStatus  string                      `json:"review_evidence_status"`
	ReviewEvidenceError   string                      `json:"review_evidence_error,omitempty"`
	ReviewResultStatus    string                      `json:"review_result_status"`

	FrozenConfig          RunFrozenConfig        `json:"frozen_config"`
	Runtime               *agentruntime.Snapshot `json:"runtime,omitempty"`
	ElapsedMS             *int64                 `json:"elapsed_ms"`
	DurationComplete      bool                   `json:"duration_complete"`
	ReviewEvidence        *ReviewEvidenceSummary `json:"review_evidence,omitempty"`
	ReviewResult          string                 `json:"review_result,omitempty"`
	ReviewResultTruncated bool                   `json:"review_result_truncated"`
	RuntimeEpoch          string                 `json:"runtime_epoch,omitempty"`

	Run                RunSummary              `json:"run"`
	ProjectID          string                  `json:"project_id"`
	ModuleID           string                  `json:"module_id"`
	RepositoryID       string                  `json:"repository_id"`
	CodexConfigID      string                  `json:"codex_config_id"`
	StartSnapshot      string                  `json:"start_snapshot,omitempty"`
	EndSnapshot        string                  `json:"end_snapshot,omitempty"`
	Workspace          *WorkspaceSummary       `json:"workspace,omitempty"`
	Failure            *FailureDetails         `json:"failure,omitempty"`
	VerificationReport *run.VerificationReport `json:"verification_report,omitempty"`
	Evidence           EvidenceDetails         `json:"evidence"`
	Approvals          []ApprovalSummary       `json:"approvals"`
	ApprovalsCut       bool                    `json:"approvals_truncated"`
	Events             []EventSummary          `json:"events"`
	EventsCut          bool                    `json:"events_truncated"`
}

func (r *Reader) ListProjects(ctx context.Context, request PageRequest) (Page[ProjectSummary], error) {
	limit, offset, err := boundedPage(request)
	if err != nil {
		return Page[ProjectSummary]{}, err
	}
	values, err := r.plane.Projects.List(ctx)
	if err != nil {
		return Page[ProjectSummary]{}, controlapi.MapError(err)
	}
	repositories, err := r.plane.Repositories.List(ctx)
	if err != nil {
		return Page[ProjectSummary]{}, controlapi.MapError(err)
	}
	counts := make(map[string]int)
	for _, value := range repositories {
		counts[value.ProjectID]++
	}
	selection, _ := r.currentSelection(ctx)
	sort.Slice(values, func(i, j int) bool {
		if values[i].Name == values[j].Name {
			return values[i].ID < values[j].ID
		}
		return values[i].Name < values[j].Name
	})
	items := make([]ProjectSummary, 0, min(limit, len(values)))
	for _, value := range values {
		status := ""
		refreshing, refreshFailed := false, false
		if contextValue, getErr := r.plane.ProjectContexts.Get(ctx, value.ID); getErr == nil {
			status, refreshing, refreshFailed = productProjectContextState(contextValue)
		} else if !errors.Is(getErr, projectcontext.ErrNotFound) {
			return Page[ProjectSummary]{}, controlapi.MapError(getErr)
		}
		items = append(items, ProjectSummary{ID: value.ID, Name: summarizeOnly(value.Name, MaxSummaryBytes), Selected: selection.CurrentSelectedProjectID == value.ID, ProjectContextStatus: status, ProjectContextRefreshing: refreshing, ProjectContextRefreshFailed: refreshFailed, RepositoryCount: counts[value.ID], CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt})
	}
	return paginate(items, limit, offset), nil
}

func (r *Reader) CurrentSelection(ctx context.Context) (Selection, error) {
	value, err := r.currentSelection(ctx)
	if err != nil {
		return Selection{}, err
	}
	if value.CurrentSelectedProjectID == "" {
		return Selection{Selected: false}, nil
	}
	updated := value.UpdatedAt
	return Selection{Selected: true, SelectedProjectID: value.CurrentSelectedProjectID, UpdatedAt: &updated}, nil
}

func (r *Reader) currentSelection(ctx context.Context) (projectselection.Selection, error) {
	value, err := r.plane.ProjectSelection.Current(ctx)
	if errors.Is(err, projectselection.ErrNotSelected) {
		return projectselection.Selection{}, nil
	}
	if err != nil {
		return projectselection.Selection{}, controlapi.MapError(err)
	}
	return value, nil
}

func (r *Reader) SelectProject(ctx context.Context, projectID string) (Selection, error) {
	if err := validID("project_id", projectID); err != nil {
		return Selection{}, err
	}
	value, err := r.control.SelectProject(ctx, projectID)
	if err != nil {
		return Selection{}, err
	}
	updated := value.UpdatedAt
	return Selection{Selected: true, SelectedProjectID: value.CurrentSelectedProjectID, UpdatedAt: &updated}, nil
}

func (r *Reader) GetProjectOverview(ctx context.Context, projectID string) (ProjectOverview, error) {
	if err := validID("project_id", projectID); err != nil {
		return ProjectOverview{}, err
	}
	value, err := r.plane.Projects.Get(ctx, projectID)
	if err != nil {
		return ProjectOverview{}, controlapi.MapError(err)
	}
	selection, err := r.currentSelection(ctx)
	if err != nil {
		return ProjectOverview{}, err
	}
	repositories, err := r.plane.Repositories.List(ctx)
	if err != nil {
		return ProjectOverview{}, controlapi.MapError(err)
	}
	count := 0
	for _, item := range repositories {
		if item.ProjectID == projectID {
			count++
		}
	}
	result := ProjectOverview{Project: ProjectSummary{ID: value.ID, Name: summarizeOnly(value.Name, MaxSummaryBytes), Selected: selection.CurrentSelectedProjectID == value.ID, RepositoryCount: count, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}, Context: ContextSummary{ProjectID: value.ID}}
	contextValue, getErr := r.plane.ProjectContexts.Get(ctx, projectID)
	if getErr == nil {
		result.Context = makeContextSummary(contextValue)
		result.Project.ProjectContextStatus, result.Project.ProjectContextRefreshing, result.Project.ProjectContextRefreshFailed = productProjectContextState(contextValue)
	} else if !errors.Is(getErr, projectcontext.ErrNotFound) {
		return ProjectOverview{}, controlapi.MapError(getErr)
	}
	probes, err := r.plane.ContextProbes.ListByProject(ctx, projectID)
	if err != nil {
		return ProjectOverview{}, controlapi.MapError(err)
	}
	if len(probes) > 0 {
		sort.Slice(probes, func(i, j int) bool { return probes[i].CreatedAt.After(probes[j].CreatedAt) })
		probe := makeProbeSummary(probes[0])
		result.LatestProbe = &probe
	}
	return result, nil
}

func (r *Reader) ListRepositories(ctx context.Context, projectID string, request PageRequest) (Page[RepositorySummary], error) {
	limit, offset, err := r.projectPage(ctx, projectID, request)
	if err != nil {
		return Page[RepositorySummary]{}, err
	}
	values, err := r.plane.Repositories.List(ctx)
	if err != nil {
		return Page[RepositorySummary]{}, controlapi.MapError(err)
	}
	items := make([]RepositorySummary, 0)
	for _, value := range values {
		if value.ProjectID != projectID {
			continue
		}
		item := RepositorySummary{ID: value.ID, ProjectID: value.ProjectID, Name: summarizeOnly(value.Name, MaxSummaryBytes), HEADStatus: "UNAVAILABLE"}
		if head, headErr := r.plane.Repositories.CurrentHEAD(ctx, value.ID); headErr == nil {
			item.CurrentHEAD, item.HEADStatus = head, "AVAILABLE"
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return paginate(items, limit, offset), nil
}

func (r *Reader) ListModules(ctx context.Context, projectID string, request PageRequest) (Page[ModuleSummary], error) {
	limit, offset, err := r.projectPage(ctx, projectID, request)
	if err != nil {
		return Page[ModuleSummary]{}, err
	}
	values, err := r.plane.Modules.ListByProject(ctx, projectID)
	if err != nil {
		return Page[ModuleSummary]{}, controlapi.MapError(err)
	}
	items := make([]ModuleSummary, 0, len(values))
	for _, value := range values {
		items = append(items, ModuleSummary{ID: value.ID, ProjectID: value.ProjectID, Key: value.Key, Name: summarizeOnly(value.Name, MaxSummaryBytes), Description: summarizeOnly(value.Description, 4096), SystemOwned: value.SystemOwned, Unclassified: value.Key == module.UnclassifiedKey && value.SystemOwned, ParentID: value.ParentID, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt})
	}
	return paginate(items, limit, offset), nil
}

func (r *Reader) ListAISessions(ctx context.Context, projectID string, request PageRequest) (Page[AISessionSummary], error) {
	limit, offset, err := r.projectPage(ctx, projectID, request)
	if err != nil {
		return Page[AISessionSummary]{}, err
	}
	values, err := r.plane.AISessions.List(ctx)
	if err != nil {
		return Page[AISessionSummary]{}, controlapi.MapError(err)
	}
	items := make([]AISessionSummary, 0)
	for _, value := range values {
		if value.ProjectID == projectID {
			items = append(items, AISessionSummary{ID: value.ID, ProjectID: value.ProjectID, CreatedAt: value.CreatedAt, RecentActivityAt: value.UpdatedAt})
		}
	}
	return paginate(items, limit, offset), nil
}

func (r *Reader) ListContextProbes(ctx context.Context, projectID string, request PageRequest) (Page[ContextProbeSummary], error) {
	limit, offset, err := r.projectPage(ctx, projectID, request)
	if err != nil {
		return Page[ContextProbeSummary]{}, err
	}
	values, err := r.plane.ContextProbes.ListByProject(ctx, projectID)
	if err != nil {
		return Page[ContextProbeSummary]{}, controlapi.MapError(err)
	}
	items := make([]ContextProbeSummary, 0, len(values))
	for _, value := range values {
		items = append(items, makeProbeSummary(value))
	}
	return paginate(items, limit, offset), nil
}

type TaskListFilter struct {
	PageRequest
	WorkKind string
}

func (r *Reader) ListTasks(ctx context.Context, projectID string, request TaskListFilter) (Page[TaskSummary], error) {
	limit, offset, err := r.projectPage(ctx, projectID, request.PageRequest)
	if err != nil {
		return Page[TaskSummary]{}, err
	}
	tasks, err := r.plane.Tasks.List(ctx)
	if err != nil {
		return Page[TaskSummary]{}, controlapi.MapError(err)
	}
	runs, err := r.plane.Runs.List(ctx)
	if err != nil {
		return Page[TaskSummary]{}, controlapi.MapError(err)
	}
	items := make([]TaskSummary, 0)
	for _, value := range tasks {
		if value.ProjectID != projectID {
			continue
		}
		if request.WorkKind != "" && string(value.WorkKind) != request.WorkKind {
			continue
		}
		summary, summaryErr := r.makeTaskSummary(ctx, value, runs)
		if summaryErr != nil {
			return Page[TaskSummary]{}, summaryErr
		}
		items = append(items, summary)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ModuleName == items[j].ModuleName {
			return items[i].ID < items[j].ID
		}
		return items[i].ModuleName < items[j].ModuleName
	})
	return paginate(items, limit, offset), nil
}

func (r *Reader) GetTask(ctx context.Context, projectID, taskID string) (TaskDetail, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return TaskDetail{}, err
	}
	value, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil || value.ProjectID != projectID {
		return TaskDetail{}, notFound()
	}
	runs, err := r.plane.Runs.List(ctx)
	if err != nil {
		return TaskDetail{}, controlapi.MapError(err)
	}
	summary, err := r.makeTaskSummary(ctx, value, runs)
	if err != nil {
		return TaskDetail{}, err
	}
	allTaskRuns := make([]run.Run, 0)
	for _, item := range runs {
		if item.TaskID == value.ID {
			allTaskRuns = append(allTaskRuns, item)
		}
	}
	sort.Slice(allTaskRuns, func(i, j int) bool { return allTaskRuns[i].Attempt > allTaskRuns[j].Attempt })
	filtered := allTaskRuns
	runHistoryCut := len(allTaskRuns) > MaxRunHistory
	if runHistoryCut {
		filtered = allTaskRuns[:MaxRunHistory]
	}
	runHistory := make([]RunSummary, 0, len(filtered))
	for _, item := range filtered {
		approvals, listErr := r.plane.Approvals.ListByRun(ctx, item.ID)
		if listErr != nil {
			return TaskDetail{}, controlapi.MapError(listErr)
		}
		runHistory = append(runHistory, makeRunSummary(item, value.Goal, openApprovalCount(approvals)))
	}
	workspaceValue, workspaceErr := r.workspaceForTask(ctx, value.ID)
	if workspaceErr != nil {
		return TaskDetail{}, workspaceErr
	}
	probes, err := r.plane.ContextProbes.ListByProject(ctx, value.ProjectID)
	if err != nil {
		return TaskDetail{}, controlapi.MapError(err)
	}
	plans, err := r.plane.TaskPlans.ListByProject(ctx, value.ProjectID)
	if err != nil {
		return TaskDetail{}, controlapi.MapError(err)
	}
	taskPlans := make([]taskplan.Plan, 0)
	for _, plan := range plans {
		if plan.TaskID == value.ID {
			taskPlans = append(taskPlans, plan)
		}
	}

	expansions, err := r.plane.ScopeExpansions.ListByProject(ctx, value.ProjectID)
	if err != nil {
		return TaskDetail{}, controlapi.MapError(err)
	}
	taskExpansions := make([]ScopeExpansionSummary, 0)
	for _, expansion := range expansions {
		if expansion.LegacyTaskID != value.ID {
			continue
		}
		reason, reasonCut := summarize(expansion.Reason, 2048)
		taskExpansions = append(taskExpansions, ScopeExpansionSummary{
			ID: expansion.ID, CurrentScope: cloneReviewScope(expansion.CurrentScope), AdditionalScope: cloneReviewScope(expansion.AdditionalScope),
			Reason: reason, ReasonTruncated: reasonCut, Status: string(expansion.Status), CreatedAt: expansion.CreatedAt, UpdatedAt: expansion.UpdatedAt, DecidedAt: cloneTime(expansion.DecidedAt),
		})
		if len(taskExpansions) == MaxLimit {
			break
		}
	}
	reviewSummaries := []ReviewSummary{}
	if value.FeatureAreaID != "" {
		reviews, listErr := r.plane.Reviews.ListByFeatureArea(ctx, value.FeatureAreaID)
		if listErr != nil {
			return TaskDetail{}, controlapi.MapError(listErr)
		}
		runByID := make(map[string]run.Run, len(allTaskRuns))
		for _, runValue := range allTaskRuns {
			runByID[runValue.ID] = runValue
		}
		for _, reviewValue := range reviews {
			if reviewValue.ProjectID != projectID || reviewValue.TaskID != value.ID {
				continue
			}
			state := "STARTING"
			var engineeringReview, agentClaim string
			var resultTruncated, agentClaimTruncated bool
			usage := TokenUsage{Status: string(run.TokenUsagePending)}
			var operation *OperationUsage
			diagnostics := ReviewDiagnostics{}
			if runValue, ok := runByID[reviewValue.RunID]; ok {
				state = consoleReviewState(runValue)
				usage = tokenUsage(runValue.TokenUsage)
				operation = operationUsage(runValue)
				diagnostics = ReviewDiagnostics{RunID: runValue.ID, WorkspaceID: runValue.WorkspaceID, WorktreeID: runValue.WorktreeID, ThreadID: runValue.ThreadID, TurnID: runValue.TurnID}
				if runValue.Result != nil {
					engineeringReview, resultTruncated = summarize(runValue.Result.AgentClaim.Message, MaxPayloadBytes)
					agentClaim, agentClaimTruncated = summarize(runValue.Result.AgentClaim.Message, MaxPayloadBytes)
				}
			} else if reviewValue.StartFailure != nil {
				state = "BLOCKED"
			}
			evidenceValue, evidenceErr := r.plane.Reviews.SummarizeEvidence(ctx, reviewValue.ID)
			if evidenceErr != nil {
				return TaskDetail{}, controlapi.MapError(evidenceErr)
			}
			reviewSummaries = append(reviewSummaries, ReviewSummary{ExecutorKind: reviewValue.ExecutorKind.Effective(), ReportID: reviewValue.ReportID, ReviewDepth: reviewValue.ReviewDepth, ReviewDisposition: reviewValue.ReviewDisposition, ID: reviewValue.ID, FeatureAreaID: reviewValue.FeatureAreaID, Focus: reviewFocusSummary(reviewValue.Focus), State: state, Usage: operation, EngineeringReview: engineeringReview, ResultTruncated: resultTruncated, AgentClaim: agentClaim, AgentClaimTruncated: agentClaimTruncated, Evidence: ReviewEvidenceSummary{SuccessfulOperations: evidenceValue.Successes, ForbiddenOperations: evidenceValue.Forbidden, FailedOperations: evidenceValue.Failures}, TokenUsage: usage, Diagnostics: diagnostics, PromptAvailable: reviewValue.FinalCodexPrompt != "", PromptSHA256: reviewValue.PromptSHA256, PromptBytes: reviewValue.PromptBytes, PromptSentAt: cloneTime(reviewValue.PromptSentAt), CreatedAt: reviewValue.CreatedAt})
		}
	}
	executionHistory := []ExecutionHistorySummary{}
	if r.plane.ExecutionProposals != nil {
		history, err := r.plane.ExecutionProposals.HistoryForTask(ctx, value.ID)
		if err != nil {
			return TaskDetail{}, controlapi.MapError(err)
		}
		for _, execution := range history {
			executionHistory = append(executionHistory, ExecutionHistorySummary{ID: execution.ID, TaskDefinitionRevisionID: execution.TaskDefinitionRevisionID, ProvenanceKind: execution.ProvenanceKind, Status: string(execution.Status), Outcome: string(execution.Outcome)})
		}
	}
	return TaskDetail{
		Task: summary, ExecutionHistory: executionHistory, CurrentDefinitionRevisionID: value.CurrentDefinitionRevisionID, BaseCommit: value.BaseCommit, WorkSpec: task.CloneWorkSpec(value.WorkSpec), VerificationSpec: task.CloneVerificationSpec(value.VerificationSpec),
		TaskPlans: taskPlans, ScopeExpansions: taskExpansions, Reviews: reviewSummaries, Workspace: workspaceValue, RunHistory: runHistory, RunHistoryCut: runHistoryCut,
		Accounting: TaskAccounting{
			TopicReview:    accountingForRuns(filterRunsByLink(allTaskRuns, true), "PLANNING_REVIEW"),
			Implementation: accountingForRuns(filterRuns(allTaskRuns, run.PurposeImplementation), "REQUESTED"),
			Verification:   accountingForRuns(filterRuns(allTaskRuns, run.PurposeVerification), verificationDisposition(value.VerificationSpec)),
			ContextProbe:   accountingForProbes(probes),
		},
	}, nil
}

func consoleReviewState(value run.Run) string {
	if value.Status == run.StatusInfrastructureFailed || value.Failure != nil && (value.Failure.Code == "REVIEW_CONTEXT_UNAVAILABLE" || value.Failure.Code == "REVIEW_EVIDENCE_INSUFFICIENT") {
		return "BLOCKED"
	}
	return string(value.Status)
}

// GetReviewPrompt returns one exact persisted Review prompt selected only by a
// product Review ID and a required Project boundary. It never accepts a file,
// SQL predicate, log path, or free-form search operand.
func (r *Reader) GetReviewPrompt(ctx context.Context, projectID, reviewID string) (ReviewPromptDiagnostic, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ReviewPromptDiagnostic{}, err
	}
	if err := validID("review_id", reviewID); err != nil {
		return ReviewPromptDiagnostic{}, err
	}
	value, err := r.plane.Reviews.Get(ctx, reviewID)
	if err != nil || value.ProjectID != projectID {
		return ReviewPromptDiagnostic{}, notFound()
	}
	return ReviewPromptDiagnostic{ReviewID: value.ID, Available: value.FinalCodexPrompt != "", Prompt: value.FinalCodexPrompt, SHA256: value.PromptSHA256, Bytes: value.PromptBytes, SentAt: cloneTime(value.PromptSentAt), CreatedAt: value.CreatedAt}, nil
}

// ReviewRepositoryDrift exposes objective Git drift facts between the
// repository state one Review was based on and the current repository state.
// Bounded and factual: no staleness, validity, or rerun semantics exist here.
func (r *Reader) ReviewRepositoryDrift(ctx context.Context, projectID, reviewID string, limit int) (productreview.RepositoryDrift, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return productreview.RepositoryDrift{}, err
	}
	if err := validID("review_id", reviewID); err != nil {
		return productreview.RepositoryDrift{}, err
	}
	return r.control.ReviewRepositoryDrift(ctx, projectID, reviewID, limit)
}

// GetRunPrompt returns the exact prompt persisted before one Codex turn. The
// owning Task supplies the mandatory Project boundary.
func (r *Reader) GetRunPrompt(ctx context.Context, projectID, runID string) (RunPromptDiagnostic, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return RunPromptDiagnostic{}, err
	}
	if err := validID("run_id", runID); err != nil {
		return RunPromptDiagnostic{}, err
	}
	value, err := r.plane.Runs.Get(ctx, runID)
	if err != nil {
		return RunPromptDiagnostic{}, notFound()
	}
	if _, err := r.ownerForRun(ctx, projectID, value); err != nil {
		return RunPromptDiagnostic{}, err
	}
	return RunPromptDiagnostic{RunID: value.ID, Purpose: string(value.Purpose), Available: value.FinalCodexPrompt != "", Prompt: value.FinalCodexPrompt, SHA256: value.PromptSHA256, Bytes: value.PromptBytes, SentAt: cloneTime(value.PromptSentAt), CreatedAt: value.CreatedAt}, nil
}

// GetContextProbePrompt returns the exact persisted prompt for one Context
// Probe, selected only through its owning Project boundary.
func (r *Reader) GetContextProbePrompt(ctx context.Context, projectID, probeID string) (ContextProbePromptDiagnostic, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ContextProbePromptDiagnostic{}, err
	}
	if err := validID("context_probe_id", probeID); err != nil {
		return ContextProbePromptDiagnostic{}, err
	}
	value, err := r.plane.ContextProbes.Get(ctx, probeID)
	if err != nil || value.ProjectID != projectID {
		return ContextProbePromptDiagnostic{}, notFound()
	}
	return ContextProbePromptDiagnostic{ContextProbeID: value.ID, Available: value.FinalCodexPrompt != "", Prompt: value.FinalCodexPrompt, SHA256: value.PromptSHA256, Bytes: value.PromptBytes, SentAt: cloneTime(value.PromptSentAt), CreatedAt: value.CreatedAt}, nil
}

type RunListFilter struct {
	PageRequest
	ActiveOnly    bool
	Status        run.Status
	Scope         string
	FeatureAreaID string
}

func (r *Reader) ListRuns(ctx context.Context, projectID string, filter RunListFilter) (Page[RunSummary], error) {
	limit, offset, err := r.projectPage(ctx, projectID, filter.PageRequest)
	if err != nil {
		return Page[RunSummary]{}, err
	}
	if filter.Status != "" && !knownRunStatus(filter.Status) {
		return Page[RunSummary]{}, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "unknown run status"}
	}
	if filter.Scope == "" {
		filter.Scope = "all"
	}
	if filter.ActiveOnly {
		filter.Scope = "current"
	}
	if filter.Scope != "all" && filter.Scope != "current" && filter.Scope != "history" {
		return Page[RunSummary]{}, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "scope must be current, history or all"}
	}
	if filter.FeatureAreaID != "" {
		if err := validID("feature_area_id", filter.FeatureAreaID); err != nil {
			return Page[RunSummary]{}, err
		}
	}
	values, err := r.plane.Runs.ListProjectPage(ctx, run.ProjectPage{ProjectID: projectID, FeatureAreaID: filter.FeatureAreaID, Scope: filter.Scope, Status: filter.Status, Limit: limit + 1, Offset: offset})
	if err != nil {
		return Page[RunSummary]{}, controlapi.MapError(err)
	}
	out := Page[RunSummary]{Items: []RunSummary{}, Limit: limit, Offset: offset, HasMore: len(values) > limit}
	if out.HasMore {
		values = values[:limit]
	}
	for _, value := range values {
		owner, ownerErr := r.ownerForRun(ctx, projectID, value)
		if ownerErr != nil {
			continue
		}
		approvals, listErr := r.plane.Approvals.ListByRun(ctx, value.ID)
		if listErr != nil {
			return Page[RunSummary]{}, controlapi.MapError(listErr)
		}
		summary := makeRunSummary(value, owner.Goal, openApprovalCount(approvals))
		summary.ExecutorKind = owner.ExecutorKind
		summary.ReviewAuthorizationRequired = owner.ReviewAuthorizationRequired
		if value.TaskID != "" {
			if t, e := r.plane.Tasks.Get(ctx, value.TaskID); e == nil && t.ProjectID == projectID {
				summary.TaskTitle = summarizeOnly(t.Title, 2048)
				summary.TaskPhase = string(t.Phase)
			}
		}
		out.Items = append(out.Items, summary)
	}
	return out, nil
}

func (r *Reader) GetRun(ctx context.Context, projectID, runID string) (RunDetail, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return RunDetail{}, err
	}
	runValue, err := r.plane.Runs.Get(ctx, runID)
	if err != nil {
		return RunDetail{}, controlapi.MapError(err)
	}
	owner, err := r.ownerForRun(ctx, projectID, runValue)
	if err != nil {
		return RunDetail{}, err
	}
	approvalValues, err := r.plane.Approvals.ListByRun(ctx, runValue.ID)
	if err != nil {
		return RunDetail{}, controlapi.MapError(err)
	}
	approvalsCut := len(approvalValues) > MaxLimit
	if approvalsCut {
		approvalValues = approvalValues[len(approvalValues)-MaxLimit:]
	}
	approvals := make([]ApprovalSummary, 0, len(approvalValues))
	for _, value := range approvalValues {
		approvals = append(approvals, makeApprovalSummary(value, runValue, owner.Goal))
	}
	eventValues, err := r.control.ListEvents(ctx, controlapi.EventFilter{RunID: runValue.ID, Limit: MaxEventCount})
	if err != nil {
		return RunDetail{}, err
	}
	events := make([]EventSummary, 0, len(eventValues))
	for _, value := range eventValues {
		events = append(events, makeEventSummary(value))
	}
	workspaceValue, workspaceErr := r.workspaceForRun(ctx, runValue)
	if workspaceErr != nil {
		return RunDetail{}, workspaceErr
	}
	detail := RunDetail{
		Run: makeRunSummary(runValue, owner.Goal, openApprovalCount(approvalValues)), ProjectID: projectID, ModuleID: owner.ModuleID,
		RepositoryID: owner.RepositoryID, CodexConfigID: owner.CodexConfigID, StartSnapshot: runValue.StartSnapshot, EndSnapshot: runValue.EndSnapshot,
		Workspace: workspaceValue, VerificationReport: boundedVerificationReport(runValue.VerificationReport), Evidence: EvidenceDetails{ChangedFiles: []string{}}, Approvals: approvals, ApprovalsCut: approvalsCut,
		Events: events, EventsCut: len(eventValues) == MaxEventCount,
	}
	detail.FrozenConfig = RunFrozenConfig{Model: owner.Model, ReasoningEffort: owner.ReasoningEffort, Source: owner.ConfigSource, TaskDefinitionRevisionID: owner.RevisionID}
	detail.Run.ExecutorKind = owner.ExecutorKind
	detail.Run.ReviewAuthorizationRequired = owner.ReviewAuthorizationRequired
	if runValue.TaskID != "" {
		if taskValue, taskErr := r.plane.Tasks.Get(ctx, runValue.TaskID); taskErr == nil && taskValue.ProjectID == projectID {
			detail.Run.TaskTitle = summarizeOnly(taskValue.Title, 2048)
			detail.Run.TaskPhase = string(taskValue.Phase)
		}
	}
	if detail.FrozenConfig.Source == "" {
		detail.FrozenConfig.Source = "UNKNOWN"
	}
	detail.Runtime = boundedRunRuntime(runValue)
	detail.Run.AgentRuntime = detail.Runtime
	detail.ElapsedMS, detail.DurationComplete = presentation.RunElapsed(runValue, time.Now())
	detail.RuntimeEpoch = runValue.RuntimeEpoch
	detail.Handoff, err = r.plane.ExecutionProposals.ReadHandoff(ctx, projectID, runValue.ID)
	if err != nil {
		detail.Handoff = executionproposal.Handoff{RunID: runValue.ID, State: executionproposal.HandoffUnknown, Code: "HANDOFF_EVIDENCE_UNAVAILABLE", OwnerKind: "IN_PROCESS_SUPERVISOR"}
	}
	detail.SessionState = sessionState(runValue, detail.Handoff)
	detail.ReviewEvidenceStatus = "NOT_APPLICABLE"
	detail.ReviewResultStatus = "NOT_APPLICABLE"
	if runValue.ReviewID != "" {
		detail.ReviewResultStatus = "PENDING"
		if terminalStatus(runValue.Status) {
			detail.ReviewResultStatus = "UNAVAILABLE"
		}
		evidence, e := r.plane.Reviews.SummarizeEvidence(ctx, runValue.ReviewID)
		if e == nil {
			detail.ReviewEvidenceStatus = "AVAILABLE"
			detail.ReviewEvidence = &ReviewEvidenceSummary{SuccessfulOperations: evidence.Successes, ForbiddenOperations: evidence.Forbidden, FailedOperations: evidence.Failures, LastObservedAt: cloneTime(evidence.LastObservedAt)}
		} else {
			detail.ReviewEvidenceStatus = "READ_FAILED"
			detail.ReviewEvidenceError = "Review evidence could not be read"
		}
		if runValue.Result != nil && runValue.Result.AgentClaim.Message != "" {
			detail.ReviewResultStatus = "AVAILABLE"
			detail.ReviewResult = summarizeOnly(runValue.Result.AgentClaim.Message, MaxArtifactBytes)
			detail.ReviewResultTruncated = len(runValue.Result.AgentClaim.Message) > MaxArtifactBytes
		}
	}
	if runValue.ResultProtocol == run.NaturalResultProtocol {
		analysis, err := r.control.RecordedRunAnalysisForOperator(ctx, projectID, runValue.ID)
		if err != nil {
			detail.AnalysisReadError = "GPT 分析读取失败，请刷新重试。"
		} else {
			detail.Analysis = &analysis
		}
	}
	if runValue.Result != nil {
		detail.NativeOutput = summarizeOnly(runValue.Result.AgentClaim.Message, MaxArtifactBytes)
		detail.NativeOutputTruncated = len(runValue.Result.AgentClaim.Message) > MaxArtifactBytes
		detail.Evidence.AgentClaim = summarizeOnly(runValue.Result.AgentClaim.Message, MaxArtifactBytes)
		detail.Evidence.AgentClaimTruncated = len(runValue.Result.AgentClaim.Message) > MaxArtifactBytes
	}
	if runValue.Failure != nil {
		detail.Failure = &FailureDetails{Kind: string(runValue.Failure.Kind), Stage: summarizeOnly(runValue.Failure.Stage, MaxSummaryBytes), Code: summarizeOnly(runValue.Failure.Code, MaxSummaryBytes), Message: summarizeOnly(runValue.Failure.Message, 2048)}
	}
	if evidenceValue, evidenceErr := r.plane.Evidence.Get(ctx, runValue.ID); evidenceErr == nil {
		detail.Evidence.Available = true
		detail.Evidence.BaseCommit, detail.Evidence.HeadCommit = evidenceValue.BaseCommit, evidenceValue.HeadCommit
		detail.Evidence.StartSnapshot, detail.Evidence.EndSnapshot, detail.Evidence.Dirty = evidenceValue.StartSnapshot, evidenceValue.EndSnapshot, evidenceValue.Dirty
		detail.Evidence.ChangedFiles = append([]string(nil), evidenceValue.ChangedFiles...)
		if len(detail.Evidence.ChangedFiles) > MaxChangedFiles {
			detail.Evidence.ChangedFiles = detail.Evidence.ChangedFiles[:MaxChangedFiles]
			detail.Evidence.ChangedFilesTruncated = true
		}
		detail.Evidence.DiffArtifactID = evidenceValue.DiffArtifactID
		if err := r.readEvidenceArtifacts(ctx, runValue.ID, &detail.Evidence); err != nil {
			return RunDetail{}, err
		}
	} else if !isNotFound(evidenceErr) {
		return RunDetail{}, controlapi.MapError(evidenceErr)
	}
	return detail, nil
}

type ApprovalListFilter struct {
	PageRequest
	PendingOnly bool
}

func (r *Reader) ListApprovals(ctx context.Context, projectID string, filter ApprovalListFilter) (Page[ApprovalSummary], error) {
	limit, offset, err := r.projectPage(ctx, projectID, filter.PageRequest)
	if err != nil {
		return Page[ApprovalSummary]{}, err
	}
	runs, err := r.plane.Runs.List(ctx)
	if err != nil {
		return Page[ApprovalSummary]{}, controlapi.MapError(err)
	}
	items := make([]ApprovalSummary, 0)
	for _, runValue := range runs {
		owner, ownerErr := r.ownerForRun(ctx, projectID, runValue)
		if ownerErr != nil {
			continue
		}
		values, listErr := r.plane.Approvals.ListByRun(ctx, runValue.ID)
		if listErr != nil {
			return Page[ApprovalSummary]{}, controlapi.MapError(listErr)
		}
		for _, value := range values {
			if filter.PendingOnly && !openApproval(value.Status) {
				continue
			}
			items = append(items, makeApprovalSummary(value, runValue, owner.Goal))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return paginate(items, limit, offset), nil
}

func (r *Reader) makeTaskSummary(ctx context.Context, value task.Task, allRuns []run.Run) (TaskSummary, error) {
	// Both list and detail have authorized this Task's Project before reaching
	// this shared presentation boundary.
	if err := r.control.EnrichTaskExecution(ctx, &value); err != nil {
		return TaskSummary{}, err
	}
	moduleValue, err := r.plane.Modules.Get(ctx, value.ModuleID)
	if err != nil && value.ModuleID != "" {
		return TaskSummary{}, controlapi.MapError(err)
	}
	repositoryValue, err := r.plane.Repositories.Get(ctx, value.RepositoryID)
	if err != nil && value.RepositoryID != "" {
		return TaskSummary{}, controlapi.MapError(err)
	}
	configValue, err := r.plane.CodexConfigs.Get(ctx, value.CodexConfigID)
	if err != nil && value.CodexConfigID != "" {
		return TaskSummary{}, controlapi.MapError(err)
	}
	goal, goalCut := summarize(value.Goal, 2048)
	problem, problemCut := summarize(value.ProblemStatement, 2048)
	planStatus := "NONE"
	if value.ConfirmedTaskPlanID != "" {
		planStatus = "APPLIED"
	}
	if plans, listErr := r.plane.TaskPlans.ListByProject(ctx, value.ProjectID); listErr == nil {
		for _, plan := range plans {
			if plan.TaskID == value.ID && plan.Status != taskplan.StatusApplied {
				planStatus = string(plan.Status)
				break
			}
		}
	}
	result := TaskSummary{RequiredReviewDepth: value.RequiredReviewDepth, AllowedReviewDepths: task.AllowedReviewDepths(value.RequiredReviewDepth), Title: value.Title, Phase: value.Phase, CurrentReviewID: value.CurrentReviewID, CurrentExecutionID: value.CurrentExecutionID, Work: value.Work,
		ID: value.ID, ProjectID: value.ProjectID, WorkKind: string(value.WorkKind), Model: value.Model, ReasoningEffort: value.ReasoningEffort, Goal: goal, GoalTruncated: goalCut, ProblemStatement: problem, ProblemTruncated: problemCut, TaskPlanStatus: planStatus, ConfirmedTaskPlanID: value.ConfirmedTaskPlanID, Status: string(value.Status), ModuleID: value.ModuleID, ModuleName: summarizeOnly(moduleValue.Name, MaxSummaryBytes),
		ModuleKey: moduleValue.Key, Unclassified: moduleValue.Key == module.UnclassifiedKey && moduleValue.SystemOwned, RepositoryID: value.RepositoryID,
		RepositoryName: summarizeOnly(repositoryValue.Name, MaxSummaryBytes), CodexConfigID: value.CodexConfigID, CodexConfigName: summarizeOnly(configValue.Name, MaxSummaryBytes), VerificationMode: string(value.VerificationSpec.Mode),
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}

	if value.VerificationSpec.Waiver != nil {
		result.VerificationWaiver = string(value.VerificationSpec.Waiver.Source)
	}
	var latest *run.Run
	for _, item := range allRuns {
		if item.TaskID != value.ID {
			continue
		}
		result.RunCount++
		if latest == nil || item.CreatedAt.After(latest.CreatedAt) || (item.CreatedAt.Equal(latest.CreatedAt) && item.ID > latest.ID) {
			copy := item
			latest = &copy
		}
	}
	if latest != nil {
		approvals, listErr := r.plane.Approvals.ListByRun(ctx, latest.ID)
		if listErr != nil {
			return TaskSummary{}, controlapi.MapError(listErr)
		}
		runSummary := makeRunSummary(*latest, value.Goal, openApprovalCount(approvals))
		result.LatestRun = &runSummary
		result.WorkspaceID = latest.WorkspaceID
	}
	if workspaceValue, getErr := r.plane.Workspaces.GetByFeatureArea(ctx, value.FeatureAreaID); getErr == nil {
		result.WorkspaceID = workspaceValue.ID
	} else if !errors.Is(getErr, workspace.ErrNotFound) {
		return TaskSummary{}, controlapi.MapError(getErr)
	}
	return result, nil
}

func (r *Reader) workspaceForTask(ctx context.Context, taskID string) (*WorkspaceSummary, error) {
	owner, err := r.plane.Tasks.Get(ctx, taskID)
	if err != nil {
		return nil, controlapi.MapError(err)
	}
	value, err := r.plane.Workspaces.GetByFeatureArea(ctx, owner.FeatureAreaID)
	if errors.Is(err, workspace.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, controlapi.MapError(err)
	}
	return r.workspaceSummary(ctx, value)
}

func (r *Reader) workspaceSummary(ctx context.Context, value workspace.Workspace) (*WorkspaceSummary, error) {
	result := &WorkspaceSummary{ID: value.ID, FeatureAreaID: value.FeatureAreaID, ProjectID: value.ProjectID, RepositoryID: value.RepositoryID, BaseCommit: value.BaseCommit, WorktreeID: value.WorktreeID, State: string(value.Status), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
	if value.WorktreeID != "" {
		worktreeValue, getErr := r.plane.Worktrees.Get(ctx, value.WorktreeID)
		if getErr == nil {
			result.WorktreePath, result.WorktreeBranch, result.WorktreeStatus = worktreeValue.Path, worktreeValue.Branch, string(worktreeValue.Status)
		} else if !errors.Is(getErr, worktree.ErrNotFound) {
			return nil, controlapi.MapError(getErr)
		}
	}
	return result, nil
}

func (r *Reader) readEvidenceArtifacts(ctx context.Context, runID string, target *EvidenceDetails) error {
	values, err := r.plane.Artifacts.ListByRun(ctx, runID)
	if err != nil {
		return controlapi.MapError(err)
	}
	if len(values) > MaxArtifactCount {
		values = values[:MaxArtifactCount]
	}
	for _, value := range values {
		if value.Kind != artifact.KindGitDiff && value.Kind != artifact.KindAgentMessage {
			continue
		}
		chunk, readErr := r.control.ReadArtifact(ctx, value.ID, 0, MaxArtifactBytes)
		if readErr != nil {
			return readErr
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(chunk.Data)
		if decodeErr != nil {
			return &controlapi.Error{Code: controlapi.CodeInternal, Message: "artifact data could not be decoded"}
		}
		switch value.Kind {
		case artifact.KindGitDiff:
			target.Diff, target.DiffTruncated = string(decoded), chunk.More
		case artifact.KindAgentMessage:
			target.AgentClaim, target.AgentClaimTruncated = string(decoded), chunk.More
		}
	}
	return nil
}

func makeContextSummary(value projectcontext.ProjectContext) ContextSummary {
	summary, cut := summarize(value.Result, MaxSummaryBytes)
	return ContextSummary{Available: true, ProjectID: value.ProjectID, Status: string(value.Status), LatestProbeID: value.LatestProbeID, ResultSummary: summary, ResultSummaryCut: cut, RootGuidanceIdentity: summarizeOnly(value.RootGuidanceIdentity, MaxSummaryBytes), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func productProjectContextState(value projectcontext.ProjectContext) (state string, refreshing, refreshFailed bool) {
	state = string(value.Status)
	if !value.SnapshotUsable() {
		return state, false, false
	}
	refreshing, refreshFailed = value.RefreshInProgress(), value.RefreshFailed()
	if value.Status == projectcontext.StatusRunning || value.Status == projectcontext.StatusFailed {
		state = string(projectcontext.StatusReady)
	}
	return state, refreshing, refreshFailed
}

func makeProbeSummary(value contextprobe.Probe) ContextProbeSummary {
	result, cut := summarize(value.Result, MaxSummaryBytes)
	selected := []string{}
	ownerKind := ""
	if value.Canonical != nil {
		ownerKind = "PROJECT_INSPECTION"
		for _, target := range value.Canonical.Targets {
			selected = append(selected, target.RepositoryID)
		}
	} else {
		selected = append(selected, value.RepositoryID)
		if value.RuntimeLimitation == "" {
			value.RuntimeLimitation = "LEGACY_INSPECTION_NO_CANONICAL_RUNTIME"
		}
	}
	return ContextProbeSummary{
		ID: value.ID, OwnerKind: ownerKind, TaskID: value.TaskID, RunID: value.RunID, ProjectID: value.ProjectID, AISessionID: value.AISessionID, RepositoryID: value.RepositoryID, SelectedRepositoryIDs: selected, PrimaryMemberID: value.PrimaryMemberID, WorkspaceID: value.WorkspaceID, RuntimeWorksetSHA256: value.RuntimeWorksetSHA256, ReportSHA256: value.ReportSHA256, ResultSHA256: value.ResultSHA256, RuntimeLimitation: value.RuntimeLimitation, CodexConfigID: value.CodexConfigID,
		ScopeKind: string(value.Scope.Kind), ScopePaths: append([]string(nil), value.Scope.Paths...), Status: string(value.Status), ThreadID: value.ThreadID, TurnID: value.TurnID,
		StartedAt: value.StartedAt, FinishedAt: value.FinishedAt, DurationMilliseconds: duration(value.StartedAt, value.FinishedAt), TokenUsage: tokenUsage(value.TokenUsage),
		ResultSummary: result, ResultSummaryCut: cut, FailureSummary: summarizeOnly(value.FailureMessage, MaxSummaryBytes), PromptAvailable: value.FinalCodexPrompt != "", PromptSHA256: value.PromptSHA256, PromptBytes: value.PromptBytes, PromptSentAt: cloneTime(value.PromptSentAt), CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func makeRunSummary(value run.Run, goal string, pendingApprovals int) RunSummary {
	result := RunSummary{ResultProtocol: value.ResultProtocol, AnalysisState: value.AnalysisState,
		AgentRuntime: boundedRunRuntime(value), AgentExecutionID: value.AgentExecutionID,
		ID: value.ID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, OperationKind: string(value.OperationKind), OperationID: value.OperationID, ReviewID: value.ReviewID, TaskID: value.TaskID, TaskGoal: summarizeOnly(goal, 2048), Purpose: string(value.Purpose), ExecutionProposalID: value.ExecutionProposalID, Attempt: value.Attempt,
		Status: string(value.Status), WorkspaceID: value.WorkspaceID, WorktreeID: value.WorktreeID, ThreadID: value.ThreadID, TurnID: value.TurnID,
		CreatedAt: value.CreatedAt, StartedAt: value.StartedAt, FinishedAt: value.FinishedAt, DurationMilliseconds: duration(value.StartedAt, value.FinishedAt),
		TokenUsage: tokenUsage(value.TokenUsage), PendingApprovalCount: pendingApprovals,
		PromptAvailable: value.FinalCodexPrompt != "", PromptSHA256: value.PromptSHA256, PromptBytes: value.PromptBytes, PromptSentAt: cloneTime(value.PromptSentAt),
	}
	if value.Failure != nil {
		result.FailureSummary = summarizeOnly(value.Failure.Message, MaxSummaryBytes)
	}
	if value.VerificationReport != nil {
		result.VerificationReport, result.VerificationCheckCount = "REPORT_"+string(value.VerificationReport.Availability), len(value.VerificationReport.Checks)
	}
	if result.AgentRuntime != nil {
		for _, request := range result.AgentRuntime.Requests {
			if agentruntime.OpenRequest(request) {
				result.PendingRequestCount++
			}
		}
	}
	return result
}

func makeApprovalSummary(value approval.Approval, runValue run.Run, goal string) ApprovalSummary {
	payload, truncated := safePayload(value.RequestPayload, MaxPayloadBytes)
	decision := value.Decision
	if decision != nil {
		copy := *decision
		copy.Permissions = nil // requested permission details remain in the redacted payload
		decision = &copy
	}
	return ApprovalSummary{
		ID: value.ID, TaskID: runValue.TaskID, TaskGoal: summarizeOnly(goal, 2048), RunID: value.RunID, RunPurpose: string(runValue.Purpose), RunStatus: string(runValue.Status),
		ThreadID: value.ThreadID, TurnID: value.TurnID, ItemID: value.ItemID, Kind: string(value.Kind), Status: string(value.Status),
		AllowedDecisions: append([]approval.DecisionType(nil), value.AllowedDecisions...), RequestPayload: payload, RequestPayloadTruncated: truncated,
		Decision: decision, CreatedAt: value.CreatedAt, DecidedAt: value.DecidedAt, DeliveredAt: value.DeliveredAt,
	}
}

func makeEventSummary(value event.Event) EventSummary {
	var payload struct {
		From       string            `json:"from"`
		To         string            `json:"to"`
		ReasonCode string            `json:"reason_code"`
		Decision   approval.Decision `json:"decision"`
	}
	_ = json.Unmarshal(value.Payload, &payload)
	return EventSummary{Sequence: value.Sequence, Type: value.Type, From: payload.From, To: payload.To, ReasonCode: payload.ReasonCode, DecisionType: string(payload.Decision.Type), CreatedAt: value.CreatedAt}
}

func boundedVerificationReport(value *run.VerificationReport) *run.VerificationReport {
	if value == nil {
		return nil
	}
	result := &run.VerificationReport{Version: value.Version, Availability: value.Availability, Summary: summarizeOnly(value.Summary, 2048), LegacyResultStatus: value.LegacyResultStatus, Checks: append([]run.VerificationCheckOutcome(nil), value.Checks...), AcceptanceCriteria: append([]run.VerificationCriterionOutcome(nil), value.AcceptanceCriteria...)}
	if len(result.Checks) > 100 {
		result.Checks = result.Checks[:100]
	}
	if len(result.AcceptanceCriteria) > 100 {
		result.AcceptanceCriteria = result.AcceptanceCriteria[:100]
	}
	for i := range result.AcceptanceCriteria {
		result.AcceptanceCriteria[i].Criterion = summarizeOnly(result.AcceptanceCriteria[i].Criterion, 8<<10)
		result.AcceptanceCriteria[i].Reported = boundedVerificationReported(result.AcceptanceCriteria[i].Reported)
	}
	for index := range result.Checks {
		result.Checks[index].Reported = boundedVerificationReported(result.Checks[index].Reported)
		if fact := result.Checks[index].Observed; fact != nil {
			copy := *fact
			copy.Command = summarizeOnly(copy.Command, 8<<10)
			var cut bool
			copy.Evidence, cut = summarize(copy.Evidence, 32<<10)
			copy.EvidenceTruncated = copy.EvidenceTruncated || cut
			copy.Reason = summarizeOnly(copy.Reason, MaxSummaryBytes)
			copy.Summary = summarizeOnly(copy.Summary, MaxSummaryBytes)
			result.Checks[index].Observed = &copy
		}
		result.Checks[index].DeclaredCommand = summarizeOnly(result.Checks[index].DeclaredCommand, 8<<10)
		result.Checks[index].ObservedCommand = summarizeOnly(result.Checks[index].ObservedCommand, 8<<10)
		result.Checks[index].Evidence = summarizeOnly(result.Checks[index].Evidence, 32<<10)
		result.Checks[index].FailureReason = summarizeOnly(result.Checks[index].FailureReason, MaxSummaryBytes)
		result.Checks[index].Summary = summarizeOnly(result.Checks[index].Summary, MaxSummaryBytes)
	}
	return result
}

func tokenUsage(value run.TokenUsageAccounting) TokenUsage {
	result := TokenUsage{Status: string(value.Status), ModelContextWindow: value.ModelContextWindow}
	if value.Status == run.TokenUsageAvailable && value.Usage != nil {
		counts := tokenCounts(*value.Usage)
		result.Counts = &counts
	}
	return result
}

func tokenCounts(value run.TokenCounts) TokenCounts {
	return TokenCounts{InputTokens: value.InputTokens, CachedInputTokens: value.CachedInputTokens, CacheWriteInputTokens: value.CacheWriteInputTokens, OutputTokens: value.OutputTokens, ReasoningOutputTokens: value.ReasoningOutputTokens, TotalTokens: value.TotalTokens}
}

func accountingForRuns(values []run.Run, disposition string) Accounting {
	if disposition != "REQUESTED" {
		return Accounting{Disposition: disposition, Duration: AccountingValue{Status: disposition}, TokenUsage: AccountingValue{Status: disposition}}
	}
	if len(values) == 0 {
		return Accounting{Disposition: disposition, Duration: AccountingValue{Status: "PENDING"}, TokenUsage: AccountingValue{Status: "PENDING"}}
	}
	durationStatus, tokenStatus := "AVAILABLE", "AVAILABLE"
	var totalDuration int64
	totalTokens := TokenCounts{}
	for _, value := range values {
		if !terminalStatus(value.Status) {
			durationStatus, tokenStatus = "PENDING", "PENDING"
			continue
		}
		elapsed := duration(value.StartedAt, value.FinishedAt)
		if elapsed == nil && durationStatus != "PENDING" {
			durationStatus = "UNAVAILABLE"
		} else if elapsed != nil {
			totalDuration += *elapsed
		}
		if tokenStatus == "PENDING" {
			continue
		}
		if value.TokenUsage.Status != run.TokenUsageAvailable || value.TokenUsage.Usage == nil {
			tokenStatus = "UNAVAILABLE"
			continue
		}
		addTokens(&totalTokens, tokenCounts(*value.TokenUsage.Usage))
	}
	result := Accounting{Disposition: disposition, Duration: AccountingValue{Status: durationStatus}, TokenUsage: AccountingValue{Status: tokenStatus}}
	if durationStatus == "AVAILABLE" {
		result.Duration.Milliseconds = &totalDuration
	}
	if tokenStatus == "AVAILABLE" {
		result.TokenUsage.Tokens = &totalTokens
	}
	return result
}

func accountingForProbes(values []contextprobe.Probe) Accounting {
	if len(values) == 0 {
		return Accounting{Disposition: "NOT_REQUESTED", Duration: AccountingValue{Status: "NOT_REQUESTED"}, TokenUsage: AccountingValue{Status: "NOT_REQUESTED"}}
	}
	durationStatus, tokenStatus := "AVAILABLE", "AVAILABLE"
	var totalDuration int64
	totalTokens := TokenCounts{}
	for _, value := range values {
		if value.Status == contextprobe.StatusCreated || value.Status == contextprobe.StatusRunning {
			durationStatus, tokenStatus = "PENDING", "PENDING"
			continue
		}
		elapsed := duration(value.StartedAt, value.FinishedAt)
		if elapsed == nil && durationStatus != "PENDING" {
			durationStatus = "UNAVAILABLE"
		} else if elapsed != nil {
			totalDuration += *elapsed
		}
		if tokenStatus == "PENDING" {
			continue
		}
		if value.TokenUsage.Status != run.TokenUsageAvailable || value.TokenUsage.Usage == nil {
			tokenStatus = "UNAVAILABLE"
			continue
		}
		addTokens(&totalTokens, tokenCounts(*value.TokenUsage.Usage))
	}
	result := Accounting{Disposition: "REQUESTED", Duration: AccountingValue{Status: durationStatus}, TokenUsage: AccountingValue{Status: tokenStatus}}
	if durationStatus == "AVAILABLE" {
		result.Duration.Milliseconds = &totalDuration
	}
	if tokenStatus == "AVAILABLE" {
		result.TokenUsage.Tokens = &totalTokens
	}
	return result
}

func addTokens(target *TokenCounts, value TokenCounts) {
	target.InputTokens += value.InputTokens
	target.CachedInputTokens += value.CachedInputTokens
	target.CacheWriteInputTokens += value.CacheWriteInputTokens
	target.OutputTokens += value.OutputTokens
	target.ReasoningOutputTokens += value.ReasoningOutputTokens
	target.TotalTokens += value.TotalTokens
}

func filterRuns(values []run.Run, purpose run.Purpose) []run.Run {
	result := make([]run.Run, 0)
	for _, value := range values {
		if value.Purpose == purpose {
			result = append(result, value)
		}
	}
	return result
}

func filterRunsByLink(values []run.Run, featureAreaReview bool) []run.Run {
	result := make([]run.Run, 0)
	for _, value := range values {
		if value.Purpose != run.PurposeScopeReview {
			continue
		}
		if featureAreaReview && value.ReviewID != "" {
			result = append(result, value)
		}
	}
	return result
}

func verificationDisposition(value task.VerificationSpec) string {
	if value.Mode == task.VerificationModeNone && value.Waiver != nil && value.Waiver.Source == task.VerificationWaiverUser {
		return "USER_WAIVED"
	}
	if value.Mode == task.VerificationModeNone {
		return "NOT_REQUESTED"
	}
	return "REQUESTED"
}

func duration(started, finished *time.Time) *int64 {
	if started == nil || finished == nil || finished.Before(*started) {
		return nil
	}
	value := finished.Sub(*started).Milliseconds()
	return &value
}

func terminalStatus(value run.Status) bool {
	return value == run.StatusSucceeded || value == run.StatusFailed || value == run.StatusInfrastructureFailed || value == run.StatusCanceled
}

func activeStatus(value run.Status) bool {
	return value == run.StatusPreparing || value == run.StatusRunning || value == run.StatusWaitingApproval || value == run.StatusCollecting
}

func knownRunStatus(value run.Status) bool {
	switch value {
	case run.StatusCreated, run.StatusPreparing, run.StatusRunning, run.StatusWaitingApproval, run.StatusCollecting, run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
		return true
	default:
		return false
	}
}

func openApproval(value approval.Status) bool {
	return value == approval.StatusPending || value == approval.StatusDecided
}

func openApprovalCount(values []approval.Approval) int {
	count := 0
	for _, value := range values {
		if openApproval(value.Status) {
			count++
		}
	}
	return count
}

func safePayload(raw json.RawMessage, limit int) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "[unavailable: invalid approval payload]", false
	}
	redactSecrets(value)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "[unavailable: approval payload could not be encoded]", false
	}
	if len(encoded) <= limit {
		return string(encoded), false
	}
	return string(encoded[:limit]) + "\n… [truncated]", true
}

func redactSecrets(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if secretKey(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			redactSecrets(nested)
		}
	case []any:
		for _, nested := range typed {
			redactSecrets(nested)
		}
	}
}

func secretKey(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_"))
	for _, fragment := range []string{"secret", "password", "passwd", "api_key", "apikey", "access_token", "refresh_token", "authorization", "cookie", "credential"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

func summarize(value string, limit int) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value, false
	}
	return value[:limit] + "…", true
}

func summarizeOnly(value string, limit int) string {
	result, _ := summarize(value, limit)
	return result
}

func cloneReviewScope(value run.ReviewScope) run.ReviewScope {
	value.Paths = append([]string(nil), value.Paths...)
	return value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func boundedPage(value PageRequest) (int, int, error) {
	limit := value.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimit {
		return 0, 0, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "limit must be between 1 and 100"}
	}
	if value.Offset < 0 || value.Offset > 1_000_000 {
		return 0, 0, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "offset must be between 0 and 1000000"}
	}
	return limit, value.Offset, nil
}

func paginate[T any](values []T, limit, offset int) Page[T] {
	result := Page[T]{Items: []T{}, Limit: limit, Offset: offset}
	if offset >= len(values) {
		return result
	}
	end := offset + limit
	if end < len(values) {
		result.HasMore = true
	} else {
		end = len(values)
	}
	result.Items = values[offset:end]
	return result
}

func (r *Reader) projectPage(ctx context.Context, projectID string, request PageRequest) (int, int, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return 0, 0, err
	}
	return boundedPage(request)
}

func (r *Reader) requireProject(ctx context.Context, projectID string) error {
	if _, err := localoperator.BindProject(ctx, projectID); err != nil {
		return controlapi.MapError(err)
	}
	if err := validID("project_id", projectID); err != nil {
		return err
	}
	if _, err := r.plane.Projects.Get(ctx, projectID); err != nil {
		return controlapi.MapError(err)
	}
	return nil
}

func validID(name, value string) error {
	if value == "" {
		return &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: name + " is required"}
	}
	if len(value) > maxIdentifierSize {
		return &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: name + " exceeds 128 bytes"}
	}
	return nil
}

func notFound() error {
	return &controlapi.Error{Code: controlapi.CodeNotFound, Message: "requested control-plane entity was not found"}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *controlapi.Error
	if errors.As(controlapi.MapError(err), &apiErr) {
		return apiErr.Code == controlapi.CodeNotFound
	}
	return false
}

// ownerForRun reads immutable operation provenance. Task fallback is solely for
// historical Runs whose direct Project owner was not determinable at migration.
type runReadOwner struct {
	RepositoryID, CodexConfigID, ModuleID, Goal, Model, ReasoningEffort, ConfigSource, RevisionID, ExecutorKind string
	ReviewAuthorizationRequired                                                                                 bool
}

func (r *Reader) ownerForRun(ctx context.Context, projectID string, value run.Run) (runReadOwner, error) {
	if value.ProjectID != "" && value.ProjectID != projectID {
		return runReadOwner{}, notFound()
	}
	if value.TaskID != "" && value.WorkflowOwned() {
		if err := r.requireTaskRun(ctx, projectID, value.TaskID, value.ID); err != nil {
			return runReadOwner{}, err
		}
	}
	if value.ExecutionProposalID != "" {
		e, err := r.plane.ExecutionProposals.GetForProject(ctx, projectID, value.ExecutionProposalID)
		if err != nil {
			return runReadOwner{}, notFound()
		}
		t, err := r.plane.Tasks.Get(ctx, e.TaskID)
		if err != nil || t.ProjectID != projectID {
			return runReadOwner{}, notFound()
		}
		source := "UNKNOWN"
		if e.RuntimeConfig.Model != "" || e.RuntimeConfig.ReasoningEffort != "" {
			source = "EXECUTION_RUNTIME_CONFIG"
		}
		return runReadOwner{RepositoryID: t.RepositoryID, CodexConfigID: t.CodexConfigID, ModuleID: t.ModuleID, Goal: t.Goal, Model: e.RuntimeConfig.Model, ReasoningEffort: e.RuntimeConfig.ReasoningEffort, ConfigSource: source, RevisionID: e.TaskDefinitionRevisionID}, nil
	}
	if value.ReviewID != "" {
		review, err := r.plane.Reviews.Get(ctx, value.ReviewID)
		if err != nil || review.ProjectID != projectID || review.RunID != value.ID || review.TaskID != value.TaskID || review.FeatureAreaID != value.FeatureAreaID {
			return runReadOwner{}, notFound()
		}
		source := "UNKNOWN"
		if review.Model != "" || review.ReasoningEffort != "" {
			source = "REVIEW"
		}
		return runReadOwner{RepositoryID: review.RepositoryID, CodexConfigID: review.CodexConfigID, Goal: review.EngineeringGoal, Model: review.Model, ReasoningEffort: review.ReasoningEffort, ConfigSource: source, ExecutorKind: string(review.ExecutorKind.Effective()), ReviewAuthorizationRequired: review.ExecutorKind == productreview.ExecutorUpperLLM && !terminalStatus(value.Status) && review.ReportID == "" && review.UpperAccess != nil && (review.UpperAccess.State == "RECOVERY_REQUIRED" || review.UpperAccess.Target.RuntimeEpoch != r.plane.DB.RuntimeEpoch() || value.RuntimeEpoch != r.plane.DB.RuntimeEpoch())}, nil
	}
	if value.TaskID != "" {
		task, err := r.plane.Tasks.Get(ctx, value.TaskID)
		if err != nil || task.ProjectID != projectID {
			return runReadOwner{}, notFound()
		}
		return runReadOwner{RepositoryID: task.RepositoryID, CodexConfigID: task.CodexConfigID, ModuleID: task.ModuleID, Goal: task.Goal}, nil
	}
	if value.ProjectID == projectID {
		return runReadOwner{}, nil
	}
	return runReadOwner{}, notFound()
}
func (r *Reader) workspaceForRun(ctx context.Context, value run.Run) (*WorkspaceSummary, error) {
	if value.WorkspaceID == "" {
		return nil, nil
	}
	environment, err := r.plane.Workspaces.Get(ctx, value.WorkspaceID)
	if err != nil {
		return nil, controlapi.MapError(err)
	}
	return r.workspaceSummary(ctx, environment)
}

func boundedVerificationReported(v run.VerificationReportedFact) run.VerificationReportedFact {
	v.Summary = summarizeOnly(v.Summary, MaxSummaryBytes)
	v.Evidence = summarizeOnly(v.Evidence, 32<<10)
	v.Reason = summarizeOnly(v.Reason, MaxSummaryBytes)
	return v
}
