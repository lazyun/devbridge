package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
	"testing"
)

func TestUnknownResponseFacadeRejectsOrdinaryAnswerBeforeWrite(t *testing.T) {
	id := agentruntime.Identity{ProjectID: "p", FeatureAreaID: "a", TaskID: "t", RunID: "r", OperationKind: "EXECUTION", OperationID: "e", ThreadID: "th", TurnID: "tu", RuntimeEpoch: "epoch"}
	q := agentruntime.Request{Identity: id, ID: "q", Kind: "CODEX_REQUEST", State: "RESPONDING", Delivery: "UNKNOWN", UserDecision: &agentruntime.UserDecision{ID: "immutable", UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND"}}}
	value := run.Run{ID: "r", ProjectID: "p", FeatureAreaID: "a", TaskID: "t", OperationKind: "EXECUTION", OperationID: "e", ThreadID: "th", TurnID: "tu", RuntimeEpoch: "epoch", AgentRuntime: &agentruntime.Snapshot{Identity: id, Requests: []agentruntime.Request{q}}}
	f := &Facade{}
	_, err := f.submitAgentUserDecision(context.Background(), agentruntime.UserDecisionInput{Identity: id, RuntimeRequestID: "q", Action: "RESPOND"}, value, func(agentruntime.Resolution) (agentruntime.Snapshot, error) {
		t.Fatal("backend response invoked")
		return agentruntime.Snapshot{}, nil
	}, func(executioncancel.Input) (executioncancel.Receipt, error) {
		t.Fatal("cancel invoked")
		return executioncancel.Receipt{}, nil
	})
	if !errors.Is(err, agentruntime.ErrResponseDispositionUnknown) || MapError(err).Code != "RESPONSE_DISPOSITION_UNKNOWN" {
		t.Fatalf("error=%v", err)
	}
}
func TestUnknownRecoveryUsesCanonicalCancellationAndPreservesAnswer(t *testing.T) {
	id := agentruntime.Identity{ProjectID: "p", RunID: "r", OperationKind: "EXECUTION", OperationID: "e", ConnectionID: "c"}
	q := agentruntime.Request{Identity: id, ID: "q", Kind: "CODEX_REQUEST", State: "RESPONDING", Delivery: "UNKNOWN", UserDecision: &agentruntime.UserDecision{ID: "immutable", UserDecisionInput: agentruntime.UserDecisionInput{Action: "RESPOND", RawText: "exact answer"}}}
	value := run.Run{ID: "r", ProjectID: "p", OperationKind: "EXECUTION", OperationID: "e", AgentRuntime: &agentruntime.Snapshot{Identity: id, Requests: []agentruntime.Request{q}}}
	f := &Facade{deps: Dependencies{Runs: approvalRunStub{value: value}}}
	for _, action := range []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			in := AgentRequestRecoveryInput{Identity: id, RuntimeRequestID: "q", CommandRequestID: "command", Action: action}
			result, err := f.recoverAgentRequest(context.Background(), in, value, func(input executioncancel.Input) (executioncancel.Receipt, error) {
				calls++
				if input.RunID != "r" || input.OwnerID != "e" || input.RequestID != "command" {
					t.Fatalf("wrong cancel=%+v", input)
				}
				return executioncancel.Receipt{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if action == "CONTINUE_WAITING" {
				want = 0
			}
			if calls != want {
				t.Fatalf("calls=%d", calls)
			}
			if result.Runtime.Requests[0].UserDecision.ID != "immutable" || result.Runtime.Requests[0].UserDecision.RawText != "exact answer" || result.Runtime.Requests[0].Delivery != "UNKNOWN" {
				t.Fatal("decision or disposition altered")
			}
			if result.SuccessorRequiresConfirmation != (action == "STOP_AND_REVISE") {
				t.Fatal("successor confirmation contract")
			}
			in.ConnectionID = "other"
			if _, err := f.recoverAgentRequest(context.Background(), in, value, func(executioncancel.Input) (executioncancel.Receipt, error) {
				t.Fatal("wrong identity canceled")
				return executioncancel.Receipt{}, nil
			}); !errors.Is(err, agentruntime.ErrStale) {
				t.Fatalf("wrong identity: %v", err)
			}
		})
	}
}
