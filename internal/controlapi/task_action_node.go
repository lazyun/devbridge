package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/taskaction"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type taskActionNodeReader interface {
	ReadTaskActionNode(context.Context, string, string, string) (taskaction.Node, error)
}

func (f *Facade) attachTaskActionNode(ctx context.Context, session string, out *p.Projection) error {
	v := out.TaskView
	reader, ok := f.deps.Confirmations.(taskActionNodeReader)
	if !ok || v == nil {
		return nil
	}
	node, err := reader.ReadTaskActionNode(ctx, out.Details.ProjectID, v.ID, session)
	if err != nil {
		return err
	}
	v.ActionNode = &node
	if node.Kind == "NO_RUN_RECOVERY" && v.CurrentExecution != nil && v.Details.TaskDefinitionRevisionID != "" {
		v.Actions = append(v.Actions, p.Capability{Action: "STOP_AND_REVISE", Available: true, Target: &p.ActionTarget{TaskID: v.ID, FeatureAreaID: out.Details.FeatureAreaID, ExecutionID: v.CurrentExecution.ID, TaskDefinitionRevisionID: v.Details.TaskDefinitionRevisionID, TaskActionGeneration: node.Generation}})
	}
	// Internal preflight for this exact prepared parent can inspect its own
	// eligibility. Other commands and read surfaces never inherit that context.
	blocked := node.Blocking && !(node.Owned && taskaction.Intent(ctx) == node.IntentID)
	originalRecovery := func(action string) bool { return node.Kind == "NO_RUN_RECOVERY" && action == "STOP_AND_REVISE" }
	for i := range v.Actions {
		if v.Actions[i].Target != nil {
			copy := *v.Actions[i].Target
			copy.TaskActionGeneration = node.Generation
			v.Actions[i].Target = &copy
		}
		if blocked && !originalRecovery(v.Actions[i].Action) {
			v.Actions[i].Available = false
			v.Actions[i].Reason = node.Reason
		}
	}
	for i := range out.Capabilities {
		cap := &out.Capabilities[i]
		if cap.Target != nil && cap.Target.TaskID == v.ID {
			copy := *cap.Target
			copy.TaskActionGeneration = node.Generation
			cap.Target = &copy
			if blocked && !originalRecovery(cap.Action) {
				cap.Available = false
				cap.Reason = node.Reason
			}
		}
	}
	if blocked {
		v.NextStep = nil
		out.NextAction = wp.Action{Code: "RESOLVE_CURRENT_TASK_ACTION", Required: true, Message: node.Reason}
		v.CurrentAction = out.NextAction
		v.Activity.Status, v.Activity.Summary = "WAITING_CURRENT_ACTION", node.Reason
		out.Blockers = append(out.Blockers, node.Reason)
		if d := v.VerificationDecisions; d != nil {
			for i := range d.Choices {
				d.Choices[i].Available = false
				d.Choices[i].Reason = node.Reason
			}
		}
		v.CodexContinuation = nil
	}
	return nil
}

func (f *Facade) confirmationActionCurrent(ctx context.Context, intent confirmation.Intent) (bool, error) {
	if reader, ok := f.deps.Confirmations.(interface {
		TaskActionConfirmationCurrent(context.Context, string, confirmation.Intent) (bool, error)
	}); ok {
		return reader.TaskActionConfirmationCurrent(ctx, intent.AISessionID, intent)
	}
	return true, nil
}

func (f *Facade) ResolveTaskActionConflict(ctx context.Context, in taskaction.ConflictSelection) (confirmation.Intent, error) {
	project, err := f.sessionProject(ctx, in.AISessionID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	t, err := f.deps.Tasks.Get(ctx, in.TaskID)
	if err != nil || t.ProjectID != project {
		return confirmation.Intent{}, notFound()
	}
	store, ok := f.deps.Confirmations.(interface {
		ResolveTaskActionConflict(context.Context, taskaction.ConflictSelection) (confirmation.Intent, error)
	})
	if !ok {
		return confirmation.Intent{}, invalid("Task action conflict resolution unavailable")
	}
	out, err := store.ResolveTaskActionConflict(ctx, in)
	if err != nil {
		return out, err
	}
	read := confirmation.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: confirmation.Purpose(in.Purpose), ContentHash: in.ContentHash}
	if out.Commit != nil {
		read.RequestID = out.Commit.RequestID
	}
	return f.ReadConfirmation(ctx, read)
}
