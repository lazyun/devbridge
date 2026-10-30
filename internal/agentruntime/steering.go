package agentruntime

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Steering only adds input to an existing turn. It cannot change frozen scope,
// model, permissions, required checks, or any workflow approval.
type SteeringInput struct {
	Identity
	SubmissionRequestID string `json:"submissionRequestId"`
	Text                string `json:"text"`
	SubmittedBy         string `json:"submittedBy,omitempty"`
}
type SteeringReceipt struct {
	SteeringInput
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submittedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
type SteeringCapability struct {
	Allowed bool      `json:"allowed"`
	Reason  string    `json:"reason,omitempty"`
	Target  *Identity `json:"target,omitempty"`
}
type SteeringView struct {
	Capability SteeringCapability `json:"capability"`
	Receipts   []SteeringReceipt  `json:"receipts"`
}
type SteeringStore interface {
	CanSteer(context.Context, Identity) (bool, error)
	FindSteering(context.Context, SteeringInput) (SteeringReceipt, bool, error)
	BeginSteering(context.Context, SteeringInput) (SteeringReceipt, bool, error)
	FinishSteering(context.Context, SteeringReceipt) error
	ListSteering(context.Context, string) ([]SteeringReceipt, error)
}

// Handler returns ACCEPTED, REJECTED (definite native rejection), or UNKNOWN.
// It must make at most one turn/steer call and must never start a replacement.
type SteeringHandler func(context.Context, SteeringInput) string

func (s *Session) SetSteeringHandler(handler SteeringHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steer = handler
}

func SteeringReady(v Snapshot) bool {
	if v.Availability != "AVAILABLE" || v.ConnectionState != "CONNECTED" || v.TurnStatus != "inProgress" || v.RunID == "" || v.ThreadID == "" || v.TurnID == "" {
		return false
	}
	for _, r := range v.Requests {
		if OpenRequest(r) {
			return false
		}
	}
	if n := v.NativeThreadStatus; n != nil && (n.Type != "active" || len(n.ActiveFlags) != 0) {
		return false
	}
	return true
}

func (s *Service) Steering(ctx context.Context, runID string) (SteeringView, error) {
	out := SteeringView{Capability: SteeringCapability{Reason: "当前回合不可追加指令"}, Receipts: []SteeringReceipt{}}
	store, ok := s.store.(SteeringStore)
	if !ok {
		return out, nil
	}
	var err error
	out.Receipts, err = store.ListSteering(ctx, runID)
	if err != nil {
		return out, err
	}
	v, err := s.Read(ctx, runID)
	if err != nil {
		return out, err
	}
	if !SteeringReady(v) {
		return out, nil
	}
	allowed, err := store.CanSteer(ctx, v.Identity)
	if err != nil {
		return out, err
	}
	if !allowed {
		return out, nil
	}
	s.mu.Lock()
	session := s.sessions[v.ConnectionID]
	s.mu.Unlock()
	if session == nil {
		return out, nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.closing.Load() || session.err != nil || session.steer == nil || session.snapshot.Identity != v.Identity || !SteeringReady(v) || !SteeringReady(session.snapshot) {
		return out, nil
	}
	for _, r := range out.Receipts {
		if r.State == "DISPATCHING" || r.State == "UNKNOWN" {
			out.Capability.Reason = "上次发送结果待核对，不能再次发送"
			return out, nil
		}
	}
	id := v.Identity
	out.Capability = SteeringCapability{Allowed: true, Target: &id}
	return out, nil
}

func (s *Service) Steer(ctx context.Context, in SteeringInput) (SteeringReceipt, error) {
	if strings.TrimSpace(in.Text) == "" || len(in.Text) > 16<<10 || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) || in.SubmissionRequestID == "" || len(in.SubmissionRequestID) > 128 || in.SubmittedBy == "" {
		return SteeringReceipt{}, ErrInvalid
	}
	store, ok := s.store.(SteeringStore)
	if !ok {
		return SteeringReceipt{}, ErrUnsupported
	}
	// Replays remain readable after turn completion or a Core restart. The
	// immutable actor, exact target and text must all match the original receipt.
	if r, found, err := store.FindSteering(ctx, in); found || err != nil {
		return r, err
	}
	s.mu.Lock()
	session := s.sessions[in.ConnectionID]
	s.mu.Unlock()
	if session == nil {
		return SteeringReceipt{}, ErrStale
	}
	session.mu.Lock()
	if session.closed || session.closing.Load() || session.err != nil || session.steer == nil || session.snapshot.Identity != in.Identity || !SteeringReady(session.snapshot) {
		session.mu.Unlock()
		return SteeringReceipt{}, ErrStale
	}
	handler := session.steer
	r, dispatch, err := store.BeginSteering(ctx, in)
	session.mu.Unlock()
	if err != nil || !dispatch {
		return r, err
	}
	// The dispatch marker is already durable. Lost replies remain UNKNOWN and
	// are never automatically retried, including with a different request ID.
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r.State = handler(callCtx, in)
	if r.State != "ACCEPTED" && r.State != "REJECTED" {
		r.State = "UNKNOWN"
	}
	r.UpdatedAt = time.Now().UTC()
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	if err = store.FinishSteering(finishCtx, r); err != nil {
		r.State = "UNKNOWN"
		return r, errors.Join(errors.New("steering receipt persistence failed"), err)
	}
	return r, nil
}
