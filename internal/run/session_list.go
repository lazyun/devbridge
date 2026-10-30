package run

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
)

// ProjectPage is a bounded read of durable work, including reservations with no
// backend session. It cannot create or dispatch a Run.
type ProjectPage struct {
	ProjectID, FeatureAreaID, Scope string
	Status                          Status
	Limit, Offset                   int
}

func (s *Service) ListProjectPage(ctx context.Context, filter ProjectPage) ([]Run, error) {
	if filter.ProjectID == "" || filter.Limit < 1 || filter.Limit > 101 || filter.Offset < 0 || filter.Offset > 1_000_000 {
		return nil, errors.New("invalid bounded Run page")
	}
	reader, ok := s.store.(interface {
		ListProjectPage(context.Context, ProjectPage) ([]Run, error)
	})
	if !ok {
		return nil, errors.New("bounded Run inventory unavailable")
	}
	values, err := reader.ListProjectPage(ctx, filter)
	if err != nil {
		return nil, err
	}
	for i := range values {
		if s.AgentRuntime != nil {
			state, e := s.AgentRuntime.Read(ctx, values[i].ID)
			if e != nil {
				state = unavailableRuntime(values[i].ID)
			}
			values[i].AgentRuntime = &state
		}
	}
	return values, nil
}

// A display-store failure does not change the authoritative Run or its outcome.
func unavailableRuntime(runID string) agentruntime.Snapshot {
	return agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: runID}, Availability: "UNAVAILABLE", ConnectionState: "UNKNOWN", CaptureCoverage: agentruntime.Coverage{State: "UNAVAILABLE", Reasons: []string{"RUNTIME_READ_FAILED"}}}
}
