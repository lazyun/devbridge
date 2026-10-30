package controlapi

import (
	"context"
	"errors"
	"sort"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

type PanelConfirmationItem struct {
	p.TaskConfirmationItem
	TaskID    string `json:"taskId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Title     string `json:"title,omitempty"`
}

type PanelConfirmationFeed struct {
	AISessionID  string                  `json:"aiSessionId"`
	ProjectID    string                  `json:"projectId"`
	Scope        string                  `json:"scope"`
	Availability string                  `json:"availability"`
	FocusTaskID  string                  `json:"focusTaskId,omitempty"`
	Truncated    bool                    `json:"truncated"`
	Items        []PanelConfirmationItem `json:"items"`
}

// PanelConfirmationFeed reads only this caller's existing Session. It includes
// proposals before any Project, FeatureArea or Task has been created, without
// turning a displayed proposal into a navigation target or execution authority.
func (f *Facade) PanelConfirmationFeed(ctx context.Context, sessionID string) (PanelConfirmationFeed, error) {
	return f.PanelConfirmationFeedForTask(ctx, sessionID, "")
}

func (f *Facade) PanelConfirmationFeedForTask(ctx context.Context, sessionID, taskID string, requests ...string) (PanelConfirmationFeed, error) {
	out := PanelConfirmationFeed{AISessionID: sessionID, FocusTaskID: taskID, Scope: "SESSION", Availability: "AVAILABLE", Items: []PanelConfirmationItem{}}
	session, err := f.confirmationSession(ctx, sessionID)
	if err != nil || session.AISessionID != sessionID {
		return out, notFound()
	}
	projectID := ""
	if session.BoundProjectID != nil {
		projectID = *session.BoundProjectID
	}
	out.ProjectID = projectID
	reader, ok := f.deps.Confirmations.(interface {
		ListSessionConfirmations(context.Context, string) ([]c.Intent, error)
	})
	if !ok {
		return out, errors.New("session confirmation feed unavailable")
	}
	if taskID != "" {
		owner, readErr := f.deps.Tasks.Get(ctx, taskID)
		if readErr != nil || owner.ProjectID != projectID {
			return out, notFound()
		}
	}
	var values []c.Intent
	if focused, ok := f.deps.Confirmations.(interface {
		ListSessionConfirmationsForTask(context.Context, string, string, ...string) ([]c.Intent, error)
	}); ok && (taskID != "" || len(requests) > 0) {
		values, err = focused.ListSessionConfirmationsForTask(ctx, sessionID, taskID, requests...)
	} else {
		values, err = reader.ListSessionConfirmations(ctx, sessionID)
	}
	if err != nil {
		return out, MapError(err)
	}
	out.Truncated = len(values) > 20
	if out.Truncated {
		values = values[:20]
	}
	for _, v := range values {
		if v.AISessionID != sessionID || v.ProjectID != "" && v.ProjectID != projectID || v.IntentID == "" || v.Version < 1 || !validConfirmationContentHash(v.ContentHash) {
			return out, notFound()
		}
		v, err = f.deps.Confirmations.Get(ctx, sessionID, v.IntentID)
		if err != nil {
			return out, MapError(err)
		}
		if v.AISessionID != sessionID || v.ProjectID != "" && v.ProjectID != projectID || v.IntentID == "" || v.Version < 1 || !validConfirmationContentHash(v.ContentHash) {
			return out, notFound()
		}
		state := v.State
		if state == "PENDING" && !v.ExpiresAt.After(time.Now()) {
			state = "EXPIRED"
		}
		if state == "PENDING" && v.Purpose == c.ProjectSetup && v.ProjectID == "" && projectID != "" {
			state = "STALE"
		}
		taskID := ""
		if b := v.Content.Execution; b != nil {
			taskID = b.TaskID
		}
		if b := v.Content.Review; b != nil {
			taskID = b.TaskID
		}
		if taskID != "" {
			owner, readErr := f.deps.Tasks.Get(ctx, taskID)
			if readErr != nil || owner.ProjectID != projectID {
				return out, notFound()
			}
			if state == "PENDING" {
				state = taskConfirmationState(v, owner)
			}
		}
		if state == "PENDING" {
			current, e := f.confirmationActionCurrent(ctx, v)
			if e != nil {
				return out, e
			}
			if !current {
				state = "STALE"
			}
		}
		title := ""
		if v.Content.Task != nil {
			title = v.Content.Task.Title
		}
		if v.Content.FeatureArea != nil {
			title = v.Content.FeatureArea.Title
		}
		out.Items = append(out.Items, PanelConfirmationItem{TaskConfirmationItem: p.TaskConfirmationItem{AISessionID: sessionID, IntentID: v.IntentID, Version: v.Version, ContentHash: v.ContentHash, Purpose: string(v.Purpose), Action: v.Action, State: state, CreatedAt: v.CreatedAt, ExpiresAt: v.ExpiresAt}, TaskID: taskID, RequestID: v.Content.RequestID, Title: title})
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		return confirmationFeedPriority(out.Items[i].State) < confirmationFeedPriority(out.Items[j].State)
	})
	return out, nil
}
