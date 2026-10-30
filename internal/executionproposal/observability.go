package executionproposal

import (
	"context"

	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

func logReservedRunCreated(ctx context.Context, proposal Proposal, value run.Run) {
	ctx = observability.WithContext(ctx, observability.Correlation{ProjectID: proposal.ProjectID, AISessionID: proposal.AISessionID, TaskID: value.TaskID, ExecutionID: proposal.ID, RunID: value.ID})
	observability.Info(ctx, "run", "run.created", "durable Run created by atomic Execution reservation", map[string]any{"purpose": value.Purpose, "attempt": value.Attempt, "newState": value.Status})
}

func executionContext(ctx context.Context, value Proposal) context.Context {
	return observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: value.AISessionID, TaskID: value.TaskID, ExecutionID: value.ID, RunID: value.RunID})
}

func logExecutionCreated(ctx context.Context, value Proposal, reused bool) {
	ctx = executionContext(ctx, value)
	observability.Info(ctx, "execution", "execution.created", "durable Execution created", map[string]any{"status": value.Status, "taskPlanId": value.TaskPlanID, "reused": reused})
	observability.Info(ctx, "execution", "execution.authorization.presented", "Execution authorization card created", map[string]any{"status": value.Status, "taskPlanId": value.TaskPlanID})
}

func logTaskConfirmed(ctx context.Context, aiSessionID string, plan taskplan.Plan, taskValue task.Task, proposal Proposal, reused bool) {
	ctx = observability.WithContext(ctx, observability.Correlation{ProjectID: taskValue.ProjectID, AISessionID: aiSessionID, FeatureAreaID: taskValue.FeatureAreaID, TaskID: taskValue.ID, ExecutionID: proposal.ID})
	observability.Info(ctx, "task", "task.confirmed", "Task confirmation converged", map[string]any{"taskPlanId": plan.ID, "reusedExecution": reused})
}

func logExecutionAuthorized(ctx context.Context, value Proposal, reused bool) {
	ctx = executionContext(ctx, value)
	observability.Info(ctx, "execution", "execution.authorized", "Execution authorization persisted", map[string]any{"status": value.Status, "reused": reused})
}

func logExecutionOutcome(ctx context.Context, value Proposal) {
	event := "execution.failed"
	switch value.Outcome {
	case OutcomeSucceeded:
		event = "execution.succeeded"
	case OutcomeCanceled:
		event = "execution.canceled"
	}
	logExecutionTerminal(ctx, value, event, value.FailureCode)
}

func logExecutionTerminal(ctx context.Context, value Proposal, event, code string) {
	ctx = executionContext(ctx, value)
	observability.Info(ctx, "execution", event, "Execution reached a terminal outcome", map[string]any{"status": value.Status, "outcome": value.Outcome, "errorCode": code, "errorSummary": value.FailureMessage})
}
