package codex

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/agentruntime"
)

func (c *Client) runtimeForThread(threadID string) *agentruntime.Session {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	return c.runtimeSessions[threadID]
}
func (c *Client) observeRuntime(event Event) {
	if c.agentRuntime == nil {
		return
	}
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(event.Params, &p)
	if event.Method == "thread/name/updated" && p.ThreadID != "" {
		var name struct {
			ThreadName string `json:"threadName"`
		}
		if json.Unmarshal(event.Params, &name) == nil {
			c.runtimeMu.Lock()
			if c.threadNames == nil {
				c.threadNames = make(map[string]string)
			}
			c.threadNames[p.ThreadID] = name.ThreadName
			c.runtimeMu.Unlock()
		}
	}
	// Uncorrelated connection-wide notices cannot be attributed to an arbitrary
	// active Run. They remain available through the transport diagnostics path.
	if p.ThreadID == "" {
		return
	}
	c.runtimeMu.Lock()
	var sessions []*agentruntime.Session
	if p.ThreadID != "" {
		if s := c.runtimeSessions[p.ThreadID]; s != nil {
			sessions = append(sessions, s)
		}
	} else {
		for _, s := range c.runtimeSessions {
			sessions = append(sessions, s)
		}
	}
	c.runtimeMu.Unlock()
	for _, s := range sessions {
		if err := s.Observe(agentruntime.Event{Method: event.Method, Params: event.Params, RequestID: event.RequestID}); err != nil {
			c.eventDiagnostic(event, err)
			// Persistence failure must not leave an invisible approval wait. The
			// existing transport-loss/finalization path owns the resulting UNKNOWN.
			go c.rpc.fail(err)
		}
	}
}
func (c *Client) processRuntimeRequest(ctx context.Context, request ServerRequest, next serverRequestProcessor) (any, error) {
	if !agentruntime.Supports(request.Method) {
		return next(ctx, request)
	}
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(request.Params, &p)
	s := c.runtimeForThread(p.ThreadID)
	if s == nil {
		if c.detached {
			// A subscribed independent Thread may replay an old request. No
			// owner means no authority to answer, including an RPC error.
			go c.rpc.Detach()
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, &RPCError{Code: -32601, Message: "No exact runtime owner for server request"}
	}

	response, err := s.Await(ctx, request.ID, agentruntime.Event{Method: request.Method, Params: request.Params})
	if err != nil {
		code := int64(-32602)
		if errors.Is(err, agentruntime.ErrStale) {
			code = -32601
		}
		return nil, &RPCError{Code: code, Message: err.Error()}
	}
	delivered := make(chan error, 1)
	go func() {
		select {
		case err := <-delivered:
			s.Delivered(request.ID, err)
		case <-ctx.Done():
			s.Delivered(request.ID, ctx.Err())
		}
	}()
	return serverRequestResolution{result: response, delivered: delivered, beforeWrite: func() error {
		err := s.BeforeDispatch(request.ID)
		if err != nil {
			if c.detached {
				// No durable dispatch intent means no response effect is allowed,
				// including a synthesized JSON-RPC error during shutdown. Close
				// this connection before aborting its workers; the independently
				// owned server retains the exact pending request for recovery.
				go c.rpc.Detach()
			} else if !errors.Is(err, agentruntime.ErrStale) {
				go c.rpc.fail(err)
			}
		}
		return err
	}}, nil
}

func (c *Client) processTaskSandboxRequest(ctx context.Context, request ServerRequest, next serverRequestProcessor) (any, error) {
	if request.Method != "item/commandExecution/requestApproval" && request.Method != "item/fileChange/requestApproval" && request.Method != "item/permissions/requestApproval" {
		return next(ctx, request)
	}
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(request.Params, &p) != nil {
		return next(ctx, request)
	}
	c.runtimeMu.Lock()
	binding := c.threadBindings[p.ThreadID]
	c.runtimeMu.Unlock()
	policy := binding.TaskPermissions
	if c.diagnosticTaskPolicy != nil {
		policy = c.diagnosticTaskPolicy
	}
	if policy == nil {
		return next(ctx, request)
	}
	s := c.runtimeForThread(p.ThreadID)
	if s == nil && c.detached {
		// Observing an existing process is not authority to answer replayed source requests.
		go c.rpc.Detach()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s != nil {
		runID := policy.RunID
		if c.diagnosticTaskPolicy != nil {
			runID = s.RunID()
		}
		if err := s.RejectSandboxApproval(ctx, runID, agentruntime.Event{Method: request.Method, Params: request.Params, RequestID: request.ID}); err != nil {
			if !errors.Is(err, agentruntime.ErrStale) {
				go c.rpc.fail(err)
			}
			return nil, &RPCError{Code: -32601, Message: err.Error()}
		}
	}
	return nil, &RPCError{Code: -32602, Message: "TASK_SANDBOX_PERMISSION_EXCEEDED: use the approved sandbox and scratch directory; report this check BLOCKED if it cannot run without broader access, then continue independent checks"}
}
