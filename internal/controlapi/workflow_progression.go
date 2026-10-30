package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func (f *Facade) WorkflowGuidance(ctx context.Context, sessionID, areaID string) (wp.Guidance, error) {
	project, err := f.sessionProject(ctx, sessionID)
	if err != nil {
		return wp.Guidance{}, err
	}
	if f.deps.WorkflowProgression == nil {
		return wp.Guidance{}, errors.New("workflow progression unavailable")
	}
	return f.deps.WorkflowProgression.Read(ctx, project, areaID)
}
func (f *Facade) ValidateDevelopmentCreation(ctx context.Context, sessionID, areaID string) error {
	g, err := f.WorkflowGuidance(ctx, sessionID, areaID)
	if err != nil {
		return err
	}
	return developmentCreationError(g)
}
func developmentCreationError(g wp.Guidance) error {
	if g.FeatureAreaStatus == "ACTIVE" || g.FeatureAreaStatus == "DISCOVERING" {
		return nil
	}
	return wp.Reject("FEATURE_AREA_UNAVAILABLE", g)
}
func (f *Facade) ResolveReviewApproval(ctx context.Context, in wp.ResolveInput) (wp.Resolution, error) {
	if f.deps.WorkflowProgression == nil {
		return wp.Resolution{}, errors.New("workflow progression unavailable")
	}
	if _, err := f.sessionProject(ctx, in.AISessionID); err != nil {
		return wp.Resolution{}, err
	}
	return f.deps.WorkflowProgression.Resolve(ctx, in)
}

// ResolveReviewApprovalForOperator keeps a local decision distinct from an AI
// author. REVISE carries only bounded immutable review feedback; REJECT has no
// authored payload or file grant. An APPROVE
// may select the next Coding Agent runtime; Direct File approval remains
// config-free in the canonical reducer.
func (f *Facade) ResolveReviewApprovalForOperator(ctx context.Context, projectID, taskID, action string, in wp.ResolveInput, backendInput *codexconfig.BackendSelectionInput) (wp.Resolution, error) {
	if action == "REQUEST_RE_REVIEW" {
		in.Option = "REVISE"
	}
	if action == "ABANDON_TASK" {
		in.Option = "REJECT"
	}
	if action != "APPROVE" && action != "REVISE" && action != "REJECT" {
		if action != "REQUEST_RE_REVIEW" && action != "ABANDON_TASK" {
			return wp.Resolution{}, invalid("Unsupported operator Review decision")
		}
	}
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, action)
	if err != nil {
		return wp.Resolution{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return wp.Resolution{}, err
	}
	if in.AISessionID != "" || in.OperatorPrincipalID != "" || in.ExecutionInstructions != nil || in.ConfirmationIntentID != "" {
		return wp.Resolution{}, invalid("Operator Review decision cannot supply a Session, actor, or authored payload")
	}
	if action != "APPROVE" && (in.NextRunRuntimeConfig != nil || backendInput != nil) {
		return wp.Resolution{}, invalid("Only operator APPROVE may select backend and runtime configurations")
	}
	if in.Option == "REVISE" && !wp.ValidReviewFeedback(in.ReviewFeedback) || in.Option != "REVISE" && in.ReviewFeedback != "" {
		return wp.Resolution{}, invalid("REVISE requires only non-empty bounded reviewFeedback")
	}
	if action == "APPROVE" && (in.NextRunRuntimeConfig == nil) != (backendInput == nil) {
		return wp.Resolution{}, invalid("Backend and runtime selections must be submitted together")
	}
	if backendInput != nil {
		selected, selectionErr := reader.resolveBackendSelection(ctx, "", projectID, in.FeatureAreaID, backendInput)
		if selectionErr != nil {
			return wp.Resolution{}, invalid(selectionErr.Error())
		}
		in.BackendSelection = &selected
	}
	in.RequestID, err = localoperator.RequestID(ctx, projectID, taskID, action, in.RequestID)
	if err != nil {
		return wp.Resolution{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	in.OperatorPrincipalID = actor.PrincipalID()
	if reader.deps.WorkflowProgression == nil {
		return wp.Resolution{}, errors.New("workflow progression unavailable")
	}
	return reader.deps.WorkflowProgression.Resolve(ctx, in)
}

func (f *Facade) ResolveTaskAcceptance(ctx context.Context, in wp.TaskAcceptanceInput) (wp.TaskAcceptanceResolution, error) {
	store, ok := f.deps.WorkflowProgression.(wp.TaskAcceptanceStore)
	if !ok {
		return wp.TaskAcceptanceResolution{}, errors.New("Task acceptance progression unavailable")
	}
	if _, err := f.sessionProject(ctx, in.AISessionID); err != nil {
		return wp.TaskAcceptanceResolution{}, err
	}
	return store.ResolveTaskAcceptance(ctx, in)
}

// ResolveTaskAcceptanceForOperator reaches the same acceptance reducer with a
// trusted local actor. The reducer records that actor as the source of any
// successor content; it never borrows an AI Session as that author.
func (f *Facade) ResolveTaskAcceptanceForOperator(ctx context.Context, projectID, taskID, action string, in wp.TaskAcceptanceInput) (wp.TaskAcceptanceResolution, error) {
	switch action {
	case wp.AcceptanceAccept, wp.AcceptanceReject, wp.AcceptanceRequestChanges, wp.AcceptanceRequestReverification, wp.AcceptanceRetryImplementation:
	default:
		return wp.TaskAcceptanceResolution{}, invalid("Unsupported operator acceptance action")
	}
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, action)
	if err != nil {
		return wp.TaskAcceptanceResolution{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return wp.TaskAcceptanceResolution{}, err
	}
	if in.AISessionID != "" || in.OperatorPrincipalID != "" {
		return wp.TaskAcceptanceResolution{}, invalid("Operator acceptance cannot supply a Session or caller actor")
	}
	if action == wp.AcceptanceRequestChanges && in.NextRunRuntimeConfig == nil {
		return wp.TaskAcceptanceResolution{}, invalid("Operator Request Changes requires a selected runtime configuration")
	}
	if action == wp.AcceptanceRequestReverification && in.NextRunRuntimeConfig != nil {
		return wp.TaskAcceptanceResolution{}, invalid("Reverification inherits the predecessor runtime configuration")
	}
	in.RequestID, err = localoperator.RequestID(ctx, projectID, taskID, action, in.RequestID)
	if err != nil {
		return wp.TaskAcceptanceResolution{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	in.OperatorPrincipalID = actor.PrincipalID()
	store, ok := reader.deps.WorkflowProgression.(wp.TaskAcceptanceStore)
	if !ok {
		return wp.TaskAcceptanceResolution{}, errors.New("Task acceptance progression unavailable")
	}
	return store.ResolveTaskAcceptance(ctx, in)
}

// StartTaskExecutionForOperator uses the same exact-target reservation and
// runtime handoff as the Session path without manufacturing a Session identity.
// The selected run is durable before dispatch is attempted; callers must treat
// a non-accepted handoff as reserved/unknown rather than backend acceptance.
func (f *Facade) StartTaskExecutionForOperator(ctx context.Context, projectID, taskID, action, requestID string, expected wp.TaskExecutionExpectation) (executionproposal.StartResult, error) {
	if action != "START_EXECUTION" && action != "START_VERIFICATION" {
		return executionproposal.StartResult{}, invalid("Unsupported operator execution start")
	}
	if !validTaskExecutionExpectation(expected) || expected.Phase == task.PhaseImplementationReady && action != "START_EXECUTION" || expected.Phase == task.PhaseVerificationReady && action != "START_VERIFICATION" {
		return executionproposal.StartResult{}, invalid("Exact Task execution target is required")
	}
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, action)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return executionproposal.StartResult{}, err
	}
	requestID, err = localoperator.RequestID(ctx, projectID, taskID, action, requestID)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	store, ok := reader.deps.WorkflowProgression.(interface {
		ReserveTaskExecutionExpectedForOperator(context.Context, string, string, string, string, string, *wp.TaskExecutionExpectation) (executionproposal.Proposal, run.Run, error)
	})
	if !ok {
		return executionproposal.StartResult{}, errors.New("Exact Task progression unavailable")
	}
	value, reserved, err := store.ReserveTaskExecutionExpectedForOperator(ctx, projectID, taskID, action, actor.PrincipalID(), requestID, &expected)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	starter, ok := f.deps.ExecutionProposals.(interface {
		StartAuthorizedTurnForProject(context.Context, string, executionproposal.Proposal, run.Run) (executionproposal.StartResult, error)
	})
	if !ok {
		return executionproposal.StartResult{Proposal: value, Run: reserved}, errors.New("reserved Execution handoff unavailable")
	}
	return starter.StartAuthorizedTurnForProject(ctx, projectID, value, reserved)
}

// TaskExecutionStartReplayForOperator only recognizes the caller's own
// immutable receipt. requestID is already source-scoped by the Console ingress;
// unlike the fresh command entry it must not be derived a second time. It is
// used before live-capability freshness checks so a response-loss retry can
// resume the original handoff without reopening it.
func (f *Facade) TaskExecutionStartReplayForOperator(ctx context.Context, projectID, taskID, action, requestID string, expected wp.TaskExecutionExpectation) (bool, error) {
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, action)
	if err != nil {
		return false, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return false, err
	}
	actor, _ := localoperator.FromContext(ctx)
	store, ok := reader.deps.WorkflowProgression.(interface {
		TaskExecutionStartReplayForOperator(context.Context, string, string, string, string, string, wp.TaskExecutionExpectation) (bool, error)
	})
	if !ok {
		return false, errors.New("Task execution start receipt reader unavailable")
	}
	return store.TaskExecutionStartReplayForOperator(ctx, projectID, taskID, action, actor.PrincipalID(), requestID, expected)
}

func (f *Facade) StartApprovedExecution(ctx context.Context, sessionID, executionID string) (executionproposal.StartResult, error) {
	e, err := f.deps.ExecutionProposals.Get(ctx, sessionID, executionID)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	t, err := f.deps.Tasks.Get(ctx, e.TaskID)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	if t.CurrentExecutionID != executionID {
		return executionproposal.StartResult{}, executionproposal.ErrConflict
	}
	return f.StartTaskExecutionExpected(ctx, sessionID, t.ID, wp.TaskExecutionExpectation{FeatureAreaID: t.FeatureAreaID, TaskDefinitionRevisionID: e.TaskDefinitionRevisionID, ExecutionID: e.ID, Phase: t.Phase, PrimaryMemberID: e.PrimaryMemberID, ApprovedWorksetHash: e.ApprovedWorksetHash})
}

// applyProgressionPresentation keeps every UI a mechanical consumer of Core.
func applyProgressionPresentation(out *presentation.Projection, g wp.Guidance) {
	out.SourceGeneration = g.Generation
	out.SourceSequence = g.SourceSequence
	out.Generation = g.Generation
	out.WorkflowMode = g.WorkflowMode
	out.LatestReviewStatus = g.LatestReviewStatus
	out.CurrentWorkflowReview = g.CurrentWorkflowReview
	out.Guidance = &g
	out.CurrentState = g.CurrentState
	out.NextAction = g.NextAction
	if g.TaskID != "" {
		out.Details.TaskID = g.TaskID
		out.Details.ReviewID = g.ReviewID
		out.Details.TaskDefinitionRevisionID = g.TaskDefinitionRevisionID
		out.Details.ExecutionID = g.ExecutionID
		out.Details.RunID = g.RunID
	}
	if out.Recovery != nil && out.Recovery.Required {
		return
	}
	if out.PendingDecision != nil && (out.PendingDecision.Kind == "APPROVAL" || out.PendingDecision.Kind == "RUNTIME_APPROVAL") {
		return
	}
	out.CurrentTask = g.CurrentTask
	out.LatestTask = g.LatestTask
	out.CurrentWorkspaceID = g.CurrentWorkspaceID
	out.WorkflowStatus = g.CurrentState
	out.PendingDecision = nil
	if g.Approval != nil && g.Approval.Status == "PENDING" && (g.TaskPhase == "REVIEW_APPROVAL_PENDING" || g.NextAction.Code == "REVIEW_RESULT_RESOLUTION_REQUIRED") {
		a := g.Approval
		kind := wp.KindReviewApproval
		if g.NextAction.Code == "REVIEW_RESULT_RESOLUTION_REQUIRED" {
			kind = wp.KindInvalidReviewResultResolution
		}
		out.PendingDecision = &presentation.Decision{CreatedAt: a.CreatedAt, Kind: kind, ID: a.DecisionID, SubjectID: a.TaskID, Summary: a.Summary, AllowedActions: a.AllowedActions, Guarantee: "DURABLE_REQUEST_RECEIPT"}
	}
	if g.NextAction.Code == "RESOLVE_TASK_ACCEPTANCE" && g.ExecutionID != "" {
		out.PendingDecision = &presentation.Decision{Kind: "TASK_ACCEPTANCE", ID: g.DecisionID, SubjectID: g.ExecutionID, Summary: "Read this exact Execution result and its current successor or acceptance eligibility before the user decides.", AllowedActions: []string{wp.AcceptanceAccept, wp.AcceptanceReject, wp.AcceptanceRequestChanges, wp.AcceptanceRequestReverification}, Guarantee: "DURABLE_REQUEST_RECEIPT"}
	}
	// Preserve canonical stop/recovery actions. New advancement is derived here.
	kept := out.Capabilities[:0]
	for _, c := range out.Capabilities {
		switch c.Action {
		case "REFRESH", "VIEW_DETAILS", "RETIRE", "RECOVER", "CANCEL_EXECUTION", "RESET_AGENT_CONTEXT":
			kept = append(kept, c)
		}
	}
	out.Capabilities = kept
	if g.CanCreateTask() {
		out.Capabilities = append(out.Capabilities, presentation.Capability{Action: "CREATE_TASK", Available: true})
	}
	if g.CurrentState == "FEATURE_AREA_IDLE" {
		out.CurrentPhase = "IDLE"
		out.Blockers = nil
		out.Progress = nil
		return
	}
	if out.PendingDecision != nil && out.PendingDecision.Kind != "TASK_ACCEPTANCE" {
		for _, option := range out.PendingDecision.AllowedActions {
			decisionOption := option
			if option == "REQUEST_RE_REVIEW" {
				decisionOption = "REVISE"
			}
			if option == "ABANDON_TASK" {
				decisionOption = "REJECT"
			}
			out.Capabilities = append(out.Capabilities, presentation.Capability{Action: option, Available: true, DecisionOption: decisionOption, Target: &presentation.ActionTarget{TaskID: g.TaskID, FeatureAreaID: g.FeatureAreaID, DecisionID: g.DecisionID, SubjectID: g.TaskID}})
		}
	}
	if out.PendingDecision != nil && out.PendingDecision.Kind == wp.KindInvalidReviewResultResolution {
		for _, action := range g.AvailableActions {
			if action == "OPEN_PROJECT_REPOSITORIES" {
				out.Capabilities = append(out.Capabilities, presentation.Capability{Action: action, Available: true, Target: &presentation.ActionTarget{TaskID: g.TaskID, FeatureAreaID: g.FeatureAreaID, SubjectID: g.TaskID}})
			}
		}
	}

	if g.CurrentTask != nil && g.CurrentTask.Status == string(task.StatusActive) {
		switch g.NextAction.Code {
		case "CONFIRM_IMPLEMENTATION", "CONFIRM_AND_START_IMPLEMENTATION", "START_TASK_REVIEW", "START_EXECUTION", "START_VERIFICATION":
			out.Capabilities = append(out.Capabilities, presentation.Capability{Action: g.NextAction.Code, Available: true, Target: &presentation.ActionTarget{TaskID: g.TaskID, FeatureAreaID: g.FeatureAreaID, SubjectID: g.TaskID, ExecutionID: g.ExecutionID, TaskDefinitionRevisionID: g.TaskDefinitionRevisionID}})
		}
	}

	status := task.StatusActive
	if g.LatestTask != nil {
		status = task.Status(g.LatestTask.Status)
	}
	out.Progress = presentation.TaskPhaseProgress(g.TaskPhase, status)
	out.CurrentPhase = string(g.TaskPhase)
	for _, step := range out.Progress {
		if step.Status == "CURRENT" {
			out.CurrentPhase = step.Phase
			break
		}
	}

}

func (f *Facade) WorkflowGuidanceForResource(ctx context.Context, s wp.ResourceScope) (wp.Guidance, error) {
	if s.AISessionID != "" {
		project, err := f.sessionProject(ctx, s.AISessionID)
		if err != nil {
			return wp.Guidance{}, err
		}
		if s.ProjectID != "" && s.ProjectID != project {
			return wp.Guidance{}, notFound()
		}
		s.ProjectID = project
	}
	switch s.OwnerType {
	case "TASK":
		s.TaskID = s.OwnerID
	case "REVIEW":
		s.ReviewID = s.OwnerID
	case "EXECUTION":
		s.ExecutionID = s.OwnerID
	}
	var project, area string
	if s.RunID != "" {
		r, err := f.deps.Runs.Get(ctx, s.RunID)
		if err != nil {
			return wp.Guidance{}, err
		}
		project, area = r.ProjectID, r.FeatureAreaID
		if s.TaskID != "" && s.TaskID != r.TaskID {
			return wp.Guidance{}, notFound()
		}
		s.TaskID = r.TaskID
	}
	if s.ExecutionID != "" {
		e, err := f.deps.ExecutionProposals.Get(ctx, s.AISessionID, s.ExecutionID)
		if err != nil {
			return wp.Guidance{}, err
		}
		s.TaskID = e.TaskID
	}
	if s.TaskID != "" {
		t, err := f.deps.Tasks.Get(ctx, s.TaskID)
		if err != nil {
			return wp.Guidance{}, err
		}
		project, area = t.ProjectID, t.FeatureAreaID
	}
	if s.ReviewID != "" {
		r, err := f.deps.Reviews.Get(ctx, s.ReviewID)
		if err != nil {
			return wp.Guidance{}, err
		}
		project, area = r.ProjectID, r.FeatureAreaID
		if s.TaskID != "" && s.TaskID != r.TaskID {
			return wp.Guidance{}, notFound()
		}
		s.TaskID = r.TaskID
	}
	if project != "" {
		if s.ProjectID != "" && s.ProjectID != project {
			return wp.Guidance{}, notFound()
		}
		s.ProjectID = project
	}
	if area != "" {
		if s.FeatureAreaID != "" && s.FeatureAreaID != area {
			return wp.Guidance{}, notFound()
		}
		s.FeatureAreaID = area
	}
	if s.ProjectID == "" || f.deps.WorkflowProgression == nil {
		return wp.Guidance{}, errors.New("workflow scope unavailable")
	}
	if s.TaskID != "" {
		reader, ok := f.deps.WorkflowProgression.(interface {
			ReadTask(context.Context, string, string, string) (wp.Guidance, error)
		})
		if !ok {
			return wp.Guidance{}, errors.New("Task guidance unavailable")
		}
		return reader.ReadTask(ctx, s.ProjectID, s.FeatureAreaID, s.TaskID)
	}
	return f.deps.WorkflowProgression.Read(ctx, s.ProjectID, s.FeatureAreaID)
}

// StartTaskExecution addresses only the semantic Task, using its current revision.
func (f *Facade) StartTaskExecution(ctx context.Context, session, taskID string) (executionproposal.StartResult, error) {
	if _, err := f.sessionProject(ctx, session); err != nil {
		return executionproposal.StartResult{}, err
	}
	store, ok := f.deps.WorkflowProgression.(interface {
		ReserveTaskExecution(context.Context, string, string) (executionproposal.Proposal, run.Run, error)
	})
	if !ok {
		return executionproposal.StartResult{}, errors.New("Task progression unavailable")
	}
	value, reserved, err := store.ReserveTaskExecution(ctx, session, taskID)
	if err != nil {
		return executionproposal.StartResult{}, MapError(err)
	}
	value, reserved, err = f.prepareAdmittedTaskExecution(ctx, session, value, reserved)
	if err != nil {
		return executionproposal.StartResult{Proposal: value, Run: reserved}, MapError(err)
	}
	starter, ok := f.deps.ExecutionProposals.(interface {
		StartAuthorizedTurn(context.Context, string, executionproposal.Proposal, run.Run) (executionproposal.StartResult, error)
	})
	if !ok {
		return executionproposal.StartResult{Proposal: value, Run: reserved}, errors.New("reserved Execution handoff unavailable")
	}
	return starter.StartAuthorizedTurn(ctx, session, value, reserved)
}
