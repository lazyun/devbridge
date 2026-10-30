package agentruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func recordedDispatchFixture(t *testing.T) (*requestFailureStore, *Service, *Session, Request, Resolution) {
	t.Helper()
	m := &requestFailureStore{}
	svc := NewService(m)
	s := svc.NewTurn(context.Background(), "run", "thread", false)
	t.Cleanup(s.Close)
	if err := s.Bind("turn"); err != nil {
		t.Fatal(err)
	}
	r := addRequest(t, s, "item/tool/requestUserInput")
	in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "dispatch-test", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "exact answer", SourceSurface: "TEST", SubmittedBy: "user"}
	in.PayloadSHA256 = DecisionPayloadSHA256(in)
	d, err := svc.SubmitUserDecision(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return m, svc, s, r, Resolution{Identity: r.Identity, RuntimeRequestID: r.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: r.RequestID, ItemID: r.ItemID}
}

func TestLateDispatchFenceCannotEraseUnknownAttempt(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(map[bool]string{false: "dispatching", true: "unknown"}[delivered], func(t *testing.T) {
			_, svc, s, r, cmd := recordedDispatchFixture(t)
			if _, err := svc.Resolve(context.Background(), cmd); err != nil {
				t.Fatal(err)
			}
			if err := s.BeforeDispatch(r.RequestID); err != nil {
				t.Fatal(err)
			}
			if delivered {
				s.Delivered(r.RequestID, nil)
			}
			// A second, fenced invocation proves only that invocation did not
			// write. It cannot establish non-consumption of the earlier attempt.
			err := s.BeforeDispatch(r.RequestID)
			if !errors.Is(err, ErrStale) {
				t.Fatalf("duplicate dispatch was not fenced: %v", err)
			}
			s.Delivered(r.RequestID, err)
			state, err := svc.Read(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			q := state.Requests[0]
			if ResponseDisposition(q) != "UNKNOWN" || len(q.DispatchAttempts) != 1 {
				t.Fatalf("late fence erased prior uncertain attempt: disposition=%s attempts=%d", ResponseDisposition(q), len(q.DispatchAttempts))
			}
			for _, proof := range q.RecoveryEvidence {
				if proof.Kind == "PRE_EFFECT_REJECTED" {
					t.Fatal("late fence manufactured retry authority for an earlier attempt")
				}
			}
			if _, err := svc.Resolve(context.Background(), cmd); !errors.Is(err, ErrResponseDispositionUnknown) {
				t.Fatalf("uncertain answer became retryable: %v", err)
			}
		})
	}
}

func TestResponseIntentAndDispatchFailureRestoreSnapshot(t *testing.T) {
	for _, phase := range []string{"intent", "dispatch"} {
		t.Run(phase, func(t *testing.T) {
			m, svc, s, r, cmd := recordedDispatchFixture(t)
			if phase == "dispatch" {
				if _, err := svc.Resolve(context.Background(), cmd); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			before := clone(s.snapshot)
			s.mu.Unlock()
			durableBefore, _ := svc.Read(context.Background(), "run")
			m.fail.Store(true)
			var err error
			if phase == "intent" {
				_, err = svc.Resolve(context.Background(), cmd)
			} else {
				err = s.BeforeDispatch(r.RequestID)
			}
			if !errors.Is(err, errRequestSave) {
				t.Fatalf("write fault=%v", err)
			}
			s.mu.Lock()
			after := clone(s.snapshot)
			s.mu.Unlock()
			if !reflect.DeepEqual(before, after) {
				t.Error("failed send admission changed volatile authority")
			}
			durable, _ := svc.Read(context.Background(), "run")
			if !reflect.DeepEqual(durableBefore, durable) {
				t.Fatal("failed admission changed durable authority")
			}
			if phase == "intent" {
				if _, err = svc.Resolve(context.Background(), cmd); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = s.BeforeDispatch(r.RequestID); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
