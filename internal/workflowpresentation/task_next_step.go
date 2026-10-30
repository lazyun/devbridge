package workflowpresentation

import "github.com/lazyun/devbridge/internal/task"

// TaskNextStep orders the user's next read/analysis without changing lifecycle
// or granting execution authority. Recommended analysis is never a Core gate.
type TaskNextStep struct {
	Code                     string `json:"code"`
	Label                    string `json:"label"`
	Summary                  string `json:"summary"`
	Background               string `json:"background,omitempty"`
	Recommended              bool   `json:"recommended"`
	TaskID                   string `json:"taskId"`
	ExecutionID              string `json:"executionId"`
	ReviewID                 string `json:"reviewId,omitempty"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
	RunID                    string `json:"runId"`
	Purpose                  string `json:"purpose"`
}

func TaskNextStepFor(v TaskView) *TaskNextStep {
	if next := taskRuntimeApprovalNextStep(v); next != nil {
		return next
	}
	if next := taskAnalysisNextStepFor(v); next != nil {
		return next
	}
	r := v.RunRecovery
	if r == nil || r.RunID == "" || v.Terminal || v.ReadOnly || v.Status != "ACTIVE" {
		return nil
	}
	code := "PROCESS_RUN_RESULT"
	if r.Required {
		code = "RECOVER_RUN_RESULT"
	} else if r.ResultState == "UNAVAILABLE" {
		code = "CHECK_RUN_RESULT"
	}
	next := &TaskNextStep{Code: code, Label: r.Label, Summary: r.Summary, TaskID: v.ID, TaskDefinitionRevisionID: v.Details.TaskDefinitionRevisionID, RunID: r.RunID, Purpose: r.Purpose}
	if r.Purpose == "SCOPE_REVIEW" && v.CurrentReview != nil {
		next.ReviewID = v.CurrentReview.ID
	} else if v.CurrentExecution != nil && (r.Purpose == "IMPLEMENTATION" || r.Purpose == "VERIFICATION") {
		next.ExecutionID = v.CurrentExecution.ID
	} else {
		return nil
	}
	return next
}

func taskRuntimeApprovalNextStep(v TaskView) *TaskNextStep {
	a := v.Activity.Runtime
	if v.Terminal || v.ReadOnly || v.Status != "ACTIVE" || a == nil || a.Availability != "AVAILABLE" || a.TaskID != v.ID || a.TaskDefinitionRevisionID != v.Details.TaskDefinitionRevisionID || a.ConnectionState != "CONNECTED" {
		return nil
	}
	executionID, reviewID, purpose := "", "", ""
	if v.CurrentExecution != nil && a.OperationKind == "EXECUTION" && a.OperationID == v.CurrentExecution.ID {
		executionID = v.CurrentExecution.ID
		if v.Phase == task.PhaseImplementing && a.RunID == v.CurrentExecution.ImplementationRunID {
			purpose = "IMPLEMENTATION"
		}
		if v.Phase == task.PhaseVerifying && a.RunID == v.CurrentExecution.VerificationRunID {
			purpose = "VERIFICATION"
		}
	}
	if v.CurrentReview != nil && v.Phase.ReviewStage() && a.OperationKind == "REVIEW" && a.OperationID == v.CurrentReview.ID && v.CurrentReview.Run != nil && a.RunID == v.CurrentReview.Run.ID {
		reviewID = v.CurrentReview.ID
		purpose = "SCOPE_REVIEW"
	}
	if purpose == "" {
		return nil
	}
	for _, q := range a.Requests {
		if q.Kind != "RUNTIME_APPROVAL" || q.State != "PENDING" || q.Identity != a.Identity || q.ID == "" {
			continue
		}
		label := "等待运行权限批准"
		if q.Method == "item/commandExecution/requestApproval" {
			label = "等待命令批准"
		}
		if q.Method == "item/fileChange/requestApproval" {
			label = "等待文件修改批准"
		}
		return &TaskNextStep{Code: "RESOLVE_RUNTIME_APPROVAL", Label: label, Summary: "Codex 已暂停，等待你核对并批准本条原生权限请求。", TaskID: v.ID, ExecutionID: executionID, ReviewID: reviewID, TaskDefinitionRevisionID: v.Details.TaskDefinitionRevisionID, RunID: a.RunID, Purpose: purpose}
	}
	return nil
}

func taskAnalysisNextStepFor(v TaskView) *TaskNextStep {
	if v.Terminal || v.ReadOnly || v.Status != "ACTIVE" || v.Phase.ReviewStage() {
		return nil
	}
	e := v.CurrentExecution
	if e == nil || e.ID == "" || v.ID == "" || v.Details.TaskDefinitionRevisionID == "" || e.TaskDefinitionRevisionID != v.Details.TaskDefinitionRevisionID {
		return nil
	}
	terminal := func(r *VerificationRunView) bool {
		if r == nil {
			return false
		}
		switch r.TransportStatus {
		case "SUCCEEDED", "FAILED", "INFRASTRUCTURE_FAILED", "CANCELED":
			return true
		}
		return false
	}
	makeStep := func(r *VerificationRunView, purpose, label string, recommended bool) *TaskNextStep {
		return &TaskNextStep{Code: "ANALYZE_RUN_OUTPUT", Label: "待 GPT 分析" + label + "结果", Summary: "先让 GPT 分析本次" + label + "输出与实际证据，再决定后续处理。", Recommended: recommended, TaskID: v.ID, ExecutionID: e.ID, TaskDefinitionRevisionID: e.TaskDefinitionRevisionID, RunID: r.ID, Purpose: purpose}
	}
	verification := v.VerificationRun
	if v.currentVerificationExact && terminal(verification) && verification.ID == e.VerificationRunID && verification.AnalysisState == "REQUIRED" && (v.Phase == task.PhaseVerifying || v.Phase == task.PhaseRecoveryRequired && v.BusinessPhase == task.PhaseVerifying) {
		return makeStep(verification, "VERIFICATION", "验证", false)
	}
	implementation := v.ImplementationRun
	if !v.currentVerificationExact || !v.currentImplementationExact || v.Phase != task.PhaseRecoveryRequired || v.BusinessPhase != task.PhaseVerifying || !terminal(verification) || verification.ID == "" || verification.ID != e.VerificationRunID || verification.AnalysisState != "NOT_REQUIRED" || verification.TransportStatus == "SUCCEEDED" {
		return nil
	}
	if !terminal(implementation) || implementation.ID == "" || implementation.ID != e.ImplementationRunID || implementation.TransportStatus != "SUCCEEDED" || implementation.AnalysisState != "OPTIONAL" {
		return nil
	}
	next := makeStep(implementation, "IMPLEMENTATION", "实施", true)
	next.Background = "验证运行已中断；分析实施结果后，再决定如何处理验证。"
	return next
}
