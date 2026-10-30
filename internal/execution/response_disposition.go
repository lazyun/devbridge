package execution

import (
	"context"
	"errors"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/run"
)

// awaitResponseDisposition keeps the original live owner available to canonical
// cancellation. A completed Turn is observation, never a request receipt.
func (c *Coordinator) awaitResponseDisposition(ctx context.Context, runID string) (bool, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := runtimeAuthorityRun(context.WithoutCancel(ctx), c.Runs, runID)
		if err != nil {
			return false, err
		}
		if current.AgentRuntime == nil || !agentruntime.HasUnresolvedResponse(*current.AgentRuntime) {
			return false, nil
		}
		if c.Cancellations != nil {
			canceled, err := c.Cancellations.Requested(context.WithoutCancel(ctx), runID)
			if err != nil {
				return false, err
			}
			if canceled {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, agentruntime.ErrResponseDispositionUnknown
		case <-ticker.C:
		}
	}
}

// Production Run services expose strict request reads. The fallback supports
// alternate adapters while refusing an explicitly failed display projection.
func runtimeAuthorityRun(ctx context.Context, runs RunService, id string) (run.Run, error) {
	if reader, ok := runs.(interface {
		GetForRuntimeAuthority(context.Context, string) (run.Run, error)
	}); ok {
		return reader.GetForRuntimeAuthority(ctx, id)
	}
	v, err := runs.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if v.AgentRuntime != nil {
		for _, reason := range v.AgentRuntime.CaptureCoverage.Reasons {
			if reason == "RUNTIME_READ_FAILED" {
				return run.Run{}, errors.New("runtime request authority unavailable")
			}
		}
	}
	return v, nil
}
