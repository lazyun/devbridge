package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/directfile"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fileImplementationResultService interface {
	GetFileImplementationResult(context.Context, directfile.ImplementationResultReadInput) (directfile.ImplementationResult, error)
	SubmitFileImplementationResult(context.Context, directfile.ImplementationResultInput) (directfile.ImplementationResult, error)
	AcceptFileImplementationResult(context.Context, directfile.ImplementationAcceptInput) (directfile.ImplementationResult, error)
}

func (s *StaticService) GetFileImplementationResult(ctx context.Context, in directfile.ImplementationResultReadInput) (directfile.ImplementationResult, error) {
	if s.control == nil {
		return directfile.ImplementationResult{}, errors.New("file result reader unavailable")
	}
	return s.control.GetFileImplementationResult(ctx, in)
}

func (s *StaticService) SubmitFileImplementationResult(ctx context.Context, in directfile.ImplementationResultInput) (directfile.ImplementationResult, error) {
	if s.control == nil {
		return directfile.ImplementationResult{}, errors.New("file result service unavailable")
	}
	return s.control.SubmitFileImplementationResult(ctx, in)
}
func (s *StaticService) AcceptFileImplementationResult(ctx context.Context, in directfile.ImplementationAcceptInput) (directfile.ImplementationResult, error) {
	if s.control == nil {
		return directfile.ImplementationResult{}, errors.New("file result acceptance unavailable")
	}
	return s.control.AcceptFileImplementationResult(ctx, in)
}
func fileResultSchema(accept bool) any {
	p := map[string]any{"aiSessionId": confirmationString(1, 128), "taskId": confirmationString(1, 128), "taskDefinitionRevisionId": confirmationString(1, 128), "workflowId": confirmationString(1, 128), "requestId": confirmationString(1, 128), "protocolVersion": map[string]any{"type": "integer", "enum": []int{1, 2}}, "runtimeEpoch": confirmationString(1, 128), "grantGeneration": map[string]any{"type": "integer", "minimum": 1}}
	r := []string{"aiSessionId", "taskId", "taskDefinitionRevisionId", "workflowId", "requestId"}
	if accept {
		p["resultId"] = confirmationString(1, 128)
		r = append(r, "resultId")
	} else {
		p["workerClaim"] = fileWorkerIdentitySchema()
		p["summary"] = confirmationString(1, 16384)
		p["verificationNotes"] = confirmationString(0, 16384)
		p["members"] = confirmationArray(confirmationObject(map[string]any{"repositoryId": confirmationString(1, 128), "memberId": confirmationString(1, 128), "memberVersion": map[string]any{"type": "integer", "minimum": 1}, "workspaceId": confirmationString(0, 128), "worktreeId": confirmationString(0, 128), "originCommit": confirmationString(0, 128), "startSnapshot": confirmationString(0, 128), "endSnapshot": confirmationString(0, 128), "changedPaths": confirmationArray(confirmationString(1, 4096), 0, 128), "state": confirmationString(0, 64)}, "repositoryId", "memberId", "memberVersion"), 1, 128)
		r = append(r, "summary", "verificationNotes")
	}
	schema := confirmationObject(p, r...)
	// Legacy result callers remain readable.  A v2 result must carry the
	// current authorization fence. Core derives the full member evidence;
	// optional input member statements never become filesystem authority.
	v2Required := []string{"runtimeEpoch", "grantGeneration"}
	if accept {
		v2Required = []string{"runtimeEpoch", "grantGeneration"}
	}
	schema["allOf"] = []any{map[string]any{"if": map[string]any{"properties": map[string]any{"protocolVersion": map[string]any{"const": 2}}, "required": []string{"protocolVersion"}}, "then": map[string]any{"required": v2Required}}}
	return schema
}
func registerFileImplementationResults(server *sdkmcp.Server, service BridgeService) {
	readSchema := confirmationObject(map[string]any{"aiSessionId": confirmationString(1, 128), "taskId": confirmationString(1, 128), "taskDefinitionRevisionId": confirmationString(1, 128), "workflowId": confirmationString(1, 128)}, "aiSessionId", "taskId", "taskDefinitionRevisionId", "workflowId")
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "file_implementation_result_get", Title: "Read exact file implementation result", Description: "Pure read of the exact Task/revision/workflow formal file result, including its separate acceptance receipt state. Use the workflow from the original confirmation; never substitute the latest Task. Historical results remain readable after grant closure or revision changes. Missing result is an accurate read error, not permission to submit again. accepted does not prove Task Git commit completion; read the original Task commit status separately. No workspace access, grant update, replay or dispatch.", InputSchema: readSchema, Annotations: toolAnnotations("Read file implementation result", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in directfile.ImplementationResultReadInput) (*sdkmcp.CallToolResult, directfile.ImplementationResult, error) {
		p, ok := service.(fileImplementationResultService)
		if !ok {
			return nil, directfile.ImplementationResult{}, errors.New("file result reader unavailable")
		}
		out, err := p.GetFileImplementationResult(ctx, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "file_implementation_result_submit", Title: "Submit file implementation result", Description: "Submit the actual result for the exact Task/revision/Direct File workflow. An enrolled file_worker_claim workflow requires its exact workerClaim; missing or foreign claims cannot finalize it. Core observes the full pinned member set and atomically records the formal result while closing write access. All RUN verification is executed by Codex in a separate read-only VERIFICATION Run using the backend/model explicitly confirmed at START_DIRECT_FILE. Submission freezes the file snapshots, closes write access, creates the exact verification-only Execution and starts its durable handoff. Notes never count as executed checks. READY or verificationStartError means dispatch needs attention; read the exact Execution/Run and use verification_continue without changing frozen configuration. SUCCEEDED describes transport completion; inspect the verification report for check results. Does not accept the Task. Explicit historical NONE waivers remain NOT_RUN. Reuse exact requestId/payload after response loss.", InputSchema: fileResultSchema(false), Annotations: stateChangingAnnotations("Submit file implementation result", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in directfile.ImplementationResultInput) (*sdkmcp.CallToolResult, directfile.ImplementationResult, error) {
		p, ok := service.(fileImplementationResultService)
		if !ok {
			return nil, directfile.ImplementationResult{}, errors.New("file result authority unavailable")
		}
		out, err := p.SubmitFileImplementationResult(ctx, in)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "file_implementation_accept", Title: "Accept exact file implementation result", Description: "Apply the user's separate explicit acceptance of this exact submitted file result, current immutable revision, closed workflow and unchanged snapshot. Closing access or submitting a result is not acceptance. A RUN verificationSpec with NOT_RUN remains blocked; no automatic waiver or unconfirmed Codex verification. Successful acceptance uses the original Task commit receipt path; commitError is partial success and task_commit_retry retains its original contract.", InputSchema: fileResultSchema(true), Annotations: stateChangingAnnotations("Accept file implementation result", true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in directfile.ImplementationAcceptInput) (*sdkmcp.CallToolResult, directfile.ImplementationResult, error) {
		p, ok := service.(fileImplementationResultService)
		if !ok {
			return nil, directfile.ImplementationResult{}, errors.New("file result acceptance unavailable")
		}
		out, err := p.AcceptFileImplementationResult(ctx, in)
		return nil, out, publicToolError(err)
	})
}
