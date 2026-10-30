package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/localoperator"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"strings"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/task"
)

type taskVerificationDecisionsStore interface {
	PrepareTaskCompletionPlan(context.Context, string, string, string, string) (delivery.TaskCompletionPlan, error)
	ReadTaskCompletion(context.Context, string, string) (*delivery.TaskCompletionStatus, error)
	FinishTaskCompletion(context.Context, string, string) (task.Task, error)
	ReadTaskHold(context.Context, string, string) (*task.Hold, error)
	ReadTaskContinuation(context.Context, string, string) (bool, error)
	ChangeTaskHold(context.Context, task.HoldInput) (task.Task, error)
}

func verificationTaskChoice(action string) bool {
	return action == "COMPLETE_TASK" || action == "DEFER_TASK" || action == "CONTINUE_TASK"
}

func (f *Facade) ChangeTaskHoldForSession(ctx context.Context, session string, in task.HoldInput) (task.Task, error) {
	project, err := f.sessionProject(ctx, session)
	if err != nil {
		return task.Task{}, err
	}
	if in.ProjectID != "" && in.ProjectID != project {
		return task.Task{}, notFound()
	}
	in.ProjectID, in.Actor = project, "session:"+session
	store, ok := f.deps.WorkflowProgression.(taskVerificationDecisionsStore)
	if !ok {
		return task.Task{}, errors.New("Task verification decisions unavailable")
	}
	return store.ChangeTaskHold(ctx, in)
}

func (f *Facade) validateVerificationChoiceConfirmation(ctx context.Context, in *confirmation.PrepareInput, project string) error {
	b := in.Execution
	if b == nil || b.Action != in.Action || b.TaskID == "" || b.FeatureAreaID == "" || b.ExecutionID == "" || b.TaskDefinitionRevisionID == "" || len(in.Variants) != 1 || in.RecommendedRuntimeConfig != nil {
		return invalid("Exact Task verification choice required")
	}
	for _, v := range in.Variants {
		if v.WorkSpec != nil || v.VerificationSpec != nil || v.ExecutionInstructions != nil || v.ImplementationMode != "" || v.ExecutorKind != "" || v.ReviewInstruction != "" || len(v.RepositoryTargets) > 0 {
			return invalid("Task verification choice cannot replace implementation content")
		}
	}
	t, err := f.deps.Tasks.Get(ctx, b.TaskID)
	if err != nil {
		return err
	}
	if t.ProjectID != project || t.FeatureAreaID != b.FeatureAreaID || t.CurrentExecutionID != b.ExecutionID || t.CurrentDefinitionRevisionID != b.TaskDefinitionRevisionID || t.Status != task.StatusActive || t.LegacyReadOnly {
		return invalid("Task verification choice target changed")
	}
	store, ok := f.deps.WorkflowProgression.(taskVerificationDecisionsStore)
	if !ok {
		return invalid("Task verification decisions unavailable")
	}
	if in.Action == "COMPLETE_TASK" {
		if b.DecisionID != wp.TaskAcceptanceDecisionID(b.ExecutionID) {
			return invalid("Exact Task acceptance decision required")
		}
		fresh, e := store.PrepareTaskCompletionPlan(ctx, project, b.TaskID, b.ExecutionID, b.TaskDefinitionRevisionID)
		if e != nil {
			return e
		}
		if b.CompletionPlan != nil && b.CompletionPlan.SHA256 != fresh.SHA256 {
			return invalid("Task completion source or development target changed")
		}
		b.CompletionPlan = &fresh
		return nil
	}
	if b.CompletionPlan != nil {
		return invalid("Only completion can authorize integration")
	}
	if in.Action == "DEFER_TASK" && (strings.TrimSpace(b.Note) == "" || len(b.Note) > 8192) {
		return invalid("Please provide the checkpoint and remaining work for the user to confirm")
	}
	view, e := f.taskView(ctx, t)
	if e != nil {
		return e
	}
	if view.BusinessPhase != task.PhaseVerifying && t.Phase != task.PhaseVerifying && t.Phase != task.PhaseAcceptancePending {
		return invalid("Task is not at a verification checkpoint")
	}
	if view.VerificationRun != nil && !terminalVerificationTransport(view.VerificationRun.TransportStatus) {
		return invalid("Wait for the current verification run to stop before saving this checkpoint")
	}
	return nil
}

func terminalVerificationTransport(status string) bool {
	switch status {
	case "SUCCEEDED", "FAILED", "CANCELED", "INFRASTRUCTURE_FAILED":
		return true
	}
	return false
}

func (f *Facade) attachVerificationDecisions(ctx context.Context, v *presentation.TaskView, t task.Task) error {
	store, ok := f.deps.WorkflowProgression.(taskVerificationDecisionsStore)
	if !ok || t.Type != task.TypeDevelopment || t.CurrentExecutionID == "" || t.CurrentDefinitionRevisionID == "" {
		return nil
	}
	completion, err := store.ReadTaskCompletion(ctx, t.ProjectID, t.ID)
	if err != nil {
		return err
	}
	phase := v.BusinessPhase
	if phase == "" {
		phase = t.Phase
	}
	if completion == nil && (t.Status != task.StatusActive || t.LegacyReadOnly || (phase != task.PhaseVerifying && t.Phase != task.PhaseAcceptancePending)) {
		return nil
	}
	hold, err := store.ReadTaskHold(ctx, t.ProjectID, t.ID)
	if err != nil {
		return err
	}
	continued, err := store.ReadTaskContinuation(ctx, t.ProjectID, t.ID)
	if err != nil {
		return err
	}
	v.VerificationDecisions = &presentation.VerificationDecisions{ContinuationConfirmed: continued, TaskID: t.ID, ExecutionID: t.CurrentExecutionID, RevisionID: t.CurrentDefinitionRevisionID, Hold: hold, Completion: completion, Choices: []presentation.VerificationChoice{}}
	return nil
}

func attachVerificationChoiceActions(out *presentation.Projection, t task.Task) {
	v := out.TaskView
	if v == nil || v.VerificationDecisions == nil {
		return
	}
	d := v.VerificationDecisions
	allowed := v.ActionsStatus == "AVAILABLE" && !v.ReadOnly && !v.Terminal && t.Status == task.StatusActive
	idle := v.VerificationRun == nil || terminalVerificationTransport(v.VerificationRun.TransportStatus)
	complete := false
	for _, cap := range v.Actions {
		if cap.Action == "ACCEPT" && cap.Available {
			complete = true
		}
	}
	complete = allowed && complete && presentation.VerificationCompletionReady(*v) && d.Hold == nil && d.Completion == nil
	choices := []presentation.VerificationChoice{{Action: "CONTINUE_TASK", Label: "延续任务", Available: allowed && idle && d.Completion == nil}, {Action: "COMPLETE_TASK", Label: "完成并合入", Available: complete}, {Action: "DEFER_TASK", Label: "暂存任务", Available: allowed && idle && d.Hold == nil && d.Completion == nil}}
	for i := range choices {
		c := &choices[i]
		if !c.Available {
			switch {
			case d.Completion != nil:
				c.Reason = "任务已进入交付，请处理原合入记录。"
			case d.Hold != nil:
				c.Reason = "任务已暂存，请先延续原任务。"
			case !idle:
				c.Reason = "等待当前验证运行结束后处理。"
			case c.Action == "COMPLETE_TASK":
				c.Reason = "必需验证或验收标准尚未通过，不能完成并合入。"
			default:
				c.Reason = "当前任务资格暂不可用，请刷新。"
			}
		}
		out.TaskView.Actions = append(out.TaskView.Actions, presentation.Capability{Action: c.Action, Available: c.Available, Reason: c.Reason, Target: &presentation.ActionTarget{TaskID: t.ID, FeatureAreaID: t.FeatureAreaID, ExecutionID: t.CurrentExecutionID, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(t.CurrentExecutionID), SubjectID: t.CurrentExecutionID}})
	}
	if d.Completion != nil && d.Completion.State != "COMPLETED" && d.Completion.State != "REPLAN_REQUIRED" {
		found := false
		for i := range v.Actions {
			if v.Actions[i].Action == "RETRY_TASK_COMMIT" {
				found = true
				v.Actions[i].Available = allowed
				v.Actions[i].Reason = ""
			}
		}
		if !found {
			v.Actions = append(v.Actions, presentation.Capability{Action: "RETRY_TASK_COMMIT", Available: allowed, Target: &presentation.ActionTarget{TaskID: t.ID, FeatureAreaID: t.FeatureAreaID}})
		}
	}
	d.Choices = choices
	if d.Hold != nil {
		for i := range v.Actions {
			if v.Actions[i].Action != "CONTINUE_TASK" && v.Actions[i].Available {
				v.Actions[i].Available = false
				v.Actions[i].Reason = "TASK_DEFERRED: 请先延续原任务。"
			}
		}
	}
	presentation.ApplyVerificationDecisions(v)
	out.NextStep = v.NextStep
}

func (f *Facade) PrepareTaskCompletionForOperator(ctx context.Context, project, id string) (delivery.TaskCompletionPlan, error) {
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return delivery.TaskCompletionPlan{}, err
	}
	t, err := reader.deps.Tasks.Get(ctx, id)
	if err != nil || t.ProjectID != project {
		return delivery.TaskCompletionPlan{}, notFound()
	}
	store, ok := reader.deps.WorkflowProgression.(taskVerificationDecisionsStore)
	if !ok {
		return delivery.TaskCompletionPlan{}, invalid("Task completion unavailable")
	}
	return store.PrepareTaskCompletionPlan(ctx, project, id, t.CurrentExecutionID, t.CurrentDefinitionRevisionID)
}

func (f *Facade) ChangeTaskHoldForOperator(ctx context.Context, project string, in task.HoldInput) (task.Task, error) {
	ctx, err := localoperator.BindTask(ctx, project, in.TaskID, in.Action)
	if err != nil {
		return task.Task{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	in.Actor = "operator:" + actor.PrincipalID()
	in.ProjectID = project
	store, ok := f.deps.WorkflowProgression.(taskVerificationDecisionsStore)
	if !ok {
		return task.Task{}, invalid("Task verification choices unavailable")
	}
	return store.ChangeTaskHold(ctx, in)
}

func (f *Facade) CompleteTaskForOperator(ctx context.Context, project string, p delivery.TaskCompletionPlan, request string) (wp.TaskAcceptanceResolution, error) {
	ctx, err := localoperator.BindTask(ctx, project, p.TaskID, "COMPLETE_TASK")
	if err != nil {
		return wp.TaskAcceptanceResolution{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	store, ok := f.deps.WorkflowProgression.(wp.TaskAcceptanceStore)
	if !ok || project != p.ProjectID {
		return wp.TaskAcceptanceResolution{}, notFound()
	}
	return store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{OperatorPrincipalID: actor.PrincipalID(), TaskID: p.TaskID, FeatureAreaID: p.FeatureAreaID, TaskDefinitionRevisionID: p.RevisionID, ExecutionID: p.ExecutionID, DecisionID: wp.TaskAcceptanceDecisionID(p.ExecutionID), Option: "ACCEPT", RequestID: request, CompletionPlan: &p})
}
