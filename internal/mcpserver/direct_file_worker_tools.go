package mcpserver

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/directfile"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type directFileWorkerService interface {
	ClaimFileImplementationWorker(context.Context, directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error)
	GetFileImplementationWorker(context.Context, directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error)
}

func fileWorkerIdentitySchema() map[string]any {
	return confirmationObject(map[string]any{"requestId": confirmationString(1, 128), "workerId": confirmationString(1, 128)}, "requestId", "workerId")
}

func fileWorkerClaimSchema() map[string]any {
	properties := map[string]any{}
	ids := []string{"aiSessionId", "featureAreaId", "taskId", "taskDefinitionRevisionId", "decisionId", "workflowId", "runtimeEpoch", "requestId", "workerId"}
	for _, id := range ids {
		properties[id] = confirmationString(1, 128)
	}
	properties["protocolVersion"] = map[string]any{"type": "integer", "const": 2}
	properties["grantGeneration"] = map[string]any{"type": "integer", "minimum": 1}
	return confirmationObject(properties, append(ids, "protocolVersion", "grantGeneration")...)
}

func registerDirectFileWorkerTools(server *sdkmcp.Server, service BridgeService) {
	register := func(name, description string, readOnly bool, call func(directFileWorkerService, context.Context, directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error)) {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: name, Description: description, InputSchema: fileWorkerClaimSchema(), Annotations: toolAnnotations("Direct File pull delivery", readOnly, readOnly)}, func(ctx context.Context, request *sdkmcp.CallToolRequest, in directfile.WorkerClaimInput) (*sdkmcp.CallToolResult, directfile.WorkerClaimResult, error) {
			ctx, err := withSessionRequest(ctx, request)
			if err != nil {
				return nil, directfile.WorkerClaimResult{}, publicToolError(err)
			}
			worker, ok := service.(directFileWorkerService)
			if !ok {
				return nil, directfile.WorkerClaimResult{}, errors.New("Direct File pull worker unavailable")
			}
			out, err := call(worker, ctx, in)
			return nil, out, publicToolError(err)
		})
	}
	register("file_worker_claim", "Claim one pull delivery for an ALREADY approved and started v2 DIRECT_FILE workflow, before any file mutation. Copy the exact Task/revision/decision/workflow/runtime epoch/generation from its active authority. This does not approve work, start a Coding Agent Run, launch a cloud task or grant command execution. workerId is only a correlation label under the original authenticated Session. Core persists dispatch intent before returning unchanged executionPayload.instruction separately from the complete frozen definition, file authority and verification requirements. Only the first returned payload may be acted on; lost or ambiguous delivery requires file_worker_get with the exact original input, never a new request or worker. Supply workerClaim={requestId,workerId} on every file_write and file_implementation_result_submit for an enrolled workflow. Use existing bounded file operations; do not infer model consumption, host identity or exactly-once execution. Formal file results do not waive separately required verification or accept the Task.", false, func(s directFileWorkerService, ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
		return s.ClaimFileImplementationWorker(ctx, in)
	})
	register("file_worker_get", "Read the exact historical Direct File pull receipt and known formal result. Never returns executable input, refreshes authority, changes files, retries dispatch or resumes work. OUTCOME_UNKNOWN is not permission to execute from another readback or choose a different worker. Same-workflow recovery of enrolled workers is unsupported; existing explicit cancellation, when no unresolved file-effect receipt blocks it, revokes Core file/report authority without rolling back effects or proving external host termination. Any successor requires separate existing workflow authorization. Required command verification and user acceptance remain separate.", true, func(s directFileWorkerService, ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
		return s.GetFileImplementationWorker(ctx, in)
	})
}

func (s *StaticService) ClaimFileImplementationWorker(ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
	if s == nil || s.control == nil {
		return directfile.WorkerClaimResult{}, errors.New("Direct File pull worker unavailable")
	}
	return s.control.ClaimFileImplementationWorker(ctx, in)
}

func (s *StaticService) GetFileImplementationWorker(ctx context.Context, in directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error) {
	if s == nil || s.control == nil {
		return directfile.WorkerClaimResult{}, errors.New("Direct File pull worker unavailable")
	}
	return s.control.GetFileImplementationWorker(ctx, in)
}
