package consoleapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/workflowevents"
)

func (r *Reader) TaskEventStatus(ctx context.Context, project, task string) (workflowevents.Status, error) {
	if err := r.requireProject(ctx, project); err != nil {
		return workflowevents.Status{}, err
	}
	return r.control.WorkflowEventStatus(ctx, project, task)
}

type CodexHistoryPage struct {
	RunID        string `json:"runId"`
	Availability string `json:"availability"`
	Notice       string `json:"notice,omitempty"`
	codex.HistoryPage
}

func (r *Reader) CodexHistory(ctx context.Context, project, task, runID, cursor string, limit int) (CodexHistoryPage, error) {
	out := CodexHistoryPage{RunID: runID, Availability: "UNAVAILABLE", HistoryPage: codex.HistoryPage{Items: []codex.HistoryItem{}}}
	value, err := r.sessionRun(ctx, project, task, runID)
	if err != nil {
		return out, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 50 || len(cursor) > 4096 {
		return out, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Invalid Codex history page"}
	}
	out.ThreadID = value.ThreadID
	out.TurnID = value.TurnID
	if value.ThreadID == "" || value.TurnID == "" {
		out.Notice = "此 Run 没有准确的 Codex Thread/Turn；AI 文件审查不创建 Codex 会话。"
		return out, nil
	}
	out.HistoryPage, err = r.control.ReadCodexHistory(ctx, project, task, runID, cursor, limit)
	if err != nil {
		out.HistoryPage = codex.HistoryPage{ThreadID: value.ThreadID, TurnID: value.TurnID, Items: []codex.HistoryItem{}}
		out.Notice = "原 Codex 后端离线、历史不可读取或不支持分页；未恢复执行，仍可查看 Bridge 已采集记录。"
		return out, nil
	}
	out.Availability = "AVAILABLE"
	return out, nil
}
