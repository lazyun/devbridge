package workflowpresentation

import (
	"fmt"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
)

const (
	DomainStatusActive = "ACTIVE"
	DomainStatusClosed = "CLOSED"
)

type ProductOperationActivity struct {
	State       string `json:"state"`
	Message     string `json:"message,omitempty"`
	Kind        string `json:"kind"`
	OperationID string `json:"operationId"`
	RunID       string `json:"runId"`
}

type ProductTechnicalDetails struct {
	InternalStatus string `json:"internalStatus,omitempty"`
	Diagnostic     string `json:"diagnostic,omitempty"`
	ReadError      string `json:"readError,omitempty"`
}

// CompactTaskProjection is the shared, bounded Task identity used by default
// workspace, navigation, and list reads. Engineering requirements and
// definition content belong exclusively to an explicit exact Task read.
type CompactTaskProjection struct {
	RequiredReviewDepth         *task.ReviewDepth  `json:"requiredReviewDepth"`
	AllowedReviewDepths         []task.ReviewDepth `json:"allowedReviewDepths"`
	ID                          string             `json:"id"`
	Title                       string             `json:"title"`
	ProjectID                   string             `json:"projectId"`
	FeatureAreaID               string             `json:"featureAreaId"`
	RepositoryID                string             `json:"repositoryId,omitempty"`
	Phase                       task.Phase         `json:"phase"`
	Status                      task.Status        `json:"status"`
	CurrentReviewID             string             `json:"currentReviewId,omitempty"`
	CurrentExecutionID          string             `json:"currentExecutionId,omitempty"`
	CurrentDefinitionRevisionID string             `json:"currentDefinitionRevisionId,omitempty"`
	UpdatedAt                   time.Time          `json:"updatedAt"`
	DetailsAvailable            bool               `json:"detailsAvailable"`
}

func NewCompactTaskProjection(value task.Task) CompactTaskProjection {
	return CompactTaskProjection{
		RequiredReviewDepth: value.RequiredReviewDepth, AllowedReviewDepths: task.AllowedReviewDepths(value.RequiredReviewDepth),
		ID: value.ID, Title: boundedTaskTitle(value.Title), ProjectID: value.ProjectID,
		FeatureAreaID: value.FeatureAreaID, RepositoryID: value.RepositoryID,
		Phase: value.Phase, Status: value.Status, CurrentReviewID: value.CurrentReviewID,
		CurrentExecutionID: value.CurrentExecutionID, CurrentDefinitionRevisionID: value.CurrentDefinitionRevisionID,
		UpdatedAt: value.UpdatedAt, DetailsAvailable: value.ID != "",
	}
}

func boundedTaskTitle(value string) string {
	const limit = 512
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// FeatureAreaProductProjection is the single client-neutral read model for a
// FeatureArea. Internal lifecycle remains diagnostic; clients consume
// DomainStatus and the other derived product fields directly.
type FeatureAreaProductProjection struct {
	FeatureAreaID     string                    `json:"featureAreaId"`
	FeatureAreaName   string                    `json:"featureAreaName"`
	DomainStatus      string                    `json:"domainStatus,omitempty"`
	InternalStatus    string                    `json:"internalStatus,omitempty"`
	CurrentTask       *wp.TaskFact              `json:"currentTask,omitempty"`
	LatestTask        *wp.TaskFact              `json:"latestTask,omitempty"`
	TaskPhase         string                    `json:"taskPhase,omitempty"`
	OperationActivity *ProductOperationActivity `json:"operationActivity,omitempty"`
	RequiredAction    *wp.Action                `json:"requiredAction,omitempty"`
	AvailableActions  []string                  `json:"availableActions"`
	TechnicalDetails  ProductTechnicalDetails   `json:"technicalDetails"`
}

// FeatureAreaDomainStatus maps Core's authoritative lifecycle into the two
// product states. Unknown values deliberately return ok=false.
func FeatureAreaDomainStatus(internal string, legacyReadOnly bool) (status string, ok bool) {
	if legacyReadOnly {
		return DomainStatusClosed, true
	}
	switch internal {
	case "DISCOVERING", "ACTIVE", "RETIRING":
		return DomainStatusActive, true
	case "RETIRED", "DECOMPOSED":
		return DomainStatusClosed, true
	default:
		return "", false
	}
}

func NewFeatureAreaProductProjection(id, name, internal string, legacyReadOnly bool) FeatureAreaProductProjection {
	status, ok := FeatureAreaDomainStatus(internal, legacyReadOnly)
	out := FeatureAreaProductProjection{
		FeatureAreaID: id, FeatureAreaName: name, DomainStatus: status, InternalStatus: internal,
		AvailableActions: []string{}, TechnicalDetails: ProductTechnicalDetails{InternalStatus: internal},
	}
	if !ok {
		out.TechnicalDetails.Diagnostic = "UNKNOWN_FEATURE_AREA_INTERNAL_STATUS"
	}
	return out
}

// FeatureAreaNavigationItem and TaskNavigationItem are bounded, display-only
// navigation facts. Selecting either item is local Card state and never changes
// the authoritative workflow or Project binding.
type FeatureAreaNavigationItem struct {
	Product FeatureAreaProductProjection `json:"product"`
	// RunningDevelopmentTaskID is presentation telemetry from current workflow guidance.
	RunningDevelopmentTaskID        string                  `json:"runningDevelopmentTaskId,omitempty"`
	Availability                    string                  `json:"availability"`
	ReviewStatus                    string                  `json:"reviewStatus"`
	CommitStatus                    string                  `json:"commitStatus"`
	MergedCommit                    string                  `json:"mergedCommit,omitempty"`
	CurrentTaskID                   string                  `json:"currentTaskId,omitempty"`
	CurrentTaskTitle                string                  `json:"currentTaskTitle,omitempty"`
	CurrentTaskPhase                string                  `json:"currentTaskPhase,omitempty"`
	CurrentTaskStatus               string                  `json:"currentTaskStatus,omitempty"`
	CurrentTaskCommitStatus         string                  `json:"currentTaskCommitStatus,omitempty"`
	CurrentTaskDevIntegrationStatus string                  `json:"currentTaskDevIntegrationStatus,omitempty"`
	CurrentTaskCommitSHA            string                  `json:"currentTaskCommitSha,omitempty"`
	CurrentTaskMergedCommit         string                  `json:"currentTaskMergedCommit,omitempty"`
	LatestTaskID                    string                  `json:"latestTaskId,omitempty"`
	LatestTaskTitle                 string                  `json:"latestTaskTitle,omitempty"`
	LatestTaskPhase                 string                  `json:"latestTaskPhase,omitempty"`
	LatestTaskStatus                string                  `json:"latestTaskStatus,omitempty"`
	LatestTaskCommitStatus          string                  `json:"latestTaskCommitStatus,omitempty"`
	LatestTaskDevIntegrationStatus  string                  `json:"latestTaskDevIntegrationStatus,omitempty"`
	LatestTaskCommitSHA             string                  `json:"latestTaskCommitSha,omitempty"`
	LatestTaskMergedCommit          string                  `json:"latestTaskMergedCommit,omitempty"`
	CurrentAction                   wp.Action               `json:"currentAction"`
	ReadError                       string                  `json:"readError,omitempty"`
	UpdatedAt                       time.Time               `json:"updatedAt"`
	Retirement                      *featurearea.Retirement `json:"retirement,omitempty"`
	ID                              string                  `json:"id"`
	Name                            string                  `json:"name"`
	Status                          string                  `json:"status"`
}

type TaskNavigationItem struct {
	Work          task.Work `json:"work"`
	ID            string    `json:"id"`
	FeatureAreaID string    `json:"featureAreaId"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// PendingConfirmation is a read-only projection of an already submitted
// confirmation. It carries the original durable identity and any authoritative
// child identities, but deliberately exposes no command or fallback action.
type PendingConfirmation struct {
	IntentID        string `json:"intentId"`
	RequestID       string `json:"requestId"`
	Purpose         string `json:"purpose"`
	Action          string `json:"action"`
	State           string `json:"state"`
	Outcome         string `json:"outcome"`
	OutcomeRecorded bool   `json:"outcomeRecorded"`
	NextAction      string `json:"nextAction,omitempty"`
	FeatureAreaID   string `json:"featureAreaId,omitempty"`
	TaskID          string `json:"taskId,omitempty"`
	ReviewID        string `json:"reviewId,omitempty"`
	RunID           string `json:"runId,omitempty"`
}

// WorkspaceProjection is the pure-read model for the long-lived Dev Bridge
// Card. Presentation is scoped to the exact selected FeatureArea or Task;
// A nil Presentation may also mean selected-detail enrichment failed; the
// inventory and SelectedWork remain fresh in that case.
// PendingDecisions is the Project-wide navigation inventory, including foreign
// owners. Card adapters must derive current-scope interactions before rendering
// controls or establishing authority; empty selected Task does not unbind Area.
type WorkspaceProjection struct {
	ConfirmationFeed *TaskConfirmationFeed `json:"confirmationFeed,omitempty"`
	PresentationID   string                `json:"presentationId"`
	RefreshedAt      time.Time             `json:"refreshedAt"`
	// SelectedWorkReadError is local enrichment failure; ReadError is reserved
	// for authoritative Workspace reads.
	SelectedWorkReadError string `json:"selectedWorkReadError,omitempty"`
	// PendingDecisionsReadError preserves a usable Project inventory when an
	// independent authoritative decision read must be retried. It never turns a
	// stale partial decision list into an executable action.
	PendingDecisionsReadError     string                      `json:"pendingDecisionsReadError,omitempty"`
	PendingDecisions              []PendingDecision           `json:"pendingDecisions"`
	PendingConfirmationsReadError string                      `json:"pendingConfirmationsReadError,omitempty"`
	PendingConfirmations          []PendingConfirmation       `json:"pendingConfirmations,omitempty"`
	ReadError                     string                      `json:"readError,omitempty"`
	SelectedWork                  *SelectedOperation          `json:"selectedWork"`
	SchemaVersion                 string                      `json:"schemaVersion"`
	DevelopmentContext            DevelopmentContext          `json:"developmentContext"`
	FeatureAreas                  []FeatureAreaNavigationItem `json:"featureAreas"`
	SelectedFeatureAreaID         string                      `json:"selectedFeatureAreaId,omitempty"`
	Tasks                         []TaskNavigationItem        `json:"tasks"`
	SelectedTaskID                string                      `json:"selectedTaskId,omitempty"`
	Presentation                  *Projection                 `json:"presentation,omitempty"`
}

// WorkspaceIdentity is routing metadata, never a persisted workflow entity.
func WorkspaceIdentity(session, project string) string { return "workspace:" + session + ":" + project }
func Availability(status string) string {
	switch status {
	case "DISCOVERING", "ACTIVE":
		return "AVAILABLE"
	case "RETIRING":
		return "CLOSING"
	case "RETIRED":
		return "CLOSED"
	case "DECOMPOSED":
		return "DECOMPOSED"
	}
	return "UNAVAILABLE"
}
func AvailabilityLabel(status string) string {
	switch status {
	case "AVAILABLE":
		return "可用"
	case "CLOSING":
		return "关闭中"
	case "CLOSED":
		return "已关闭"
	case "DECOMPOSED":
		return "已拆分"
	}
	return "不可用"
}
func DomainStatusLabel(status string) string {
	switch status {
	case DomainStatusActive:
		return "活跃"
	case DomainStatusClosed:
		return "已关闭"
	default:
		return "状态不可用"
	}
}
func ReviewLabel(status string) string {
	switch status {
	case "":
		return "未审查"
	case "RUNNING", "PENDING", "PREPARING":
		return "审查中"
	case "REVIEW_APPROVAL_PENDING", "WAITING_APPROVAL":
		return "等待确认"
	case "FAILED", "INFRASTRUCTURE_FAILED":
		return "失败"
	case "BLOCKED":
		return "受阻"
	case "SUCCEEDED", "COMPLETE", "COMPLETED", "APPROVED":
		return "已完成"
	}
	return "审查不可用"
}

// CurrentAction is shared by inventory and detailed projections. Eligibility
// remains an observed fact and commands always revalidate their exact target.
func CurrentAction(p Projection) wp.Action {
	if p.PendingDecision != nil {
		return wp.Action{Code: "RESOLVE_DECISION", Required: true, Message: p.PendingDecision.Summary}
	}
	if action, ok := DirectFileWorkerAction(p); ok {
		return action
	}
	if p.Recovery != nil && p.Recovery.Required {
		if p.Recovery.Reason == "UPPER_WORKER_CONTINUATION_UNSUPPORTED" {
			if p.CurrentOperation != nil && p.CurrentOperation.Kind == "REVIEW" {
				for _, capability := range p.Capabilities {
					target := capability.Target
					if capability.Action == "CANCEL_EXECUTION" && capability.Available && target != nil && target.OperationKind == "REVIEW" && target.OperationID == p.CurrentOperation.ID && target.RunID == p.CurrentOperation.RunID && target.RunID != "" {
						return wp.Action{Code: "CANCEL_REVIEW", Required: true, Message: p.Recovery.Summary}
					}
				}
			}
			return wp.Action{Code: "REFRESH", Required: true, Message: p.Recovery.Summary}
		}
		return wp.Action{Code: "RECOVER", Required: true, Message: p.Recovery.Summary}
	}
	if p.Commit != nil {
		if p.Commit.CanRecoverMerge {
			return wp.Action{Code: "RECOVER_FEATURE_AREA_MERGE", Required: true, Message: "继续原逐仓合入"}
		}
		if p.Commit.MergeRetryOperation != nil {
			return wp.Action{Code: "REPREPARE_FEATURE_AREA_MERGE", Required: true, Message: "重新授权已拒绝的合入"}
		}
		if p.Commit.MergeRecoveryState == "UNKNOWN" {
			return wp.Action{Code: "REFRESH", Required: true, Message: "合入结果尚未确认，仅可查看回执"}
		}
	}
	if p.CommitView != nil && p.CommitView.Activity == "RECOVERY_REQUIRED" && p.Commit != nil && p.Commit.CanCommit {
		return wp.Action{Code: "COMMIT_FEATURE_AREA", Required: true, Message: "恢复功能领域提交"}
	}
	if strings.Contains(p.NextAction.Code, "RECOVER") || strings.Contains(p.NextAction.Code, "RESOLVE") {
		return p.NextAction
	}
	if p.Commit != nil && p.Commit.CanRetry {
		return wp.Action{Code: "RETRY_TASK_COMMIT", Required: true, Message: "重试 Task 自动提交"}
	}
	if p.Commit != nil && p.Commit.CanCommit {
		return wp.Action{Code: "COMMIT_FEATURE_AREA", Required: true, Message: "提交功能领域到 dev"}
	}
	if p.NextAction.Code != "" {
		return p.NextAction
	}
	return wp.Action{Code: "NONE", Message: "无待处理动作"}
}

// DirectFileWorkerAction preserves an exact enrolled workflow's unsupported
// continuation boundary. Only authoritative, same-owner guidance may offer its
// existing explicit cancellation. Missing or foreign observations require a
// refresh; neither this projection nor a worker claim grants cancellation.
func DirectFileWorkerAction(p Projection) (wp.Action, bool) {
	state := p.DirectFile
	if state == nil || state.FailureCode != directfile.WorkerContinuationUnsupported {
		return wp.Action{}, false
	}
	message := state.FailureReason
	if message == "" {
		message = "Worker continuation is unsupported; inspect the exact workflow. No worker stop or rollback is proven."
	}
	fallback := wp.Action{Code: "REFRESH", Required: true, Message: message}
	g := p.Guidance
	if state.ProtocolVersion != 2 || state.WorkflowID == "" || state.RuntimeEpoch == "" || state.GrantGeneration <= 0 ||
		state.TaskID == "" || state.TaskDefinitionRevisionID == "" || state.DecisionID == "" || state.AISessionID == "" ||
		state.ProjectID == "" || state.FeatureAreaID == "" || state.Enabled ||
		p.TaskView == nil || p.TaskView.ID != state.TaskID || p.TaskView.Terminal ||
		p.TaskView.DirectFile == nil || p.TaskView.DirectFile.WorkflowID != state.WorkflowID ||
		p.TaskView.DirectFile.RuntimeEpoch != state.RuntimeEpoch || p.TaskView.DirectFile.GrantGeneration != state.GrantGeneration ||
		p.Details.TaskID != state.TaskID || p.Details.TaskDefinitionRevisionID != state.TaskDefinitionRevisionID ||
		g == nil || g.ProjectID != state.ProjectID || g.FeatureAreaID != state.FeatureAreaID ||
		g.TaskID != state.TaskID || g.TaskDefinitionRevisionID != state.TaskDefinitionRevisionID {
		return fallback, true
	}
	if p.NextAction.Code == "RESOLVE_CURRENT_TASK_ACTION" {
		// The current Task-action owner was attached after file guidance. An
		// unresolved consumed confirmation must not be replaced by an offer to
		// prepare another cancellation, which admission would correctly reject.
		if node := p.TaskView.ActionNode; node != nil && node.TaskID == state.TaskID && node.Blocking {
			return p.NextAction, true
		}
		return fallback, true
	}
	if g.NextAction.Code == "CANCEL_DIRECT_FILE" {
		return wp.Action{Code: "CANCEL_DIRECT_FILE", Required: true, Message: message}, true
	}
	if g.NextAction.Code == "WAIT_FOR_DIRECT_FILE" {
		return wp.Action{Code: "WAIT_FOR_DIRECT_FILE", Required: true, Message: message}, true
	}
	return fallback, true
}

func SelectWorkspace(out *WorkspaceProjection, areaID, taskID string) {
	sort.SliceStable(out.FeatureAreas, func(i, j int) bool {
		a, b := out.FeatureAreas[i], out.FeatureAreas[j]
		if a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.ID < b.ID
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	valid := false
	for _, a := range out.FeatureAreas {
		if a.ID == areaID {
			valid = true
		}
	}
	if !valid {
		areaID = ""
		taskID = ""
		best := 99
		for _, a := range out.FeatureAreas {
			if a.Availability != "AVAILABLE" {
				continue
			}
			rank := 3
			if a.Status == "ACTIVE" {
				rank = 2
			}
			if a.CurrentTaskID != "" && !terminalTask(a.CurrentTaskStatus) {
				rank = 1
			}
			if a.CurrentAction.Required && !strings.HasPrefix(a.CurrentAction.Code, "WAIT") {
				rank = 0
			}
			if rank < best {
				best = rank
				areaID = a.ID
			}
		}
	}
	tasks := make([]TaskNavigationItem, 0)
	for _, t := range out.Tasks {
		if t.FeatureAreaID == areaID {
			tasks = append(tasks, t)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].UpdatedAt.Equal(tasks[j].UpdatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})
	out.Tasks = tasks
	out.SelectedFeatureAreaID = areaID
	selectedWorkID := ""
	out.SelectedTaskID = ""
	out.SelectedWork = nil
	for _, t := range tasks {
		if t.ID == taskID {
			selectedWorkID = t.ID
		}
	}
	if selectedWorkID == "" {
		best := 99
		for _, t := range tasks {
			rank := 2
			if !terminalTask(t.Status) {
				rank = 0
			}
			for _, a := range out.FeatureAreas {
				if a.ID == areaID && a.CurrentTaskID == t.ID && a.CurrentAction.Code == "RETRY_TASK_COMMIT" {
					rank = 1
				}
			}
			if rank < best {
				best = rank
				selectedWorkID = t.ID
			}
		}
	}
	for _, t := range tasks {
		if t.ID == selectedWorkID {
			if t.Work.Type == task.TypeDevelopment {
				out.SelectedTaskID = t.ID
			}
		}
	}
}
func terminalTask(s string) bool { return s == "SUCCEEDED" || s == "FAILED" || s == "CANCELED" }
func RenderWorkspaceText(p WorkspaceProjection) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n", p.DevelopmentContext.Project.Name)
	for _, r := range p.DevelopmentContext.Repositories {
		fmt.Fprintf(&b, "Branch: %s · %s\n", r.Branch, r.Revision)
	}
	b.WriteString("Feature Areas:\n")
	for _, a := range p.FeatureAreas {
		commit := "提交不可用"
		switch a.CommitStatus {
		case "UNCOMMITTED":
			commit = "未提交"
		case "COMMITTED":
			commit = "无未提交工作"
			if a.MergedCommit != "" {
				commit = "已合入 dev · " + shortCommit(a.MergedCommit)
			}
		}
		activity := ""
		if a.Product.OperationActivity != nil {
			activity = a.Product.OperationActivity.Message
		}
		fmt.Fprintf(&b, "- %s · %s · Review %s · 提交 %s · 当前 Task %s · Task 阶段 %s · 运行活动 %s · Action %s\n", a.Name, DomainStatusLabel(a.Product.DomainStatus), ReviewLabel(a.ReviewStatus), commit, a.CurrentTaskTitle, a.Product.TaskPhase, activity, a.CurrentAction.Code)
		if a.ReadError != "" {
			fmt.Fprintf(&b, "  Read failure: %s\n", a.ReadError)
		}
	}
	area, taskName, action := p.SelectedFeatureAreaID, p.SelectedTaskID, "NONE"
	for _, a := range p.FeatureAreas {
		if a.ID == p.SelectedFeatureAreaID {
			area = a.Name + " (" + a.ID + ")"
			action = a.CurrentAction.Code
		}
	}
	if p.SelectedWork != nil {
		taskName = p.SelectedWork.Kind + " (" + p.SelectedWork.OperationID + ") · ownerTaskId=" + p.SelectedWork.OwnerTaskID + " · runId=" + p.SelectedWork.RunID + " · " + p.SelectedWork.Status
	}
	if p.Presentation != nil {
		action = CurrentAction(*p.Presentation).Code
	}
	fmt.Fprintf(&b, "Current FeatureArea: %s\nCurrent Work: %s\nCurrent Action: %s\nRefreshed: %s\n", area, taskName, action, p.RefreshedAt.Format(time.RFC3339))
	if p.SelectedWorkReadError != "" {
		fmt.Fprintf(&b, "Selected work read failure: %s\n", p.SelectedWorkReadError)
	}
	if p.Presentation != nil {
		b.WriteString(RenderText(*p.Presentation))
	}
	return b.String()
}
