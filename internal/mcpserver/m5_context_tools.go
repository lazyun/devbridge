package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type m5ContextService interface {
	GetProjectContextForSession(context.Context, string) (projectcontext.ProjectContext, error)
	CreateModuleForSession(context.Context, string, string, string, string, *string) (module.Module, error)
	CreateContextProbe(context.Context, contextprobe.CreateInput) (contextprobe.Probe, error)
	GetContextProbeForSession(context.Context, string, string) (contextprobe.Probe, error)
	ListContextProbesForSession(context.Context, string) ([]contextprobe.Probe, error)
	StartContextProbe(context.Context, string, string) (contextprobe.Probe, error)
	ClassifyTaskForSession(context.Context, string, string, string) (task.Task, error)
	GetTaskClassificationContextForSession(context.Context, string, string) (controlapi.TaskClassificationContext, error)
}

type m5CanonicalInspectionService interface {
	PrepareProjectInspection(context.Context, contextprobe.PrepareInspectionInput) (contextprobe.InspectionConfirmationPlan, error)
	ConfirmProjectInspection(context.Context, contextprobe.ConfirmInspectionInput) (contextprobe.Probe, error)
	StartConfirmedProjectInspection(context.Context, contextprobe.StartInspectionInput) (contextprobe.Probe, error)
}

type ProjectContextOutput struct {
	ProjectID            string `json:"projectId"`
	Status               string `json:"status"`
	RootGuidanceIdentity string `json:"rootGuidanceIdentity,omitempty"`
	LatestProbeID        string `json:"latestProbeId,omitempty"`
	Result               string `json:"result,omitempty"`
	ResultArtifactRef    string `json:"resultArtifactRef,omitempty"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
}

type moduleCreateInput struct {
	AISessionID string  `json:"aiSessionId"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ParentID    *string `json:"parentId,omitempty"`
}

type taskClassifyInput struct {
	AISessionID string `json:"aiSessionId"`
	TaskID      string `json:"taskId"`
	ModuleID    string `json:"moduleId"`
}

type probeScopeInput struct {
	Kind  string   `json:"kind"`
	Paths []string `json:"paths,omitempty"`
}

type probeCreateInput struct {
	AISessionID      string          `json:"aiSessionId"`
	RepositoryID     string          `json:"repositoryId"`
	CodexConfigID    string          `json:"codexConfigId"`
	Scope            probeScopeInput `json:"scope"`
	CodexInstruction string          `json:"codexInstruction"`
}

type probeRefInput struct {
	AISessionID string `json:"aiSessionId"`
	ProbeID     string `json:"probeId"`
}

type ContextProbeOutput struct {
	ID                string           `json:"id"`
	TaskID            string           `json:"taskId"`
	RunID             string           `json:"runId,omitempty"`
	AgentExecutionID  string           `json:"agentExecutionId,omitempty"`
	AISessionID       string           `json:"aiSessionId"`
	ProjectID         string           `json:"projectId"`
	RepositoryID      string           `json:"repositoryId"`
	CodexConfigID     string           `json:"codexConfigId"`
	Scope             ProbeScopeOutput `json:"scope"`
	CodexInstruction  string           `json:"codexInstruction"`
	Status            string           `json:"status"`
	ThreadID          string           `json:"threadId,omitempty"`
	TurnID            string           `json:"turnId,omitempty"`
	StartedAt         *string          `json:"startedAt,omitempty"`
	FinishedAt        *string          `json:"finishedAt,omitempty"`
	TokenUsage        TokenUsageOutput `json:"tokenUsage"`
	Result            string           `json:"result,omitempty"`
	ResultArtifactRef string           `json:"resultArtifactRef,omitempty"`
	FailureMessage    string           `json:"failureMessage,omitempty"`
	Canonical         *contextprobe.InspectionConfirmationPlan `json:"canonical,omitempty"`
	WorkspaceID       string           `json:"workspaceId,omitempty"`
	RuntimeWorksetSHA256 string        `json:"runtimeWorksetSha256,omitempty"`
	ReportSHA256      string           `json:"reportSha256,omitempty"`
	ResultSHA256      string           `json:"resultSha256,omitempty"`
	RuntimeLimitation string           `json:"runtimeLimitation,omitempty"`
	CreatedAt         string           `json:"createdAt"`
	UpdatedAt         string           `json:"updatedAt"`
}

type ProbeScopeOutput struct {
	Kind  string   `json:"kind"`
	Paths []string `json:"paths"`
}
type ContextProbeListOutput struct {
	ContextProbes []ContextProbeOutput `json:"contextProbes"`
}

type TaskClassificationContextOutput struct {
	Task                  TaskOutput           `json:"task"`
	Module                ModuleOutput         `json:"module"`
	LatestRun             *RunOutput           `json:"latestRun,omitempty"`
	ChangedFiles          []string             `json:"changedFiles"`
	DiffArtifactID        string               `json:"diffArtifactId,omitempty"`
	AgentReportArtifactID string               `json:"agentReportArtifactId,omitempty"`
	ProjectContext        ProjectContextOutput `json:"projectContext"`
}

const (
	projectContextOutputSchema        = `{"type":"object","properties":{"projectId":{"type":"string"},"status":{"type":"string","enum":["REQUIRED","RUNNING","READY","NO_GUIDANCE","FAILED"]},"rootGuidanceIdentity":{"type":"string"},"latestProbeId":{"type":"string"},"result":{"type":"string","maxLength":65536},"resultArtifactRef":{"type":"string"},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["projectId","status","createdAt","updatedAt"],"additionalProperties":false}`
	moduleOutputSchema                = `{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"key":{"type":"string"},"name":{"type":"string"},"description":{"type":"string"},"systemOwned":{"type":"boolean"},"parentId":{"type":"string"},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","projectId","key","name","description","systemOwned","createdAt","updatedAt"],"additionalProperties":false}`
	moduleCreateInputSchema           = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"key":{"type":"string","minLength":1,"maxLength":128},"name":{"type":"string","minLength":1,"maxLength":256},"description":{"type":"string","minLength":1,"maxLength":4096},"parentId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","key","name","description"],"additionalProperties":false}`
	taskClassifyInputSchema           = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"moduleId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","taskId","moduleId"],"additionalProperties":false}`
	probeCreateInputSchema            = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128},"codexConfigId":{"type":"string","minLength":1,"maxLength":128},"scope":{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string","minLength":1,"maxLength":4096}}},"required":["kind"],"additionalProperties":false},"codexInstruction":{"type":"string","minLength":1,"maxLength":65536}},"required":["aiSessionId","repositoryId","codexConfigId","scope","codexInstruction"],"additionalProperties":false}`
	probeRefInputSchema               = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"probeId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","probeId"],"additionalProperties":false}`
	projectInspectionTargetSchema     = `{"type":"object","properties":{"repositoryId":{"type":"string"},"repositoryName":{"type":"string"},"memberId":{"type":"string"},"memberVersion":{"type":"integer","const":1},"originCommit":{"type":"string"},"originRef":{"type":"string"},"scope":{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string"}}},"required":["kind","paths"],"additionalProperties":false}},"required":["repositoryId","repositoryName","memberId","memberVersion","originCommit","scope"],"additionalProperties":false}`
	projectInspectionPlanSchema       = `{"type":"object","properties":{"contractVersion":{"type":"integer","const":1},"aiSessionId":{"type":"string"},"projectId":{"type":"string"},"requestId":{"type":"string"},"inspectionId":{"type":"string"},"taskId":{"type":"string"},"payloadSha256":{"type":"string"},"codexInstruction":{"type":"string"},"backend":{"type":"object","properties":{"configId":{"type":"string"},"configFingerprint":{"type":"string"},"configName":{"type":"string"},"source":{"type":"string"}},"required":["configId","configFingerprint","configName","source"],"additionalProperties":false},"model":{"type":"string"},"reasoningEffort":{"type":"string"},"serviceTier":{"type":"string"},"primaryMemberId":{"type":"string"},"targets":{"type":"array","minItems":1,"maxItems":128,"items":` + projectInspectionTargetSchema + `}},"required":["contractVersion","aiSessionId","projectId","requestId","inspectionId","taskId","payloadSha256","codexInstruction","backend","model","reasoningEffort","primaryMemberId","targets"],"additionalProperties":false}`
	projectInspectionPrepareSchema    = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1},"requestId":{"type":"string","minLength":1},"codexInstruction":{"type":"string","minLength":1,"maxLength":65536},"backend":{"type":"object","properties":{"configId":{"type":"string","minLength":1},"configFingerprint":{"type":"string","minLength":1}},"required":["configId","configFingerprint"],"additionalProperties":false},"model":{"type":"string","minLength":1},"reasoningEffort":{"type":"string"},"serviceTier":{"type":"string"},"primaryRepositoryId":{"type":"string","minLength":1},"targets":{"type":"array","minItems":1,"maxItems":128,"items":{"type":"object","properties":{"repositoryId":{"type":"string","minLength":1},"scope":{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string"}}},"required":["kind"],"additionalProperties":false}},"required":["repositoryId","scope"],"additionalProperties":false}}},"required":["aiSessionId","requestId","codexInstruction","backend","model","primaryRepositoryId","targets"],"additionalProperties":false}`
	projectInspectionConfirmSchema    = `{"type":"object","properties":{"plan":` + projectInspectionPlanSchema + `,"payloadSha256":{"type":"string","minLength":64,"maxLength":64}},"required":["plan","payloadSha256"],"additionalProperties":false}`
	projectInspectionStartSchema      = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1},"projectId":{"type":"string","minLength":1},"inspectionOwnerTaskId":{"type":"string","minLength":1},"inspectionOperationId":{"type":"string","minLength":1},"expectedOwnerState":{"type":"string","enum":["CREATED","RUNNING"]},"expectedRuntimeEpoch":{"type":"string"},"expectedWorksetSha256":{"type":"string"},"requestId":{"type":"string","minLength":1},"payloadSha256":{"type":"string","minLength":64,"maxLength":64}},"required":["aiSessionId","projectId","inspectionOwnerTaskId","inspectionOperationId","expectedOwnerState","requestId","payloadSha256"],"additionalProperties":false}`
	probeOutputSchema                 = `{"type":"object","properties":{"id":{"type":"string"},"taskId":{"type":"string"},"runId":{"type":"string"},"agentExecutionId":{"type":"string"},"aiSessionId":{"type":"string"},"projectId":{"type":"string"},"repositoryId":{"type":"string"},"codexConfigId":{"type":"string"},"scope":{"type":"object","properties":{"kind":{"type":"string"},"paths":{"type":"array","maxItems":128,"items":{"type":"string"}}},"required":["kind","paths"],"additionalProperties":false},"codexInstruction":{"type":"string","maxLength":65536},"status":{"type":"string"},"threadId":{"type":"string"},"turnId":{"type":"string"},"startedAt":{"type":"string"},"finishedAt":{"type":"string"},"tokenUsage":{"type":"object","properties":{"status":{"type":"string"},"usage":{"type":"object","properties":{"inputTokens":{"type":"integer"},"cachedInputTokens":{"type":"integer"},"cacheWriteInputTokens":{"type":"integer"},"outputTokens":{"type":"integer"},"reasoningOutputTokens":{"type":"integer"},"totalTokens":{"type":"integer"}},"required":["inputTokens","cachedInputTokens","cacheWriteInputTokens","outputTokens","reasoningOutputTokens","totalTokens"],"additionalProperties":false},"modelContextWindow":{"type":"integer"}},"required":["status"],"additionalProperties":false},"result":{"type":"string","maxLength":65536},"resultArtifactRef":{"type":"string"},"failureMessage":{"type":"string","maxLength":2048},"canonical":` + projectInspectionPlanSchema + `,"workspaceId":{"type":"string"},"runtimeWorksetSha256":{"type":"string"},"reportSha256":{"type":"string"},"resultSha256":{"type":"string"},"runtimeLimitation":{"type":"string"},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","taskId","aiSessionId","projectId","repositoryId","codexConfigId","scope","codexInstruction","status","tokenUsage","createdAt","updatedAt"],"additionalProperties":false}`
	probeListOutputSchema             = `{"type":"object","properties":{"contextProbes":{"type":"array","maxItems":100,"items":` + probeOutputSchema + `}},"required":["contextProbes"],"additionalProperties":false}`
	classificationContextOutputSchema = `{"type":"object","properties":{"task":` + taskOutputSchema + `,"module":` + moduleOutputSchema + `,"latestRun":` + runOutputSchema + `,"changedFiles":{"type":"array","maxItems":256,"items":{"type":"string"}},"diffArtifactId":{"type":"string"},"agentReportArtifactId":{"type":"string"},"projectContext":` + projectContextOutputSchema + `},"required":["task","module","changedFiles","projectContext"],"additionalProperties":false}`
)

func registerM5ContextTools(server *sdkmcp.Server, base BridgeService) {
	service, ok := base.(m5ContextService)
	if !ok {
		return
	}
	canonical, canonicalAvailable := base.(m5CanonicalInspectionService)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_context_get", Title: "Get Project context", Description: "Return durable Project-level orientation and the latest bounded ContextProbe result. Project Context is Project orientation. READY and NO_GUIDANCE are usable states.", InputSchema: json.RawMessage(sessionInputSchema), OutputSchema: json.RawMessage(projectContextOutputSchema), Annotations: toolAnnotations("Get Project context", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input sessionInput) (*sdkmcp.CallToolResult, ProjectContextOutput, error) {
		value, err := service.GetProjectContextForSession(ctx, input.AISessionID)
		if err != nil {
			return nil, ProjectContextOutput{}, publicToolError(err)
		}
		return nil, projectContextOutput(value), nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "module_create", Title: "Create Project module", Description: "Create one normal Module in the AI session's Project from GPT-supplied semantic metadata. Dev Bridge does not scan or infer modules.", InputSchema: json.RawMessage(moduleCreateInputSchema), OutputSchema: json.RawMessage(moduleOutputSchema), Annotations: stateChangingAnnotations("Create Project module", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input moduleCreateInput) (*sdkmcp.CallToolResult, ModuleOutput, error) {
		value, err := service.CreateModuleForSession(ctx, input.AISessionID, input.Key, input.Name, input.Description, input.ParentID)
		if err != nil {
			return nil, ModuleOutput{}, publicToolError(err)
		}
		return nil, moduleOutputs([]module.Module{value})[0], nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "task_classify", Title: "Classify Task", Description: "Move an Unclassified Task to a regular Module in the same Project when no active Run exists.", InputSchema: json.RawMessage(taskClassifyInputSchema), OutputSchema: json.RawMessage(taskOutputSchema), Annotations: stateChangingAnnotations("Classify Task", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input taskClassifyInput) (*sdkmcp.CallToolResult, TaskOutput, error) {
		value, err := service.ClassifyTaskForSession(ctx, input.AISessionID, input.TaskID, input.ModuleID)
		if err != nil {
			return nil, TaskOutput{}, publicToolError(err)
		}
		return nil, taskOutput(value, true), nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "task_classification_context_get", Title: "Get Task classification context", Description: "Return bounded Task, current Module, latest Run, ChangedFiles, evidence artifact references, and ProjectContext facts for GPT classification.", InputSchema: json.RawMessage(taskGetInputSchema), OutputSchema: json.RawMessage(classificationContextOutputSchema), Annotations: toolAnnotations("Get Task classification context", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input taskGetInput) (*sdkmcp.CallToolResult, TaskClassificationContextOutput, error) {
		value, err := service.GetTaskClassificationContextForSession(ctx, input.AISessionID, input.TaskID)
		if err != nil {
			return nil, TaskClassificationContextOutput{}, publicToolError(err)
		}
		return nil, taskClassificationContextOutput(value), nil
	})
	if canonicalAvailable {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_inspection_prepare", Title: "Prepare Project Inspection", Description: "Purely read and freeze the exact multi-repository Project Inspection confirmation plan. This creates no Task, probe, workspace, worktree, conversation, claim, run, event, or model session.", InputSchema: json.RawMessage(projectInspectionPrepareSchema), OutputSchema: json.RawMessage(projectInspectionPlanSchema), Annotations: toolAnnotations("Prepare Project Inspection", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input contextprobe.PrepareInspectionInput) (*sdkmcp.CallToolResult, contextprobe.InspectionConfirmationPlan, error) {
			value, err := canonical.PrepareProjectInspection(ctx, input)
			if err != nil { return nil, contextprobe.InspectionConfirmationPlan{}, publicToolError(err) }
			return nil, value, nil
		})
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_inspection_confirm", Title: "Confirm Project Inspection", Description: "Persist the exact prepared Project Inspection Task, ContextProbe, and immutable target receipt. The payload hash must match the plan.", InputSchema: json.RawMessage(projectInspectionConfirmSchema), OutputSchema: json.RawMessage(probeOutputSchema), Annotations: stateChangingAnnotations("Confirm Project Inspection", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input contextprobe.ConfirmInspectionInput) (*sdkmcp.CallToolResult, ContextProbeOutput, error) {
			value, err := canonical.ConfirmProjectInspection(ctx, input)
			if err != nil { return nil, ContextProbeOutput{}, publicToolError(err) }
			return nil, contextProbeOutput(value), nil
		})
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "context_probe_get", Title: "Get ContextProbe", Description: "Return one ContextProbe in the AI session's Project, including independent status, result, and token accounting.", InputSchema: json.RawMessage(probeRefInputSchema), OutputSchema: json.RawMessage(probeOutputSchema), Annotations: toolAnnotations("Get ContextProbe", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input probeRefInput) (*sdkmcp.CallToolResult, ContextProbeOutput, error) {
		value, err := service.GetContextProbeForSession(ctx, input.AISessionID, input.ProbeID)
		if err != nil {
			return nil, ContextProbeOutput{}, publicToolError(err)
		}
		return nil, contextProbeOutput(value), nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "context_probe_list", Title: "List ContextProbes", Description: "List newest bounded ContextProbe records for the AI session's Project.", InputSchema: json.RawMessage(listInputSchema), OutputSchema: json.RawMessage(probeListOutputSchema), Annotations: toolAnnotations("List ContextProbes", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input listInput) (*sdkmcp.CallToolResult, ContextProbeListOutput, error) {
		values, err := service.ListContextProbesForSession(ctx, input.AISessionID)
		if err != nil {
			return nil, ContextProbeListOutput{}, publicToolError(err)
		}
		limit, offset := input.Limit, input.Offset
		if limit == 0 {
			limit = 50
		}
		if offset > len(values) {
			offset = len(values)
		}
		end := offset + limit
		if end > len(values) {
			end = len(values)
		}
		return nil, ContextProbeListOutput{ContextProbes: contextProbeOutputs(values[offset:end])}, nil
	})
	if canonicalAvailable {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_inspection_start", Title: "Start Project Inspection", Description: "Start only the exact confirmed Project Inspection identity and payload. Core loads its frozen targets, backend, model, reasoning, workspace, and workset.", InputSchema: json.RawMessage(projectInspectionStartSchema), OutputSchema: json.RawMessage(probeOutputSchema), Annotations: stateChangingAnnotations("Start Project Inspection", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input contextprobe.StartInspectionInput) (*sdkmcp.CallToolResult, ContextProbeOutput, error) {
			value, err := canonical.StartConfirmedProjectInspection(ctx, input)
			if err != nil { return nil, ContextProbeOutput{}, publicToolError(err) }
			return nil, contextProbeOutput(value), nil
		})
	}
}

func registerCanonicalInspectionTools(server *sdkmcp.Server, base BridgeService) {
	service, ok := base.(m5CanonicalInspectionService)
	if !ok { return }
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_inspection_prepare", Title: "Prepare Project Inspection", Description: "Purely read and freeze the exact multi-repository Project Inspection confirmation plan. This creates no Task, probe, workspace, worktree, conversation, claim, run, event, or model session.", InputSchema: json.RawMessage(projectInspectionPrepareSchema), OutputSchema: json.RawMessage(projectInspectionPlanSchema), Annotations: toolAnnotations("Prepare Project Inspection", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input contextprobe.PrepareInspectionInput) (*sdkmcp.CallToolResult, contextprobe.InspectionConfirmationPlan, error) {
		value, err := service.PrepareProjectInspection(ctx, input)
		if err != nil { return nil, contextprobe.InspectionConfirmationPlan{}, publicToolError(err) }
		return nil, value, nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_inspection_confirm", Title: "Confirm Project Inspection", Description: "Persist the exact prepared Project Inspection Task, ContextProbe, and immutable target receipt. The payload hash must match the plan.", InputSchema: json.RawMessage(projectInspectionConfirmSchema), OutputSchema: json.RawMessage(probeOutputSchema), Annotations: stateChangingAnnotations("Confirm Project Inspection", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input contextprobe.ConfirmInspectionInput) (*sdkmcp.CallToolResult, ContextProbeOutput, error) {
		value, err := service.ConfirmProjectInspection(ctx, input)
		if err != nil { return nil, ContextProbeOutput{}, publicToolError(err) }
		return nil, contextProbeOutput(value), nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_inspection_start", Title: "Start Project Inspection", Description: "Start only the exact confirmed Project Inspection identity and payload. Core loads its frozen targets, backend, model, reasoning, workspace, and workset.", InputSchema: json.RawMessage(projectInspectionStartSchema), OutputSchema: json.RawMessage(probeOutputSchema), Annotations: stateChangingAnnotations("Start Project Inspection", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input contextprobe.StartInspectionInput) (*sdkmcp.CallToolResult, ContextProbeOutput, error) {
		value, err := service.StartConfirmedProjectInspection(ctx, input)
		if err != nil { return nil, ContextProbeOutput{}, publicToolError(err) }
		return nil, contextProbeOutput(value), nil
	})
}

func taskClassificationContextOutput(value controlapi.TaskClassificationContext) TaskClassificationContextOutput {
	changedFiles := make([]string, len(value.ChangedFiles))
	copy(changedFiles, value.ChangedFiles)
	output := TaskClassificationContextOutput{Task: taskOutput(value.Task, true), Module: moduleOutputs([]module.Module{value.Module})[0], ChangedFiles: changedFiles, DiffArtifactID: value.DiffArtifactID, AgentReportArtifactID: value.AgentReportArtifactID, ProjectContext: projectContextOutput(value.ProjectContext)}
	if len(output.ChangedFiles) > 256 {
		output.ChangedFiles = output.ChangedFiles[:256]
	}
	if value.LatestRun != nil {
		runValue := runOutput(*value.LatestRun, true)
		output.LatestRun = &runValue
	}
	return output
}

func projectContextOutput(value projectcontext.ProjectContext) ProjectContextOutput {
	return ProjectContextOutput{ProjectID: value.ProjectID, Status: string(value.Status), RootGuidanceIdentity: value.RootGuidanceIdentity, LatestProbeID: value.LatestProbeID, Result: boundedUTF8(value.Result, 64<<10), ResultArtifactRef: value.ResultArtifactRef, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}
}

func contextProbeOutputs(values []contextprobe.Probe) []ContextProbeOutput {
	result := make([]ContextProbeOutput, 0, len(values))
	for _, value := range values {
		result = append(result, contextProbeOutput(value))
	}
	return result
}

func contextProbeOutput(value contextprobe.Probe) ContextProbeOutput {
	result := ContextProbeOutput{ID: value.ID, TaskID: value.TaskID, RunID: value.RunID, AgentExecutionID: value.AgentExecutionID, AISessionID: value.AISessionID, ProjectID: value.ProjectID, RepositoryID: value.RepositoryID, CodexConfigID: value.CodexConfigID, Scope: ProbeScopeOutput{Kind: string(value.Scope.Kind), Paths: append([]string(nil), value.Scope.Paths...)}, CodexInstruction: value.CodexInstruction, Status: string(value.Status), ThreadID: value.ThreadID, TurnID: value.TurnID, StartedAt: formattedTime(value.StartedAt), FinishedAt: formattedTime(value.FinishedAt), TokenUsage: TokenUsageOutput{Status: string(value.TokenUsage.Status), ModelContextWindow: value.TokenUsage.ModelContextWindow}, Result: boundedUTF8(value.Result, 64<<10), ResultArtifactRef: value.ResultArtifactRef, FailureMessage: boundedUTF8(value.FailureMessage, 2048), Canonical: value.Canonical, WorkspaceID: value.WorkspaceID, RuntimeWorksetSHA256: value.RuntimeWorksetSHA256, ReportSHA256: value.ReportSHA256, ResultSHA256: value.ResultSHA256, RuntimeLimitation: value.RuntimeLimitation, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}
	if value.TokenUsage.Usage != nil {
		usage := tokenCountsOutput(*value.TokenUsage.Usage)
		result.TokenUsage.Usage = &usage
	}
	return result
}
