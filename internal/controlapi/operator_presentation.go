package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/taskplan"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

// operatorReader adapts already authorized Project facts to the shared
// projection. The empty internal Session parameter is never passed to Session
// storage, and this private clone cannot expose Session-only file authority.
func (f *Facade) operatorReader(ctx context.Context, projectID string) (context.Context, *Facade, error) {
	ctx, err := localoperator.BindProject(ctx, projectID)
	if err != nil {
		return ctx, nil, err
	}
	if err = localoperator.RequireProject(ctx, projectID); err != nil {
		return ctx, nil, err
	}
	if _, err = f.deps.Projects.Get(ctx, projectID); err != nil {
		return ctx, nil, MapError(err)
	}
	reader := *f
	reader.operatorProject = projectID
	reader.deps.FeatureAreas = operatorAreaReads{FeatureAreaService: f.deps.FeatureAreas, project: projectID}
	reader.deps.ExecutionProposals = operatorExecutionReads{ExecutionProposalService: f.deps.ExecutionProposals, project: projectID}
	reader.deps.TaskPlans = operatorPlanReads{TaskPlanService: f.deps.TaskPlans, project: projectID}
	// An operator sees Task phase/mode and reports through Project facts; it does
	// not receive an AI Session's live file access grants or workspace handles.
	reader.deps.DirectFiles = nil
	return ctx, &reader, nil
}

func (f *Facade) TaskPresentationForOperator(ctx context.Context, project, area, taskID string) (p.Projection, error) {
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return p.Projection{}, err
	}
	reader.deps.FeatureAreas = operatorAreaReads{FeatureAreaService: f.deps.FeatureAreas, project: project, current: area}
	return reader.TaskPresentationForSession(ctx, "", area, taskID)
}
func (f *Facade) FeatureAreaPresentationForOperator(ctx context.Context, project, area string) (p.Projection, error) {
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return p.Projection{}, err
	}
	reader.deps.FeatureAreas = operatorAreaReads{FeatureAreaService: f.deps.FeatureAreas, project: project, current: area}
	return reader.FeatureAreaPresentationForSession(ctx, "", area)
}
func (f *Facade) FeatureAreaRetireCapabilityForOperator(ctx context.Context, project, area string) (p.Capability, error) {
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return p.Capability{}, err
	}
	cap, err := reader.FeatureAreaRetireCapabilityForSession(ctx, "", area)
	if err == nil && cap.Available {
		cap.Available = false
		cap.Reason = "OPERATOR_ACTION_IDENTITY_UNAVAILABLE: retirement still requires a persisted independent operator actor"
	}
	return cap, err
}
func (f *Facade) ExecutionCancellationPresentationForOperator(ctx context.Context, project, execution string) (p.Capability, *p.Cancellation, error) {
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return p.Capability{}, nil, err
	}
	return reader.ExecutionCancellationPresentationForSession(ctx, "", execution)
}
func (f *Facade) ListExecutionCancellationsForOperator(ctx context.Context, project, execution string) ([]executioncancel.Receipt, error) {
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return nil, err
	}
	return reader.ListExecutionCancellationsForSession(ctx, "", execution)
}

type operatorAreaReads struct {
	FeatureAreaService
	project string
	current string
}

func (s operatorAreaReads) GetForSession(ctx context.Context, session, id string) (featurearea.FeatureArea, error) {
	if session != "" {
		return featurearea.FeatureArea{}, invalid("Operator read does not accept a Session")
	}
	if err := localoperator.RequireProject(ctx, s.project); err != nil {
		return featurearea.FeatureArea{}, err
	}
	reader, ok := s.FeatureAreaService.(interface {
		GetHistoryForProject(context.Context, string, string) (featurearea.FeatureArea, error)
	})
	if !ok {
		return featurearea.FeatureArea{}, errors.New("Project FeatureArea read unavailable")
	}
	return reader.GetHistoryForProject(ctx, s.project, id)
}
func (s operatorAreaReads) GetHistoryForSession(ctx context.Context, session, id string) (featurearea.FeatureArea, error) {
	return s.GetForSession(ctx, session, id)
}
func (s operatorAreaReads) ListForSession(ctx context.Context, session string) ([]featurearea.FeatureArea, error) {
	if session != "" {
		return nil, invalid("Operator read does not accept a Session")
	}
	if err := localoperator.RequireProject(ctx, s.project); err != nil {
		return nil, err
	}
	reader, ok := s.FeatureAreaService.(interface {
		ListHistoryByProject(context.Context, string, int) ([]featurearea.FeatureArea, error)
	})
	if !ok {
		return nil, errors.New("Project FeatureArea inventory unavailable")
	}
	return reader.ListHistoryByProject(ctx, s.project, MaxListLimit)
}

func (s operatorAreaReads) CurrentForSession(ctx context.Context, session string) (featurearea.FeatureArea, error) {
	if session != "" || s.current == "" {
		return featurearea.FeatureArea{}, invalid("Operator read requires an exact FeatureArea")
	}
	return s.GetForSession(ctx, "", s.current)
}

type operatorExecutionReads struct {
	ExecutionProposalService
	project string
}

func (s operatorExecutionReads) Get(ctx context.Context, session, id string) (executionproposal.Proposal, error) {
	if session != "" {
		return executionproposal.Proposal{}, invalid("Operator read does not accept a Session")
	}
	if err := localoperator.RequireProject(ctx, s.project); err != nil {
		return executionproposal.Proposal{}, err
	}
	reader, ok := s.ExecutionProposalService.(interface {
		GetForProject(context.Context, string, string) (executionproposal.Proposal, error)
	})
	if !ok {
		return executionproposal.Proposal{}, errors.New("Project Execution read unavailable")
	}
	return reader.GetForProject(ctx, s.project, id)
}
func (s operatorExecutionReads) ListByTask(ctx context.Context, id string) ([]executionproposal.Proposal, error) {
	if err := localoperator.RequireProject(ctx, s.project); err != nil {
		return nil, err
	}
	reader, ok := s.ExecutionProposalService.(interface {
		ListByTask(context.Context, string) ([]executionproposal.Proposal, error)
	})
	if !ok {
		return nil, errors.New("Execution history unavailable")
	}
	values, err := reader.ListByTask(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, v := range values {
		if v.ProjectID != s.project || v.TaskID != id {
			return nil, notFound()
		}
	}
	return values, nil
}

type operatorPlanReads struct {
	TaskPlanService
	project string
}

func (s operatorPlanReads) Get(ctx context.Context, session, id string) (taskplan.Plan, error) {
	if session != "" {
		return taskplan.Plan{}, invalid("Operator read does not accept a Session")
	}
	if err := localoperator.RequireProject(ctx, s.project); err != nil {
		return taskplan.Plan{}, err
	}
	reader, ok := s.TaskPlanService.(interface {
		ListByProject(context.Context, string) ([]taskplan.Plan, error)
	})
	if !ok {
		return taskplan.Plan{}, errors.New("Project plan history unavailable")
	}
	values, err := reader.ListByProject(ctx, s.project)
	if err != nil {
		return taskplan.Plan{}, err
	}
	for _, v := range values {
		if v.ID == id && v.ProjectID == s.project {
			return v, nil
		}
	}
	return taskplan.Plan{}, notFound()
}

func (f *Facade) DevelopmentWorkspaceForOperator(ctx context.Context, project, area, taskID string) (p.WorkspaceProjection, error) {
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return p.WorkspaceProjection{}, err
	}
	return reader.DevelopmentWorkspaceForSession(ctx, "", area, taskID)
}

// Only expose commands with independently persisted operator actor contracts.
// Supplemental review requests have their own operator receipt and do not
// borrow the historical AI Session or its file grants.
func operatorTaskActionSupported(action string) bool {
	switch action {
	case "CONTINUE_TASK", "COMPLETE_TASK", "DEFER_TASK", "REQUEST_TASK_REVIEW", "START_TASK_REVIEW", "APPROVE_AND_START", "START_EXECUTION", "START_VERIFICATION", "CANCEL_EXECUTION", "RECOVER", "RETRY_TASK_COMMIT", "STOP_AND_REVISE":
		return true
	}
	return false
}

func (f *Facade) applyOperatorActionBoundary(ctx context.Context, out *p.Projection) error {
	reader, ok := f.deps.WorkflowProgression.(interface {
		OperatorFileAuthorityBlocked(context.Context, string, string) (bool, error)
	})
	if !ok {
		return errors.New("Project file authority qualification unavailable")
	}
	blocked, err := reader.OperatorFileAuthorityBlocked(ctx, f.operatorProject, out.Details.FeatureAreaID)
	if err != nil {
		return err
	}
	for i := range out.Capabilities {
		c := &out.Capabilities[i]

		if c.Action == "APPROVE_AND_START" {
			c.Available = false
			c.Reason = operatorStartRequiresConfirmation + ": " + localApproveAndStartRequiresConfirmationFailure().Message
		}
		if c.Action == "START_EXECUTION" && out.TaskView != nil && out.TaskView.CurrentReview == nil && out.TaskView.CurrentExecution == nil && out.TaskView.ImplementationMode == "CODING_AGENT" {
			owner, readErr := f.deps.Tasks.Get(ctx, out.Details.TaskID)
			if readErr != nil {
				return readErr
			}
			if owner.ReviewApprovalID == "" {
				c.Available = false
				c.Reason = "请回到 AI 对话，通过启动确认卡片选择运行配置并启动已确认的方案。"
			}
		}
		if c.Action == "START_EXECUTION" && out.TaskView != nil && out.TaskView.ImplementationMode == "CODING_AGENT" && out.TaskView.ExecutionStartTarget != nil && out.TaskView.ExecutionStartTarget.ApprovedWorksetHash != "" && (out.TaskView.CurrentExecution == nil || out.TaskView.CurrentExecution.TaskDefinitionRevisionID != out.TaskView.Details.TaskDefinitionRevisionID) {
			c.Available = false
			c.Reason = operatorStartRequiresConfirmation + ": " + operatorStartRequiresConfirmationFailure().Message
		}
		if taskActionKind(c.Action) && !operatorTaskActionSupported(c.Action) || c.Action == "RETIRE" || c.Action == "CREATE_TASK" {
			c.Available = false
			c.Reason = "OPERATOR_ACTION_IDENTITY_UNAVAILABLE: this command requires an independently persisted operator actor contract"
		}
		if blocked {
			switch c.Action {
			case "START_EXECUTION", "START_VERIFICATION", "START_REVIEW", "START_TASK_REVIEW", "RESET_AGENT_CONTEXT", "COMMIT_FEATURE_AREA", "RETRY_TASK_COMMIT", "RECOVER_FEATURE_AREA_MERGE", "REPREPARE_FEATURE_AREA_MERGE":
				c.Available = false
				c.Reason = "DIRECT_FILE_CAPABILITY_ACTIVE"
			}
		}
	}
	return nil
}

func (f *Facade) ReviewReportForOperator(ctx context.Context, project, reviewID string) (review.StoredReport, error) {
	ctx, err := localoperator.BindProject(ctx, project)
	if err != nil {
		return review.StoredReport{}, MapError(err)
	}
	reader, ok := f.deps.UpperReviewResults.(interface {
		GetReviewReportForOperator(context.Context, string, string) (review.StoredReport, error)
	})
	if !ok {
		return review.StoredReport{}, errors.New("Project Review result read unavailable")
	}
	value, err := reader.GetReviewReportForOperator(ctx, project, reviewID)
	if err != nil {
		return value, MapError(err)
	}
	return value, nil
}

func sealOperatorTaskActions(out *p.Projection) {
	if out.TaskView == nil {
		return
	}
	for i := range out.TaskView.Actions {
		c := &out.TaskView.Actions[i]
		if c.Action == "COMPLETE_TASK" && out.TaskView.ImplementationMode == "DIRECT_FILE" {
			c.Available = false
			c.Reason = "文件实施的验收与合入需要在原 AI 对话中正式确认。"
			if d := out.TaskView.VerificationDecisions; d != nil {
				for j := range d.Choices {
					if d.Choices[j].Action == c.Action {
						d.Choices[j].Available = false
						d.Choices[j].Reason = c.Reason
					}
				}
			}
		}

		if !operatorTaskActionSupported(c.Action) {
			c.Available = false
			c.Reason = "OPERATOR_ACTION_IDENTITY_UNAVAILABLE: this command requires an independently persisted operator actor contract"
		}
	}
	for i := range out.Capabilities {
		c := &out.Capabilities[i]
		if taskActionKind(c.Action) && !operatorTaskActionSupported(c.Action) {
			c.Available = false
			c.Reason = "OPERATOR_ACTION_IDENTITY_UNAVAILABLE: this command requires an independently persisted operator actor contract"
		}
	}
	if d := out.PendingDecision; d != nil && (d.Kind == "REVIEW_APPROVAL" || d.Kind == "TASK_ACCEPTANCE") {
		d.AllowedActions = []string{}
	}
}

func (s operatorExecutionReads) GetForProject(ctx context.Context, project, id string) (executionproposal.Proposal, error) {
	if project != s.project {
		return executionproposal.Proposal{}, notFound()
	}
	return s.Get(ctx, "", id)
}
