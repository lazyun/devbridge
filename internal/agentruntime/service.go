package agentruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/observability"
)

type Service struct {
	store      Store
	mu         sync.Mutex
	sessions   map[string]*Session
	generation string
	viewMu     sync.Mutex
	views      map[string]sessionView
	viewSerial uint64
}

func NewService(store Store) *Service {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("runtime stream generation unavailable")
	}
	return &Service{store: store, sessions: map[string]*Session{}, generation: hex.EncodeToString(id[:]), views: map[string]sessionView{}}
}
func (s *Service) Read(ctx context.Context, runID string) (Snapshot, error) {
	v, err := s.store.Read(ctx, runID)
	ApplyRequestProjections(&v)
	return v, err
}
func (s *Service) NewTurn(ctx context.Context, runID, threadID string, readOnly bool) *Session {
	var id [16]byte
	_, err := rand.Read(id[:])
	if err == nil {
		err = initContract()
	}
	session := &Session{connectionID: hex.EncodeToString(id[:]), runID: runID, events: make(chan Event, 64), stop: make(chan struct{}), done: make(chan struct{}), service: s, ctx: context.WithoutCancel(ctx), readOnly: readOnly, ready: make(chan struct{}), waiters: map[string]chan json.RawMessage{}, responseQueued: map[string]bool{}, err: err, snapshot: Snapshot{Identity: Identity{RunID: runID, ThreadID: threadID, ConnectionID: hex.EncodeToString(id[:])}, Availability: "AVAILABLE", WaitingState: "RUNNING", ConnectionState: "CONNECTED", CaptureCoverage: Coverage{State: "PARTIAL", Reasons: []string{"MATERIALIZED_ITEMS_NOT_EVENT_LOG"}}}}
	session.projectionCtx, session.projectionCancel = context.WithCancel(session.ctx)
	session.historyCtx, session.historyCancel = context.WithCancel(session.ctx)
	session.history = make(chan HistoryBatch, 2)
	session.historyStop = make(chan struct{})
	session.historyDone = make(chan struct{})
	go session.projectHistory()
	go session.projectNotifications()
	return session
}
func (s *Service) Resolve(ctx context.Context, in Resolution) (Snapshot, error) {
	s.mu.Lock()
	session := s.sessions[in.ConnectionID]
	s.mu.Unlock()
	if session == nil {
		return Snapshot{}, ErrStale
	}
	return session.resolve(ctx, in)
}

type Session struct {
	steer               SteeringHandler
	connectionID        string
	closing             atomic.Bool
	historyQueued       atomic.Uint64
	historyMaterialized atomic.Uint64
	projectionCtx       context.Context
	projectionCancel    context.CancelFunc
	projectionWrite     bool
	historyCtx          context.Context
	historyCancel       context.CancelFunc
	history             chan HistoryBatch
	historyStop         chan struct{}
	historyDone         chan struct{}
	historyFailed       atomic.Bool
	historyOverflow     atomic.Bool

	runID                   string
	events                  chan Event
	stop                    chan struct{}
	done                    chan struct{}
	stopOnce                sync.Once
	projectionLost          atomic.Bool
	projectionStorageFailed atomic.Bool
	batching                bool
	dirty                   bool

	service            *Service
	ctx                context.Context
	mu                 sync.Mutex
	snapshot           Snapshot
	readOnly           bool
	ready              chan struct{}
	readyOnce          sync.Once
	bound              bool
	closed             bool
	err                error
	buffer             []Event
	bufferBytes        int
	displayBufferBytes int
	displayBufferCount int
	waiters            map[string]chan json.RawMessage
	responseQueued     map[string]bool
}

func (s *Session) Bind(turnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing.Load() || s.err != nil {
		return ErrStale
	}
	id := s.snapshot.Identity
	id.TurnID = turnID
	identity, err := s.service.store.Open(s.ctx, id)
	if err != nil {
		s.err = err
		s.readyOnce.Do(func() { close(s.ready) })
		return err
	}
	s.snapshot.Identity = identity
	s.bound = true
	s.service.mu.Lock()
	if s.closing.Load() {
		s.service.mu.Unlock()
		s.err = ErrStale
		s.readyOnce.Do(func() { close(s.ready) })
		return ErrStale
	}
	s.service.sessions[identity.ConnectionID] = s
	s.service.mu.Unlock()
	if err = s.save(false); err == nil {
		sort.SliceStable(s.buffer, func(i, j int) bool { return s.buffer[i].ReceivedAt.Before(s.buffer[j].ReceivedAt) })
		for _, event := range s.buffer {
			display := len(event.RequestID) == 0 && event.Method != "serverRequest/resolved" && event.Method != "bridge/requestRejected"
			s.batching = display
			observeErr := s.observe(event)
			s.batching = false
			if observeErr != nil {
				err = observeErr
				break
			}
		}
		if err == nil && s.dirty {
			s.dirty = false
			if displayErr := s.saveProjection(); displayErr != nil {
				s.projectionStorageFailed.Store(true)
				s.projectionLost.Store(true)
			}
		}
	}
	s.buffer = nil
	s.bufferBytes = 0
	s.displayBufferBytes = 0
	s.displayBufferCount = 0
	s.err = err
	s.readyOnce.Do(func() { close(s.ready) })
	return err
}

// Observe receives the transport owner's correlated notification copy. Display
// traffic never waits for the database or a browser. Requests retain their
// existing durable authority path and cannot be consumed by a UI subscription.
func (s *Session) Observe(event Event) error {
	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = time.Now().UTC()
	}
	if len(event.RequestID) != 0 || event.Method == "serverRequest/resolved" || event.Method == "bridge/requestRejected" {
		if len(event.RequestID) != 0 && s.closing.Load() {
			return ErrStale
		}
		return s.observeNow(event)
	}
	if strings.HasPrefix(event.Method, "item/reasoning/") {
		return nil
	}
	select {
	case <-s.stop:
		return nil
	default:
	}
	if len(event.Params) > MaxPayload || len(event.Method) > 256 {
		s.projectionLost.Store(true)
		return nil
	}
	event.Params = append(json.RawMessage(nil), event.Params...)
	select {
	case s.events <- event:
	default:
		s.projectionLost.Store(true)
	}
	return nil
}
func (s *Session) observeNow(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if !s.bound {
		if len(s.buffer) >= 256 || s.bufferBytes+len(event.Params) > 1<<20 {
			s.err = fmt.Errorf("agent telemetry pre-bind capacity exceeded")
			return s.err
		}
		s.buffer = append(s.buffer, event)
		s.bufferBytes += len(event.Params)
		return nil
	}
	if s.err != nil {
		return s.err
	}
	return s.observe(event)
}
func (s *Session) observe(event Event) (err error) {
	var releaseAfterSave []json.RawMessage
	if len(event.RequestID) != 0 || event.Method == "serverRequest/resolved" || event.Method == "bridge/requestRejected" {
		// Request state and lifecycle receipts remain retryable on write failure.
		// Publish or release waiters only after the corresponding save succeeds.
		before := clone(s.snapshot)
		defer func() {
			if err != nil {
				s.snapshot = before
				return
			}
			for _, id := range releaseAfterSave {
				s.releaseWaiter(id)
			}
		}()
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(event.Params, &p) != nil {
		return nil
	}
	var threadID, turnID string
	_ = json.Unmarshal(p["threadId"], &threadID)
	_ = json.Unmarshal(p["turnId"], &turnID)
	if b := p["turn"]; b != nil {
		var t struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(b, &t)
		turnID = t.ID
	}
	if threadID != "" && threadID != s.snapshot.ThreadID {
		return nil
	}
	if turnID != "" && turnID != s.snapshot.TurnID {
		return nil
	}
	if (threadID == "" || turnID == "") && (strings.HasPrefix(event.Method, "item/") || strings.HasPrefix(event.Method, "turn/")) {
		return nil
	}
	// Lifecycle resolution has no Turn in the pinned wire; require the exact
	// existing typed request ID. All other Run-visible notices require a Turn.
	if turnID == "" && event.Method != "thread/name/updated" && event.Method != "thread/status/changed" {
		if event.Method != "serverRequest/resolved" && event.Method != "bridge/requestRejected" {
			return nil
		}
		matched := false
		for _, request := range s.snapshot.Requests {
			if request.ThreadID == threadID && requestKey(request.RequestID) == requestKey(p["requestId"]) {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
	}
	terminal := s.snapshot.TurnStatus == "completed" || s.snapshot.TurnStatus == "failed" || s.snapshot.TurnStatus == "interrupted"
	if terminal && event.Method != "serverRequest/resolved" {
		return nil
	}
	if event.Method == "thread/name/updated" && threadID == s.snapshot.ThreadID {
		var name string
		_ = json.Unmarshal(p["threadName"], &name)
		s.snapshot.Agent.SessionName = sessionName(name)
		return s.save(false)
	}
	if event.Method == "thread/status/changed" {
		var status ThreadStatus
		if threadID == "" || json.Unmarshal(p["status"], &status) != nil || !ValidThreadStatus(status) {
			return nil
		}
		status.ObservedAt = event.ReceivedAt
		if status.ObservedAt.IsZero() {
			status.ObservedAt = time.Now().UTC()
		}
		s.snapshot.NativeThreadStatus = &status
	}
	now := event.ReceivedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.After(s.snapshot.LastActivityAt) {
		s.snapshot.LastActivityAt = now
	}
	if len(event.RequestID) != 0 {
		if len(event.RequestID) > 1024 {
			s.diagnostic(event, "Server request ID exceeds telemetry cap", now)
			return s.save(false)
		}
		if !Supports(event.Method) {
			if event.Method != "item/tool/call" {
				s.diagnostic(event, "Unsupported agent request", now)
				s.snapshot.LastWarning = &Diagnostic{Method: event.Method, At: now, Summary: "UNSUPPORTED_AGENT_REQUEST"}
			}
			return s.save(false)
		}
		if err := Validate(event.Method, "request", event.Params); err != nil {
			s.diagnostic(event, "Invalid runtime request payload", now)
			return s.save(false)
		}
		// An uncorrelated elicitation remains unsupported; never infer Turn ownership.
		if threadID == "" || turnID == "" {
			s.diagnostic(event, "Request has no exact Turn correlation", now)
			return s.save(false)
		}
		if event.Method == "mcpServer/elicitation/request" && string(p["mode"]) == `"openai/form"` {
			s.diagnostic(event, "openai/form capability is not advertised", now)
			return s.save(false)
		}
		for i := range s.snapshot.Requests {
			r := &s.snapshot.Requests[i]
			if requestKey(r.RequestID) == requestKey(event.RequestID) {
				if r.OriginalIdentity != nil {
					return s.replayRequest(r, event)
				}
				return nil
			}
		}
		if len(s.snapshot.Requests) >= MaxRequests {
			// Keep closed identities for the lifetime of this Turn. Evicting one
			// would let a replay become a new answerable request with the same ID.
			s.diagnostic(event, "Turn request capacity exceeded", now)
			return s.save(false)
		}
		var itemID string
		_ = json.Unmarshal(p["itemId"], &itemID)
		request := Request{Identity: s.snapshot.Identity, RequestID: event.RequestID, ItemID: itemID, Method: event.Method, Payload: event.Params, State: "PENDING", WaitingState: WaitingState(event.Method), CreatedAt: now, AvailableResponses: available(event.Method, event.Params, s.readOnly)}
		request.ID = "runtime_request_" + hashBytes(raw([]any{request.Identity, request.RequestID}))
		request.Kind = "RUNTIME_APPROVAL"
		request.PayloadSHA256 = hashBytes(raw(request.Payload))
		request.ResponseSchema = raw(contract.Methods[event.Method].Response)
		request.ResponseSchemaSHA256 = hashBytes(request.ResponseSchema)
		request.UpdatedAt = now
		if event.Method == "item/tool/requestUserInput" || event.Method == "mcpServer/elicitation/request" {
			request.Kind = "CODEX_REQUEST"
			request.State = "WAITING_FOR_USER"
			request.Delivery = "NOT_DISPATCHED"
			if CompleteDecisionIdentity(request.Identity) {
				request.AvailableActions = []string{"RESPOND", "STOP_AND_REVISE", "CANCEL_OPERATION"}

			}
		}
		s.snapshot.Requests = append(s.snapshot.Requests, request)
		if err := s.save(false); err != nil {
			return err
		}
		s.waiters[requestKey(event.RequestID)] = make(chan json.RawMessage, 1)
		return nil
	}
	switch event.Method {
	case "bridge/requestRejected":
		var method string
		_ = json.Unmarshal(p["rejectedMethod"], &method)
		s.snapshot.LastWarning = &Diagnostic{Method: method, At: now, Summary: summary(p)}
		for i := range s.snapshot.Requests {
			r := &s.snapshot.Requests[i]
			if requestKey(r.RequestID) == requestKey(p["requestId"]) && OpenRequest(*r) {
				r.State = "REJECTED"
				r.Delivery = "JSONRPC_ERROR"
				r.ResolvedAt = &now
				releaseAfterSave = append(releaseAfterSave, r.RequestID)
			}
		}
	case "turn/started":
		s.snapshot.TurnStatus = "inProgress"
	case "turn/completed":
		var t struct {
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(p["turn"], &t)
		s.snapshot.TurnStatus = t.Status
		if len(t.Error) > 0 && string(t.Error) != "null" {
			s.snapshot.LastError = &Diagnostic{Method: event.Method, At: now, Summary: "Codex Turn " + t.Status}
		}
		s.closePending("CLOSED_TURN", now)
	case "turn/plan/updated":
		var steps []PlanStep
		var explanation string
		if json.Unmarshal(p["plan"], &steps) != nil || len(steps) > 256 {
			s.diagnostic(event, "Invalid or oversized official plan", now)
			break
		}
		valid := true
		completed := 0
		for _, step := range steps {
			if len(step.Step) > 4096 {
				valid = false
			}
			switch step.Status {
			case "completed":
				completed++
			case "pending", "inProgress":
			default:
				valid = false
			}
		}
		if valid {
			if len(event.Params) > MaxPayload {
				s.diagnostic(event, "Official plan exceeds telemetry cap", now)
				break
			}
			_ = json.Unmarshal(p["explanation"], &explanation)
			s.snapshot.LatestPlan = &Plan{Steps: steps, Explanation: explanation, Completed: completed, Total: len(steps), Revision: s.snapshot.Sequence + 1}
		}
	case "item/started", "item/completed":
		var item struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		}
		if json.Unmarshal(p["item"], &item) != nil || item.ID == "" || len(item.ID) > 1024 || len(item.Type) > 128 {
			break
		}
		i := -1
		for j, v := range s.snapshot.Items {
			if v.ID == item.ID {
				i = j
				break
			}
		}
		if i < 0 {
			if len(s.snapshot.Items) >= MaxItems {
				s.snapshot.Items = s.snapshot.Items[1:]
			}
			s.snapshot.Items = append(s.snapshot.Items, Item{ID: item.ID})
			i = len(s.snapshot.Items) - 1
		}
		v := &s.snapshot.Items[i]
		if v.CompletedAt != nil && event.Method == "item/started" {
			break
		}
		v.Type = item.Type
		v.Source = "APP_SERVER"
		v.Revision = s.snapshot.Sequence + 1
		v.Status = item.Status
		if len(v.Status) > 128 {
			v.Status = "unsupported"
			v.Truncated = true
		}
		v.Unsupported = !contains(contract.ItemTypes, item.Type)
		if v.Unsupported {
			s.diagnostic(event, "Unsupported agent item type", now)
		}
		v.Metadata = itemMetadata(p["item"])
		var body struct {
			Text             *string `json:"text"`
			AggregatedOutput *string `json:"aggregatedOutput"`
		}
		_ = json.Unmarshal(p["item"], &body)
		if item.Type == "agentMessage" || item.Type == "plan" {
			if body.Text != nil {
				v.Text = tail(*body.Text, MaxOutput)
				v.Truncated = len(*body.Text) > MaxOutput
			} else if event.Method == "item/completed" {
				v.Truncated = true
				s.snapshot.CaptureCoverage.State = "GAP"
				if !contains(s.snapshot.CaptureCoverage.Reasons, "FINAL_ITEM_TEXT_MISSING") {
					s.snapshot.CaptureCoverage.Reasons = append(s.snapshot.CaptureCoverage.Reasons, "FINAL_ITEM_TEXT_MISSING")
				}
			}
		}
		if item.Type == "commandExecution" && event.Method == "item/completed" && body.AggregatedOutput != nil {
			v.OutputBytes = int64(len(*body.AggregatedOutput))
			v.OutputTail = tail(*body.AggregatedOutput, MaxOutput)
		}

		v.Truncated = v.Truncated || len(p["item"]) > MaxPayload || v.OutputBytes > MaxOutput
		// Reasoning content is not retained; lifecycle and allowed metadata suffice.
		if v.Type == "reasoning" {
			v.Metadata = raw(map[string]any{"id": item.ID, "type": item.Type})
		}
		if event.Method == "item/started" {
			v.StartedAt = &now
			if v.Status == "" {
				v.Status = "inProgress"
			}
		} else {
			v.CompletedAt = &now
			if v.Status == "" {
				v.Status = "completed"
			}
		}
		copy := *v
		s.snapshot.CurrentItem = &copy
	case "item/commandExecution/outputDelta", "item/fileChange/outputDelta", "item/agentMessage/delta", "item/plan/delta":
		var id, delta string
		_ = json.Unmarshal(p["itemId"], &id)
		_ = json.Unmarshal(p["delta"], &delta)
		if id == "" || len(id) > 1024 {
			break
		}
		found := false
		for _, item := range s.snapshot.Items {
			if item.ID == id {
				found = true
				break
			}
		}
		if !found {
			kind := "commandExecution"
			switch event.Method {
			case "item/agentMessage/delta":
				kind = "agentMessage"
			case "item/plan/delta":
				kind = "plan"
			case "item/fileChange/outputDelta":
				kind = "fileChange"
			}
			if len(s.snapshot.Items) >= MaxItems {
				s.snapshot.Items = s.snapshot.Items[1:]
			}
			s.snapshot.Items = append(s.snapshot.Items, Item{ID: id, Type: kind, Source: "APP_SERVER", Status: "unknown", Metadata: json.RawMessage(`{}`), Truncated: true})
			s.snapshot.CaptureCoverage.State = "GAP"
			if !contains(s.snapshot.CaptureCoverage.Reasons, "ITEM_DELTA_WITHOUT_CAPTURED_START") {
				s.snapshot.CaptureCoverage.Reasons = append(s.snapshot.CaptureCoverage.Reasons, "ITEM_DELTA_WITHOUT_CAPTURED_START")
			}
		}
		for i := range s.snapshot.Items {
			v := &s.snapshot.Items[i]
			if v.ID == id {
				if v.CompletedAt != nil {
					break
				}
				if event.Method == "item/agentMessage/delta" || event.Method == "item/plan/delta" {
					v.Truncated = v.Truncated || len(v.Text)+len(delta) > MaxOutput
					v.Text = tail(v.Text+delta, MaxOutput)
				} else {
					v.OutputBytes += int64(len(delta))
					v.OutputTail = tail(v.OutputTail+delta, MaxOutput)
					v.Truncated = v.Truncated || v.OutputBytes > MaxOutput
				}
				v.Revision = s.snapshot.Sequence + 1
				if s.snapshot.CurrentItem != nil && s.snapshot.CurrentItem.ID == id {
					copy := *v
					s.snapshot.CurrentItem = &copy
				}
				break
			}
		}
	case "turn/diff/updated":
		var diff string
		_ = json.Unmarshal(p["diff"], &diff)
		s.snapshot.DiffBytes = len(diff)
		s.snapshot.TurnDiff = tail(diff, MaxOutput)
		s.snapshot.DiffSource = "Agent-reported / App Server turn diff"
	case "serverRequest/resolved":
		for i := range s.snapshot.Requests {
			r := &s.snapshot.Requests[i]
			if requestKey(r.RequestID) == requestKey(p["requestId"]) && r.ThreadID == threadID {
				if r.State == "CONFIRMED_RESOLVED" || ResponseDisposition(*r) == "CONSUMED" || (r.Kind == "RUNTIME_APPROVAL" && r.State == "REJECTED" && r.Delivery == "JSONRPC_ERROR") {
					return nil
				}
				if r.Kind == "CODEX_REQUEST" && (r.State == "CANCELED" || r.State == "STALE") {
					break
				}
				r.State = "CONFIRMED_RESOLVED"
				if r.Kind == "CODEX_REQUEST" {
					r.State = "RESPONDED"
					r.Delivery = "CONSUMED"
					r.RecoveryEvidence = append(r.RecoveryEvidence, responseEvidence(*r, "SERVER_REQUEST_RESOLVED", now))
					if r.UserDecision == nil {
						r.State = "STALE"
						r.Delivery = "NOT_APPLICABLE"
					} else if r.UserDecision.Action != "RESPOND" {
						r.State = "CANCELED"
						r.Delivery = "NOT_APPLICABLE"
					}
					r.AvailableActions = nil
					r.UpdatedAt = now
				}
				r.ResolvedAt = &now
				releaseAfterSave = append(releaseAfterSave, r.RequestID)
				break
			}
		}
	case "error":
		s.snapshot.LastError = &Diagnostic{Method: event.Method, At: now, Summary: summary(p)}
	case "warning", "guardianWarning", "configWarning", "deprecationNotice":
		s.snapshot.LastWarning = &Diagnostic{Method: event.Method, At: now, Summary: summary(p)}
	default:
		if !contains(contract.Notifications, event.Method) || strings.HasPrefix(event.Method, "item/") {
			s.diagnostic(event, "Unsupported agent notification", now)
			s.snapshot.LastWarning = &Diagnostic{Method: tail(event.Method, 256), At: now, Summary: "UNSUPPORTED_DISPLAY_NOTIFICATION"}
		}
	}
	return s.save(false)
}
func (s *Session) Await(ctx context.Context, id json.RawMessage, expected ...Event) (json.RawMessage, error) {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	waiter := s.waiters[requestKey(id)]
	err := s.err
	if len(expected) > 0 {
		matches := false
		for _, request := range s.snapshot.Requests {
			if requestKey(request.RequestID) == requestKey(id) && request.Method == expected[0].Method && bytes.Equal(request.Payload, expected[0].Params) {
				matches = true
				break
			}
		}
		if !matches {
			err = ErrStale
		}
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if waiter == nil {
		return nil, ErrStale
	}
	select {
	case response, ok := <-waiter:
		if !ok {
			return nil, ErrStale
		}
		return response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// RunID returns the immutable owner of this active runtime session.
func (s *Session) RunID() string { return s.runID }

// RejectSandboxApproval records a host policy refusal, not a fabricated user
// decision. Persist it before returning a JSON-RPC error to the native caller.
func (s *Session) RejectSandboxApproval(ctx context.Context, runID string, expected Event) error {
	select {
	case <-s.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.err != nil || s.snapshot.RunID != runID {
		return ErrStale
	}
	for i := range s.snapshot.Requests {
		r := &s.snapshot.Requests[i]
		if requestKey(r.RequestID) != requestKey(expected.RequestID) {
			continue
		}
		if r.Kind != "RUNTIME_APPROVAL" || r.State != "PENDING" || r.Method != expected.Method || !bytes.Equal(r.Payload, expected.Params) || len(r.Resolution) > 0 {
			return ErrStale
		}
		before := clone(s.snapshot)
		now := time.Now().UTC()
		r.State = "REJECTED"
		r.Delivery = "JSONRPC_ERROR"
		r.ResolvedAt = &now
		r.UpdatedAt = now
		s.snapshot.LastWarning = &Diagnostic{Method: r.Method, At: now, Summary: "TASK_SANDBOX_PERMISSION_EXCEEDED: revise the formal task scope; no permission was granted"}
		if err := s.save(false); err != nil {
			s.snapshot = before
			return err
		}
		s.releaseWaiter(r.RequestID)
		return nil
	}
	return ErrStale
}
func (s *Session) resolve(ctx context.Context, in Resolution) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing.Load() || s.err != nil || in.Identity != s.snapshot.Identity {
		return Snapshot{}, ErrStale
	}
	for i := range s.snapshot.Requests {
		r := &s.snapshot.Requests[i]
		if requestKey(r.RequestID) != requestKey(in.RequestID) {
			continue
		}
		if (r.Method == "item/tool/requestUserInput" || r.Method == "mcpServer/elicitation/request") && r.Kind != "CODEX_REQUEST" {
			return Snapshot{}, ErrStale
		}
		if r.Kind == "CODEX_REQUEST" && r.UserDecision != nil && in.UserDecisionID == r.UserDecision.ID && in.PayloadSHA256 != "" && in.PayloadSHA256 != r.UserDecision.PayloadSHA256 {
			return Snapshot{}, ErrRequestConflict
		}
		if r.Kind == "RUNTIME_APPROVAL" && (in.RuntimeRequestID != "" || in.PayloadSHA256 != "") {
			if in.RuntimeRequestID != r.ID || in.PayloadSHA256 != r.PayloadSHA256 || in.ExpectedState != "PENDING" || in.UserDecisionID != "" {
				return Snapshot{}, ErrStale
			}
		}
		if r.Kind == "CODEX_REQUEST" && (ResponseDisposition(*r) == "UNKNOWN" || ResponseDisposition(*r) == "DISPATCHING") {
			return Snapshot{}, ErrResponseDispositionUnknown
		}
		if s.responseQueued[requestKey(r.RequestID)] {
			return Snapshot{}, ErrStale
		}
		if r.Kind == "CODEX_REQUEST" {
			if d := ResponseDisposition(*r); d == "UNKNOWN" || d == "DISPATCHING" {
				return Snapshot{}, ErrResponseDispositionUnknown
			}
			if in.RuntimeRequestID != r.ID || in.UserDecisionID == "" || len(in.Response) > 0 || in.ExpectedState != "USER_DECISION_RECORDED" || r.State != "USER_DECISION_RECORDED" {
				return Snapshot{}, ErrStale
			}
			d, err := s.service.UserDecision(ctx, r.RunID, in.UserDecisionID)
			if err != nil {
				return Snapshot{}, err
			}
			if d.PayloadSHA256 != in.PayloadSHA256 || d.RuntimeRequestID != r.ID {
				return Snapshot{}, ErrInvalid
			}
			in.Response, err = SerializeUserDecision(*r, d)
			if err != nil {
				return Snapshot{}, err
			}
		}
		if r.ItemID != in.ItemID || (r.State != "PENDING" && r.State != "USER_DECISION_RECORDED") || s.waiters[requestKey(r.RequestID)] == nil {
			return Snapshot{}, ErrStale
		}
		if err := validateResponse(*r, in.Response, s.readOnly); err != nil {
			return Snapshot{}, err
		}
		canonicalCancel := false
		var decision struct {
			Decision string `json:"decision"`
		}
		_ = json.Unmarshal(in.Response, &decision)
		if decision.Decision == "cancel" {
			admit, ok := ctx.Value(cancellationKey{}).(func() error)
			if !ok {
				return Snapshot{}, fmt.Errorf("%w: canonical cancellation is required", ErrInvalid)
			}
			if err := admit(); err != nil {
				return Snapshot{}, err
			}
			canonicalCancel = true
		}
		before := clone(s.snapshot)
		r.State = "INTENDED"
		if r.Kind == "CODEX_REQUEST" {
			r.State = "USER_DECISION_RECORDED"
		}
		r.Resolution = append(json.RawMessage(nil), in.Response...)
		r.Delivery = "NOT_SENT"
		if r.Kind == "CODEX_REQUEST" {
			r.Delivery = "NOT_DISPATCHED"
		}
		r.UpdatedAt = time.Now().UTC()
		if err := s.save(!canonicalCancel); err != nil {
			s.snapshot = before
			return Snapshot{}, err
		}
		select {
		case s.waiters[requestKey(r.RequestID)] <- r.Resolution:
			s.responseQueued[requestKey(r.RequestID)] = true
		default:
			return Snapshot{}, ErrStale
		}
		return clone(s.snapshot), nil
	}
	return Snapshot{}, ErrStale
}
func (s *Session) Delivered(id json.RawMessage, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for i := range s.snapshot.Requests {
		r := &s.snapshot.Requests[i]
		if requestKey(r.RequestID) == requestKey(id) {
			if r.Kind == "CODEX_REQUEST" && (r.State == "RESPONDED" || r.State == "CANCELED" || r.State == "STALE") {
				return
			}
			preEffect := err != nil && ResponseDisposition(*r) == "NOT_DISPATCHED"
			r.UpdatedAt = time.Now().UTC()
			if err != nil {
				r.Delivery = "RESPONSE_UNCERTAIN"
				if errors.Is(err, ErrStale) {
					r.Delivery = "NOT_SENT_FENCED"
				}
			} else {
				r.Delivery = "TRANSPORT_WRITTEN"
				if r.State == "INTENDED" {
					r.State = "SENT"
				}
			}
			if r.Kind == "CODEX_REQUEST" && r.State != "RESPONDED" && r.State != "CANCELED" && r.State != "STALE" {
				r.State = "RESPONDING"
				r.Delivery = "UNKNOWN"
				// A rejected invocation proves no effect only while this request
				// is still undispatched. A late fence cannot erase an earlier
				// durable attempt whose consumption remains unknown.
				if preEffect {
					r.State = "USER_DECISION_RECORDED"
					r.Delivery = "NOT_DISPATCHED"
					r.RecoveryEvidence = append(r.RecoveryEvidence, responseEvidence(*r, "PRE_EFFECT_REJECTED", r.UpdatedAt))
				} else {
					r.RecoveryEvidence = append(r.RecoveryEvidence, responseEvidence(*r, "TRANSPORT_OUTCOME_UNKNOWN", r.UpdatedAt))
				}
				r.AvailableActions = nil
			}
			_ = s.save(false)
			break
		}
	}
}
func (s *Session) Close() {
	s.closing.Store(true)
	s.service.mu.Lock()
	delete(s.service.sessions, s.connectionID)
	s.service.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		// Cancel only display workers. Existing Core/client shutdown retains process
		// and request lifecycle authority; display shutdown cannot delay it forever.
		s.projectionLost.Store(true)
		s.projectionCancel()
		s.historyCancel()
	}
}
func (s *Session) finishClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.readyOnce.Do(func() { close(s.ready) })
	if s.bound {
		if s.snapshot.TurnStatus != "completed" && s.snapshot.TurnStatus != "failed" && s.snapshot.TurnStatus != "interrupted" {
			s.snapshot.ConnectionState = "CONNECTION_LOST"
			s.closePending("CONNECTION_LOST", time.Now().UTC())
		} else {
			s.snapshot.ConnectionState = "CLOSED"
		}
		_ = s.saveProjection()
		s.service.mu.Lock()
		delete(s.service.sessions, s.snapshot.ConnectionID)
		s.service.mu.Unlock()
	}
}
func (s *Session) closePending(state string, at time.Time) {
	for i := range s.snapshot.Requests {
		r := &s.snapshot.Requests[i]
		if OpenRequest(*r) {
			previousState := r.State
			r.UpdatedAt = at
			r.State = state
			if r.Kind == "CODEX_REQUEST" {
				if state == "CONNECTION_LOST" {
					r.State = "RESPONDING"
					if r.UserDecision == nil {
						r.State = "WAITING_FOR_USER"
					} else if ResponseDisposition(*r) == "NOT_DISPATCHED" || r.Delivery == "" {
						r.State = "USER_DECISION_RECORDED"
					} else {
						r.Delivery = "UNKNOWN"
					}
				} else {
					r.State = "STALE"
					if previousState == "UNKNOWN" || previousState == "RESPONDING" {
						r.State = "RESPONDING"
						r.Delivery = "UNKNOWN"
					}
					if s.snapshot.TurnStatus == "interrupted" && r.Delivery != "UNKNOWN" {
						r.State = "CANCELED"
					}
				}
				r.AvailableActions = nil
			}
			if r.Kind != "CODEX_REQUEST" {
				r.ResolvedAt = &at
			}
			s.releaseWaiter(r.RequestID)
		}
	}
}
func (s *Session) releaseWaiter(id json.RawMessage) {
	if ch := s.waiters[requestKey(id)]; ch != nil {
		close(ch)
		delete(s.waiters, requestKey(id))
	}
}
func (s *Session) save(resolution bool) error {
	if s.batching && !resolution {
		s.dirty = true
		return nil
	}
	if s.projectionLost.Swap(false) {
		reason := "PROJECTION_QUEUE_OVERFLOW_OR_OVERSIZED_EVENT"
		if s.projectionStorageFailed.Swap(false) {
			reason = "RUNTIME_PROJECTION_STORAGE_FAILED"
		}
		if s.historyFailed.Swap(false) {
			reason = "HISTORY_STORAGE_FAILED"
		}
		if s.historyOverflow.Swap(false) {
			reason = "HISTORY_QUEUE_OVERFLOW"
		}
		s.snapshot.CaptureCoverage.State = "GAP"
		if !contains(s.snapshot.CaptureCoverage.Reasons, reason) {
			s.snapshot.CaptureCoverage.Reasons = append(s.snapshot.CaptureCoverage.Reasons, reason)
		}
	}

	s.snapshot.WaitingState = "RUNNING"
	for _, r := range s.snapshot.Requests {
		if OpenRequest(r) {
			s.snapshot.WaitingState = r.WaitingState
			break
		}
	}
	if s.snapshot.ConnectionState == "CONNECTION_LOST" {
		s.snapshot.WaitingState = "CONNECTION_LOST"
	}
	ApplyRequestProjections(&s.snapshot)
	expected := s.snapshot.Sequence
	s.snapshot.Sequence++
	s.snapshot.UpdatedAt = time.Now().UTC()
	writeCtx := s.ctx
	cancel := func() {}
	if s.projectionWrite {
		writeCtx, cancel = context.WithTimeout(s.projectionCtx, 150*time.Millisecond)
	}
	defer cancel()
	if err := s.service.store.Save(writeCtx, expected, s.snapshot, resolution); err != nil {
		s.snapshot.Sequence = expected
		return err
	}
	s.enqueueHistory()
	return nil
}
func (s *Session) saveProjection() error {
	s.projectionWrite = true
	defer func() { s.projectionWrite = false }()
	return s.save(false)
}
func (s *Session) diagnostic(event Event, message string, at time.Time) {
	observability.Event(s.ctx, slog.LevelWarn, "codex", "codex.runtime.diagnostic", tail(message, 1024), map[string]any{"method": event.Method, "runId": s.snapshot.RunID, "threadId": s.snapshot.ThreadID, "turnId": s.snapshot.TurnID})
	s.snapshot.Diagnostics = append(s.snapshot.Diagnostics, Diagnostic{Method: event.Method, At: at, Summary: tail(message, 1024)})
	if len(s.snapshot.Diagnostics) > 16 {
		s.snapshot.Diagnostics = s.snapshot.Diagnostics[1:]
	}
}
func summary(p map[string]json.RawMessage) string {
	if b := p["error"]; len(b) > 0 {
		var nested map[string]json.RawMessage
		if json.Unmarshal(b, &nested) == nil {
			return summary(nested)
		}
	}
	for _, k := range []string{"message", "summary"} {
		var v string
		if json.Unmarshal(p[k], &v) == nil && v != "" {
			return tail(v, 2048)
		}
	}
	return "Codex reported an error or warning"
}
func tail(v string, limit int) string {
	if len(v) > limit {
		v = v[len(v)-limit:]
	}
	for len(v) > 0 && !utf8.ValidString(v) {
		v = v[1:]
	}
	return v
}
func boundedMetadata(v json.RawMessage) json.RawMessage {
	if len(v) <= MaxPayload {
		return append(json.RawMessage(nil), v...)
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(v, &m)
	result := map[string]any{"truncated": true, "originalBytes": len(v)}
	for _, k := range []string{"id", "type", "status", "command", "cwd", "commandActions", "exitCode", "durationMs", "codexErrorInfo", "additionalDetails", "message"} {
		if b := m[k]; len(b) > 0 && len(b) < 8192 {
			result[k] = b
		}
	}
	return raw(result)
}
func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
func clone(v Snapshot) Snapshot {
	var c Snapshot
	_ = json.Unmarshal(raw(v), &c)
	ApplyRequestProjections(&c)
	return c
}

// WithCanonicalCancellation delegates command/file "cancel" to the existing
// authoritative cancellation admission before any Codex response is dispatched.
type cancellationKey struct{}

func WithCanonicalCancellation(ctx context.Context, admit func() error) context.Context {
	return context.WithValue(ctx, cancellationKey{}, admit)
}

func itemMetadata(value json.RawMessage) json.RawMessage {
	var item map[string]json.RawMessage
	_ = json.Unmarshal(value, &item)
	if string(item["type"]) != `"fileChange"` {
		allowed := map[string]json.RawMessage{}
		for _, key := range []string{"id", "type", "status", "command", "commandActions", "cwd", "exitCode", "durationMs", "phase"} {
			if v := item[key]; len(v) > 0 && len(v) <= 4096 {
				allowed[key] = v
			}
		}
		return raw(allowed)
	}
	var changes []struct {
		Path string          `json:"path"`
		Kind json.RawMessage `json:"kind"`
		Diff string          `json:"diff"`
	}
	_ = json.Unmarshal(item["changes"], &changes)
	summaries := []map[string]any{}
	size := 0
	for i, c := range changes {
		if i == 128 {
			break
		}
		entry := map[string]any{"path": tail(c.Path, 4096), "kind": fileChangeKind(c.Kind), "diffBytes": len(c.Diff), "diff": tail(c.Diff, 8192), "truncated": len(c.Diff) > 8192, "source": "APP_SERVER"}
		size += len(raw(entry))
		if size > MaxPayload/2-1024 {
			break
		}
		summaries = append(summaries, entry)
	}
	return boundedMetadata(raw(map[string]any{"id": item["id"], "type": item["type"], "status": item["status"], "changes": summaries, "changeCount": len(changes), "truncated": len(summaries) < len(changes), "source": "App Server file change telemetry"}))
}

// BeforeDispatch rechecks ownership on the writer side of the response queue.
// A stale/cleaned request is never replayed to a later Turn.
func (s *Session) BeforeDispatch(id json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.closing.Load() || s.err != nil {
		return ErrStale
	}
	for i := range s.snapshot.Requests {
		r := &s.snapshot.Requests[i]
		if requestKey(r.RequestID) == requestKey(id) {
			if r.State != "INTENDED" && r.State != "USER_DECISION_RECORDED" {
				return ErrStale
			}
			var decision struct {
				Decision string `json:"decision"`
			}
			_ = json.Unmarshal(r.Resolution, &decision)
			if r.Kind == "CODEX_REQUEST" && (r.UserDecision == nil || r.UserDecision.Action != "RESPOND") {
				return ErrStale
			}
			before := clone(s.snapshot)
			if r.Kind == "CODEX_REQUEST" {
				r.State = "RESPONDING"
			}
			r.Delivery = "DISPATCHING"
			r.UpdatedAt = time.Now().UTC()
			if r.Kind == "CODEX_REQUEST" {
				r.DispatchAttempts = append(r.DispatchAttempts, newResponseDispatchAttempt(*r))
			}
			if err := s.save(decision.Decision != "cancel"); err != nil {
				s.snapshot = before
				return err
			}
			return nil
		}
	}
	return ErrStale
}

func sessionName(name string) string {
	if len(name) > 2048 {
		name = name[:2048]
	}
	return strings.ToValidUTF8(name, "")
}
func (s *Session) SetSessionName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.Agent.SessionName = sessionName(name)
}

func fileChangeKind(value json.RawMessage) json.RawMessage {
	var shape struct {
		Type     string  `json:"type"`
		MovePath *string `json:"movePath,omitempty"`
	}
	if json.Unmarshal(value, &shape) != nil {
		return json.RawMessage(`{"type":"unsupported"}`)
	}
	if len(shape.Type) > 64 {
		shape.Type = "unsupported"
	}
	if shape.MovePath != nil {
		path := tail(*shape.MovePath, 4096)
		shape.MovePath = &path
	}
	return raw(shape)
}

// Flush drains already queued observations without closing the session. Late
// exact resolved receipts remain observable while the workflow owner is held.
func (s *Session) Flush(ctx context.Context) error {
	barrier := make(chan struct{})
	select {
	case s.events <- Event{barrier: barrier}:
	case <-s.done:
		return ErrStale
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-s.done:
		return ErrStale
	case <-ctx.Done():
		return ctx.Err()
	}
}
