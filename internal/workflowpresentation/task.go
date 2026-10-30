package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/featurearea"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	"github.com/lazyun/devbridge/internal/taskgoal"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// TaskView is a read-only product view. Status and actions come from the
// existing owners; no renderer aggregates accounting or advances work.
type TaskView struct {
	ActionNode                 *taskaction.Node       `json:"actionNode,omitempty"`
	VerificationDecisions      *VerificationDecisions `json:"verificationDecisions,omitempty"`
	currentImplementationExact bool
	currentVerificationExact   bool
	NextStep                   *TaskNextStep                   `json:"nextStep"`
	RunRecovery                *TaskRunRecovery                `json:"runRecovery,omitempty"`
	CodexContinuation          *CodexContinuation              `json:"codexContinuation,omitempty"`
	BusinessPhase              task.Phase                      `json:"businessPhase,omitempty"`
	BusinessPhaseSourceEventID string                          `json:"businessPhaseSourceEventId,omitempty"`
	ExecutionStartTarget       *wp.TaskExecutionExpectation    `json:"executionStartTarget,omitempty"`
	CurrentReviewRequest       *wp.TaskReviewRequestResolution `json:"currentReviewRequest,omitempty"`
	ImplementationMode         string                          `json:"implementationMode"`
	DirectFile                 *directfile.State               `json:"directFile,omitempty"`
	OriginalRequirement        task.OriginalRequirement        `json:"originalRequirement"`
	RequiredReviewDepth        *task.ReviewDepth               `json:"requiredReviewDepth"`
	AllowedReviewDepths        []task.ReviewDepth              `json:"allowedReviewDepths"`
	// Candidates become capabilities only after exact Core guidance requests disposition.
	RuntimeDispositionActions []string               `json:"-"`
	CurrentWorkKind           string                 `json:"currentWorkKind"`
	CurrentWorkSpec           *task.WorkSpec         `json:"currentWorkSpec,omitempty"`
	CurrentVerificationSpec   *task.VerificationSpec `json:"currentVerificationSpec,omitempty"`
	Phase                     task.Phase             `json:"phase"`
	PhaseLabel                string                 `json:"phaseLabel"`
	PhaseDescription          string                 `json:"phaseDescription"`
	CurrentReview             *ReviewView            `json:"currentReview,omitempty"`
	ReviewHistory             []ReviewView           `json:"reviewHistory"`
	CurrentAction             wp.Action              `json:"currentAction"`
	Recovery                  *Recovery              `json:"recovery,omitempty"`
	CommitStatus              string                 `json:"commitStatus,omitempty"`
	CommitSHA                 string                 `json:"commitSha,omitempty"`
	DevIntegrationStatus      string                 `json:"devIntegrationStatus,omitempty"`
	MergedCommit              string                 `json:"mergedCommit,omitempty"`
	CommitActivity            string                 `json:"commitActivity,omitempty"`
	CommitErrorSummary        string                 `json:"commitErrorSummary,omitempty"`
	CommitSummary             string                 `json:"commitSummary,omitempty"`
	DevSummary                string                 `json:"devSummary,omitempty"`

	ID                         string                   `json:"id"`
	Title                      string                   `json:"title"`
	Goal                       string                   `json:"goal"`
	Kind                       string                   `json:"kind"`
	Status                     string                   `json:"status"`
	Terminal                   bool                     `json:"terminal"`
	ReadOnly                   bool                     `json:"readOnly"`
	Progress                   []PhaseProgress          `json:"progress"`
	Activity                   TaskActivity             `json:"activity"`
	Goals                      []GoalProgress           `json:"goals"`
	Usage                      TaskUsage                `json:"usage"`
	Result                     *TaskResult              `json:"result,omitempty"`
	Verification               *TaskResult              `json:"verification,omitempty"`
	OverallState               string                   `json:"overallState"`
	CurrentExecution           *ExecutionView           `json:"currentExecution,omitempty"`
	ImplementationRun          *VerificationRunView     `json:"implementationRun,omitempty"`
	VerificationRun            *VerificationRunView     `json:"verificationRun,omitempty"`
	VerificationReport         *run.VerificationReport  `json:"verificationReport,omitempty"`
	CurrentExecutionAcceptance *ExecutionAcceptanceView `json:"currentExecutionAcceptance,omitempty"`
	ExecutionHistory           []ExecutionHistoryView   `json:"executionHistory"`
	// ActionsStatus distinguishes an authoritative empty set from unavailable facts.
	ActionsStatus    string          `json:"actionsStatus"`
	ActionsReadError string          `json:"actionsReadError,omitempty"`
	Actions          []Capability    `json:"actions"`
	Details          TaskDiagnostics `json:"details"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}
type ReviewView struct {
	Activity          *ReviewActivity          `json:"activity,omitempty"`
	ExecutorKind      review.ExecutorKind      `json:"executorKind"`
	ExecutorMetadata  *review.ExecutorMetadata `json:"executorMetadata,omitempty"`
	UpperAccess       *review.UpperAccess      `json:"upperAccess,omitempty"`
	ReportID          string                   `json:"reportId,omitempty"`
	Stage             string                   `json:"stage,omitempty"`
	StageLabel        string                   `json:"stageLabel,omitempty"`
	NextActor         string                   `json:"nextActor,omitempty"`
	Baseline          string                   `json:"baseline,omitempty"`
	ReviewDepth       task.ReviewDepth         `json:"reviewDepth"`
	ReviewDisposition review.Disposition       `json:"reviewDisposition,omitempty"`
	ID                string                   `json:"id"`
	TaskID            string                   `json:"taskId"`
	Ordinal           int                      `json:"ordinal"`
	Status            string                   `json:"status"`
	Run               *VerificationRunView     `json:"run,omitempty"`
	ResultAvailable   bool                     `json:"resultAvailable"`
	Instruction       string                   `json:"instruction,omitempty"`
	FailureSummary    string                   `json:"failureSummary,omitempty"`
	CreatedAt         time.Time                `json:"createdAt"`
	UpdatedAt         time.Time                `json:"updatedAt"`
}

type ExecutionView struct {
	Summary                  string                            `json:"summary,omitempty"`
	ProvenanceKind           string                            `json:"provenanceKind"`
	UpdatedAt                time.Time                         `json:"updatedAt"`
	ID                       string                            `json:"id"`
	Outcome                  string                            `json:"outcome"`
	State                    string                            `json:"state"`
	FailureCode              string                            `json:"failureCode,omitempty"`
	TaskDefinitionRevisionID string                            `json:"taskDefinitionRevisionId"`
	ImplementationRunID      string                            `json:"implementationRunId,omitempty"`
	VerificationRunID        string                            `json:"verificationRunId,omitempty"`
	RuntimeConfig            codexruntime.NextRunRuntimeConfig `json:"runtimeConfig"`
	BackendSelection         *codexconfig.BackendSelection     `json:"backendSelection,omitempty"`
}
type VerificationRunView struct {
	NativeResultState string `json:"nativeResultState,omitempty"`
	AnalysisState     string `json:"analysisState,omitempty"`
	ID                string `json:"id"`
	TransportStatus   string `json:"transportStatus"`
	FailureCode       string `json:"failureCode,omitempty"`
}
type ExecutionAcceptanceView struct {
	DecisionID     string                      `json:"decisionId"`
	Status         string                      `json:"status"`
	AllowedActions []string                    `json:"allowedActions"`
	Receipt        *decisionresolution.Receipt `json:"receipt,omitempty"`
}
type ExecutionHistoryView struct {
	SupersededByReviewRequestID string                      `json:"supersededByReviewRequestId,omitempty"`
	PredecessorExecutionID      string                      `json:"predecessorExecutionId,omitempty"`
	SourceDecisionID            string                      `json:"sourceDecisionId,omitempty"`
	SourceAction                string                      `json:"sourceAction,omitempty"`
	Current                     bool                        `json:"current"`
	Ordinal                     int                         `json:"ordinal"`
	ImplementationRun           *VerificationRunView        `json:"implementationRun,omitempty"`
	VerificationRun             *VerificationRunView        `json:"verificationRun,omitempty"`
	Execution                   ExecutionView               `json:"execution"`
	Acceptance                  *decisionresolution.Receipt `json:"acceptance,omitempty"`
}
type GoalProgress struct {
	Title  string `json:"title"`
	Status string `json:"status"`
}
type TaskActivity struct {
	RecoveryStatus *recoverystatus.Projection `json:"recoveryStatus,omitempty"`
	// Operation identity is an observation only. It lets every surface show
	// the activity's exact owner without deriving activity from a decision or
	// from a later Task pointer.
	OperationKind string                 `json:"operationKind,omitempty"`
	OperationID   string                 `json:"operationId,omitempty"`
	RunID         string                 `json:"runId,omitempty"`
	FailureCode   string                 `json:"failureCode,omitempty"`
	Status        string                 `json:"status"`
	Summary       string                 `json:"summary"`
	ElapsedMS     *int64                 `json:"elapsedMs,omitempty"`
	NativePlan    *agentruntime.Plan     `json:"nativePlan,omitempty"`
	Runtime       *agentruntime.Snapshot `json:"runtime,omitempty"`
}
type TaskResult struct {
	Status  string `json:"status"`
	Summary string `json:"summary"`
}
type TaskTokens struct {
	Input           int64 `json:"input"`
	CachedInput     int64 `json:"cachedInput"`
	NonCachedInput  int64 `json:"nonCachedInput"`
	Output          int64 `json:"output"`
	ReasoningOutput int64 `json:"reasoningOutput"`
	Total           int64 `json:"total"`
}
type TaskUsage struct {
	Status           run.TokenUsageStatus `json:"status"`
	Tokens           *TaskTokens          `json:"tokens,omitempty"`
	DurationMS       int64                `json:"durationMs"`
	DurationComplete bool                 `json:"durationComplete"`
	ImplementationMS int64                `json:"implementationMs"`
	VerificationMS   int64                `json:"verificationMs"`
	OtherMS          int64                `json:"otherMs"`
	RunCount         int                  `json:"runCount"`
	UnavailableRuns  []string             `json:"unavailableRuns"`
	PendingRuns      []string             `json:"pendingRuns"`
}
type TaskDiagnostics struct {
	DefinitionRevisionsStatus string                      `json:"definitionRevisionsStatus"`
	CommitReceipt             *featurearea.TaskCommitFact `json:"commitReceipt,omitempty"`
	CommitSHA                 string                      `json:"commitSha,omitempty"`
	CommittedAt               *time.Time                  `json:"committedAt,omitempty"`
	MergedCommit              string                      `json:"mergedCommit,omitempty"`
	MergedAt                  *time.Time                  `json:"mergedAt,omitempty"`

	TaskDefinitionRevisionID string                          `json:"taskDefinitionRevisionId"`
	Model                    string                          `json:"model"`
	ReasoningEffort          string                          `json:"reasoningEffort"`
	Runs                     []TaskRunDetail                 `json:"runs"`
	RunsTotal                int                             `json:"runsTotal"`
	RunsTruncated            bool                            `json:"runsTruncated"`
	DefinitionRevisions      []TaskDefinitionRevisionSummary `json:"definitionRevisions"`
}
type TaskDefinitionRevisionSummary struct {
	InstructionSource *task.InstructionSource `json:"instructionSource,omitempty"`
	ID                string                  `json:"id"`
	Ordinal           int                     `json:"ordinal"`
	Current           bool                    `json:"current"`
	PayloadSHA256     string                  `json:"payloadSha256"`
	CreatedAt         time.Time               `json:"createdAt"`
}

const DefaultTaskRunLimit = 20

type TaskRunDetail struct {
	// ExecutionID retains its published AgentExecution identity; new callers use AgentExecutionID.
	AgentExecutionID         string               `json:"agentExecutionId"`
	TaskExecutionID          string               `json:"taskExecutionId"`
	ReviewID                 string               `json:"reviewId"`
	OperationKind            string               `json:"operationKind"`
	OperationID              string               `json:"operationId"`
	RunPurpose               string               `json:"runPurpose"`
	Model                    string               `json:"model"`
	ReasoningEffort          string               `json:"reasoningEffort"`
	ConfigSource             string               `json:"configSource"`
	TaskDefinitionRevisionID string               `json:"taskDefinitionRevisionId"`
	CreatedAt                time.Time            `json:"createdAt"`
	StartedAt                *time.Time           `json:"startedAt"`
	FinishedAt               *time.Time           `json:"finishedAt"`
	ElapsedMS                *int64               `json:"elapsedMs"`
	DurationComplete         bool                 `json:"durationComplete"`
	ID                       string               `json:"id"`
	ExecutionID              string               `json:"executionId"`
	WorkspaceID              string               `json:"workspaceId"`
	ThreadID                 string               `json:"threadId"`
	TurnID                   string               `json:"turnId"`
	Purpose                  string               `json:"purpose"`
	TransportStatus          string               `json:"transportStatus"`
	FailureCode              string               `json:"failureCode,omitempty"`
	TokenStatus              run.TokenUsageStatus `json:"tokenStatus"`
	Result                   string               `json:"result"`
	Commit                   string               `json:"commit"`
}

// BuildTask consumes all Runs belonging to this exact Task across Executions.
// Linked Review Tasks are separate intentions and are never billed twice.
// Work.ReadOnly constrains the operation, not its owner's workflow decisions.
func BuildTask(t task.Task, values []run.Run, goals []taskgoal.Progress, now time.Time) TaskView {
	v := TaskView{ImplementationMode: "UNKNOWN", ReviewHistory: []ReviewView{}, Phase: t.Phase, ID: t.ID, Title: t.Title, Goal: t.WorkSpec.Goal, Kind: string(t.Type), Status: string(t.Status), OverallState: string(t.Status), ReadOnly: t.LegacyReadOnly, Progress: []PhaseProgress{}, Goals: []GoalProgress{}, Actions: []Capability{}, ActionsStatus: "UNAVAILABLE", ActionsReadError: "Task action facts have not been assembled.", ExecutionHistory: []ExecutionHistoryView{}, UpdatedAt: t.UpdatedAt}
	v.OriginalRequirement = t.OriginalRequirement
	v.OriginalRequirement.Constraints = append([]string(nil), t.OriginalRequirement.Constraints...)
	v.AllowedReviewDepths = []task.ReviewDepth{}
	if t.Status == task.StatusActive && (t.Phase == task.PhaseReviewRequired || t.Phase == task.PhaseReviewing) && t.RequiredReviewDepth != nil {
		depth := *t.RequiredReviewDepth
		v.RequiredReviewDepth = &depth
		v.AllowedReviewDepths = task.AllowedReviewDepths(&depth)
	}
	v.CurrentWorkKind = "TASK"
	if t.Phase.ReviewStage() {
		v.CurrentWorkKind = "REVIEW"
	}
	if t.Status == task.StatusSucceeded || t.Status == task.StatusCanceled || t.Status == task.StatusFailed {
		v.CurrentWorkKind = "NONE"
	}
	v.BusinessPhase = t.Phase
	if t.Phase == task.PhaseRecoveryRequired {
		v.BusinessPhase = ""
		if t.RecoveryOrigin != nil && t.RecoveryOrigin.SourceEventID != "" && t.RecoveryOrigin.Phase.Valid() && t.RecoveryOrigin.Phase != task.PhaseRecoveryRequired {
			v.BusinessPhase = t.RecoveryOrigin.Phase
			v.BusinessPhaseSourceEventID = t.RecoveryOrigin.SourceEventID
		}
	}
	v.PhaseLabel, v.PhaseDescription = TaskPhaseLabel(t.Phase, t.Status)
	if t.Status == task.StatusActive && t.Phase == task.PhaseRecoveryRequired && v.BusinessPhase != "" {
		v.PhaseLabel, v.PhaseDescription = TaskPhaseLabel(v.BusinessPhase, t.Status)
		if stage := map[task.Phase]string{task.PhaseReviewing: "审查", task.PhaseImplementing: "实施", task.PhaseVerifying: "验证"}[v.BusinessPhase]; stage != "" {
			v.PhaseDescription = stage + "阶段受阻，等待恢复"
		} else {
			v.PhaseDescription += "（等待恢复）"
		}
	}
	if t.Status == task.StatusActive && t.Phase == task.PhaseReviewRequired && v.RequiredReviewDepth != nil && *v.RequiredReviewDepth == task.ReviewDepthDeep {
		v.PhaseDescription = "需要深度审查"
	}
	if t.CurrentDefinitionRevisionID != "" {
		work, verification := task.CloneWorkSpec(t.WorkSpec), task.CloneVerificationSpec(t.VerificationSpec)
		v.CurrentWorkSpec, v.CurrentVerificationSpec = &work, &verification
	}
	if t.Status == task.StatusActive && t.Phase == task.PhaseImplementationReady && t.RequiredReviewDepth == nil && t.CurrentReviewID == "" && t.WorkSpec.ImplementationMode == "" {
		v.PhaseLabel = "待实施方案确认"
		v.PhaseDescription = "已跳过审查，等待确认完整实施与验证方案"
	}
	if v.Goal == "" {
		v.Goal = t.Goal
	}
	if v.Title == "" {
		v.Title = v.Goal
	}
	if v.Kind == "" {
		v.Kind = "DEVELOPMENT"
	}
	if t.Type == task.TypeInspection && t.Work.Status != "" {
		v.Status = t.Work.Status
	}
	if v.Status == "COMPLETED" || v.Status == "COMPLETE" {
		v.Status = "SUCCEEDED"
	}
	switch v.Status {
	case "SUCCEEDED", "FAILED", "CANCELED", "REJECTED", "UNKNOWN_ORPHANED", "INFRASTRUCTURE_FAILED", "BLOCKED", "CONFLICT", "SCOPE_CONFLICT":
		v.Terminal = true
	}
	if v.Terminal && (t.Work.DomainResult == "SCOPE_CONFLICT" || t.Work.DomainResult == "CONFLICT") {
		v.Status = "SCOPE_CONFLICT"
	}
	v.Activity = taskPhaseActivity(v)
	if v.Terminal {
		v.Result = &TaskResult{Status: v.Status, Summary: "Task 已结束"}
	}
	v.Details = TaskDiagnostics{TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, Model: t.Model, ReasoningEffort: t.ReasoningEffort, Runs: []TaskRunDetail{}}
	exact := []run.Run{}
	seen := map[string]bool{}
	for _, r := range values {
		if r.ID != "" && !seen[r.ID] && r.TaskID == t.ID && r.ProjectID == t.ProjectID && r.FeatureAreaID == t.FeatureAreaID {
			exact = append(exact, r)
			seen[r.ID] = true
		}
	}
	sort.Slice(exact, func(i, j int) bool {
		if exact[i].CreatedAt.Equal(exact[j].CreatedAt) {
			return exact[i].ID < exact[j].ID
		}
		return exact[i].CreatedAt.Before(exact[j].CreatedAt)
	})
	u := TaskUsage{Status: run.TokenUsageAvailable, DurationComplete: true, RunCount: len(exact), UnavailableRuns: []string{}, PendingRuns: []string{}}
	counts := TaskTokens{}
	phases := map[string]PhaseProgress{}
	var selected *run.Run
	for i := range exact {
		r := &exact[i]
		phase := string(r.Purpose)
		if phase == "SCOPE_REVIEW" {
			phase = "REVIEW"
		}
		status := string(r.Status)
		if r.Purpose == run.PurposeVerification {
			result := VerificationPresentation(*r)
			v.Verification = &result
			// The progression rail is transport/lifecycle only. Report
			// availability is exposed separately and is not a stage outcome.
			status = string(r.Status)
		}
		if t.Type == task.TypeInspection {
			phases[phase] = PhaseProgress{Phase: phase, Status: status, SourceID: r.ID}
		}
		if r.ID == t.Work.RunID || (t.Type == task.TypeInspection && selected == nil) {
			selected = r
		}
		ms, ok := runDuration(*r, now)
		if !ok {
			u.DurationComplete = false
		}
		u.DurationMS += ms
		switch r.Purpose {
		case run.PurposeImplementation:
			u.ImplementationMS += ms
		case run.PurposeVerification:
			u.VerificationMS += ms
		default:
			u.OtherMS += ms
		}
		tokenStatus := r.TokenUsage.Status
		switch {
		case tokenStatus == run.TokenUsageAvailable && r.TokenUsage.Usage != nil:
			c := r.TokenUsage.Usage
			counts.Input += c.InputTokens
			counts.CachedInput += c.CachedInputTokens
			counts.Output += c.OutputTokens
			counts.ReasoningOutput += c.ReasoningOutputTokens
			counts.Total += c.TotalTokens
		case tokenStatus == run.TokenUsagePending:
			u.PendingRuns = append(u.PendingRuns, r.ID)
		default:
			tokenStatus = run.TokenUsageUnavailable
			u.UnavailableRuns = append(u.UnavailableRuns, r.ID)
		}
		if len(v.Details.Runs) < DefaultTaskRunLimit {
			v.Details.Runs = append(v.Details.Runs, TaskRunSummary(*r, now))
		}
	}
	if len(u.UnavailableRuns) > 0 {
		u.Status = run.TokenUsageUnavailable
	} else if len(u.PendingRuns) > 0 || len(exact) == 0 {
		u.Status = run.TokenUsagePending
	}
	if u.Status == run.TokenUsageAvailable {
		counts.NonCachedInput = counts.Input - counts.CachedInput
		u.Tokens = &counts
	}
	v.Details.RunsTotal = len(exact)
	v.Details.RunsTruncated = len(exact) > len(v.Details.Runs)
	v.Usage = u
	if selected != nil {
		r := selected
		if !v.Terminal && t.Phase == task.PhaseReviewing && r.Purpose == run.PurposeScopeReview && r.ReviewID == t.CurrentReviewID {
			v.Activity.ElapsedMS, _ = RunElapsed(*r, now)
		}
		// Review activity requires its executor owner, attached below by
		// AttachTaskReviews. A selected historical Run is not active work.
		if !v.Terminal && (t.Type == task.TypeInspection ||
			(t.Phase == task.PhaseImplementing && r.Purpose == run.PurposeImplementation) ||
			(t.Phase == task.PhaseVerifying && r.Purpose == run.PurposeVerification)) {
			if attachCodingAgentActivity(&v.Activity, *r) {
				v.Activity.ElapsedMS, _ = RunElapsed(*r, now)
			}
		}
		if v.Result != nil {
			if r.Failure != nil {
				v.Result.Summary = shortSummary(r.Failure.Message)
			} else if r.Result != nil && r.Result.AgentClaim.Message != "" {
				v.Result.Summary = shortSummary(r.Result.AgentClaim.Message)
			}
		}
	}
	for _, g := range goals {
		v.Goals = append(v.Goals, GoalProgress{Title: g.Title, Status: string(g.Status)})
	}
	if t.Type == task.TypeInspection {
		fact, ok := phases["INSPECTION"]
		if !ok {
			fact = PhaseProgress{Phase: "INSPECTION", Status: "PENDING"}
		}
		v.Progress = append(v.Progress, fact)
	} else {
		v.Status = string(t.Status)
		v.Terminal = t.Status == task.StatusSucceeded || t.Status == task.StatusCanceled || t.Status == task.StatusFailed
		v.Progress = TaskPhaseProgress(t.Phase, t.Status)
		if t.Phase == task.PhaseRecoveryRequired && v.BusinessPhase != "" {
			v.Progress = TaskPhaseProgress(v.BusinessPhase, t.Status)
			if t.Status == task.StatusActive {
				for i := range v.Progress {
					if v.Progress[i].Status == "CURRENT" {
						v.Progress[i].Status = "BLOCKED"
					}
				}
			}
		}
		if t.Phase == task.PhaseImplementationReady && t.CurrentReviewID == "" && t.ReviewApprovalID == "" && t.RequiredReviewDepth == nil {
			v.Progress[0].Status = "SKIPPED"
			if t.WorkSpec.ImplementationMode == "" {
				v.Progress[1].Status = "PENDING"
			}
		}
	}
	if t.Phase == task.PhaseVerifying || t.Type == task.TypeInspection {
		for _, r := range exact {
			if r.AnalysisState == "REQUIRED" && (r.Purpose == run.PurposeVerification && r.ExecutionProposalID == t.CurrentExecutionID || r.Purpose == run.PurposeInspection && r.ID == t.Work.RunID) {
				v.PhaseLabel, v.PhaseDescription = "待 GPT 分析", "Codex 已执行完成，等待 GPT 分析输出与执行证据。"
				v.Activity.Status, v.Activity.Summary = "RUN_ANALYSIS_REQUIRED", v.PhaseDescription
			}
		}
	}
	applyTaskCommit(&v, t)
	return v
}

// Task phase and terminal state remain the fallback authority when no exact
// active executor telemetry exists. Activity does not create workflow states.
func taskPhaseActivity(v TaskView) TaskActivity {
	activity := TaskActivity{Status: string(v.Phase), Summary: v.PhaseDescription}
	if v.Terminal {
		activity.Status = v.Status
		switch v.Status {
		case "SUCCEEDED":
			activity.Summary = "Task 已完成"
		case "CANCELED":
			activity.Summary = "Task 已取消"
		case "FAILED":
			activity.Summary = "Task 失败"
		default:
			activity.Summary = "Task 已结束"
		}
		return activity
	}
	switch v.Phase {
	case task.PhaseImplementationReady:
		if v.CurrentReview == nil && (v.CurrentWorkSpec == nil || v.CurrentWorkSpec.ImplementationMode == "") {
			activity.Summary = "待实施方案确认；用户已选择跳过审查，尚未授权执行"
		}
	case task.PhaseReviewRequired:
		activity.Summary = "待开始审查"
	case task.PhaseReviewApprovalPending:
		activity.Summary = "审查已完成，等待用户审批"
	}
	if activity.Status == "" {
		activity.Status = v.Status
	}
	if activity.Summary == "" {
		activity.Summary = "等待 Task 活动"
	}
	return activity
}

func attachCodingAgentActivity(activity *TaskActivity, r run.Run) bool {
	a := r.AgentRuntime
	if a == nil || a.Availability != "AVAILABLE" || r.ID == "" ||
		a.RunID != r.ID || a.ProjectID != r.ProjectID || a.FeatureAreaID != r.FeatureAreaID || a.TaskID != r.TaskID ||
		a.ThreadID == "" || a.ThreadID != r.ThreadID || a.TurnID == "" || a.TurnID != r.TurnID {
		return false
	}
	switch r.Status {
	case run.StatusCreated, run.StatusPreparing, run.StatusRunning, run.StatusWaitingApproval, run.StatusCollecting:
	default:
		return false
	}
	activity.Status = string(r.Status)
	activity.Runtime = a
	activity.NativePlan = a.LatestPlan
	if a.WaitingState != "" {
		activity.Status = a.WaitingState
	}
	activity.Summary = map[string]string{"commandExecution": "正在执行命令", "fileChange": "正在修改文件", "mcpToolCall": "正在调用工具", "agentMessage": "正在回复", "reasoning": "正在分析"}[itemType(a)]
	if activity.Summary == "" {
		activity.Summary = "Codex 正在工作"
	}
	if strings.HasPrefix(a.WaitingState, "WAITING_FOR_") {
		activity.Summary = "等待用户响应"
	}
	if a.WaitingState == "WAITING_FOR_COMMAND_APPROVAL" {
		activity.Summary = "等待命令批准，命令尚未执行"
	}
	if a.WaitingState == "WAITING_FOR_FILE_APPROVAL" {
		activity.Summary = "等待文件修改批准"
	}
	if a.AgentHealth == "POSSIBLY_STALLED" {
		activity.Summary = "较长时间没有新活动"
	}
	return true
}

func itemType(a *agentruntime.Snapshot) string {
	if a.CurrentItem != nil {
		return a.CurrentItem.Type
	}
	return ""
}
func shortSummary(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > 600 {
		return string(r[:600]) + "…"
	}
	return string(r)
}
func runDuration(r run.Run, now time.Time) (int64, bool) {
	ms, complete := RunElapsed(r, now)
	if ms == nil {
		return 0, false
	}
	return *ms, complete
}

// Semantic reports do not prove deterministic checks passed. Free-form claims
// are displayed as reports, never parsed into new workflow authority.
func VerificationPresentation(r run.Run) TaskResult {
	v := TaskResult{Status: string(r.Status), Summary: "Verification 尚未结束"}
	if r.Status != run.StatusSucceeded {
		if r.Failure != nil {
			v.Summary = shortSummary(r.Failure.Message)
		}
		return v
	}
	v.Status = "REPORT_UNAVAILABLE"
	v.Summary = "Verification Run 已完成，但没有可用的结构化报告。"
	if r.VerificationReport != nil && r.VerificationReport.Availability == run.VerificationReportAvailable {
		v.Status = "REPORT_AVAILABLE"
		v.Summary = shortSummary(r.VerificationReport.Summary)
	} else if r.VerificationReport != nil && r.VerificationReport.Availability == run.VerificationReportInsufficient {
		v.Status = "VERIFICATION_EVIDENCE_INSUFFICIENT"
		v.Summary = "Verification 已结束，但必需检查的执行证据不完整；请重新验证。"
	}
	return v
}

// BuildTaskWithHistory enriches the same Task-first projection with durable
// Execution/acceptance history. Renderers consume these fields mechanically.
func BuildTaskWithHistory(t task.Task, values []run.Run, goals []taskgoal.Progress, executions []executionproposal.Proposal, receipts []decisionresolution.Receipt, now time.Time) TaskView {
	v := BuildTask(t, values, goals, now)
	exactExecutions := make([]executionproposal.Proposal, 0, len(executions))
	seenExecutions := map[string]bool{}
	for _, e := range executions {
		if e.ID != "" && !seenExecutions[e.ID] && e.TaskID == t.ID && e.ProjectID == t.ProjectID {
			exactExecutions = append(exactExecutions, e)
			seenExecutions[e.ID] = true
		}
	}
	executions = exactExecutions
	sort.Slice(executions, func(i, j int) bool {
		if executions[i].CreatedAt.Equal(executions[j].CreatedAt) {
			return executions[i].ID < executions[j].ID
		}
		return executions[i].CreatedAt.Before(executions[j].CreatedAt)
	})
	byExecution := map[string]decisionresolution.Receipt{}
	for _, receipt := range receipts {
		byExecution[receipt.SubjectID] = receipt
	}
	runByID := map[string]run.Run{}
	for _, value := range values {
		if value.TaskID == t.ID && value.ProjectID == t.ProjectID && value.FeatureAreaID == t.FeatureAreaID {
			runByID[value.ID] = value
		}
	}
	for index, execution := range executions {
		e := executionView(execution)
		h := ExecutionHistoryView{Execution: e, Current: execution.ID == t.CurrentExecutionID, Ordinal: index + 1}
		for _, receipt := range receipts {
			if receipt.ProjectID == t.ProjectID && receipt.Kind == decisionresolution.KindTaskAcceptance && receipt.ResultKind == "EXECUTION_PROPOSAL" && receipt.ResultID == execution.ID && seenExecutions[receipt.SubjectID] && (receipt.Option == "REQUEST_CHANGES" || receipt.Option == "REQUEST_REVERIFICATION" || receipt.Option == "RETRY_IMPLEMENTATION") {
				h.SourceAction = receipt.Option
				h.PredecessorExecutionID, h.SourceDecisionID = receipt.SubjectID, receipt.DecisionID
				break
			}
		}
		if r, ok := runByID[execution.ImplementationRunID]; ok && r.ExecutionProposalID == execution.ID {
			h.ImplementationRun = taskRunTransport(r)
		}
		if r, ok := runByID[execution.VerificationRunID]; ok && r.ExecutionProposalID == execution.ID {
			h.VerificationRun = taskRunTransport(r)
		}
		if receipt, ok := byExecution[execution.ID]; ok {
			copy := receipt
			h.Acceptance = &copy
		}
		v.ExecutionHistory = append(v.ExecutionHistory, h)
	}
	if len(executions) == 0 || t.CurrentExecutionID == "" {
		return v
	}
	var current executionproposal.Proposal
	for _, e := range executions {
		if e.ID == t.CurrentExecutionID {
			current = e
			break
		}
	}
	if current.ID == "" {
		return v
	}
	if !t.Phase.ReviewStage() && !v.Terminal {
		v.CurrentWorkKind = "EXECUTION"
	}
	v.CurrentExecution = ptrExecutionView(executionView(current))
	if implementation, ok := runByID[current.ImplementationRunID]; ok {
		v.currentImplementationExact = implementation.ExecutionProposalID == current.ID && implementation.Purpose == run.PurposeImplementation
		v.ImplementationRun = taskRunTransport(implementation)
	}
	if verification, ok := runByID[current.VerificationRunID]; ok {
		v.currentVerificationExact = verification.ExecutionProposalID == current.ID && verification.Purpose == run.PurposeVerification
		v.VerificationRun = taskRunTransport(verification)
		v.VerificationReport = taskVerificationReport(verification)
	}
	if !v.Terminal && !t.Phase.ReviewStage() {
		attachCurrentExecutionActivity(&v.Activity, current, runByID, now)
	}
	acceptance := &ExecutionAcceptanceView{DecisionID: decisionresolution.TaskAcceptanceDecisionID(current.ID), AllowedActions: []string{}}
	if receipt, ok := byExecution[current.ID]; ok {
		copy := receipt
		acceptance.Receipt = &copy
		acceptance.Status = map[string]string{"ACCEPT": "ACCEPTED", "REJECT": "REJECTED", "REQUEST_CHANGES": "CHANGES_REQUESTED", "REQUEST_REVERIFICATION": "REVERIFICATION_REQUESTED", "RETRY_IMPLEMENTATION": "IMPLEMENTATION_RETRY_REQUESTED", "REQUEST_REVIEW": "SUPERSEDED_FOR_REVIEW"}[receipt.Option]
	} else if v.Terminal {
		acceptance.Status = "NOT_RECORDED"
		v.ReadOnly = true
	} else if t.Phase == task.PhaseAcceptancePending {
		acceptance.Status = "WAITING_ACCEPTANCE"
		acceptance.AllowedActions = acceptanceActions(current, runByID, reverificationImplementation(current, executions, receipts, runByID))
	} else {
		acceptance.Status = "EXECUTION_IN_PROGRESS"
		if len(receipts) > 0 {
			latest := receipts[len(receipts)-1]
			if latest.ResultID == current.ID {
				acceptance.Status = map[string]string{"REQUEST_CHANGES": "CHANGES_REQUESTED", "REQUEST_REVERIFICATION": "REVERIFICATION_REQUESTED", "RETRY_IMPLEMENTATION": "IMPLEMENTATION_RETRY_REQUESTED", "REQUEST_REVIEW": "SUPERSEDED_FOR_REVIEW"}[latest.Option]
			}
		}
	}
	if !v.Terminal && acceptance.Receipt == nil {
		for _, action := range acceptanceActions(current, runByID, reverificationImplementation(current, executions, receipts, runByID)) {
			if action != "ACCEPT" {
				v.RuntimeDispositionActions = append(v.RuntimeDispositionActions, action)
			}
		}
	}
	v.CurrentExecutionAcceptance = acceptance
	ApplyTaskRunRecovery(&v)
	v.NextStep = TaskNextStepFor(v)

	return v
}

func executionView(value executionproposal.Proposal) ExecutionView {
	return ExecutionView{
		Summary:                  shortSummary(value.WorkSpec.Goal),
		ProvenanceKind:           value.ProvenanceKind,
		UpdatedAt:                value.UpdatedAt,
		ID:                       value.ID,
		Outcome:                  string(value.Outcome),
		State:                    string(value.Status),
		FailureCode:              value.FailureCode,
		TaskDefinitionRevisionID: value.TaskDefinitionRevisionID,
		ImplementationRunID:      value.ImplementationRunID,
		VerificationRunID:        value.VerificationRunID,
		RuntimeConfig:            value.RuntimeConfig,
		BackendSelection:         value.BackendSelection,
	}
}

func ptrExecutionView(value ExecutionView) *ExecutionView { return &value }

// attachCurrentExecutionActivity makes the displayed activity an exact
// Execution/Run observation. It is deliberately called after the Task's
// durable phase was projected: a failed Run does not rewrite an ACTIVE Task's
// phase, and a pending decision does not rewrite the failed Run as success.
func attachCurrentExecutionActivity(activity *TaskActivity, execution executionproposal.Proposal, runs map[string]run.Run, now time.Time) {
	if activity == nil || execution.ID == "" {
		return
	}
	activity.OperationKind, activity.OperationID = "EXECUTION", execution.ID
	selected, purpose := currentExecutionRun(execution, runs)
	if selected == nil {
		if execution.Outcome == executionproposal.OutcomeFailed || execution.Outcome == executionproposal.OutcomeCanceled {
			activity.Status = string(execution.Outcome)
			activity.FailureCode = execution.FailureCode
			activity.Summary = executionOutcomeSummary(execution.Outcome)
		}
		return
	}
	activity.RunID = selected.ID
	activity.Status = string(selected.Status)
	if selected.Failure != nil {
		activity.FailureCode = selected.Failure.Code
	}
	if selected.Status == run.StatusRunning || selected.Status == run.StatusWaitingApproval || selected.Status == run.StatusCollecting {
		if attachCodingAgentActivity(activity, *selected) {
			activity.ElapsedMS, _ = RunElapsed(*selected, now)
		}
		return
	}
	activity.Runtime, activity.NativePlan, activity.ElapsedMS = nil, nil, nil
	activity.Summary = executionRunSummary(purpose, selected.Status)
}

func currentExecutionRun(execution executionproposal.Proposal, runs map[string]run.Run) (*run.Run, run.Purpose) {
	if execution.VerificationRunID != "" {
		if value, ok := runs[execution.VerificationRunID]; ok && value.ExecutionProposalID == execution.ID && value.Purpose == run.PurposeVerification {
			copy := value
			return &copy, run.PurposeVerification
		}
	}
	if execution.ImplementationRunID != "" {
		if value, ok := runs[execution.ImplementationRunID]; ok && value.ExecutionProposalID == execution.ID && value.Purpose == run.PurposeImplementation {
			copy := value
			return &copy, run.PurposeImplementation
		}
	}
	return nil, ""
}

func executionOutcomeSummary(outcome executionproposal.Outcome) string {
	switch outcome {
	case executionproposal.OutcomeFailed:
		return "本次 Execution 已失败；Task 阶段和后续决定以独立的持久事实为准。"
	case executionproposal.OutcomeCanceled:
		return "本次 Execution 已取消；Task 阶段和后续决定以独立的持久事实为准。"
	default:
		return "当前 Execution 尚无可展示的 Run 事实。"
	}
}

func executionRunSummary(purpose run.Purpose, status run.Status) string {
	label := "Implementation"
	if purpose == run.PurposeVerification {
		label = "Verification"
	}
	switch status {
	case run.StatusCreated:
		return label + " Run 已预约；请读取原 handoff 回执确认后端派发结果。"
	case run.StatusPreparing:
		return label + " Run 正在准备。"
	case run.StatusSucceeded:
		return label + " Run 已完成；后续阶段和用户决定由独立工作流事实决定。"
	case run.StatusInfrastructureFailed:
		return "本次 " + label + " Run 基础设施失败；Task 阶段未因此改写。"
	case run.StatusFailed:
		return "本次 " + label + " Run 失败；Task 阶段未因此改写。"
	case run.StatusCanceled:
		return "本次 " + label + " Run 已取消；Task 阶段未因此改写。"
	default:
		return label + " Run 状态：" + string(status)
	}
}

func taskRunTransport(value run.Run) *VerificationRunView {
	view := &VerificationRunView{NativeResultState: value.NativeResultState, AnalysisState: value.AnalysisState, ID: value.ID, TransportStatus: string(value.Status)}
	if value.Failure != nil {
		view.FailureCode = value.Failure.Code
	}
	return view
}

func taskVerificationReport(value run.Run) *run.VerificationReport {
	if value.Failure != nil && value.Failure.Code == "VERIFICATION_MUTATED_WORKTREE" {
		return &run.VerificationReport{Availability: run.VerificationReportUnavailable, Summary: "Verification modified the managed worktree; no valid report is available."}
	}
	if value.VerificationReport != nil {
		return value.VerificationReport
	}
	return &run.VerificationReport{Availability: run.VerificationReportUnavailable, Summary: "Structured Verification Report unavailable."}
}

func acceptanceActions(current executionproposal.Proposal, runByID map[string]run.Run, implemented bool) []string {
	actions := []string{"REJECT", "REQUEST_CHANGES"}
	acceptAllowed := false
	if current.VerificationSpec.Mode == task.VerificationModeNone {
		acceptAllowed = current.VerificationSpec.Waiver != nil && current.VerificationSpec.Waiver.Source == task.VerificationWaiverUser && strings.TrimSpace(current.VerificationSpec.Waiver.Reason) != ""
	} else if verification, ok := runByID[current.VerificationRunID]; ok {
		acceptAllowed = (verification.Failure == nil || verification.Failure.Code != "VERIFICATION_MUTATED_WORKTREE") && verification.VerificationReport != nil && verificationReportMatchesSpec(*verification.VerificationReport, current.VerificationSpec)
		if acceptAllowed && current.ApprovedWorksetHash != "" {
			acceptAllowed = verification.RuntimeWorkset != nil && verification.RuntimeWorkset.RunID == verification.ID && run.VerificationEvidenceComplete(*verification.VerificationReport, current.VerificationSpec, verification.RuntimeWorkset)
		}
	}
	if acceptAllowed {
		actions = append([]string{"ACCEPT"}, actions...)
	}
	if current.VerificationSpec.Mode == task.VerificationModeRun {
		if implemented {
			actions = append(actions, "REQUEST_REVERIFICATION")
		}
	}
	return actions
}

func verificationReportMatchesSpec(report run.VerificationReport, spec task.VerificationSpec) bool {
	return report.Availability == run.VerificationReportAvailable && run.VerificationReportMatchesSpec(report, spec)
}

// Reverification receipts preserve the same definition and implementation evidence.
// This read helper never assigns an ancestor Run to the successor Execution.
func reverificationImplementation(current executionproposal.Proposal, executions []executionproposal.Proposal, receipts []decisionresolution.Receipt, runs map[string]run.Run) bool {
	byID := map[string]executionproposal.Proposal{}
	for _, e := range executions {
		if e.TaskID == current.TaskID && e.ProjectID == current.ProjectID && e.TaskDefinitionRevisionID == current.TaskDefinitionRevisionID {
			byID[e.ID] = e
		}
	}
	seen := map[string]bool{}
	for !seen[current.ID] {
		seen[current.ID] = true
		if implementation, ok := runs[current.ImplementationRunID]; ok && implementation.Status == run.StatusSucceeded {
			return true
		}
		predecessor := ""
		for _, receipt := range receipts {
			if receipt.Kind == decisionresolution.KindTaskAcceptance && receipt.Option == "REQUEST_REVERIFICATION" && receipt.ResultID == current.ID {
				predecessor = receipt.SubjectID
				break
			}
		}
		next, ok := byID[predecessor]
		if !ok {
			return false
		}
		current = next
	}
	return false
}

func applyTaskCommit(v *TaskView, t task.Task) {
	if t.Type != task.TypeDevelopment {
		return
	}
	v.CommitStatus = "UNCOMMITTED"
	v.CommitSummary = "代码：未提交"
	v.CommitSHA = t.CommitSHA
	v.MergedCommit = t.MergedCommit
	v.Details.CommitSHA = t.CommitSHA
	v.Details.CommittedAt = t.CommittedAt
	v.Details.MergedCommit = t.MergedCommit
	v.Details.MergedAt = t.MergedAt
	if t.CommitSHA != "" {
		v.CommitStatus = "COMMITTED"
		v.CommitSummary = "代码：已提交 · " + shortCommit(t.CommitSHA)
		v.DevIntegrationStatus = "UNMERGED"
		v.DevSummary = "Dev：等待功能领域提交"
	}
	if t.MergedCommit != "" {
		v.DevIntegrationStatus = "MERGED"
		v.DevSummary = "Dev：已合入 · " + shortCommit(t.MergedCommit)
	}
}
func shortCommit(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// AttachTaskReviews adds exact history and executor-specific current activity.
// The durable Task phase still owns whether a Review can be active.
func AttachTaskReviews(v *TaskView, t task.Task, reviews []review.Review, runs []run.Run) {
	byID := map[string]run.Run{}
	for _, r := range runs {
		if r.TaskID == t.ID && r.ProjectID == t.ProjectID && r.FeatureAreaID == t.FeatureAreaID {
			byID[r.ID] = r
		}
	}
	v.ReviewHistory = []ReviewView{}
	v.CurrentReview = nil
	seen := map[string]bool{}
	for _, r := range reviews {
		if r.ID == "" || seen[r.ID] || r.TaskID != t.ID || r.ProjectID != t.ProjectID || r.FeatureAreaID != t.FeatureAreaID {
			continue
		}
		seen[r.ID] = true
		stage, label, actor := UpperReviewStage(r)
		item := ReviewView{Activity: PresentReviewActivity(r, time.Now()), ExecutorKind: r.ExecutorKind.Effective(), ExecutorMetadata: r.ExecutorMetadata, UpperAccess: r.UpperAccess, ReportID: r.ReportID, Stage: stage, StageLabel: label, NextActor: actor, Baseline: r.BaseCommit, ReviewDepth: r.ReviewDepth, ReviewDisposition: r.ReviewDisposition, ID: r.ID, TaskID: r.TaskID, Ordinal: r.Ordinal, Status: r.TerminalStatus, Instruction: r.Instruction, CreatedAt: r.CreatedAt, UpdatedAt: r.CreatedAt}
		if item.Status == "" {
			item.Status = "PENDING"
		}
		if r.StartFailure != nil {
			item.Status = "FAILED"
			item.FailureSummary = r.StartFailure.Blocker
		}
		if child, ok := byID[r.RunID]; ok && child.ReviewID == r.ID {
			item.Run = taskRunTransport(child)
			item.Status = string(child.Status)
			item.UpdatedAt = child.UpdatedAt
			item.ResultAvailable = child.Result != nil && child.Result.AgentClaim.Message != ""
			if child.Failure != nil {
				item.FailureSummary = child.Failure.Message
			}
		}
		if r.ExecutorKind == review.ExecutorUpperLLM {
			item.ResultAvailable = r.ReportID != ""
		}
		v.ReviewHistory = append(v.ReviewHistory, item)
		if r.ID == t.CurrentReviewID {
			copy := item
			v.CurrentReview = &copy
			if r.ExecutorKind == review.ExecutorUpperLLM && t.Phase.ReviewStage() {
				v.Usage.Status = run.TokenUsageUnavailable
				v.Usage.Tokens = nil
			}
			if v.Terminal {
				continue
			}
			if t.Phase == task.PhaseReviewApprovalPending {
				v.Activity = taskPhaseActivity(*v)
				if r.ExecutorKind == review.ExecutorUpperLLM && item.Stage == "COMPLETED" {
					v.Activity.Summary = "GPT Review 已完成，等待用户审批"
				}
				continue
			}
			if t.Phase != task.PhaseReviewing {
				continue
			}
			if r.ExecutorKind == review.ExecutorUpperLLM {
				v.Activity.Runtime = nil
				v.Activity.NativePlan = nil
				v.Activity.Status = item.Stage
				v.Activity.Summary = item.StageLabel
				if item.Activity.Inactive {
					v.Activity.Summary = item.Activity.Notice
				}
			} else if r.ExecutorKind.Effective() == review.ExecutorCodingAgent && r.TerminalStatus == "" && r.StartFailure == nil {
				if child, ok := byID[r.RunID]; ok && child.ReviewID == r.ID && child.Purpose == run.PurposeScopeReview &&
					(child.OperationKind == "" || child.OperationKind == run.OperationReview) && (child.OperationID == "" || child.OperationID == r.ID) {
					attachCodingAgentActivity(&v.Activity, child)
				}
			}
			if item.Run != nil && item.Run.AnalysisState == "REQUIRED" && t.Phase == task.PhaseReviewing {
				item.Stage, item.StageLabel, item.NextActor = "ANALYSIS_REQUIRED", "Codex 已完成，等待 GPT 分析", "GPT"
				v.CurrentReview.Stage, v.CurrentReview.StageLabel, v.CurrentReview.NextActor = item.Stage, item.StageLabel, item.NextActor
				v.PhaseLabel, v.PhaseDescription = "待 GPT 分析", "Codex 审查已执行完成，等待 GPT 读取输出和证据后提交分析。"
				v.Activity.Status, v.Activity.Summary = "RUN_ANALYSIS_REQUIRED", v.PhaseDescription
			}
			if item.Run != nil {
				switch item.Run.TransportStatus {
				case "INFRASTRUCTURE_FAILED", "FAILED", "CANCELED":
					v.Activity.Status = item.Run.TransportStatus
					v.Activity.Summary = "审查未完成／本次运行已中断"
					v.Activity.Runtime = nil
					v.Activity.NativePlan = nil
				}
			}
		}
	}
	sort.Slice(v.ReviewHistory, func(i, j int) bool {
		if v.ReviewHistory[i].Ordinal == v.ReviewHistory[j].Ordinal {
			return v.ReviewHistory[i].ID < v.ReviewHistory[j].ID
		}
		return v.ReviewHistory[i].Ordinal < v.ReviewHistory[j].Ordinal
	})
}

// TaskRunSummary is the shared bounded Run row. Detailed output is loaded on demand.
func TaskRunSummary(r run.Run, now time.Time) TaskRunDetail {
	purpose := string(r.Purpose)
	if r.Purpose == run.PurposeScopeReview {
		purpose = "REVIEW"
	}
	d := TaskRunDetail{ID: r.ID, ExecutionID: r.AgentExecutionID, AgentExecutionID: r.AgentExecutionID, TaskExecutionID: r.ExecutionProposalID, ReviewID: r.ReviewID, OperationKind: string(r.OperationKind), OperationID: r.OperationID, RunPurpose: string(r.Purpose), Purpose: purpose, WorkspaceID: r.WorkspaceID, ThreadID: r.ThreadID, TurnID: r.TurnID, TransportStatus: string(r.Status), TokenStatus: r.TokenUsage.Status, ConfigSource: "UNKNOWN", CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
	if d.TokenStatus == "" {
		d.TokenStatus = run.TokenUsageUnavailable
	}
	d.ElapsedMS, d.DurationComplete = RunElapsed(r, now)
	if r.Result != nil {
		d.Result = shortSummary(r.Result.AgentClaim.Message)
		d.Commit = r.Result.GitEvidence.HeadCommit
	}
	if r.Failure != nil {
		d.FailureCode = shortSummary(r.Failure.Code)
	}
	return d
}

// RunElapsed does not fabricate zero for missing/invalid timestamps or mark a
// still-running duration complete.
func RunElapsed(r run.Run, now time.Time) (*int64, bool) {
	if r.StartedAt == nil || r.StartedAt.IsZero() {
		return nil, false
	}
	end := now
	if r.FinishedAt != nil {
		end = *r.FinishedAt
	} else {
		switch r.Status {
		case run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
			return nil, false
		}
	}
	if end.Before(*r.StartedAt) {
		return nil, false
	}
	ms := end.Sub(*r.StartedAt).Milliseconds()
	return &ms, r.FinishedAt != nil
}

// AttachTaskRunConfig uses only frozen durable owner configuration.
func AttachTaskRunConfig(d *TaskRunDetail, executions []executionproposal.Proposal, reviews []review.Review) {
	for _, e := range executions {
		if d.TaskExecutionID != "" && e.ID == d.TaskExecutionID {
			d.Model, d.ReasoningEffort, d.TaskDefinitionRevisionID = e.RuntimeConfig.Model, e.RuntimeConfig.ReasoningEffort, e.TaskDefinitionRevisionID
			if d.Model != "" || d.ReasoningEffort != "" {
				d.ConfigSource = "EXECUTION_RUNTIME_CONFIG"
			}
			return
		}
	}
	for _, r := range reviews {
		if d.ReviewID != "" && r.ID == d.ReviewID {
			if r.ExecutorKind == review.ExecutorUpperLLM {
				d.Model = ""
				d.ReasoningEffort = ""
				d.TokenStatus = run.TokenUsageUnavailable
				d.ConfigSource = "UNAVAILABLE"
				return
			}
			d.Model, d.ReasoningEffort = r.Model, r.ReasoningEffort
			if d.Model != "" || d.ReasoningEffort != "" {
				d.ConfigSource = "REVIEW"
			}
			return
		}
	}
}

// TaskRunOwned checks both directions of the durable operation relationship.
// Transport retries never create new presentation ownership.
func TaskRunOwned(t task.Task, r run.Run, executions []executionproposal.Proposal, reviews []review.Review) bool {
	if r.ID == "" || r.TaskID != t.ID || r.ProjectID != t.ProjectID || r.FeatureAreaID != t.FeatureAreaID {
		return false
	}
	switch r.Purpose {
	case run.PurposeScopeReview:
		if r.ExecutionProposalID != "" || r.ReviewID == "" {
			return false
		}
		if r.OperationKind != "" && r.OperationKind != run.OperationReview || r.OperationID != "" && r.OperationID != r.ReviewID {
			return false
		}
		for _, owner := range reviews {
			if owner.ID == r.ReviewID && owner.TaskID == t.ID && owner.ProjectID == t.ProjectID && owner.FeatureAreaID == t.FeatureAreaID && owner.RunID == r.ID {
				return true
			}
		}
	case run.PurposeImplementation, run.PurposeVerification:
		if r.ReviewID != "" || r.ExecutionProposalID == "" {
			return false
		}
		if r.OperationKind != "" && r.OperationKind != run.OperationExecution || r.OperationID != "" && r.OperationID != r.ExecutionProposalID {
			return false
		}
		for _, owner := range executions {
			if owner.ID == r.ExecutionProposalID && owner.TaskID == t.ID && owner.ProjectID == t.ProjectID {
				if r.Purpose == run.PurposeImplementation {
					return owner.ImplementationRunID == r.ID
				}
				return owner.VerificationRunID == r.ID
			}
		}
	}
	return false
}
