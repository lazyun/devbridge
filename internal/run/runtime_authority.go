package run

import (
	"context"

	"github.com/lazyun/devbridge/internal/agentruntime"
)

// GetForRuntimeAuthority must not turn a failed request read into the empty
// display fallback used by Get. Durable stores can omit unrelated telemetry.
func (s *Service) GetForRuntimeAuthority(ctx context.Context, id string) (Run, error) {
	v, err := s.store.Get(ctx, id)
	if err != nil {
		return Run{}, err
	}
	var state agentruntime.Snapshot
	if reader, ok := s.store.(interface {
		ReadRuntimeRequests(context.Context, string) (agentruntime.Snapshot, error)
	}); ok {
		state, err = reader.ReadRuntimeRequests(ctx, id)
	} else if s.AgentRuntime != nil {
		state, err = s.AgentRuntime.Read(ctx, id)
	} else {
		return v, nil
	}
	if err != nil {
		return Run{}, err
	}
	v.AgentRuntime = &state
	return v, nil
}
