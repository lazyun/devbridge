package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/directfile"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DirectFileService is optional so existing test doubles and deployments can
// continue to satisfy BridgeService while the Core capability rolls out.
type DirectFileService interface {
	DirectFileEnable(context.Context, directfile.Scope) (directfile.State, error)
	DirectFileDisable(context.Context, directfile.Scope) (directfile.State, error)
	DirectFileFileList(context.Context, directfile.Input) (directfile.Result, error)
	DirectFileFileRead(context.Context, directfile.Input) (directfile.Result, error)
	DirectFileFileCreate(context.Context, directfile.Input) (directfile.Result, error)
	DirectFileFileReplace(context.Context, directfile.Input) (directfile.Result, error)
	DirectFileFileDelete(context.Context, directfile.Input) (directfile.Result, error)
	DirectFileFileMove(context.Context, directfile.Input) (directfile.Result, error)
}

const directFileScopeSchema = `"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"workflowId":{"type":"string","minLength":1,"maxLength":128}`

const directFileMemberSchema = `,"repositoryId":{"type":"string","minLength":1,"maxLength":128},"memberId":{"type":"string","minLength":1,"maxLength":128},"memberVersion":{"type":"integer","minimum":1}`
const directFileDestinationMemberSchema = `,"destinationRepositoryId":{"type":"string","minLength":1,"maxLength":128},"destinationMemberId":{"type":"string","minLength":1,"maxLength":128}`

// v2 task mutations are independently replayable operations.  The authority
// validates these values against its durable grant; the schema ensures a v2
// caller cannot accidentally omit one and fall back to legacy semantics.
const directFileMutationMetaSchema = `,"protocolVersion":{"type":"integer","enum":[1,2]},"requestId":{"type":"string","minLength":1,"maxLength":128},"runtimeEpoch":{"type":"string","minLength":1,"maxLength":128},"grantGeneration":{"type":"integer","minimum":1},"expectedDigest":{"type":"string","pattern":"^[a-f0-9]{64}$"}`
const directFileV2MutationRequiredSchema = `,{"if":{"properties":{"protocolVersion":{"const":2}},"required":["protocolVersion"]},"then":{"required":["requestId","runtimeEpoch","grantGeneration","repositoryId","memberId","memberVersion"]}}`
const directFileV2ExistingMutationRequiredSchema = `,{"if":{"properties":{"protocolVersion":{"const":2}},"required":["protocolVersion"]},"then":{"required":["requestId","runtimeEpoch","grantGeneration","expectedDigest","repositoryId","memberId","memberVersion"]}}`

// Keep the decoder-input bound in the public schema. The Core then applies
// directfile.MaxWriteBytes to decoded bytes for both utf8 and base64 content.
const directFileContentMaxLength = directfile.MaxBase64EncodedBytes

const directFileOutputSchema = `{"type":"object","properties":{"memberId":{"type":"string"},"memberVersion":{"type":"integer","minimum":1},"sourceKind":{"type":"string"},"originCommit":{"type":"string"},"startSnapshot":{"type":"string"},"evidenceId":{"type":"string"},"digest":{"type":"string"},"path":{"type":"string"},"content":{"type":"string"},"contentEncoding":{"type":"string","enum":["utf8","base64"]},"size":{"type":"integer","minimum":0},"entries":{"type":"array","maxItems":256,"items":{"type":"object","properties":{"name":{"type":"string"},"type":{"type":"string","enum":["file","directory","symlink"]}},"required":["name","type"],"additionalProperties":false}},"truncated":{"type":"boolean"},"workflowComplete":{"type":"boolean"},"state":{"type":"string"},"aiSessionId":{"type":"string"},"taskId":{"type":"string"},"taskDefinitionRevisionId":{"type":"string"},"decisionId":{"type":"string"},"workspaceId":{"type":"string"},"allowedOperations":{"type":"array","items":{"type":"string"}},"executionScope":{"type":"object","properties":{"kind":{"type":"string","enum":["","PATHS","REPOSITORY"]},"paths":{"type":"array","items":{"type":"string"}}},"required":["kind"],"additionalProperties":false},"failureCode":{"type":"string"},"failureReason":{"type":"string"},"startedAt":{"type":"string"},"lastOperation":{"type":"string"},"enabled":{"type":"boolean"},"projectId":{"type":"string"},"repositoryId":{"type":"string"},"featureAreaId":{"type":"string"},"worktreeId":{"type":"string"},"workflowId":{"type":"string"},"protocolVersion":{"type":"integer","enum":[1,2]},"runtimeEpoch":{"type":"string"},"grantGeneration":{"type":"integer","minimum":1},"repositoryTargets":` + repositoryTargetsJSONSchema + `,"code":{"type":"string","minLength":1,"maxLength":64},"message":{"type":"string","maxLength":4096}},"additionalProperties":false}`

type directFileToolOutput struct {
	*directfile.Result
	*directfile.State
	// Result and State intentionally remain embedded for the stable flat wire
	// shape. Their shared source fields need an outer owner, otherwise
	// encoding/json treats the equal-depth names as ambiguous and silently
	// drops them from every successful response.
	RepositoryID string `json:"repositoryId,omitempty"`
	WorkspaceID  string `json:"workspaceId,omitempty"`
	WorktreeID   string `json:"worktreeId,omitempty"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
}

func directFileResultOutput(value directfile.Result) directFileToolOutput {
	return directFileToolOutput{Result: &value, RepositoryID: value.RepositoryID, WorkspaceID: value.WorkspaceID, WorktreeID: value.WorktreeID}
}

func directFileStateOutput(value directfile.State) directFileToolOutput {
	return directFileToolOutput{State: &value, RepositoryID: value.RepositoryID, WorkspaceID: value.WorkspaceID, WorktreeID: value.WorktreeID}
}

func directFileSchema(extra, required string) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + directFileScopeSchema + extra + `},"required":["aiSessionId","featureAreaId","workflowId"` + required + `],"additionalProperties":false}`)
}

func directFileContentSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + directFileScopeSchema + directFileMemberSchema + directFileMutationMetaSchema + `,"path":{"type":"string","minLength":1,"maxLength":4096},"content":{"type":"string"},"contentEncoding":{"type":"string","enum":["utf8","base64"]},"workflowComplete":{"type":"boolean"}},"required":["aiSessionId","featureAreaId","workflowId","path","content"],"allOf":[{"if":{"properties":{"contentEncoding":{"const":"base64"}},"required":["contentEncoding"]},"then":{"properties":{"content":{"maxLength":` + strconv.Itoa(directFileContentMaxLength) + `}}},"else":{"properties":{"content":{"maxLength":` + strconv.Itoa(directfile.MaxWriteBytes) + `}}}}` + directFileV2MutationRequiredSchema + `],"additionalProperties":false}`)
}

func RegisterDirectFileTools(server *sdkmcp.Server, service BridgeService) {
	df := optionalDirectFileService{base: service}
	add := func(name, title, description string, schema json.RawMessage, fn func(context.Context, directfile.Input) (directfile.Result, error)) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Title: title, Description: description, InputSchema: schema, OutputSchema: json.RawMessage(directFileOutputSchema), Annotations: &sdkmcp.ToolAnnotations{Title: title, ReadOnlyHint: name == "file_list" || name == "file_read", IdempotentHint: false}}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in directfile.Input) (*sdkmcp.CallToolResult, directFileToolOutput, error) {
			out, err := fn(ctx, in)
			if err == nil {
				return nil, directFileResultOutput(out), nil
			}
			return directFileStructuredError(err)
		})
	}
	scope := func(name, title, desc string, fn func(context.Context, directfile.Scope) (directfile.State, error)) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Title: title, Description: desc, InputSchema: directFileSchema("", ""), OutputSchema: json.RawMessage(directFileOutputSchema), Annotations: &sdkmcp.ToolAnnotations{Title: title, ReadOnlyHint: false, IdempotentHint: name == "direct_file_disable"}}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in directfile.Scope) (*sdkmcp.CallToolResult, directFileToolOutput, error) {
			out, err := fn(ctx, in)
			if err == nil {
				return nil, directFileStateOutput(out), nil
			}
			return directFileStructuredError(err)
		})
	}
	scope("direct_file_enable", "Enable Direct File", "Only for explicit standalone file requests independent of Task implementation. An approved Task v2 definition remains NO_START until its separately confirmed START_DIRECT_FILE step; never call enable to bypass that exact approval, scope or an active writer.", df.DirectFileEnable)
	scope("direct_file_disable", "Disable Direct File", "Disable the current Direct File workflow immediately.", df.DirectFileDisable)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "direct_file_reconcile_task_authorization", Title: "Reconcile approved Task file authorization", Description: "Explicit maintenance write for an exact existing approval receipt and current TaskDefinitionRevision. Reuses the original approval; never creates a Review/Task or edits old proposal/hash. Explicit DIRECT_FILE definitions need no second approval. Legacy definitions missing the channel require the user's already confirmed DIRECT_FILE choice recorded with confirmed=true; never infer it from prose. Closed or revoked grants remain closed. This is not a normal post-APPROVE enable step and is never invoked by GET or file operations.", InputSchema: json.RawMessage(`{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"taskDefinitionRevisionId":{"type":"string","minLength":1,"maxLength":128},"decisionId":{"type":"string","minLength":1,"maxLength":256},"implementationMode":{"type":"string","const":"DIRECT_FILE"},"confirmed":{"type":"boolean"}},"required":["aiSessionId","taskId","taskDefinitionRevisionId","decisionId"],"additionalProperties":false}`), OutputSchema: json.RawMessage(directFileOutputSchema), Annotations: stateChangingAnnotations("Reconcile approved Task file authorization", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in directfile.Registration) (*sdkmcp.CallToolResult, directFileToolOutput, error) {
		provider, ok := service.(interface {
			DirectFileReconcileTaskAuthorization(context.Context, directfile.Registration) (directfile.State, error)
		})
		if !ok {
			return directFileStructuredError(directfile.Failure(directfile.NotEnabled, "Task file authorization maintenance unavailable"))
		}
		out, err := provider.DirectFileReconcileTaskAuthorization(ctx, in)
		if err != nil {
			return directFileStructuredError(err)
		}
		return nil, directFileStateOutput(out), nil
	})

	add("file_list", "List files", "List immediate entries in a worktree-relative directory. Use either a Direct File workflowId or the exact reviewTarget from review_access_get. UPPER_LLM access is read-only; workflowComplete only closes reading and still requires review_result_submit.", reviewFileReadSchema(), df.DirectFileFileList)
	add("file_read", "Read file", "Read one bounded regular file without interpretation. Use either a Direct File workflowId or the exact reviewTarget from review_access_get. UPPER_LLM access is read-only; workflowComplete only closes reading and still requires review_result_submit.", reviewFileReadSchema(), df.DirectFileFileRead)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "file_write", Title: "Write whole file", Description: "Apply exactly CREATE, REPLACE, DELETE or MOVE under an existing ACTIVE workflowId from explicit standalone authorization or a separately confirmed START_DIRECT_FILE Task step. A v2 mutation carries its exact requestId, runtimeEpoch, grantGeneration and member identity; REPLACE, DELETE and MOVE also carry the observed source digest. CREATE never overwrites; REPLACE requires an existing regular file; DELETE is nonrecursive; MOVE does not overwrite or fall back across filesystems. Existing Core containment, symlink and size checks remain authoritative. No implicit enable, upsert, patch, append or shell. Set workflowComplete on the final call to revoke authorization.", InputSchema: json.RawMessage(SurfaceFileWriteInputSchema), OutputSchema: json.RawMessage(directFileOutputSchema), Annotations: stateChangingAnnotations("Write whole file", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in SurfaceFileWriteInput) (*sdkmcp.CallToolResult, directFileToolOutput, error) {
		out, err := SurfaceFileWriteCore(ctx, service, in)
		if err != nil {
			return directFileStructuredError(err)
		}
		return nil, directFileResultOutput(out), nil
	})
}

func directFileStructuredError(err error) (*sdkmcp.CallToolResult, directFileToolOutput, error) {
	var fileErr *directfile.Error
	if !errors.As(err, &fileErr) {
		var apiErr *controlapi.Error
		if errors.As(err, &apiErr) {
			return &sdkmcp.CallToolResult{IsError: true}, directFileToolOutput{Code: string(apiErr.Code), Message: boundedUTF8(apiErr.Message, 4096)}, nil
		}
		fileErr = &directfile.Error{Code: directfile.OperationFailed, Message: "direct file operation failed"}
	}
	out := directFileToolOutput{Code: fileErr.Code, Message: fileErr.Message}
	return &sdkmcp.CallToolResult{IsError: true}, out, nil
}

// These delegates keep the catalog stable when a deployment lacks Direct File.
// A missing optional Core port is a failure, never implicit authorization.
type optionalDirectFileService struct{ base BridgeService }

func (s optionalDirectFileService) DirectFileEnable(ctx context.Context, in directfile.Scope) (directfile.State, error) {
	p, ok := s.base.(DirectFileService)
	if !ok {
		return directfile.State{}, directfile.Failure(directfile.NotEnabled, "Direct File capability unavailable")
	}
	return p.DirectFileEnable(ctx, in)
}
func (s optionalDirectFileService) DirectFileDisable(ctx context.Context, in directfile.Scope) (directfile.State, error) {
	p, ok := s.base.(DirectFileService)
	if !ok {
		return directfile.State{}, directfile.Failure(directfile.NotEnabled, "Direct File capability unavailable")
	}
	return p.DirectFileDisable(ctx, in)
}
func (s optionalDirectFileService) DirectFileFileList(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	p, ok := s.base.(DirectFileService)
	if !ok {
		return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File capability unavailable")
	}
	return p.DirectFileFileList(ctx, in)
}
func (s optionalDirectFileService) DirectFileFileRead(ctx context.Context, in directfile.Input) (directfile.Result, error) {
	p, ok := s.base.(DirectFileService)
	if !ok {
		return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File capability unavailable")
	}
	return p.DirectFileFileRead(ctx, in)
}

type SurfaceFileWriteInput struct {
	ProtocolVersion         int    `json:"protocolVersion,omitempty"`
	RequestID               string `json:"requestId,omitempty"`
	RuntimeEpoch            string `json:"runtimeEpoch,omitempty"`
	GrantGeneration         int64  `json:"grantGeneration,omitempty"`
	ExpectedDigest          string `json:"expectedDigest,omitempty"`
	RepositoryID            string `json:"repositoryId,omitempty"`
	MemberID                string `json:"memberId,omitempty"`
	MemberVersion           int    `json:"memberVersion,omitempty"`
	DestinationRepositoryID string `json:"destinationRepositoryId,omitempty"`
	DestinationMemberID     string `json:"destinationMemberId,omitempty"`

	directfile.Scope
	Action           string  `json:"action"`
	Path             string  `json:"path"`
	Content          *string `json:"content,omitempty"`
	ContentEncoding  *string `json:"contentEncoding,omitempty"`
	Destination      *string `json:"destination,omitempty"`
	WorkflowComplete bool    `json:"workflowComplete,omitempty"`
}

var SurfaceFileWriteInputSchema = surfaceTaggedUnion("action", map[string]string{
	"CREATE": string(directFileContentSchema()), "REPLACE": string(directFileContentSchema()),
	"DELETE": string(directFileMutationSchema(directFileMemberSchema+`,"path":{"type":"string","minLength":1,"maxLength":4096},"workflowComplete":{"type":"boolean"}`, `,"path"`)),
	"MOVE":   string(directFileMutationSchema(directFileMemberSchema+directFileDestinationMemberSchema+`,"path":{"type":"string","minLength":1,"maxLength":4096},"destination":{"type":"string","minLength":1,"maxLength":4096},"workflowComplete":{"type":"boolean"}`, `,"path","destination"`)),
})

func directFileMutationSchema(extra, required string) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + directFileScopeSchema + extra + directFileMutationMetaSchema + `},"required":["aiSessionId","featureAreaId","workflowId"` + required + `],"allOf":[` + directFileV2ExistingMutationRequiredSchema[1:] + `],"additionalProperties":false}`)
}

func SurfaceFileWriteCore(ctx context.Context, service BridgeService, in SurfaceFileWriteInput) (directfile.Result, error) {
	if strings.TrimSpace(in.AISessionID) == "" || strings.TrimSpace(in.FeatureAreaID) == "" || strings.TrimSpace(in.WorkflowID) == "" {
		return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "exact Session, FeatureArea and workflowId required")
	}
	p, ok := service.(DirectFileService)
	if !ok {
		return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File capability unavailable")
	}
	value := directfile.Input{Scope: in.Scope, Path: in.Path, WorkflowComplete: in.WorkflowComplete, RequestID: in.RequestID, RuntimeEpoch: in.RuntimeEpoch, GrantGeneration: in.GrantGeneration, ExpectedDigest: in.ExpectedDigest, RepositoryID: in.RepositoryID, MemberID: in.MemberID, MemberVersion: in.MemberVersion, DestinationRepositoryID: in.DestinationRepositoryID, DestinationMemberID: in.DestinationMemberID}
	if in.Action != "MOVE" && (in.DestinationRepositoryID != "" || in.DestinationMemberID != "") {
		return directfile.Result{}, directfile.Failure(directfile.InvalidTarget, "destination identity is valid only for MOVE")
	}
	if in.Action == "CREATE" || in.Action == "REPLACE" {
		if in.Content == nil || in.Destination != nil {
			return directfile.Result{}, directfile.Failure(directfile.InvalidTarget, "content required; destination forbidden")
		}
		value.Content = *in.Content
		if in.ContentEncoding != nil {
			value.ContentEncoding = *in.ContentEncoding
		}
		if in.Action == "CREATE" {
			return p.DirectFileFileCreate(ctx, value)
		}
		return p.DirectFileFileReplace(ctx, value)
	}
	if in.Content != nil || in.ContentEncoding != nil {
		return directfile.Result{}, directfile.Failure(directfile.InvalidTarget, "content forbidden for DELETE/MOVE")
	}
	switch in.Action {
	case "DELETE":
		if in.Destination != nil {
			return directfile.Result{}, directfile.Failure(directfile.InvalidTarget, "destination forbidden for DELETE")
		}
		return p.DirectFileFileDelete(ctx, value)
	case "MOVE":
		if in.Destination == nil || *in.Destination == "" {
			return directfile.Result{}, directfile.Failure(directfile.InvalidTarget, "MOVE requires destination")
		}
		value.Destination = *in.Destination
		return p.DirectFileFileMove(ctx, value)
	default:
		return directfile.Result{}, directfile.Failure(directfile.InvalidTarget, "unsupported file action")
	}
}

func reviewFileReadSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + directFileScopeSchema + directFileMemberSchema + directFileMutationMetaSchema + `,"reviewTarget":` + UpperReviewTargetSchema + `,"path":{"type":"string","minLength":1,"maxLength":4096},"workflowComplete":{"type":"boolean"}},"required":["path"],"oneOf":[{"required":["reviewTarget"],"not":{"anyOf":[{"required":["workflowId"]},{"required":["aiSessionId"]},{"required":["featureAreaId"]}]}},{"required":["aiSessionId","featureAreaId","workflowId"],"not":{"required":["reviewTarget"]}}],"allOf":[` + directFileV2MutationRequiredSchema[1:] + `],"additionalProperties":false}`)
}
