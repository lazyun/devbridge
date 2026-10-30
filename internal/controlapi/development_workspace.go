package controlapi

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

type workspaceSelection struct {
	area, task              string
	projectInspectionTaskID string
	taskExplicit            bool
}
type workspaceSelections struct {
	sync.Mutex
	values map[string]workspaceSelection
}

type inventoryCommitReader interface {
	CommitInventoryCapability(context.Context, string, string) (featurearea.CommitCapability, error)
	CommitInventoryFacts(context.Context, string, string) (featurearea.CommitFacts, error)
}

// DevelopmentWorkspaceForSession is the shared Project inventory and navigation
// read. Selection is a per-request read; no server selection memory or workflow command is changed.
func (f *Facade) DevelopmentWorkspaceForSession(ctx context.Context, sessionID, areaID, taskID string) (p.WorkspaceProjection, error) {
	return f.developmentWorkspaceForSession(ctx, sessionID, areaID, taskID, false)
}

func (f *Facade) developmentWorkspaceForSession(ctx context.Context, sessionID, areaID, taskID string, exact bool) (p.WorkspaceProjection, error) {
	development, err := f.DevelopmentContextPresentationForSession(ctx, sessionID)
	if err != nil {
		return p.WorkspaceProjection{}, err
	}
	out := p.WorkspaceProjection{SchemaVersion: "1", PresentationID: p.WorkspaceIdentity(sessionID, development.Project.ID), DevelopmentContext: development, FeatureAreas: []p.FeatureAreaNavigationItem{}, Tasks: []p.TaskNavigationItem{}, PendingDecisions: []p.PendingDecision{}, PendingConfirmations: []p.PendingConfirmation{}}
	var areas []featurearea.FeatureArea
	if reader, ok := f.deps.FeatureAreas.(interface {
		ListInventoryForSession(context.Context, string) ([]featurearea.FeatureArea, error)
	}); ok {
		areas, err = reader.ListInventoryForSession(ctx, sessionID)
	} else {
		areas, err = f.deps.FeatureAreas.ListForSession(ctx, sessionID)
	}
	if err != nil {
		return out, MapError(err)
	}
	tasks, err := f.deps.Tasks.List(ctx)
	if err != nil {
		return out, MapError(err)
	}
	// An explicit historical target must fail closed, never select newer work.
	if taskID != "" {
		selected, readErr := f.deps.Tasks.Get(ctx, taskID)
		if readErr != nil || selected.ProjectID != development.Project.ID || (areaID != "" && selected.FeatureAreaID != areaID) {
			return out, notFound()
		}
		areaID = selected.FeatureAreaID
	}
	if areaID != "" {
		found := false
		for _, area := range areas {
			if area.ID == areaID && area.ProjectID == development.Project.ID {
				found = true
			}
		}
		if !found {
			selected, readErr := (historicalFeatureAreaRead{f.deps.FeatureAreas}).GetForSession(ctx, sessionID, areaID)
			if readErr != nil || selected.ProjectID != development.Project.ID {
				return out, notFound()
			}
			areas = append(areas, selected)
		}
	}
	inspectionTaskID := taskID
	byArea := make(map[string][]task.Task)
	var selectedProjectInspection *task.Task
	for _, t := range tasks {
		if t.ProjectID != development.Project.ID {
			continue
		}
		if t.FeatureAreaID != "" {
			byArea[t.FeatureAreaID] = append(byArea[t.FeatureAreaID], t)
			continue
		}
		work := workspaceWork(t)
		if t.ID == inspectionTaskID && work.Type == task.TypeInspection && (t.Scope == task.ScopeProject || work.Scope == task.ScopeProject) {
			value := t
			selectedProjectInspection = &value
		}
	}
	for _, a := range areas {
		if a.ProjectID != development.Project.ID || (a.ID != areaID && (a.Status == featurearea.StatusRetired || a.Status == featurearea.StatusDecomposed)) {
			continue
		}
		item := p.FeatureAreaNavigationItem{ID: a.ID, Name: a.Title, Status: string(a.Status), Availability: p.Availability(string(a.Status)), UpdatedAt: a.UpdatedAt, Retirement: a.Retirement, CommitStatus: "UNAVAILABLE", Product: p.NewFeatureAreaProductProjection(a.ID, a.Title, string(a.Status), a.LegacyReadOnly)}
		projected := p.Projection{LifecycleStatus: string(a.Status)}
		authoritativeLatestTaskID := ""
		canCreateTask := false
		if f.deps.WorkflowProgression != nil {
			g, e := f.deps.WorkflowProgression.Read(ctx, development.Project.ID, a.ID)
			if e != nil {
				item.ReadError = "Workflow read failed"
				item.ReviewStatus = "UNAVAILABLE"
			} else {
				canCreateTask = g.CanCreateTask()
				projected.NextAction = g.NextAction
				item.ReviewStatus = g.LatestReviewStatus
				if g.TaskPhase == task.PhaseReviewApprovalPending {
					item.ReviewStatus = g.CurrentState
				}
				if g.CurrentTask != nil {
					current := *g.CurrentTask
					item.Product.CurrentTask = &current
					item.Product.TaskPhase = string(current.Phase)
					item.CurrentTaskID = g.CurrentTask.ID
					if a.Status == featurearea.StatusActive && (g.TaskPhase == task.PhaseImplementing || g.TaskPhase == task.PhaseVerifying) {
						for _, t := range byArea[a.ID] {
							if t.ID == g.CurrentTask.ID && workspaceWork(t).Type == task.TypeDevelopment {
								item.RunningDevelopmentTaskID = t.ID
							}
						}
					}
				}
				if g.LatestTask != nil {
					latest := *g.LatestTask
					item.Product.LatestTask = &latest
					authoritativeLatestTaskID = g.LatestTask.ID
				}
			}
		}
		var areaCommitFacts featurearea.CommitFacts
		if reader, ok := f.deps.WorkflowProgression.(inventoryCommitReader); ok {
			cap, e := reader.CommitInventoryCapability(ctx, development.Project.ID, a.ID)
			if e != nil {
				item.ReadError = "Commit read failed"
			} else {
				projected.Commit = &cap
				facts, e := reader.CommitInventoryFacts(ctx, development.Project.ID, a.ID)
				if e != nil {
					item.ReadError = "Commit facts read failed"
					cap.CanRetry = false
					cap.CanCommit = false
				} else {
					areaCommitFacts = facts
					p.ApplyCommitFacts(&projected, facts)
				}
				item.CommitStatus = cap.CommitStatus
				item.MergedCommit = cap.MergedCommit
				if cap.CanRetry {
					item.CurrentTaskID = cap.RetryTaskID
				}
			}
		}
		var latest *task.Task
		for _, t := range byArea[a.ID] {
			name := t.Title
			if name == "" {
				name = t.WorkSpec.Goal
			}
			if name == "" {
				name = t.ID
			}
			out.Tasks = append(out.Tasks, p.TaskNavigationItem{ID: t.ID, FeatureAreaID: a.ID, Name: name, Status: string(t.Status), Work: workspaceWork(t), UpdatedAt: t.UpdatedAt})
			if latest == nil || t.UpdatedAt.After(latest.UpdatedAt) || t.UpdatedAt.Equal(latest.UpdatedAt) && t.ID < latest.ID {
				v := t
				latest = &v
			}
			if t.UpdatedAt.After(item.UpdatedAt) {
				item.UpdatedAt = t.UpdatedAt
			}
		}
		if authoritativeLatestTaskID != "" {
			for _, t := range byArea[a.ID] {
				if t.ID == authoritativeLatestTaskID {
					v := t
					latest = &v
					break
				}
			}
		}
		if latest != nil {
			item.LatestTaskID = latest.ID
			item.LatestTaskTitle = latest.Title
			if item.LatestTaskTitle == "" {
				item.LatestTaskTitle = latest.WorkSpec.Goal
			}
			if item.LatestTaskTitle == "" {
				item.LatestTaskTitle = latest.ID
			}
			item.LatestTaskPhase = string(latest.Phase)
			item.LatestTaskStatus = string(latest.Status)
			latestView := p.BuildTask(*latest, nil, nil, time.Time{})
			if fact, ok := areaCommitFacts.Task(latest.ID); ok {
				p.ApplyTaskDeliveryFacts(&latestView, fact)
			}
			item.LatestTaskCommitStatus = latestView.CommitStatus
			item.LatestTaskDevIntegrationStatus = latestView.DevIntegrationStatus
			item.LatestTaskCommitSHA = latestView.CommitSHA
			item.LatestTaskMergedCommit = latestView.MergedCommit
		}
		for _, t := range byArea[a.ID] {
			if t.ID == item.CurrentTaskID {
				v := p.BuildTask(t, nil, nil, time.Time{})
				if fact, ok := areaCommitFacts.Task(t.ID); ok {
					p.ApplyTaskDeliveryFacts(&v, fact)
				}
				item.CurrentTaskTitle = t.Title
				if item.CurrentTaskTitle == "" {
					item.CurrentTaskTitle = t.WorkSpec.Goal
				}
				if item.CurrentTaskTitle == "" {
					item.CurrentTaskTitle = t.ID
				}
				item.CurrentTaskStatus = string(t.Status)
				item.CurrentTaskPhase = string(t.Phase)
				item.CurrentTaskCommitStatus = v.CommitStatus
				item.CurrentTaskDevIntegrationStatus = v.DevIntegrationStatus
				item.CurrentTaskCommitSHA = v.CommitSHA
				item.CurrentTaskMergedCommit = v.MergedCommit
			}
		}
		item.CurrentAction = p.CurrentAction(projected)
		if canCreateTask {
			item.Product.AvailableActions = append(item.Product.AvailableActions, "CREATE_TASK")
		}
		if item.CurrentAction.Code != "" && item.CurrentAction.Code != "NONE" {
			if item.CurrentAction.Required {
				action := item.CurrentAction
				item.Product.RequiredAction = &action
			} else if item.CurrentAction.Code != "REFRESH" {
				item.Product.AvailableActions = append(item.Product.AvailableActions, item.CurrentAction.Code)
			}
		}
		if item.Product.CurrentTask != nil {
			operation, operationErr := f.FeatureAreaAgentOperationState(ctx, sessionID, a.ID)
			if operationErr != nil {
				item.Product.TechnicalDetails.Diagnostic = "OPERATION_ACTIVITY_READ_FAILED"
				item.Product.TechnicalDetails.ReadError = "Agent operation activity unavailable"
			} else if operation.Claim != nil && operation.Claim.OperationID != "" && operation.Claim.RunID != "" && (operation.Status == "BUSY" || operation.Status == "RECOVERY_REQUIRED") {
				item.Product.OperationActivity = &p.ProductOperationActivity{State: operation.Status, Message: operation.Message, Kind: string(operation.Claim.Kind), OperationID: operation.Claim.OperationID, RunID: operation.Claim.RunID}
			}
		}
		if item.ReadError != "" {
			item.RunningDevelopmentTaskID = ""
			item.CurrentAction.Code = "REFRESH"
			item.CurrentAction.Required = false
			item.CurrentAction.Message = "读取失败，请刷新"
			item.Product.TechnicalDetails.ReadError = item.ReadError
			item.Product.RequiredAction = nil
		}
		out.FeatureAreas = append(out.FeatureAreas, item)
	}
	// Project inspections are explicit Project envelopes, never FeatureArea
	// navigation. They must bypass a remembered FeatureArea selection so a
	// previous semantic Task cannot replace the exact inspection operation.
	if selectedProjectInspection != nil {
		out.SelectedFeatureAreaID = ""
		out.SelectedTaskID = ""
		out.SelectedWork = f.workspaceSelectedOperation(ctx, sessionID, *selectedProjectInspection)
		out.RefreshedAt = time.Now().UTC()
		f.attachProjectPendingState(ctx, sessionID, development.Project.ID, &out)
		return out, nil
	}

	if exact && areaID == "" && taskID == "" {
		// Keep all Project inventory. Selecting and then clearing would already
		// filter Tasks to an arbitrary FeatureArea and read its selected detail.
		out.RefreshedAt = time.Now().UTC()
		f.attachProjectPendingState(ctx, sessionID, development.Project.ID, &out)
		return out, nil
	}
	p.SelectWorkspace(&out, areaID, taskID)
	if exact && taskID == "" {
		// An explicitly selected Area overview must stay an overview. The
		// legacy selector may recommend its current Task for other callers.
		out.SelectedTaskID = ""
		out.SelectedWork = nil
	}
	if areaID != "" && out.SelectedFeatureAreaID != areaID || taskID != "" && out.SelectedTaskID != taskID {
		return out, notFound()
	}
	if out.SelectedFeatureAreaID != "" {
		var v p.Projection
		if out.SelectedTaskID != "" {
			v, err = f.TaskPresentationForSession(ctx, sessionID, out.SelectedFeatureAreaID, out.SelectedTaskID)
		} else {
			v, err = f.FeatureAreaPresentationForSession(ctx, sessionID, out.SelectedFeatureAreaID)
		}
		if err != nil {
			out.SelectedWorkReadError = "当前工作详情暂时不可用"
			// A detail projection failure must not turn a stale navigation item
			// into an operation. When the exact owner still has an authoritative
			// agent-execution projection, retain only that reducer output.
			if out.SelectedTaskID != "" {
				for _, candidate := range tasks {
					if candidate.ID == out.SelectedTaskID {
						out.SelectedWork = f.workspaceSelectedOperation(ctx, sessionID, candidate)
						break
					}
				}
			}
		} else {
			out.Presentation = &v
			// Navigation keeps the selected semantic Task ID. The selected work
			// payload is the exact current operation owned by that Task, never a
			// copy of its navigation DTO.
			if v.TaskView != nil {
				out.SelectedWork = selectedOperationWithRecovery(p.SelectedOperationFromTaskView(*v.TaskView), v)
			}
		}
	}
	out.RefreshedAt = time.Now().UTC()
	f.attachTaskConfirmationFeed(ctx, sessionID, &out)
	f.attachProjectPendingState(ctx, sessionID, development.Project.ID, &out)
	return out, nil
}

func (f *Facade) attachProjectPendingState(ctx context.Context, sessionID, projectID string, out *p.WorkspaceProjection) {
	f.attachProjectPendingDecisions(ctx, sessionID, out)
	f.attachProjectPendingConfirmations(ctx, sessionID, projectID, out)
}

type projectConfirmationReader interface {
	ListProject(context.Context, string, string) ([]confirmation.Intent, error)
}

func (f *Facade) attachProjectPendingConfirmations(ctx context.Context, sessionID, projectID string, out *p.WorkspaceProjection) {
	reader, ok := f.deps.Confirmations.(projectConfirmationReader)
	if !ok {
		return
	}
	values, err := reader.ListProject(ctx, sessionID, projectID)
	if err != nil {
		out.PendingConfirmationsReadError = "Pending confirmations temporarily unavailable; refresh before creating work."
		return
	}
	for _, value := range values {
		if value.Action != "CREATE_TASK_AND_REVIEW" || value.Commit == nil || value.State == "COMPLETED" || value.State == "REJECTED" {
			continue
		}
		item := p.PendingConfirmation{IntentID: value.IntentID, RequestID: value.Commit.RequestID, Purpose: string(value.Purpose), Action: value.Action, State: value.State, Outcome: string(confirmation.Unknown)}
		if value.Submission != nil {
			item.RequestID = value.Submission.Request.RequestID
			item.Outcome = string(value.Submission.Outcome)
			item.OutcomeRecorded = value.Submission.Recorded
			item.NextAction = string(value.Submission.NextAction)
		}
		for _, step := range value.Steps {
			if step.FeatureAreaID != "" {
				item.FeatureAreaID = step.FeatureAreaID
			}
			if step.TaskID != "" {
				item.TaskID = step.TaskID
			}
			if step.ReviewID != "" {
				item.ReviewID = step.ReviewID
			}
			if step.RunID != "" {
				item.RunID = step.RunID
			}
		}
		out.PendingConfirmations = append(out.PendingConfirmations, item)
	}
	sort.Slice(out.PendingConfirmations, func(i, j int) bool {
		return out.PendingConfirmations[i].IntentID < out.PendingConfirmations[j].IntentID
	})
	for i := range out.FeatureAreas {
		for _, pending := range out.PendingConfirmations {
			if pending.FeatureAreaID == out.FeatureAreas[i].ID || pending.FeatureAreaID == "" && confirmationOwnsArea(values, pending.IntentID, out.FeatureAreas[i].ID) {
				out.FeatureAreas[i].CurrentAction.Code = "READ_CONFIRMATION_RESULT"
				out.FeatureAreas[i].CurrentAction.Required = true
				out.FeatureAreas[i].CurrentAction.Message = "创建 Task 并开始审查的原确认结果未闭合；读取原请求结果后再创建工作"
				action := out.FeatureAreas[i].CurrentAction
				out.FeatureAreas[i].Product.RequiredAction = &action
				break
			}
		}
	}
}

func confirmationOwnsArea(values []confirmation.Intent, intentID, areaID string) bool {
	for _, value := range values {
		if value.IntentID == intentID && value.Content.Task != nil && value.Content.Task.FeatureAreaID == areaID {
			return true
		}
	}
	return false
}

func (f *Facade) attachProjectPendingDecisions(ctx context.Context, sessionID string, out *p.WorkspaceProjection) {
	// Lightweight inventory consumers (including compatibility test doubles)
	// may not expose the complete authoritative read graph. Do not manufacture
	// decisions from their partial state; production composition supplies all
	// of these services and therefore receives the Project inventory below.
	if f.deps.Reviews == nil || f.deps.ExecutionProposals == nil || f.deps.Runs == nil || f.deps.Approvals == nil || f.deps.WorkflowProgression == nil {
		return
	}
	decisions, err := f.ProjectPendingDecisionsForSession(ctx, sessionID)
	if err != nil {
		var partial *projectPendingDecisionsPartialError
		if errors.As(err, &partial) {
			// The inventory remains authoritative for the individually readable
			// items. The warning is informational; clients use the non-nil list
			// as the aggregation authority and must not fall back to selected
			// detail actions.
			out.PendingDecisions = decisions
			out.PendingDecisionsReadError = partial.Error()
			return
		}
		out.PendingDecisions = []p.PendingDecision{}
		out.PendingDecisionsReadError = "Pending decisions temporarily unavailable; refresh before resolving."
		return
	}
	out.PendingDecisions = decisions
}

func selectedOperationWithRecovery(op *p.SelectedOperation, value p.Projection) *p.SelectedOperation {
	if op == nil {
		return nil
	}
	if value.Recovery != nil {
		op.RecoveryRequired = value.Recovery.Required
	} else if value.TaskStages != nil && value.TaskStages.Recovery != nil {
		op.RecoveryRequired = value.TaskStages.Recovery.Required
	}
	return op
}

func (f *Facade) workspaceSelectedOperation(ctx context.Context, sessionID string, owner task.Task) *p.SelectedOperation {
	if f.deps.AgentExecutions == nil {
		return nil
	}
	if err := f.EnrichTaskExecution(ctx, &owner); err != nil || owner.Work.ExecutionID == "" {
		return nil
	}
	v, err := f.GetAgentExecutionForSession(ctx, sessionID, owner.Work.ExecutionID)
	if err != nil || v.TaskID != owner.ID {
		return nil
	}
	return p.SelectedOperationFromAgentExecution(v)
}

// Inventory includes non-Development work. Legacy Tasks without a type are
// Development, but an explicit work type must never be lost during projection.
func workspaceWork(t task.Task) task.Work {
	w := t.Work
	if w.Type == "" {
		w.Type = t.Type
	}
	if w.Type == "" {
		w.Type = task.TypeDevelopment
	}
	return w
}

// ExactDevelopmentWorkspaceForSession preserves an entry's explicitly empty
// selection as well as exact historical selections. Navigation stays local.
func (f *Facade) ExactDevelopmentWorkspaceForSession(ctx context.Context, sessionID, areaID, taskID string) (p.WorkspaceProjection, error) {
	return f.developmentWorkspaceForSession(ctx, sessionID, areaID, taskID, true)
}
