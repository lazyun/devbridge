package controlapi

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type failedOperatorHandoffRead struct {
	ExecutionProposalService
	calls   int
	failure error
	value   executionproposal.Handoff
}

func (s *failedOperatorHandoffRead) ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error) {
	s.calls++
	if s.failure != nil {
		return s.value, s.failure
	}
	if s.value.RunID != "" {
		return s.value, nil
	}
	return executionproposal.Handoff{}, errors.New("handoff storage offline after reservation")
}

func TestOperatorStartHandoffRejectsPartialAndForeignReadResults(t *testing.T) {
	v := executionproposal.Proposal{ID: "execution-original", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", Status: executionproposal.StatusConsumed}
	r := run.Run{ID: "run-original", ProjectID: v.ProjectID, TaskID: v.TaskID, ExecutionProposalID: v.ID, Status: run.StatusCreated}
	for _, kind := range []string{"partial-error", "foreign-run", "foreign-execution", "foreign-task"} {
		t.Run(kind, func(t *testing.T) {
			reader := &failedOperatorHandoffRead{value: executionproposal.Handoff{RunID: r.ID, ExecutionID: v.ID, TaskID: v.TaskID, State: executionproposal.HandoffAccepted, RecordExists: true}}
			switch kind {
			case "partial-error":
				reader.value.RunID = "unverified-partial-run"
				reader.failure = errors.New("handoff scan failed after partial data")
			case "foreign-run":
				reader.value.RunID = "foreign-run"
			case "foreign-execution":
				reader.value.ExecutionID = "foreign-execution"
			case "foreign-task":
				reader.value.TaskID = "foreign-task"
			}
			f := Facade{deps: Dependencies{ExecutionProposals: reader}}
			out, settled, err := f.operatorStartHandoff(context.Background(), v, r)
			if err == nil || !settled || reader.calls != 1 || !reflect.DeepEqual(out.Handoff, executionproposal.Handoff{}) {
				t.Fatalf("untrusted handoff escaped: %+v settled=%t err=%v", out, settled, err)
			}
			if kind == "partial-error" {
				if out.Receipt == nil || out.Receipt.OriginalExecutionID != v.ID || out.Receipt.OriginalRunID != r.ID || !reflect.DeepEqual(out.Proposal, v) || !reflect.DeepEqual(out.Run, r) {
					t.Fatalf("verified original receipt was replaced/lost: %+v", out)
				}
			} else if out.Receipt != nil || out.Proposal.ID != "" || out.Run.ID != "" {
				t.Fatalf("foreign owner conflict did not clear result: %+v", out)
			}
		})
	}
}

func TestOperatorStartLateAccessDenialHidesReceipt(t *testing.T) {
	for _, failure := range []error{localoperator.ErrAccessDenied, executionproposal.ErrNotFound} {
		v := executionproposal.Proposal{ID: "private-execution", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision"}
		r := run.Run{ID: "private-run", ProjectID: v.ProjectID, TaskID: v.TaskID, ExecutionProposalID: v.ID}
		reader := &failedOperatorHandoffRead{failure: failure}
		f := Facade{deps: Dependencies{ExecutionProposals: reader}}
		out, settled, err := f.operatorStartHandoff(context.Background(), v, r)
		if err == nil || !settled || out.Receipt != nil || out.Proposal.ID != "" || out.Run.ID != "" || out.Handoff.RunID != "" {
			t.Fatalf("late denial exposed original receipt: %+v %v", out, err)
		}
		store := &operatorStartFailureReceiptStore{value: v, reserved: r, found: true}
		out, err = operatorStartReservationFailure(context.Background(), store, wp.OperatorExecutionStartRequest{}, failure)
		if err == nil || store.reads != 0 || out.Receipt != nil || out.Proposal.ID != "" {
			t.Fatalf("denied reservation probed or exposed receipt: %+v reads=%d %v", out, store.reads, err)
		}
	}
}

func TestOperatorStartHandoffReadFailureKeepsVerifiedIdentity(t *testing.T) {
	v := executionproposal.Proposal{ID: "execution-original", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", Status: executionproposal.StatusConsumed, ImplementationRunID: "run-original"}
	r := run.Run{ID: "run-original", ProjectID: v.ProjectID, TaskID: v.TaskID, ExecutionProposalID: v.ID, Status: run.StatusCreated}
	reader := &failedOperatorHandoffRead{}
	f := Facade{deps: Dependencies{ExecutionProposals: reader}}
	out, settled, err := f.operatorStartHandoff(context.Background(), v, r)
	if err == nil || !settled || reader.calls != 1 {
		t.Fatalf("read failure lost: settled=%t calls=%d err=%v", settled, reader.calls, err)
	}
	want := &executionproposal.StartReceiptIdentity{ProjectID: v.ProjectID, TaskID: v.TaskID, TaskDefinitionRevisionID: v.TaskDefinitionRevisionID, OriginalExecutionID: v.ID, OriginalRunID: r.ID}
	if !reflect.DeepEqual(out.Receipt, want) || !reflect.DeepEqual(out.Proposal, v) || !reflect.DeepEqual(out.Run, r) || out.Handoff.RecordExists {
		t.Fatalf("known identity replaced by synthetic/read-failed evidence: %+v", out)
	}
}

type operatorStartFailureReceiptStore struct {
	value    executionproposal.Proposal
	reserved run.Run
	found    bool
	err      error
	reads    int
}

func (s *operatorStartFailureReceiptStore) ReadOperatorSelectedTaskExecution(context.Context, wp.OperatorExecutionStartRequest) (executionproposal.Proposal, run.Run, bool, error) {
	s.reads++
	return s.value, s.reserved, s.found, s.err
}
func (*operatorStartFailureReceiptStore) ReserveOperatorSelectedTaskExecution(context.Context, wp.OperatorExecutionStartRequest, codexconfig.BackendSelection) (executionproposal.Proposal, run.Run, error) {
	panic("failure projection must not reserve or dispatch")
}

func TestOperatorStartFailureNeedsIndependentDurableReceipt(t *testing.T) {
	for _, kind := range []string{"committed-admission", "missing", "read-failed"} {
		t.Run(kind, func(t *testing.T) {
			s := &operatorStartFailureReceiptStore{value: executionproposal.Proposal{ID: "known-execution", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", Status: executionproposal.StatusAdmitted}, found: true}
			if kind == "missing" {
				s.found = false
			}
			if kind == "read-failed" {
				s.err = errors.New("receipt source read failed")
			}
			out, err := operatorStartReservationFailure(context.Background(), s, wp.OperatorExecutionStartRequest{}, errors.New("preparation failed"))
			if err == nil || s.reads != 1 {
				t.Fatalf("failure projection result=%+v reads=%d err=%v", out, s.reads, err)
			}
			if kind == "committed-admission" {
				if out.Receipt == nil || out.Receipt.OriginalExecutionID != s.value.ID || out.Receipt.OriginalRunID != "" || out.Run.ID != "" {
					t.Fatalf("admission proof lost/fabricated Run: %+v", out)
				}
			} else if out.Receipt != nil || out.Proposal.ID != "" || out.Run.ID != "" {
				t.Fatalf("unverified/staged object was promoted to durable identity: %+v", out)
			}
		})
	}
}
