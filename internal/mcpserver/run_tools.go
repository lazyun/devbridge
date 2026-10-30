package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/run"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type runRefInput struct {
	AISessionID string `json:"aiSessionId"`
	RunID       string `json:"runId"`
}

type runListInput struct {
	AISessionID string `json:"aiSessionId"`
	TaskID      string `json:"taskId,omitempty"`
	Status      string `json:"status,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	Offset      int    `json:"offset,omitempty"`
}

type TokenCountsOutput struct {
	InputTokens           int64 `json:"inputTokens"`
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

type TokenUsageOutput struct {
	Status             string             `json:"status"`
	Usage              *TokenCountsOutput `json:"usage,omitempty"`
	ModelContextWindow *int64             `json:"modelContextWindow,omitempty"`
}

type RunFailureOutput struct {
	Kind    string `json:"kind"`
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type VerificationCheckOutcomeOutput struct {
	ID                      string                        `json:"id"`
	Index                   int                           `json:"index"`
	Kind                    string                        `json:"kind"`
	Required                bool                          `json:"required"`
	Status                  string                        `json:"status"`
	DeclaredCommand         string                        `json:"declaredCommand,omitempty"`
	ObservedCommand         string                        `json:"observedCommand,omitempty"`
	ExitCode                *int                          `json:"exitCode,omitempty"`
	Evidence                string                        `json:"evidence,omitempty"`
	EvidenceTruncated       bool                          `json:"evidenceTruncated"`
	StartedAt               *string                       `json:"startedAt,omitempty"`
	FinishedAt              *string                       `json:"finishedAt,omitempty"`
	DurationMillis          *int64                        `json:"durationMillis,omitempty"`
	FailureReason           string                        `json:"failureReason,omitempty"`
	SemanticResultAvailable bool                          `json:"semanticResultAvailable"`
	Summary                 string                        `json:"summary,omitempty"`
	Observed                *run.VerificationObservedFact `json:"observed,omitempty"`
	Reported                run.VerificationReportedFact  `json:"reported"`
}

type VerificationReportOutput struct {
	Version            int                                `json:"version"`
	Availability       string                             `json:"availability"`
	Summary            string                             `json:"summary"`
	LegacyResultStatus string                             `json:"legacyResultStatus,omitempty"`
	CheckCount         int                                `json:"checkCount"`
	Checks             []VerificationCheckOutcomeOutput   `json:"checks,omitempty"`
	AcceptanceCriteria []run.VerificationCriterionOutcome `json:"acceptanceCriteria,omitempty"`
}

type GitEvidenceOutput struct {
	BaseCommit     string   `json:"baseCommit"`
	HeadCommit     string   `json:"headCommit"`
	StartSnapshot  string   `json:"startSnapshot"`
	EndSnapshot    string   `json:"endSnapshot"`
	Dirty          bool     `json:"dirty"`
	ChangedFiles   []string `json:"changedFiles"`
	DiffArtifactID string   `json:"diffArtifactId,omitempty"`
}

type RunOutput struct {
	ExecutionID         string                    `json:"executionId,omitempty"`
	ContextProbeID      string                    `json:"contextProbeId,omitempty"`
	AgentRuntime        *agentruntime.Snapshot    `json:"agentRuntime,omitempty"`
	ProjectID           string                    `json:"projectId,omitempty"`
	FeatureAreaID       string                    `json:"featureAreaId,omitempty"`
	OperationKind       string                    `json:"operationKind,omitempty"`
	OperationID         string                    `json:"operationId,omitempty"`
	ReviewID            string                    `json:"reviewId,omitempty"`
	ID                  string                    `json:"id"`
	TaskID              string                    `json:"taskId"`
	Purpose             string                    `json:"purpose"`
	ExecutionProposalID string                    `json:"executionProposalId,omitempty"`
	CodexInstruction    string                    `json:"codexInstruction,omitempty"`
	ReviewScope         *ReviewScopeOutput        `json:"reviewScope,omitempty"`
	WorkspaceID         string                    `json:"workspaceId,omitempty"`
	WorktreeID          string                    `json:"worktreeId,omitempty"`
	ThreadID            string                    `json:"threadId,omitempty"`
	TurnID              string                    `json:"turnId,omitempty"`
	Attempt             int                       `json:"attempt"`
	Status              string                    `json:"status"`
	CreatedAt           string                    `json:"createdAt"`
	UpdatedAt           string                    `json:"updatedAt"`
	StartedAt           *string                   `json:"startedAt,omitempty"`
	FinishedAt          *string                   `json:"finishedAt,omitempty"`
	DurationMillis      *int64                    `json:"durationMillis,omitempty"`
	TokenUsage          TokenUsageOutput          `json:"tokenUsage"`
	AgentClaim          string                    `json:"agentClaim,omitempty"`
	GitEvidence         *GitEvidenceOutput        `json:"gitEvidence,omitempty"`
	ChangedFiles        []string                  `json:"changedFiles"`
	Failure             *RunFailureOutput         `json:"failure,omitempty"`
	VerificationReport  *VerificationReportOutput `json:"verificationReport,omitempty"`
}

const verificationFactStatusSchema = `{"type":"string","enum":["PASSED","FAILED","BLOCKED","NOT_RUN","UNKNOWN"]}`
const verificationReportedFactOutputSchema = `{"type":"object","properties":{"status":` + verificationFactStatusSchema + `,"summary":{"type":"string","maxLength":2048},"evidence":{"type":"string","maxLength":32768},"reason":{"type":"string","maxLength":2048}},"required":["status","summary"],"additionalProperties":false}`
const verificationObservedFactOutputSchema = `{"type":"object","properties":{"status":` + verificationFactStatusSchema + `,"command":{"type":"string","maxLength":8192},"exitCode":{"type":"integer"},"evidence":{"type":"string","maxLength":32768},"evidenceTruncated":{"type":"boolean"},"startedAt":{"type":"string"},"finishedAt":{"type":"string"},"durationMillis":{"type":"integer","minimum":0},"reason":{"type":"string","maxLength":2048},"summary":{"type":"string","maxLength":2048}},"required":["status","evidenceTruncated"],"additionalProperties":false}`
const verificationCheckReportOutputSchema = `{"type":"object","properties":{"id":{"type":"string"},"index":{"type":"integer"},"kind":{"type":"string"},"required":{"type":"boolean"},"status":` + verificationFactStatusSchema + `,"declaredCommand":{"type":"string","maxLength":8192},"observedCommand":{"type":"string","maxLength":8192},"exitCode":{"type":"integer"},"evidence":{"type":"string","maxLength":32768},"evidenceTruncated":{"type":"boolean"},"startedAt":{"type":"string"},"finishedAt":{"type":"string"},"durationMillis":{"type":"integer","minimum":0},"failureReason":{"type":"string","maxLength":2048},"semanticResultAvailable":{"type":"boolean"},"summary":{"type":"string","maxLength":2048},"observed":` + verificationObservedFactOutputSchema + `,"reported":` + verificationReportedFactOutputSchema + `},"required":["id","index","kind","required","status","evidenceTruncated","reported"],"additionalProperties":false}`
const verificationCriterionReportOutputSchema = `{"type":"object","properties":{"index":{"type":"integer"},"criterion":{"type":"string","maxLength":2048},"reported":` + verificationReportedFactOutputSchema + `},"required":["index","criterion","reported"],"additionalProperties":false}`
const verificationReportOutputSchema = `{"type":"object","properties":{"version":{"type":"integer","enum":[1,2]},"availability":{"type":"string","const":"AVAILABLE"},"summary":{"type":"string","maxLength":4096},"legacyResultStatus":{"type":"string"},"checkCount":{"type":"integer","minimum":0,"maximum":32},"checks":{"type":"array","maxItems":32,"items":` + verificationCheckReportOutputSchema + `},"acceptanceCriteria":{"type":"array","maxItems":32,"items":` + verificationCriterionReportOutputSchema + `}},"required":["version","availability","summary","checkCount"],"additionalProperties":false}`

type ReviewScopeOutput struct {
	Kind  string   `json:"kind"`
	Paths []string `json:"paths"`
}

type RunListOutput struct {
	Runs []RunOutput `json:"runs"`
}

const (
	tokenUsageOutputSchema = `{"type":"object","properties":{"status":{"type":"string"},"usage":{"type":"object","properties":{"inputTokens":{"type":"integer"},"cachedInputTokens":{"type":"integer"},"cacheWriteInputTokens":{"type":"integer"},"outputTokens":{"type":"integer"},"reasoningOutputTokens":{"type":"integer"},"totalTokens":{"type":"integer"}},"required":["inputTokens","cachedInputTokens","cacheWriteInputTokens","outputTokens","reasoningOutputTokens","totalTokens"],"additionalProperties":false},"modelContextWindow":{"type":"integer"}},"required":["status"],"additionalProperties":false}`
	runRefInputSchema      = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","runId"],"additionalProperties":false}`
	runListInputSchema     = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","maxLength":128},"status":{"type":"string","enum":["CREATED","PREPARING","RUNNING","WAITING_APPROVAL","COLLECTING","SUCCEEDED","FAILED","INFRASTRUCTURE_FAILED","CANCELED"]},"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0,"maximum":1000000}},"required":["aiSessionId"],"additionalProperties":false}`
	runOutputSchema        = `{"type":"object","properties":{"executionId":{"type":"string"},"contextProbeId":{"type":"string"},"agentRuntime":{"type":"object"},"projectId":{"type":"string"},"featureAreaId":{"type":"string"},"operationKind":{"type":"string"},"operationId":{"type":"string"},"reviewId":{"type":"string"},"id":{"type":"string"},"taskId":{"type":"string"},"purpose":{"type":"string","enum":["SCOPE_REVIEW","IMPLEMENTATION","VERIFICATION","INSPECTION"]},"executionProposalId":{"type":"string"},"codexInstruction":{"type":"string","maxLength":65536},"reviewScope":{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string"}}},"required":["kind","paths"],"additionalProperties":false},"workspaceId":{"type":"string"},"worktreeId":{"type":"string"},"threadId":{"type":"string"},"turnId":{"type":"string"},"attempt":{"type":"integer"},"status":{"type":"string"},"createdAt":{"type":"string"},"updatedAt":{"type":"string"},"startedAt":{"type":"string"},"finishedAt":{"type":"string"},"durationMillis":{"type":"integer","minimum":0},"tokenUsage":{"type":"object","properties":{"status":{"type":"string"},"usage":{"type":"object","properties":{"inputTokens":{"type":"integer"},"cachedInputTokens":{"type":"integer"},"cacheWriteInputTokens":{"type":"integer"},"outputTokens":{"type":"integer"},"reasoningOutputTokens":{"type":"integer"},"totalTokens":{"type":"integer"}},"required":["inputTokens","cachedInputTokens","cacheWriteInputTokens","outputTokens","reasoningOutputTokens","totalTokens"],"additionalProperties":false},"modelContextWindow":{"type":"integer"}},"required":["status"],"additionalProperties":false},"agentClaim":{"type":"string","maxLength":65536},"changedFiles":{"type":"array","maxItems":4096,"items":{"type":"string","maxLength":4096}},"gitEvidence":{"type":"object","properties":{"baseCommit":{"type":"string"},"headCommit":{"type":"string"},"startSnapshot":{"type":"string"},"endSnapshot":{"type":"string"},"dirty":{"type":"boolean"},"changedFiles":{"type":"array","maxItems":4096,"items":{"type":"string","maxLength":4096}},"diffArtifactId":{"type":"string"}},"required":["baseCommit","headCommit","startSnapshot","endSnapshot","dirty","changedFiles"],"additionalProperties":false},"failure":{"type":"object","properties":{"kind":{"type":"string"},"stage":{"type":"string"},"code":{"type":"string"},"message":{"type":"string","maxLength":2048}},"required":["kind","stage","code","message"],"additionalProperties":false},"verificationReport":` + verificationReportOutputSchema + `},"required":["id","taskId","purpose","attempt","status","createdAt","updatedAt","tokenUsage","changedFiles"],"additionalProperties":false}`
	runListOutputSchema    = `{"type":"object","properties":{"runs":{"type":"array","maxItems":100,"items":` + runOutputSchema + `}},"required":["runs"],"additionalProperties":false}`
)

func registerRunTools(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "run_get", Title: "Get Run", Description: "Return bounded status, timing, token usage, and failure facts for one Run in the AI session's Project.",
		InputSchema: json.RawMessage(runRefInputSchema), OutputSchema: json.RawMessage(runOutputSchema), Annotations: toolAnnotations("Get Run", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input runRefInput) (*sdkmcp.CallToolResult, RunOutput, error) {
		value, err := service.GetRunForSession(ctx, input.AISessionID, input.RunID)
		if err != nil {
			return nil, RunOutput{}, publicToolError(err)
		}
		return nil, runOutput(value, true), nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "run_list", Title: "List Runs", Description: "List bounded Run facts for the AI session's Project with optional Task and status filters.",
		InputSchema: json.RawMessage(runListInputSchema), OutputSchema: json.RawMessage(runListOutputSchema), Annotations: toolAnnotations("List Runs", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input runListInput) (*sdkmcp.CallToolResult, RunListOutput, error) {
		values, err := service.ListRunsForSession(ctx, input.AISessionID, controlapi.RunFilter{TaskID: input.TaskID, Status: run.Status(input.Status), ListOptions: controlapi.ListOptions{Limit: input.Limit, Offset: input.Offset}})
		if err != nil {
			return nil, RunListOutput{}, publicToolError(err)
		}
		return nil, RunListOutput{Runs: runOutputs(values)}, nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "run_cancel", Title: "Cancel Run", Description: "Target and join only the selected live RuntimeSupervisor Run in the AI session's Project.",
		InputSchema: json.RawMessage(runRefInputSchema), OutputSchema: json.RawMessage(runOutputSchema), Annotations: stateChangingAnnotations("Cancel Run", true), Meta: decisionToolMeta(),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input runRefInput) (*sdkmcp.CallToolResult, RunOutput, error) {
		value, err := service.CancelRunForSession(ctx, input.AISessionID, input.RunID)
		if err != nil {
			return nil, RunOutput{}, publicToolError(err)
		}
		return nil, runOutput(value, true), nil
	})
}

func runOutputs(values []run.Run) []RunOutput {
	result := make([]RunOutput, 0, len(values))
	for _, value := range values {
		result = append(result, runOutput(value, false))
	}
	return result
}

func runOutput(value run.Run, details bool) RunOutput {
	result := RunOutput{
		AgentRuntime: value.AgentRuntime, ExecutionID: value.AgentExecutionID, ContextProbeID: value.ContextProbeID,
		ID: value.ID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, OperationKind: string(value.OperationKind), OperationID: value.OperationID, ReviewID: value.ReviewID, TaskID: value.TaskID, Purpose: string(value.Purpose), ExecutionProposalID: value.ExecutionProposalID, WorkspaceID: value.WorkspaceID, WorktreeID: value.WorktreeID, ThreadID: value.ThreadID, TurnID: value.TurnID, Attempt: value.Attempt, Status: string(value.Status), CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat),
		StartedAt: formattedTime(value.StartedAt), FinishedAt: formattedTime(value.FinishedAt),
		TokenUsage: TokenUsageOutput{Status: string(value.TokenUsage.Status), ModelContextWindow: value.TokenUsage.ModelContextWindow}, ChangedFiles: []string{},
	}
	if value.StartedAt != nil && value.FinishedAt != nil {
		duration := value.FinishedAt.Sub(*value.StartedAt).Milliseconds()
		if duration < 0 {
			duration = 0
		}
		result.DurationMillis = &duration
	}
	if value.TokenUsage.Usage != nil {
		usage := tokenCountsOutput(*value.TokenUsage.Usage)
		result.TokenUsage.Usage = &usage
	}
	if value.Failure != nil {
		result.Failure = &RunFailureOutput{Kind: string(value.Failure.Kind), Stage: boundedUTF8(value.Failure.Stage, 256), Code: boundedUTF8(value.Failure.Code, 256), Message: boundedUTF8(value.Failure.Message, 2048)}
	}
	if value.Purpose == run.PurposeScopeReview {
		paths := append([]string(nil), value.ReviewScope.Paths...)
		if paths == nil {
			paths = []string{}
		}
		result.ReviewScope = &ReviewScopeOutput{Kind: string(value.ReviewScope.Kind), Paths: paths}
		if details {
			result.CodexInstruction = boundedUTF8(value.CodexInstruction, 64<<10)
		}
	}
	if value.Result != nil {
		result.AgentClaim = boundedUTF8(value.Result.AgentClaim.Message, 64<<10)
		files := append([]string(nil), value.Result.GitEvidence.ChangedFiles...)
		if files == nil {
			files = []string{}
		}
		result.ChangedFiles = files
		result.GitEvidence = &GitEvidenceOutput{BaseCommit: value.Result.GitEvidence.BaseCommit, HeadCommit: value.Result.GitEvidence.HeadCommit, StartSnapshot: value.Result.GitEvidence.StartSnapshot, EndSnapshot: value.Result.GitEvidence.EndSnapshot, Dirty: value.Result.GitEvidence.Dirty, ChangedFiles: files, DiffArtifactID: value.Result.GitEvidence.DiffArtifactID}
	}
	if value.VerificationReport != nil {
		verification := &VerificationReportOutput{Version: value.VerificationReport.Version, Availability: string(value.VerificationReport.Availability), Summary: boundedUTF8(value.VerificationReport.Summary, 4096), LegacyResultStatus: string(value.VerificationReport.LegacyResultStatus), CheckCount: len(value.VerificationReport.Checks)}
		if details {
			verification.AcceptanceCriteria = append([]run.VerificationCriterionOutcome(nil), value.VerificationReport.AcceptanceCriteria...)
			verification.Checks = make([]VerificationCheckOutcomeOutput, 0, len(value.VerificationReport.Checks))
			for _, check := range value.VerificationReport.Checks {
				verification.Checks = append(verification.Checks, VerificationCheckOutcomeOutput{ID: check.ID, Index: check.Index, Kind: check.Kind, Required: check.Required, Status: string(check.Status), DeclaredCommand: boundedUTF8(check.DeclaredCommand, 8192), ObservedCommand: boundedUTF8(check.ObservedCommand, 8192), ExitCode: check.ExitCode, Evidence: boundedUTF8(check.Evidence, 32<<10), EvidenceTruncated: check.EvidenceTruncated, StartedAt: formattedTime(check.StartedAt), FinishedAt: formattedTime(check.FinishedAt), DurationMillis: check.DurationMillis, FailureReason: boundedUTF8(check.FailureReason, 256), SemanticResultAvailable: check.SemanticResultAvailable, Summary: boundedUTF8(check.Summary, 2048), Observed: check.Observed, Reported: check.Reported})
			}
		}
		result.VerificationReport = verification
	}
	return result
}

func tokenCountsOutput(value run.TokenCounts) TokenCountsOutput {
	return TokenCountsOutput{InputTokens: value.InputTokens, CachedInputTokens: value.CachedInputTokens, CacheWriteInputTokens: value.CacheWriteInputTokens, OutputTokens: value.OutputTokens, ReasoningOutputTokens: value.ReasoningOutputTokens, TotalTokens: value.TotalTokens}
}
