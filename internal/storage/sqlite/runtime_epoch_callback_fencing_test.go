package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workspace"
)

// A callback retains the context created for its Run. Once a new process has
// recovered the abandoned operation, none of its late Host Tool, approval,
// evidence, token, or finalization writes may reach SQLite.
func TestRecoveredRuntimeRejectsEveryStaleCallbackWrite(t *testing.T) {
	f := newAgentStorageFixture(t, "stale-callbacks")
	runValue, _ := beginRecoveryRun(t, f, agentconversation.Review, "stale-callbacks", true)
	conversations := NewAgentConversationStore(f.db)
	claim, err := conversations.GetClaim(context.Background(), f.area.ID)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := conversations.Boundary(context.Background(), runValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldContext := runtimeepoch.WithFence(context.Background(), runValue.RuntimeEpoch, runValue.ID)

	workspaces := NewWorkspaceStore(f.db)
	if err := workspaces.Create(context.Background(), workspace.Workspace{ID: "workspace-stale-callbacks", ProjectID: runValue.ProjectID, RepositoryID: f.task.RepositoryID, FeatureAreaID: runValue.FeatureAreaID, BaseCommit: f.task.BaseCommit, Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	reviewValue, err := NewReviewStore(f.db).Get(context.Background(), runValue.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	approvals := NewApprovalStore(f.db)
	approvalValue, err := approvals.CreatePending(oldContext, approval.Approval{ID: "approval-stale-callbacks", RunID: runValue.ID, ThreadID: runValue.ThreadID, TurnID: runValue.TurnID, Kind: approval.KindCommandExecution, RequestPayload: json.RawMessage(`{"command":"true"}`), AllowedDecisions: []approval.DecisionType{approval.DecisionApproveOnce}}, testNow)
	if err != nil {
		t.Fatalf("current-epoch approval write: %v", err)
	}

	// Reopening allocates epoch B. Recovery processes the abandoned epoch-A
	// operation before the old in-memory callback attempts its writes.
	reopenAgentFixtureDB(t, &f)
	if err := NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	conversations = NewAgentConversationStore(f.db)
	if _, err := conversations.Recover(context.Background(), agentconversation.RecoverInput{FeatureAreaID: claim.FeatureAreaID, OperationKind: claim.Kind, OperationID: claim.OperationID, RunID: claim.RunID, AgentConversationID: boundary.AgentConversationID, RequestID: "recover-stale-callbacks"}); err != nil {
		t.Fatal(err)
	}
	successor, err := prepareAgentOperation(t, f, agentconversation.Review, "stale-callback-successor").make(context.Background(), "run-stale-callback-successor")
	if err != nil {
		t.Fatal(err)
	}
	beforeClaim, err := conversations.GetClaim(context.Background(), f.area.ID)
	if err != nil || beforeClaim.RunID != successor.ID {
		t.Fatalf("successor claim=%+v err=%v", beforeClaim, err)
	}

	tests := []struct {
		name  string
		write func() error
	}{
		{"Host Tool Goal", func() error {
			return NewTaskStore(f.db).UpdateGoalProgress(oldContext, "missing-execution", "missing-goal", taskgoal.StatusRunning, testNow.Add(2*time.Hour))
		}},
		{"approval decision", func() error {
			_, err := NewApprovalStore(f.db).Decide(oldContext, approvalValue.ID, approval.Decision{Type: approval.DecisionApproveOnce}, testNow.Add(2*time.Hour))
			return err
		}},
		{"review prompt", func() error {
			_, err := NewReviewStore(f.db).RecordPrompt(oldContext, reviewValue.ID, "late review", testNow.Add(2*time.Hour))
			return err
		}},
		{"review evidence", func() error {
			return NewReviewStore(f.db).RecordAccess(oldContext, review.AccessEvidence{ID: "review-evidence-late", ReviewID: reviewValue.ID, Operation: "read", Outcome: review.AccessSuccess, RecordedAt: testNow.Add(2 * time.Hour)})
		}},
		{"token accounting", func() error {
			_, err := NewWorkspaceStore(f.db).Update(oldContext, "workspace-stale-callbacks", func(value *workspace.Workspace) error {
				value.TokenBaseline.TotalTokens++
				value.UpdatedAt = testNow.Add(2 * time.Hour)
				return nil
			})
			return err
		}},
		{"finalizer", func() error {
			return NewAgentConversationStore(f.db).FinishRun(oldContext, runValue.ID, agentconversation.Unknown, "", true)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); !errors.Is(err, runtimeepoch.ErrFenced) {
				t.Fatalf("late %s error=%v, want %v", tc.name, err, runtimeepoch.ErrFenced)
			}
		})
	}
	afterClaim, err := conversations.GetClaim(context.Background(), f.area.ID)
	if err != nil || afterClaim != beforeClaim {
		t.Fatalf("stale callbacks changed successor claim: before=%+v after=%+v err=%v", beforeClaim, afterClaim, err)
	}
}

func TestHostToolFenceClosesAtFinalizationIntent(t *testing.T) {
	f := newAgentStorageFixture(t, "host-tool-intent")
	runValue, _ := beginRecoveryRun(t, f, agentconversation.Review, "host-tool-intent", true)
	hostContext := runtimeepoch.WithHostToolFence(context.Background(), runValue.RuntimeEpoch, runValue.ID)
	reviews := NewReviewStore(f.db)
	value, err := reviews.Get(context.Background(), runValue.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if err := reviews.RecordAccess(hostContext, review.AccessEvidence{ID: "review-evidence-before-intent", ReviewID: value.ID, Operation: "read", Outcome: review.AccessSuccess, RecordedAt: testNow}); err != nil {
		t.Fatalf("authorized Host Tool access: %v", err)
	}
	if err := NewAgentConversationStore(f.db).RecordFinalization(context.Background(), runValue.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: "turn-host-tool-intent", Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	if err := reviews.RecordAccess(hostContext, review.AccessEvidence{ID: "review-evidence-after-intent", ReviewID: value.ID, Operation: "read", Outcome: review.AccessSuccess, RecordedAt: testNow.Add(time.Second)}); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("Host Tool access after finalization intent=%v, want %v", err, runtimeepoch.ErrFenced)
	}
	summary, err := reviews.SummarizeEvidence(context.Background(), value.ID)
	if err != nil || summary.Successes != 1 {
		t.Fatalf("evidence after fenced callback=%+v err=%v", summary, err)
	}
}
