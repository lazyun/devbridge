package controlapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

func TestLegacyCancelCannotBypassDurableWorkflowCancellation(t *testing.T) {
	for _, target := range []run.Run{
		{OperationKind: run.OperationExecution, OperationID: "execution-a", Purpose: run.PurposeImplementation},
		{OperationKind: run.OperationExecution, OperationID: "execution-a", Purpose: run.PurposeVerification},
		{ExecutionProposalID: "legacy-execution-a", Purpose: run.PurposeImplementation},
		{ExecutionProposalID: "legacy-execution-a", Purpose: run.PurposeVerification},
		{OperationKind: run.OperationReview, ReviewID: "review-a"},
		{OperationID: "unclassified-operation"},
	} {
		t.Run(string(target.OperationKind)+"/"+target.ExecutionProposalID+"/"+string(target.Purpose)+"/"+target.OperationID, func(t *testing.T) {
			target.ID, target.ProjectID, target.Status = "run-a", "project-a", run.StatusRunning
			runner := &scopeRunner{}
			facade := &Facade{deps: Dependencies{
				AISessions: scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
				Runs:       scopeRuns{"run-a": target}, Runner: runner,
			}}
			for _, cancel := range []func() (run.Run, error){
				func() (run.Run, error) { return facade.CancelRun(context.Background(), target.ID) },
				func() (run.Run, error) {
					return facade.CancelRunForSession(context.Background(), "session-a", target.ID)
				},
			} {
				if _, err := cancel(); err == nil || MapError(err).Code != CodeInvalidArgument {
					t.Fatalf("legacy cancellation accepted workflow Run: target=%+v err=%v", target, err)
				}
			}
			if runner.cancelCalls != 0 {
				t.Fatalf("legacy request signaled backend %d times", runner.cancelCalls)
			}
			got, _ := facade.GetRun(context.Background(), target.ID)
			if got.Status != run.StatusRunning {
				t.Fatalf("rejected request changed Run: %+v", got)
			}
		})
	}
}

func TestLegacyCancelPreservesStandaloneRunCompatibility(t *testing.T) {
	runner := &scopeRunner{}
	facade := &Facade{deps: Dependencies{
		AISessions: scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
		Runs:       scopeRuns{"run-a": {ID: "run-a", ProjectID: "project-a", TaskID: "legacy-task", Purpose: run.PurposeImplementation, Status: run.StatusRunning}},
		Runner:     runner,
	}}
	if got, err := facade.CancelRunForSession(context.Background(), "session-a", "run-a"); err != nil || got.Status != run.StatusCanceled || runner.cancelCalls != 1 {
		t.Fatalf("standalone cancel=%+v calls=%d err=%v", got, runner.cancelCalls, err)
	}
}

type cancellationRunnerStub struct {
	Runner
	calls []executioncancel.Input
	err   error
}

func (s *cancellationRunnerStub) CancelExecution(_ context.Context, input executioncancel.Input) (executioncancel.Receipt, error) {
	s.calls = append(s.calls, input)
	return executioncancel.Receipt{ProjectID: input.ProjectID, ExecutionID: input.ExecutionID, RunID: input.RunID, RequestID: input.RequestID}, s.err
}

type cancellationReadStub struct {
	executioncancel.Store
	project, execution string
	receipts           []executioncancel.Receipt
}

func (s *cancellationReadStub) List(_ context.Context, projectID, executionID string) ([]executioncancel.Receipt, error) {
	s.project, s.execution = projectID, executionID
	if s.receipts != nil {
		return s.receipts, nil
	}
	return []executioncancel.Receipt{{ProjectID: projectID, ExecutionID: executionID, RequestID: "original"}}, nil
}

func TestExecutionCancellationMessageRetainsExactMachineStateAndClaim(t *testing.T) {
	claim := &agentconversation.Claim{Kind: agentconversation.Execution, OperationID: "execution-a", RunID: "run-a"}
	store := &cancellationReadStub{receipts: []executioncancel.Receipt{{RunID: "run-a", State: "CANCEL_REQUESTED"}}}
	facade := &Facade{deps: Dependencies{ExecutionCancellations: store}}
	state := agentconversation.State{Status: "BUSY", Claim: claim, Message: "original"}
	got, err := facade.ExecutionCancellationState(context.Background(), "project-a", state)
	if err != nil || got.Status != "BUSY" || got.Claim != claim || !strings.Contains(got.Message, "cancellation requested") {
		t.Fatalf("state=%+v err=%v", got, err)
	}
	store.receipts[0].RecoveryRequired = true
	state.Status = "RECOVERY_REQUIRED"
	got, err = facade.ExecutionCancellationState(context.Background(), "project-a", state)
	if err != nil || got.Status != "RECOVERY_REQUIRED" || got.Claim != claim || !strings.Contains(got.Message, "outcome is unknown") {
		t.Fatalf("state=%+v err=%v", got, err)
	}
	store.receipts[0].RunID = "old-run"
	got, err = facade.ExecutionCancellationState(context.Background(), "project-a", state)
	if err != nil || got.Message != "original" {
		t.Fatalf("stale receipt changed current message: %+v %v", got, err)
	}
}

func TestExecutionCancellationBindsSessionAndRequiresExactRun(t *testing.T) {
	runner := &cancellationRunnerStub{}
	receipts := &cancellationReadStub{}
	facade := &Facade{deps: Dependencies{AISessions: scopeSessions{"session-a": aisession.Session{ID: "session-a", ProjectID: "project-a"}, "fresh": aisession.Session{ID: "fresh", ProjectID: "project-a"}}, Runner: runner, ExecutionCancellations: receipts}}
	input := executioncancel.Input{AISessionID: "session-a", ExecutionID: "execution-a", RunID: "run-a", RequestID: "cancel-a", Reason: "stop exact run"}
	if got, err := facade.CancelExecutionForSession(context.Background(), input); err != nil || got.ProjectID != "project-a" || len(runner.calls) != 1 || runner.calls[0].RunID != "run-a" || runner.calls[0].Reason != input.Reason {
		t.Fatalf("receipt=%+v calls=%+v err=%v", got, runner.calls, err)
	}
	for _, mutation := range []struct {
		name   string
		change func(*executioncancel.Input)
		code   ErrorCode
	}{
		{"foreign project", func(in *executioncancel.Input) { in.ProjectID = "project-b" }, "PROJECT_MISMATCH"},
		{"missing request", func(in *executioncancel.Input) { in.RequestID = "" }, CodeInvalidArgument},
		{"unknown session", func(in *executioncancel.Input) { in.AISessionID = "missing" }, CodeNotFound},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := input
			mutation.change(&changed)
			_, err := facade.CancelExecutionForSession(context.Background(), changed)
			if err == nil || MapError(err).Code != mutation.code || len(runner.calls) != 1 {
				t.Fatalf("calls=%+v err=%v", runner.calls, err)
			}
		})
	}
	got, err := facade.ListExecutionCancellationsForSession(context.Background(), "fresh", "execution-a")
	if err != nil || len(got) != 1 || receipts.project != "project-a" || receipts.execution != "execution-a" {
		t.Fatalf("fresh receipts=%+v store=%+v err=%v", got, receipts, err)
	}
}

func TestExecutionCancellationDomainErrorsKeepExactCodes(t *testing.T) {
	for _, domain := range []error{executioncancel.ErrInvalidArgument, executioncancel.ErrExecutionNotRunning, executioncancel.ErrRunNotActive, executioncancel.ErrRunExecutionMismatch, executioncancel.ErrClaimMismatch, executioncancel.ErrRecoveryRequired, executioncancel.ErrRequestConflict, executioncancel.ErrProjectMismatch, executioncancel.ErrOutcomeUnknown} {
		mapped := MapError(errors.Join(errors.New("cancellation operation"), domain))
		if string(mapped.Code) != domain.Error() || !errors.Is(mapped, domain) {
			t.Fatalf("domain=%v mapped=%+v", domain, mapped)
		}
	}
}
