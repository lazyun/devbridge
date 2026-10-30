package controlapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// historicalFeatureAreaRead authorizes navigation without granting engineering access.
type historicalFeatureAreaRead struct{ FeatureAreaService }

func (s historicalFeatureAreaRead) GetForSession(ctx context.Context, session, id string) (featurearea.FeatureArea, error) {
	if history, ok := s.FeatureAreaService.(interface {
		GetHistoryForSession(context.Context, string, string) (featurearea.FeatureArea, error)
	}); ok {
		return history.GetHistoryForSession(ctx, session, id)
	}
	return s.FeatureAreaService.GetForSession(ctx, session, id)
}

func (f *Facade) currentWorkflowSnapshot(ctx context.Context, sessionID, areaID string) (CurrentWorkflow, error) {
	reader := *f
	reader.deps.FeatureAreas = historicalFeatureAreaRead{reader.deps.FeatureAreas}

	return reader.CurrentWorkflowForSession(ctx, sessionID, areaID)
}

func (f *Facade) currentTaskWorkflowSnapshot(ctx context.Context, sessionID, taskID string) (CurrentWorkflow, error) {
	reader := *f

	return reader.CurrentWorkflowForTaskForSession(ctx, sessionID, taskID)
}

// DevelopmentContextPresentationForSession is the shared pure Refresh read for
// both context-only and FeatureArea presentations. It never opens a Project.
func (f *Facade) DevelopmentContextPresentationForSession(ctx context.Context, sessionID string) (presentation.DevelopmentContext, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return presentation.DevelopmentContext{}, err
	}
	project, err := f.deps.Projects.Get(ctx, projectID)
	if err != nil {
		return presentation.DevelopmentContext{}, MapError(err)
	}
	development, err := f.DevelopmentContext(ctx, projectID)
	if err != nil {
		return presentation.DevelopmentContext{}, err
	}
	return developmentPresentation(project.ID, project.Name, development), nil
}

func developmentPresentation(projectID, name string, context developmentcycle.Context) presentation.DevelopmentContext {
	out := presentation.DevelopmentContext{Project: presentation.Identity{ID: projectID, Name: name}, SelectionRequired: context.DevelopmentBranchSelectionRequired, Ready: !context.DevelopmentBranchSelectionRequired && len(context.Repositories) > 0, Repositories: []presentation.RepositoryContext{}}
	for _, repository := range context.Repositories {
		if repository.DevelopmentRefStatus != "EXISTS" {
			out.Ready = false
		}
		item := presentation.RepositoryContext{Repository: presentation.Identity{ID: repository.RepositoryID, Name: repository.RepositoryName}, DevelopmentRef: repository.DevelopmentRef, Branch: repository.BranchShortName, Revision: repository.CurrentHeadSHA, SelectedRevision: repository.SelectedHeadSHA, Status: repository.DevelopmentRefStatus, SelectionRequired: repository.DevelopmentBranchSelectionRequired, RefChanged: repository.RefChanged, CheckoutMatches: repository.RepositoryCheckoutMatchesDevelopmentRef}
		if repository.StatusCode != "" {
			item.Status = repository.StatusCode
		}
		if repository.DevelopmentCycle != nil {
			item.DevelopmentCycleID = repository.DevelopmentCycle.ID
		}
		out.Repositories = append(out.Repositories, item)
	}
	return out
}

// FeatureAreaPresentationForSession is client independent and read only. The
// explicit FeatureArea identity survives new sessions and completed Tasks.
func (f *Facade) FeatureAreaPresentationForSession(ctx context.Context, sessionID, areaID string) (presentation.Projection, error) {
	for attempt := 0; attempt < 4; attempt++ {
		out, err := f.featureAreaPresentationSnapshot(ctx, sessionID, areaID)
		if err != nil || out.Guidance == nil {
			return out, err
		}
		after, err := f.WorkflowGuidance(ctx, sessionID, areaID)
		if err != nil {
			return presentation.Projection{}, err
		}
		if after.Generation == out.SourceGeneration {
			out.SourceGeneration = ""
			return out, nil
		}
	}
	return presentation.Projection{}, errors.New("workflow snapshot changed; refresh required")
}
func (f *Facade) featureAreaPresentationSnapshot(ctx context.Context, sessionID, areaID string) (presentation.Projection, error) {
	if err := validID("featureAreaId", areaID); err != nil {
		return presentation.Projection{}, err
	}
	development, err := f.DevelopmentContextPresentationForSession(ctx, sessionID)
	if err != nil {
		return presentation.Projection{}, err
	}
	current, err := f.currentWorkflowSnapshot(ctx, sessionID, areaID)
	if err != nil {
		return presentation.Projection{}, err
	}
	return f.featureAreaPresentationForCurrent(ctx, sessionID, areaID, "", development, current)
}

// TaskPresentationForSession is a pure read of one explicitly selected Task's
// exact current operation graph. It does not make the Task current, claim work,
// or change FeatureArea/Project selection.
func (f *Facade) TaskPresentationForSession(ctx context.Context, sessionID, areaID, taskID string) (presentation.Projection, error) {
	projectID, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return presentation.Projection{}, err
	}
	selected, err := f.deps.Tasks.Get(ctx, taskID)
	if err != nil || selected.ProjectID != projectID || selected.FeatureAreaID != areaID {
		return presentation.Projection{}, notFound()
	}
	if selected.Type == task.TypeInspection {
		return f.simpleTaskPresentation(ctx, sessionID, areaID, selected)
	}
	if f.deps.WorkflowProgression == nil {
		return presentation.Projection{}, errors.New("Task workflow guidance unavailable; refresh required")
	}
	// An authorized local operator opens an exact Task resource and has no AI
	// Session-level FeatureArea selection. Build that projection from the exact
	// Task resource guidance; the command owner still revalidates every target.
	if _, ok := localoperator.FromContext(ctx); ok && sessionID == "" {
		return f.taskPresentationSnapshot(ctx, sessionID, areaID, taskID)
	}

	for attempt := 0; attempt < 4; attempt++ {
		// Resolve the selected Task first to retain the exact project authorization boundary.
		if _, err := f.currentTaskWorkflowSnapshot(ctx, sessionID, taskID); err != nil {
			return presentation.Projection{}, err
		}
		before, err := f.WorkflowGuidance(ctx, sessionID, areaID)
		if err != nil {
			return presentation.Projection{}, err
		}
		out, err := f.taskPresentationSnapshot(ctx, sessionID, areaID, taskID)
		if err != nil {
			return out, err
		}
		after, err := f.WorkflowGuidance(ctx, sessionID, areaID)
		if err != nil {
			return presentation.Projection{}, err
		}
		if before.Generation == after.Generation {
			out.SourceGeneration = ""
			return out, nil
		}
	}
	return presentation.Projection{}, errors.New("workflow snapshot changed; refresh required")
}
func (f *Facade) taskPresentationSnapshot(ctx context.Context, sessionID, areaID, taskID string) (presentation.Projection, error) {
	if err := validID("featureAreaId", areaID); err != nil {
		return presentation.Projection{}, err
	}
	if err := validID("taskId", taskID); err != nil {
		return presentation.Projection{}, err
	}
	development, err := f.DevelopmentContextPresentationForSession(ctx, sessionID)
	if err != nil {
		return presentation.Projection{}, err
	}
	current, err := f.currentTaskWorkflowSnapshot(ctx, sessionID, taskID)
	if err != nil {
		return presentation.Projection{}, err
	}
	// Exact Task navigation must not be replaced by a newer FeatureArea Review.
	// Operation ownership is still projected independently and continues to
	// fence every action capability.
	selectTaskPresentationReview(&current)
	if f.deps.WorkflowProgression != nil {
		g, e := f.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: sessionID, TaskID: taskID})
		if e != nil {
			return presentation.Projection{}, e
		}
		current.Guidance = &g
	}
	return f.featureAreaPresentationForCurrent(ctx, sessionID, areaID, taskID, development, current)
}

func (f *Facade) featureAreaPresentationForCurrent(ctx context.Context, sessionID, areaID, taskID string, development presentation.DevelopmentContext, current CurrentWorkflow) (presentation.Projection, error) {
	var qualificationErr error
	if current.Topic == nil || current.Topic.ID != areaID || current.Topic.ProjectID != development.Project.ID {
		return presentation.Projection{}, notFound()
	}
	if taskID != "" && (current.Task == nil || current.Task.ID != taskID || current.Task.FeatureAreaID != areaID) {
		return presentation.Projection{}, notFound()
	}
	if current.Task != nil && current.Task.Type == task.TypeDevelopment {
		if err := validateTaskPresentationGuidance(*current.Task, current.Guidance); err != nil {
			return presentation.Projection{}, err
		}
	}
	contextValue, err := f.GetProjectContextForSession(ctx, sessionID)
	if err != nil {
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
			return presentation.Projection{}, err
		}
	}
	var operation agentconversation.State
	if current.AgentOperation != nil {
		operation = *current.AgentOperation
	} else {
		operation, err = f.FeatureAreaAgentOperationState(ctx, sessionID, areaID)
		if err != nil {
			return presentation.Projection{}, err
		}
	}
	out := projectFeatureAreaPresentation(current, development, contextValue, operation)
	out.AgentOperation = presentation.AgentOperation{Status: operation.Status, Message: operation.Message, ConversationID: operation.AgentConversationID}
	if operation.Claim != nil {
		at := operation.Claim.CreatedAt
		out.AgentOperation.StartedAt = &at
	}
	if presentationReviewCurrent(current) {
		current.Execution = nil
	}
	if current.Execution != nil {
		receipts, err := f.ListExecutionCancellationsForSession(ctx, sessionID, current.Execution.ID)
		if err != nil {
			if f.operatorProject == "" {
				return presentation.Projection{}, err
			}
			qualificationErr = errors.Join(qualificationErr, err)
		}
		for _, receipt := range receipts {
			if receipt.RecoveryRequired && receipt.ResolvedAt == nil {
				out.Recovery = &presentation.Recovery{Status: receipt.State, Required: true, Summary: "Execution outcome is unknown; explicit recovery is required."}
				applyPresentationRecovery(&out, "RECOVERY_REQUIRED")
				if receipt.RequestedAt.After(out.UpdatedAt) {
					out.UpdatedAt = receipt.RequestedAt
				}
				for i := range out.Capabilities {
					if out.Capabilities[i].Action != "REFRESH" && out.Capabilities[i].Action != "VIEW_DETAILS" {
						out.Capabilities[i].Available = false
						out.Capabilities[i].Reason = "Recovery requires an exact authoritative eligibility check."
					}
				}
			}
		}
	}
	if err := f.enrichFeatureAreaActionCapabilities(ctx, sessionID, current, operation, &out); err != nil {
		if f.operatorProject == "" {
			return presentation.Projection{}, err
		}
		qualificationErr = errors.Join(qualificationErr, err)
	}
	if current.Guidance != nil {
		applyProgressionPresentation(&out, *current.Guidance)
	}

	setFeatureAreaCapability(&out, "RESET_AGENT_CONTEXT", current.Topic.Status == featurearea.StatusActive && operation.Status == "READY" && operation.Claim == nil, "Requires an idle, usable FeatureArea.", nil)
	projectFeatureAreaRetireCapability(current.Topic, operation, &out)
	if err := f.qualifyRetirementGit(ctx, current.Topic, &out); err != nil {
		return presentation.Projection{}, err
	}
	enforceReviewRetirementPresentation(current.Topic, current.CurrentReview, &out)
	current.AgentOperation = &operation
	projectCardStages(&out, current)
	if out.TaskStages != nil {
		out.TaskStages.ReadOnly = current.Task.LegacyReadOnly || (current.Guidance != nil && current.Guidance.Legacy)
	}
	selection := f.ExecutionAllowlist().Clone()
	out.TaskOptions = &presentation.TaskOptions{Models: selection.Models, ReasoningEfforts: selection.ReasoningEfforts, ModelConfigs: selection.ModelConfigs, RuntimeOptions: selection.RuntimeOptions()}
	if f.deps.PiAgentEnabled != nil {
		out.TaskOptions.PiAgentEnabled = f.deps.PiAgentEnabled()
	}
	out.TaskOptions.ConversationModelDefault = f.conversationModelDefault(ctx, current.Topic.ID)
	for _, capability := range out.Capabilities {
		if (capability.Action == wp.AcceptanceRequestChanges || capability.Action == wp.AcceptanceRetryImplementation) && current.Execution != nil {
			out.TaskOptions.LockedBackend = current.Execution.BackendSelection
		}
	}
	if current.Task != nil {
		if backends, backendErr := f.backendSelectionOptions(ctx, sessionID, current.Topic.ProjectID, current.Topic.ID); backendErr != nil {
			out.TaskOptions.BackendInventoryReadError = backendErr.Error()
		} else {
			out.TaskOptions.BackendCandidates = backends.Candidates
			out.TaskOptions.BackendInventoryComplete = backends.Complete
			out.TaskOptions.BackendContinuity = backends.Continuity
			if out.TaskOptions.LockedBackend == nil && backends.Continuity != nil && backends.Continuity.State == agentconversation.BackendContinuityBoundAvailable {
				out.TaskOptions.LockedBackend = backends.Continuity.BoundBackend
			}
		}
	}
	out.Generation = ""
	if out.Guidance != nil {
		out.Guidance.Generation = ""
	}
	if !contextValue.SnapshotUsable() || !development.Ready {
		for i := range out.Capabilities {
			if out.Capabilities[i].Action == "CREATE_TASK" {
				out.Capabilities[i].Available = false
				out.Capabilities[i].Reason = "A usable Project Context and selected Development Branch are required."
			}
		}
	}
	if current.Task != nil && out.Details.TaskID == current.Task.ID {
		if err := f.attachTaskView(ctx, &out, *current.Task); err != nil {
			return presentation.Projection{}, err
		}
	}
	if out.TaskView == nil {
		out.ProductProgress = nil
		for _, phase := range out.Progress {
			if phase.Phase == "REVIEW" || phase.Phase == "APPROVAL" {
				if phase.Phase == "APPROVAL" {
					phase.Phase = "APPROVED"
				}
				out.ProductProgress = append(out.ProductProgress, phase)
			}
		}
	}
	if taskID == "" && out.TaskView == nil && out.LatestTask != nil {
		latest, e := f.deps.Tasks.Get(ctx, out.LatestTask.ID)
		if e != nil {
			return presentation.Projection{}, e
		}
		if latest.ProjectID != current.Topic.ProjectID || latest.FeatureAreaID != areaID {
			return presentation.Projection{}, notFound()
		}
		v, e := f.taskView(ctx, latest)
		if e != nil {
			return presentation.Projection{}, e
		}
		v.ReadOnly = true
		v.ActionsStatus, v.ActionsReadError = "AVAILABLE", ""
		out.LatestTaskView = &v
	}
	if err = f.applyCommitPresentation(ctx, &out, current.Topic.ProjectID, areaID); err != nil {
		if f.operatorProject == "" {
			return presentation.Projection{}, err
		}
		qualificationErr = errors.Join(qualificationErr, err)
	}

	if f.deps.DirectFiles != nil {
		state, err := f.DirectFileStatus(ctx, sessionID, areaID)
		if err != nil {
			return presentation.Projection{}, err
		}
		areaFileActive := state.Enabled
		// A Task surface reads its own revision-bound authority, never borrows
		// the area's unrelated standalone workflow.
		if current.Task != nil && out.TaskView != nil && current.Task.CurrentDefinitionRevisionID != "" {
			exact, e := f.DirectFileTaskStatus(ctx, sessionID, current.Task.ID)
			if e != nil {
				return presentation.Projection{}, e
			}
			if (exact.TaskID != "" || exact.WorkflowID != "" || exact.Enabled || exact.State == "ACTIVE") && (exact.TaskID != current.Task.ID || exact.TaskDefinitionRevisionID != current.Task.CurrentDefinitionRevisionID || exact.ProjectID != current.Task.ProjectID || exact.FeatureAreaID != areaID || exact.AISessionID != sessionID) {
				return presentation.Projection{}, notFound()
			}
			if exact.TaskID != "" {
				out.TaskView.DirectFile = &exact
				if out.TaskView.ImplementationMode == "UNKNOWN" && exact.WorkflowID != "" && !current.Task.Phase.ReviewStage() {
					out.TaskView.ImplementationMode = "DIRECT_FILE"
				}
			}
			state = exact
		}
		out.DirectFile = &state
		if v := out.TaskView; v != nil && v.ImplementationMode == "DIRECT_FILE" && current.Task != nil && (current.Task.Phase == task.PhaseImplementationReady || current.Task.Phase == task.PhaseImplementing) && current.Task.Status == task.StatusActive && out.PendingDecision == nil && (out.Recovery == nil || !out.Recovery.Required) {
			message, code := "方案已批准 · 文件权限不可用", "DIRECT_FILE_UNAVAILABLE"
			if state.State == "ACTIVE" && state.Enabled && state.WorkflowID != "" && v.DirectFile != nil {
				message, code = "方案已批准 · 文件权限已就绪；可由 GPT 直接开始文件操作", "DIRECT_FILE_READY"
			}
			if state.State == "PREPARING" {
				message, code = "方案已批准 · 文件工作区准备中", "DIRECT_FILE_PREPARING"
			}
			if state.State == "CLOSED" {
				message, code = "方案已批准 · 文件授权已关闭", "DIRECT_FILE_CLOSED"
			}
			if state.FailureReason != "" {
				message += "；" + state.FailureReason
			}
			v.Activity = presentation.TaskActivity{Status: state.State, Summary: message}
			if state.StartedAt != nil {
				v.Activity.Summary = "GPT 文件操作已有记录；" + message
			}
			out.NextAction = wp.Action{Code: code, Message: message}
		}
		// A stale enrolled file worker cannot regain delivery authority through
		// generic recovery copy. Keep its actual grant state and use only exact
		// authoritative cancellation guidance (or refresh when unavailable).
		if action, worker := presentation.DirectFileWorkerAction(out); worker {
			out.NextAction = action
			if out.TaskView != nil && out.TaskView.ID == state.TaskID {
				out.TaskView.Activity = presentation.TaskActivity{Status: state.State, Summary: action.Message}
			}
		}
		if areaFileActive || state.Enabled {
			for i := range out.Capabilities {
				switch out.Capabilities[i].Action {
				case "START_EXECUTION", "START_REVIEW", "RESET_AGENT_CONTEXT", "COMMIT_FEATURE_AREA", "RETRY_TASK_COMMIT", "RECOVER_FEATURE_AREA_MERGE", "REPREPARE_FEATURE_AREA_MERGE":
					out.Capabilities[i].Available = false
					out.Capabilities[i].Reason = "DIRECT_FILE_CAPABILITY_ACTIVE"
				}
			}
		}
	}
	if f.operatorProject != "" {
		qualificationErr = errors.Join(qualificationErr, f.applyOperatorActionBoundary(ctx, &out))
	}
	// Assemble once, after commit eligibility and all safety blockers are final.
	if current.Task != nil && out.TaskView != nil {
		if err := f.enrichImplementationRetry(ctx, sessionID, *current.Task, &out); err != nil {
			if f.operatorProject == "" {
				return presentation.Projection{}, err
			}
			qualificationErr = errors.Join(qualificationErr, err)
		}
		if err := f.enrichTaskReviewRequest(ctx, sessionID, *current.Task, &out); err != nil {
			if f.operatorProject == "" {
				return presentation.Projection{}, err
			}
			qualificationErr = errors.Join(qualificationErr, err)
		}
		if err := f.enrichExecutionRuntimeEligibility(ctx, sessionID, *current.Task, &out); err != nil {
			if f.operatorProject == "" {
				return presentation.Projection{}, err
			}
			qualificationErr = errors.Join(qualificationErr, err)
		}
		assembleTaskActions(&out, *current.Task)
		if err := f.attachTaskActionNode(ctx, sessionID, &out); err != nil {
			return presentation.Projection{}, err
		}
		if f.operatorProject != "" {
			sealOperatorTaskActions(&out)
		}
		if qualificationErr != nil {
			out.TaskView.ActionsStatus = "UNAVAILABLE"
			out.TaskView.ActionsReadError = "Operation qualification facts could not be confirmed; refresh required."
			for i := range out.TaskView.Actions {
				out.TaskView.Actions[i].Available = false
				out.TaskView.Actions[i].Reason = out.TaskView.ActionsReadError
			}
			for i := range out.Capabilities {
				if taskActionKind(out.Capabilities[i].Action) {
					out.Capabilities[i].Available = false
					out.Capabilities[i].Reason = out.TaskView.ActionsReadError
				}
			}
			if out.PendingDecision != nil {
				out.PendingDecision.AllowedActions = []string{}
			}
		}
		out.TaskView.CodexContinuation = presentation.CodexContinuationFor(*out.TaskView)
	}
	if qualificationErr != nil {
		out.Blockers = append(out.Blockers, "Operation qualification facts could not be confirmed; refresh required.")
		for i := range out.Capabilities {
			if out.Capabilities[i].Action != "REFRESH" && out.Capabilities[i].Action != "VIEW_DETAILS" {
				out.Capabilities[i].Available = false
				out.Capabilities[i].Reason = "Operation qualification facts could not be confirmed; refresh required."
			}
		}
	}
	encoded, _ := json.Marshal(out)
	hash := sha256.Sum256(encoded)
	out.Generation = hex.EncodeToString(hash[:])
	if out.Guidance != nil {
		out.Guidance.Generation = out.Generation
	}
	out.NextAction = presentation.CurrentAction(out)
	populateFeatureAreaProductProjection(&out, current.Topic.LegacyReadOnly)
	if out.TaskView != nil {
		out.TaskView.CurrentAction = out.NextAction
		out.TaskView.Recovery = out.Recovery
	}
	return out, nil
}

func populateFeatureAreaProductProjection(out *presentation.Projection, legacyReadOnly bool) {
	product := presentation.NewFeatureAreaProductProjection(out.FeatureArea.ID, out.FeatureArea.Name, out.LifecycleStatus, legacyReadOnly)
	product.CurrentTask = out.CurrentTask
	product.LatestTask = out.LatestTask
	if out.CurrentTask != nil {
		product.TaskPhase = string(out.CurrentTask.Phase)
		if operation := out.CurrentOperation; operation != nil && operation.ID != "" && operation.RunID != "" && (out.AgentOperation.Status == "BUSY" || out.AgentOperation.Status == "RECOVERY_REQUIRED") {
			product.OperationActivity = &presentation.ProductOperationActivity{State: out.AgentOperation.Status, Message: out.AgentOperation.Message, Kind: operation.Kind, OperationID: operation.ID, RunID: operation.RunID}
		}
	}
	seen := map[string]bool{}
	for _, capability := range out.Capabilities {
		if capability.Available && capability.Action != "" && !seen[capability.Action] {
			product.AvailableActions = append(product.AvailableActions, capability.Action)
			seen[capability.Action] = true
		}
	}
	if out.NextAction.Required {
		action := out.NextAction
		product.RequiredAction = &action
	}
	out.Product = product
}

// FeatureAreaRetireCapabilityForSession is the smallest authoritative read
// projection needed by presentation adapters that offer lifecycle closure. It
// intentionally does not require Project Context or Development Branch
// readiness, because neither is a prerequisite of the existing RETIRE
// lifecycle transition.
func (f *Facade) FeatureAreaRetireCapabilityForSession(ctx context.Context, sessionID, areaID string) (presentation.Capability, error) {
	area, err := f.GetFeatureAreaForSession(ctx, sessionID, areaID)
	if err != nil {
		return presentation.Capability{}, err
	}
	out := presentation.Projection{}
	projectFeatureAreaRetireCapability(&area, agentconversation.State{}, &out)
	if err := f.qualifyRetirementGit(ctx, &area, &out); err != nil {
		return presentation.Capability{}, err
	}
	return out.Capabilities[0], nil
}

func (f *Facade) qualifyRetirementGit(ctx context.Context, area *featurearea.FeatureArea, out *presentation.Projection) error {
	if area == nil || !featurearea.CanRetire(*area) {
		return nil
	}
	reader, ok := f.deps.WorkflowProgression.(interface {
		FeatureAreaRetirementBlocked(context.Context, string, string) (bool, error)
	})
	if !ok {
		return nil
	}
	blocked, err := reader.FeatureAreaRetirementBlocked(ctx, area.ProjectID, area.ID)
	if err != nil {
		return MapError(err)
	}
	if blocked {
		setFeatureAreaCapability(out, "RETIRE", false, "代码交付尚未结清，请先处理未完成的代码交付或合入计划。", nil)
	}
	return nil
}

// projectFeatureAreaRetireCapability exposes stop intent independently of current
// work, Decisions and claims. The lifecycle write remains authoritative.
func projectFeatureAreaRetireCapability(area *featurearea.FeatureArea, _ agentconversation.State, out *presentation.Projection) {
	available := area != nil && featurearea.CanRetire(*area)
	reason := ""
	if !available {
		reason = "FeatureArea is already closing, closed, or immutable."
	}
	setFeatureAreaCapability(out, "RETIRE", available, reason, nil)
}

// enforceRetirementPresentation is applied after capability enrichment, so no
// adapter can turn a historical Decision or recovery into engineering authority.
func enforceRetirementPresentation(area *featurearea.FeatureArea, out *presentation.Projection) {
	enforceReviewRetirementPresentation(area, nil, out)
}

func enforceReviewRetirementPresentation(area *featurearea.FeatureArea, currentReview *review.Review, out *presentation.Projection) {
	if area == nil || (area.Status != featurearea.StatusRetiring && area.Status != featurearea.StatusRetired) {
		return
	}
	out.Retirement = area.Retirement
	out.WorkflowStatus = string(area.Status)
	out.PendingDecision = nil
	out.Recovery = nil
	for i := range out.Capabilities {
		// Retirement uses the same canonical Upper cancellation protocol to
		// revoke an existing grant. Preserve only an already-qualified exact
		// cancellation; this exception never manufactures a capability.
		if area.Status == featurearea.StatusRetiring && currentReview != nil && currentReview.ExecutorKind == review.ExecutorUpperLLM && exactUpperCancellation(out.Capabilities[i], area.ID, currentReview.TaskID, currentReview.ID, currentReview.RunID) {
			continue
		}
		if out.Capabilities[i].Action != "REFRESH" && out.Capabilities[i].Action != "VIEW_DETAILS" {
			out.Capabilities[i].Available = false
			out.Capabilities[i].Reason = "FeatureArea engineering authority is closed."
		}
	}
}

// projectFeatureAreaPresentation is a non-persisted projection, not a workflow
// transition system. All object selection is owned by CurrentWorkflow.
func projectFeatureAreaPresentation(current CurrentWorkflow, development presentation.DevelopmentContext, contextValue projectcontext.ProjectContext, operation agentconversation.State) (out presentation.Projection) {
	defer func() {
		if current.AgentExecution != nil {
			out.AgentExecution = current.AgentExecution
			out.Details.ExecutionID = current.AgentExecution.ExecutionID
		}
	}()
	area := current.Topic
	out = presentation.Projection{AgentExecution: current.AgentExecution, SchemaVersion: "1", FeatureArea: presentation.Identity{ID: area.ID, Name: area.Title}, DevelopmentContext: development, LifecycleStatus: string(area.Status), UpdatedAt: area.UpdatedAt, Blockers: []string{}, Capabilities: []presentation.Capability{}, Details: presentation.Details{View: "FEATURE_AREA_DETAILS", ProjectID: area.ProjectID, FeatureAreaID: area.ID}}
	updated := func(at time.Time) {
		if at.After(out.UpdatedAt) {
			out.UpdatedAt = at
		}
	}
	if t := current.Task; t != nil {
		out.Details.TaskID = t.ID
		out.Details.TaskDefinitionRevisionID = t.CurrentDefinitionRevisionID
		out.Details.ReviewID = t.CurrentReviewID
		out.Details.ExecutionID = t.CurrentExecutionID
		out.Details.RunID = t.Work.RunID
		out.Details.Baseline = t.BaseCommit
		out.CurrentPhase = string(t.Phase)
		out.WorkflowStatus = string(t.Phase)
		out.Progress = presentation.TaskPhaseProgress(t.Phase, t.Status)
		updated(t.UpdatedAt)
		if t.Status != task.StatusActive {
			out.WorkflowStatus = string(t.Status)
		}
	} else {
		out.WorkflowStatus = "FEATURE_AREA_IDLE"
		out.CurrentPhase = "IDLE"
	}
	if a := current.PendingApproval; a != nil && a.Status == approval.StatusPending {
		effect := "CANCEL_LEGACY_RUN"
		if a.WorkflowOwnedRun {
			effect = "DECLINE_ACTION"
		}
		out.PendingDecision = &presentation.Decision{CreatedAt: presentation.OptionalDecisionTime(a.CreatedAt), Kind: "RUNTIME_APPROVAL", ID: "RUNTIME_APPROVAL:" + a.ID, SubjectID: a.ID, Summary: "Runtime approval required: " + string(a.Kind), Guarantee: "AUTHORITATIVE_ENTITY_STATE", CancelEffect: effect, AllowedActions: []string{}}
		for _, action := range a.AllowedDecisions {
			if action == approval.DecisionGrantRequestedPermissions {
				normalized, err := approval.NormalizeDecision(*a, approval.Decision{Type: action})
				if err != nil {
					continue
				}
				out.PendingDecision.Permissions = normalized.Permissions
			}
			out.PendingDecision.AllowedActions = append(out.PendingDecision.AllowedActions, string(action))
		}
		out.WorkflowStatus = "WAITING_APPROVAL"
		updated(a.CreatedAt)
	}
	if operation.Claim != nil {
		claim := operation.Claim
		out.CurrentOperation = &presentation.Operation{Kind: string(claim.Kind), ID: claim.OperationID, RunID: claim.RunID, Status: operation.Status}
		out.Details.RunID = claim.RunID
		for _, r := range current.Runs {
			if r.ID == claim.RunID && r.AgentRuntime != nil {
				summary := r.AgentRuntime.Summary()
				out.AgentRuntime = &summary
				break
			}
		}
		updated(claim.CreatedAt)

	}
	switch operation.Status {
	case "UNKNOWN", "RECOVERY_REQUIRED", "CONFLICT", "UNAVAILABLE", "FORBIDDEN":
		out.Recovery = &presentation.Recovery{Status: operation.Status, Required: true, Summary: operation.Message}
		if operation.Status == "RECOVERY_REQUIRED" && operation.Claim != nil && current.CurrentReview != nil && current.Task != nil && current.CurrentReview.ExecutorKind == review.ExecutorUpperLLM && current.CurrentReview.ID == current.Task.CurrentReviewID && current.CurrentReview.TaskID == current.Task.ID && operation.Claim.Kind == agentconversation.Review && operation.Claim.OperationID == current.CurrentReview.ID && operation.Claim.RunID == current.CurrentReview.RunID {
			out.Recovery.Reason = "GPT_REVIEW_AUTHORIZATION_REQUIRED"
			out.Recovery.Summary = "服务已重启，原 GPT 审查的文件授权已失效。需要重新授权后才能继续审查并提交报告；原任务和审查范围保留。"
		}
		applyPresentationRecovery(&out, operation.Status)
	}
	if !featurearea.IsUsable(*area) || !contextValue.SnapshotUsable() {
		out.Blockers = append(out.Blockers, "FeatureArea or Project Context is unavailable.")
		if out.Recovery == nil {
			out.WorkflowStatus = "BLOCKED"
		}
	}
	if development.SelectionRequired {
		out.Blockers = append(out.Blockers, "Development Branch selection required.")
	}
	idle := operation.Claim == nil && operation.Status == "READY"
	usable := featurearea.IsUsable(*area) && out.Recovery == nil
	out.Capabilities = append(out.Capabilities, presentation.Capability{Action: "REFRESH", Available: true}, presentation.Capability{Action: "VIEW_DETAILS", Available: true})
	if decision := out.PendingDecision; decision != nil {
		decision.Guarantee = "AUTHORITATIVE_ENTITY_STATE"
		if decision.Kind == "TASK_CONFIRMATION" {
			decision.Guarantee = "DURABLE_REQUEST_RECEIPT"
		}
		for _, action := range decision.AllowedActions {
			available := usable && idle
			if decision.Kind == "RUNTIME_APPROVAL" {
				available = usable && operation.Claim != nil && current.PendingApproval.RunID == operation.Claim.RunID && operation.Status != "RECOVERY_REQUIRED"
			}
			option := action
			if decision.Kind != "RUNTIME_APPROVAL" {
				if action == "AUTHORIZE" {
					option = "EXECUTE"
				}
				if action == "REJECT" {
					option = "CANCEL"
				}
			}
			reason := ""
			if !available {
				reason = "Current lifecycle or operation ownership does not permit this decision."
			}
			target := &presentation.ActionTarget{FeatureAreaID: area.ID, DecisionID: decision.ID, SubjectID: decision.SubjectID}
			out.Capabilities = append(out.Capabilities, presentation.Capability{Action: action, DecisionOption: option, Available: available, Reason: reason, Target: target})
		}
	}
	if out.Recovery != nil {
		out.Capabilities = append(out.Capabilities, presentation.Capability{Action: "RECOVER", Available: false, Reason: "Exact recovery eligibility is not projected by this foundation."})
	}
	return out
}

func applyPresentationRecovery(out *presentation.Projection, status string) {
	out.WorkflowStatus = status
	for i := range out.Progress {
		if out.Progress[i].Phase == out.CurrentPhase {
			if out.CurrentPhase == "REVIEW" {
				// Agent recovery describes ownership safety, not the Review's
				// product outcome. Keep failed/blocked Review facts visible.
				continue
			}
			out.Progress[i].Status = status
		}
	}
}

// Choose a presentation surface from the authoritative operation claim or
// admitted object chronology. Pending Task/Execution decisions are never
// dropped merely because a Review is newer. This does not create a cycle ID,
// rebind historical objects, or affect which operations can be admitted.
func presentationReviewCurrent(current CurrentWorkflow) bool {
	return current.Task != nil && current.Task.Phase.ReviewStage() && current.CurrentReview != nil && current.CurrentReview.ID == current.Task.CurrentReviewID && current.CurrentReview.TaskID == current.Task.ID
}

func presentationRunStatus(status run.Status) string {
	switch status {
	case run.StatusSucceeded:
		return "COMPLETE"
	case run.StatusCreated, run.StatusPreparing, run.StatusRunning, run.StatusCollecting:
		return "RUNNING"
	default:
		if status == "" {
			return "UNKNOWN"
		}
		return string(status)
	}
}
