package controlapi

import (
	"context"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	"sort"
	"time"
)

func (f *Facade) attachTaskConfirmationFeed(ctx context.Context, session string, out *p.WorkspaceProjection) {
	if session == "" || out.SelectedTaskID == "" || out.SelectedWorkReadError != "" {
		return
	}
	feed := &p.TaskConfirmationFeed{AISessionID: session, ProjectID: out.DevelopmentContext.Project.ID, TaskID: out.SelectedTaskID, Availability: "UNAVAILABLE", Items: []p.TaskConfirmationItem{}}
	out.ConfirmationFeed = feed
	reader, ok := f.deps.Confirmations.(interface {
		ListTaskConfirmations(context.Context, string, string, string) ([]c.Intent, error)
	})
	if !ok {
		feed.ReadError = "当前服务无法读取任务操作列表"
		return
	}
	values, err := reader.ListTaskConfirmations(ctx, session, feed.ProjectID, feed.TaskID)
	if err != nil {
		feed.ReadError = "操作列表暂时不可读取，请刷新；原确认记录保留。"
		return
	}
	owner, err := f.deps.Tasks.Get(ctx, feed.TaskID)
	if err != nil || owner.ProjectID != feed.ProjectID {
		feed.ReadError = "任务身份暂时不可核对，请刷新。"
		return
	}
	feed.Truncated = len(values) > 20
	if feed.Truncated {
		values = values[:20]
	}
	for _, v := range values {
		latest, readErr := f.deps.Confirmations.Get(ctx, session, v.IntentID)
		if readErr != nil {
			feed.ReadError = "确认状态暂时不可核对，请刷新。"
			feed.Items = []p.TaskConfirmationItem{}
			return
		}
		v = latest
		if v.AISessionID != session || v.ProjectID != feed.ProjectID || v.IntentID == "" || v.Version < 1 || !validConfirmationContentHash(v.ContentHash) {
			feed.ReadError = "操作身份无法核对，请刷新。"
			feed.Items = []p.TaskConfirmationItem{}
			return
		}
		state := v.State
		if state == "PENDING" && !v.ExpiresAt.After(time.Now()) {
			state = "EXPIRED"
		}
		if state == "PENDING" {
			state = taskConfirmationState(v, owner)
		}
		if state == "PENDING" {
			current, e := f.confirmationActionCurrent(ctx, v)
			if e != nil {
				feed.ReadError = "任务当前操作暂不可核对"
				return
			}
			if !current {
				state = "STALE"
			}
		}
		feed.Items = append(feed.Items, p.TaskConfirmationItem{AISessionID: session, IntentID: v.IntentID, Version: v.Version, ContentHash: v.ContentHash, Purpose: string(v.Purpose), Action: v.Action, State: state, CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt})
	}
	// Apply priority after receipt readback and stale/expiry classification.
	// Cached older renderers also consume this order directly.
	sort.SliceStable(feed.Items, func(i, j int) bool {
		return confirmationFeedPriority(feed.Items[i].State) < confirmationFeedPriority(feed.Items[j].State)
	})
	feed.Availability = "AVAILABLE"
}

func taskConfirmationState(v c.Intent, owner task.Task) string {
	stale := owner.Status != task.StatusActive
	if b := v.Content.Execution; b != nil {
		stale = stale || b.TaskDefinitionRevisionID != "" && b.TaskDefinitionRevisionID != owner.CurrentDefinitionRevisionID || b.ExecutionID != "" && b.ExecutionID != owner.CurrentExecutionID || b.ReviewID != "" && b.ReviewID != owner.CurrentReviewID
	}
	if b := v.Content.Execution; b != nil && b.ExpectedTarget != nil {
		x := b.ExpectedTarget
		stale = stale || x.FeatureAreaID != owner.FeatureAreaID || x.TaskDefinitionRevisionID != owner.CurrentDefinitionRevisionID || x.ExecutionID != owner.CurrentExecutionID || x.Phase != owner.Phase
	}
	if b := v.Content.Review; b != nil && b.ReviewID != "" {
		stale = stale || b.ReviewID != owner.CurrentReviewID
	}
	if b := v.Content.Review; b != nil && b.ExpectedTarget != nil {
		x := b.ExpectedTarget
		stale = stale || x.Phase != owner.Phase || x.TaskDefinitionRevisionID != owner.CurrentDefinitionRevisionID || x.CurrentReviewID != owner.CurrentReviewID || x.CurrentExecutionID != owner.CurrentExecutionID
	}
	if stale {
		return "STALE"
	}
	return "PENDING"
}

func confirmationFeedPriority(state string) int {
	switch state {
	case "CONFIRMING":
		return 0
	case "PENDING":
		return 1
	case "UNKNOWN", "PARTIAL":
		return 2
	default:
		return 3
	}
}
