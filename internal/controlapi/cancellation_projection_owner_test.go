package controlapi

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

type ownerProjectionCancellation struct {
	cancellationReadStub
	upper agentconversation.Eligibility
	calls int
}

func (s *ownerProjectionCancellation) ReviewCancelEligibility(context.Context, string) (agentconversation.Eligibility, error) {
	s.calls++
	return s.upper, nil
}

type ownerProjectionRunner struct{ cancellationLiveRunner }

func (ownerProjectionRunner) NoBackendCancellationAvailable() bool { return true }

// Pure projection regression: historical review pointers never override the
// independently observed Execution owner. No writer or storage rows are mocked
// into the integration test; these stubs only enumerate read-side boundaries.
func TestCancellationProjectionUsesObservedOwner(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       review.ExecutorKind
		claim      string
		want       bool
		upperCalls int
	}{
		{"historical-upper-current-execution", review.ExecutorUpperLLM, "execution", true, 0},
		{"historical-coding-current-execution", review.ExecutorCodingAgent, "execution", true, 0},
		{"current-upper-review", review.ExecutorUpperLLM, "review", true, 1},
		{"current-coding-review", review.ExecutorCodingAgent, "review", true, 0},
		{"missing-owner-upper", review.ExecutorUpperLLM, "missing", false, 0},
		{"missing-owner-coding", review.ExecutorCodingAgent, "missing", false, 0},
		{"mismatched-review-upper", review.ExecutorUpperLLM, "foreign", false, 0},
		{"mismatched-review-coding", review.ExecutorCodingAgent, "foreign", false, 0},
		{"wrong-review-run-upper", review.ExecutorUpperLLM, "wrong-run", false, 1},
		{"wrong-review-run-coding", review.ExecutorCodingAgent, "wrong-run", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executionTarget := &agentconversation.ActionTarget{FeatureAreaID: "area", ExecutionID: "execution", OperationKind: agentconversation.Execution, OperationID: "execution", RunID: "execution-run", AgentConversationID: "conversation"}
			upperTarget := &agentconversation.ActionTarget{FeatureAreaID: "area", OperationKind: agentconversation.Review, OperationID: "review", RunID: "review-run"}
			store := &ownerProjectionCancellation{upper: agentconversation.Eligibility{Available: true, Target: upperTarget}}
			f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": aisession.Session{ID: "session", ProjectID: "project"}}, AgentConversations: capabilityConversationStub{cancel: agentconversation.Eligibility{Available: true, Target: executionTarget}}, ExecutionCancellations: store, Runner: ownerProjectionRunner{cancellationLiveRunner{available: true}}, Runs: scopeRuns{"review-run": run.Run{ID: "review-run", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", Status: run.StatusRunning, RuntimeEpoch: "epoch", ThreadID: "thread", TurnID: "turn"}}}}
			current := CurrentWorkflow{Topic: &featurearea.FeatureArea{ID: "area"}, Task: &task.Task{ID: "task", ProjectID: "project"}, Execution: &executionproposal.Proposal{ID: "execution"}, CurrentReview: &review.Review{ID: "review", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", RunID: "review-run", ExecutorKind: tc.kind}}
			op := agentconversation.State{Status: "BUSY", AgentConversationID: "conversation", Claim: &agentconversation.Claim{FeatureAreaID: "area", Kind: agentconversation.Execution, OperationID: "execution", RunID: "execution-run", RuntimeEpoch: "epoch"}}
			wantOwner, wantRun := "execution", "execution-run"
			if tc.claim != "execution" {
				op.Claim.Kind = agentconversation.Review
				op.Claim.OperationID = "review"
				op.Claim.RunID = "review-run"
				wantOwner, wantRun = "review", "review-run"
			}
			switch tc.claim {
			case "missing":
				op.Claim = nil
			case "foreign":
				op.Claim.OperationID = "different-review"
			case "wrong-run":
				op.Claim.RunID = "different-run"
			}
			out := p.Projection{}
			if e := f.enrichFeatureAreaActionCapabilities(context.Background(), "session", current, op, &out); e != nil {
				t.Fatal(e)
			}
			found := false
			for _, cap := range out.Capabilities {
				if cap.Action != "CANCEL_EXECUTION" {
					continue
				}
				found = true
				if cap.Available != tc.want {
					t.Fatalf("wrong eligibility %+v", cap)
				}
				if tc.want && (cap.Target == nil || cap.Target.OperationID != wantOwner || cap.Target.RunID != wantRun || cap.Target.TaskID != "task") {
					t.Fatalf("wrong exact owner %+v", cap)
				}
				if !tc.want && cap.Target != nil {
					t.Fatalf("failed-closed target leaked %+v", cap)
				}
			}
			if !found || store.calls != tc.upperCalls {
				t.Fatalf("found=%t upper-reader-calls=%d want%d", found, store.calls, tc.upperCalls)
			}
		})
	}
}
