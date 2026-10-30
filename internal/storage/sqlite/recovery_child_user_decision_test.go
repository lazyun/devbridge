package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestOrdinaryRuntimeOpenCannotAcceptForgedRecoveryIdentity(t *testing.T) {
	f := newAgentStorageFixture(t, "forged-recovery-decision")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "forged-recovery-decision", true)
	ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
	id, err := NewAgentRuntimeStore(f.db).Open(ctx, agentruntime.Identity{RunID: r.ID, ThreadID: "thread-forged-recovery-decision", TurnID: "turn-forged-recovery-decision", ConnectionID: "forged-recovery-connection", RecoveryStatusID: "caller-forged-recovery"})
	if err != nil || id.RecoveryStatusID != "" || !agentruntime.CompleteDecisionIdentity(id) {
		t.Fatalf("caller selected diagnostic identity: %+v %v", id, err)
	}
}

func TestIndependentRecoveryChildUserDecisionUsesExactChildIdentity(t *testing.T) {
	for _, mutation := range []string{"none", "source-run", "source-conversation", "foreign-recovery", "dispatch-receipt"} {
		t.Run(mutation, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, recovery *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				ctx = runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
				store := NewAgentRuntimeStore(f.db)
				service := agentruntime.NewService(store)
				session := service.NewTurn(ctx, child.ID, child.ThreadID, true)
				defer session.Close()
				if err := session.Bind(child.TurnID); err != nil {
					t.Fatal(err)
				}
				payload, _ := json.Marshal(map[string]any{"threadId": child.ThreadID, "turnId": child.TurnID, "itemId": "child-question", "startedAtMs": 1, "isBlocking": true, "questions": []map[string]any{{"id": "q", "header": "Choice", "question": "Stop?", "isOther": true, "options": []map[string]string{{"label": "yes", "description": "Yes"}}}}})
				if err := session.Observe(agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`91`), Params: payload}); err != nil {
					t.Fatal(err)
				}
				view, err := store.Read(ctx, child.ID)
				if err != nil || len(view.Requests) != 1 {
					t.Fatalf("question=%+v err=%v", view, err)
				}
				q := view.Requests[0]
				if q.RecoveryStatusID != record.ID || q.AgentConversationID != "" || q.ExecutionID != source.OperationID || !agentruntime.CompleteDecisionIdentity(q.Identity) {
					t.Fatalf("child borrowed source identity: %+v", q.Identity)
				}
				in := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "child-cancel-decision", ExpectedState: q.State, Action: "CANCEL_OPERATION", SourceSurface: "TEST", SubmittedBy: "user:test"}
				switch mutation {
				case "source-run":
					in.RunID, in.ThreadID, in.TurnID = source.ID, source.ThreadID, source.TurnID
				case "source-conversation":
					in.AgentConversationID = record.Spec.AgentConversationID
				case "foreign-recovery":
					in.RecoveryStatusID = "other-recovery"
				case "dispatch-receipt":
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "recovery_child_turn_receipts")
					if _, err = f.db.sql.ExecContext(ctx, `UPDATE recovery_child_turn_receipts SET payload_sha256=?`, rs.Hash([]byte("foreign-dispatch"))); err != nil {
						t.Fatal(err)
					}
				}
				in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
				decision, err := service.SubmitUserDecision(ctx, in)
				if mutation == "none" {
					if err != nil || decision.RecoveryStatusID != record.ID {
						t.Fatalf("child cancellation decision rejected: %+v %v", decision, err)
					}
				} else if err == nil {
					t.Fatal("inexact child authority accepted decision")
				}
			})
		})
	}
}
