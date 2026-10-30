package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

func TestGoalCapabilityErrorsRemainStructured(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{goalError(goalInvalidArguments, errors.New("bad input")), "INVALID_ARGUMENTS"},
		{goalError(goalUnknownGoal, errors.New("missing")), "UNKNOWN_GOAL"},
		{goalError(goalInvalidState, errors.New("transition")), "INVALID_STATE"},
		{goalError(goalInvalidOperation, errors.New("operation")), "INVALID_STATE_OPERATION"},
		{errors.New("unavailable"), "OPERATION_CONTEXT_UNAVAILABLE"},
	}
	for _, tc := range cases {
		if got, _ := goalCapabilityError(tc.err); got != tc.want {
			t.Errorf("goalCapabilityError(%v)=%q, want %q", tc.err, got, tc.want)
		}
	}
}

type goalCapabilityRecorder struct {
	calls  int
	result taskgoal.Progress
	err    error
}

func (r *goalCapabilityRecorder) Report(_ context.Context, _, _ string, _ taskgoal.Status) (taskgoal.Progress, error) {
	r.calls++
	return r.result, r.err
}
func (*goalCapabilityRecorder) ListForExecution(context.Context, string) ([]taskgoal.Progress, error) {
	return nil, nil
}

func goalResponseCode(t *testing.T, response codex.DynamicToolCallResponse) string {
	t.Helper()
	if response.Success || len(response.ContentItems) != 1 {
		t.Fatalf("expected structured rejection, got %+v", response)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(response.ContentItems[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Error.Code
}

func TestGoalCapabilityDispatcherEnforcesOperationAndGoalState(t *testing.T) {
	base := run.Run{ID: "run-goal", ThreadID: "thread-goal", TurnID: "turn-goal", Purpose: run.PurposeImplementation, ExecutionProposalID: "execution-goal", Status: run.StatusRunning}
	transition := &taskgoal.ErrInvalidTransition{}
	for _, tc := range []struct {
		name      string
		run       run.Run
		claim     agentconversation.Claim
		claimErr  error
		reportErr error
		want      string
		calls     int
		auth      int
	}{
		{name: "active review", run: base, claim: agentconversation.Claim{RunID: base.ID, Kind: agentconversation.Review}, want: "INVALID_STATE_OPERATION", calls: 0, auth: 1},
		{name: "unknown goal", run: base, claim: agentconversation.Claim{RunID: base.ID, Kind: agentconversation.Execution}, reportErr: taskgoal.ErrNotFound, want: "UNKNOWN_GOAL", calls: 1, auth: 1},
		{name: "invalid transition", run: base, claim: agentconversation.Claim{RunID: base.ID, Kind: agentconversation.Execution}, reportErr: transition, want: "INVALID_STATE", calls: 1, auth: 1},
		{name: "terminal interaction", run: func() run.Run { v := base; v.Status = run.StatusSucceeded; return v }(), want: "OPERATION_CONTEXT_UNAVAILABLE", calls: 0, auth: 0},
		{name: "claim mismatch", run: base, claim: agentconversation.Claim{RunID: "newer-run", Kind: agentconversation.Execution}, want: "OPERATION_CONTEXT_UNAVAILABLE", calls: 0, auth: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &goalCapabilityRecorder{result: taskgoal.Progress{GoalID: "goal-1", Status: taskgoal.StatusRunning}, err: tc.reportErr}
			dispatcher := &GoalCapabilityDispatcher{Runs: capabilityRunLookup{value: tc.run}, Goals: recorder}
			if tc.auth != 0 {
				dispatcher.Conversations = &architectureClaimRuntime{claim: tc.claim, err: tc.claimErr}
			}
			response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: tc.run.ThreadID, TurnID: tc.run.TurnID, Tool: GoalProgressTool, Arguments: json.RawMessage(`{"goalId":"goal-1","status":"RUNNING"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if got := goalResponseCode(t, response); got != tc.want {
				t.Fatalf("error code=%q want=%q", got, tc.want)
			}
			if recorder.calls != tc.calls {
				t.Fatalf("Report calls=%d want=%d", recorder.calls, tc.calls)
			}
			if runtime, ok := dispatcher.Conversations.(*architectureClaimRuntime); ok && runtime.calls != tc.auth {
				t.Fatalf("AuthorizeRun calls=%d want=%d", runtime.calls, tc.auth)
			}
		})
	}

	// Repeating the current status is a legal idempotent Goal report.
	recorder := &goalCapabilityRecorder{result: taskgoal.Progress{GoalID: "goal-1", Status: taskgoal.StatusRunning}}
	dispatcher := &GoalCapabilityDispatcher{Runs: capabilityRunLookup{value: base}, Goals: recorder, Conversations: &architectureClaimRuntime{claim: agentconversation.Claim{RunID: base.ID, Kind: agentconversation.Execution}}}
	response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: base.ThreadID, TurnID: base.TurnID, Tool: GoalProgressTool, Arguments: json.RawMessage(`{"goalId":"goal-1","status":"RUNNING"}`)})
	if err != nil || !response.Success || recorder.calls != 1 {
		t.Fatalf("same-status report=%+v calls=%d err=%v", response, recorder.calls, err)
	}
}

func TestFeatureAreaHostToolV1SchemasHideModelSuppliedRoutingIDs(t *testing.T) {
	if agentconversation.HostToolContractVersion != 1 {
		t.Fatalf("host tool contract version=%d", agentconversation.HostToolContractVersion)
	}
	encoded, err := json.Marshal(FeatureAreaHostToolV1Specs())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"featureAreaId", "taskId", "reviewId", "executionId", "aiSessionId", "runId", "threadId", "turnId"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("host tool v1 schema exposes %s: %s", forbidden, encoded)
		}
	}
}
