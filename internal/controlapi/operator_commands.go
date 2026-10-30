package controlapi

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/task"
)

// These adapters authorize the current operator separately from immutable
// business provenance, then delegate to the original command owners.
func (f *Facade) CancelExecutionForOperator(ctx context.Context, project, taskID string, in executioncancel.Input) (executioncancel.Receipt, error) {
	ctx, err := localoperator.BindTask(ctx, project, taskID, "CANCEL_EXECUTION")
	if err != nil {
		return executioncancel.Receipt{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return executioncancel.Receipt{}, err
	}
	if in.AISessionID != "" || in.OperatorPrincipalID != "" || in.ProjectID != "" && in.ProjectID != project {
		return executioncancel.Receipt{}, invalid("Operator cancellation cannot supply a Session or actor")
	}
	in.RequestID, err = localoperator.RequestID(ctx, project, taskID, "CANCEL_EXECUTION", in.RequestID)
	if err != nil {
		return executioncancel.Receipt{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	in.OperatorPrincipalID = actor.PrincipalID()
	return reader.CancelExecutionForSession(ctx, in)
}
func (f *Facade) RecoverFeatureAreaOperationForOperator(ctx context.Context, project, taskID string, in agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	ctx, err := localoperator.BindTask(ctx, project, taskID, "RECOVER")
	if err != nil {
		return agentconversation.RecoveryResult{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return agentconversation.RecoveryResult{}, err
	}
	if in.OperatorPrincipalID != "" || in.ProjectID != "" && in.ProjectID != project {
		return agentconversation.RecoveryResult{}, invalid("Operator recovery cannot supply an actor")
	}
	in.RequestID, err = localoperator.RequestID(ctx, project, taskID, "RECOVER", in.RequestID)
	if err != nil {
		return agentconversation.RecoveryResult{}, MapError(err)
	}
	actor, _ := localoperator.FromContext(ctx)
	in.OperatorPrincipalID = actor.PrincipalID()
	return reader.RecoverFeatureAreaAgentOperation(ctx, "", in)
}
func (f *Facade) RetryAcceptedTaskCommitForOperator(ctx context.Context, project, taskID string) (task.Task, error) {
	ctx, err := localoperator.BindTask(ctx, project, taskID, "RETRY_TASK_COMMIT")
	if err != nil {
		return task.Task{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return task.Task{}, err
	}
	// The original receipt is already Task/Project scoped. Retrying it creates
	// neither a new acceptance nor a new actor/request receipt.
	return reader.RetryAcceptedTaskCommit(ctx, "", taskID)
}

func (f *Facade) ReprepareAcceptedTaskCommitForOperator(ctx context.Context, project string, in delivery.RetryInput) (delivery.RetryReceipt, error) {
	ctx, err := localoperator.BindTask(ctx, project, in.TaskID, "RETRY_TASK_COMMIT")
	if err != nil {
		return delivery.RetryReceipt{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return delivery.RetryReceipt{}, err
	}
	in.RequestID, err = localoperator.RequestID(ctx, project, in.TaskID, "RETRY_TASK_COMMIT", in.RequestID)
	if err != nil {
		return delivery.RetryReceipt{}, MapError(err)
	}
	return reader.ReprepareAcceptedTaskCommit(ctx, "", in)
}

func (f *Facade) ResolveAgentRequestForOperator(ctx context.Context, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	ctx, err := localoperator.BindProject(ctx, in.ProjectID)
	if err != nil {
		return agentruntime.Snapshot{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, in.ProjectID)
	if err != nil {
		return agentruntime.Snapshot{}, err
	}
	value, _, err := reader.scopedRun(ctx, "", in.RunID)
	if err != nil {
		return agentruntime.Snapshot{}, err
	}
	if value.ProjectID != in.ProjectID || value.FeatureAreaID != in.FeatureAreaID || value.TaskID != in.TaskID || value.ThreadID != in.ThreadID || value.TurnID != in.TurnID {
		return agentruntime.Snapshot{}, MapError(agentruntime.ErrStale)
	}
	if err := rejectUnknownResponse(value, in.RuntimeRequestID, in.PayloadSHA256); err != nil {
		return agentruntime.Snapshot{}, err
	}
	resolver, ok := f.deps.Runs.(interface {
		ResolveAgentRequest(context.Context, agentruntime.Resolution) (agentruntime.Snapshot, error)
	})
	if !ok {
		return agentruntime.Snapshot{}, MapError(agentruntime.ErrStale)
	}
	// Runtime requests keep their exact live connection/request identity. They
	// are not workflow decision receipts and cannot start a new operation.
	ctx = agentruntime.WithCanonicalCancellation(ctx, func() error {
		id := fmt.Sprintf("agent-cancel-%x", sha256.Sum256(append([]byte(in.ConnectionID), in.RequestID...)))
		_, e := f.CancelExecutionForOperator(ctx, in.ProjectID, in.TaskID, executioncancel.Input{OwnerType: string(value.OperationKind), OwnerID: value.OperationID, RunID: value.ID, RequestID: id, Reason: "User canceled a Codex approval"})
		return e
	})
	result, err := resolver.ResolveAgentRequest(ctx, in)
	if err != nil {
		return result, MapError(err)
	}
	return result, nil
}

func (f *Facade) CommitFeatureAreaForOperator(ctx context.Context, project, area, request string) (featurearea.FeatureArea, error) {
	ctx, err := localoperator.BindArea(ctx, project, area, "COMMIT_FEATURE_AREA")
	if err != nil {
		return featurearea.FeatureArea{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	request, err = localoperator.RequestID(ctx, project, "", "COMMIT_FEATURE_AREA", request)
	if err != nil {
		return featurearea.FeatureArea{}, MapError(err)
	}
	return reader.CommitFeatureArea(ctx, "", area, request)
}
func (f *Facade) ResetFeatureAreaAgentContextForOperator(ctx context.Context, project, area, request string) (agentconversation.Conversation, error) {
	ctx, err := localoperator.BindArea(ctx, project, area, "RESET_AGENT_CONTEXT")
	if err != nil {
		return agentconversation.Conversation{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	a, err := reader.GetFeatureAreaForSession(ctx, "", area)
	if err != nil {
		return agentconversation.Conversation{}, err
	}
	if a.Status == featurearea.StatusRetiring {
		return agentconversation.Conversation{}, MapError(featurearea.ErrRetiring)
	}
	if !featurearea.IsUsable(a) {
		return agentconversation.Conversation{}, MapError(featurearea.ErrUnavailable)
	}
	request, err = localoperator.RequestID(ctx, project, "", "RESET_AGENT_CONTEXT", request)
	if err != nil {
		return agentconversation.Conversation{}, MapError(err)
	}
	return reader.ResetFeatureAreaAgentContext(ctx, "", area, request)
}

func (f *Facade) RecoverFeatureAreaMergeForOperator(ctx context.Context, project, area, request string) (featurearea.FeatureArea, error) {
	ctx, err := localoperator.BindArea(ctx, project, area, "RECOVER_FEATURE_AREA_MERGE")
	if err != nil {
		return featurearea.FeatureArea{}, MapError(err)
	}
	_, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return featurearea.FeatureArea{}, err
	}
	store, ok := reader.deps.WorkflowProgression.(interface {
		RecoverFeatureAreaMerge(context.Context, string, string, string) (featurearea.FeatureArea, error)
	})
	if !ok {
		return featurearea.FeatureArea{}, featurearea.ErrConflict
	}
	// This is the original durable parent ID, not a newly namespaced command.
	return store.RecoverFeatureAreaMerge(ctx, project, area, request)
}

func (f *Facade) ReprepareFeatureAreaMergeForOperator(ctx context.Context, project string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error) {
	ctx, err := localoperator.BindArea(ctx, project, in.FeatureAreaID, "REPREPARE_FEATURE_AREA_MERGE")
	if err != nil {
		return delivery.MergeRetryReceipt{}, MapError(err)
	}
	ctx, reader, err := f.operatorReader(ctx, project)
	if err != nil {
		return delivery.MergeRetryReceipt{}, err
	}
	in.RequestID, err = localoperator.RequestID(ctx, project, "", "REPREPARE_FEATURE_AREA_MERGE", in.RequestID)
	if err != nil {
		return delivery.MergeRetryReceipt{}, MapError(err)
	}
	return reader.ReprepareFeatureAreaMerge(ctx, "", in)
}
