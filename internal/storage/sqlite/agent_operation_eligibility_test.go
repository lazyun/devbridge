package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

func TestCancelEligibilityRequiresExactLiveExecutionOwnerAndIsPureRead(t *testing.T) {
	f, value, conversation, _ := cancellationStoreFixture(t, "eligibility")
	store := NewAgentConversationStore(f.db)
	beforeRun, err := NewRunStore(f.db).Get(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.CancelEligibility(context.Background(), f.area.ID)
	if err != nil || !got.Available || got.Target == nil {
		t.Fatalf("eligible cancellation=%+v err=%v", got, err)
	}
	if got.Target.FeatureAreaID != f.area.ID || got.Target.ExecutionID != value.ExecutionProposalID || got.Target.OperationID != value.ExecutionProposalID || got.Target.RunID != value.ID || got.Target.AgentConversationID != conversation.ID || got.Target.OperationKind != agentconversation.Execution {
		t.Fatalf("exact cancel target=%+v", got.Target)
	}
	afterRun, err := NewRunStore(f.db).Get(context.Background(), value.ID)
	if err != nil || !reflect.DeepEqual(afterRun, beforeRun) {
		t.Fatalf("eligibility mutated run: before=%+v after=%+v err=%v", beforeRun, afterRun, err)
	}
	var receipts int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM execution_cancellations WHERE run_id=?`, value.ID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("eligibility wrote cancellation receipt count=%d err=%v", receipts, err)
	}
	reopenAgentFixtureDB(t, &f)
	store = NewAgentConversationStore(f.db)
	got, err = store.CancelEligibility(context.Background(), f.area.ID)
	if err != nil || got.Available || got.Target != nil {
		t.Fatalf("stale epoch cancellation=%+v err=%v", got, err)
	}
}

func TestCancelEligibilityHidesPendingReceiptMissingClaimAndOldImplementation(t *testing.T) {
	ctx := context.Background()
	t.Run("pending receipt", func(t *testing.T) {
		f, value, _, input := cancellationStoreFixture(t, "eligibility-pending")
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, input, true); err != nil {
			t.Fatal(err)
		}
		got, err := NewAgentConversationStore(f.db).CancelEligibility(ctx, f.area.ID)
		if err != nil || got.Available || got.Target != nil {
			t.Fatalf("pending receipt eligibility=%+v err=%v", got, err)
		}
		if receipt, err := NewExecutionCancellationStore(f.db).Get(ctx, input.ProjectID, input.RequestID); err != nil || receipt.RunID != value.ID || receipt.State != "CANCEL_REQUESTED" {
			t.Fatalf("eligibility changed pending receipt=%+v err=%v", receipt, err)
		}
	})
	t.Run("missing claim", func(t *testing.T) {
		f, _, _, _ := cancellationStoreFixture(t, "eligibility-no-claim")
		if _, err := f.db.sql.Exec(`DELETE FROM feature_area_agent_claims WHERE feature_area_id=?`, f.area.ID); err != nil {
			t.Fatal(err)
		}
		got, err := NewAgentConversationStore(f.db).CancelEligibility(ctx, f.area.ID)
		if err != nil || got.Available || got.Target != nil {
			t.Fatalf("missing claim eligibility=%+v err=%v", got, err)
		}
	})
	t.Run("verification supersedes implementation", func(t *testing.T) {
		f, implementation, original, input := cancellationStoreFixture(t, "eligibility-verification", true)
		conversations := NewAgentConversationStore(f.db)
		if err := conversations.RecordFinalization(ctx, implementation.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: implementation.TurnID, Quiesced: true}); err != nil {
			t.Fatal(err)
		}
		runs := run.NewService(NewRunStore(f.db), nil, nil)
		if _, err := runs.Transition(ctx, implementation.ID, run.StatusCollecting); err != nil {
			t.Fatal(err)
		}
		if _, err := runs.Succeed(ctx, implementation.ID, run.Result{}); err != nil {
			t.Fatal(err)
		}
		_, verification, err := NewExecutionProposalStore(f.db).ReserveVerificationRun(ctx, implementation.ExecutionProposalID, "run-eligibility-verification-next", testNow.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		setAgentRunState(t, f.db, verification.ID, run.StatusPreparing, "", "")
		continued, err := conversations.BeginRun(ctx, verification.ID)
		if err != nil || continued.ID != original.ID {
			t.Fatalf("verification continuation=%+v err=%v", continued, err)
		}
		setAgentRunState(t, f.db, verification.ID, run.StatusRunning, implementation.ThreadID, "turn-eligibility-verification-next")
		if err := conversations.MarkTurnPending(ctx, verification.ID); err != nil {
			t.Fatal(err)
		}
		got, err := conversations.CancelEligibility(ctx, f.area.ID)
		if err != nil || !got.Available || got.Target == nil || got.Target.RunID != verification.ID || got.Target.RunID == input.RunID {
			t.Fatalf("verification eligibility=%+v err=%v", got, err)
		}
		old := input
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, old, true); !errors.Is(err, executioncancel.ErrRunNotActive) {
			t.Fatalf("old implementation cancellation err=%v", err)
		}
	})
}

func TestRecoveryEligibilityRequiresExactOldTerminalUnknownClaim(t *testing.T) {
	f := newAgentStorageFixture(t, "recovery-eligibility")
	value, conversation := beginRecoveryRun(t, f, agentconversation.Execution, "recovery-eligibility", true)
	current, err := NewAgentConversationStore(f.db).RecoveryEligibility(context.Background(), f.area.ID)
	if err != nil || current.Available || current.Target != nil || current.Prerequisite != "CORE_RESTART" {
		t.Fatalf("current epoch recovery=%+v err=%v", current, err)
	}
	reopenAgentFixtureDB(t, &f)
	store := NewAgentConversationStore(f.db)
	got, err := store.RecoveryEligibility(context.Background(), f.area.ID)
	if err != nil || got.Available || got.Target != nil {
		t.Fatalf("nonterminal old claim recovery=%+v err=%v", got, err)
	}
	if err := NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), testNow.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err = store.RecoveryEligibility(context.Background(), f.area.ID)
	if err != nil || !got.Available || got.Target == nil {
		t.Fatalf("exact recovery eligibility=%+v err=%v", got, err)
	}
	if got.Target.FeatureAreaID != f.area.ID || got.Target.RunID != value.ID || got.Target.OperationID != value.ExecutionProposalID || got.Target.AgentConversationID != conversation.ID || got.Target.OperationKind != agentconversation.Execution {
		t.Fatalf("exact recovery target=%+v", got.Target)
	}
	if _, err := f.db.sql.Exec(`UPDATE agent_conversations SET ended_at=? WHERE id=?`, formatTime(testNow.Add(3*time.Hour)), conversation.ID); err != nil {
		t.Fatal(err)
	}
	got, err = store.RecoveryEligibility(context.Background(), f.area.ID)
	if err != nil || got.Available || got.Target != nil {
		t.Fatalf("wrong canonical conversation recovery=%+v err=%v", got, err)
	}
	if latest, err := NewRunStore(f.db).Get(context.Background(), value.ID); err != nil || latest.Status != run.StatusInfrastructureFailed {
		t.Fatalf("read eligibility mutated recovered run=%+v err=%v", latest, err)
	}
}
