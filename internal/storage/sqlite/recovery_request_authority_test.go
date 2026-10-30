package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestRecoverySourceAdmissionReadsRequestAuthority(t *testing.T) {
	for _, mode := range []string{"pending", "snapshot-missing-request", "snapshot-missing", "snapshot-wrong-shape", "request-malformed", "request-missing", "request-table-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f, spec := recoveryStatusSourceFixture(t)
			ctx := runtimeepoch.WithFence(context.Background(), spec.RuntimeEpoch, spec.RunID)
			runtime := agentruntime.NewService(NewAgentRuntimeStore(f.db))
			session := runtime.NewTurn(ctx, spec.RunID, spec.ThreadID, false)
			defer session.Close()
			if err := session.Bind(spec.TurnID); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"threadId": spec.ThreadID, "turnId": spec.TurnID, "itemId": "item", "isBlocking": true, "questions": []any{map[string]any{"id": "q", "header": "Question", "question": "Choose?", "isOther": true, "options": []any{map[string]any{"label": "yes", "description": "yes"}}}}})
			if err := session.Observe(agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`1`), Params: payload}); err != nil {
				t.Fatal(err)
			}
			query := ""
			switch mode {
			case "snapshot-missing-request":
				query = `UPDATE agent_runtime_states SET snapshot_json=json_set(snapshot_json,'$.requests',json('[]')) WHERE run_id=?`
			case "snapshot-missing":
				query = `DELETE FROM agent_runtime_states WHERE run_id=?`
			case "snapshot-wrong-shape":
				query = `UPDATE agent_runtime_states SET snapshot_json='{}' WHERE run_id=?`
			case "request-malformed":
				query = `UPDATE codex_server_requests SET request_json='{"state":123}' WHERE run_id=?`
			case "request-missing":
				query = `DELETE FROM codex_server_requests WHERE run_id=?`
			case "request-table-unavailable":
				if _, err := f.db.sql.Exec(`ALTER TABLE codex_server_requests RENAME TO test_unavailable_requests`); err != nil {
					t.Fatal(err)
				}
			}
			if query != "" {
				if _, err := f.db.sql.Exec(query, spec.RunID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := f.db.beginWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, childErr := recoveryRequestDispositionTx(ctx, tx, spec.RunID, false)
			_ = tx.Rollback()
			if childErr == nil {
				t.Fatal("child resolution guard accepted pending or unreadable request authority")
			}
			if _, err := NewRecoveryStatusStore(f.db).Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source)); err == nil {
				t.Fatal("unreadable or pending request acquired recovery admission")
			}
			var count int
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM recovery_status_records WHERE source_run_id=?`, spec.RunID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("rejected admission left a recovery record")
			}
		})
	}
}

func TestRecoveryRequestAuthorityKeepsSourceUnknownSeparateFromChild(t *testing.T) {
	f, spec := recoveryStatusSourceFixture(t)
	ctx := runtimeepoch.WithFence(context.Background(), spec.RuntimeEpoch, spec.RunID)
	store := NewAgentRuntimeStore(f.db)
	runtime := agentruntime.NewService(store)
	session := runtime.NewTurn(ctx, spec.RunID, spec.ThreadID, false)
	defer session.Close()
	if err := session.Bind(spec.TurnID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"threadId": spec.ThreadID, "turnId": spec.TurnID, "itemId": "item", "isBlocking": true, "questions": []any{map[string]any{"id": "q", "header": "Question", "question": "Choose?", "isOther": true, "options": []any{map[string]any{"label": "yes", "description": "yes"}}}}})
	if err := session.Observe(agentruntime.Event{Method: "item/tool/requestUserInput", RequestID: json.RawMessage(`1`), Params: payload}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(ctx, spec.RunID)
	if err != nil || len(state.Requests) != 1 {
		t.Fatalf("request: %+v %v", state, err)
	}
	q := state.Requests[0]
	assertGates := func(wantSource, wantChild bool, wantDisposition string) {
		t.Helper()
		tx, err := f.db.beginWrite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		disposition, sourceErr := recoveryRequestDispositionTx(ctx, tx, spec.RunID, true)
		_, childErr := recoveryRequestDispositionTx(ctx, tx, spec.RunID, false)
		if (sourceErr == nil) != wantSource || (childErr == nil) != wantChild || disposition != wantDisposition {
			t.Fatalf("source=%q/%v child=%v", disposition, sourceErr, childErr)
		}
	}
	assertGates(false, false, "")
	in := agentruntime.UserDecisionInput{Identity: q.Identity, RuntimeRequestID: q.ID, SubmissionRequestID: "recovery-answer", ExpectedState: "WAITING_FOR_USER", Action: "RESPOND", RawText: "yes", SourceSurface: "TEST", SubmittedBy: "user:test"}
	in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
	d, err := runtime.SubmitUserDecision(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	assertGates(false, false, "")
	if _, err = runtime.Resolve(ctx, agentruntime.Resolution{Identity: q.Identity, RuntimeRequestID: q.ID, UserDecisionID: d.ID, ExpectedState: "USER_DECISION_RECORDED", PayloadSHA256: d.PayloadSHA256, RequestID: q.RequestID, ItemID: q.ItemID}); err != nil {
		t.Fatal(err)
	}
	if err = session.BeforeDispatch(q.RequestID); err != nil {
		t.Fatal(err)
	}
	assertGates(false, false, "")
	session.Delivered(q.RequestID, nil)
	assertGates(true, false, "UNKNOWN")
	recoveries := NewRecoveryStatusStore(f.db)
	prepared, err := recoveries.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil || prepared.SourceResponseDisposition != "UNKNOWN" {
		t.Fatalf("source UNKNOWN admission: %+v %v", prepared, err)
	}
	resolved, _ := json.Marshal(map[string]any{"threadId": spec.ThreadID, "requestId": 1})
	// Fail after the snapshot write, while inserting its authoritative receipt.
	// The transaction and the live Session must both roll back for an exact retry.
	if _, err = f.db.sql.Exec(`CREATE TEMP TRIGGER fail_resolved_receipt BEFORE INSERT ON runtime_response_evidence WHEN NEW.kind='SERVER_REQUEST_RESOLVED' BEGIN SELECT RAISE(ABORT,'injected consumed receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = session.Observe(agentruntime.Event{Method: "serverRequest/resolved", Params: resolved}); err == nil {
		t.Fatal("receipt persistence fault did not fire")
	}
	assertGates(true, false, "UNKNOWN")
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_resolved_receipt`); err != nil {
		t.Fatal(err)
	}
	if err = session.Observe(agentruntime.Event{Method: "serverRequest/resolved", Params: resolved}); err != nil {
		t.Fatal(err)
	}
	assertGates(true, true, "")
	if err = session.Observe(agentruntime.Event{Method: "serverRequest/resolved", Params: resolved}); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM runtime_response_evidence WHERE run_id=? AND kind='SERVER_REQUEST_RESOLVED'`, spec.RunID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipt retries created %d consumed evidence rows: %v", receipts, err)
	}
	// Existing exact receipts are historical readback, not fresh admission.
	if _, err = f.db.sql.Exec(`UPDATE agent_runtime_states SET snapshot_json='{}' WHERE run_id=?`, spec.RunID); err != nil {
		t.Fatal(err)
	}
	again, err := recoveries.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil || again.ID != prepared.ID || again.SourceResponseDisposition != "UNKNOWN" {
		t.Fatalf("exact replay changed: %+v %v", again, err)
	}
}
