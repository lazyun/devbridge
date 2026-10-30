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

func TestResponseDispatchIntentAtomicFailureAndNoUnprovenRetry(t *testing.T) {
	f := newAgentStorageFixture(t, "dispatch-atomic")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "dispatch-atomic", true)
	r.ThreadID = "thread-dispatch-atomic"
	r.TurnID = "turn-dispatch-atomic"
	ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
	st := NewAgentRuntimeStore(f.db)
	svc := agentruntime.NewService(st)
	session := svc.NewTurn(ctx, r.ID, r.ThreadID, false)
	defer session.Close()
	if e := session.Bind(r.TurnID); e != nil {
		t.Fatal(e)
	}
	payload, _ := json.Marshal(map[string]any{"threadId": r.ThreadID, "turnId": r.TurnID, "itemId": "item", "isBlocking": true, "questions": []any{map[string]any{"id": "q", "header": "Question", "question": "Choose?", "isOther": true, "options": []any{map[string]any{"label": "yes", "description": "yes"}}}}})
	if e := session.Observe(agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`1`), Params: payload}); e != nil {
		t.Fatal(e)
	}
	v, e := st.Read(ctx, r.ID)
	if e != nil || len(v.Requests) != 1 {
		t.Fatal("request missing", e)
	}
	q := v.Requests[0]
	in := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "atomic-explicit", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: " preserve exact answer\n", SourceSurface: "TEST", SubmittedBy: "user:test"}
	in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
	d, e := svc.SubmitUserDecision(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Resolve(ctx, agentruntime.Resolution{Identity: q.Identity, RuntimeRequestID: q.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: q.RequestID, ItemID: q.ItemID}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.sql.Exec(`CREATE TRIGGER test_dispatch_failure BEFORE INSERT ON runtime_response_dispatch_attempts BEGIN SELECT RAISE(ABORT,'injected intent failure'); END`); e != nil {
		t.Fatal(e)
	}
	if e = session.BeforeDispatch(q.RequestID); e == nil {
		t.Fatal("dispatch entered without durable intent")
	}
	session.Delivered(q.RequestID, e)
	failed, e := st.Read(ctx, r.ID)
	if e != nil || agentruntime.ResponseDisposition(failed.Requests[0]) != "NOT_DISPATCHED" || len(failed.Requests[0].DispatchAttempts) != 0 {
		t.Fatal("failed intent changed snapshot", e)
	}
	var count int
	if e = f.db.sql.QueryRow(`SELECT count(*) FROM runtime_response_dispatch_attempts`).Scan(&count); e != nil || count != 0 {
		t.Fatal("partial intent survived", e)
	}
	if _, e = f.db.sql.Exec(`DROP TRIGGER test_dispatch_failure`); e != nil {
		t.Fatal(e)
	}
	if e = session.BeforeDispatch(q.RequestID); e != nil {
		t.Fatal(e)
	}
	session.Delivered(q.RequestID, nil)
	late := session.BeforeDispatch(q.RequestID)
	if !errors.Is(late, agentruntime.ErrStale) {
		t.Fatalf("duplicate attempt was not fenced: %v", late)
	}
	session.Delivered(q.RequestID, late)
	v, e = st.Read(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if agentruntime.ResponseDisposition(v.Requests[0]) != "UNKNOWN" {
		t.Fatal("late rejected invocation erased the earlier dispatch")
	}
	var falseProofs int
	if e = f.db.sql.QueryRow(`SELECT count(*) FROM runtime_response_evidence WHERE run_id=? AND kind='PRE_EFFECT_REJECTED' AND json_extract(evidence_json,'$.attemptId')<>''`, r.ID).Scan(&falseProofs); e != nil || falseProofs != 0 {
		t.Fatalf("late fence manufactured retry proof: count=%d err=%v", falseProofs, e)
	}
	tx, e := f.db.beginWrite(ctx)
	if e != nil {
		t.Fatal(e)
	}
	gateErr := requireResolvedResponsesTx(ctx, tx, r.ID)
	_ = tx.Rollback()
	if !errors.Is(gateErr, agentruntime.ErrResponseDispositionUnknown) {
		t.Fatalf("late fence released finalization: %v", gateErr)
	}
	for _, state := range []string{"NOT_DISPATCHED", "CONSUMED"} {
		data, _ := json.Marshal(v)
		var forged agentruntime.Snapshot
		_ = json.Unmarshal(data, &forged)
		forged.Sequence++
		forged.Requests[0].Delivery = state
		forged.Requests[0].State = "USER_DECISION_RECORDED"
		if state == "CONSUMED" {
			forged.Requests[0].State = "RESPONDED"
		}
		if e = st.Save(ctx, v.Sequence, forged, false); !errors.Is(e, agentruntime.ErrResponseDispositionUnknown) {
			t.Fatalf("unproven %s admitted: %v", state, e)
		}
	}
	after, e := st.Read(ctx, r.ID)
	if e != nil || after.Sequence != v.Sequence || len(after.Requests[0].DispatchAttempts) != 1 || after.Requests[0].UserDecision.RawText != in.RawText {
		t.Fatal("rejected transition mutated history", e)
	}
	if _, e = f.db.sql.Exec(`UPDATE runtime_user_decisions SET decision_json='{}' WHERE id=?`, d.ID); e == nil {
		t.Fatal("decision became mutable")
	}
}
