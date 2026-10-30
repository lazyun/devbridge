package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
)

// TaskRunRecovery distinguishes missing native results from workflow fences.
// It is read metadata only; capabilities and canonical lifecycle stay owned
// by their existing admission protocols.
type TaskRunRecovery struct {
	RunID       string `json:"runId,omitempty"`
	Purpose     string `json:"purpose,omitempty"`
	ResultState string `json:"resultState"`
	Required    bool   `json:"required"`
	Label       string `json:"label"`
	Summary     string `json:"summary"`
}

func TaskRunRecoveryFor(v TaskView) *TaskRunRecovery {
	if v.Terminal || v.ReadOnly || v.Status != "ACTIVE" || (v.Phase != task.PhaseRecoveryRequired && (v.Recovery == nil || !v.Recovery.Required)) {
		return nil
	}
	out := &TaskRunRecovery{ResultState: "UNAVAILABLE", Label: "流程受阻，待核对", Summary: "当前流程存在未解决的阻塞，需要核对准确运行及处理结果。"}
	if r := v.CurrentReview; v.Phase == task.PhaseReviewing && v.Recovery != nil && v.Recovery.Reason == "GPT_REVIEW_AUTHORIZATION_REQUIRED" && r != nil && r.ID != "" && r.TaskID == v.ID && r.ExecutorKind == review.ExecutorUpperLLM {
		// File authorization renewal is not missing native Codex output. Leave
		// RunID empty so TaskNextStep cannot turn it into a result-recovery step.
		out.ResultState, out.Label, out.Summary = "NOT_APPLICABLE", "GPT 审查待重新授权", v.Recovery.Summary
		return out
	}
	if v.Phase == task.PhaseRecoveryRequired && v.BusinessPhaseSourceEventID == "" {
		return out
	}
	if v.BusinessPhase == task.PhaseReviewing {
		r := v.CurrentReview
		if r == nil || r.ID == "" || r.TaskID != v.ID || r.ExecutorKind.Effective() != review.ExecutorCodingAgent || r.Run == nil {
			return out
		}
		return describeRunRecovery(out, r.Run, "审查", r.Run.ID, "SCOPE_REVIEW")
	}
	e := v.CurrentExecution
	if e == nil || e.ID == "" || e.TaskDefinitionRevisionID == "" || e.TaskDefinitionRevisionID != v.Details.TaskDefinitionRevisionID {
		return out
	}
	var r *VerificationRunView
	var label, expected string
	switch v.BusinessPhase {
	case task.PhaseImplementing:
		if !v.currentImplementationExact {
			return out
		}
		r, label, expected, out.Purpose = v.ImplementationRun, "实施", e.ImplementationRunID, "IMPLEMENTATION"
	case task.PhaseVerifying:
		if !v.currentVerificationExact {
			return out
		}
		r, label, expected, out.Purpose = v.VerificationRun, "验证", e.VerificationRunID, "VERIFICATION"
	case task.PhaseDelivering:
		out.Label, out.Summary = "交付受阻", "代码交付存在未解决的阻塞，请处理交付回执及其错误。"
		return out
	default:
		return out
	}
	return describeRunRecovery(out, r, label, expected, out.Purpose)
}

func describeRunRecovery(out *TaskRunRecovery, r *VerificationRunView, label, expected, purpose string) *TaskRunRecovery {
	if r == nil || r.ID == "" || r.ID != expected {
		return out
	}
	// A typed failure of the exact current Implementation is already a known
	// scope stop. Explain its handling without granting retry/review authority
	// or turning an independently missing native result into a received result.
	if purpose == "IMPLEMENTATION" && r.FailureCode == "SCOPE_CONFLICT" && (r.TransportStatus == "FAILED" || r.TransportStatus == "INFRASTRUCTURE_FAILED") {
		out.Purpose, out.RunID, out.ResultState = purpose, r.ID, r.NativeResultState
		out.Label = "核对实施范围冲突"
		out.Summary = "实施因范围冲突停止；保留已改文件，先核对失败原因、获批范围与实际变更，再按 Core 当前资格处理。范围不变且恢复核验允许时可同版本续作；范围变化必须正式确认新方案。是否补审取决于工程判断和补审资格，不能直接重试、启动验证或验收。"
		if out.ResultState == "" {
			out.ResultState = "UNAVAILABLE"
		}
		if out.ResultState == "UNKNOWN" {
			out.Required = true
			out.Label = "找回实施结果并核对范围冲突"
			out.Summary = "实施已记录范围冲突，但最终原生结果仍未知，先核对准确运行并找回结果；" + out.Summary
		}
		return out
	}
	if r.NativeResultState == "" {
		return out
	}
	out.Purpose = purpose
	out.RunID, out.ResultState = r.ID, r.NativeResultState
	switch r.NativeResultState {
	case "UNKNOWN":
		out.Required = true
		out.Label = "待找回" + label + "结果"
		out.Summary = "尚未收到这次 Codex " + label + "运行的最终结果，需要先核对并找回该轮结果；其他已完成的运行和分析均保留。"
	case "RECEIVED":
		out.Label = label + "结果已收到，待处理"
		out.Summary = "这次 Codex " + label + "运行的最终结果已收到，请处理结果及后续流程的阻塞项。"
	case "NOT_STARTED":
		out.Label = label + "尚未开始，待处理"
		out.Summary = "这次" + label + "尚未向 Codex 派发执行，请处理启动流程后再发起。"
	default:
		out.ResultState = "UNAVAILABLE"
		out.Label = label + "结果状态待核对"
		out.Summary = "暂时无法确认这次 Codex " + label + "运行是否已返回最终结果，请先刷新并核对准确运行。"
	}
	return out
}

func ApplyTaskRunRecovery(v *TaskView) {
	v.RunRecovery = TaskRunRecoveryFor(*v)
	if r := v.RunRecovery; r != nil {
		v.PhaseDescription = r.Summary
		v.Activity.Summary = r.Summary
	}
}
