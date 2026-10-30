package controlapi

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/run"
)

type approvalRunStub struct{ value run.Run }

func (s approvalRunStub) Get(context.Context, string) (run.Run, error) { return s.value, nil }
func (s approvalRunStub) List(context.Context) ([]run.Run, error)      { return []run.Run{s.value}, nil }

type approvalServiceStub struct {
	values    []approval.Approval
	decideErr error
}

func (s *approvalServiceStub) Get(_ context.Context, id string) (approval.Approval, error) {
	for _, value := range s.values {
		if value.ID == id {
			return value, nil
		}
	}
	return approval.Approval{}, approval.ErrNotFound
}
func (s *approvalServiceStub) ListByRun(_ context.Context, runID string) ([]approval.Approval, error) {
	var values []approval.Approval
	for _, value := range s.values {
		if value.RunID == runID {
			values = append(values, value)
		}
	}
	return values, nil
}
func (s *approvalServiceStub) Decide(_ context.Context, id string, decision approval.Decision) (approval.Approval, error) {
	if s.decideErr != nil {
		return approval.Approval{}, s.decideErr
	}
	value, err := s.Get(context.Background(), id)
	if err != nil {
		return approval.Approval{}, err
	}
	value.Status = approval.StatusDelivered
	value.Decision = &decision
	return value, nil
}

func TestControlAPIApprovalListGetAndDecision(t *testing.T) {
	approvals := &approvalServiceStub{values: []approval.Approval{
		{ID: "approval-1", RunID: "run-1", Status: approval.StatusPending},
		{ID: "approval-2", RunID: "run-1", Status: approval.StatusDelivered},
	}}
	facade := &Facade{deps: Dependencies{Runs: approvalRunStub{value: run.Run{ID: "run-1"}}, Approvals: approvals}}
	listed, err := facade.ListApprovals(context.Background(), "run-1", ListOptions{})
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	pending, err := facade.ListPendingApprovals(context.Background(), "run-1", ListOptions{})
	if err != nil || len(pending) != 1 || pending[0].ID != "approval-1" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	got, err := facade.GetApproval(context.Background(), "approval-1")
	if err != nil || got.ID != "approval-1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	decided, err := facade.DecideApproval(context.Background(), "approval-1", approval.Decision{Type: approval.DecisionDecline})
	if err != nil || decided.Status != approval.StatusDelivered || decided.Decision == nil || decided.Decision.Type != approval.DecisionDecline {
		t.Fatalf("decided=%+v err=%v", decided, err)
	}
}

func TestControlAPIApprovalDecisionErrorsAreStructured(t *testing.T) {
	service := &approvalServiceStub{decideErr: approval.ErrInvalidDecision}
	facade := &Facade{deps: Dependencies{Runs: approvalRunStub{value: run.Run{ID: "run-1"}}, Approvals: service}}
	_, err := facade.DecideApproval(context.Background(), "approval-1", approval.Decision{Type: approval.DecisionApproveOnce})
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidState {
		t.Fatalf("illegal decision err=%v", err)
	}
	service.decideErr = approval.ErrAlreadyResolved
	_, err = facade.DecideApproval(context.Background(), "approval-1", approval.Decision{Type: approval.DecisionDecline})
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidState {
		t.Fatalf("duplicate decision err=%v", err)
	}
	service.decideErr = approval.ErrNotFound
	_, err = facade.DecideApproval(context.Background(), "approval-missing", approval.Decision{Type: approval.DecisionDecline})
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Fatalf("missing decision err=%v", err)
	}
}
