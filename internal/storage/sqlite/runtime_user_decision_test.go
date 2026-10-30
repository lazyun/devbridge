package sqlite

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"testing"
)

func TestRuntimeUserDecisionImmutableDispatchAndRestart(t *testing.T) {
	f := newAgentStorageFixture(t, "user-decision")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "user-decision", true)
	ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
	store := NewAgentRuntimeStore(f.db)
	svc := agentruntime.NewService(store)
	session := svc.NewTurn(ctx, r.ID, "thread-user-decision", false)
	defer session.Close()
	if err := session.Bind("turn-user-decision"); err != nil {
		t.Fatal(err)
	}
	p := json.RawMessage(`{"threadId":"thread-user-decision","turnId":"turn-user-decision","itemId":"i","startedAtMs":1,"isBlocking":true,"questions":[{"id":"q","header":"Choice","question":"Choose?","isOther":true,"options":[{"label":"yes","description":"yes"}]}]}`)
	if err := session.Observe(agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`9`), Params: p}); err != nil {
		t.Fatal(err)
	}
	v, err := store.Read(ctx, r.ID)
	if err != nil || len(v.Requests) != 1 {
		t.Fatalf("request: %+v %v", v, err)
	}
	q := v.Requests[0]
	if q.State != "WAITING_FOR_USER" || !agentruntime.CompleteDecisionIdentity(q.Identity) {
		t.Fatalf("identity %+v", q)
	}
	raw := agentruntime.Resolution{Identity: q.Identity, RequestID: q.RequestID, ItemID: q.ItemID, Response: json.RawMessage(`{"answers":{"q":{"answers":["yes"]}}}`)}
	if _, err = svc.Resolve(ctx, raw); err == nil {
		t.Fatal("raw answer admitted")
	}
	in := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "explicit-event", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "用户\n exact answer", SourceSurface: "TEST", SubmittedBy: "user:test"}
	in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
	d, err := svc.SubmitUserDecision(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.SubmitUserDecision(ctx, in)
	if err != nil || again.ID != d.ID {
		t.Fatal("replay", err)
	}
	bad := in
	bad.RawText = "different"
	bad.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(bad)
	if _, err = svc.SubmitUserDecision(ctx, bad); err == nil {
		t.Fatal("overwrite")
	}
	// Display snapshot from before admission cannot erase the independently durable answer.
	v.Sequence++
	if err = store.Save(ctx, v.Sequence-1, v, false); err != nil {
		t.Fatal(err)
	}
	v, err = store.Read(ctx, r.ID)
	if err != nil || v.Requests[0].UserDecision == nil || v.Requests[0].State != "USER_DECISION_RECORDED" {
		t.Fatal("clobber", err)
	}
	// Corrupt the disposable projection row with valid JSON of the wrong DTO
	// shape. Read must not fall back to the stale pre-decision snapshot.
	var original string
	if err = f.db.sql.QueryRowContext(ctx, `SELECT request_json FROM codex_server_requests WHERE run_id=?`, r.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.ExecContext(ctx, `UPDATE codex_server_requests SET request_json='{"state":123}' WHERE run_id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	failed, readErr := store.Read(ctx, r.ID)
	if readErr == nil || len(failed.Requests) != 0 {
		t.Fatalf("read corruption exposed stale waiting projection: %+v %v", failed, readErr)
	}
	if _, err = f.db.sql.ExecContext(ctx, `UPDATE codex_server_requests SET request_json=? WHERE run_id=?`, original, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.ExecContext(ctx, `ALTER TABLE codex_server_requests RENAME TO codex_server_requests_read_failure`); err != nil {
		t.Fatal(err)
	}
	failed, readErr = store.Read(ctx, r.ID)
	if readErr == nil || len(failed.Requests) != 0 {
		t.Fatalf("read SQL failure exposed stale waiting projection: %+v %v", failed, readErr)
	}
	if _, err = f.db.sql.ExecContext(ctx, `ALTER TABLE codex_server_requests_read_failure RENAME TO codex_server_requests`); err != nil {
		t.Fatal(err)
	}
	// Save used stale local sequence deliberately; rebind service projection to current DB sequence for dispatch fixture.
	session.Close()
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestDB(t, f.root)
	defer reopened.Close()
	store = NewAgentRuntimeStore(reopened)
	svc = agentruntime.NewService(store)
	recovered, err := svc.UserDecision(ctx, r.ID, d.ID)
	if err != nil || recovered.RawText != in.RawText {
		t.Fatal("restart decision", err)
	}
	if _, err = svc.Resolve(ctx, agentruntime.Resolution{Identity: q.Identity, RequestID: q.RequestID, RuntimeRequestID: q.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256}); err == nil {
		t.Fatal("restart blind dispatch")
	}
}
