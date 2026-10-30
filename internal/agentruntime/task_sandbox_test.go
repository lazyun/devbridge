package agentruntime

import (
	"context"
	"errors"
	"testing"
)

func TestTaskSandboxRefusalSaveFailureAllowsExactRetry(t *testing.T) {
	m := &requestFailureStore{}
	svc := NewService(m)
	s := svc.NewTurn(context.Background(), "run", "thread", false)
	defer s.Close()
	if err := s.Bind("turn"); err != nil {
		t.Fatal(err)
	}
	r := addRequest(t, s, "item/commandExecution/requestApproval")
	e := Event{Method: r.Method, RequestID: r.RequestID, Params: r.Payload}
	m.fail.Store(true)
	if err := s.RejectSandboxApproval(context.Background(), "run", e); !errors.Is(err, errRequestSave) {
		t.Fatal(err)
	}
	v, err := svc.Read(context.Background(), "run")
	if err != nil || v.Requests[0].State != "PENDING" {
		t.Fatal("failed persistence published refusal")
	}
	if err := s.RejectSandboxApproval(context.Background(), "run", e); err != nil {
		t.Fatal(err)
	}
}

func TestTaskSandboxRefusalClosesWaitWithoutUserDecision(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval"} {
		t.Run(method, func(t *testing.T) {
			service, s, _ := newSession(t)
			r := addRequest(t, s, method)
			if err := s.RejectSandboxApproval(context.Background(), "run", Event{Method: method, RequestID: r.RequestID, Params: r.Payload}); err != nil {
				t.Fatal(err)
			}
			if err := s.Observe(Event{Method: "serverRequest/resolved", Params: raw(map[string]any{"threadId": r.ThreadID, "requestId": r.RequestID})}); err != nil {
				t.Fatal(err)
			}
			v, err := service.Read(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			if v.WaitingState != "RUNNING" || v.Requests[0].State != "REJECTED" || v.Requests[0].UserDecision != nil || len(v.Requests[0].Resolution) != 0 || v.LastWarning == nil {
				t.Fatalf("false user decision or permanent wait: %+v", v)
			}
			if err = s.RejectSandboxApproval(context.Background(), "run", Event{Method: method, RequestID: r.RequestID, Params: r.Payload}); err == nil {
				t.Fatal("closed approval processed twice")
			}
		})
	}
}

func TestTaskSandboxRefusalRequiresExactRunAndPayload(t *testing.T) {
	service, s, _ := newSession(t)
	r := addRequest(t, s, "item/commandExecution/requestApproval")
	if s.RejectSandboxApproval(context.Background(), "foreign", Event{Method: r.Method, RequestID: r.RequestID, Params: r.Payload}) == nil {
		t.Fatal("foreign Run denied")
	}
	if s.RejectSandboxApproval(context.Background(), "run", Event{Method: r.Method, RequestID: r.RequestID, Params: raw(map[string]any{})}) == nil {
		t.Fatal("different request denied")
	}
	v, _ := service.Read(context.Background(), "run")
	if v.Requests[0].State != "PENDING" {
		t.Fatal("unrelated request changed")
	}
}
