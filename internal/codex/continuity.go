package codex

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex/wire"
)

func attachAppServer(ctx context.Context, config Config) (*Client, error) {
	if config.DiagnosticTaskReadOnly && config.TaskRuntimeBinding == nil {
		return nil, errors.New("TASK_DIAGNOSTIC_SOURCE_BINDING_MISSING")
	}
	if b := config.TaskRuntimeBinding; b != nil {
		if err := validateRuntimeBinding(*b); err != nil {
			return nil, err
		}
		if b.TaskPermissions == nil || config.AppServerBinding.TaskPermissionHash != b.TaskPermissions.ContentHash {
			return nil, errors.New("TASK_PERMISSION_PROCESS_BINDING_MISMATCH")
		}
	} else if config.AppServerBinding.TaskPermissionHash != "" {
		return nil, errors.New("TASK_PERMISSION_PROCESS_BINDING_MISSING")
	}
	if err := appserver.Verify(ctx, *config.AppServerBinding); err != nil {
		return nil, err
	}
	socket, err := dialUnixWebSocket(ctx, config.AppServerBinding.SocketPath)
	if err != nil {
		return nil, err
	}
	c := &Client{executorBoundary: true, nativeVersion: config.AppServerBinding.CodexVersion, detached: true, usageStore: config.UsageStore, agentRuntime: config.AgentRuntime, runtimeSessions: make(map[string]*agentruntime.Session), threadNames: map[string]string{}, processContext: context.WithoutCancel(ctx), turns: make(map[turnKey]*turnState), starting: make(map[string]*turnStartState), retired: make(map[turnKey]uint64), threadUsage: make(map[string]threadUsageState)}
	processor := serverRequestProcessor(builtInServerRequestPolicy)
	if config.TaskRuntimeBinding != nil {
		c.taskPermissionHash = config.TaskRuntimeBinding.TaskPermissions.ContentHash
		if config.DiagnosticTaskReadOnly {
			c.diagnosticTaskPolicy = config.TaskRuntimeBinding.TaskPermissions.Clone()
		}
	}
	if config.ApprovalMode == ApprovalModeInteractive {
		c.approvals = newApprovalBroker(config.AuthorizationCeiling)
		processor = c.approvals.process
	}
	if config.DynamicToolHandler != nil {
		c.dynamicTools = newDynamicToolBroker(config.DynamicToolHandler)
		next := processor
		processor = func(ctx context.Context, r ServerRequest) (any, error) { return c.dynamicTools.process(ctx, r, next) }
	}
	if config.AgentRuntime != nil {
		next := processor
		processor = func(ctx context.Context, r ServerRequest) (any, error) { return c.processRuntimeRequest(ctx, r, next) }
	}
	nextTaskProcessor := processor
	processor = func(ctx context.Context, r ServerRequest) (any, error) {
		return c.processTaskSandboxRequest(ctx, r, nextTaskProcessor)
	}
	c.rpc = newRPCTransportWithProcessor(socket, socket, processor, c.handleEvent, func(r ServerRequest) { c.observeRuntime(Event{Method: r.Method, Params: r.Params, RequestID: r.ID}) })
	var initialized wire.InitializeResponse
	params := initializeParams(config)
	params.Capabilities = &wire.InitializeCapabilities{ExperimentalAPI: true}
	if err = c.rpc.Call(ctx, "initialize", params, &initialized); err == nil {
		err = c.rpc.Notify("initialized", struct{}{})
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// TurnObservation contains only evidence read from the still-live original
// server. Missing/not-loaded threads are never resumed as a recovery attempt.
type TurnObservation struct {
	ThreadID     string
	TurnID       string
	Loaded       bool
	ThreadStatus string
	Turn         wire.Turn
	CWD          string
}

func (c *Client) ObserveLoadedTurn(ctx context.Context, threadID, turnID string) (TurnObservation, error) {
	out := TurnObservation{ThreadID: threadID, TurnID: turnID}
	if !c.detached || threadID == "" || turnID == "" {
		return out, errors.New("exact detached thread and turn required")
	}
	cursor := ""
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		if pages >= 100 {
			return out, errors.New("loaded thread pagination limit exceeded")
		}
		var loaded struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if e := c.rpc.Call(ctx, "thread/loaded/list", params, &loaded); e != nil {
			return out, e
		}
		for _, id := range loaded.Data {
			if id == threadID {
				out.Loaded = true
			}
		}
		if out.Loaded || loaded.NextCursor == nil {
			break
		}
		if *loaded.NextCursor == "" || len(*loaded.NextCursor) > 4096 || seen[*loaded.NextCursor] {
			return out, errors.New("loaded thread cursor did not advance")
		}
		cursor = *loaded.NextCursor
		seen[cursor] = true
	}
	if !out.Loaded {
		out.ThreadStatus = "notLoaded"
		return out, errors.New("original thread is not loaded")
	}
	var read struct {
		Thread struct {
			ID     string `json:"id"`
			CWD    string `json:"cwd"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
			Turns []wire.Turn `json:"turns"`
		} `json:"thread"`
	}
	if e := c.rpc.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": c.nativeVersion == "0.149.0"}, &read); e != nil {
		return out, e
	}
	if read.Thread.ID != threadID {
		return out, errors.New("thread/read identity mismatch")
	}
	out.CWD = read.Thread.CWD
	out.ThreadStatus = read.Thread.Status.Type
	// Retain the explicitly validated legacy backend; modern backends never
	// fall back to deprecated full-history hydration after a pagination error.
	if c.nativeVersion == "0.149.0" {
		for _, turn := range read.Thread.Turns {
			if turn.ID == turnID {
				out.Turn = turn
				return out, nil
			}
		}
		return out, errors.New("original turn is absent")
	}
	// Paginated threads no longer hydrate all turns through thread/read.
	// Request full items explicitly: terminal evidence must never be inferred
	// from the summary view (which can omit commands and the final message).
	cursor = ""
	seen = map[string]bool{}
	for pages := 0; pages < 100; pages++ {
		var page wire.ThreadTurnsListResponse
		params := wire.ThreadTurnsListParams{ThreadID: threadID, Limit: 50, SortDirection: "desc", ItemsView: "full", Cursor: cursor}
		if err := c.rpc.Call(ctx, "thread/turns/list", params, &page); err != nil {
			return out, err
		}
		if len(page.Data) > 50 {
			return out, errors.New("oversized turn history page")
		}
		for _, turn := range page.Data {
			if turn.ID == turnID {
				out.Turn = turn
				return out, nil
			}
		}
		if page.NextCursor == nil {
			return out, errors.New("original turn is absent")
		}
		cursor = *page.NextCursor
		if cursor == "" || len(cursor) > 4096 || seen[cursor] {
			return out, errors.New("invalid turn history pagination")
		}
		seen[cursor] = true
	}
	return out, errors.New("turn history pagination limit exceeded")
}

type TurnRejoinOptions struct {
	TurnOptions
	TurnID         string
	RuntimeSession *agentruntime.Session
	DynamicTools   []DynamicToolSpec
}

// RejoinTurn observes/subscribes to an existing active turn. It never calls
// turn/start and installs the restored runtime owner before pending replay.
func (c *Client) RejoinTurn(ctx context.Context, o TurnRejoinOptions) (TurnResult, error) {
	result := TurnResult{ThreadID: o.ThreadID, TurnID: o.TurnID}
	observed, e := c.ObserveLoadedTurn(ctx, o.ThreadID, o.TurnID)
	if e != nil {
		return result, e
	}
	if len(o.RuntimeBinding.RuntimeWorkspaceRoots) > 0 {
		binding, err := bindingForThread(observed.CWD, o.RuntimeBinding)
		if err != nil {
			return result, err
		}
		if err = c.requireSameThreadBinding(o.ThreadID, binding); err != nil {
			return result, err
		}
		c.runtimeMu.Lock()
		if c.threadBindings == nil {
			c.threadBindings = make(map[string]RuntimeBinding)
		}
		c.threadBindings[o.ThreadID] = cloneRuntimeBinding(binding)
		c.runtimeMu.Unlock()
	}
	if len(o.DynamicTools) > 0 && (c.dynamicTools == nil || c.dynamicTools.handler == nil) {
		return result, errors.New("dynamic tools require local handler")
	}
	if err := validateDynamicToolSpecs(o.DynamicTools); err != nil {
		return result, err
	}
	c.dynamicTools.registerThread(o.ThreadID, o.DynamicTools)
	if observed.Turn.Status != "inProgress" {
		if o.RuntimeSession != nil {
			if err := observeRecoveredTerminal(o.RuntimeSession, o.ThreadID, observed.Turn); err != nil {
				o.RuntimeSession.Close()
				return result, err
			}
			c.runtimeMu.Lock()
			c.runtimeSessions[o.ThreadID] = o.RuntimeSession
			c.runtimeMu.Unlock()
			c.finishRuntimeTurn(ctx, o.RunID, o.ThreadID, o.RuntimeSession)
		}
		return resultFromObservedTurn(o.ThreadID, observed.Turn)
	}
	if observed.ThreadStatus != "active" {
		return result, errors.New("original turn is not active")
	}
	if c.agentRuntime != nil && o.RuntimeSession == nil {
		return result, errors.New("restored runtime session required")
	}
	key := turnKey{threadID: o.ThreadID, turnID: o.TurnID}
	state := &turnState{waiter: make(chan struct{}), commands: make(map[string]CommandExecution)}
	waiter := state.waiter
	c.mu.Lock()
	if c.turns[key] != nil {
		c.mu.Unlock()
		return result, errors.New("turn already attached")
	}
	c.turnSerial++
	state.serial = c.turnSerial
	c.turns[key] = state
	c.mu.Unlock()
	if o.RuntimeSession != nil {
		o.RuntimeSession.SetSteeringHandler(c.steerCurrentTurn)
		c.runtimeMu.Lock()
		if c.runtimeSessions[o.ThreadID] != nil {
			c.runtimeMu.Unlock()
			return result, errors.New("runtime already attached")
		}
		c.runtimeSessions[o.ThreadID] = o.RuntimeSession
		c.runtimeMu.Unlock()
		defer func() {
			c.mu.Lock()
			completed := state.completion != nil
			c.mu.Unlock()
			if !completed {
				_ = c.rpc.Detach()
			}
			c.finishRuntimeTurn(ctx, o.RunID, o.ThreadID, o.RuntimeSession)
		}()
	}
	// No authority/config overrides are sent while subscribing to a live turn.
	var resumed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if e = c.rpc.Call(ctx, "thread/resume", map[string]any{"threadId": o.ThreadID, "excludeTurns": true}, &resumed); e != nil {
		return result, e
	}
	if resumed.Thread.ID != o.ThreadID {
		return result, errors.New("resume returned different thread")
	}
	select {
	case <-waiter:
	case <-ctx.Done():
		return result, ctx.Err()
	case <-c.rpc.Done():
		return result, c.rpc.Err()
	}
	c.mu.Lock()
	completion := state.completion
	c.retireTurnLocked(key, state)
	c.mu.Unlock()
	if completion == nil {
		return result, errors.New("missing original turn completion")
	}
	result, err := resultFromObservedTurn(o.ThreadID, completion.Turn)
	if commands := commandExecutions(state); len(commands) > 0 {
		result.Commands = commands
	}
	if result.FinalMessage == "" {
		if state.finalMessage.seen {
			result.FinalMessage = state.finalMessage.text
		} else if state.fallback.seen {
			result.FinalMessage = state.fallback.text
		}
	}
	return result, err
}
func resultFromObservedTurn(thread string, turn wire.Turn) (TurnResult, error) {
	r := TurnResult{ThreadID: thread, TurnID: turn.ID, FinalMessage: finalMessageFromItems(turn.Items).text}
	state := &turnState{commands: make(map[string]CommandExecution)}
	for _, item := range turn.Items {
		observeCommand(state, item, true, time.Now().UTC())
	}
	r.Commands = commandExecutions(state)
	if turn.Status != "completed" {
		message := ""
		if turn.Error != nil {
			message = turn.Error.Message
		}
		return r, &TurnError{ThreadID: thread, TurnID: turn.ID, Status: turn.Status, Message: message, Failure: turn.Error}
	}
	return r, nil
}

// AppServerQuiescent only observes loaded threads. Any uncertain observation
// fails closed; lifecycle owners may stop the instance only after canonical
// operation finalization and this independent server check.
func (c *Client) AppServerQuiescent(ctx context.Context) (bool, error) {
	if !c.detached {
		return false, errors.New("independent App Server required")
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		params := map[string]any{"limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := c.rpc.Call(ctx, "thread/loaded/list", params, &page); err != nil {
			return false, err
		}
		for _, threadID := range page.Data {
			var read struct {
				Thread struct {
					ID     string `json:"id"`
					Status struct {
						Type string `json:"type"`
					} `json:"status"`
				} `json:"thread"`
			}
			if err := c.rpc.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": false}, &read); err != nil {
				return false, err
			}
			if read.Thread.ID != threadID || read.Thread.Status.Type != "idle" {
				return false, nil
			}
		}
		if page.NextCursor == nil {
			return true, nil
		}
		cursor = *page.NextCursor
		if cursor == "" || seen[cursor] {
			return false, errors.New("invalid loaded thread pagination")
		}
		seen[cursor] = true
	}
}

// A terminal full turn page is authoritative protocol evidence, including when the
// completion happened between the initial bootstrap read and subscription.
func observeRecoveredTerminal(session *agentruntime.Session, threadID string, turn wire.Turn) error {
	for _, item := range turn.Items {
		params, err := json.Marshal(wire.ItemCompletedParams{ThreadID: threadID, TurnID: turn.ID, Item: item})
		if err != nil {
			return err
		}
		if err = session.Observe(agentruntime.Event{Method: "item/completed", Params: params}); err != nil {
			return err
		}
	}
	params, err := json.Marshal(wire.TurnCompletedParams{ThreadID: threadID, Turn: turn})
	if err != nil {
		return err
	}
	return session.Observe(agentruntime.Event{Method: "turn/completed", Params: params})
}
