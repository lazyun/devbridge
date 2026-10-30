package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/review"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxNameBytes       = 256
	maxGoalBytes       = 16 << 10
	maxConstraintCount = 32
	maxConstraintBytes = 2 << 10
)

type sessionOpenInput struct {
	SelectionID string `json:"selectionId"`
}
type projectSelectionRecordInput struct {
	ProjectID string `json:"projectId"`
}
type projectListInput struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}

type sessionInput struct {
	AISessionID string `json:"aiSessionId"`
}

type listInput struct {
	AISessionID string `json:"aiSessionId"`
	Limit       int    `json:"limit,omitempty"`
	Offset      int    `json:"offset,omitempty"`
}

type taskGetInput struct {
	AISessionID string `json:"aiSessionId"`
	TaskID      string `json:"taskId"`
}

type repositoryGetInput struct {
	AISessionID  string `json:"aiSessionId"`
	RepositoryID string `json:"repositoryId"`
}

type ProjectMetadata struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

type AISessionOutput struct {
	ID                   string          `json:"id"`
	ProjectID            string          `json:"projectId"`
	ProjectContextStatus string          `json:"projectContextStatus"`
	Project              ProjectMetadata `json:"project"`
	CreatedAt            string          `json:"createdAt"`
	UpdatedAt            string          `json:"updatedAt"`
}

type ProjectListItemOutput struct {
	ProjectID            string `json:"projectId"`
	Name                 string `json:"name"`
	ProjectContextStatus string `json:"projectContextStatus"`
	RepositoryCount      int    `json:"repositoryCount"`
}
type ProjectListOutput struct {
	Projects []ProjectListItemOutput `json:"projects"`
}

type ProjectSelectionOutput struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Status      string `json:"status"`
	AISessionID string `json:"aiSessionId,omitempty"`
	CreatedAt   string `json:"createdAt"`
	ConsumedAt  string `json:"consumedAt,omitempty"`
}

type ModuleOutput struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"projectId"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	SystemOwned bool    `json:"systemOwned"`
	ParentID    *string `json:"parentId,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   string  `json:"updatedAt"`
}

type ModuleListOutput struct {
	Modules []ModuleOutput `json:"modules"`
}

type RepositoryOutput struct {
	ID                string `json:"id"`
	ProjectID         string `json:"projectId"`
	Name              string `json:"name"`
	CurrentHeadCommit string `json:"currentHeadCommit"`
}

type RepositoryListOutput struct {
	Repositories []RepositoryOutput `json:"repositories"`
}

type WorkSpecOutput struct {
	Goal             string              `json:"goal"`
	Constraints      []string            `json:"constraints,omitempty"`
	CodexInstruction string              `json:"codexInstruction,omitempty"`
	ExecutionScope   task.ExecutionScope `json:"executionScope"`
}

type VerificationCheckOutput struct {
	Kind        string   `json:"kind"`
	Command     string   `json:"command,omitempty"`
	Instruction string   `json:"instruction,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	Packages    []string `json:"packages,omitempty"`
	Required    bool     `json:"required"`
}

type VerificationWaiverOutput struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type VerificationSpecOutput struct {
	Mode               string                    `json:"mode"`
	CostAccounting     string                    `json:"costAccounting"`
	Scope              string                    `json:"scope,omitempty"`
	Checks             []VerificationCheckOutput `json:"checks,omitempty"`
	CheckCount         int                       `json:"checkCount"`
	AcceptanceCriteria []string                  `json:"acceptanceCriteria,omitempty"`
	CodexInstruction   string                    `json:"codexInstruction,omitempty"`
	Waiver             *VerificationWaiverOutput `json:"waiver,omitempty"`
}

type TaskOutput struct {
	RequiredReviewDepth *task.ReviewDepth       `json:"requiredReviewDepth"`
	AllowedReviewDepths []task.ReviewDepth      `json:"allowedReviewDepths"`
	Title               string                  `json:"title"`
	Phase               task.Phase              `json:"phase"`
	CurrentReviewID     string                  `json:"currentReviewId,omitempty"`
	CurrentExecutionID  string                  `json:"currentExecutionId,omitempty"`
	Work                task.Work               `json:"work"`
	ID                  string                  `json:"id"`
	ProjectID           string                  `json:"projectId"`
	ModuleID            string                  `json:"moduleId"`
	RepositoryID        string                  `json:"repositoryId"`
	ProblemStatement    string                  `json:"problemStatement"`
	ConfirmedTaskPlanID string                  `json:"confirmedTaskPlanId,omitempty"`
	Goal                string                  `json:"goal,omitempty"`
	WorkSpec            *WorkSpecOutput         `json:"workSpec,omitempty"`
	VerificationSpec    *VerificationSpecOutput `json:"verificationSpec,omitempty"`
	Status              string                  `json:"status"`
	CodexConfigID       string                  `json:"codexConfigId"`
	Constraints         []string                `json:"constraints,omitempty"`
	BaseCommit          string                  `json:"baseCommit,omitempty"`
	CreatedAt           string                  `json:"createdAt"`
	UpdatedAt           string                  `json:"updatedAt"`
}

type TaskListOutput struct {
	Tasks []TaskOutput `json:"tasks"`
}

type CodexConfigOutput struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type CodexConfigListOutput struct {
	CodexConfigs []CodexConfigOutput `json:"codexConfigs"`
}

const (
	executionScopeOutputSchema   = `{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY","LEGACY_UNSPECIFIED"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string","maxLength":4096}}},"required":["kind"],"additionalProperties":false}`
	workSpecSchema               = `{"type":"object","properties":{"commandPermissions":` + commandPermissionsSchema + `,"implementationMode":{"type":"string","enum":["CODING_AGENT","DIRECT_FILE"]},"goal":{"type":"string","minLength":1,"maxLength":16384},"constraints":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":2048}},"codexInstruction":{"type":"string","description":"Complete, standalone Implementation instructions for the selected implementation channel: state the role, concrete goal and changes, exact scope, preserved content, technical limits, checks and deliverables. Incorporate resolved user decisions. Do not depend on prior chat or require another approval or a receipt search. Include engineering work only; omit workflow IDs, authorization conditions and Core receipt lookups. Core enforces authorization before dispatch.","maxLength":65536},"executionScope":` + executionScopeOutputSchema + `},"required":["goal","executionScope"],"additionalProperties":false}`
	verificationCheckSchema      = `{"type":"object","properties":{"repositoryId":{"type":"string","minLength":1},"workingDirectory":{"type":"string","minLength":1},"repositoryIds":{"type":"array","minItems":1,"maxItems":128,"items":{"type":"string","minLength":1}},"kind":{"type":"string","enum":["TEST_COMMAND","STATIC_ANALYSIS","DIFF_CHECK","GIT_ASSERTION","CUSTOM"]},"command":{"type":"string","maxLength":8192},"instruction":{"type":"string","maxLength":8192},"paths":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":2048}},"packages":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":2048}},"required":{"type":"boolean"}},"required":["kind","required"],"additionalProperties":false}`
	verificationSpecSchema       = `{"type":"object","properties":{"mode":{"type":"string","enum":["RUN","NONE"]},"costAccounting":{"type":"string","enum":["NOT_REQUESTED","RECORDED_PER_RUN"]},"scope":{"type":"string","maxLength":16384},"checks":{"type":"array","maxItems":32,"items":` + verificationCheckSchema + `},"checkCount":{"type":"integer","minimum":0,"maximum":32},"acceptanceCriteria":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":2048}},"codexInstruction":{"type":"string","description":"Complete, standalone Verification instructions: state the verification role, exact target and scope, checks, expected evidence, acceptance criteria, limits and deliverables. Do not use the Implementation instructions, depend on prior chat or require another approval or a receipt search. Include engineering checks only; omit workflow IDs, authorization conditions and Core receipt lookups. Applicable only when mode is RUN; Core enforces authorization before dispatch.","maxLength":65536},"waiver":{"type":"object","properties":{"source":{"type":"string","enum":["USER_WAIVED","LEGACY_UNSPECIFIED"]},"reason":{"type":"string","maxLength":2048}},"required":["source","reason"],"additionalProperties":false}},"required":["mode","costAccounting","checkCount"],"additionalProperties":false}`
	emptyInputSchema             = `{"type":"object","properties":{},"additionalProperties":false}`
	projectListInputSchema       = `{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0,"maximum":1000000}},"additionalProperties":false}`
	projectListOutputSchema      = `{"type":"object","properties":{"projects":{"type":"array","maxItems":100,"items":{"type":"object","properties":{"projectId":{"type":"string"},"name":{"type":"string","maxLength":256},"projectContextStatus":{"type":"string","enum":["REQUIRED","RUNNING","READY","NO_GUIDANCE","FAILED"]},"repositoryCount":{"type":"integer","minimum":0}},"required":["projectId","name","projectContextStatus","repositoryCount"],"additionalProperties":false}}},"required":["projects"],"additionalProperties":false}`
	projectSelectionInputSchema  = `{"type":"object","properties":{"projectId":{"type":"string","minLength":1,"maxLength":128}},"required":["projectId"],"additionalProperties":false}`
	projectSelectionOutputSchema = `{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"status":{"type":"string","enum":["RECORDED","CONSUMED"]},"aiSessionId":{"type":"string"},"createdAt":{"type":"string"},"consumedAt":{"type":"string"}},"required":["id","projectId","status","createdAt"],"additionalProperties":false}`
	sessionOpenInputSchema       = `{"type":"object","properties":{"selectionId":{"type":"string","minLength":1,"maxLength":128}},"required":["selectionId"],"additionalProperties":false}`
	sessionInputSchema           = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId"],"additionalProperties":false}`
	listInputSchema              = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0,"maximum":1000000}},"required":["aiSessionId"],"additionalProperties":false}`
	taskGetInputSchema           = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","taskId"],"additionalProperties":false}`
	repositoryGetInputSchema     = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","repositoryId"],"additionalProperties":false}`
	sessionOutputSchema          = `{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"projectContextStatus":{"type":"string","enum":["REQUIRED","RUNNING","READY","NO_GUIDANCE","FAILED"]},"project":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string","maxLength":256},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","name","createdAt","updatedAt"],"additionalProperties":false},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","projectId","projectContextStatus","project","createdAt","updatedAt"],"additionalProperties":false}`
	moduleListOutputSchema       = `{"type":"object","properties":{"modules":{"type":"array","maxItems":100,"items":{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"key":{"type":"string"},"name":{"type":"string"},"description":{"type":"string"},"systemOwned":{"type":"boolean"},"parentId":{"type":"string"},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","projectId","key","name","description","systemOwned","createdAt","updatedAt"],"additionalProperties":false}}},"required":["modules"],"additionalProperties":false}`
	repositoryOutputSchema       = `{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"name":{"type":"string","maxLength":256},"currentHeadCommit":{"type":"string","pattern":"^([0-9a-f]{40}|[0-9a-f]{64})$"}},"required":["id","projectId","name","currentHeadCommit"],"additionalProperties":false}`
	repositoryListOutputSchema   = `{"type":"object","properties":{"repositories":{"type":"array","maxItems":100,"items":{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"name":{"type":"string","maxLength":256},"currentHeadCommit":{"type":"string","pattern":"^([0-9a-f]{40}|[0-9a-f]{64})$"}},"required":["id","projectId","name","currentHeadCommit"],"additionalProperties":false}}},"required":["repositories"],"additionalProperties":false}`
	taskOutputSchema             = `{"type":"object","properties":{"requiredReviewDepth":{"type":["string","null"],"enum":["FAST","DEEP",null]},"allowedReviewDepths":{"type":"array","items":{"type":"string","enum":["FAST","DEEP"]}},"title":{"type":"string"},"phase":{"type":"string"},"currentReviewId":{"type":"string"},"currentExecutionId":{"type":"string"},"work":{"type":"object","properties":{"executionId":{"type":"string"},"executionProposalId":{"type":"string"},"executionKind":{"type":"string"},"executionStatus":{"type":"string"},"domainStatus":{"type":"string"},"domainResult":{"type":"string"},"type":{"type":"string"},"scope":{"type":"string"},"phase":{"type":"string"},"status":{"type":"string"},"ownerType":{"type":"string"},"ownerId":{"type":"string"},"runId":{"type":"string"},"runStatus":{"type":"string"},"agent":{"type":"string"},"developmentBranches":{"type":["array","null"],"items":{"type":"string"}},"developmentBranchProvenance":{"type":"string"},"tokenAvailability":{"type":"string"},"updatedAt":{"type":"string"},"cancellationState":{"type":"string"},"backendDisposition":{"type":"string"},"provenance":{"type":"string"},"readOnly":{"type":"boolean"}},"required":["type","scope","phase","status","ownerType","ownerId","agent","tokenAvailability","updatedAt","provenance","readOnly"],"additionalProperties":false},"id":{"type":"string"},"projectId":{"type":"string"},"moduleId":{"type":"string"},"repositoryId":{"type":"string"},"problemStatement":{"type":"string","maxLength":16384},"confirmedTaskPlanId":{"type":"string"},"goal":{"type":"string"},"workSpec":` + workSpecSchema + `,"verificationSpec":` + verificationSpecSchema + `,"status":{"type":"string","enum":["ACTIVE","SUCCEEDED","FAILED","CANCELED"]},"codexConfigId":{"type":"string"},"constraints":{"type":"array","items":{"type":"string"}},"baseCommit":{"type":"string"},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","projectId","moduleId","repositoryId","problemStatement","status","codexConfigId","createdAt","updatedAt"],"additionalProperties":false}`
	taskListOutputSchema         = `{"type":"object","properties":{"tasks":{"type":"array","maxItems":100,"items":` + taskOutputSchema + `}},"required":["tasks"],"additionalProperties":false}`
	codexConfigListOutputSchema  = `{"type":"object","properties":{"codexConfigs":{"type":"array","maxItems":100,"items":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"},"enabled":{"type":"boolean"}},"required":["id","name","enabled"],"additionalProperties":false}}},"required":["codexConfigs"],"additionalProperties":false}`
)

func registerContextTools(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "project_list", Title: "List Projects", Description: "This is the next bootstrap step after Bridge readiness when the user is starting to use Dev Bridge and there is no active or bound AI session. Enumerate candidate Projects only; this does not select a Project or create an AI session, Task, TaskPlan, or Run. After receiving results, you MUST present the candidates to the user, MUST stop, and MUST wait for a subsequent explicit user selection of a concrete Project. A minimal start-use request such as \"use Dev Bridge\" or \"initialize Dev Bridge\" is not Project selection authority. You MUST NOT infer or choose a Project yourself, even when exactly one Project exists (the one available Project), or from Console selected Project UI state (Console selection), previous admin-fixture selection, Project name guesses, \"this project\", or the mere fact that the user said to use Dev Bridge.",
		InputSchema: json.RawMessage(projectListInputSchema), OutputSchema: json.RawMessage(projectListOutputSchema), Annotations: toolAnnotations("List Projects", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input projectListInput) (*sdkmcp.CallToolResult, ProjectListOutput, error) {
		values, err := service.ListProjects(ctx, aicontext.ListOptions{Limit: input.Limit, Offset: input.Offset})
		if err != nil {
			return nil, ProjectListOutput{}, publicToolError(err)
		}
		items := make([]ProjectListItemOutput, 0, len(values))
		for _, value := range values {
			items = append(items, ProjectListItemOutput{ProjectID: value.ID, Name: boundedUTF8(value.Name, maxNameBytes), ProjectContextStatus: string(value.ProjectContextStatus), RepositoryCount: value.RepositoryCount})
		}
		return nil, ProjectListOutput{Projects: items}, nil
	})
	// Historical names remain explicitly closed; they must not expose a second
	// Session creation path even if this older registration surface is used.
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "project_selection_record", Title: "Legacy selection entry closed", Description: "Retired creation entry. Use session_initialize(requestId) to obtain a real UNBOUND Session, then project_open with that same Session, the user's Project and a stable binding requestId. This legacy call creates no record.", InputSchema: json.RawMessage(projectSelectionInputSchema), OutputSchema: json.RawMessage(projectSelectionOutputSchema), Annotations: toolAnnotations("Legacy selection closed", true, true)}, func(context.Context, *sdkmcp.CallToolRequest, projectSelectionRecordInput) (*sdkmcp.CallToolResult, ProjectSelectionOutput, error) {
		return nil, ProjectSelectionOutput{}, publicToolError(aisession.ErrInitializationRequired)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "session_open", Title: "Legacy Session creation closed", Description: "Retired creation entry. Only session_initialize creates new Sessions; a historical selectionId cannot create or exchange for a Session. Read an already known Session with session_initialize(aiSessionId).", InputSchema: json.RawMessage(sessionOpenInputSchema), OutputSchema: json.RawMessage(sessionOutputSchema), Annotations: toolAnnotations("Legacy Session creation closed", true, true)}, func(context.Context, *sdkmcp.CallToolRequest, sessionOpenInput) (*sdkmcp.CallToolResult, AISessionOutput, error) {
		return nil, AISessionOutput{}, publicToolError(aisession.ErrInitializationRequired)
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "session_get", Title: "Get AI session", Description: "Read an existing BOUND Session with its immutable Project metadata. New initialization and UNBOUND reads use session_initialize; this historical read never creates or binds a Session.",
		InputSchema: json.RawMessage(sessionInputSchema), OutputSchema: json.RawMessage(sessionOutputSchema), Annotations: toolAnnotations("Get AI session", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input sessionInput) (*sdkmcp.CallToolResult, AISessionOutput, error) {
		value, err := service.GetSession(ctx, input.AISessionID)
		if err != nil {
			return nil, AISessionOutput{}, publicToolError(err)
		}
		return nil, sessionOutput(value), nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "module_list", Title: "List Project modules", Description: "List only the registered Modules belonging to the AI session's immutable Project.",
		InputSchema: json.RawMessage(listInputSchema), OutputSchema: json.RawMessage(moduleListOutputSchema), Annotations: toolAnnotations("List Project modules", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input listInput) (*sdkmcp.CallToolResult, ModuleListOutput, error) {
		values, err := service.ListModules(ctx, input.AISessionID, aicontext.ListOptions{Limit: input.Limit, Offset: input.Offset})
		if err != nil {
			return nil, ModuleListOutput{}, publicToolError(err)
		}
		return nil, ModuleListOutput{Modules: moduleOutputs(values)}, nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "repository_list", Title: "List Project repositories", Description: "List bounded, path-free Repository context for the AI session's immutable Project, including each current full HEAD commit.",
		InputSchema: json.RawMessage(listInputSchema), OutputSchema: json.RawMessage(repositoryListOutputSchema), Annotations: toolAnnotations("List Project repositories", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input listInput) (*sdkmcp.CallToolResult, RepositoryListOutput, error) {
		values, err := service.ListRepositories(ctx, input.AISessionID, aicontext.ListOptions{Limit: input.Limit, Offset: input.Offset})
		if err != nil {
			return nil, RepositoryListOutput{}, publicToolError(err)
		}
		return nil, RepositoryListOutput{Repositories: repositoryOutputs(values)}, nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "repository_get", Title: "Get Project repository", Description: "Get path-free Repository context and its current full HEAD commit after validating ownership by the AI session's immutable Project.",
		InputSchema: json.RawMessage(repositoryGetInputSchema), OutputSchema: json.RawMessage(repositoryOutputSchema), Annotations: toolAnnotations("Get Project repository", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input repositoryGetInput) (*sdkmcp.CallToolResult, RepositoryOutput, error) {
		value, err := service.GetRepository(ctx, input.AISessionID, input.RepositoryID)
		if err != nil {
			return nil, RepositoryOutput{}, publicToolError(err)
		}
		return nil, repositoryOutput(value), nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "task_list", Title: "List Project tasks", Description: "List bounded structured Task summaries for the AI session's immutable Project without semantic ranking.",
		InputSchema: json.RawMessage(listInputSchema), OutputSchema: json.RawMessage(taskListOutputSchema), Annotations: toolAnnotations("List Project tasks", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input listInput) (*sdkmcp.CallToolResult, TaskListOutput, error) {
		values, err := service.ListTasks(ctx, input.AISessionID, aicontext.ListOptions{Limit: input.Limit, Offset: input.Offset})
		if err != nil {
			return nil, TaskListOutput{}, publicToolError(err)
		}
		return nil, TaskListOutput{Tasks: taskOutputs(values, false)}, nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "task_get", Title: "Get Project task", Description: "Get one Task after validating that it belongs to the AI session's immutable Project.",
		InputSchema: json.RawMessage(taskGetInputSchema), OutputSchema: json.RawMessage(taskOutputSchema), Annotations: toolAnnotations("Get Project task", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input taskGetInput) (*sdkmcp.CallToolResult, TaskOutput, error) {
		value, err := service.GetTask(ctx, input.AISessionID, input.TaskID)
		if err != nil {
			return nil, TaskOutput{}, publicToolError(err)
		}
		return nil, taskOutput(value, true), nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: "codex_config_list", Title: "List Codex configurations", Description: "List bounded non-secret metadata for Bridge Codex configurations available to the AI session.",
		InputSchema: json.RawMessage(listInputSchema), OutputSchema: json.RawMessage(codexConfigListOutputSchema), Annotations: toolAnnotations("List Codex configurations", true, true),
	}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input listInput) (*sdkmcp.CallToolResult, CodexConfigListOutput, error) {
		values, err := service.ListCodexConfigs(ctx, input.AISessionID, aicontext.ListOptions{Limit: input.Limit, Offset: input.Offset})
		if err != nil {
			return nil, CodexConfigListOutput{}, publicToolError(err)
		}
		return nil, CodexConfigListOutput{CodexConfigs: codexConfigOutputs(values)}, nil
	})
}

func projectSelectionOutput(value aiprojectselection.Selection) ProjectSelectionOutput {
	result := ProjectSelectionOutput{ID: value.ID, ProjectID: value.ProjectID, Status: string(value.Status), AISessionID: value.AISessionID, CreatedAt: value.CreatedAt.UTC().Format(timeFormat)}
	if value.ConsumedAt != nil {
		result.ConsumedAt = value.ConsumedAt.UTC().Format(timeFormat)
	}
	return result
}

func toolAnnotations(title string, readOnly, idempotent bool) *sdkmcp.ToolAnnotations {
	nonDestructive := false
	closedWorld := false
	return &sdkmcp.ToolAnnotations{Title: title, ReadOnlyHint: readOnly, DestructiveHint: &nonDestructive, IdempotentHint: idempotent, OpenWorldHint: &closedWorld}
}

func stateChangingAnnotations(title string, destructive bool) *sdkmcp.ToolAnnotations {
	closedWorld := false
	return &sdkmcp.ToolAnnotations{Title: title, ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: false, OpenWorldHint: &closedWorld}
}

func sessionOutput(value aicontext.SessionContext) AISessionOutput {
	return AISessionOutput{
		ID: value.Session.ID, ProjectID: value.Session.ProjectID, ProjectContextStatus: string(value.ProjectContext.Status),
		Project:   ProjectMetadata{ID: value.Project.ID, Name: boundedUTF8(value.Project.Name, maxNameBytes), CreatedAt: value.Project.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.Project.UpdatedAt.UTC().Format(timeFormat)},
		CreatedAt: value.Session.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.Session.UpdatedAt.UTC().Format(timeFormat),
	}
}

func moduleOutputs(values []module.Module) []ModuleOutput {
	result := make([]ModuleOutput, 0, len(values))
	for _, value := range values {
		result = append(result, ModuleOutput{ID: value.ID, ProjectID: value.ProjectID, Key: value.Key, Name: boundedUTF8(value.Name, maxNameBytes), Description: boundedUTF8(value.Description, 4096), SystemOwned: value.SystemOwned, ParentID: value.ParentID, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)})
	}
	return result
}

func repositoryOutputs(values []aicontext.RepositoryContext) []RepositoryOutput {
	result := make([]RepositoryOutput, 0, len(values))
	for _, value := range values {
		result = append(result, repositoryOutput(value))
	}
	return result
}

func repositoryOutput(value aicontext.RepositoryContext) RepositoryOutput {
	return RepositoryOutput{ID: value.ID, ProjectID: value.ProjectID, Name: boundedUTF8(value.Name, maxNameBytes), CurrentHeadCommit: value.CurrentHeadCommit}
}

func taskOutputs(values []task.Task, details bool) []TaskOutput {
	result := make([]TaskOutput, 0, len(values))
	for _, value := range values {
		result = append(result, taskOutput(value, details))
	}
	return result
}

func taskOutput(value task.Task, details bool) TaskOutput {
	result := TaskOutput{RequiredReviewDepth: value.RequiredReviewDepth, AllowedReviewDepths: task.AllowedReviewDepths(value.RequiredReviewDepth), Title: value.Title, Phase: value.Phase, CurrentReviewID: value.CurrentReviewID, CurrentExecutionID: value.CurrentExecutionID, Work: value.Work, ID: value.ID, ProjectID: value.ProjectID, ModuleID: value.ModuleID, RepositoryID: value.RepositoryID, ProblemStatement: boundedUTF8(value.ProblemStatement, maxGoalBytes), ConfirmedTaskPlanID: value.ConfirmedTaskPlanID, Goal: boundedUTF8(value.Goal, maxGoalBytes), Status: string(value.Status), CodexConfigID: value.CodexConfigID, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}
	if (value.Type == "" || value.Type == task.TypeDevelopment) && value.CurrentDefinitionRevisionID != "" {
		work := workSpecOutput(value.WorkSpec, details)
		verification := verificationSpecOutput(value.VerificationSpec, details)
		result.WorkSpec, result.VerificationSpec = &work, &verification
	}
	if details {
		constraintCount := len(value.Constraints)
		if constraintCount > maxConstraintCount {
			constraintCount = maxConstraintCount
		}
		result.Constraints = make([]string, 0, constraintCount)
		for _, constraint := range value.Constraints[:constraintCount] {
			result.Constraints = append(result.Constraints, boundedUTF8(constraint, maxConstraintBytes))
		}
		result.BaseCommit = value.BaseCommit
	}
	return result
}

func workSpecOutput(value task.WorkSpec, details bool) WorkSpecOutput {
	result := WorkSpecOutput{Goal: boundedUTF8(value.Goal, task.MaxGoalBytes), ExecutionScope: task.ExecutionScope{Kind: value.ExecutionScope.Kind, Paths: append([]string(nil), value.ExecutionScope.Paths...)}}
	if details {
		result.Constraints = boundedStrings(value.Constraints, task.MaxConstraintCount, task.MaxConstraintBytes)
		result.CodexInstruction = boundedUTF8(value.CodexInstruction, task.MaxCodexInstructionBytes)
	}
	return result
}

func verificationSpecOutput(value task.VerificationSpec, details bool) VerificationSpecOutput {
	costAccounting := "RECORDED_PER_RUN"
	if value.Mode == task.VerificationModeNone {
		costAccounting = "NOT_REQUESTED"
	}
	result := VerificationSpecOutput{Mode: string(value.Mode), CostAccounting: costAccounting, Scope: boundedUTF8(value.Scope, task.MaxVerificationScopeBytes), CheckCount: len(value.Checks)}
	if value.Waiver != nil {
		result.Waiver = &VerificationWaiverOutput{Source: string(value.Waiver.Source), Reason: boundedUTF8(value.Waiver.Reason, task.MaxWaiverReasonBytes)}
	}
	if details {
		result.Checks = make([]VerificationCheckOutput, 0, len(value.Checks))
		for _, check := range value.Checks {
			result.Checks = append(result.Checks, VerificationCheckOutput{Kind: string(check.Kind), Command: boundedUTF8(check.Command, task.MaxVerificationCheckValueBytes), Instruction: boundedUTF8(check.Instruction, task.MaxVerificationCheckValueBytes), Paths: boundedStrings(check.Paths, task.MaxVerificationPathCount, task.MaxVerificationPathBytes), Packages: boundedStrings(check.Packages, task.MaxVerificationPathCount, task.MaxVerificationPathBytes), Required: check.Required})
		}
		result.AcceptanceCriteria = boundedStrings(value.AcceptanceCriteria, task.MaxAcceptanceCriterionCount, task.MaxAcceptanceCriterionBytes)
		result.CodexInstruction = boundedUTF8(value.CodexInstruction, task.MaxCodexInstructionBytes)
	}
	return result
}

func boundedStrings(values []string, maxCount, maxBytes int) []string {
	if len(values) > maxCount {
		values = values[:maxCount]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, boundedUTF8(value, maxBytes))
	}
	return result
}

func codexConfigOutputs(values []codexconfig.Config) []CodexConfigOutput {
	result := make([]CodexConfigOutput, 0, len(values))
	for _, value := range values {
		result = append(result, CodexConfigOutput{ID: value.ID, Name: boundedUTF8(value.Name, maxNameBytes), Enabled: value.Enabled})
	}
	return result
}

// Keep the existing safe wire text for Session admission failures without
// dropping their typed cause before the SDK serializes the tool error.
type sessionAdmissionToolError struct {
	message string
	cause   error
}

func (e *sessionAdmissionToolError) Error() string { return e.message }
func (e *sessionAdmissionToolError) Unwrap() error { return e.cause }

func publicToolError(err error) error {
	var depthErr *review.DepthTooShallowError
	if errors.As(err, &depthErr) {
		raw, _ := json.Marshal(struct {
			Code string `json:"code"`
			*review.DepthTooShallowError
		}{"REVIEW_DEPTH_TOO_SHALLOW", depthErr})
		return errors.New(string(raw))
	}
	var progressionErr *wp.Error
	if errors.As(err, &progressionErr) {
		raw, _ := json.Marshal(progressionErr)
		return errors.New(string(raw))
	}
	if err == nil {
		return nil
	}
	var apiErr *controlapi.Error
	if errors.As(err, &apiErr) {
		if apiErr == nil {
			return errors.New("INTERNAL_ERROR: typed nil control-plane error")
		}
		if apiErr.Code == controlapi.CodeConfirmationCallerContextRequired {
			return &sessionAdmissionToolError{message: string(apiErr.Code) + ": " + apiErr.Message, cause: err}
		}
		return errors.New(string(apiErr.Code) + ": " + apiErr.Message)
	}
	switch {
	case errors.Is(err, aisession.ErrNotFound):
		return errors.New("AI_SESSION_NOT_FOUND: the requested Session is unavailable; do not initialize a replacement implicitly")
	case errors.Is(err, aisession.ErrInitializationRequired):
		return errors.New("SESSION_INITIALIZATION_REQUIRED: call session_initialize first; legacy creation entries are closed")
	case errors.Is(err, aisession.ErrProjectUnbound):
		return &sessionAdmissionToolError{message: "SESSION_PROJECT_REQUIRED: explicitly bind this existing Session before Project work", cause: err}
	case errors.Is(err, aisession.ErrInvalidInitialization):
		return errors.New("INVALID_SESSION_INITIALIZATION: supply exactly one valid requestId or aiSessionId")
	case errors.Is(err, aisession.ErrScopeMismatch):
		return &sessionAdmissionToolError{message: "SESSION_SCOPE_MISMATCH: Session belongs to another caller scope", cause: err}
	case errors.Is(err, aisession.ErrConflict):
		return errors.New("SESSION_REQUEST_CONFLICT: request or immutable binding does not match")
	case errors.Is(err, aicontext.ErrSessionProjectAlreadyBound):
		return errors.New("AI_SESSION_PROJECT_ALREADY_BOUND: 当前会话已绑定其他项目，请新建会话后切换项目。")
	case errors.Is(err, aicontext.ErrSelectionConsumed):
		return errors.New("AI Project selection has already been consumed")
	case errors.Is(err, aicontext.ErrInvalidArgument):
		return errors.New("invalid tool arguments")
	case errors.Is(err, aicontext.ErrNotFound):
		return errors.New("requested session-scoped entity was not found")
	default:
		return err
	}
}

func boundedUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"
