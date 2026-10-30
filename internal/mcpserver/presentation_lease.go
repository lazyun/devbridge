package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"sync"
)

type presentationLease struct {
	mu          sync.Mutex
	fingerprint string
	identity    map[string]any
}
type presentationLeases struct {
	mu       sync.Mutex
	requests map[string]*presentationLease
}

func newPresentationLeases() *presentationLeases {
	return &presentationLeases{requests: make(map[string]*presentationLease)}
}
func presentationHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

type presentationReplayKey struct{}

func presentationReplayExact(ctx context.Context) bool {
	v, _ := ctx.Value(presentationReplayKey{}).(bool)
	return v
}

// Store presentation intent identity, never cached business authority. Retries
// re-read the same resolved scope, so an old pending approval cannot be replayed
// as actionable. This is process-local and makes no host mounting guarantee.
func (l *presentationLeases) emit(ctx context.Context, next sdkmcp.MethodHandler, method string, request sdkmcp.Request, params *sdkmcp.CallToolParamsRaw) (sdkmcp.Result, error) {
	var input map[string]any
	if err := json.Unmarshal(params.Arguments, &input); err != nil {
		return nil, err
	}
	requestID := stringValue(input, "presentationRequestId")
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("presentationRequestId is required")
	}
	if (input["display"] == nil || input["display"] == "") && params.Name != "project_selection_show" {
		input["display"] = "COLLAPSED"
	}
	normalized, _ := json.Marshal(input)
	fingerprint := params.Name + ":" + string(normalized)
	scope := stringValue(input, "aiSessionId")
	if params.Name == "project_selection_show" {
		scope = "project-selection"
	}
	key := scope + ":" + requestID
	l.mu.Lock()
	intent, exists := l.requests[key]
	if exists && intent.fingerprint != fingerprint {
		l.mu.Unlock()
		return nil, errors.New("PRESENTATION_REQUEST_CONFLICT: presentationRequestId was used with different arguments")
	}
	if !exists {
		if len(l.requests) >= 4096 {
			l.mu.Unlock()
			return nil, errors.New("PRESENTATION_CAPACITY: process-local presentation intent capacity reached")
		}
		intent = &presentationLease{fingerprint: fingerprint}
		l.requests[key] = intent
	}
	l.mu.Unlock()
	// Serialize only this intent; unrelated Sessions do not wait on its reads.
	intent.mu.Lock()
	defer intent.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if intent.identity != nil && params.Name != "project_selection_show" {
		for name, value := range intent.identity {
			input[name] = value
		}
		ctx = context.WithValue(ctx, presentationReplayKey{}, true)
		if original, ok := request.(*sdkmcp.CallToolRequest); ok {
			copyRequest, copyParams := *original, *params
			copyParams.Arguments, _ = json.Marshal(input)
			copyRequest.Params = &copyParams
			request = &copyRequest
		}
	}
	result, err := next(ctx, method, request)
	out, ok := result.(*sdkmcp.CallToolResult)
	if err != nil || !ok || out == nil || out.IsError {
		return result, err
	}
	if out.Meta == nil {
		out.Meta = sdkmcp.Meta{}
	}
	out.Meta["emission"] = string(presentationPolicy(params.Name))
	out.Meta["presentationOperation"] = "MOUNT_REQUESTED"
	out.Meta["hostVisibility"] = "UNCONFIRMED"
	// UI association belongs to the registered tool descriptor. Result _meta is
	// hydration data, not a second mutable template registration on every replay.
	if intent.identity == nil {
		intent.identity = map[string]any{}
		for _, name := range []string{"projectId", "featureAreaId", "taskId", "decisionId", "target", "actionTarget"} {
			if value := out.Meta[name]; value != nil {
				if text, ok := value.(string); ok && text == "" {
					continue
				}
				intent.identity[name] = value
			}
		}
	}
	return out, nil
}
