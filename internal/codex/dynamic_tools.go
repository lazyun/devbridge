package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

const dynamicToolFailureMessage = "dynamic tool call was not accepted"

// DynamicToolSpec is the function-only dynamic-tool declaration sent at thread
// start. Type must be "function" and InputSchema must contain valid JSON.
type DynamicToolSpec = wire.DynamicToolSpec

// DynamicToolCallParams contains the App Server-provided call correlation and
// unmodified JSON arguments for a registered dynamic tool.
type DynamicToolCallParams = wire.DynamicToolCallParams

// DynamicToolCallResponse is the inputText-only response to a dynamic tool
// invocation.
type DynamicToolCallResponse = wire.DynamicToolCallResponse

type DynamicToolCallOutputContentItem = wire.DynamicToolCallOutputContentItem

// DynamicToolHandler handles calls only after the client has verified that the
// thread and tool were registered by a successful StartThread call.
type DynamicToolHandler func(context.Context, DynamicToolCallParams) (DynamicToolCallResponse, error)

type dynamicToolBroker struct {
	handler DynamicToolHandler

	mu      sync.RWMutex
	threads map[string]map[string]struct{}
}

func newDynamicToolBroker(handler DynamicToolHandler) *dynamicToolBroker {
	return &dynamicToolBroker{handler: handler, threads: make(map[string]map[string]struct{})}
}

func validateDynamicToolSpecs(specs []DynamicToolSpec) error {
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.Type != "function" {
			return errors.New("dynamic tool type must be function")
		}
		if spec.Name == "" {
			return errors.New("dynamic tool name is required")
		}
		if !json.Valid(spec.InputSchema) {
			return fmt.Errorf("dynamic tool %q has an invalid input schema", spec.Name)
		}
		if _, duplicate := seen[spec.Name]; duplicate {
			return fmt.Errorf("dynamic tool %q is duplicated", spec.Name)
		}
		seen[spec.Name] = struct{}{}
	}
	return nil
}

func (b *dynamicToolBroker) registerThread(threadID string, specs []DynamicToolSpec) {
	if b == nil || len(specs) == 0 {
		return
	}
	tools := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		tools[spec.Name] = struct{}{}
	}
	b.mu.Lock()
	b.threads[threadID] = tools
	b.mu.Unlock()
}

func (b *dynamicToolBroker) process(ctx context.Context, request ServerRequest, next serverRequestProcessor) (any, error) {
	if request.Method != "item/tool/call" {
		return next(ctx, request)
	}

	var params DynamicToolCallParams
	if err := json.Unmarshal(request.Params, &params); err != nil || !validDynamicToolCall(params) || !b.registered(params) {
		return rejectedDynamicToolCall(), nil
	}
	response, err := b.handler(ctx, params)
	if err != nil || !validDynamicToolResponse(response) {
		return rejectedDynamicToolCall(), nil
	}
	return response, nil
}

func validDynamicToolCall(params DynamicToolCallParams) bool {
	return params.CallID != "" && params.ThreadID != "" && params.TurnID != "" && params.Tool != "" && len(params.Arguments) != 0
}

func (b *dynamicToolBroker) registered(params DynamicToolCallParams) bool {
	if b == nil || b.handler == nil || params.Namespace != nil {
		return false
	}
	b.mu.RLock()
	_, registered := b.threads[params.ThreadID][params.Tool]
	b.mu.RUnlock()
	return registered
}

func validDynamicToolResponse(response DynamicToolCallResponse) bool {
	for _, item := range response.ContentItems {
		if item.Type != "inputText" {
			return false
		}
	}
	return true
}

func rejectedDynamicToolCall() DynamicToolCallResponse {
	return DynamicToolCallResponse{
		Success: false,
		ContentItems: []DynamicToolCallOutputContentItem{{
			Type: "inputText", Text: dynamicToolFailureMessage,
		}},
	}
}
