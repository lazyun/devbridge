package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

// GoalProgressTool is the internal dynamic tool Codex uses to report Task
// Goal progress during a goal-bearing IMPLEMENTATION Run.
const GoalProgressTool = "report_goal_progress"

// GoalDynamicToolSpecs returns the report_goal_progress dynamic tool spec
// declared at thread start for goal-bearing IMPLEMENTATION Runs.
func GoalDynamicToolSpecs() []codex.DynamicToolSpec {
	return []codex.DynamicToolSpec{
		{Type: "function", Name: GoalProgressTool, Description: "Report progress for one ordered Task Goal of this execution.", InputSchema: json.RawMessage(`{"type":"object","properties":{"goalId":{"type":"string","minLength":1,"maxLength":128},"status":{"type":"string","enum":["RUNNING","COMPLETED","FAILED","SKIPPED"]}},"required":["goalId","status"],"additionalProperties":false}`)},
	}
}

// FeatureAreaHostToolV1Specs is the complete, stable declaration made when a
// FeatureArea conversation creates its backend thread. Operation-specific
// authorization is enforced by dispatchers, never by changing visibility.
func FeatureAreaHostToolV1Specs() []codex.DynamicToolSpec {
	return append(ReviewDynamicToolSpecs(), GoalDynamicToolSpecs()...)
}

type goalInteractionRunLookup interface {
	GetByInteraction(context.Context, string, string) (run.Run, error)
}

type goalProgressRecorder interface {
	Report(ctx context.Context, executionID, goalID string, status taskgoal.Status) (taskgoal.Progress, error)
	ListForExecution(ctx context.Context, executionID string) ([]taskgoal.Progress, error)
}

// GoalCapabilityDispatcher serves report_goal_progress calls from Codex
// during IMPLEMENTATION Runs. It mirrors ReviewCapabilityDispatcher: it binds
// the interaction to a trusted Run, requires an active goal-bearing
// implementation Run, and applies the transition-guarded goal status.
type GoalCapabilityDispatcher struct {
	Runs          goalInteractionRunLookup
	Goals         goalProgressRecorder
	Conversations agentconversation.Runtime
}

func (d *GoalCapabilityDispatcher) Handle(ctx context.Context, call codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
	logCtx := ctx
	result, handleErr := d.handle(ctx, call)
	if handleErr != nil {
		code, message := goalCapabilityError(handleErr)
		observability.Info(logCtx, "execution", "codex.execution.goal.rejected", "Codex Task Goal progress was rejected", map[string]any{"threadId": call.ThreadID, "turnId": call.TurnID, "errorCode": code})
		return dynamicToolResult(false, map[string]any{"error": map[string]string{"code": code, "message": message}}), nil
	}
	observability.Info(logCtx, "execution", "codex.execution.goal.reported", "Codex reported Task Goal progress", map[string]any{"threadId": call.ThreadID, "turnId": call.TurnID, "goalId": result.GoalID, "status": string(result.Status)})
	return dynamicToolResult(true, map[string]any{"goalId": result.GoalID, "status": string(result.Status)}), nil
}

type goalCapabilityErrorCode string

const (
	goalInvalidArguments   goalCapabilityErrorCode = "INVALID_ARGUMENTS"
	goalUnknownGoal        goalCapabilityErrorCode = "UNKNOWN_GOAL"
	goalInvalidState       goalCapabilityErrorCode = "INVALID_STATE"
	goalInvalidOperation   goalCapabilityErrorCode = "INVALID_STATE_OPERATION"
	goalContextUnavailable goalCapabilityErrorCode = "OPERATION_CONTEXT_UNAVAILABLE"
)

type goalCapabilityErrorValue struct {
	code goalCapabilityErrorCode
	err  error
}

func (e *goalCapabilityErrorValue) Error() string { return e.err.Error() }
func (e *goalCapabilityErrorValue) Unwrap() error { return e.err }
func goalError(code goalCapabilityErrorCode, err error) error {
	return &goalCapabilityErrorValue{code: code, err: err}
}

func goalCapabilityError(err error) (string, string) {
	var typed *goalCapabilityErrorValue
	if errors.As(err, &typed) {
		switch typed.code {
		case goalInvalidArguments:
			return string(typed.code), "Goal progress arguments are invalid."
		case goalUnknownGoal:
			return string(typed.code), "The requested Goal is not part of this execution."
		case goalInvalidState:
			return string(typed.code), "This Goal cannot transition to the requested status."
		case goalInvalidOperation:
			return string(typed.code), "Goal progress is not legal for this operation."
		}
	}
	return string(goalContextUnavailable), "Task Goal progress cannot be recorded for this interaction."
}

type goalReportResult struct {
	GoalID string
	Status taskgoal.Status
}

func (d *GoalCapabilityDispatcher) handle(ctx context.Context, call codex.DynamicToolCallParams) (goalReportResult, error) {
	if d == nil || d.Runs == nil || d.Goals == nil {
		return goalReportResult{}, goalError(goalContextUnavailable, errors.New("goal capability dependencies are incomplete"))
	}
	var input struct {
		GoalID string `json:"goalId"`
		Status string `json:"status"`
	}
	if err := decodeClosedArguments(call.Arguments, &input); err != nil {
		return goalReportResult{}, goalError(goalInvalidArguments, errors.New("invalid goal progress arguments"))
	}
	if strings.TrimSpace(input.GoalID) == "" || len(input.GoalID) > 128 {
		return goalReportResult{}, goalError(goalInvalidArguments, errors.New("invalid goal identity"))
	}
	status := taskgoal.Status(input.Status)
	if !taskgoal.ValidStatus(status) {
		return goalReportResult{}, goalError(goalInvalidArguments, errors.New("invalid goal progress status"))
	}
	runValue, err := d.awaitInteraction(ctx, call.ThreadID, call.TurnID)
	if err != nil {
		return goalReportResult{}, goalError(goalContextUnavailable, err)
	}
	if runValue.Status != run.StatusRunning {
		return goalReportResult{}, goalError(goalContextUnavailable, errors.New("Codex interaction is terminal or unavailable"))
	}
	if d.Conversations != nil {
		claim, claimErr := d.Conversations.AuthorizeRun(ctx, runValue.ID)
		if claimErr != nil {
			return goalReportResult{}, goalError(goalContextUnavailable, claimErr)
		}
		if claim.RunID != runValue.ID {
			return goalReportResult{}, goalError(goalContextUnavailable, errors.New("Codex interaction does not own the active Feature Area operation"))
		}
		if claim.Kind != agentconversation.Execution {
			return goalReportResult{}, goalError(goalInvalidOperation, errors.New("Goal progress is not legal for this operation"))
		}
		ctx = runtimeepoch.WithHostToolFence(ctx, claim.RuntimeEpoch, claim.RunID)
	}
	if runValue.Purpose != run.PurposeImplementation || runValue.ExecutionProposalID == "" {
		return goalReportResult{}, goalError(goalInvalidOperation, errors.New("Codex interaction is not an implementation Run"))
	}
	progress, err := d.Goals.Report(ctx, runValue.ExecutionProposalID, input.GoalID, status)
	if err != nil {
		if errors.Is(err, taskgoal.ErrNotFound) {
			return goalReportResult{}, goalError(goalUnknownGoal, err)
		}
		var transition *taskgoal.ErrInvalidTransition
		if errors.As(err, &transition) {
			return goalReportResult{}, goalError(goalInvalidState, err)
		}
		return goalReportResult{}, goalError(goalContextUnavailable, err)
	}
	return goalReportResult{GoalID: progress.GoalID, Status: progress.Status}, nil
}

func (d *GoalCapabilityDispatcher) awaitInteraction(ctx context.Context, threadID, turnID string) (run.Run, error) {
	timer := time.NewTimer(interactionBindingWait)
	defer timer.Stop()
	ticker := time.NewTicker(interactionBindingPoll)
	defer ticker.Stop()
	for {
		value, err := d.Runs.GetByInteraction(ctx, threadID, turnID)
		if err == nil {
			return value, nil
		}
		select {
		case <-ctx.Done():
			return run.Run{}, ctx.Err()
		case <-timer.C:
			return run.Run{}, err
		case <-ticker.C:
		}
	}
}
