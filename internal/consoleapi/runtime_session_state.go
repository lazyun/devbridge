package consoleapi

import (
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
)

// SessionState separates observable backend evidence from the Task phase.
type SessionState struct {
	Code     string   `json:"code"`
	Label    string   `json:"label"`
	Evidence []string `json:"evidence"`
}

func sessionState(v run.Run, h executionproposal.Handoff) SessionState {
	out := SessionState{Code: "HANDOFF_UNCONFIRMED", Label: "交接证据不足", Evidence: []string{"CORE_RUN:" + string(v.Status)}}
	if terminalStatus(v.Status) {
		out.Code = "TERMINAL"
		out.Label = "已结束 · " + string(v.Status)
		return out
	}
	if v.Status == run.StatusCreated {
		out.Code = "REGISTERED"
		out.Label = "Run 已预约"
	}
	if h.State == executionproposal.HandoffPreparing || v.Status == run.StatusPreparing {
		out.Code = "PREPARING"
		out.Label = "准备中，后端受理未确认"
	}
	if h.Accepted() {
		out.Code = "SUPERVISOR_ACCEPTED"
		out.Label = "Supervisor 已接管"
		out.Evidence = append(out.Evidence, "SUPERVISOR_RECEIPT")
	}
	if h.State == executionproposal.HandoffUnknown {
		out.Code = "HANDOFF_UNKNOWN"
		out.Label = "交接结果未知"
	}
	if h.State == executionproposal.HandoffRejected {
		out.Code = "HANDOFF_REJECTED"
		out.Label = "交接被拒绝"
	}
	a := v.AgentRuntime
	if a == nil || a.Availability != "AVAILABLE" || a.Sequence == 0 || a.RunID != v.ID || a.ThreadID != v.ThreadID || a.TurnID != v.TurnID || a.ThreadID == "" || a.TurnID == "" {
		return out
	}
	out.Evidence = append(out.Evidence, "APP_SERVER_TURN_OBSERVED")
	if a.ConnectionState != "CONNECTED" {
		out.Code = "CONNECTION_UNKNOWN"
		out.Label = "连接未知，以下为最后记录"
		return out
	}
	out.Code = "BACKEND_ACCEPTED"
	out.Label = "Codex 已受理"
	if a.TurnStatus == "inProgress" {
		out.Code = "RUNNING"
		out.Label = "执行中"
	}
	if v.Status == run.StatusWaitingApproval {
		out.Code = "WAITING_RUNTIME_APPROVAL"
		out.Label = "等待运行时审批"
	}
	for _, request := range a.Requests {
		if agentruntime.OpenRequest(request) {
			out.Code = "WAITING_REQUEST"
			out.Label = "等待运行时请求"
			if request.Kind == "RUNTIME_APPROVAL" {
				out.Code = "WAITING_RUNTIME_APPROVAL"
				out.Label = "等待命令或文件权限批准"
			}
			break
		}
	}
	return out
}
