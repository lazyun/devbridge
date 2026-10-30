package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex/wire"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/structuredschema"
)

const (
	maxRememberedTurns          = 256
	maxProvisionalTurnsPerStart = 64
	maxRetiredTurns             = 256
)

type Config struct {
	TaskRuntimeBinding *RuntimeBinding
	// DiagnosticTaskReadOnly requires the exact source process binding and selects
	// its separate read-only, network-disabled profile for diagnostic child Turns.
	DiagnosticTaskReadOnly bool
	AppServerBinding       *appserver.Binding
	AgentRuntime           *agentruntime.Service
	UsageStore             codexusage.Store
	CodexPath              string
	VerifiedExecutable     codexlaunch.VerifiedExecutable
	ApprovalMode           ApprovalMode
	AuthorizationCeiling   AuthorizationCeiling
	DynamicToolHandler     DynamicToolHandler
}

// AuthorizationCeiling is the maximum runtime authority an operation can
// exercise. Runtime approvals can authorize an action within this boundary;
// they can never raise the boundary itself.
type AuthorizationCeiling string

const (
	AuthorizationReadOnly       AuthorizationCeiling = "READ_ONLY"
	AuthorizationWorkspaceWrite AuthorizationCeiling = "WORKSPACE_WRITE"
)

type Client struct {
	taskPermissionHash   string
	diagnosticTaskPolicy *TaskPermissions
	executorBoundary     bool
	executorThreads      map[string]bool
	nativeVersion        string
	detached             bool
	processLaunchID      string
	processContext       context.Context
	threadNames          map[string]string
	threadBindings       map[string]RuntimeBinding
	agentRuntime         *agentruntime.Service
	runtimeMu            sync.Mutex
	runtimeSessions      map[string]*agentruntime.Session
	usageStore           codexusage.Store
	process              *appServerProcess
	rpc                  *rpcTransport
	approvals            *approvalBroker
	dynamicTools         *dynamicToolBroker

	mu            sync.Mutex
	turns         map[turnKey]*turnState
	starting      map[string]*turnStartState
	retired       map[turnKey]uint64
	threadUsage   map[string]threadUsageState
	turnSerial    uint64
	retiredSerial uint64
	watchDone     chan struct{}
	closeOnce     sync.Once
}

type ThreadOptions struct {
	CWD             string
	RuntimeBinding  RuntimeBinding
	DynamicTools    []DynamicToolSpec
	Model           string
	ReasoningEffort string
	ServiceTier     string
}

type ThreadResumeOptions struct {
	ThreadID       string
	CWD            string
	RuntimeBinding RuntimeBinding
	UsageBaseline  *TokenCounts
	// DynamicTools are local-only registrations for this new App Server client.
	// Codex 0.149.0 does not support dynamicTools on thread/resume.
	DynamicTools    []DynamicToolSpec
	Model           string
	ReasoningEffort string
	ServiceTier     string
}

type Thread struct {
	ID string
}

type TurnOptions struct {
	RunID                string // Bridge attribution owner, never sent to Codex.
	ThreadID             string
	Prompt               string
	OutputSchema         json.RawMessage
	OnStarted            func(threadID, turnID string) error
	Model                string
	ReasoningEffort      string
	ServiceTier          *string
	AuthorizationCeiling AuthorizationCeiling
	RuntimeBinding       RuntimeBinding
}

// RuntimeBinding is the frozen filesystem authority passed to the pinned
// Codex 0.149.0 methods. RuntimeWorkspaceRoots are readable roots and
// WritableRoots is their write-capable subset. CWD remains an explicit thread
// option and must equal one of RuntimeWorkspaceRoots.
//
// An empty binding is only compatible with the legacy single-root thread
// start/resume API, where it is synthesized from that explicit CWD. A
// multi-root binding is never inferred from mutable process state.
type RuntimeBinding struct {
	RuntimeWorkspaceRoots []string
	WritableRoots         []string
	TaskPermissions       *TaskPermissions `json:",omitempty"`
}

type TurnResult struct {
	ThreadID     string
	TurnID       string
	FinalMessage string
	Commands     []CommandExecution
	TokenUsage   *TurnTokenUsage
}

// CommandExecution is observed execution evidence, distinct from the agent's
// final message. Output is bounded when captured from the App Server.
type CommandExecution struct {
	ItemID          string
	Command         string
	CWD             string
	Status          string
	Actions         []string
	ExitCode        *int
	Output          string
	OutputTruncated bool
	DurationMS      *int64
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

type TokenCounts = codexusage.Counts

type TurnTokenUsage struct {
	Attribution        codexusage.RunUsageAttribution
	StartCumulative    TokenCounts
	EndCumulative      TokenCounts
	ModelContextWindow *int64
	Reliable           bool
}

type turnKey struct {
	threadID string
	turnID   string
}

type turnState struct {
	serial         uint64
	finalMessage   observedMessage
	fallback       observedMessage
	completion     *wire.TurnCompletedParams
	waiter         chan struct{}
	usage          *codexusage.Tracker
	usageConfirmed bool
	usageContext   context.Context
	commands       map[string]CommandExecution
	commandOrder   []string
}

type threadUsageState struct {
	ledger        codexusage.CodexUsageLedger
	persistFailed bool
}

type observedMessage struct {
	seen bool
	text string
}

type turnStartState struct {
	baseline     threadUsageState
	runID        string
	threadID     string
	usageContext context.Context
	cancel       context.CancelFunc
	candidates   map[turnKey]struct{}
	overflow     *TurnLifecycleOverflowError
}

func Start(ctx context.Context, config Config) (*Client, error) {
	if config.AppServerBinding != nil {
		return attachAppServer(ctx, config)
	}
	if config.DiagnosticTaskReadOnly {
		return nil, errors.New("TASK_DIAGNOSTIC_REQUIRES_SOURCE_PROCESS")
	}
	var process *appServerProcess
	var err error
	var taskArgs []string
	if b := config.TaskRuntimeBinding; b != nil {
		if err = validateRuntimeBinding(*b); err != nil {
			return nil, err
		}
		if b.TaskPermissions == nil {
			return nil, errors.New("TASK_PERMISSION_PROFILE_MISSING")
		}
		taskArgs, err = b.TaskPermissions.ProfileArgs(b.RuntimeWorkspaceRoots, b.WritableRoots)
		if err != nil {
			return nil, err
		}
	}
	if config.VerifiedExecutable != nil {
		process, err = startVerifiedAppServer(ctx, config.VerifiedExecutable, taskArgs)
	} else {
		process, err = startAppServer(config.CodexPath, taskArgs)
	}
	if err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	observability.Info(ctx, "codex", "codex.app_server.started", "Codex app-server started", map[string]any{"executable": process.cmd.Path, "pid": process.cmd.Process.Pid})
	client := &Client{
		executorBoundary: true,
		usageStore:       config.UsageStore,
		agentRuntime:     config.AgentRuntime,
		runtimeSessions:  make(map[string]*agentruntime.Session),
		threadNames:      map[string]string{},
		processContext:   context.WithoutCancel(ctx),
		process:          process,
		turns:            make(map[turnKey]*turnState),
		starting:         make(map[string]*turnStartState),
		retired:          make(map[turnKey]uint64),
		threadUsage:      make(map[string]threadUsageState),
		watchDone:        make(chan struct{}),
	}
	if config.TaskRuntimeBinding != nil {
		client.taskPermissionHash = config.TaskRuntimeBinding.TaskPermissions.ContentHash
	}
	if client.agentRuntime != nil {
		client.processLaunchID, err = client.agentRuntime.ProcessStarted(ctx, process.cmd.Process.Pid, process.cmd.Path, process.startedAt)
		if err != nil {
			observability.Error(ctx, "codex", "codex.process.telemetry_failed", "Process launch telemetry unavailable", map[string]any{"error": err.Error()})
		}
	}
	processor := serverRequestProcessor(builtInServerRequestPolicy)
	if config.ApprovalMode == ApprovalModeInteractive {
		client.approvals = newApprovalBroker(config.AuthorizationCeiling)
		processor = client.approvals.process
	}
	if config.DynamicToolHandler != nil {
		client.dynamicTools = newDynamicToolBroker(config.DynamicToolHandler)
		next := processor
		processor = func(ctx context.Context, request ServerRequest) (any, error) {
			return client.dynamicTools.process(ctx, request, next)
		}
	}
	if config.AgentRuntime != nil {
		next := processor
		processor = func(ctx context.Context, request ServerRequest) (any, error) {
			return client.processRuntimeRequest(ctx, request, next)
		}
	}
	nextTaskProcessor := processor
	processor = func(ctx context.Context, request ServerRequest) (any, error) {
		return client.processTaskSandboxRequest(ctx, request, nextTaskProcessor)
	}
	client.rpc = newRPCTransportWithProcessor(process.stdout, process.stdin, processor, client.handleEvent, func(r ServerRequest) {
		client.observeRuntime(Event{Method: r.Method, Params: r.Params, RequestID: r.ID})
	})
	go client.watchProcess()

	var initialized wire.InitializeResponse
	err = client.rpc.Call(ctx, "initialize", initializeParams(config), &initialized)
	if err == nil {
		err = client.rpc.Notify("initialized", struct{}{})
	}
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize codex app-server: %w", err)
	}
	return client, nil
}

func initializeParams(config Config) wire.InitializeParams {
	params := wire.InitializeParams{ClientInfo: wire.ClientInfo{Name: "devbridge", Title: "Dev Bridge", Version: "0.1.0"}}
	if config.DynamicToolHandler != nil || config.TaskRuntimeBinding != nil {
		params.Capabilities = &wire.InitializeCapabilities{ExperimentalAPI: true}
	}
	return params
}

func (c *Client) StartThread(ctx context.Context, options ThreadOptions) (Thread, error) {
	binding, err := bindingForThread(options.CWD, options.RuntimeBinding)
	if err != nil {
		return Thread{}, err
	}
	if err = c.requireTaskProfile(binding); err != nil {
		return Thread{}, err
	}
	if len(options.DynamicTools) != 0 && (c.dynamicTools == nil || c.dynamicTools.handler == nil) {
		return Thread{}, errors.New("dynamic tools require a dynamic tool handler")
	}
	if err := validateDynamicToolSpecs(options.DynamicTools); err != nil {
		return Thread{}, err
	}
	var response wire.ThreadStartResponse
	params := wire.ThreadStartParams{CWD: options.CWD, DynamicTools: options.DynamicTools, Model: options.Model, Sandbox: threadSandbox(binding), RuntimeWorkspaceRoots: cloneRoots(binding.RuntimeWorkspaceRoots)}
	if options.Model != "" {
		params.ModelProvider = "openai"
	}
	params.Config, params.DeveloperInstructions, err = c.executorConfig(ctx, options.CWD)
	if err != nil {
		return Thread{}, err
	}
	params.ApprovalPolicy = c.taskApprovalPolicy(binding)
	if profile := c.taskProfileID(binding); profile != "" {
		params.Permissions = profile
		params.Sandbox = ""
	}
	params.Config = c.taskEnvironmentConfig(binding, params.Config)
	params.DeveloperInstructions += c.taskInstructions(binding)
	if options.ReasoningEffort != "" || options.ServiceTier != "" {
		if params.Config == nil {
			params.Config = map[string]any{}
		}
		if options.ReasoningEffort != "" {
			params.Config["model_reasoning_effort"] = options.ReasoningEffort
		}
		if options.ServiceTier != "" {
			params.Config["service_tier"] = options.ServiceTier
		}
	}
	if err := c.rpc.Call(ctx, "thread/start", params, &response); err != nil {
		return Thread{}, fmt.Errorf("start codex thread: %w", err)
	}
	if response.Thread.ID == "" {
		return Thread{}, &ProtocolError{Message: "thread/start returned an empty thread id"}
	}
	if err = c.verifyTaskProfile(binding, response.ActivePermissionProfile); err != nil {
		return Thread{}, err
	}
	c.runtimeMu.Lock()
	if c.threadNames == nil {
		c.threadNames = make(map[string]string)
	}
	if response.Thread.Name != nil {
		c.threadNames[response.Thread.ID] = *response.Thread.Name
	} else {
		c.threadNames[response.Thread.ID] = ""
	}
	if c.threadBindings == nil {
		c.threadBindings = make(map[string]RuntimeBinding)
	}
	c.threadBindings[response.Thread.ID] = cloneRuntimeBinding(binding)
	c.runtimeMu.Unlock()
	c.installUsageLedger(ctx, response.Thread.ID, true, nil)
	if err := c.verifyExecutorTools(ctx, response.Thread.ID); err != nil {
		return Thread{}, err
	}
	c.markExecutorThread(response.Thread.ID)
	c.dynamicTools.registerThread(response.Thread.ID, options.DynamicTools)
	return Thread{ID: response.Thread.ID}, nil
}

func (c *Client) SetThreadName(ctx context.Context, threadID, name string) error {
	if threadID == "" || name == "" {
		return errors.New("thread id and name are required")
	}
	var response wire.ThreadSetNameResponse
	if err := c.rpc.Call(ctx, "thread/name/set", wire.ThreadSetNameParams{ThreadID: threadID, Name: name}, &response); err != nil {
		return fmt.Errorf("set Codex thread name: %w", err)
	}
	return nil
}

// ResumeThread restores the exact conversation. Resume itself does not change
// accounting epochs; the durable Bridge ledger takes precedence over legacy
// caller baselines. Replayed old-Turn usage notifications are ignored.
func (c *Client) ResumeThread(ctx context.Context, options ThreadResumeOptions) (Thread, error) {
	if options.ThreadID == "" {
		return Thread{}, errors.New("thread id is required")
	}
	binding, err := bindingForThread(options.CWD, options.RuntimeBinding)
	if err != nil {
		return Thread{}, err
	}
	if err := c.requireSameThreadBinding(options.ThreadID, binding); err != nil {
		return Thread{}, err
	}
	if err = c.requireTaskProfile(binding); err != nil {
		return Thread{}, err
	}
	if len(options.DynamicTools) != 0 && (c.dynamicTools == nil || c.dynamicTools.handler == nil) {
		return Thread{}, errors.New("dynamic tools require a dynamic tool handler")
	}
	if err := validateDynamicToolSpecs(options.DynamicTools); err != nil {
		return Thread{}, err
	}
	var response wire.ThreadResumeResponse
	resumeParams := wire.ThreadResumeParams{ExcludeTurns: true, ThreadID: options.ThreadID, CWD: options.CWD, Model: options.Model, ServiceTier: options.ServiceTier, Sandbox: threadSandbox(binding), RuntimeWorkspaceRoots: cloneRoots(binding.RuntimeWorkspaceRoots)}
	if options.Model != "" {
		resumeParams.ModelProvider = "openai"
	}
	resumeParams.Config, resumeParams.DeveloperInstructions, err = c.executorConfig(ctx, options.CWD)
	if err != nil {
		return Thread{}, err
	}
	resumeParams.ApprovalPolicy = c.taskApprovalPolicy(binding)
	if profile := c.taskProfileID(binding); profile != "" {
		resumeParams.Permissions = profile
		resumeParams.Sandbox = ""
	}
	resumeParams.Config = c.taskEnvironmentConfig(binding, resumeParams.Config)
	resumeParams.DeveloperInstructions += c.taskInstructions(binding)
	if options.ReasoningEffort != "" {
		if resumeParams.Config == nil {
			resumeParams.Config = map[string]any{}
		}
		resumeParams.Config["model_reasoning_effort"] = options.ReasoningEffort
	}
	if err := c.rpc.Call(ctx, "thread/resume", resumeParams, &response); err != nil {
		return Thread{}, fmt.Errorf("resume codex thread: %w", err)
	}
	if response.Thread.ID == "" || response.Thread.ID != options.ThreadID {
		return Thread{}, &ProtocolError{Message: fmt.Sprintf("thread/resume returned unexpected thread id %q", response.Thread.ID)}
	}
	if err = c.verifyTaskProfile(binding, response.ActivePermissionProfile); err != nil {
		return Thread{}, err
	}
	if response.CWD == "" || filepath.Clean(response.CWD) != filepath.Clean(options.CWD) {
		return Thread{}, &ProtocolError{Message: fmt.Sprintf("thread/resume returned unexpected cwd %q", response.CWD)}
	}
	c.runtimeMu.Lock()
	if c.threadNames == nil {
		c.threadNames = make(map[string]string)
	}
	if response.Thread.Name != nil {
		c.threadNames[response.Thread.ID] = *response.Thread.Name
	} else {
		c.threadNames[response.Thread.ID] = ""
	}
	if c.threadBindings == nil {
		c.threadBindings = make(map[string]RuntimeBinding)
	}
	c.threadBindings[response.Thread.ID] = cloneRuntimeBinding(binding)
	c.runtimeMu.Unlock()
	c.installUsageLedger(ctx, response.Thread.ID, false, options.UsageBaseline)
	if err := c.verifyExecutorTools(ctx, response.Thread.ID); err != nil {
		return Thread{}, err
	}
	c.markExecutorThread(response.Thread.ID)
	c.dynamicTools.registerThread(response.Thread.ID, options.DynamicTools)
	return Thread{ID: response.Thread.ID}, nil
}

func (c *Client) StartTurn(ctx context.Context, options TurnOptions) (TurnResult, error) {
	if options.ThreadID == "" {
		return TurnResult{}, errors.New("turn thread id is required")
	}
	if options.Prompt == "" {
		return TurnResult{}, errors.New("turn prompt is required")
	}
	if len(options.OutputSchema) != 0 {
		if err := structuredschema.Validate(options.OutputSchema); err != nil {
			return TurnResult{}, fmt.Errorf("turn output schema is not strict: %w", err)
		}
	}
	binding, bound, err := c.bindingForTurn(options.ThreadID, options.RuntimeBinding)
	if err != nil {
		return TurnResult{}, err
	}
	if c.diagnosticTaskPolicy != nil && (options.AuthorizationCeiling != AuthorizationReadOnly || options.RunID == "") {
		return TurnResult{}, errors.New("TASK_DIAGNOSTIC_REQUIRES_READ_ONLY_TURN")
	}
	if binding.TaskPermissions != nil && (binding.TaskPermissions.RunID != options.RunID || (options.AuthorizationCeiling == AuthorizationReadOnly && binding.TaskPermissions.Purpose == "IMPLEMENTATION")) {
		return TurnResult{}, errors.New("TASK_PERMISSION_RUN_OR_PURPOSE_MISMATCH")
	}
	if err = c.requireTaskProfile(binding); err != nil {
		return TurnResult{}, err
	}
	mode, err := c.executorTurnMode(options, binding)
	if err != nil {
		return TurnResult{}, err
	}
	if err := c.ensureExecutorThread(ctx, options, binding); err != nil {
		return TurnResult{}, err
	}
	var runtimeSession *agentruntime.Session
	terminalObserved := false
	if c.agentRuntime != nil && options.RunID != "" {
		runtimeSession = c.agentRuntime.NewTurn(ctx, options.RunID, options.ThreadID, options.AuthorizationCeiling == AuthorizationReadOnly)
		c.runtimeMu.Lock()
		if c.runtimeSessions[options.ThreadID] != nil {
			c.runtimeMu.Unlock()
			runtimeSession.Close()
			return TurnResult{}, errors.New("runtime Turn already active")
		}
		runtimeSession.SetSessionName(c.threadNames[options.ThreadID])
		c.runtimeSessions[options.ThreadID] = runtimeSession
		c.runtimeMu.Unlock()
		if err := c.agentRuntime.BindProcess(ctx, options.RunID, c.processLaunchID); err != nil {
			observability.Error(ctx, "codex", "codex.process.binding_failed", "Run process telemetry unavailable", map[string]any{"error": err.Error()})
		}
		defer func() {
			if c.detached && !terminalObserved {
				_ = c.rpc.Detach()
			}
			c.finishRuntimeTurn(ctx, options.RunID, options.ThreadID, runtimeSession)
		}()
	}
	callContext, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	start, err := c.beginTurnStart(ctx, options.ThreadID, options.RunID, cancelCall)
	if err != nil {
		return TurnResult{}, err
	}
	if mode != nil {
		if err := c.injectTurnPolicy(callContext, options.ThreadID, mode); err != nil {
			if overflow := c.failTurnStart(start); overflow != nil {
				return TurnResult{}, overflow
			}
			return TurnResult{}, err
		}
	}
	params := wire.TurnStartParams{
		CollaborationMode: mode,
		ThreadID:          options.ThreadID,
		Input:             []wire.UserInput{{Type: "text", Text: options.Prompt}},
		OutputSchema:      append(json.RawMessage(nil), options.OutputSchema...),
		Model:             options.Model,
		Effort:            options.ReasoningEffort,
		ServiceTier:       options.ServiceTier,
		SandboxPolicy:     sandboxPolicyForBinding(options.AuthorizationCeiling, binding, bound),
		ApprovalPolicy:    c.taskApprovalPolicy(binding),
	}
	if profile := c.taskProfileID(binding); profile != "" {
		params.Permissions = profile
		params.SandboxPolicy = nil
	}
	if bound {
		params.RuntimeWorkspaceRoots = cloneRoots(binding.RuntimeWorkspaceRoots)
	}
	var response wire.TurnStartResponse
	if err := c.rpc.Call(callContext, "turn/start", params, &response); err != nil {
		if overflow := c.failTurnStart(start); overflow != nil {
			return TurnResult{}, overflow
		}
		return TurnResult{}, fmt.Errorf("start codex turn: %w", err)
	}
	if response.Turn.ID == "" {
		c.failTurnStart(start)
		return TurnResult{}, &ProtocolError{Message: "turn/start returned an empty turn id"}
	}

	key := turnKey{threadID: options.ThreadID, turnID: response.Turn.ID}
	result := TurnResult{ThreadID: options.ThreadID, TurnID: response.Turn.ID}
	c.mu.Lock()
	if start.overflow != nil {
		overflow := start.overflow
		c.finishTurnStartLocked(start, nil)
		c.mu.Unlock()
		return TurnResult{}, overflow
	}
	c.finishTurnStartLocked(start, &key)
	delete(c.retired, key)
	state := c.getOrCreateTurnLocked(key)
	// Use the dispatch-time coordinate even when all notifications preceded
	// the turn/start response. Other provisional Turns never reach the ledger.
	if state.usage == nil {
		state.usage = codexusage.Begin(start.baseline.ledger, options.RunID, key.turnID)
	}
	state.usageContext = start.usageContext
	state.usageConfirmed = true
	if start.baseline.persistFailed {
		state.usage.Unavailable("PERSISTENCE_FAILED")
	}
	if state.completion != nil {
		state.usage.Finish(true)
	}
	c.persistTurnUsageLocked(state)
	if state.completion == nil {
		state.waiter = make(chan struct{})
	}
	waiter := state.waiter
	completed := state.completion != nil
	c.compactTurnsLocked(&key)
	c.mu.Unlock()
	if options.OnStarted != nil {
		if err := options.OnStarted(options.ThreadID, response.Turn.ID); err != nil {
			interruptCtx, cancelInterrupt := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.interruptTurn(interruptCtx, options.ThreadID, response.Turn.ID)
			cancelInterrupt()
			c.retireTurn(key, state)
			return result, fmt.Errorf("record started Codex turn: %w", err)
		}
	}

	if runtimeSession != nil {
		runtimeSession.SetSteeringHandler(c.steerCurrentTurn)
		if err := runtimeSession.Bind(response.Turn.ID); err != nil {
			c.rpc.fail(err)
			return result, err
		}
	}
	if !completed {
		select {
		case <-waiter:
		case <-ctx.Done():
			if !c.turnCompleted(key, state) {
				c.retireTurn(key, state)
				return result, ctx.Err()
			}
		case <-c.rpc.Done():
			if !c.turnCompleted(key, state) {
				c.retireTurn(key, state)
				return TurnResult{}, c.rpc.Err()
			}
		}
	}

	c.mu.Lock()
	state = c.turns[key]
	if state != nil {
		c.retireTurnLocked(key, state)
	}
	c.mu.Unlock()
	if state == nil || state.completion == nil {
		return TurnResult{}, &ProtocolError{Message: "turn completion state disappeared"}
	}
	completion := state.completion
	terminalObserved = true
	message := state.finalMessage
	if !message.seen {
		message = state.fallback
	}
	if fromTurn := finalMessageFromItems(completion.Turn.Items); fromTurn.seen {
		message = fromTurn
	}
	result.FinalMessage = message.text
	result.Commands = commandExecutions(state)
	if state.usage != nil {
		a := state.usage.Attribution
		result.TokenUsage = &TurnTokenUsage{
			StartCumulative: a.LogicalStart, EndCumulative: a.LogicalEnd,
			ModelContextWindow: cloneInt64(a.ModelContextWindow), Reliable: a.Reliable && a.Observed && a.Complete,
			Attribution: a,
		}
	}
	if completion.Turn.Status != "completed" {
		failure := ""
		if completion.Turn.Error != nil {
			failure = completion.Turn.Error.Message
		}
		return result, &TurnError{ThreadID: result.ThreadID, TurnID: result.TurnID, Status: completion.Turn.Status, Message: failure, Failure: completion.Turn.Error}
	}
	return result, nil
}

func sandboxPolicy(ceiling AuthorizationCeiling) *wire.SandboxPolicy {
	switch ceiling {
	case AuthorizationReadOnly:
		return &wire.SandboxPolicy{Type: "readOnly"}
	case AuthorizationWorkspaceWrite:
		return &wire.SandboxPolicy{Type: "workspaceWrite"}
	default:
		return nil
	}
}

func (c *Client) interruptTurn(ctx context.Context, threadID, turnID string) error {
	var response wire.TurnInterruptResponse
	if err := c.rpc.Call(ctx, "turn/interrupt", wire.TurnInterruptParams{ThreadID: threadID, TurnID: turnID}, &response); err != nil {
		return fmt.Errorf("interrupt codex turn: %w", err)
	}
	return nil
}

// InterruptTurn requests interruption of one exact Turn. The empty protocol
// response is only an ACK. StartTurn continues waiting for turn/completed.
func (c *Client) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return errors.New("interrupt requires exact thread and turn IDs")
	}
	return c.interruptTurn(ctx, threadID, turnID)
}

func (c *Client) Events() <-chan Event      { return c.rpc.Events() }
func (c *Client) Diagnostics() <-chan error { return c.rpc.Diagnostics() }

func (c *Client) NextApproval(ctx context.Context) (ApprovalRequest, error) {
	if c == nil || c.approvals == nil {
		return ApprovalRequest{}, ErrApprovalModeDisabled
	}
	return c.approvals.next(ctx)
}

func (c *Client) ResolveApproval(ctx context.Context, id string, decision ApprovalDecision) error {
	if c == nil || c.approvals == nil {
		return ErrApprovalModeDisabled
	}
	return c.approvals.resolve(ctx, id, decision)
}
func (c *Client) StderrSnapshot() string {
	if c.process == nil {
		return ""
	}
	return c.process.stderrText()
}

func (c *Client) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.approvals != nil {
			c.approvals.close()
		}
		if c.rpc != nil {
			if c.detached {
				closeErr = c.rpc.Detach()
			} else {
				closeErr = c.rpc.Close()
			}
		}
		if c.process != nil {
			if err := c.process.Close(); closeErr == nil {
				closeErr = err
			}
		}
		if c.watchDone != nil {
			<-c.watchDone
		}
		c.runtimeMu.Lock()
		sessions := c.runtimeSessions
		c.runtimeSessions = make(map[string]*agentruntime.Session)
		c.runtimeMu.Unlock()
		for _, session := range sessions {
			session.Close()
		}
	})
	return closeErr
}

func (c *Client) watchProcess() {
	defer close(c.watchDone)
	<-c.process.done
	if c.agentRuntime != nil {
		if err := c.agentRuntime.ProcessEnded(c.processContext, c.processLaunchID, c.process.endedAt, c.process.cmd.ProcessState.ExitCode()); err != nil {
			observability.Error(c.processContext, "codex", "codex.process.exit_telemetry_failed", "Process exit telemetry unavailable", map[string]any{"error": err.Error()})
		}
	}
	detail := strings.TrimSpace(c.process.stderrText())
	err := fmt.Errorf("%w", ErrAppServerExited)
	if waitErr := c.process.waitError(); waitErr != nil {
		err = fmt.Errorf("%w: %v", ErrAppServerExited, waitErr)
	}
	if detail != "" {
		err = fmt.Errorf("%w; stderr: %s", err, detail)
	}
	c.rpc.fail(err)
}

func (c *Client) handleEvent(event Event) {
	c.observeRuntime(event)
	if len(event.RequestID) != 0 {
		return
	}
	switch event.Method {
	case "item/started":
		var params wire.ItemStartedParams
		if err := json.Unmarshal(event.Params, &params); err != nil || params.ThreadID == "" || params.TurnID == "" {
			c.eventDiagnostic(event, err)
			return
		}
		key := turnKey{threadID: params.ThreadID, turnID: params.TurnID}
		c.mu.Lock()
		state, cancel := c.observeTurnLocked(key)
		if state != nil {
			observeCommand(state, params.Item, false, eventTime(params.StartedAtMS))
		}
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case "item/completed":
		var params wire.ItemCompletedParams
		if err := json.Unmarshal(event.Params, &params); err != nil || params.ThreadID == "" || params.TurnID == "" {
			c.eventDiagnostic(event, err)
			return
		}
		key := turnKey{threadID: params.ThreadID, turnID: params.TurnID}
		c.mu.Lock()
		state, cancel := c.observeTurnLocked(key)
		if state != nil {
			var itemType struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(params.Item, &itemType) == nil && itemType.Type == "commandExecution" {
				observeCommand(state, params.Item, true, eventTime(params.CompletedAtMS))
			} else {
				var message wire.AgentMessage
				if json.Unmarshal(params.Item, &message) == nil && message.Type == "agentMessage" {
					if message.Phase != nil && *message.Phase == "final_answer" {
						state.finalMessage = observedMessage{seen: true, text: message.Text}
					} else if message.Phase == nil {
						state.fallback = observedMessage{seen: true, text: message.Text}
					}
				}
			}
		}
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case "turn/completed":
		var params wire.TurnCompletedParams
		if err := json.Unmarshal(event.Params, &params); err != nil || params.ThreadID == "" || params.Turn.ID == "" {
			c.eventDiagnostic(event, err)
			return
		}
		key := turnKey{threadID: params.ThreadID, turnID: params.Turn.ID}
		c.mu.Lock()
		state, cancel := c.observeTurnLocked(key)
		if state != nil && state.completion == nil {
			for _, item := range params.Turn.Items {
				observeCommand(state, item, true, time.Now().UTC())
			}
			state.completion = &params
			if state.usageConfirmed {
				state.usage.Finish(true)
				c.persistTurnUsageLocked(state)
			}
			if state.waiter != nil {
				close(state.waiter)
				state.waiter = nil
			}
		}
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case "thread/tokenUsage/updated":
		var params wire.ThreadTokenUsageUpdatedParams
		if err := json.Unmarshal(event.Params, &params); err != nil || params.ThreadID == "" || params.TurnID == "" {
			c.eventDiagnostic(event, err)
			return
		}
		key := turnKey{threadID: params.ThreadID, turnID: params.TurnID}
		c.mu.Lock()
		// Only dispatched or provisional candidates can carry usage. This also
		// rejects old-Turn replay after the bounded retired cache has been evicted.
		existing := c.turns[key]
		if c.starting[key.threadID] == nil && (existing == nil || !existing.usageConfirmed) {
			c.mu.Unlock()
			return
		}
		state, cancel := c.observeTurnLocked(key)
		if state != nil && state.usage == nil {
			if start := c.starting[key.threadID]; start != nil {
				state.usage = codexusage.Begin(start.baseline.ledger, start.runID, key.turnID)
			}
		}
		if state != nil && state.completion == nil && state.usage != nil {
			raw, err := decodeRawCumulative(params.TokenUsage.Total)
			if err != nil {
				state.usage.InvalidTelemetry()
				c.eventDiagnostic(event, err)
			} else {
				state.usage.Observe(raw, params.TokenUsage.ModelContextWindow)
			}
			if state.usageConfirmed {
				c.persistTurnUsageLocked(state)
			}
		}
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

const maxCommandOutputBytes = 32 << 10

func observeCommand(state *turnState, raw json.RawMessage, completed bool, observedAt time.Time) {
	var item wire.CommandExecution
	if json.Unmarshal(raw, &item) != nil || item.Type != "commandExecution" || item.ID == "" || item.Command == "" {
		return
	}
	if state.commands == nil {
		state.commands = make(map[string]CommandExecution)
	}
	value, exists := state.commands[item.ID]
	if !exists {
		value = CommandExecution{ItemID: item.ID}
		state.commandOrder = append(state.commandOrder, item.ID)
	}
	value.Command, value.CWD, value.Status = item.Command, item.CWD, item.Status
	value.Actions = commandActions(item.CommandActions)
	if !completed && value.StartedAt == nil {
		started := observedAt
		value.StartedAt = &started
	}
	if completed {
		finished := observedAt
		value.FinishedAt = &finished
		value.ExitCode = cloneInt(item.ExitCode)
		value.DurationMS = cloneInt64(item.DurationMS)
		if item.AggregatedOutput != nil {
			value.Output, value.OutputTruncated = boundedCommandOutput(*item.AggregatedOutput)
		}
	}
	state.commands[item.ID] = value
}

func commandActions(actions []wire.CommandAction) []string {
	result := make([]string, 0, len(actions))
	for _, action := range actions {
		if strings.TrimSpace(action.Command) != "" {
			result = append(result, action.Command)
		}
	}
	return result
}

func eventTime(milliseconds *int64) time.Time {
	if milliseconds == nil || *milliseconds <= 0 {
		return time.Now().UTC()
	}
	return time.UnixMilli(*milliseconds).UTC()
}

func commandExecutions(state *turnState) []CommandExecution {
	if state == nil || len(state.commandOrder) == 0 {
		return []CommandExecution{}
	}
	result := make([]CommandExecution, 0, len(state.commandOrder))
	for _, id := range state.commandOrder {
		value := state.commands[id]
		value.ExitCode = cloneInt(value.ExitCode)
		value.DurationMS = cloneInt64(value.DurationMS)
		value.StartedAt = cloneTime(value.StartedAt)
		value.FinishedAt = cloneTime(value.FinishedAt)
		value.Actions = append([]string(nil), value.Actions...)
		result = append(result, value)
	}
	return result
}

func boundedCommandOutput(value string) (string, bool) {
	if len(value) <= maxCommandOutputBytes {
		return value, false
	}
	return value[:maxCommandOutputBytes], true
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (c *Client) eventDiagnostic(event Event, decodeErr error) {
	message := "missing required lifecycle identifiers"
	if decodeErr != nil {
		message = decodeErr.Error()
	}
	c.rpc.diagnostic(&ProtocolError{Message: "malformed " + event.Method + " notification: " + message, Input: clippedLine(event.Params)})
}

func (c *Client) observeTurnLocked(key turnKey) (*turnState, context.CancelFunc) {
	if _, retired := c.retired[key]; retired {
		return nil, nil
	}
	if state := c.turns[key]; state != nil {
		return state, nil
	}
	if start := c.starting[key.threadID]; start != nil {
		if start.overflow != nil {
			c.retireTurnLocked(key, nil)
			return nil, nil
		}
		if _, exists := start.candidates[key]; !exists {
			if len(start.candidates) >= maxProvisionalTurnsPerStart {
				start.overflow = &TurnLifecycleOverflowError{
					ThreadID: key.threadID,
					Limit:    maxProvisionalTurnsPerStart,
				}
				for candidate := range start.candidates {
					if state := c.turns[candidate]; state != nil {
						c.retireTurnLocked(candidate, state)
					}
				}
				c.retireTurnLocked(key, nil)
				start.candidates = make(map[turnKey]struct{})
				return nil, start.cancel
			}
			start.candidates[key] = struct{}{}
		}
	}
	return c.getOrCreateTurnLocked(key), nil
}

func (c *Client) getOrCreateTurnLocked(key turnKey) *turnState {
	if state := c.turns[key]; state != nil {
		return state
	}
	c.turnSerial++
	state := &turnState{serial: c.turnSerial}
	if start := c.starting[key.threadID]; start != nil {
		state.usage = codexusage.Begin(start.baseline.ledger, start.runID, key.turnID)
	}
	c.turns[key] = state
	c.compactTurnsLocked(&key)
	return state
}

func tokenCounts(value wire.TokenUsageBreakdown) TokenCounts {
	return TokenCounts{InputTokens: value.InputTokens, CachedInputTokens: value.CachedInputTokens, CacheWriteInputTokens: value.CacheWriteInputTokens, OutputTokens: value.OutputTokens, ReasoningOutputTokens: value.ReasoningOutputTokens, TotalTokens: value.TotalTokens}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (c *Client) compactTurnsLocked(preserve *turnKey) {
	for len(c.turns) > maxRememberedTurns {
		var oldestKey turnKey
		oldestSerial := ^uint64(0)
		for candidateKey, candidate := range c.turns {
			start := c.starting[candidateKey.threadID]
			protected := false
			if start != nil {
				_, protected = start.candidates[candidateKey]
			}
			preserved := preserve != nil && candidateKey == *preserve
			if candidate.waiter == nil && !protected && !preserved && candidate.serial < oldestSerial {
				oldestKey, oldestSerial = candidateKey, candidate.serial
			}
		}
		if oldestSerial == ^uint64(0) {
			return
		}
		delete(c.turns, oldestKey)
	}
}

func (c *Client) beginTurnStart(ctx context.Context, threadID, runID string, cancel context.CancelFunc) (*turnStartState, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.starting == nil {
		c.starting = make(map[string]*turnStartState)
	}
	for key, state := range c.turns {
		if key.threadID == threadID && state.usageConfirmed && state.completion == nil {
			return nil, &ProtocolError{Message: "another Turn is active for thread " + threadID}
		}
	}
	if c.starting[threadID] != nil {
		return nil, &ProtocolError{Message: "another turn/start is already pending for thread " + threadID}
	}
	baseline := c.threadUsage[threadID]
	baseline.ledger.ThreadID = threadID
	if baseline.ledger.AccountingEpoch == 0 {
		baseline.ledger.AccountingEpoch = 1
	}
	// Persist the uncertain dispatch BEFORE issuing turn/start. A crash before
	// its response must not make unobserved spending part of the next Run.
	pending := baseline
	pending.ledger.InFlightTurnID = "pending"
	c.saveUsageLocked(ctx, &pending, nil)
	baseline.ledger.Revision = pending.ledger.Revision
	baseline.persistFailed = pending.persistFailed
	start := &turnStartState{
		baseline: baseline, runID: runID,
		threadID: threadID, usageContext: ctx,
		cancel:     cancel,
		candidates: make(map[turnKey]struct{}),
	}
	c.starting[threadID] = start
	return start, nil
}

func (c *Client) failTurnStart(start *turnStartState) *TurnLifecycleOverflowError {
	c.mu.Lock()
	overflow := start.overflow
	c.finishTurnStartLocked(start, nil)
	c.mu.Unlock()
	return overflow
}

func (c *Client) finishTurnStartLocked(start *turnStartState, preserve *turnKey) {
	if c.starting[start.threadID] != start {
		return
	}
	for candidate := range start.candidates {
		if preserve != nil && candidate == *preserve {
			continue
		}
		if state := c.turns[candidate]; state != nil {
			c.retireTurnLocked(candidate, state)
		}
	}
	delete(c.starting, start.threadID)
	c.compactTurnsLocked(preserve)
}

func (c *Client) turnCompleted(key turnKey, state *turnState) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turns[key] == state && state.completion != nil
}

func (c *Client) retireTurn(key turnKey, state *turnState) {
	c.mu.Lock()
	c.retireTurnLocked(key, state)
	c.mu.Unlock()
}

func (c *Client) retireTurnLocked(key turnKey, state *turnState) {
	if state != nil && c.turns[key] != state {
		return
	}
	if state != nil && state.usageConfirmed && state.completion == nil {
		state.usage.Finish(false)
		c.persistTurnUsageLocked(state)
	}
	delete(c.turns, key)
	if c.retired == nil {
		c.retired = make(map[turnKey]uint64)
	}
	c.retiredSerial++
	c.retired[key] = c.retiredSerial
	for len(c.retired) > maxRetiredTurns {
		var oldestKey turnKey
		oldestSerial := ^uint64(0)
		for candidate, serial := range c.retired {
			if serial < oldestSerial {
				oldestKey, oldestSerial = candidate, serial
			}
		}
		delete(c.retired, oldestKey)
	}
}

func finalMessageFromItems(items []json.RawMessage) observedMessage {
	var final, fallback observedMessage
	for _, item := range items {
		var message wire.AgentMessage
		if json.Unmarshal(item, &message) != nil || message.Type != "agentMessage" {
			continue
		}
		if message.Phase != nil && *message.Phase == "final_answer" {
			final = observedMessage{seen: true, text: message.Text}
		} else if message.Phase == nil {
			fallback = observedMessage{seen: true, text: message.Text}
		}
	}
	if final.seen {
		return final
	}
	return fallback
}

// Executable returns the exact path used to launch this app-server.
func (c *Client) Executable() string { return c.process.cmd.Path }
