package workflowpresentation

import "github.com/lazyun/devbridge/internal/run"

// CodexContinuation describes the current interrupted Run and an already
// assembled Core capability. It never authorizes a Turn or changes old results.
type CodexContinuation struct {
	RunID   string      `json:"runId"`
	Purpose string      `json:"purpose"`
	State   string      `json:"state"`
	Label   string      `json:"label"`
	Summary string      `json:"summary"`
	Action  *Capability `json:"action,omitempty"`
}

func CodexContinuationFor(v TaskView) *CodexContinuation {
	if v.Terminal || v.ReadOnly || v.Status != "ACTIVE" || v.ActionsStatus != "AVAILABLE" {
		return nil
	}
	var r *VerificationRunView
	var purpose, action, owner string
	if e := v.CurrentExecution; e != nil && e.TaskDefinitionRevisionID != "" && e.TaskDefinitionRevisionID == v.Details.TaskDefinitionRevisionID && v.ImplementationMode == "CODING_AGENT" {
		owner = e.ID
		if e.VerificationRunID != "" {
			if v.currentVerificationExact && v.VerificationRun != nil && v.VerificationRun.ID == e.VerificationRunID {
				r, purpose, action = v.VerificationRun, "VERIFICATION", "REQUEST_REVERIFICATION"
			}
		} else if v.currentImplementationExact && v.ImplementationRun != nil && v.ImplementationRun.ID == e.ImplementationRunID {
			r, purpose, action = v.ImplementationRun, "IMPLEMENTATION", "RETRY_IMPLEMENTATION"
		}
	}
	if v.Phase.ReviewStage() || v.BusinessPhase == "REVIEWING" {
		r, purpose, action, owner = nil, "SCOPE_REVIEW", "REQUEST_TASK_REVIEW", ""
		if review := v.CurrentReview; review != nil && review.TaskID == v.ID && review.ExecutorKind == "CODING_AGENT" {
			r, owner = review.Run, review.ID
		}
	}
	if r == nil || r.ID == "" || (r.TransportStatus != "FAILED" && r.TransportStatus != "INFRASTRUCTURE_FAILED") || !codexInterrupted(r.FailureCode) {
		return nil
	}
	out := &CodexContinuation{RunID: r.ID, Purpose: purpose, State: "RECOVERY_REQUIRED", Label: "Codex 已中断，待核对后继续", Summary: "已保留原任务、已改文件和运行记录。先核对原运行结果与权限收口状态，再按当前资格继续；不会自动重发旧命令。"}
	if r.NativeResultState != "RECEIVED" && r.NativeResultState != "NOT_STARTED" {
		return out
	}
	if v.VerificationDecisions != nil && (v.VerificationDecisions.Hold != nil || v.VerificationDecisions.Completion != nil) {
		out.State, out.Label = "UNAVAILABLE", "Codex 已中断，暂不可继续"
		out.Summary = "原任务已有暂存或交付决定，请先处理该决定；已改文件和中断记录保留。"
		return out
	}
	out.State, out.Label = "UNAVAILABLE", "Codex 已中断，暂不可继续"
	out.Summary = "已保留原任务与已改文件，当前 Core 尚未允许续作。请核对恢复状态和当前操作资格。"
	for _, cap := range v.Actions {
		t := cap.Target
		if cap.Action != action || !cap.Available || t == nil || t.TaskID != v.ID || t.FeatureAreaID == "" {
			continue
		}
		if purpose == "SCOPE_REVIEW" {
			if t.ExpectedReviewRequest == nil || t.ExpectedReviewRequest.CurrentReviewID != owner {
				continue
			}
		} else if t.ExecutionID != owner || t.SubjectID != owner || t.TaskDefinitionRevisionID != v.Details.TaskDefinitionRevisionID || v.CurrentExecutionAcceptance == nil || v.CurrentExecutionAcceptance.Receipt != nil || t.DecisionID != v.CurrentExecutionAcceptance.DecisionID {
			continue
		}
		copy := cap
		out.State, out.Label, out.Action = "AVAILABLE", "Codex 已中断，可以继续", &copy
		out.Summary = "可沿原任务准备续作，保留已改文件并沿用获批范围。确认后启动新的运行，原中断记录保留；范围变化时需先修订方案。"
		if purpose == "VERIFICATION" {
			out.Summary = "可沿原任务重新验证现有实现，保留已改文件和获批检查。确认后启动新的验证运行；原中断不代表检查失败或通过。"
		} else if purpose == "SCOPE_REVIEW" {
			out.Summary = "可在原任务准备接续审查方案。确认后开始新的审查，保留原审查及其已读取的证据。"
		}
		return out
	}
	return out
}

func codexInterrupted(code string) bool {
	return code == run.CodexTurnInterruptedCode || code == "bridge_shutdown_interrupted" || code == "bridge_restart_interrupted"
}
