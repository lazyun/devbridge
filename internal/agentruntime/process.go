package agentruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// ProcessLaunch is launcher telemetry, never an ownership or cancellation key.
type ProcessLaunch struct {
	LaunchID     string     `json:"launchId"`
	PID          int        `json:"pid"`
	Executable   string     `json:"executable"`
	StartedAt    time.Time  `json:"startedAt"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
	ExitCode     *int       `json:"exitCode,omitempty"`
	RuntimeEpoch string     `json:"runtimeEpoch"`
}
type Agent struct {
	Name                string         `json:"name"`
	SessionName         string         `json:"sessionName"`
	ProcessAvailability string         `json:"processAvailability"`
	ProcessLaunchID     string         `json:"processLaunchId,omitempty"`
	PID                 int            `json:"pid,omitempty"`
	Process             *ProcessLaunch `json:"process,omitempty"`
}
type ProcessStore interface {
	RecordProcess(context.Context, ProcessLaunch) error
	BindProcess(context.Context, string, string) error
	EndProcess(context.Context, string, time.Time, int) error
}

func (s *Service) ProcessStarted(ctx context.Context, pid int, executable string, at time.Time) (string, error) {
	store, ok := s.store.(ProcessStore)
	if !ok {
		return "", nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	launch := ProcessLaunch{LaunchID: "agent_process_" + hex.EncodeToString(id[:]), PID: pid, Executable: executable, StartedAt: at}
	if err := store.RecordProcess(ctx, launch); err != nil {
		return "", err
	}
	return launch.LaunchID, nil
}
func (s *Service) BindProcess(ctx context.Context, runID, launchID string) error {
	if store, ok := s.store.(ProcessStore); ok && launchID != "" {
		return store.BindProcess(ctx, runID, launchID)
	}
	return nil
}
func (s *Service) ProcessEnded(ctx context.Context, id string, at time.Time, code int) error {
	if store, ok := s.store.(ProcessStore); ok && id != "" {
		return store.EndProcess(ctx, id, at, code)
	}
	return nil
}

// Health is a pure classifier. Terminal, unknown or disconnected observations
// cannot become stalled; an open official request takes precedence over age.
func Health(v Snapshot, status string, now time.Time) string {
	if status != "RUNNING" || v.ConnectionState != "CONNECTED" {
		return "ACTIVE"
	}
	for _, r := range v.Requests {
		if OpenRequest(r) {
			return "WAITING"
		}
	}
	if !v.LastActivityAt.IsZero() && now.Sub(v.LastActivityAt) > 30*time.Minute {
		return "POSSIBLY_STALLED"
	}
	return "ACTIVE"
}
