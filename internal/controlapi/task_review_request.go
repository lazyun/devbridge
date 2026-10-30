package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func (f *Facade) RequestTaskReview(ctx context.Context, in wp.TaskReviewRequestInput) (wp.TaskReviewRequestResolution, error) {
	if _, err := f.sessionProject(ctx, in.AISessionID); err != nil {
		return wp.TaskReviewRequestResolution{}, err
	}
	store, ok := f.deps.WorkflowProgression.(wp.TaskReviewRequestStore)
	if !ok {
		return wp.TaskReviewRequestResolution{}, errors.New("Task supplemental Review unavailable")
	}
	return store.RequestTaskReview(ctx, in)
}

// RequestTaskReviewForOperator records the same supplemental-review reducer
// with a durable local actor source. It never converts an operator into an AI
// Session, and therefore cannot inherit a Session-owned Direct File grant.
func (f *Facade) RequestTaskReviewForOperator(ctx context.Context, projectID, taskID, action string, in wp.TaskReviewRequestInput) (wp.TaskReviewRequestResolution, error) {
	if action != wp.ActionRequestTaskReview {
		return wp.TaskReviewRequestResolution{}, invalid("Unsupported operator supplemental Review action")
	}
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, action)
	if err != nil {
		return wp.TaskReviewRequestResolution{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return wp.TaskReviewRequestResolution{}, err
	}
	if in.AISessionID != "" || in.OperatorPrincipalID != "" || in.TaskID != taskID {
		return wp.TaskReviewRequestResolution{}, invalid("Operator supplemental Review cannot supply a Session or actor")
	}
	in.RequestID, err = localoperator.RequestID(ctx, projectID, taskID, action, in.RequestID)
	if err != nil {
		return wp.TaskReviewRequestResolution{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	in.OperatorPrincipalID = actor.PrincipalID()
	store, ok := reader.deps.WorkflowProgression.(wp.TaskReviewRequestStore)
	if !ok {
		return wp.TaskReviewRequestResolution{}, errors.New("Task supplemental Review unavailable")
	}
	return store.RequestTaskReview(ctx, in)
}

func (f *Facade) TaskReviewRequestReplayForOperator(ctx context.Context, projectID, taskID string, in wp.TaskReviewRequestInput) (bool, error) {
	ctx, err := localoperator.BindTask(ctx, projectID, taskID, wp.ActionRequestTaskReview)
	if err != nil {
		return false, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, projectID)
	if err != nil {
		return false, err
	}
	if in.AISessionID != "" || in.OperatorPrincipalID != "" || in.TaskID != taskID {
		return false, invalid("Operator supplemental Review cannot supply a Session or actor")
	}
	actor, _ := localoperator.FromContext(ctx)
	in.OperatorPrincipalID = actor.PrincipalID()
	store, ok := reader.deps.WorkflowProgression.(wp.TaskReviewRequestReplayStore)
	if !ok {
		return false, errors.New("Operator supplemental Review replay unavailable")
	}
	return store.TaskReviewRequestReplayForOperator(ctx, projectID, in)
}

// Eligibility is read from the command owner after all independent projection
// reads. Its exact target must still match this Task snapshot; availability is
// not a transferable authorization and the write transaction repeats admission.
func (f *Facade) enrichTaskReviewRequest(ctx context.Context, sessionID string, t task.Task, out *p.Projection) error {
	if out.TaskView == nil || t.Type != task.TypeDevelopment || t.LegacyReadOnly {
		return nil
	}
	store, ok := f.deps.WorkflowProgression.(wp.TaskReviewRequestStore)
	if !ok {
		return errors.New("Task supplemental Review eligibility unavailable")
	}
	var fact wp.TaskReviewRequestEligibility
	var err error
	if f.operatorProject != "" {
		reader, ok := f.deps.WorkflowProgression.(interface {
			TaskReviewRequestEligibilityForOperator(context.Context, string, string) (wp.TaskReviewRequestEligibility, error)
		})
		if !ok {
			return errors.New("Operator supplemental Review eligibility unavailable")
		}
		fact, err = reader.TaskReviewRequestEligibilityForOperator(ctx, f.operatorProject, t.ID)
	} else {
		fact, err = store.TaskReviewRequestEligibility(ctx, sessionID, t.ID)
	}
	if err != nil {
		return err
	}
	x := fact.Target
	if x != nil && (x.Phase != t.Phase || x.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || x.CurrentReviewID != t.CurrentReviewID || x.CurrentExecutionID != t.CurrentExecutionID) {
		return errors.New("Task supplemental Review target changed; refresh required")
	}
	out.TaskView.CurrentReviewRequest = fact.CurrentRequest
	for i := range out.TaskView.ExecutionHistory {
		h := &out.TaskView.ExecutionHistory[i]
		h.SupersededByReviewRequestID = fact.SupersededExecutions[h.Execution.ID]
	}
	if t.Phase == task.PhaseReviewRequired && fact.CurrentRequest != nil {
		out.TaskView.PhaseLabel = "待补充审查"
		out.TaskView.PhaseDescription = "已请求补充审查，尚未启动新的 Review。"
		out.TaskView.Activity.Summary = out.TaskView.PhaseDescription
		out.NextAction.Message = out.TaskView.PhaseDescription
		if out.Guidance != nil {
			out.Guidance.NextAction.Message = out.TaskView.PhaseDescription
		}
	}
	if t.Status != task.StatusActive {
		return nil
	}
	if x == nil {
		if fact.Reason != "" {
			out.Capabilities = append(out.Capabilities, p.Capability{Action: wp.ActionRequestTaskReview, Available: false, Reason: fact.Reason})
		}
		return nil
	}
	floor := t.RequiredReviewDepth
	if fact.Available {
		floor, err = f.preparedReviewDepthFloor(ctx, t, true)
		if err != nil {
			return err
		}
	}
	out.Capabilities = append(out.Capabilities, p.Capability{Action: wp.ActionRequestTaskReview, Available: fact.Available, Reason: fact.Reason, RequiredReviewDepth: floor, AllowedReviewDepths: task.AllowedReviewDepths(floor), RequiredInputs: []string{"reviewPurpose"}, Target: &p.ActionTarget{TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, SubjectID: t.ID, ExpectedReviewRequest: x}})
	return nil
}
