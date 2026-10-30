package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type DecisionResolveInput struct {
	TaskID                   string                 `json:"taskId,omitempty"`
	ExecutionID              string                 `json:"executionId,omitempty"`
	TaskDefinitionRevisionID string                 `json:"taskDefinitionRevisionId,omitempty"`
	FeatureAreaID            string                 `json:"featureAreaId,omitempty"`
	AISessionID              string                 `json:"aiSessionId"`
	DecisionID               string                 `json:"decisionId"`
	Option                   string                 `json:"option"`
	RequestID                string                 `json:"requestId"`
	NextWorkSpec             *task.WorkSpec         `json:"nextWorkSpec,omitempty"`
	NextVerificationSpec     *task.VerificationSpec `json:"nextVerificationSpec,omitempty"`
}

type DecisionReceiptOutput struct {
	DecisionID   string `json:"decisionId"`
	DecisionKind string `json:"decisionKind"`
	SubjectKind  string `json:"subjectKind"`
	SubjectID    string `json:"subjectId"`
	Option       string `json:"option"`
	RequestID    string `json:"requestId"`
	ResultKind   string `json:"resultKind"`
	ResultID     string `json:"resultId"`
	ResolvedAt   string `json:"resolvedAt"`
}

type DecisionResolveOutput struct {
	Resolved                bool                  `json:"resolved"`
	Accepted                bool                  `json:"accepted"`
	Replay                  bool                  `json:"replay"`
	AlreadyResolved         bool                  `json:"alreadyResolved"`
	Conflict                bool                  `json:"conflict"`
	Receipt                 DecisionReceiptOutput `json:"resolutionReceipt"`
	State                   string                `json:"state,omitempty"`
	PendingDecisionID       string                `json:"pendingDecisionId,omitempty"`
	WorkflowRefreshRequired bool                  `json:"workflowRefreshRequired"`
	Message                 string                `json:"message,omitempty"`
}

func decisionReceiptOutput(value decisionresolution.Receipt) DecisionReceiptOutput {
	return DecisionReceiptOutput{DecisionID: value.DecisionID, DecisionKind: value.Kind, SubjectKind: value.SubjectKind, SubjectID: value.SubjectID, Option: value.Option, RequestID: value.RequestID, ResultKind: value.ResultKind, ResultID: value.ResultID, ResolvedAt: value.ResolvedAt.UTC().Format(timeFormat)}
}

func resolutionFromExisting(value decisionresolution.Receipt, in DecisionResolveInput, byRequest bool) DecisionResolveOutput {
	exact := value.DecisionID == in.DecisionID && value.Option == strings.TrimSpace(in.Option)
	replay := byRequest && exact && value.RequestID == in.RequestID
	already := !replay && exact
	conflict := !exact || byRequest && value.RequestID == in.RequestID && value.DecisionID != in.DecisionID
	message := "The Decision was already resolved; the durable result is returned."
	if conflict {
		message = "The Decision is already resolved with a different option, or this requestId is bound to a different payload. The existing durable resolution is returned."
	}
	return DecisionResolveOutput{Resolved: true, Accepted: exact, Replay: replay, AlreadyResolved: already, Conflict: conflict, Receipt: decisionReceiptOutput(value), Message: message}
}

// ResolveDecisionCore is the single application resolver used by ChatGPT's
// app-private action and Local MCP's trusted-client decision_respond tool.
func ResolveDecisionCore(ctx context.Context, service BridgeService, in DecisionResolveInput) (DecisionResolveOutput, error) {
	kind, subjectID, ok := strings.Cut(strings.TrimSpace(in.DecisionID), ":")
	option, requestID := strings.TrimSpace(in.Option), strings.TrimSpace(in.RequestID)
	if ok && kind == decisionresolution.KindTaskAcceptance && subjectID != "" {
		if in.TaskID == "" || in.FeatureAreaID == "" || in.ExecutionID == "" || in.TaskDefinitionRevisionID == "" {
			return DecisionResolveOutput{}, publicToolError(errors.New("Task Acceptance requires exact taskId, featureAreaId, executionId and taskDefinitionRevisionId"))
		}
		resolver, supported := service.(interface {
			ResolveTaskAcceptance(context.Context, wp.TaskAcceptanceInput) (wp.TaskAcceptanceResolution, error)
		})
		if !supported {
			return DecisionResolveOutput{}, publicToolError(errors.New("Task acceptance resolution unavailable"))
		}
		if in.ExecutionID != "" && in.ExecutionID != subjectID {
			return DecisionResolveOutput{}, publicToolError(errors.New("stale Acceptance execution"))
		}
		resolution, err := resolver.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{TaskID: in.TaskID, FeatureAreaID: in.FeatureAreaID, TaskDefinitionRevisionID: in.TaskDefinitionRevisionID, AISessionID: in.AISessionID, DecisionID: in.DecisionID, ExecutionID: subjectID, Option: option, RequestID: requestID, NextWorkSpec: in.NextWorkSpec, NextVerificationSpec: in.NextVerificationSpec})
		if err != nil {
			return DecisionResolveOutput{}, publicToolError(err)
		}
		message := "Task acceptance decision committed durably."
		if resolution.CommitError != "" {
			message = "Task 已验收 · 代码提交失败: " + resolution.CommitError
		}
		return DecisionResolveOutput{Resolved: true, Accepted: true, Replay: resolution.Replay, Receipt: decisionReceiptOutput(resolution.Receipt), WorkflowRefreshRequired: true, Message: message}, nil
	}
	return DecisionResolveOutput{}, publicToolError(fmt.Errorf("unsupported Decision identity; use the current Task Review gate or Task Acceptance"))
}
