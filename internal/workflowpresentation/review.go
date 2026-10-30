package workflowpresentation

import (
	"time"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

// ReviewProgressStatus describes one exact Review, never aggregate history.
// A successful Run is not a completed current Review while its owner remains
// active (for example, while finalization is still in progress).
func ReviewProgressStatus(value review.Review, execution *run.Run, active bool) string {
	if value.TerminalStatus == "CANCELED" {
		return "CANCELED"
	}
	if value.StartFailure != nil {
		return "BLOCKED"
	}
	if execution == nil || execution.ID != value.RunID || execution.ReviewID != value.ID {
		return "UNKNOWN"
	}
	switch execution.Status {
	case run.StatusCreated, run.StatusPreparing, run.StatusRunning, run.StatusCollecting, run.StatusWaitingApproval:
		return "RUNNING"
	case run.StatusSucceeded:
		if active {
			return "RUNNING"
		}
		return "COMPLETE"
	case run.StatusFailed, run.StatusInfrastructureFailed:
		return "FAILED"
	case run.StatusCanceled:
		return "CANCELED"
	default:
		return "UNKNOWN"
	}
}

// UpperReviewStage uses durable authorization/report facts, never idle time or
// inferred model thinking. Elapsed time is Bridge-observed review elapsed.
func UpperReviewStage(v review.Review) (string, string, string) {
	if v.ExecutorKind != review.ExecutorUpperLLM {
		return "", "", ""
	}
	if v.TerminalStatus == "CANCELED" {
		return "CANCELED", "审查已取消", "USER"
	}
	if v.ReportID != "" {
		return "COMPLETED", "审查已完成，报告可读", "USER"
	}
	if v.UpperAccess == nil {
		return "PREPARING", "正在准备审查环境", "CORE"
	}
	switch v.UpperAccess.State {
	case "PREPARING":
		if v.UpperAccess.Blocker != "" {
			return "PREPARATION_FAILED", "审查环境准备失败：" + v.UpperAccess.Blocker, "UPPER_LLM"
		}
		return "PREPARING", "正在准备审查环境", "CORE"
	case "ACTIVE":
		if v.UpperAccess.LastObservedAt != nil {
			return "READING", "GPT 已读取文件，等待原对话继续审查并提交报告", "UPPER_LLM"
		}
		return "AWAITING_FILE_ACCESS", "等待原 GPT 对话读取文件并提交审查报告", "UPPER_LLM"
	case "AWAITING_REPORT":
		return "AWAITING_REPORT", "等待原 GPT 对话提交审查报告", "UPPER_LLM"
	case "FINALIZING":
		return "FINALIZING", "正在完成审查", "CORE"
	case "RECOVERY_REQUIRED":
		return "RECOVERY_REQUIRED", "审查中断，授权失效，待恢复", "USER"
	case "CANCELED":
		return "CANCELED", "审查已取消", "USER"
	default:
		return "AWAITING_REPORT", "等待原 GPT 对话提交审查报告", "UPPER_LLM"
	}
}

// ReviewActivity is a read-only explanation of the executor and observed activity.
// Silence is not a failure or evidence that a model is thinking. Reads never
// update LastActivityAt or dispatch work.
type ReviewActivity struct {
	ExecutionMethod string     `json:"executionMethod"`
	Stage           string     `json:"stage"`
	Summary         string     `json:"summary"`
	LastActivityAt  *time.Time `json:"lastActivityAt,omitempty"`
	Inactive        bool       `json:"inactive"`
	Notice          string     `json:"notice,omitempty"`
}

func PresentReviewActivity(value review.Review, now time.Time) *ReviewActivity {
	if value.ExecutorKind == review.ExecutorPiAgent {
		return &ReviewActivity{ExecutionMethod: "Pi Agent 后台审查", Summary: "由 Pi Agent 执行；以实际活动记录为准"}
	}
	if value.ExecutorKind.Effective() != review.ExecutorUpperLLM {
		return &ReviewActivity{ExecutionMethod: "Codex 审查", Summary: "由 Codex 后台执行；可在运行会话查看实际活动"}
	}
	stage, label, _ := UpperReviewStage(value)
	out := &ReviewActivity{ExecutionMethod: "GPT 审查", Stage: stage, Summary: label}
	if value.UpperAccess != nil && value.UpperAccess.LastObservedAt != nil {
		observed := *value.UpperAccess.LastObservedAt
		out.LastActivityAt = &observed
	}
	if stage != "AWAITING_FILE_ACCESS" && stage != "READING" && stage != "AWAITING_REPORT" {
		return out
	}
	baseline := value.CreatedAt
	if out.LastActivityAt != nil {
		baseline = *out.LastActivityAt
	}
	if !baseline.IsZero() && now.Sub(baseline) >= 5*time.Minute {
		out.Inactive = true
		out.Notice = "已至少 5 分钟没有新的文件审查记录。请在原 GPT 对话中继续审查并提交报告；刷新页面只更新已记录的状态。"
	}
	return out
}
