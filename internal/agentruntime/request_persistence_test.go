package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
)

var errRequestSave = errors.New("injected request save failure")

type requestFailureStore struct {
	decisionMemoryStore
	fail atomic.Bool
}

func (m *requestFailureStore) Save(ctx context.Context, expected uint64, v Snapshot, resolution bool) error {
	if m.fail.Swap(false) {
		return errRequestSave
	}
	return m.memoryStore.Save(ctx, expected, v, resolution)
}
func (m *requestFailureStore) BindAppServer(context.Context, string, appserver.Binding) error {
	return nil
}
func (m *requestFailureStore) AppServerBinding(context.Context, string) (appserver.Binding, error) {
	return appserver.Binding{}, nil
}
func (m *requestFailureStore) Reattach(_ context.Context, id Identity, _ appserver.Binding) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Model the durable store's identity adoption; the tests below exercise the
	// Session's actual replay, persistence, waiter and dispatch code.
	v := clone(m.state)
	v.ConnectionID = id.ConnectionID
	v.RuntimeEpoch = "reattached-epoch"
	for i := range v.Requests {
		r := &v.Requests[i]
		original := r.Identity
		r.OriginalIdentity = &original
		r.Identity = v.Identity
		r.PendingReplayVerified = false
	}
	m.state = clone(v)
	return v, nil
}
func assertRequestNotPublished(t *testing.T, s *Session, event Event, before Snapshot) {
	t.Helper()
	s.mu.Lock()
	current := clone(s.snapshot)
	s.mu.Unlock()
	if !reflect.DeepEqual(current, before) {
		t.Error("failed request save changed the session snapshot")
	}
	durable, err := s.service.Read(context.Background(), "run")
	if err != nil || !reflect.DeepEqual(durable, before) {
		t.Errorf("failed request save changed durable state: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Await(ctx, event.RequestID, event); !errors.Is(err, ErrStale) {
		t.Errorf("failed request save published a waiter: %v", err)
	}
}
func TestInitialRequestSaveFailureAllowsExactRetry(t *testing.T) {
	for _, method := range []string{"item/tool/requestUserInput", "item/commandExecution/requestApproval"} {
		t.Run(method, func(t *testing.T) {
			m := &requestFailureStore{}
			svc := NewService(m)
			s := svc.NewTurn(context.Background(), "run", "thread", false)
			defer s.Close()
			if err := s.Bind("turn"); err != nil {
				t.Fatal(err)
			}
			before, _ := svc.Read(context.Background(), "run")
			event := Event{Method: method, RequestID: raw("server-id"), Params: requestFixture(method)}
			m.fail.Store(true)
			if err := s.Observe(event); !errors.Is(err, errRequestSave) {
				t.Fatal(err)
			}
			assertRequestNotPublished(t, s, event, before)
			if err := s.Observe(event); err != nil {
				t.Fatal(err)
			}
			after, _ := svc.Read(context.Background(), "run")
			if len(after.Requests) != 1 || after.Sequence != before.Sequence+1 {
				t.Fatal("retry did not persist the request exactly once")
			}
		})
	}
}

func TestRequestLifecycleReceiptSaveFailureIsAtomic(t *testing.T) {
	for _, kind := range []string{"approval", "answered"} {
		for _, notification := range []string{"serverRequest/resolved", "bridge/requestRejected"} {
			t.Run(kind+"/"+notification, func(t *testing.T) {
				m := &requestFailureStore{}
				svc := NewService(m)
				s := svc.NewTurn(context.Background(), "run", "thread", false)
				defer s.Close()
				if err := s.Bind("turn"); err != nil {
					t.Fatal(err)
				}
				method := "item/commandExecution/requestApproval"
				if kind == "answered" {
					method = "item/tool/requestUserInput"
				}
				r := addRequest(t, s, method)
				if kind == "answered" {
					in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "receipt-test", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "exact answer", SourceSurface: "TEST", SubmittedBy: "user"}
					in.PayloadSHA256 = DecisionPayloadSHA256(in)
					d, err := svc.SubmitUserDecision(context.Background(), in)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = svc.Resolve(context.Background(), Resolution{Identity: r.Identity, RuntimeRequestID: r.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: r.RequestID, ItemID: r.ItemID}); err != nil {
						t.Fatal(err)
					}
					if notification == "serverRequest/resolved" {
						if err = s.BeforeDispatch(r.RequestID); err != nil {
							t.Fatal(err)
						}
						s.Delivered(r.RequestID, nil)
					}
				}
				s.mu.Lock()
				before := clone(s.snapshot)
				waiter := s.waiters[requestKey(r.RequestID)]
				s.mu.Unlock()
				durableBefore, _ := svc.Read(context.Background(), "run")
				event := Event{Method: notification, ReceivedAt: time.Now(), Params: raw(map[string]any{"threadId": "thread", "requestId": "server-id", "rejectedMethod": method, "message": "rejected"})}
				m.fail.Store(true)
				if err := s.Observe(event); !errors.Is(err, errRequestSave) {
					t.Fatalf("injected persistence error=%v", err)
				}
				s.mu.Lock()
				unchanged, retained := reflect.DeepEqual(s.snapshot, before), s.waiters[requestKey(r.RequestID)] == waiter && waiter != nil
				s.mu.Unlock()
				if !unchanged || !retained {
					t.Errorf("uncommitted receipt changed snapshot=%v or released waiter=%v", !unchanged, !retained)
				}
				durable, err := svc.Read(context.Background(), "run")
				if err != nil || !reflect.DeepEqual(durable, durableBefore) {
					t.Fatalf("failed write changed durable request: %v", err)
				}
				if err = s.Observe(event); err != nil {
					t.Fatal(err)
				}
				committed, _ := svc.Read(context.Background(), "run")
				if committed.Sequence != before.Sequence+1 || committed.Requests[0].ResolvedAt == nil {
					t.Fatal("retry did not commit exactly once")
				}
				if err = s.Observe(event); err != nil {
					t.Fatal(err)
				}
				repeated, _ := svc.Read(context.Background(), "run")
				if !reflect.DeepEqual(committed.Requests, repeated.Requests) {
					t.Error("repeated receipt rewrote terminal request evidence")
				}
			})
		}
	}
}
func TestReplayedRequestSaveFailureAllowsExactRetry(t *testing.T) {
	for _, mode := range []string{"waiting", "recorded", "unknown", "approval"} {
		t.Run(mode, func(t *testing.T) {
			m := &requestFailureStore{}
			svc := NewService(m)
			s := svc.NewTurn(context.Background(), "run", "thread", false)
			if err := s.Bind("turn"); err != nil {
				t.Fatal(err)
			}
			method := "item/tool/requestUserInput"
			if mode == "approval" {
				method = "item/commandExecution/requestApproval"
			}
			r := addRequest(t, s, method)
			var want json.RawMessage
			if mode == "recorded" || mode == "unknown" {
				in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "click", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "exact answer", SourceSurface: "TEST", SubmittedBy: "user"}
				in.PayloadSHA256 = DecisionPayloadSHA256(in)
				d, err := svc.SubmitUserDecision(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				want, err = SerializeUserDecision(r, d)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "unknown" {
					_, err = svc.Resolve(context.Background(), Resolution{Identity: r.Identity, RuntimeRequestID: r.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: r.RequestID, ItemID: r.ItemID})
					if err != nil {
						t.Fatal(err)
					}
					if err := s.BeforeDispatch(r.RequestID); err != nil {
						t.Fatal(err)
					}
					s.Delivered(r.RequestID, nil)
				}
			}
			// A crashed owner cannot write its shutdown projection. Keep the
			// last durable pending request, including a pending approval.
			m.fail.Store(true)
			s.Close()
			restored, err := svc.RestoreTurn(context.Background(), "run", "thread", "turn", appserver.Binding{}, false)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			before, _ := svc.Read(context.Background(), "run")
			event := Event{Method: method, RequestID: r.RequestID, Params: r.Payload}
			m.fail.Store(true)
			if err := restored.Observe(event); !errors.Is(err, errRequestSave) {
				t.Errorf("expected replay save failure, got %v", err)
			}
			assertRequestNotPublished(t, restored, event, before)
			if t.Failed() {
				return
			}
			if err := restored.Observe(event); err != nil {
				t.Fatal(err)
			}
			after, _ := svc.Read(context.Background(), "run")
			if !after.Requests[0].PendingReplayVerified || after.Sequence != before.Sequence+1 {
				t.Fatal("retry did not persist exact replay proof")
			}
			if err := restored.Observe(event); err != nil {
				t.Fatal(err)
			}
			duplicate, _ := svc.Read(context.Background(), "run")
			if !reflect.DeepEqual(after, duplicate) {
				t.Fatal("duplicate replay changed durable state")
			}
			if want != nil {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				got, err := restored.Await(ctx, r.RequestID, event)
				if err != nil || string(got) != string(want) {
					t.Fatalf("saved answer not queued: %s %v", got, err)
				}
				if len(after.Requests[0].RecoveryEvidence) != len(before.Requests[0].RecoveryEvidence)+1 {
					t.Fatal("failed attempt leaked recovery evidence")
				}
				restored.mu.Lock()
				pending := len(restored.waiters[requestKey(r.RequestID)])
				restored.mu.Unlock()
				if pending != 0 {
					t.Fatal("duplicate answer queued")
				}
				if err := restored.BeforeDispatch(r.RequestID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestReplayedStopDecisionDoesNotPublishWaiter(t *testing.T) {
	for _, action := range []string{"STOP_AND_REVISE", "CANCEL_OPERATION"} {
		t.Run(action, func(t *testing.T) {
			m := &requestFailureStore{}
			svc := NewService(m)
			s := svc.NewTurn(context.Background(), "run", "thread", false)
			if err := s.Bind("turn"); err != nil {
				t.Fatal(err)
			}
			r := addRequest(t, s, "item/tool/requestUserInput")
			in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "stop", ExpectedState: "WAITING_FOR_USER", Action: action, SourceSurface: "TEST", SubmittedBy: "user"}
			in.PayloadSHA256 = DecisionPayloadSHA256(in)
			if _, err := svc.SubmitUserDecision(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			s.Close()
			restored, err := svc.RestoreTurn(context.Background(), "run", "thread", "turn", appserver.Binding{}, false)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			before, _ := svc.Read(context.Background(), "run")
			event := Event{Method: r.Method, RequestID: r.RequestID, Params: r.Payload}
			for i := 0; i < 2; i++ {
				if err := restored.Observe(event); !errors.Is(err, ErrStale) {
					t.Errorf("stop decision replay admitted: %v", err)
				}
				assertRequestNotPublished(t, restored, event, before)
			}
		})
	}
}
