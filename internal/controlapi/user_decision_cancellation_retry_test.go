package controlapi

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

type cancellationDecisionRunStub struct {
	approvalRunStub
	decision agentruntime.UserDecision
}

func (s cancellationDecisionRunStub) SubmitUserDecision(context.Context, agentruntime.UserDecisionInput) (agentruntime.UserDecision, error) {
	return s.decision, nil
}

func TestUserDecisionCancellationRetryObservesOnlyUnfinishedReceipt(t *testing.T) {
	for _, action := range []string{"CANCEL_OPERATION", "STOP_AND_REVISE"} {
		for _, requestState := range []string{"USER_DECISION_RECORDED", "UNKNOWN", "RESPONDING", "RESPONDED", "CANCELED", "STALE"} {
			for _, receiptState := range []string{"CANCEL_REQUESTED", "RECOVERY_REQUIRED", "CANCELED", "ALREADY_TERMINAL", "CANCEL_NOT_APPLIED", "OUTCOME_UNKNOWN"} {
				t.Run(action+"/"+requestState+"/"+receiptState, func(t *testing.T) {
					id := agentruntime.Identity{ProjectID: "p", FeatureAreaID: "a", TaskID: "t", RunID: "r", OperationKind: "EXECUTION", OperationID: "e", ThreadID: "th", TurnID: "tu", RuntimeEpoch: "epoch"}
					in := agentruntime.UserDecisionInput{Identity: id, RuntimeRequestID: "q", SubmissionRequestID: "submission", Action: action}
					in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
					decision := agentruntime.UserDecision{ID: "decision", UserDecisionInput: in}
					q := agentruntime.Request{Identity: id, ID: "q", Kind: "CODEX_REQUEST", State: requestState, UserDecision: &decision}
					value := run.Run{ID: "r", ProjectID: "p", FeatureAreaID: "a", TaskID: "t", OperationKind: "EXECUTION", OperationID: "e", ThreadID: "th", TurnID: "tu", RuntimeEpoch: "epoch", AgentRuntime: &agentruntime.Snapshot{Identity: id, Requests: []agentruntime.Request{q}}}
					receipt := executioncancel.Receipt{ProjectID: "p", OwnerType: "EXECUTION", OwnerID: "e", RunID: "r", RequestID: fmt.Sprintf("user-decision-%x", sha256.Sum256([]byte(decision.ID))), State: receiptState}
					f := &Facade{deps: Dependencies{Runs: cancellationDecisionRunStub{approvalRunStub: approvalRunStub{value: value}, decision: decision}, ExecutionCancellations: &cancellationReadStub{receipts: []executioncancel.Receipt{receipt}}}}
					pending := receiptState == "CANCEL_REQUESTED" || receiptState == "RECOVERY_REQUIRED"
					// Without termination evidence the retry must preserve the error.
					for _, observeErr := range []error{nil, executioncancel.ErrOutcomeUnknown} {
						calls := 0
						got, err := f.submitAgentUserDecision(context.Background(), in, value, func(agentruntime.Resolution) (agentruntime.Snapshot, error) {
							t.Fatal("cancellation sent an answer")
							return agentruntime.Snapshot{}, nil
						}, func(input executioncancel.Input) (executioncancel.Receipt, error) {
							calls++
							want := executioncancel.Input{ProjectID: "p", OwnerType: "EXECUTION", OwnerID: "e", RunID: "r", RequestID: receipt.RequestID, Reason: "Explicit user decision: " + action}
							if !reflect.DeepEqual(input, want) {
								t.Fatalf("retry changed cancellation identity: got=%+v want=%+v", input, want)
							}
							return receipt, observeErr
						})
						wantCalls, wantErr := 0, error(nil)
						if pending {
							wantCalls, wantErr = 1, observeErr
						}
						if calls != wantCalls || !errors.Is(err, wantErr) || got.Cancellation == nil || !reflect.DeepEqual(*got.Cancellation, receipt) || got.Decision.ID != decision.ID {
							t.Fatalf("retry calls=%d want=%d receipt=%+v err=%v want=%v", calls, wantCalls, got.Cancellation, err, wantErr)
						}
					}
				})
			}
		}
	}
}

func TestAdoptedRunAcceptsOnlyExactRecordedCancellationReplay(t *testing.T) {
	for _, mutation := range []string{"none", "actor", "action", "epoch", "submission", "missing-decision"} {
		t.Run(mutation, func(t *testing.T) {
			id := agentruntime.Identity{ProjectID: "p", FeatureAreaID: "a", TaskID: "t", RunID: "r", OperationKind: "EXECUTION", OperationID: "e", ThreadID: "th", TurnID: "tu", RuntimeEpoch: "old"}
			in := agentruntime.UserDecisionInput{Identity: id, RuntimeRequestID: "q", SubmissionRequestID: "submission", Action: "CANCEL_OPERATION", SubmittedBy: "session:original"}
			in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
			decision := agentruntime.UserDecision{ID: "decision", UserDecisionInput: in}
			q := agentruntime.Request{Identity: id, ID: "q", Kind: "CODEX_REQUEST", State: "STALE", UserDecision: &decision}
			q.RuntimeEpoch = "current"
			switch mutation {
			case "actor":
				in.SubmittedBy = "session:other"
			case "action":
				in.Action, in.RawText = "RESPOND", "yes"
			case "epoch":
				in.RuntimeEpoch = "foreign"
			case "submission":
				in.SubmissionRequestID = "new"
			case "missing-decision":
				q.UserDecision = nil
			}
			in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
			value := run.Run{ID: "r", ProjectID: "p", FeatureAreaID: "a", TaskID: "t", OperationKind: "EXECUTION", OperationID: "e", ThreadID: "th", TurnID: "tu", RuntimeEpoch: "current", AgentRuntime: &agentruntime.Snapshot{Identity: q.Identity, Requests: []agentruntime.Request{q}}}
			receipt := executioncancel.Receipt{ProjectID: "p", RunID: "r", RequestID: fmt.Sprintf("user-decision-%x", sha256.Sum256([]byte(decision.ID))), State: "RECOVERY_REQUIRED"}
			f := &Facade{deps: Dependencies{Runs: cancellationDecisionRunStub{approvalRunStub: approvalRunStub{value: value}, decision: decision}, ExecutionCancellations: &cancellationReadStub{receipts: []executioncancel.Receipt{receipt}}}}
			calls := 0
			got, err := f.submitAgentUserDecision(context.Background(), in, value, func(agentruntime.Resolution) (agentruntime.Snapshot, error) {
				t.Fatal("old epoch response dispatched")
				return agentruntime.Snapshot{}, nil
			}, func(input executioncancel.Input) (executioncancel.Receipt, error) {
				calls++
				if input.RequestID != receipt.RequestID || input.RunID != id.RunID {
					t.Fatalf("retry minted a new cancellation: %+v", input)
				}
				return receipt, nil
			})
			if mutation == "none" {
				if err != nil || calls != 1 || got.Decision.ID != decision.ID {
					t.Fatalf("exact historical cancellation rejected: calls=%d %v", calls, err)
				}
			} else {
				wantErr := agentruntime.ErrStale
				if mutation == "action" || mutation == "epoch" {
					wantErr = agentruntime.ErrRequestConflict
				}
				if !errors.Is(err, wantErr) || calls != 0 {
					t.Fatalf("inexact historical decision passed: calls=%d %v", calls, err)
				}
			}
		})
	}
}
