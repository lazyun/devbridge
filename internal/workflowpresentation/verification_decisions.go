package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

type VerificationDecisions struct {
	ContinuationConfirmed bool                           `json:"continuationConfirmed"`
	TaskID                string                         `json:"taskId"`
	ExecutionID           string                         `json:"executionId"`
	RevisionID            string                         `json:"taskDefinitionRevisionId"`
	Hold                  *task.Hold                     `json:"hold,omitempty"`
	Completion            *delivery.TaskCompletionStatus `json:"completion,omitempty"`
	Choices               []VerificationChoice           `json:"choices"`
}
type VerificationChoice struct {
	Action    string `json:"action"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func VerificationCompletionReady(v TaskView) bool {
	if v.CurrentVerificationSpec == nil {
		return false
	}
	spec := v.CurrentVerificationSpec
	if spec.Mode == task.VerificationModeNone {
		return spec.Waiver != nil && spec.Waiver.Source == task.VerificationWaiverUser && spec.Waiver.Reason != ""
	}
	r := v.VerificationReport
	if v.VerificationRun == nil || v.VerificationRun.TransportStatus != "SUCCEEDED" || r == nil || r.Availability != run.VerificationReportAvailable || !run.VerificationReportMatchesSpec(*r, *spec) {
		return false
	}
	return run.VerificationAssessmentPassed(*r, *spec)
}

func ApplyVerificationDecisions(v *TaskView) {
	d := v.VerificationDecisions
	if d == nil {
		return
	}
	if d.Hold != nil {
		v.PhaseDescription = "任务已暂存，等待继续"
		v.Activity.Status = "DEFERRED"
		v.Activity.Summary = d.Hold.Note
		v.OverallState = "DEFERRED"
		v.NextStep = nil
	}
	if d.Completion != nil {
		switch d.Completion.State {
		case "COMPLETED":
			v.PhaseDescription = "任务已完成并合入"
		case "REPLAN_REQUIRED":
			v.PhaseDescription = "等待确认新的合入计划"
			v.Activity.Summary = "原计划已释放，请按当前开发分支准备并确认新的合入计划。"
			v.NextStep = nil
		case "BLOCKED":
			v.PhaseDescription = "合入受阻"
			v.Activity.Summary = d.Completion.Error
			v.NextStep = nil
		default:
			v.PhaseDescription = "验收已确认，正在提交并合入"
			v.NextStep = nil
		}
	}
}
