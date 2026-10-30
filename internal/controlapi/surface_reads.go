package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"sort"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// CompactWorkspaceForSession reads only authoritative current pointers. The
// Core guidance reducer retains its generation fence; independent runtime and
// capability reads remain separate observations, never an atomic snapshot.
func (f *Facade) CompactWorkspaceForSession(ctx context.Context, sessionID, areaID, taskID string) (p.Projection, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return p.Projection{}, err
	}
	if taskID != "" {
		t, e := f.deps.Tasks.Get(ctx, taskID)
		if e != nil || t.ProjectID != projectID || (areaID != "" && areaID != t.FeatureAreaID) {
			return p.Projection{}, notFound()
		}
		areaID = t.FeatureAreaID
	}
	if taskID == "" && areaID == "" {
		// An explicitly Project-only read is not a request to select a unique
		// or recent FeatureArea/Task. Inventory is a separate navigation read.
		return p.Projection{SchemaVersion: "1", CurrentState: "PROJECT_READY", NextAction: wp.Action{Code: "WAIT_FOR_USER_TASK", Message: "Select exact work when needed."}, Capabilities: []p.Capability{}, Blockers: []string{}, Details: p.Details{ProjectID: projectID}}, nil
	}
	out := p.Projection{SchemaVersion: "1", Capabilities: []p.Capability{}, Blockers: []string{}, Details: p.Details{ProjectID: projectID, FeatureAreaID: areaID, TaskID: taskID}}
	g, err := f.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: sessionID, FeatureAreaID: areaID, TaskID: taskID})
	if err != nil {
		return out, err
	}
	if g.ProjectID != projectID || (taskID != "" && g.TaskID != taskID) || (areaID != "" && g.FeatureAreaID != areaID) {
		return out, notFound()
	}
	applyProgressionPresentation(&out, g)
	out.FeatureArea.ID = g.FeatureAreaID
	out.Details.FeatureAreaID = g.FeatureAreaID
	if g.FeatureAreaID != "" {
		a, e := (historicalFeatureAreaRead{f.deps.FeatureAreas}).GetForSession(ctx, sessionID, g.FeatureAreaID)
		if e != nil {
			return out, e
		}
		out.FeatureArea.Name, out.LifecycleStatus = a.Title, string(a.Status)
		projectFeatureAreaRetireCapability(&a, agentconversation.State{}, &out)
	}
	if g.TaskID != "" {
		// Derive current Task operations through the exact Core path used by
		// Console/Card. Compact reads cannot manufacture acceptance options from
		// guidance strings or omit late commit/direct-file eligibility checks.
		exact, e := f.TaskPresentationForSession(ctx, sessionID, g.FeatureAreaID, g.TaskID)
		if e != nil {
			return exact, e
		}
		// Retain the exact Core-derived workflow facts and capabilities, but keep
		// definition and Review content behind the explicit exact Task read.
		return compactTaskPresentation(exact), nil
	}
	if g.RefreshRequired {
		return out, errors.New("workflow changed; refresh required")
	}
	return out, nil
}

func compactTaskPresentation(exact p.Projection) p.Projection {
	if exact.TaskView != nil {
		exact.VerificationDecisions = exact.TaskView.VerificationDecisions
		for _, c := range exact.TaskView.Actions {
			if verificationTaskChoice(c.Action) {
				exact.Capabilities = append(exact.Capabilities, c)
			}
		}
	}
	exact.TaskView = nil
	exact.LatestTaskView = nil
	return exact
}

// WorkspaceRuntimeDecisionsForSession reads the exact current Run only. Full
// project inventory is an explicit navigation read and has its own error state.
func (f *Facade) WorkspaceRuntimeDecisionsForSession(ctx context.Context, sessionID, runID string) ([]p.PendingDecision, error) {
	out := []p.PendingDecision{}
	if runID == "" {
		return out, nil
	}
	r, owner, err := f.scopedRun(ctx, sessionID, runID)
	if err != nil {
		return nil, err
	}
	approvals, err := f.ListApprovalsForSession(ctx, sessionID, ApprovalFilter{RunID: runID, PendingOnly: true, ListOptions: ListOptions{Limit: MaxListLimit}})
	if err != nil {
		return nil, err
	}
	for _, a := range approvals {
		item := runtimePendingDecision(a)
		item.TaskID, item.FeatureAreaID = owner.ID, owner.FeatureAreaID
		out = append(out, item)
	}
	if r.AgentRuntime != nil {
		for _, request := range r.AgentRuntime.Requests {
			if agentruntime.OpenRequest(request) {
				out = append(out, runtimeRequestPendingDecision(r, request, owner))
			}
		}
	}
	return out, nil
}

// ListSurfaceTasksForSession filters semantic Tasks before pagination, so an
// Inspection envelope or historical REVIEW compatibility row cannot consume a
// TASK page position.
func (f *Facade) ListSurfaceTasksForSession(ctx context.Context, sessionID, areaID, taskID string, options ListOptions) ([]task.Task, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := f.deps.Tasks.List(ctx)
	if err != nil {
		return nil, MapError(err)
	}
	filtered := []task.Task{}
	for _, t := range values {
		if t.ProjectID == projectID && (areaID == "" || t.FeatureAreaID == areaID) && (taskID == "" || t.ID == taskID) && workspaceWork(t).Type == task.TypeDevelopment {
			filtered = append(filtered, t)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].UpdatedAt.Equal(filtered[j].UpdatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
	})
	return page(filtered, offset, limit), nil
}

// Pagination precedes projection of operation details. The existing durable
// Core envelopes are the only inventory; no new operation registry is created.
func (f *Facade) ListSurfaceOperationsForSession(ctx context.Context, sessionID, areaID, taskID string, options ListOptions) ([]agentexecution.Projection, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	if f.deps.AgentExecutions == nil {
		return nil, errors.New("operation inventory unavailable")
	}
	values, err := f.deps.AgentExecutions.ListByProject(ctx, projectID)
	if err != nil {
		return nil, MapError(err)
	}
	filtered := []agentexecution.Execution{}
	for _, v := range values {
		if (areaID == "" || v.FeatureAreaID == areaID) && (taskID == "" || v.TaskID == taskID) {
			filtered = append(filtered, v)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt.Equal(filtered[j].CreatedAt) {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})
	out := []agentexecution.Projection{}
	for _, v := range page(filtered, offset, limit) {
		p, err := f.projectAgentExecution(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// ListSurfaceFeatureAreasForSession includes durable historical areas for knowledge
// lookup. Listing never changes their lifecycle or engineering admission.
func (f *Facade) ListSurfaceFeatureAreasForSession(ctx context.Context, sessionID, areaID string, options ListOptions) ([]featurearea.FeatureArea, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	reader, ok := f.deps.FeatureAreas.(interface {
		ListHistoryForSession(context.Context, string) ([]featurearea.FeatureArea, error)
	})
	if !ok {
		return nil, errors.New("historical FeatureArea inventory unavailable")
	}
	values, err := reader.ListHistoryForSession(ctx, sessionID)
	if err != nil {
		return nil, MapError(err)
	}
	filtered := []featurearea.FeatureArea{}
	for _, value := range values {
		if value.ProjectID != projectID {
			return nil, notFound()
		}
		if areaID == "" || value.ID == areaID {
			filtered = append(filtered, value)
		}
	}
	// Stable identity ordering does not imply relevance, priority or an implicit selection.
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	return page(filtered, offset, limit), nil
}
