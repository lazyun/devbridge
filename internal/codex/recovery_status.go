package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

// RecoveryStatusTurnOptions binds a new diagnostic Turn to the original loaded
// Thread. BeforeDispatch must durably fence the single dispatch attempt. It runs
// only after the last read-only protocol admission check and before turn/start.
type RecoveryStatusTurnOptions struct {
	TurnOptions
	SourceTurnID   string
	ExpectedCWD    string
	BeforeDispatch func() error
}

// RecoveryStatusChildTurnOptions separates read-only source provenance from a
// fresh child Thread whose roots are owned by its prepared RuntimeWorkset.
// Callbacks are durable attempt/receipt boundaries; callers must not retry an
// uncertain thread/start or turn/start response as a new dispatch.
type RecoveryStatusChildTurnOptions struct {
	SourceThreadID        string
	SourceTurnID          string
	SourceCWD             string
	ChildThread           ThreadOptions
	ExistingChildThreadID string
	ChildTurn             TurnOptions
	BeforeThreadCreate    func() error
	OnThreadCreated       func(string) error
	BeforeTurnDispatch    func(string) error
}

func (c *Client) StartRecoveryStatusChildTurn(ctx context.Context, o RecoveryStatusChildTurnOptions) (TurnResult, error) {
	if o.SourceThreadID == "" || o.SourceTurnID == "" || o.SourceCWD == "" || o.ChildTurn.RunID == "" || o.ChildTurn.Prompt == "" || o.BeforeTurnDispatch == nil || (o.ExistingChildThreadID == "" && (o.BeforeThreadCreate == nil || o.OnThreadCreated == nil)) {
		return TurnResult{}, errors.New("incomplete recovery child authority")
	}
	if o.ChildThread.CWD == o.SourceCWD || o.ChildThread.CWD == "" || o.ChildTurn.ThreadID != "" {
		return TurnResult{}, errors.New("recovery child must use a fresh prepared root and Thread")
	}
	for _, root := range o.ChildThread.RuntimeBinding.RuntimeWorkspaceRoots {
		if root == o.SourceCWD {
			return TurnResult{}, errors.New("recovery child root matches source root")
		}
	}
	if _, err := c.ObserveRecoverySource(ctx, o.SourceThreadID, o.SourceTurnID, o.SourceCWD); err != nil {
		return TurnResult{}, err
	}
	binding, err := bindingForThread(o.ChildThread.CWD, o.ChildThread.RuntimeBinding)
	if err != nil {
		return TurnResult{}, err
	}
	if !sameRuntimeBinding(binding, o.ChildTurn.RuntimeBinding) {
		return TurnResult{}, errors.New("recovery child Turn binding differs from child Thread")
	}
	var child Thread
	if o.ExistingChildThreadID != "" {
		if o.ExistingChildThreadID == o.SourceThreadID {
			return TurnResult{}, errors.New("recovery child Thread matches source Thread")
		}
		child, err = c.readLoadedRecoveryChildThread(ctx, o.ExistingChildThreadID, o.ChildThread.CWD, binding)
		if err != nil {
			return TurnResult{}, err
		}
	} else {
		if err = o.BeforeThreadCreate(); err != nil {
			return TurnResult{}, err
		}
		child, err = c.StartThread(ctx, o.ChildThread)
		if err != nil {
			return TurnResult{}, err
		}
	}
	if child.ID == o.SourceThreadID {
		return TurnResult{}, errors.New("recovery child Thread matches source Thread")
	}
	if o.ExistingChildThreadID == "" {
		if err = o.OnThreadCreated(child.ID); err != nil {
			return TurnResult{}, err
		}
	}
	boundaryTurn := o.ChildTurn
	boundaryTurn.ThreadID = child.ID
	if err = c.ensureExecutorThread(ctx, boundaryTurn, binding); err != nil {
		return TurnResult{}, err
	}
	if err = o.BeforeTurnDispatch(child.ID); err != nil {
		return TurnResult{}, err
	}
	if o.ExistingChildThreadID != "" {
		started := o.ChildTurn.OnStarted
		o.ChildTurn.OnStarted = func(thread, turn string) error {
			if started != nil {
				if err := started(thread, turn); err != nil {
					return err
				}
			}
			// The first turn creates the rollout. Only now can this new
			// connection subscribe to the retained Thread's notifications.
			return c.subscribeRecoveryChildTurn(ctx, thread, turn, o.ChildThread.CWD)
		}
	}
	o.ChildTurn.ThreadID = child.ID
	o.ChildTurn.AuthorizationCeiling = AuthorizationReadOnly
	return c.StartTurn(ctx, o.ChildTurn)
}

func (c *Client) subscribeRecoveryChildTurn(ctx context.Context, thread, turn, cwd string) error {
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := retryRecoveryRolloutRead(ctx, thread, func(readCtx context.Context) error {
		return c.rpc.Call(readCtx, "thread/resume", map[string]any{"threadId": thread, "excludeTurns": true}, &response)
	}); err != nil {
		return fmt.Errorf("subscribe accepted Recovery Turn: %w", err)
	}
	if response.Thread.ID != thread {
		return errors.New("Recovery subscription returned different Thread")
	}
	var observed TurnObservation
	err := retryRecoveryRolloutRead(ctx, thread, func(readCtx context.Context) error {
		var err error
		observed, err = c.ObserveLoadedTurn(readCtx, thread, turn)
		return err
	})
	if err != nil {
		return fmt.Errorf("read accepted Recovery Turn: %w", err)
	}
	if observed.CWD != cwd {
		return errors.New("Recovery subscription changed child root")
	}
	switch observed.Turn.Status {
	case "inProgress":
		return nil
	case "completed", "failed", "interrupted":
		// Completion may precede subscription. Reconcile the exact terminal
		// read through the same event path as a delivered notification.
		params, err := json.Marshal(wire.TurnCompletedParams{ThreadID: thread, Turn: observed.Turn})
		if err != nil {
			return err
		}
		c.handleEvent(Event{Method: "turn/completed", Params: params})
		return nil
	default:
		return errors.New("Recovery subscription returned unknown Turn state")
	}
}

// The native first-turn acknowledgement can precede its rollout metadata
// flush. Retry only these read/subscription errors for the same accepted ID;
// this helper never retries thread/start or turn/start.
func retryRecoveryRolloutRead(ctx context.Context, thread string, read func(context.Context) error) error {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		err := read(readCtx)
		if err == nil {
			return nil
		}
		var rpc *RPCError
		if !errors.As(err, &rpc) || !strings.Contains(rpc.Message, thread) || !(rpc.Code == -32600 && strings.Contains(rpc.Message, "no rollout found for thread id") || rpc.Code == -32603 && strings.Contains(rpc.Message, "rollout") && strings.Contains(rpc.Message, "is empty")) {
			return err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-readCtx.Done():
			timer.Stop()
			return readCtx.Err()
		case <-timer.C:
		}
	}
}

// A newly created, still unstarted native Thread has no rollout to resume.
// Its durable receipt belongs to the same retained App Server; require that
// exact loaded idle Thread before installing the local read-only binding.
// The caller still has to claim the first turn dispatch durably.
func (c *Client) readLoadedRecoveryChildThread(ctx context.Context, id, cwd string, binding RuntimeBinding) (Thread, error) {
	if !c.detached || id == "" {
		return Thread{}, errors.New("exact detached Recovery child Thread required")
	}
	loaded := false
	cursor := ""
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
			return Thread{}, err
		}
		for _, candidate := range page.Data {
			if candidate == id {
				loaded = true
			}
		}
		if loaded || page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor {
			return Thread{}, errors.New("loaded thread cursor did not advance")
		}
		cursor = *page.NextCursor
	}
	if !loaded {
		return Thread{}, errors.New("Recovery child Thread is not loaded")
	}
	var response struct {
		Thread struct {
			ID     string `json:"id"`
			CWD    string `json:"cwd"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err := c.rpc.Call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false}, &response); err != nil {
		return Thread{}, err
	}
	if response.Thread.ID != id || response.Thread.CWD != cwd || response.Thread.Status.Type != "idle" {
		return Thread{}, errors.New("Recovery child Thread identity or idle state mismatch")
	}
	if err := c.requireSameThreadBinding(id, binding); err != nil {
		return Thread{}, err
	}
	c.runtimeMu.Lock()
	if c.threadBindings == nil {
		c.threadBindings = make(map[string]RuntimeBinding)
	}
	c.threadBindings[id] = cloneRuntimeBinding(binding)
	c.runtimeMu.Unlock()
	c.installUsageLedger(ctx, id, false, nil)
	return Thread{ID: id}, nil
}

// ObserveRecoverySource never creates/resumes a Thread or sends user input.
func (c *Client) ObserveRecoverySource(ctx context.Context, threadID, sourceTurnID, cwd string) (TurnObservation, error) {
	o, err := c.ObserveLoadedTurn(ctx, threadID, sourceTurnID)
	if err != nil {
		return o, fmt.Errorf("RECOVERY_THREAD_UNAVAILABLE: %w", err)
	}
	if o.CWD != cwd || cwd == "" {
		return o, errors.New("RECOVERY_THREAD_IDENTITY_MISMATCH")
	}
	if o.ThreadStatus != "idle" {
		return o, errors.New("RECOVERY_THREAD_ACTIVE")
	}
	switch o.Turn.Status {
	case "completed", "failed", "interrupted":
	default:
		return o, errors.New("RECOVERY_SOURCE_TURN_NOT_TERMINAL")
	}
	return o, nil
}

// StartRecoveryStatusTurn transmits caller-authored text without additions. The
// schema, roots and read-only intent travel solely in machine protocol fields.
// It deliberately cannot fall back to thread/start or a different Thread.
func (c *Client) StartRecoveryStatusTurn(ctx context.Context, o RecoveryStatusTurnOptions) (TurnResult, error) {
	if o.RunID == "" || o.SourceTurnID == "" || o.BeforeDispatch == nil {
		return TurnResult{}, errors.New("incomplete recovery status binding")
	}
	if _, err := c.ObserveRecoverySource(ctx, o.ThreadID, o.SourceTurnID, o.ExpectedCWD); err != nil {
		return TurnResult{}, err
	}
	binding, err := bindingForThread(o.ExpectedCWD, o.RuntimeBinding)
	if err != nil {
		return TurnResult{}, err
	}
	if err = c.requireSameThreadBinding(o.ThreadID, binding); err != nil {
		return TurnResult{}, err
	}
	o.AuthorizationCeiling = AuthorizationReadOnly
	o.RuntimeBinding = binding
	// thread/read does not subscribe a fresh connection to notifications. Resume
	// the exact already-loaded idle Thread without config or authority overrides.
	var subscribed struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = c.rpc.Call(ctx, "thread/resume", map[string]any{"threadId": o.ThreadID, "excludeTurns": true}, &subscribed); err != nil {
		return TurnResult{}, err
	}
	if subscribed.Thread.ID != o.ThreadID {
		return TurnResult{}, errors.New("RECOVERY_THREAD_IDENTITY_MISMATCH")
	}
	c.installUsageLedger(ctx, o.ThreadID, false, nil)
	if err = c.ensureExecutorThread(ctx, o.TurnOptions, binding); err != nil {
		return TurnResult{}, err
	}
	if err = o.BeforeDispatch(); err != nil {
		return TurnResult{}, err
	}
	return c.StartTurn(ctx, o.TurnOptions)
}
