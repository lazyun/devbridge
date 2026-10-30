package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"strings"
	"testing"
	"time"
)

func TestAppServerContinuityReopenExactPendingDecision(t *testing.T) {
	for _, mode := range []string{"waiting", "recorded", "unknown"} {
		t.Run(mode, func(t *testing.T) { assertAppServerContinuityPending(t, mode, false) })
	}
}
func TestAppServerContinuityRequestSaveRollback(t *testing.T) {
	for _, mode := range []string{"waiting", "recorded", "unknown"} {
		t.Run(mode, func(t *testing.T) { assertAppServerContinuityPending(t, mode, true) })
	}
}
func assertAppServerContinuityPending(t *testing.T, mode string, failSave bool) {
	t.Helper()
	f := newAgentStorageFixture(t, "reattach")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "reattach", true)
	ctx := runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID)
	st := NewAgentRuntimeStore(f.db)
	svc := agentruntime.NewService(st)
	binding := appserver.Binding{ID: "instance", Epoch: 1, Endpoint: "unix:/fake", ProcessStartIdentity: "pid-start"}
	if e := st.BindAppServer(ctx, r.ID, binding); e != nil {
		t.Fatal(e)
	}
	if active, e := st.AppServerHasActiveRuns(ctx, binding.ID); e != nil || !active {
		t.Fatalf("active instance cleanup fence %v %v", active, e)
	}
	if _, e := f.db.sql.Exec(`UPDATE app_server_run_bindings SET binding_json='{}' WHERE run_id=?`, r.ID); e == nil {
		t.Fatal("binding identity mutation")
	}
	if _, e := f.db.sql.Exec(`DELETE FROM app_server_run_bindings WHERE run_id=?`, r.ID); e == nil {
		t.Fatal("binding history deletion")
	}
	session := svc.NewTurn(ctx, r.ID, "thread-reattach", false)
	if e := session.Bind("turn-reattach"); e != nil {
		t.Fatal(e)
	}
	event := agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`9`), Params: json.RawMessage(`{"threadId":"thread-reattach","turnId":"turn-reattach","itemId":"i","isBlocking":true,"questions":[{"id":"q","header":"Choice","question":"Choose?","isOther":true,"options":[{"label":"yes","description":"yes"}]}]}`)}
	if failSave {
		assertRuntimeRequestSaveRollback(t, f.db, session, r.ID, event)
	}
	if e := session.Observe(event); e != nil {
		t.Fatal(e)
	}
	v, e := st.Read(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	q := v.Requests[0]
	var input agentruntime.UserDecisionInput
	if mode != "waiting" {
		input = agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "explicit", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "exact user answer", SourceSurface: "TEST", SubmittedBy: "user:test"}
		input.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(input)
		d, e := svc.SubmitUserDecision(ctx, input)
		if e != nil {
			t.Fatal(e)
		}
		if mode == "unknown" {
			_, e = svc.Resolve(ctx, agentruntime.Resolution{Identity: q.Identity, RuntimeRequestID: q.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: q.RequestID, ItemID: q.ItemID})
			if e != nil {
				t.Fatal(e)
			}
			if e = session.BeforeDispatch(q.RequestID); e != nil {
				t.Fatal(e)
			}
			session.Delivered(q.RequestID, nil)
		}
	}
	session.Close()
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	db := openTestDB(t, f.root)
	defer db.Close()
	st = NewAgentRuntimeStore(db)
	svc = agentruntime.NewService(st)
	before, e := st.Read(context.Background(), r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if before.ConnectionState != "RECONNECTING" {
		t.Fatalf("lost candidate %+v", before)
	}
	var durableBefore string
	if e = db.sql.QueryRow(`SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, r.ID).Scan(&durableBefore); e != nil {
		t.Fatal(e)
	}
	lost, _ := json.Marshal(agentruntime.RuntimeObservation{ConnectionState: "RUNTIME_SESSION_LOST", RequestReattachment: "UNPROVEN", SourceMethod: "thread/loaded/list"})
	if e = st.RecordAppServerObservation(context.Background(), r.ID, lost); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 3; n++ {
		projection, e := st.Read(context.Background(), r.ID)
		if e != nil || projection.Requests[0].Projection.ReasonCode != "CODEX_RUNTIME_SESSION_LOST" || len(projection.Requests[0].AvailableActions) != 0 {
			t.Fatalf("lost projection %+v %v", projection, e)
		}
	}
	var durableAfter string
	if e = db.sql.QueryRow(`SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, r.ID).Scan(&durableAfter); e != nil || durableAfter != durableBefore {
		t.Fatal("GET changed durable snapshot", e)
	}
	recoveredObservation, _ := json.Marshal(agentruntime.RuntimeObservation{ConnectionState: "CONNECTED", RequestReattachment: "AWAITING_EXACT_REQUEST_REPLAY"})
	if e = st.RecordAppServerObservation(context.Background(), r.ID, recoveredObservation); e != nil {
		t.Fatal(e)
	}
	wrong := binding
	wrong.Epoch++
	if _, e = svc.RestoreTurn(context.Background(), r.ID, q.ThreadID, q.TurnID, wrong, false); e == nil {
		t.Fatal("wrong server adopted")
	}
	restored, e := svc.RestoreTurn(context.Background(), r.ID, q.ThreadID, q.TurnID, binding, false)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	untouched, e := st.Read(context.Background(), r.ID)
	if e != nil {
		t.Fatal(e)
	}
	premature := agentruntime.UserDecisionInput{Identity: untouched.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "premature", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "unproven", SourceSurface: "TEST", SubmittedBy: "user:test"}
	premature.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(premature)
	if _, e = svc.SubmitUserDecision(context.Background(), premature); e == nil {
		t.Fatal("answer accepted before exact replay")
	}
	if mode == "unknown" && agentruntime.ResponseDisposition(untouched.Requests[0]) != "UNKNOWN" {
		t.Fatal("unknown inferred as acknowledged")
	}
	if _, e = restored.Await(context.Background(), q.RequestID, event); e == nil {
		t.Fatal("response dispatched without pending replay")
	}
	mismatch := event
	mismatch.Params = json.RawMessage(strings.Replace(string(event.Params), "Choose?", "Changed?", 1))
	if e = restored.Observe(mismatch); e == nil {
		t.Fatal("changed payload replay accepted")
	}
	if e = st.Save(ctx, untouched.Sequence, untouched, false); e == nil {
		t.Fatal("old callback adopted")
	}

	if mode != "waiting" {
		if _, e = svc.SubmitUserDecision(context.Background(), input); e == nil {
			t.Fatal("old caller epoch accepted")
		}
	}
	if failSave {
		assertRuntimeRequestSaveRollback(t, db, restored, r.ID, event)
	}
	if e = restored.Observe(event); e != nil {
		t.Fatal(e)
	}
	after, e := st.Read(context.Background(), r.ID)
	if e != nil {
		t.Fatal(e)
	}
	if after.AppServerInstanceID != binding.ID || after.AppServerInstanceEpoch != binding.Epoch {
		t.Fatal("instance identity not preserved")
	}
	wrongInstanceInput := premature
	wrongInstanceInput.Identity = after.Identity
	wrongInstanceInput.AppServerInstanceEpoch++
	wrongInstanceInput.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(wrongInstanceInput)
	if _, e = svc.SubmitUserDecision(context.Background(), wrongInstanceInput); e == nil {
		t.Fatal("wrong request App Server epoch admitted")
	}
	if after.Observation == nil || after.Observation.RequestReattachment != "RECOVERED" {
		t.Fatalf("replay observation %+v", after.Observation)
	}
	if len(after.Requests) != 1 || after.Requests[0].ID != q.ID || after.RuntimeEpoch == q.RuntimeEpoch || after.ConnectionID == q.ConnectionID {
		t.Fatalf("identity %+v", after)
	}
	expectedDecisionIdentity := q.Identity
	if mode == "waiting" {
		input = premature
		input.Identity = after.Requests[0].Identity
		input.SubmissionRequestID = "answer-after-reconnect"
		input.RawText = "exact user answer"
		input.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(input)
		d, err := svc.SubmitUserDecision(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Resolve(context.Background(), agentruntime.Resolution{Identity: input.Identity, RuntimeRequestID: q.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: q.RequestID, ItemID: q.ItemID}); err != nil {
			t.Fatal(err)
		}
		after, e = st.Read(context.Background(), r.ID)
		if e != nil {
			t.Fatal(e)
		}
		expectedDecisionIdentity = input.Identity
	}
	{
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		answer, e := restored.Await(waitCtx, q.RequestID, event)
		if e != nil || string(answer) != `{"answers":{"q":{"answers":["exact user answer"]}}}` {
			t.Fatalf("response %s %v", answer, e)
		}
		if after.Requests[0].UserDecision.Identity != expectedDecisionIdentity {
			t.Fatal("decision mutated")
		}

		if e = restored.BeforeDispatch(q.RequestID); e != nil {
			t.Fatal(e)
		}
		restored.Delivered(q.RequestID, nil)
		dispatched, e := st.Read(context.Background(), r.ID)
		if e != nil {
			t.Fatal(e)
		}
		want := 1
		if mode == "unknown" {
			want = 2
		}
		if len(dispatched.Requests[0].DispatchAttempts) != want {
			t.Fatal("transport attempt history lost")
		}
		for _, a := range dispatched.Requests[0].DispatchAttempts {
			if string(a.Payload) != string(answer) || a.UserDecisionID != after.Requests[0].UserDecision.ID {
				t.Fatal("retry changed original answer")
			}
		}
		if mode != "waiting" && dispatched.Requests[0].DispatchAttempts[want-1].RecoveryEvidenceID == "" {
			t.Fatal("missing exact pending evidence")
		}
		if e = restored.Observe(agentruntime.Event{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"thread-reattach","requestId":9}`)}); e != nil {
			t.Fatal(e)
		}
		consumed, e := st.Read(context.Background(), r.ID)
		if e != nil || agentruntime.ResponseDisposition(consumed.Requests[0]) != "CONSUMED" {
			t.Fatal("exact receipt not consumed", e)
		}
		var count int
		if e = db.sql.QueryRow(`SELECT count(*) FROM runtime_response_dispatch_attempts WHERE runtime_request_id=?`, q.ID).Scan(&count); e != nil || count != want {
			t.Fatal("dispatch ledger", count, e)
		}
		if _, e = db.sql.Exec(`UPDATE runtime_response_dispatch_attempts SET payload_sha256=payload_sha256 WHERE runtime_request_id=?`, q.ID); e == nil {
			t.Fatal("mutable intent")
		}
		if _, e = db.sql.Exec(`DELETE FROM runtime_response_evidence WHERE runtime_request_id=?`, q.ID); e == nil {
			t.Fatal("deletable evidence")
		}
	}
	var fk int
	rows, e := db.sql.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		fk++
	}
	rows.Close()
	if fk != 0 {
		t.Fatal("broken history FK")
	}
}

func assertRuntimeRequestSaveRollback(t *testing.T, db *DB, session *agentruntime.Session, runID string, event agentruntime.Event) {
	t.Helper()
	read := func() string {
		t.Helper()
		var snapshot string
		if err := db.sql.QueryRow(`SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, runID).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	before := read()
	// Fail after the snapshot update, inside the same transaction, so this
	// checks actual database rollback as well as the Session's waiter boundary.
	if _, err := db.sql.Exec(`CREATE TRIGGER test_request_save_failure BEFORE INSERT ON codex_server_requests BEGIN SELECT RAISE(ABORT,'injected request save failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := session.Observe(event)
	if _, dropErr := db.sql.Exec(`DROP TRIGGER test_request_save_failure`); dropErr != nil {
		t.Fatal(dropErr)
	}
	if err == nil || !strings.Contains(err.Error(), "injected request save failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	if after := read(); after != before {
		t.Fatal("failed request save changed durable snapshot")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := session.Await(ctx, event.RequestID, event); !errors.Is(err, agentruntime.ErrStale) {
		t.Fatalf("failed request save published a waiter: %v", err)
	}
}
