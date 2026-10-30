package approval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/observability"
)

type approvalLogStore struct{ value Approval }

func (s *approvalLogStore) CreatePending(_ context.Context, value Approval, now time.Time) (Approval, error) {
	value.CreatedAt = now
	s.value = value
	return value, nil
}
func (s *approvalLogStore) Get(context.Context, string) (Approval, error) { return s.value, nil }
func (s *approvalLogStore) ListByRun(context.Context, string) ([]Approval, error) {
	return []Approval{s.value}, nil
}
func (s *approvalLogStore) Decide(_ context.Context, _ string, decision Decision, now time.Time) (Approval, error) {
	s.value.Status, s.value.Decision, s.value.DecidedAt = StatusDecided, &decision, &now
	return s.value, nil
}
func (s *approvalLogStore) MarkDelivered(_ context.Context, _ string, now time.Time) (Approval, error) {
	s.value.Status, s.value.DeliveredAt = StatusDelivered, &now
	return s.value, nil
}
func (*approvalLogStore) FailRun(context.Context, string, string, string, time.Time) error {
	return nil
}

type approvalLogResolver struct{}

func (approvalLogResolver) ResolveApproval(context.Context, string, codex.ApprovalDecision) error {
	return nil
}

type supersedingApprovalLogStore struct{ values map[string]Approval }

func (s *supersedingApprovalLogStore) CreatePending(context.Context, Approval, time.Time) (Approval, error) {
	return Approval{}, errors.New("unused")
}
func (s *supersedingApprovalLogStore) Get(_ context.Context, id string) (Approval, error) {
	return s.values[id], nil
}
func (s *supersedingApprovalLogStore) ListByRun(context.Context, string) ([]Approval, error) {
	return []Approval{s.values["approval-selected"], s.values["approval-other"]}, nil
}
func (s *supersedingApprovalLogStore) Decide(_ context.Context, id string, decision Decision, now time.Time) (Approval, error) {
	selected := s.values[id]
	selected.Status, selected.Decision, selected.DecidedAt = StatusDecided, &decision, &now
	s.values[id] = selected
	other := s.values["approval-other"]
	other.Status = StatusCanceled
	s.values[other.ID] = other
	return selected, nil
}
func (s *supersedingApprovalLogStore) MarkDelivered(_ context.Context, id string, now time.Time) (Approval, error) {
	selected := s.values[id]
	selected.Status, selected.DeliveredAt = StatusDelivered, &now
	s.values[id] = selected
	return selected, nil
}
func (*supersedingApprovalLogStore) FailRun(context.Context, string, string, string, time.Time) error {
	return nil
}

func TestApprovalOperationalOrdering(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	now := time.Unix(100, 0)
	store := &approvalLogStore{}
	service := NewService(store, func() time.Time { now = now.Add(time.Second); return now })
	unregister, err := service.Register("run-a", approvalLogResolver{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	created, err := service.Request(ctx, "run-a", codex.ApprovalRequest{ID: "approval-a", Kind: codex.ApprovalKindCommandExecution, RequestPayload: json.RawMessage(`{"command":"go test ./...","password":"never-log"}`), AllowedDecisions: []codex.DecisionType{codex.DecisionApproveOnce}})
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := service.Decide(ctx, created.ID, Decision{Type: DecisionApproveOnce})
	if err != nil || delivered.Status != StatusDelivered {
		t.Fatalf("delivered=%+v err=%v", delivered, err)
	}
	events := approvalLogEvents(t, output.Bytes())
	want := []string{"approval.created", "run.waiting_approval", "approval.decided", "approval.delivered", "run.running"}
	if len(events) != len(want) {
		t.Fatalf("events=%v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events=%v want=%v", events, want)
		}
	}
	if bytes.Contains(output.Bytes(), []byte("never-log")) || bytes.Contains(output.Bytes(), []byte("go test")) {
		t.Fatalf("approval payload leaked: %s", output.Bytes())
	}
}

func TestApprovalDeliveryFailureIsLoggedAfterDecision(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	store := &approvalLogStore{value: Approval{ID: "approval-a", RunID: "run-a", Status: StatusPending, Kind: KindCommandExecution, CreatedAt: time.Unix(1, 0), AllowedDecisions: []DecisionType{DecisionDecline}}}
	service := NewService(store, nil)
	_, err := service.Decide(ctx, "approval-a", Decision{Type: DecisionDecline})
	var delivery *DeliveryError
	if !errors.As(err, &delivery) {
		t.Fatalf("expected delivery error, got %v", err)
	}
	events := approvalLogEvents(t, output.Bytes())
	if len(events) != 3 || events[0] != "approval.decided" || events[1] != "run.infrastructure_failed" || events[2] != "approval.delivery_failed" {
		t.Fatalf("events=%v", events)
	}
}

func TestApprovalCancelLogsActualSupersededApprovalAndRunCancellation(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	created := time.Unix(1, 0)
	store := &supersedingApprovalLogStore{values: map[string]Approval{
		"approval-selected": {ID: "approval-selected", RunID: "run-a", Status: StatusPending, Kind: KindCommandExecution, CreatedAt: created, AllowedDecisions: []DecisionType{DecisionCancel}},
		"approval-other":    {ID: "approval-other", RunID: "run-a", Status: StatusPending, Kind: KindFileChange, CreatedAt: created},
	}}
	service := NewService(store, func() time.Time { return created.Add(time.Second) })
	cancelCalls := 0
	unregister, err := service.Register("run-a", approvalLogResolver{}, func() { cancelCalls++ })
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	if _, err := service.Decide(ctx, "approval-selected", Decision{Type: DecisionCancel}); err != nil {
		t.Fatal(err)
	}
	if cancelCalls != 1 {
		t.Fatalf("legacy standalone cancellation calls=%d", cancelCalls)
	}
	events := approvalLogEvents(t, output.Bytes())
	want := []string{"approval.decided", "approval.superseded", "approval.canceled", "approval.delivered", "run.canceled"}
	if len(events) != len(want) {
		t.Fatalf("events=%v want=%v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events=%v want=%v", events, want)
		}
	}
}

func approvalLogEvents(t *testing.T, raw []byte) []string {
	t.Helper()
	var result []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var entry struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		result = append(result, entry.Event)
	}
	return result
}
