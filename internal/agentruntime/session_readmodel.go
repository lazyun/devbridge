package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	MaxSessionItems     = 1024
	MaxSessionBytes     = 16 << 20
	MaxSessionItemBytes = 64 << 10
	MaxSessionPage      = 50
)

// Coverage describes capture, never workflow success. Even a healthy materialized
// history does not claim to reproduce every transient protocol event.
type Coverage struct {
	State   string   `json:"state"`
	Reasons []string `json:"reasons"`
}
type SessionLimits struct {
	ItemsPerRun  int `json:"itemsPerRun"`
	BytesPerRun  int `json:"bytesPerRun"`
	BytesPerItem int `json:"bytesPerItem"`
	ItemsPerPage int `json:"itemsPerPage"`
}

func sessionLimits() SessionLimits {
	return SessionLimits{MaxSessionItems, MaxSessionBytes, MaxSessionItemBytes, MaxSessionPage}
}

type SessionRecord struct {
	Ordinal  uint64 `json:"ordinal"`
	Revision uint64 `json:"revision"`
	Item     Item   `json:"item"`
}
type HistoryPage struct {
	MaterializedSequence uint64          `json:"materializedSequence"`
	RunID                string          `json:"runId"`
	Items                []SessionRecord `json:"items"`
	NextBefore           uint64          `json:"nextBefore"`
	HasMore              bool            `json:"hasMore"`
	Coverage             Coverage        `json:"coverage"`
	Limits               SessionLimits   `json:"limits"`
}
type SessionSnapshot struct {
	RunID                       string        `json:"runId"`
	Generation                  string        `json:"generation"`
	Cursor                      uint64        `json:"cursor"`
	Runtime                     Snapshot      `json:"runtime"`
	Coverage                    Coverage      `json:"coverage"`
	Limits                      SessionLimits `json:"limits"`
	RuntimeOwner                string        `json:"runtimeOwner"`
	SupportsCoreRestartReattach bool          `json:"supportsCoreRestartReattach"`
}
type UpdatePage struct {
	RunID         string           `json:"runId"`
	Generation    string           `json:"generation"`
	Cursor        uint64           `json:"cursor"`
	ResetRequired bool             `json:"resetRequired"`
	Reason        string           `json:"reason,omitempty"`
	Snapshot      *SessionSnapshot `json:"snapshot,omitempty"`
}
type sessionView struct {
	hash       [32]byte
	cursor     uint64
	generation string
	readAt     time.Time
}

// HistoryBatch is a bounded display DTO. It carries no requests or raw protocol.
type HistoryBatch struct {
	Identity
	Items           []Item   `json:"items"`
	Coverage        Coverage `json:"coverage"`
	ThroughSequence uint64   `json:"throughSequence"`
}
type HistoryWriter interface {
	SaveSessionHistory(context.Context, HistoryBatch) error
}

type HistoryStore interface {
	ReadSessionHistory(context.Context, string, uint64, int) (HistoryPage, error)
}

func (s *Service) SessionSnapshot(ctx context.Context, runID string) (SessionSnapshot, error) {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	v, err := s.Read(ctx, runID)
	if err != nil {
		v = unavailable(runID)
		v.CaptureCoverage = Coverage{State: "UNAVAILABLE", Reasons: []string{"RUNTIME_RECORD_READ_FAILED"}}
	}
	coverage := v.CaptureCoverage
	if coverage.State == "" {
		coverage = Coverage{State: "PARTIAL", Reasons: []string{"LEGACY_TAIL_ONLY_HISTORY_NOT_CAPTURED"}}
	}
	if v.Availability == "UNAVAILABLE" || v.Sequence == 0 {
		if len(coverage.Reasons) == 0 || coverage.State != "UNAVAILABLE" {
			coverage = Coverage{State: "UNAVAILABLE", Reasons: []string{"NO_CAPTURED_RUNTIME"}}
		}
	}
	if store, ok := s.store.(HistoryStore); ok && v.Sequence > 0 {
		page, historyErr := store.ReadSessionHistory(ctx, runID, 0, 1)
		if historyErr != nil {
			coverage = Coverage{State: "UNAVAILABLE", Reasons: []string{"HISTORY_READ_FAILED"}}
		} else {
			for _, reason := range page.Coverage.Reasons {
				if !contains(coverage.Reasons, reason) {
					coverage.Reasons = append(coverage.Reasons, reason)
				}
			}
			if page.Coverage.State == "UNAVAILABLE" || page.Coverage.State == "GAP" || (page.Coverage.State == "TRUNCATED" && coverage.State != "GAP") {
				coverage.State = page.Coverage.State
			}
		}
	}
	if v.ConnectionState == "CONNECTION_LOST" && v.TurnStatus != "completed" && v.TurnStatus != "failed" && v.TurnStatus != "interrupted" {
		coverage.State = "GAP"
		if !contains(coverage.Reasons, "FINAL_RUNTIME_CAPTURE_UNAVAILABLE") {
			coverage.Reasons = append(coverage.Reasons, "FINAL_RUNTIME_CAPTURE_UNAVAILABLE")
		}
	}
	// A failed display write is observable without changing the authoritative Run.
	s.mu.Lock()
	for _, session := range s.sessions {
		if session.runID == runID {
			if session.projectionLost.Load() {
				coverage = Coverage{State: "GAP", Reasons: []string{"PROJECTION_PENDING_OR_STORAGE_FAILED"}}
			}
			if session.historyQueued.Load() > session.historyMaterialized.Load() && !contains(coverage.Reasons, "MATERIALIZATION_PENDING") {
				coverage.Reasons = append(coverage.Reasons, "MATERIALIZATION_PENDING")
			}
		}
	}
	s.mu.Unlock()
	value := SessionSnapshot{RunID: runID, Runtime: v, Coverage: coverage, Limits: sessionLimits(), RuntimeOwner: "IN_PROCESS_SUPERVISOR"}
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(encoded)
	view, exists := s.views[runID]
	if !exists {
		if len(s.views) >= 256 {
			var oldest string
			var at time.Time
			for id, entry := range s.views {
				if oldest == "" || entry.readAt.Before(at) {
					oldest = id
					at = entry.readAt
				}
			}
			delete(s.views, oldest)
		}
		s.viewSerial++
		view.generation = fmt.Sprintf("%s:%s:%d", s.generation, runID, s.viewSerial)
	}
	if !exists || view.hash != hash {
		view.cursor++
		view.hash = hash
	}
	view.readAt = time.Now().UTC()
	s.views[runID] = view
	value.Generation = view.generation
	value.Cursor = view.cursor
	return value, nil
}
func (s *Service) History(ctx context.Context, runID string, before uint64, limit int) (HistoryPage, error) {
	if limit <= 0 {
		limit = 25
	}
	if limit > MaxSessionPage {
		limit = MaxSessionPage
	}
	if before > uint64(^uint64(0)>>1) {
		return HistoryPage{}, errors.New("SESSION_HISTORY_CURSOR_INVALID")
	}
	if store, ok := s.store.(HistoryStore); ok {
		return store.ReadSessionHistory(ctx, runID, before, limit)
	}
	return HistoryPage{RunID: runID, Items: []SessionRecord{}, Coverage: Coverage{State: "UNAVAILABLE", Reasons: []string{"HISTORY_STORE_UNAVAILABLE"}}, Limits: sessionLimits()}, nil
}

// Updates is a serializable, bounded materialized-upsert poll used by the shared
// SSE route. Each HTTP client owns its cursor. No client consumes backend events.
// Snapshot replacement includes all current request/state facts; history remains
// separately pageable and must be invalidated when the cursor changes. It is not
// a durable event replay, so skipped intermediate states are never called complete.
func (s *Service) Updates(ctx context.Context, runID, generation string, after uint64) (UpdatePage, error) {
	v, err := s.SessionSnapshot(ctx, runID)
	if err != nil {
		return UpdatePage{}, err
	}
	out := UpdatePage{RunID: runID, Generation: v.Generation, Cursor: v.Cursor}
	if generation != v.Generation {
		out.ResetRequired = true
		out.Reason = "STREAM_GENERATION_CHANGED"
		return out, nil
	}
	if after > v.Cursor {
		out.ResetRequired = true
		out.Reason = "CURSOR_OUT_OF_RANGE"
		return out, nil
	}
	if after < v.Cursor {
		out.Snapshot = &v
	}
	return out, nil
}

func (s *Session) projectNotifications() {
	defer s.finishProjection()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	pending := make([]Event, 0, 32)
	flush := func() {
		if len(pending) == 0 && !s.projectionLost.Load() {
			return
		}
		s.mu.Lock()
		if !s.closed && s.err == nil {
			s.batching = true
			for _, event := range pending {
				if !s.bound {
					if s.displayBufferCount < 64 && s.displayBufferBytes+len(event.Params) <= 1<<20 {
						s.buffer = append(s.buffer, event)
						s.displayBufferBytes += len(event.Params)
						s.displayBufferCount++
					} else {
						s.projectionLost.Store(true)
					}
				} else {
					_ = s.observe(event)
				}
			}
			s.batching = false
			if s.bound && (s.dirty || s.projectionLost.Load()) {
				s.dirty = false
				if err := s.saveProjection(); err != nil {
					s.projectionStorageFailed.Store(true)
					s.projectionLost.Store(true)
				}
			}
		}
		s.mu.Unlock()
		clear(pending)
		pending = pending[:0]
	}
	for {
		select {
		case event := <-s.events:
			if event.barrier != nil {
				flush()
				close(event.barrier)
				continue
			}
			pending = append(pending, event)
			if len(pending) == cap(pending) {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-s.stop:
			for {
				select {
				case event := <-s.events:
					if event.barrier != nil {
						flush()
						close(event.barrier)
						continue
					}
					pending = append(pending, event)
					if len(pending) == 32 {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// enqueueHistory runs while the original snapshot lock is held. It copies only
// bounded, already materialized display fields and never performs storage I/O.
func (s *Session) enqueueHistory() {
	if _, ok := s.service.store.(HistoryWriter); !ok {
		return
	}
	batch := HistoryBatch{Identity: s.snapshot.Identity, Items: append([]Item(nil), s.snapshot.Items...), Coverage: s.snapshot.CaptureCoverage, ThroughSequence: s.snapshot.Sequence}
	batch.Coverage.Reasons = append([]string(nil), batch.Coverage.Reasons...)
	select {
	case <-s.historyStop:
		return
	default:
	}
	select {
	case s.history <- batch:
		s.historyQueued.Store(batch.ThroughSequence)
	default:
		s.historyOverflow.Store(true)
		s.projectionLost.Store(true)
	}
}

func (s *Session) projectHistory() {
	defer close(s.historyDone)
	writer, ok := s.service.store.(HistoryWriter)
	if !ok {
		return
	}
	write := func(batch HistoryBatch) {
		ctx, cancel := context.WithTimeout(s.historyCtx, 250*time.Millisecond)
		err := writer.SaveSessionHistory(ctx, batch)
		cancel()
		if err != nil {
			s.historyFailed.Store(true)
			s.projectionLost.Store(true)
		} else {
			s.historyMaterialized.Store(batch.ThroughSequence)
		}
	}
	for {
		select {
		case <-s.historyCtx.Done():
			return
		case batch := <-s.history:
			write(batch)
		case <-s.historyStop:
			// Exactly two queued batches at most. No producer remains after the
			// projection worker has drained and this stop is closed.
			for i := 0; i < cap(s.history); i++ {
				select {
				case batch := <-s.history:
					write(batch)
				default:
					return
				}
			}
			return
		}
	}
}

func (s *Session) finishProjection() {
	// Persist the final runtime snapshot and enqueue its materialization marker
	// before closing the history producer. Readers can then prove any lag.
	s.finishClose()
	close(s.historyStop)
	select {
	case <-s.historyDone:
	case <-time.After(750 * time.Millisecond):
		s.historyFailed.Store(true)
		s.projectionLost.Store(true)
		s.historyCancel()
	}
	s.projectionCancel()
	s.historyCancel()
	close(s.done)
}
