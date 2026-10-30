package agentruntime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestResponseDispositionLateExactReceiptAndTerminalObservation(t *testing.T) {
	m := &decisionMemoryStore{}
	svc := NewService(m)
	s := svc.NewTurn(context.Background(), "run", "thread", false)
	defer s.Close()
	if e := s.Bind("turn"); e != nil {
		t.Fatal(e)
	}
	r := addRequest(t, s, "item/tool/requestUserInput")
	in := UserDecisionInput{Identity: r.Identity, RuntimeRequestID: r.ID, SubmissionRequestID: "explicit-click", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "unchanged original answer", SourceSurface: "TEST", SubmittedBy: "user"}
	in.PayloadSHA256 = DecisionPayloadSHA256(in)
	d, e := svc.SubmitUserDecision(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	cmd := Resolution{Identity: r.Identity, RuntimeRequestID: r.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: r.RequestID, ItemID: r.ItemID}
	if _, e = svc.Resolve(context.Background(), cmd); e != nil {
		t.Fatal(e)
	}
	if e = s.BeforeDispatch(r.RequestID); e != nil {
		t.Fatal(e)
	}
	s.Delivered(r.RequestID, nil)
	for _, event := range []Event{{Method: "serverRequest/resolved", Params: raw(map[string]any{"threadId": "other", "requestId": "server-id"})}, {Method: "serverRequest/resolved", Params: raw(map[string]any{"threadId": "thread", "requestId": "other"})}, {Method: "serverRequest/resolved", Params: raw(map[string]any{"threadId": "thread", "turnId": "other", "requestId": "server-id"})}, {Method: "turn/completed", Params: raw(map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}})}} {
		if e = s.Observe(event); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = s.Flush(ctx); e != nil {
		t.Fatal(e)
	}
	v, e := svc.Read(ctx, "run")
	if e != nil {
		t.Fatal(e)
	}
	q := v.Requests[0]
	if q.State != "RESPONDING" || q.Delivery != "UNKNOWN" || !HasUnresolvedResponse(v) || q.ResolvedAt != nil {
		t.Fatalf("terminal inferred consumption: %+v", q)
	}
	if q.Projection.ReasonCode != "CODEX_RESPONSE_RESOLUTION_READBACK_UNAVAILABLE" || q.Projection.ResendAllowed || !reflect.DeepEqual(q.Projection.AvailableActions, []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}) {
		t.Fatalf("unknown projection: %+v", q.Projection)
	}
	if _, e = svc.Resolve(ctx, cmd); !errors.Is(e, ErrResponseDispositionUnknown) {
		t.Fatalf("resend %v", e)
	}
	if len(q.DispatchAttempts) != 1 || q.DispatchAttempts[0].UserDecisionID != d.ID {
		t.Fatal("intent lost")
	}

	if e = s.Observe(Event{Method: "turn/completed", Params: raw(map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "interrupted"}})}); e != nil {
		t.Fatal(e)
	}
	if e = s.Flush(ctx); e != nil {
		t.Fatal(e)
	}
	interrupted, e := svc.Read(ctx, "run")
	if e != nil || !HasUnresolvedResponse(interrupted) || interrupted.Requests[0].State != "RESPONDING" {
		t.Fatal("unsolicited interrupted bypassed canonical cancellation", e)
	}
	if e = s.Observe(Event{Method: "serverRequest/resolved", Params: raw(map[string]any{"threadId": "thread", "requestId": "server-id"})}); e != nil {
		t.Fatal(e)
	}
	// A delayed transport-write callback cannot undo the exact consumed receipt.
	s.Delivered(r.RequestID, nil)
	v, e = svc.Read(ctx, "run")
	q = v.Requests[0]
	if e != nil || q.State != "RESPONDED" || q.Delivery != "CONSUMED" || HasUnresolvedResponse(v) || q.ResolvedAt == nil || len(q.DispatchAttempts) != 1 || q.UserDecision.RawText != in.RawText {
		t.Fatalf("late receipt %+v %v", q, e)
	}
}

func TestResponseDispositionCompatibilityAndCancellation(t *testing.T) {
	d := &UserDecision{UserDecisionInput: UserDecisionInput{Action: "RESPOND", RawText: "original"}}
	for _, tc := range []struct {
		name         string
		r            Request
		want, reason string
	}{
		{"unanswered", Request{Kind: "CODEX_REQUEST", State: "UNKNOWN"}, "NOT_APPLICABLE", "LEGACY_RECOVERY_REQUIRED"},
		{"unproven legacy", Request{Kind: "CODEX_REQUEST", State: "UNKNOWN", UserDecision: d}, "NOT_APPLICABLE", "LEGACY_RECOVERY_REQUIRED"},
		{"legacy written", Request{Kind: "CODEX_REQUEST", State: "UNKNOWN", Delivery: "TRANSPORT_WRITTEN", UserDecision: d}, "UNKNOWN", "CODEX_RESPONSE_RESOLUTION_READBACK_UNAVAILABLE"},
		{"pre effect", Request{Kind: "CODEX_REQUEST", State: "USER_DECISION_RECORDED", Delivery: "NOT_SENT", UserDecision: d}, "NOT_DISPATCHED", ""},
		{"legacy dispatch unknown", Request{Kind: "CODEX_REQUEST", State: "UNKNOWN", Delivery: "DISPATCHING", UserDecision: d}, "UNKNOWN", "CODEX_RESPONSE_RESOLUTION_READBACK_UNAVAILABLE"},
		{"legacy pre effect unknown", Request{Kind: "CODEX_REQUEST", State: "UNKNOWN", Delivery: "NOT_SENT", UserDecision: d}, "NOT_DISPATCHED", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ProjectRequest(tc.r, nil, "completed")
			if p.ResponseDisposition != tc.want || p.ReasonCode != tc.reason || p.ResendAllowed {
				t.Fatalf("projection %+v", p)
			}
		})
	}
	for _, tc := range []struct{ delivery, wantState, wantDisposition string }{{"DISPATCHING", "RESPONDING", "UNKNOWN"}, {"NOT_SENT", "USER_DECISION_RECORDED", "NOT_DISPATCHED"}} {
		legacy := Request{Kind: "CODEX_REQUEST", State: "UNKNOWN", Delivery: tc.delivery, UserDecision: d}
		NormalizeRequest(&legacy)
		if legacy.State != tc.wantState || legacy.Delivery != tc.wantDisposition {
			t.Fatalf("legacy normalization %+v", legacy)
		}
	}
	r := Request{Kind: "CODEX_REQUEST", State: "CANCELED", Delivery: "UNKNOWN", UserDecision: d}
	NormalizeRequest(&r)
	if r.State != "CANCELED" || r.Delivery != "UNKNOWN" || HasUnresolvedResponse(Snapshot{Requests: []Request{r}}) || len(ProjectRequest(r, nil, "interrupted").AvailableActions) != 0 {
		t.Fatal("cancellation rewrote answer consumption")
	}
}
