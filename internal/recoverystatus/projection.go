package recoverystatus

import "time"

// Projection is a Core read model. It never authorizes dispatch or resolution.
// In particular, engineering recovery is independent of answer consumption.
type Projection struct {
	Required                  bool                           `json:"required"`
	Eligibility               bool                           `json:"eligibility"`
	SourceOperationKind       string                         `json:"sourceOperationKind"`
	SourceOperationID         string                         `json:"sourceOperationId"`
	SourceRunID               string                         `json:"sourceRunId"`
	SourceTurnID              string                         `json:"sourceTurnId"`
	OwnerKind                 string                         `json:"ownerKind,omitempty"`
	OwnerID                   string                         `json:"ownerId,omitempty"`
	ProjectID                 string                         `json:"projectId,omitempty"`
	FeatureAreaID             string                         `json:"featureAreaId,omitempty"`
	OwnerTaskID               string                         `json:"ownerTaskId,omitempty"`
	WorkspaceID               string                         `json:"workspaceId,omitempty"`
	RuntimeWorksetHash        string                         `json:"runtimeWorksetHash,omitempty"`
	MemberCount               int                            `json:"memberCount,omitempty"`
	SourceIsRecoveryChild     bool                           `json:"sourceIsRecoveryChild,omitempty"`
	RecoveryRunID             string                         `json:"recoveryRunId,omitempty"`
	RecoveryTurnID            string                         `json:"recoveryTurnId,omitempty"`
	State                     string                         `json:"state"`
	ReportSummary             string                         `json:"reportSummary,omitempty"`
	Consistency               string                         `json:"consistency,omitempty"`
	EngineeringDisposition    string                         `json:"engineeringDisposition,omitempty"`
	SourceResponseDisposition string                         `json:"sourceResponseDisposition,omitempty"`
	ReasonCodes               []string                       `json:"reasonCodes"`
	AvailableActions          []string                       `json:"availableActions"`
	DisplayTitle              string                         `json:"displayTitle"`
	DisplayMessage            string                         `json:"displayMessage,omitempty"`
	StartedAt                 *time.Time                     `json:"startedAt,omitempty"`
	CompletedAt               *time.Time                     `json:"completedAt,omitempty"`
	LastObservedAt            *time.Time                     `json:"lastObservedAt,omitempty"`
	PurposeConfirmation       *PurposeConfirmationProjection `json:"purposeConfirmation,omitempty"`
}

// PurposeConfirmationProjection contains only exact, durable Recovery-to-intent
// facts. A missing link is a repair condition, never an inferred pending intent.
type PurposeConfirmationProjection struct {
	IntentID           string `json:"intentId,omitempty"`
	AISessionID        string `json:"aiSessionId,omitempty"`
	Version            int    `json:"version,omitempty"`
	ContentHash        string `json:"contentHash,omitempty"`
	State              string `json:"state"`
	OwnerKind          string `json:"ownerKind,omitempty"`
	SelectedVariantID  string `json:"selectedVariantId,omitempty"`
	SubmissionOutcome  string `json:"submissionOutcome,omitempty"`
	FailureCode        string `json:"failureCode,omitempty"`
	ReceiptCommitted   bool   `json:"receiptCommitted,omitempty"`
	Consumed           bool   `json:"consumed,omitempty"`
	RepairRequired     bool   `json:"repairRequired,omitempty"`
	LinkID             string `json:"linkId,omitempty"`
	LinkOrdinal        int    `json:"linkOrdinal,omitempty"`
	RecoveryGeneration int    `json:"recoveryGeneration,omitempty"`
	Historical         bool   `json:"historical,omitempty"`
	CurrentFailureCode string `json:"currentFailureCode,omitempty"`
}

// ProjectProjection accepts only Core-observed facts. Every write repeats
// admission; an action shown by an earlier read is not a capability token.
func ProjectProjection(p Projection) Projection {
	p.AvailableActions = []string{"READ_RECOVERY_STATUS"}
	reasons := make([]string, 0, len(p.ReasonCodes))
	for _, reason := range p.ReasonCodes {
		if reason != "RECOVERY_PURPOSE_REPAIR_REQUIRED" {
			reasons = append(reasons, reason)
		}
	}
	p.ReasonCodes = reasons
	p.DisplayTitle, p.DisplayMessage = "恢复证据不足", ""
	if p.OwnerKind == "PROJECT_INSPECTION" {
		p.FeatureAreaID = ""
		if p.OwnerID == "" || p.ProjectID == "" || p.OwnerTaskID == "" || p.WorkspaceID == "" || p.RuntimeWorksetHash == "" || p.MemberCount < 1 {
			p.Eligibility = false
			p.ReasonCodes = append(p.ReasonCodes, "INSPECTION_RECOVERY_FACTS_INCOMPLETE")
		}
		p.DisplayTitle = "项目级 Inspection 工程状态恢复"
		p.DisplayMessage = "Project scope；无 FeatureArea。Recovery child 与原 Inspection owner 分离。"
	}
	switch p.State {
	case "NOT_REQUIRED":
		p.Required, p.Eligibility = false, false
		p.DisplayTitle = "无需恢复工程状态"
	case "REQUIRED", "PREPARED":
		p.Required = true
		p.DisplayTitle = "需要核验 Codex 工程状态"
		if p.Eligibility {
			p.AvailableActions = append(p.AvailableActions, "START_RECOVERY_STATUS")
		}
	case "ANALYSIS_REQUIRED":
		p.DisplayTitle = "Codex 已执行完成，等待 GPT 分析"
		p.DisplayMessage = "读取 run_analysis_get 后提交 run_analysis_submit。原任务恢复与用户决定仍需单独确认。"
	case "RUNNING":
		p.DisplayTitle = "正在恢复 Codex 工程状态"
	case "REPORT_READY":
		p.DisplayTitle = "工程状态报告待核验"
	case "ASSESSED":
		if p.Consistency == "CONSISTENT" && (p.EngineeringDisposition == "RECOVERED_COMPLETE" || p.EngineeringDisposition == "RECOVERED_PARTIAL" || p.EngineeringDisposition == "REVISION_REQUIRED") {
			p.AvailableActions = append(p.AvailableActions, "RESOLVE_RECOVERY_STATUS")
		}
	case "RESOLVED":
		p.Required, p.Eligibility = false, false
		if p.EngineeringDisposition == "REVISION_REQUIRED" && p.OwnerKind != "PROJECT_INSPECTION" {
			projectPurposeConfirmation(&p)
		}
		if p.OwnerKind == "PROJECT_INSPECTION" {
			switch p.EngineeringDisposition {
			case "RECOVERED_PARTIAL":
				p.AvailableActions = append(p.AvailableActions, "CONTINUE_PROJECT_INSPECTION")
			case "REVISION_REQUIRED":
				p.AvailableActions = append(p.AvailableActions, "PREPARE_SUCCESSOR_INSPECTION")
			}
		}
	case "FAILED", "UNKNOWN":
		p.Eligibility = false
	default:
		p.State, p.Eligibility = "UNKNOWN", false
	}
	if p.State == "ASSESSED" || p.State == "RESOLVED" {
		switch p.EngineeringDisposition {
		case "RECOVERED_COMPLETE":
			p.DisplayTitle = "工程状态已重新核验：已完成"
		case "RECOVERED_PARTIAL":
			p.DisplayTitle = "工程状态已重新核验：部分完成"
		case "USER_DECISION_REQUIRED":
			p.DisplayTitle = "需要用户决定"
		case "REVISION_REQUIRED":
			p.DisplayTitle = "需要调整任务范围"
		}
	}
	canceled := false
	preparationFailed := false
	for _, reason := range p.ReasonCodes {
		canceled = canceled || reason == "RECOVERY_CANCELED"
		preparationFailed = preparationFailed || reason == "RECOVERY_PREPARATION_FAILED"
	}
	if canceled {
		p.Required, p.Eligibility = false, false
		p.DisplayTitle = "工程状态恢复已取消"
		p.AvailableActions = []string{"READ_RECOVERY_STATUS"}
	} else if p.State == "FAILED" && preparationFailed && p.RecoveryRunID != "" && p.RecoveryTurnID == "" {
		p.DisplayTitle = "工程状态恢复准备失败"
		p.DisplayMessage = "准备工作已停止，可明确取消本次恢复及其来源操作。"
		p.AvailableActions = append(p.AvailableActions, "CANCEL_OPERATION")
	} else if p.State != "RESOLVED" && p.RecoveryRunID != "" && p.RecoveryTurnID != "" {
		// These use the existing canonical cancellation/revision entry points.
		// A missing Turn ID offers no interrupt or replacement dispatch.
		p.AvailableActions = append(p.AvailableActions, "CANCEL_OPERATION")
		if p.State == "FAILED" || (p.State == "ASSESSED" && (p.Consistency != "CONSISTENT" || p.EngineeringDisposition == "RECOVERY_INSUFFICIENT" || p.EngineeringDisposition == "RECOVERY_FAILED")) {
			p.AvailableActions = append(p.AvailableActions, "STOP_AND_REVISE")
		}
	}
	if p.SourceResponseDisposition == "UNKNOWN" {
		p.DisplayMessage = p.DisplayMessage + " 旧回答是否被 Codex 消费仍无法确认。"
		if p.State == "RESOLVED" && p.Consistency == "CONSISTENT" {
			p.DisplayMessage = p.DisplayMessage + " 当前工程状态已通过新的 Recovery Turn 独立核验。"
		}
	}
	return p
}

func projectPurposeConfirmation(p *Projection) {
	c := p.PurposeConfirmation
	if c == nil || c.RepairRequired || c.IntentID == "" {
		p.ReasonCodes = append(p.ReasonCodes, "RECOVERY_PURPOSE_REPAIR_REQUIRED")
		p.AvailableActions = append(p.AvailableActions, "REPAIR_RECOVERY_PURPOSE")
		return
	}
	if c.Consumed {
		return
	}
	// An unresolved submission is always read back before considering a fresh
	// source-driven action. Drift cannot prove that its outcome was unconsumed.
	if c.SubmissionOutcome == "UNKNOWN" || c.State == "CONFIRMATION_OUTCOME_UNKNOWN" {
		c.State = "CONFIRMATION_OUTCOME_UNKNOWN"
		p.AvailableActions = append(p.AvailableActions, "READ_CONFIRMATION_RECEIPT")
		return
	}
	if c.State == "CONFIRMING" || c.State == "CONFIRMATION_SUBMITTING" {
		c.State = "CONFIRMATION_SUBMITTING"
		p.AvailableActions = append(p.AvailableActions, "READ_CONFIRMATION_RECEIPT")
		return
	}
	if c.Historical {
		return
	}
	if c.CurrentFailureCode == "EFFECTIVE_CANCELLATION" || c.FailureCode == "EFFECTIVE_CANCELLATION" {
		p.ReasonCodes = append(p.ReasonCodes, "EFFECTIVE_CANCELLATION")
		return
	}
	failureCode := c.CurrentFailureCode
	if failureCode == "" {
		failureCode = c.FailureCode
	}
	if purposeRejectedAction(failureCode) == "PREPARE_RECOVERY_STATUS" {
		p.AvailableActions = append(p.AvailableActions, "PREPARE_RECOVERY_STATUS")
		return
	}
	switch {
	case c.SubmissionOutcome == "UNKNOWN":
		c.State = "CONFIRMATION_OUTCOME_UNKNOWN"
		p.AvailableActions = append(p.AvailableActions, "READ_CONFIRMATION_RECEIPT")
	case c.SubmissionOutcome == "REJECTED_NOT_CONSUMED":
		c.State = "CONFIRMATION_REJECTED_NOT_CONSUMED"
		switch purposeRejectedAction(c.FailureCode) {
		case "EDIT_SELECTION_AND_RESUBMIT":
			p.AvailableActions = append(p.AvailableActions, "EDIT_SELECTION_AND_RESUBMIT")
		case "PREPARE_NEW_VERSION":
			p.AvailableActions = append(p.AvailableActions, "PREPARE_NEW_VERSION")
		case "PREPARE_RECOVERY_STATUS":
			p.AvailableActions = append(p.AvailableActions, "PREPARE_RECOVERY_STATUS")
		default:
			p.ReasonCodes = append(p.ReasonCodes, c.FailureCode)
		}
	case c.ReceiptCommitted:
		c.State = "CONFIRMATION_COMMITTED"
	case c.State == "PENDING":
		c.State = "CONFIRMATION_PENDING"
		p.AvailableActions = append(p.AvailableActions, "COMMIT_CONFIRMATION")
	case c.State == "CONFIRMING":
		c.State = "CONFIRMATION_SUBMITTING"
	case c.State == "STALE":
		c.State = "CONFIRMATION_STALE"
		p.AvailableActions = append(p.AvailableActions, "PREPARE_NEW_VERSION")
	}
}

func purposeRejectedAction(code string) string {
	switch code {
	case "PURPOSE_VARIANT_UNAVAILABLE", "CONFIRMATION_VARIANT_UNAVAILABLE", "PURPOSE_SELECTION_INVALID", "BACKEND_SELECTION_REQUIRED", "BACKEND_SELECTION_UNAVAILABLE", "BACKEND_SELECTION_NOT_APPLICABLE", "RUNTIME_SELECTION_REQUIRED":
		return "EDIT_SELECTION_AND_RESUBMIT"
	case "SOURCE_REVISION_CHANGED", "SOURCE_EPOCH_CHANGED", "SOURCE_WORKSET_CHANGED", "PURPOSE_SOURCE_CHANGED", "RECOVERY_PURPOSE_LINK_NOT_CURRENT":
		return "PREPARE_RECOVERY_STATUS"
	case "PURPOSE_PROJECT_CONTEXT_CHANGED", "PROJECT_TOPOLOGY_CHANGED", "CONFIRMATION_INTENT_NOT_CURRENT":
		return "PREPARE_NEW_VERSION"
	default:
		return ""
	}
}
