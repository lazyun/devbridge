package execution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workspace"
)

type architectureInteractionRuns struct {
	*recordingRuns
	previous         run.Run
	lookupErr        error
	lookups          int
	threadID, turnID string
}

func (r *architectureInteractionRuns) GetByInteraction(_ context.Context, threadID, turnID string) (run.Run, error) {
	r.lookups++
	r.threadID, r.turnID = threadID, turnID
	if r.lookupErr != nil {
		return run.Run{}, r.lookupErr
	}
	if threadID != r.previous.ThreadID || turnID != r.previous.TurnID {
		return run.Run{}, errors.New("wrong historical interaction")
	}
	return r.previous, nil
}

func TestArchitectureCanonicalResumeUsesBoundaryUsageAcrossTasks(t *testing.T) {
	ctx := context.Background()
	end := run.TokenCounts{InputTokens: 80, OutputTokens: 20, TotalTokens: 100}
	for _, tc := range []struct {
		name      string
		status    run.TokenUsageStatus
		end       *run.TokenCounts
		lookupErr error
		want      *run.TokenCounts
	}{
		{name: "different task owns latest boundary", status: run.TokenUsageAvailable, end: &end, want: &end},
		{name: "unavailable accounting", status: run.TokenUsageUnavailable, end: &end},
		{name: "missing cumulative evidence", status: run.TokenUsageAvailable},
		{name: "missing historical interaction", status: run.TokenUsageAvailable, end: &end, lookupErr: run.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			coordinator, runs, tasks, trees, session, _ := successfulCoordinator()
			trees.value.Path = t.TempDir()
			tasks.value.FeatureAreaID = "shared-area"
			conversation := agentconversation.Conversation{ID: "canonical-conversation", FeatureAreaID: tasks.value.FeatureAreaID, BackendThreadID: "canonical-thread", CurrentBoundaryTurnID: "previous-turn", ContinuationSafe: true}
			coordinator.Conversations = &recordingConversationRuntime{conversation: conversation}
			lookup := &architectureInteractionRuns{recordingRuns: runs, lookupErr: tc.lookupErr, previous: run.Run{
				ID: "other-task-run", TaskID: "other-task", ThreadID: conversation.BackendThreadID, TurnID: conversation.CurrentBoundaryTurnID,
				TokenUsage: run.TokenUsageAccounting{Status: tc.status, EndCumulative: tc.end},
			}}
			coordinator.Runs = lookup
			currentWorkspace, err := coordinator.Workspaces.EnsureForFeatureArea(ctx, tasks.value.ProjectID, tasks.value.RepositoryID, tasks.value.FeatureAreaID, tasks.value.BaseCommit)
			if err != nil {
				t.Fatal(err)
			}
			// This Task last used another context; its count must not supply the
			// baseline for the latest canonical boundary owned by another Task.
			if _, err = coordinator.Workspaces.RecordTokenBaseline(ctx, currentWorkspace.ID, &workspace.TokenCounts{TotalTokens: 7}); err != nil {
				t.Fatal(err)
			}
			if tc.want != nil {
				session.usage = &run.TokenUsageObservation{Reliable: true, StartCumulative: end, EndCumulative: run.TokenCounts{InputTokens: 95, OutputTokens: 25, TotalTokens: 120}}
			}
			prepared, prepareErr := coordinator.PrepareRun(ctx, tasks.value.ID)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			completed, err := executeDirectFeatureAreaRun(ctx, coordinator, prepared)
			if err != nil || completed.Status != run.StatusSucceeded {
				t.Fatalf("run=%+v err=%v", completed, err)
			}
			if session.startCalls != 0 || session.resumeCalls != 1 || session.thread != conversation.BackendThreadID {
				t.Fatalf("canonical resume: session=%+v", session)
			}
			if lookup.lookups != 1 || lookup.threadID != conversation.BackendThreadID || lookup.turnID != conversation.CurrentBoundaryTurnID {
				t.Fatalf("lookup did not follow exact safe boundary: %+v", lookup)
			}
			if !reflect.DeepEqual(session.resumeBaseline, tc.want) {
				t.Fatalf("resume baseline=%+v want=%+v; workspace baseline must not be used", session.resumeBaseline, tc.want)
			}
			if tc.want != nil {
				if completed.TokenUsage.Status != run.TokenUsageAvailable || completed.TokenUsage.Usage == nil || completed.TokenUsage.Usage.TotalTokens != 20 {
					t.Fatalf("current Run inherited another Task's usage: %+v", completed.TokenUsage)
				}
			} else if completed.TokenUsage.Status != run.TokenUsageUnavailable {
				t.Fatalf("missing accounting became fabricated usage: %+v", completed.TokenUsage)
			}
		})
	}
}

type architectureFailingWorkspaceAccounting struct {
	WorkspaceService
	err error
}

func (w architectureFailingWorkspaceAccounting) RecordTokenBaseline(context.Context, string, *workspace.TokenCounts) (workspace.Workspace, error) {
	return workspace.Workspace{}, w.err
}

func TestArchitectureAccountingFailurePreservesKnownCompletedTurn(t *testing.T) {
	coordinator, _, tasks, trees, session, _ := successfulCoordinator()
	trees.value.Path = t.TempDir()
	tasks.value.FeatureAreaID = "shared-area"
	runtime := &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "canonical-conversation", FeatureAreaID: tasks.value.FeatureAreaID, ContinuationSafe: true}}
	coordinator.Conversations = runtime
	accountingErr := errors.New("accounting persistence unavailable")
	coordinator.Workspaces = architectureFailingWorkspaceAccounting{WorkspaceService: coordinator.Workspaces, err: accountingErr}
	session.usage = &run.TokenUsageObservation{Reliable: true, EndCumulative: run.TokenCounts{InputTokens: 10, TotalTokens: 10}}
	prepared, prepareErr := coordinator.PrepareRun(context.Background(), tasks.value.ID)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	completed, err := executeDirectFeatureAreaRun(context.Background(), coordinator, prepared)
	if !errors.Is(err, accountingErr) || completed.Status != run.StatusInfrastructureFailed {
		t.Fatalf("accounting failure not retained: run=%+v err=%v", completed, err)
	}
	if runtime.finish != 1 || runtime.advance != agentconversation.KnownAdvance || runtime.exit != "turn-1" || !runtime.quiesced {
		t.Fatalf("local accounting failure invalidated proven backend completion: %+v", runtime)
	}
	if completed.TurnID != runtime.exit || session.closed != 1 {
		t.Fatalf("known exit lacks completed, closed runtime: run=%+v session=%+v", completed, session)
	}
}

type architectureClaimRuntime struct {
	recordingConversationRuntime
	claim agentconversation.Claim
	err   error
	calls int
}

func (r *architectureClaimRuntime) AuthorizeRun(context.Context, string) (agentconversation.Claim, error) {
	r.calls++
	return r.claim, r.err
}

func architectureToolErrorCode(t *testing.T, response codex.DynamicToolCallResponse) string {
	t.Helper()
	if response.Success || len(response.ContentItems) != 1 {
		t.Fatalf("expected one structured tool error: %+v", response)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(response.ContentItems[0].Text), &payload); err != nil || payload.Error.Code == "" {
		t.Fatalf("invalid tool error payload: response=%+v err=%v", response, err)
	}
	return payload.Error.Code
}

func TestArchitectureRepositoryToolsDistinguishWrongOperationFromStaleContext(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   run.Status
		claimRun string
		claimErr error
		want     string
		wantAuth int
	}{
		{name: "active execution", status: run.StatusRunning, claimRun: "run-internal", want: "INVALID_STATE_OPERATION", wantAuth: 1},
		{name: "claim already released", status: run.StatusRunning, claimErr: agentconversation.ErrNotFound, want: "OPERATION_CONTEXT_UNAVAILABLE", wantAuth: 1},
		{name: "claim belongs to newer run", status: run.StatusRunning, claimRun: "new-run", want: "OPERATION_CONTEXT_UNAVAILABLE", wantAuth: 1},
		{name: "terminal execution", status: run.StatusSucceeded, claimRun: "run-internal", want: "OPERATION_CONTEXT_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher, reviews, inspector := newCapabilityDispatcher()
			lookup := dispatcher.Runs.(capabilityRunLookup)
			lookup.value.Purpose, lookup.value.ReviewID, lookup.value.ExecutionProposalID = run.PurposeImplementation, "", "execution-old"
			lookup.value.Status = tc.status
			lookup.value.OperationKind = run.OperationExecution
			lookup.value.OperationID = lookup.value.ExecutionProposalID
			dispatcher.Runs = lookup
			authorization := &architectureClaimRuntime{claim: agentconversation.Claim{RunID: tc.claimRun, FeatureAreaID: lookup.value.FeatureAreaID, Kind: agentconversation.Execution, OperationID: lookup.value.ExecutionProposalID}, err: tc.claimErr}
			dispatcher.Conversations = authorization
			response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: lookup.value.ThreadID, TurnID: lookup.value.TurnID, CallID: "call-old", Tool: ReviewRepositoryReadTool, Arguments: json.RawMessage(`{"path":"source.go"}`)})
			if err != nil {
				t.Fatal(err)
			}
			if got := architectureToolErrorCode(t, response); got != tc.want {
				t.Fatalf("error code=%q want=%q", got, tc.want)
			}
			if authorization.calls != tc.wantAuth || inspector.readPath != "" || len(reviews.evidence) != 0 {
				t.Fatalf("rejected operation dispatched repository access: auth=%d read=%q evidence=%+v", authorization.calls, inspector.readPath, reviews.evidence)
			}
		})
	}
}

type architectureGoalRecorder struct{ calls int }

func (r *architectureGoalRecorder) Report(context.Context, string, string, taskgoal.Status) (taskgoal.Progress, error) {
	r.calls++
	return taskgoal.Progress{}, nil
}

func (*architectureGoalRecorder) ListForExecution(context.Context, string) ([]taskgoal.Progress, error) {
	return nil, nil
}

func TestArchitectureGoalIdentityArgumentsRejectedBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]string
	}{
		{name: "absent", args: map[string]string{"status": "RUNNING"}},
		{name: "empty", args: map[string]string{"goalId": "", "status": "RUNNING"}},
		{name: "whitespace", args: map[string]string{"goalId": " \t", "status": "RUNNING"}},
		{name: "oversized", args: map[string]string{"goalId": strings.Repeat("g", 129), "status": "RUNNING"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := &architectureInteractionRuns{previous: run.Run{ID: "run-internal", ThreadID: "thread", TurnID: "turn", Purpose: run.PurposeImplementation, Status: run.StatusRunning, ExecutionProposalID: "execution"}}
			goals := &architectureGoalRecorder{}
			dispatcher := &GoalCapabilityDispatcher{Runs: lookup, Goals: goals}
			args, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			response, err := dispatcher.Handle(context.Background(), codex.DynamicToolCallParams{ThreadID: "thread", TurnID: "turn", Tool: GoalProgressTool, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			if got := architectureToolErrorCode(t, response); got != "INVALID_ARGUMENTS" {
				t.Fatalf("invalid identity error=%q", got)
			}
			if lookup.lookups != 0 || goals.calls != 0 {
				t.Fatalf("invalid identity dispatched: lookups=%d reports=%d", lookup.lookups, goals.calls)
			}
		})
	}
}
