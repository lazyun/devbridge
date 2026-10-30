// Package probeexecution runs ContextProbes independently of normal Task/Run
// workspace semantics.
package probeexecution

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

type RepositoryLookup interface {
	Get(context.Context, string) (repository.Repository, error)
}
type ConfigLookup interface {
	Get(context.Context, string) (codexconfig.Config, error)
}

type CodexTurnResult struct {
	TurnID     string
	Message    string
	TokenUsage *run.TokenUsageObservation
}

type CodexSession interface {
	StartThread(context.Context, string) (string, error)
	StartTurn(context.Context, string, string) (CodexTurnResult, error)
	Close() error
}

type CodexFactory interface {
	Start(context.Context, codexconfig.Config) (CodexSession, error)
}

// DefaultCodexFactory starts an independent, read-only probe transport.
// Approval requests are declined because Project inspections have no engineering
// approval lifecycle and are never authorized to mutate the repository.
type DefaultCodexFactory struct{ Config codex.Config }

func (f DefaultCodexFactory) Start(ctx context.Context, selected codexconfig.Config) (CodexSession, error) {
	config := f.Config
	if selected.CodexPath != "" {
		config.CodexPath = selected.CodexPath
	}
	config.ApprovalMode = codex.ApprovalModeDecline
	client, err := codex.Start(ctx, config)
	if err != nil {
		return nil, err
	}
	return clientSession{client: client}, nil
}

type clientSession struct{ client *codex.Client }

func (s clientSession) StartThread(ctx context.Context, cwd string) (string, error) {
	value, err := s.client.StartThread(ctx, codex.ThreadOptions{CWD: cwd})
	return value.ID, err
}
func (s clientSession) StartTurn(ctx context.Context, threadID, prompt string) (CodexTurnResult, error) {
	return s.StartTurnWithStarted(ctx, threadID, prompt, nil)
}
func (s clientSession) StartTurnWithStarted(ctx context.Context, threadID, prompt string, onStarted func(string) error) (CodexTurnResult, error) {
	fence, _ := runtimeepoch.FromContext(ctx)
	value, err := s.client.StartTurn(ctx, codex.TurnOptions{RunID: fence.RunID, ThreadID: threadID, Prompt: prompt, AuthorizationCeiling: codex.AuthorizationReadOnly, OnStarted: func(_ string, turnID string) error {
		if onStarted == nil {
			return nil
		}
		return onStarted(turnID)
	}})
	result := CodexTurnResult{TurnID: value.TurnID, Message: value.FinalMessage}
	if value.TokenUsage != nil {
		result.TokenUsage = &run.TokenUsageObservation{
			Attribution:        &value.TokenUsage.Attribution,
			StartCumulative:    run.TokenCounts{InputTokens: value.TokenUsage.StartCumulative.InputTokens, CachedInputTokens: value.TokenUsage.StartCumulative.CachedInputTokens, CacheWriteInputTokens: value.TokenUsage.StartCumulative.CacheWriteInputTokens, OutputTokens: value.TokenUsage.StartCumulative.OutputTokens, ReasoningOutputTokens: value.TokenUsage.StartCumulative.ReasoningOutputTokens, TotalTokens: value.TokenUsage.StartCumulative.TotalTokens},
			EndCumulative:      run.TokenCounts{InputTokens: value.TokenUsage.EndCumulative.InputTokens, CachedInputTokens: value.TokenUsage.EndCumulative.CachedInputTokens, CacheWriteInputTokens: value.TokenUsage.EndCumulative.CacheWriteInputTokens, OutputTokens: value.TokenUsage.EndCumulative.OutputTokens, ReasoningOutputTokens: value.TokenUsage.EndCumulative.ReasoningOutputTokens, TotalTokens: value.TokenUsage.EndCumulative.TotalTokens},
			ModelContextWindow: value.TokenUsage.ModelContextWindow,
			Reliable:           value.TokenUsage.Reliable,
		}
	}
	return result, err
}
func (s clientSession) InterruptTurn(ctx context.Context, threadID, turnID string) error {
	return s.client.InterruptTurn(ctx, threadID, turnID)
}
func (s clientSession) Close() error { return s.client.Close() }

type inspectionSession struct {
	runID           string
	client          CodexSession
	ready           chan struct{}
	done            chan struct{}
	cancelRequested bool
}
type InspectionCancellationStore interface {
	executioncancel.Store
	FinishInspectionCancellation(context.Context, string, bool) error
}
type Runner struct {
	Canonical     *CanonicalRunner
	AgentRuntime  *agentruntime.Service
	Cancellations InspectionCancellationStore
	probes        *contextprobe.Service
	repositories  RepositoryLookup
	configs       ConfigLookup
	codex         CodexFactory
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	live          map[string]*inspectionSession
	wg            sync.WaitGroup
}

func NewRunner(probes *contextprobe.Service, repositories RepositoryLookup, configs ConfigLookup, codex CodexFactory) (*Runner, error) {
	if probes == nil || repositories == nil || configs == nil || codex == nil {
		return nil, errors.New("context probe runner dependencies are incomplete")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{probes: probes, repositories: repositories, configs: configs, codex: codex, ctx: ctx, cancel: cancel, live: map[string]*inspectionSession{}}, nil
}

func (r *Runner) Start(ctx context.Context, sessionID, probeID string) (contextprobe.Probe, error) {
	if r.Canonical != nil {
		return contextprobe.Probe{}, errors.New("canonical Inspection requires confirmed requestId and payload hash; legacy Inspection cannot start")
	}
	value, err := r.probes.GetForSession(ctx, sessionID, probeID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if value.Status != contextprobe.StatusCreated {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	repositoryValue, err := r.repositories.Get(ctx, value.RepositoryID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	configValue, err := r.configs.Get(ctx, value.CodexConfigID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	if !configValue.Enabled {
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	value, err = r.probes.Start(ctx, probeID)
	if err != nil {
		return contextprobe.Probe{}, err
	}
	ownedCtx := runtimeepoch.WithFence(ctx, value.RuntimeEpoch, value.RunID)
	if err = r.probes.ValidateExecution(ownedCtx, value); err != nil {
		return contextprobe.Probe{}, err
	}
	r.mu.Lock()
	if _, exists := r.live[probeID]; exists {
		r.mu.Unlock()
		return contextprobe.Probe{}, contextprobe.ErrConflict
	}
	r.live[probeID] = &inspectionSession{runID: value.RunID, ready: make(chan struct{}), done: make(chan struct{})}
	r.wg.Add(1)
	r.mu.Unlock()
	go r.execute(value, repositoryValue, configValue)
	return value, nil
}

func (r *Runner) execute(value contextprobe.Probe, repositoryValue repository.Repository, configValue codexconfig.Config) {
	r.mu.Lock()
	owned := r.live[value.ID]
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		pending := owned.cancelRequested
		close(owned.done)
		delete(r.live, value.ID)
		r.mu.Unlock()
		if pending && r.Cancellations != nil {
			_ = r.Cancellations.FinishInspectionCancellation(context.Background(), value.RunID, false)
		}
		r.wg.Done()
	}()
	ownedCtx := runtimeepoch.WithFence(r.ctx, value.RuntimeEpoch, value.RunID)
	ownedCtx = observability.WithContext(ownedCtx, observability.Correlation{ProjectID: value.ProjectID, TaskID: value.TaskID, ExecutionID: value.AgentExecutionID, RunID: value.RunID})
	var usage *run.TokenUsageObservation
	fail := func(err error) {
		_, _ = r.probes.Fail(ownedCtx, value.ID, boundedFailure(err), errors.Is(err, context.Canceled), usage)
	}
	if err := r.probes.ValidateExecution(ownedCtx, value); err != nil {
		return
	}
	session, err := r.codex.Start(ownedCtx, configValue)
	if err != nil {
		fail(err)
		return
	}
	defer session.Close()
	r.mu.Lock()
	owned.client = session
	r.mu.Unlock()
	if err := r.probes.ValidateExecution(ownedCtx, value); err != nil {
		fail(err)
		return
	}
	threadID, err := session.StartThread(ownedCtx, repositoryValue.Path)
	if err != nil {
		fail(err)
		return
	}
	value, err = r.probes.AttachThread(ownedCtx, value.ID, threadID)
	if err != nil {
		fail(err)
		return
	}
	prompt := BuildPrompt(value, repositoryValue.Path)
	if _, err := r.probes.RecordPrompt(context.WithoutCancel(ownedCtx), value.ID, prompt, time.Now().UTC()); err != nil {
		fail(err)
		return
	}
	onStarted := func(turnID string) error {
		_, err := r.probes.AttachTurn(context.WithoutCancel(ownedCtx), value.ID, turnID)
		if err == nil {
			r.mu.Lock()
			select {
			case <-owned.ready:
			default:
				close(owned.ready)
			}
			r.mu.Unlock()
		}
		return err
	}
	var result CodexTurnResult
	if err := r.probes.ValidateExecution(ownedCtx, value); err != nil {
		fail(err)
		return
	}
	if started, ok := session.(interface {
		StartTurnWithStarted(context.Context, string, string, func(string) error) (CodexTurnResult, error)
	}); ok {
		result, err = started.StartTurnWithStarted(ownedCtx, threadID, prompt, onStarted)
	} else {
		result, err = session.StartTurn(ownedCtx, threadID, prompt)
		if result.TurnID != "" {
			err = errors.Join(err, onStarted(result.TurnID))
		}
	}
	usage = result.TokenUsage
	// Preserve this live inspection owner while its immutable answer has no
	// exact receipt. Shutdown does not turn that uncertainty into failure.
	if r.AgentRuntime != nil {
		for {
			snapshot, readErr := r.AgentRuntime.Read(context.WithoutCancel(ownedCtx), value.RunID)
			if readErr != nil {
				return
			}
			if !agentruntime.HasUnresolvedResponse(snapshot) {
				break
			}
			r.mu.Lock()
			canceled := owned.cancelRequested
			r.mu.Unlock()
			if canceled {
				break
			}
			select {
			case <-ownedCtx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cancelRequested := owned.cancelRequested
	if cancelRequested && r.Cancellations != nil {
		var terminal *codex.TurnError
		known := err == nil || errors.As(err, &terminal)
		// Exact terminal output is not itself transport quiescence.
		known = session.Close() == nil && known
		if known {
			_, finishErr := r.probes.Fail(context.WithoutCancel(ownedCtx), value.ID, "Explicit user canceled inspection", true, usage)
			if finishErr != nil {
				known = false
			}
		}
		_ = r.Cancellations.FinishInspectionCancellation(context.WithoutCancel(ownedCtx), value.RunID, known)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	_, _ = r.probes.Succeed(context.WithoutCancel(ownedCtx), value.ID, result.Message, "", rootGuidanceIdentity(repositoryValue.Path), usage)
}

func (r *Runner) Shutdown() {
	if r == nil {
		return
	}
	if r.Canonical != nil {
		r.Canonical.Shutdown()
	}
	r.cancel()
	r.wg.Wait()
}

func BuildPrompt(value contextprobe.Probe, repositoryRoot string) string {
	return value.CodexInstruction
}

func rootGuidanceIdentity(root string) string {
	file, err := os.Open(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 1<<20)); err != nil {
		return ""
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func boundedFailure(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 2048 {
		return value[:2048]
	}
	return value
}

// CancelExecution reuses the canonical cancellation ledger for this independent
// inspection owner. Acknowledgment alone is never terminal or quiescent.
func (r *Runner) CancelExecution(ctx context.Context, in executioncancel.Input) (executioncancel.Receipt, error) {
	if r.Canonical != nil {
		return r.Canonical.CancelExecution(ctx, in)
	}
	in = in.Normalized()
	if r.Cancellations == nil || in.OwnerType != "INSPECTION" {
		return executioncancel.Receipt{}, executioncancel.ErrInvalidArgument
	}
	r.mu.Lock()
	owned := r.live[in.OwnerID]
	live := owned != nil && owned.runID == in.RunID
	receipt, dispatch, err := r.Cancellations.Begin(ctx, in, live)
	if err != nil || !dispatch {
		r.mu.Unlock()
		return receipt, err
	}
	owned.cancelRequested = true
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		waitCtx, stop := context.WithTimeout(r.ctx, 30*time.Second)
		defer stop()
		select {
		case <-owned.ready:
		case <-owned.done:
			return
		case <-waitCtx.Done():
			_ = r.Cancellations.RecordInterrupt(context.Background(), in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN")
			return
		}
		target, send, e := r.Cancellations.ClaimInterrupt(context.Background(), in.ProjectID, in.RequestID)
		if e != nil {
			select {
			case <-owned.done:
				return
			default:
			}
			_ = r.Cancellations.RecordInterrupt(context.Background(), in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN")
			return
		}
		if !send {
			return
		}
		r.mu.Lock()
		client := owned.client
		r.mu.Unlock()
		interrupt, ok := client.(interface {
			InterruptTurn(context.Context, string, string) error
		})
		if !ok {
			_ = r.Cancellations.RecordInterrupt(context.Background(), in.ProjectID, in.RequestID, "OUTCOME_UNKNOWN")
			return
		}
		signalCtx, stop := context.WithTimeout(r.ctx, 5*time.Second)
		defer stop()
		state := "ACKNOWLEDGED"
		if interrupt.InterruptTurn(signalCtx, target.ThreadID, target.TurnID) != nil {
			state = "OUTCOME_UNKNOWN"
		}
		_ = r.Cancellations.RecordInterrupt(context.Background(), in.ProjectID, in.RequestID, state)
	}()
	return receipt, nil
}

func (r *Runner) StartConfirmed(ctx context.Context, sessionID, probeID, requestID, payloadHash string) (contextprobe.Probe, error) {
	if r.Canonical == nil {
		return contextprobe.Probe{}, errors.New("canonical Inspection runtime unavailable")
	}
	return r.Canonical.StartConfirmed(ctx, sessionID, probeID, requestID, payloadHash)
}
func (r *Runner) RecoverAll(ctx context.Context) error {
	if r.Canonical == nil {
		return nil
	}
	return r.Canonical.RecoverAll(ctx)
}
