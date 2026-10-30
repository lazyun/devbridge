package agentruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"time"
)

type ContinuityStore interface {
	BindAppServer(context.Context, string, appserver.Binding) error
	AppServerBinding(context.Context, string) (appserver.Binding, error)
	Reattach(context.Context, Identity, appserver.Binding) (Snapshot, error)
}

func (s *Service) BindAppServer(ctx context.Context, id string, b appserver.Binding) error {
	st, ok := s.store.(ContinuityStore)
	if !ok {
		return ErrUnsupported
	}
	return st.BindAppServer(ctx, id, b)
}
func (s *Service) AppServerBinding(ctx context.Context, id string) (appserver.Binding, error) {
	st, ok := s.store.(ContinuityStore)
	if !ok {
		return appserver.Binding{}, ErrUnsupported
	}
	return st.AppServerBinding(ctx, id)
}
func (s *Service) RestoreTurn(ctx context.Context, runID, threadID, turnID string, b appserver.Binding, readOnly bool) (*Session, error) {
	st, ok := s.store.(ContinuityStore)
	if !ok {
		return nil, ErrUnsupported
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	if err := initContract(); err != nil {
		return nil, err
	}
	id := Identity{RunID: runID, ThreadID: threadID, TurnID: turnID, ConnectionID: hex.EncodeToString(token[:])}
	v, err := st.Reattach(ctx, id, b)
	if err != nil {
		return nil, err
	}
	session := s.NewTurn(runtimeepoch.WithFence(context.WithoutCancel(ctx), v.RuntimeEpoch, v.RunID), runID, threadID, readOnly)
	session.mu.Lock()
	session.snapshot = v
	session.connectionID = v.ConnectionID
	session.bound = true
	// Waiters are created only when the same server replays its exact outstanding
	// request. UNKNOWN alone is never evidence that dispatch is safe.
	session.mu.Unlock()
	s.mu.Lock()
	s.sessions[v.ConnectionID] = session
	s.mu.Unlock()
	session.readyOnce.Do(func() { close(session.ready) })
	return session, nil
}
func (s *Session) replayRequest(r *Request, event Event) error {
	if r.Method != event.Method || string(r.Payload) != string(event.Params) {
		return ErrStale
	}
	key := requestKey(r.RequestID)
	if !OpenRequest(*r) {
		return ErrStale
	}
	if s.waiters[key] != nil {
		return nil
	}
	r.PendingReplayVerified = true
	if r.Kind != "CODEX_REQUEST" {
		if err := s.save(false); err != nil {
			return err
		}
		s.waiters[key] = make(chan json.RawMessage, 1)
		return nil
	}
	if r.UserDecision == nil {
		r.State = "WAITING_FOR_USER"
		r.AvailableActions = []string{"RESPOND", "STOP_AND_REVISE", "CANCEL_OPERATION"}
		if err := s.save(false); err != nil {
			return err
		}
		s.waiters[key] = make(chan json.RawMessage, 1)
		return nil
	}
	if r.UserDecision.Action != "RESPOND" {
		return ErrStale
	}
	response, err := SerializeUserDecision(*r, *r.UserDecision)
	if err != nil {
		return err
	}
	r.RecoveryEvidence = append(r.RecoveryEvidence, responseEvidence(*r, "EXACT_PENDING_REPLAY", time.Now().UTC()))
	r.State = "USER_DECISION_RECORDED"
	r.Delivery = "NOT_DISPATCHED"
	r.Resolution = response
	r.AvailableActions = nil
	if err = s.save(true); err != nil {
		return err
	}
	s.waiters[key] = make(chan json.RawMessage, 1)
	s.waiters[key] <- response
	s.responseQueued[key] = true
	return nil
}
