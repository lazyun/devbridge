package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lazyun/devbridge/internal/observability"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var fallbackRequestSequence atomic.Uint64

var stableErrorCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)

// toolLoggingMiddleware observes the model-facing boundary without retaining
// arbitrary arguments or result bodies. The request context carries the same
// correlation through the domain and runtime layers.
func toolLoggingMiddleware() sdkmcp.Middleware {
	return func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
		return func(ctx context.Context, method string, request sdkmcp.Request) (sdkmcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, request)
			}
			params, ok := request.GetParams().(*sdkmcp.CallToolParamsRaw)
			if !ok || params == nil {
				return next(ctx, method, request)
			}

			started := time.Now()
			requestID := ""
			if extra := request.GetExtra(); extra != nil && extra.Header != nil {
				requestID = extra.Header.Get("X-Request-ID")
				if !observability.ValidRequestID(requestID) {
					requestID = ""
				}
			}
			if requestID == "" {
				requestID = newRequestID()
			}
			correlation, handles := safeRequestMetadata(params.Arguments)
			correlation.RequestID = requestID
			ctx = observability.WithContext(ctx, correlation)
			observability.Info(ctx, "mcp", "mcp.tool.requested", "MCP tool call received", map[string]any{
				"tool": params.Name, "handles": handles,
			})

			result, callErr := next(ctx, method, request)
			terminalErr := callErr
			data := map[string]any{
				"tool": params.Name, "durationMillis": time.Since(started).Milliseconds(),
			}
			if callResult, resultOK := result.(*sdkmcp.CallToolResult); resultOK && callResult != nil {
				data["handles"] = safeResultHandles(callResult.StructuredContent)
				if callResult.IsError && terminalErr == nil {
					terminalErr = callResult.GetError()
					if terminalErr == nil {
						terminalErr = errors.New("MCP tool returned an error result")
					}
				}
			}
			if terminalErr != nil {
				data["errorCode"] = errorCode(terminalErr)
				observability.Error(ctx, "mcp", "mcp.tool.failed", "MCP tool call failed", data)
			} else {
				observability.Info(ctx, "mcp", "mcp.tool.succeeded", "MCP tool call succeeded", data)
			}
			return result, callErr
		}
	}
}

func newRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "req_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("req_fallback_%d", fallbackRequestSequence.Add(1))
}

func safeRequestMetadata(raw json.RawMessage) (observability.Correlation, map[string]string) {
	var values map[string]any
	_ = json.Unmarshal(raw, &values)
	correlation := observability.Correlation{
		ProjectID: stringValue(values, "projectId"), AISessionID: stringValue(values, "aiSessionId"),
		FeatureAreaID: stringValue(values, "featureAreaId"), ReviewID: stringValue(values, "reviewId"), TaskID: stringValue(values, "taskId"),
		ExecutionID: firstString(values, "executionId", "executionProposalId"),
		RunID:       stringValue(values, "runId"), ApprovalID: stringValue(values, "approvalId"),
	}
	handles := make(map[string]string)
	for _, key := range []string{"projectId", "aiSessionId", "featureAreaId", "reviewId", "taskId", "taskPlanId", "executionId", "executionProposalId", "runId", "approvalId", "probeId", "scopeExpansionId", "repositoryId", "moduleId"} {
		if value := stringValue(values, key); value != "" {
			handles[key] = observability.Bound(value, 256)
		}
	}
	return correlation, handles
}

func safeResultHandles(value any) map[string]string {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 1<<20 {
		return nil
	}
	var root any
	if json.Unmarshal(encoded, &root) != nil {
		return nil
	}
	result := make(map[string]string)
	collectResultHandles(root, result, 0)
	return result
}

func collectResultHandles(value any, result map[string]string, depth int) {
	if depth > 5 || len(result) >= 32 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isSafeHandleKey(key) {
				if text, ok := child.(string); ok && text != "" {
					result[key] = observability.Bound(text, 256)
				}
			}
			collectResultHandles(child, result, depth+1)
		}
	case []any:
		for index, child := range typed {
			if index == 32 {
				break
			}
			collectResultHandles(child, result, depth+1)
		}
	}
}

func isSafeHandleKey(key string) bool {
	switch key {
	case "projectId", "aiSessionId", "featureAreaId", "reviewId", "taskId", "taskPlanId", "executionId", "executionProposalId", "runId", "approvalId", "probeId", "scopeExpansionId", "repositoryId", "moduleId", "threadId", "turnId":
		return true
	default:
		return false
	}
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values, key); value != "" {
			return value
		}
	}
	return ""
}

func errorCode(err error) string {
	if err == nil {
		return ""
	}
	prefix, _, _ := strings.Cut(err.Error(), ":")
	prefix = strings.TrimSpace(prefix)
	if stableErrorCode.MatchString(prefix) {
		return prefix
	}
	return "MCP_TOOL_ERROR"
}
