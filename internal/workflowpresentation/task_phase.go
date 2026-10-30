package workflowpresentation

import "github.com/lazyun/devbridge/internal/task"

// TaskPhaseProgress is a mechanical label mapping of durable Task authority.
// Neither child outcomes, decision presence nor history length are inputs.
// A recovery without durable origin has no known business stage. The caller
// may supply a verified originating phase, never infer it from child history.
func TaskPhaseProgress(phase task.Phase, status task.Status) []PhaseProgress {
	index := map[task.Phase]int{task.PhaseReviewRequired: 0, task.PhaseReviewing: 0, task.PhaseReviewApprovalPending: 1, task.PhaseImplementationReady: 2, task.PhaseImplementationPreparing: 2, task.PhaseNoRunSuccessorRequired: 2, task.PhaseImplementing: 2, task.PhaseVerificationReady: 3, task.PhaseVerifying: 3, task.PhaseAcceptancePending: 4, task.PhaseDelivering: 4}[phase]
	labels := []string{"REVIEW", "APPROVED", "IMPLEMENTATION", "VERIFICATION", "DONE"}
	out := make([]PhaseProgress, 0, len(labels))
	for i, label := range labels {
		state := "PENDING"
		if i < index && phase != task.PhaseRecoveryRequired || status == task.StatusSucceeded {
			state = "COMPLETE"
		} else if i == index && phase.Valid() && phase != task.PhaseRecoveryRequired {
			state = "CURRENT"
		}
		if status != task.StatusSucceeded && i == index && (phase == task.PhaseReviewApprovalPending || phase == task.PhaseImplementationReady || phase == task.PhaseVerificationReady || phase == task.PhaseAcceptancePending) {
			state = "PENDING"
		}
		if i == index && phase != task.PhaseRecoveryRequired && (status == task.StatusCanceled || status == task.StatusFailed) {
			state = string(status)
		}
		out = append(out, PhaseProgress{Phase: label, Status: state})
	}
	return out
}

// TaskPhaseLabel is shared display vocabulary; terminal status does not rewrite durable phase.
func TaskPhaseLabel(phase task.Phase, status task.Status) (string, string) {
	switch status {
	case task.StatusSucceeded:
		return "Done", "已完成"
	case task.StatusCanceled:
		return "Canceled", "已取消"
	case task.StatusFailed:
		return "Failed", "失败"
	}
	switch phase {
	case task.PhaseReviewRequired:
		return "Review", "待审查"
	case task.PhaseReviewing:
		return "Review", "审查中"
	case task.PhaseReviewApprovalPending:
		return "Review complete", "等待审批"
	case task.PhaseImplementationReady:
		return "Approved", "等待执行"
	case task.PhaseNoRunSuccessorRequired:
		return "Successor required", "前一执行已在创建 Run 前取消，需要准备后继执行"
	case task.PhaseImplementationPreparing:
		return "Preparing implementation", "正在准备后继执行"
	case task.PhaseImplementing:
		return "Implementation", "执行中"
	case task.PhaseVerificationReady:
		return "Verification", "待验证"
	case task.PhaseVerifying:
		return "Verification", "验证中"
	case task.PhaseAcceptancePending:
		return "Verification complete", "等待验收"
	case task.PhaseDelivering:
		return "Delivering", "交付中"
	case task.PhaseRecoveryRequired:
		return "Recovery required", "需要恢复"
	}
	return string(phase), ""
}
