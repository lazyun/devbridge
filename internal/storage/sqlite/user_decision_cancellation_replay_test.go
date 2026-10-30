package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestCancellationDecisionColdReplayPreservesIdentityAndWriteFence(t *testing.T) {
	for _, action := range []string{"CANCEL_OPERATION", "STOP_AND_REVISE", "RESPOND", "unrecorded"} {
		t.Run(action, func(t *testing.T) {
			f := newAgentStorageFixture(t, "cancel-replay")
			r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "cancel-replay", true)
			ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
			store := NewAgentRuntimeStore(f.db)
			if err := store.BindAppServer(ctx, r.ID, appserver.Binding{ID: "instance", Epoch: 1, Endpoint: "unix:/fake", ProcessStartIdentity: "pid-start"}); err != nil {
				t.Fatal(err)
			}
			svc := agentruntime.NewService(store)
			session := svc.NewTurn(ctx, r.ID, "thread-cancel-replay", false)
			if err := session.Bind("turn-cancel-replay"); err != nil {
				t.Fatal(err)
			}
			event := agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`9`), Params: json.RawMessage(`{"threadId":"thread-cancel-replay","turnId":"turn-cancel-replay","itemId":"i","isBlocking":true,"questions":[{"id":"q","header":"Choice","question":"Stop?","isOther":true,"options":[{"label":"yes","description":"yes"}]}]}`)}
			if err := session.Observe(event); err != nil {
				t.Fatal(err)
			}
			view, err := store.Read(ctx, r.ID)
			if err != nil || len(view.Requests) != 1 {
				t.Fatalf("request=%+v err=%v", view, err)
			}
			q := view.Requests[0]
			in := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "cancel-event", ExpectedState: q.State, Action: action, SourceSurface: "TEST", SubmittedBy: "user:test"}
			if action == "unrecorded" {
				in.Action = "CANCEL_OPERATION"
			} else if action == "RESPOND" {
				in.RawText = "yes"
			}
			in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
			var decision agentruntime.UserDecision
			if action != "unrecorded" {
				decision, err = svc.SubmitUserDecision(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
			}
			session.Close()
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			db := openTestDB(t, f.root)
			defer db.Close()
			store = NewAgentRuntimeStore(db)
			got, err := store.SubmitUserDecision(context.Background(), in)
			if action == "RESPOND" || action == "unrecorded" {
				if !errors.Is(err, agentruntime.ErrStale) {
					t.Fatalf("old writer admitted %s: %+v %v", action, got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, decision) {
				t.Fatalf("cold cancellation readback: %+v %v", got, err)
			}
			for _, mutation := range []string{"actor", "run", "epoch", "request", "action", "submission"} {
				changed := in
				switch mutation {
				case "actor":
					changed.SubmittedBy = "other"
				case "run":
					changed.RunID = "other"
				case "epoch":
					changed.RuntimeEpoch = db.epoch
				case "request":
					changed.RuntimeRequestID = "other"
				case "action":
					changed.Action, changed.RawText = "RESPOND", "yes"
				case "submission":
					changed.SubmissionRequestID = "other"
				}
				changed.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(changed)
				if _, err = store.SubmitUserDecision(context.Background(), changed); !errors.Is(err, agentruntime.ErrRequestConflict) {
					t.Fatalf("changed %s accepted: %v", mutation, err)
				}
			}
			stored, err := store.UserDecision(context.Background(), r.ID, decision.ID)
			if err != nil || !reflect.DeepEqual(stored, decision) {
				t.Fatalf("replay mutated decision: %+v %v", stored, err)
			}
		})
	}
}
