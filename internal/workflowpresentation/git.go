package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/featurearea"
	"strings"
)

type CommitView struct {
	Summary     string                  `json:"summary"`
	Reason      string                  `json:"reason,omitempty"`
	Activity    string                  `json:"activity,omitempty"`
	ActionLabel string                  `json:"actionLabel"`
	Details     featurearea.CommitFacts `json:"details"`
}

// CommitReason is deliberately human-readable; machine codes and raw errors
// remain in Details. It does not infer permission to retry.
func CommitReason(reason string) string {
	switch {
	case strings.Contains(reason, "MERGE_CONFLICT"):
		return "无法提交到 dev：存在代码冲突"
	case strings.Contains(reason, "RECOVERY"), strings.Contains(reason, "INCOMPLETE"):
		return "提交状态需要恢复"
	case strings.Contains(reason, "SNAPSHOT_DRIFT"):
		return "验收后的代码发生变化，请查看详情后恢复提交"
	case strings.Contains(strings.ToLower(reason), "clean"), strings.Contains(strings.ToLower(reason), "dirty"):
		return "请先处理开发工作区中尚未提交的改动"
	case strings.Contains(reason, "TASK_COMMIT_PENDING"):
		return "等待 Task 完成验证、验收和代码提交"
	case strings.Contains(reason, "BUSY"), strings.Contains(reason, "FINALIZATION_PENDING"):
		return "等待当前任务完成"
	case strings.Contains(reason, "UNAVAILABLE"):
		return "功能领域当前不可提交"
	case reason != "":
		return "提交暂时不可用，请查看详情"
	default:
		return ""
	}
}
func ApplyCommitFacts(p *Projection, facts featurearea.CommitFacts) {
	c := p.Commit
	if c == nil {
		return
	}
	v := &CommitView{Summary: "提交：未提交", Reason: CommitReason(c.Reason), ActionLabel: "提交到 " + c.DevelopmentBranch, Details: facts}
	if c.CommitStatus == "COMMITTED" && c.MergedCommit != "" {
		v.Summary = "提交：已合入 dev · " + shortCommit(c.MergedCommit)
	}
	if len(facts.Delivery.Members) > 0 {
		v.ActionLabel = "提交到各仓库开发分支"
		if c.CommitStatus == "COMMITTED" && facts.Delivery.State == "COMPLETED" && !facts.Pending {
			v.Summary = "提交：已全部合入开发分支"
		} else {
			for _, m := range facts.Delivery.Members {
				if m.State == "COMPLETED" {
					v.Summary = "提交：部分仓库已合入"
					break
				}
			}
		}
	} else if c.CommitStatus == "COMMITTED" && len(facts.Tasks) > 0 {
		noChanges := true
		for _, f := range facts.Tasks {
			if f.Pending || f.Delivery.State != "COMPLETED" || len(f.Delivery.Members) == 0 {
				noChanges = false
				break
			}
			for _, m := range f.Delivery.Members {
				if m.State != "NO_CHANGE" {
					noChanges = false
				}
			}
		}
		if noChanges {
			v.Summary = "提交：无代码变更，无需合入"
		}
	}
	// A settled delivery can no longer be waiting for the same Task's commit.
	// Keep admission facts intact; only remove this stale display explanation.
	activeTask := p.TaskView != nil && p.TaskView.Status == "ACTIVE" || p.CurrentTask != nil && p.CurrentTask.Status == "ACTIVE" || p.Product.CurrentTask != nil && p.Product.CurrentTask.Status == "ACTIVE"
	if !activeTask && c.Reason == "TASK_COMMIT_PENDING" && c.CommitStatus == "COMMITTED" && !facts.Pending && facts.LastError == "" && c.MergeRecoveryState == "" && (c.MergedCommit != "" || facts.Delivery.State == "COMPLETED") {
		v.Reason = ""
	}
	if facts.Pending {
		v.Activity = "RUNNING"
		v.Reason = "正在提交…"
		v.ActionLabel = "正在提交…"
	}
	if facts.LastError != "" {
		v.Activity = "FAILED"
		v.Reason = CommitReason(facts.LastError)
		if facts.Pending {
			v.Activity = "RECOVERY_REQUIRED"
			v.Reason = "提交状态需要恢复"
			v.ActionLabel = "恢复提交"
		}
	}
	if facts.Delivery.State == "RELEASED" {
		v.Activity = ""
		v.Reason = "旧计划已解除，请重新确认剩余合入"
	}
	if c.MergeRecoveryState != "" {
		v.Activity = "RECOVERY_REQUIRED"
		switch c.MergeRecoveryState {
		case "RESUMABLE":
			v.Reason, v.ActionLabel = "提交已中断，可继续原交付", "继续合入"
		case "REPREPARE_REQUIRED":
			v.Reason, v.ActionLabel = "合入已在执行前拒绝，请处理原因后重新授权", "重新授权合入"
		default:
			v.Activity, v.Reason, v.ActionLabel = "OUTCOME_UNKNOWN", "合入结果尚未确认，请刷新查看回执；暂不可重试", "查看回执"
		}
	}
	p.CommitView = v
	for _, t := range []*TaskView{p.TaskView, p.LatestTaskView} {
		if t == nil || t.CommitStatus == "" {
			continue
		}
		f, ok := facts.Task(t.ID)
		if ok {
			t.Details.CommitReceipt = &f
		}
		if ApplyTaskDeliveryFacts(t, f) {
			continue
		}
		if t.Status != "SUCCEEDED" || t.CommitStatus != "UNCOMMITTED" {
			continue
		}
		if f.Pending && f.LastError == "" {
			t.CommitActivity = "RUNNING"
			t.CommitSummary = "代码：正在提交…"
			if v.Reason != "提交状态需要恢复" {
				v.Reason = "等待 Task 自动提交完成"
			}
		} else if f.LastError != "" {
			t.CommitActivity = "FAILED"
			t.CommitSummary = "Task 已验收 · 代码提交失败"
			t.CommitErrorSummary = CommitReason(f.LastError)
		}
	}
	// Hide the recovery affordance until a persisted failure exists. The Core
	// capability is still required and revalidated by the canonical command.
	f, _ := facts.Task(c.RetryTaskID)
	c.CanRetry = c.CanRetry && f.LastError != ""
}

// ApplyTaskDeliveryFacts shares receipt-derived status between inventory and detail.
func ApplyTaskDeliveryFacts(t *TaskView, f featurearea.TaskCommitFact) bool {
	if t == nil {
		return false
	}
	if len(f.Delivery.Members) == 0 {
		// A scalar Task can already be integrated while another repository in
		// the parent merge is still unresolved. Preserve its scalar commit view.
		if t.ID != f.TaskID || t.Status != "SUCCEEDED" || t.CommitSHA == "" || f.Pending || f.IntegrationState != "MERGED" || f.MergedCommit == "" {
			return false
		}
		t.Details.CommitReceipt = &f
		t.MergedCommit, t.Details.MergedCommit = f.MergedCommit, f.MergedCommit
		t.Details.MergedAt = f.MergedAt
		t.DevIntegrationStatus = "MERGED"
		t.DevSummary = "Dev：已合入 · " + shortCommit(f.MergedCommit)
		return true
	}
	t.Details.CommitReceipt = &f
	t.CommitActivity = ""
	t.CommitStatus, t.CommitSummary = "UNCOMMITTED", "代码：等待交付"
	if f.Delivery.State == "COMPLETED" && !f.Pending {
		t.CommitStatus, t.CommitSummary = "COMMITTED", "代码：已完成逐仓交付"
	} else if f.Delivery.State == "ABANDONED" {
		t.CommitSummary = "代码：交付已终止"
	} else {
		for _, m := range f.Delivery.Members {
			if m.State == "COMPLETED" {
				t.CommitSummary = "代码：部分仓库已提交"
				break
			}
		}
	}
	t.DevIntegrationStatus = f.IntegrationState
	switch f.IntegrationState {
	case "MERGED":
		t.DevSummary = "Dev：已全部合入"
	case "PARTIAL":
		t.DevSummary = "Dev：部分仓库已合入"
	default:
		t.DevSummary = "Dev：等待合入"
	}
	if t.CommitStatus == "COMMITTED" {
		noChanges := true
		for _, m := range f.Delivery.Members {
			if m.State != "NO_CHANGE" {
				noChanges = false
			}
		}
		if noChanges {
			t.CommitSummary = "代码：无变更，交付完成"
			t.DevSummary = "Dev：无变更，无需合入"
		}
	}
	if f.Pending && f.LastError != "" {
		t.CommitActivity = "FAILED"
		t.CommitErrorSummary = CommitReason(f.LastError)
	}
	return true
}
