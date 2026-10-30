package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestSteeringDurableSingleDispatchAndUnknownFence(t *testing.T) {
	for _, outcome := range []string{"ACCEPTED", "REJECTED", "UNKNOWN"} {
		t.Run(outcome, func(t *testing.T) {
			f := newAgentStorageFixture(t, "steer")
			r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "steer", true)
			ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
			store := NewAgentRuntimeStore(f.db)
			svc := agentruntime.NewService(store)
			session := svc.NewTurn(ctx, r.ID, "thread-steer", false)
			defer session.Close()
			if err := session.Bind("turn-steer"); err != nil {
				t.Fatal(err)
			}
			if err := session.Observe(agentruntime.Event{Method: "turn/started", Params: json.RawMessage(`{"threadId":"thread-steer","turn":{"id":"turn-steer","status":"inProgress"}}`)}); err != nil {
				t.Fatal(err)
			}
			if err := session.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			v, err := store.Read(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			session.SetSteeringHandler(func(_ context.Context, in agentruntime.SteeringInput) string {
				calls++
				receipt, found, err := store.FindSteering(ctx, in)
				if err != nil || !found || receipt.State != "DISPATCHING" {
					t.Fatalf("dispatch preceded durable marker: %+v %v", receipt, err)
				}
				return outcome
			})
			view, err := svc.Steering(ctx, r.ID)
			if err != nil || !view.Capability.Allowed {
				t.Fatalf("active capability: %+v %v snapshot=%+v", view, err, v)
			}
			in := agentruntime.SteeringInput{Identity: v.Identity, SubmissionRequestID: "steer-request", Text: "  exact clarification\n", SubmittedBy: "operator:test"}
			receipt, err := svc.Steer(ctx, in)
			if err != nil || receipt.State != outcome || calls != 1 {
				t.Fatalf("dispatch: %+v calls=%d %v", receipt, calls, err)
			}
			// A second Service has no live transport. Exact replays are still pure
			// durable reads; it can neither invent nor dispatch a replacement turn.
			cold := agentruntime.NewService(store)
			again, err := cold.Steer(ctx, in)
			if err != nil || again != receipt || calls != 1 {
				t.Fatalf("cold replay: %+v %v", again, err)
			}
			bad := in
			bad.Text = "changed"
			if _, err = svc.Steer(ctx, bad); !errors.Is(err, agentruntime.ErrRequestConflict) {
				t.Fatalf("payload substitution: %v", err)
			}
			bad = in
			bad.SubmittedBy = "operator:other"
			if _, err = svc.Steer(ctx, bad); !errors.Is(err, agentruntime.ErrRequestConflict) {
				t.Fatalf("actor substitution: %v", err)
			}
			bad = in
			bad.SubmissionRequestID = "other-request"
			bad.TurnID = "foreign"
			if _, err = svc.Steer(ctx, bad); err == nil {
				t.Fatal("foreign turn steered")
			}
			if outcome == "UNKNOWN" {
				view, err = svc.Steering(ctx, r.ID)
				if err != nil || view.Capability.Allowed {
					t.Fatalf("unknown capability: %+v %v", view, err)
				}
				bad = in
				bad.SubmissionRequestID = "new-id-after-loss"
				if _, err = svc.Steer(ctx, bad); err == nil || calls != 1 {
					t.Fatalf("unknown retried with new id calls=%d %v", calls, err)
				}
			}
			if _, err = f.db.sql.Exec(`UPDATE runtime_steering_receipts SET input_json='{}'`); err == nil {
				t.Fatal("durable input mutable")
			}
			session.Close()
			reopenAgentFixtureDB(t, &f)
			restarted := agentruntime.NewService(NewAgentRuntimeStore(f.db))
			if after, e := restarted.Steer(context.Background(), in); e != nil || after != receipt || calls != 1 {
				t.Fatalf("database reopen changed or redelivered receipt: %+v %v", after, e)
			}
		})
	}
}

func TestSteeringRejectsApprovalCancellationAndStaleOwner(t *testing.T) {
	for _, scenario := range []string{"approval", "cancellation", "finalization", "stale-epoch", "completed", "binding"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAgentStorageFixture(t, "steer-fence")
			r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "steer-fence", true)
			ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
			store := NewAgentRuntimeStore(f.db)
			svc := agentruntime.NewService(store)
			session := svc.NewTurn(ctx, r.ID, "thread-steer-fence", false)
			defer session.Close()
			if err := session.Bind("turn-steer-fence"); err != nil {
				t.Fatal(err)
			}
			if err := session.Observe(agentruntime.Event{Method: "turn/started", Params: json.RawMessage(`{"threadId":"thread-steer-fence","turn":{"id":"turn-steer-fence","status":"inProgress"}}`)}); err != nil {
				t.Fatal(err)
			}
			if err := session.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			v, err := store.Read(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			session.SetSteeringHandler(func(context.Context, agentruntime.SteeringInput) string { calls++; return "ACCEPTED" })
			in := agentruntime.SteeringInput{Identity: v.Identity, SubmissionRequestID: "fenced", Text: "clarify", SubmittedBy: "operator:test"}
			switch scenario {
			case "approval":
				err = session.Observe(agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`9`), Params: json.RawMessage(`{"threadId":"thread-steer-fence","turnId":"turn-steer-fence","itemId":"q","isBlocking":true,"questions":[{"id":"q","header":"Choice","question":"Choose?"}]}`)})
			case "cancellation":
				_, err = f.db.sql.Exec(`INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,reason,requested_at,runtime_epoch,state,interrupt_state,thread_id,turn_id,owner_type,owner_id,task_id,backend_disposition)
 SELECT project_id,'pending-steering-cancel',execution_proposal_id,id,?,'synthetic cancellation',?,runtime_epoch,'CANCEL_REQUESTED','NOT_SENT',thread_id,turn_id,operation_kind,operation_id,task_id,'ACTIVE' FROM runs WHERE id=?`, f.session.ID, formatTime(testNow), r.ID)
			case "finalization":
				_, err = f.db.sql.Exec(`UPDATE agent_operation_boundaries SET quiesced=1 WHERE run_id=?`, r.ID)
			case "stale-epoch":
				in.RuntimeEpoch = "foreign"
			case "completed":
				err = session.Observe(agentruntime.Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread-steer-fence","turn":{"id":"turn-steer-fence","status":"completed"}}`)})
				if err == nil {
					err = session.Flush(ctx)
				}
			case "binding":
				in.AppServerInstanceID = "foreign-instance"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Steer(ctx, in); err == nil || calls != 0 {
				t.Fatalf("fence bypassed calls=%d err=%v", calls, err)
			}
		})
	}
}
