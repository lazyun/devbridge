package sqlite

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"testing"
)

func TestRuntimeAuthorityReadSeparatesTelemetryFailure(t *testing.T) {
	f := newAgentStorageFixture(t, "authority-read")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "authority-read", true)
	r.ThreadID = "thread-authority-read"
	r.TurnID = "turn-authority-read"
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

	if err := session.BeforeDispatch(q.RequestID); err != nil {
		t.Fatal(err)
	}
	session.Delivered(q.RequestID, nil)
	runs := run.NewService(NewRunStore(f.db), nil, nil)
	runs.AgentRuntime = svc
	assertUnknown := func() {
		t.Helper()
		current, err := runs.GetForRuntimeAuthority(ctx, r.ID)
		if err != nil || current.AgentRuntime == nil || !agentruntime.HasUnresolvedResponse(*current.AgentRuntime) {
			t.Fatalf("lost unknown response authority: %+v %v", current.AgentRuntime, err)
		}
	}
	assertUnknown()
	t.Run("telemetry-unavailable", func(t *testing.T) {
		if _, err := f.db.sql.Exec(`ALTER TABLE agent_process_launches RENAME TO test_unavailable_process_launches`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := f.db.sql.Exec(`ALTER TABLE test_unavailable_process_launches RENAME TO agent_process_launches`); err != nil {
				t.Fatal(err)
			}
		}()
		display, err := runs.Get(ctx, r.ID)
		if err != nil || display.AgentRuntime == nil || display.AgentRuntime.Availability != "UNAVAILABLE" {
			t.Fatalf("display fallback unavailable: %+v %v", display, err)
		}
		assertUnknown()
	})
	var requestJSON, snapshotJSON string
	if err := f.db.sql.QueryRow(`SELECT request_json FROM codex_server_requests WHERE run_id=?`, r.ID).Scan(&requestJSON); err != nil {
		t.Fatal(err)
	}
	if err := f.db.sql.QueryRow(`SELECT snapshot_json FROM agent_runtime_states WHERE run_id=?`, r.ID).Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"request-malformed", "request-wrong-owner", "snapshot-wrong-shape", "snapshot-missing-request", "snapshot-duplicate-request", "request-table-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			switch mode {
			case "request-malformed":
				if _, err := f.db.sql.Exec(`UPDATE codex_server_requests SET request_json='{"state":123}' WHERE run_id=?`, r.ID); err != nil {
					t.Fatal(err)
				}
			case "request-wrong-owner":
				if _, err := f.db.sql.Exec(`UPDATE codex_server_requests SET request_json=json_set(request_json,'$.runId','other-run') WHERE run_id=?`, r.ID); err != nil {
					t.Fatal(err)
				}
			case "snapshot-wrong-shape":
				if _, err := f.db.sql.Exec(`UPDATE agent_runtime_states SET snapshot_json='{}' WHERE run_id=?`, r.ID); err != nil {
					t.Fatal(err)
				}
			case "snapshot-missing-request":
				if _, err := f.db.sql.Exec(`UPDATE agent_runtime_states SET snapshot_json=json_set(snapshot_json,'$.requests',json('[]')) WHERE run_id=?`, r.ID); err != nil {
					t.Fatal(err)
				}
			case "snapshot-duplicate-request":
				if _, err := f.db.sql.Exec(`UPDATE agent_runtime_states SET snapshot_json=json_insert(snapshot_json,'$.requests[#]',json_extract(snapshot_json,'$.requests[0]')) WHERE run_id=?`, r.ID); err != nil {
					t.Fatal(err)
				}
			case "request-table-unavailable":
				if _, err := f.db.sql.Exec(`ALTER TABLE codex_server_requests RENAME TO test_unavailable_requests`); err != nil {
					t.Fatal(err)
				}
			}
			defer func() {
				if mode == "request-table-unavailable" {
					if _, err := f.db.sql.Exec(`ALTER TABLE test_unavailable_requests RENAME TO codex_server_requests`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.db.sql.Exec(`UPDATE codex_server_requests SET request_json=? WHERE run_id=?`, requestJSON, r.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.sql.Exec(`UPDATE agent_runtime_states SET snapshot_json=? WHERE run_id=?`, snapshotJSON, r.ID); err != nil {
					t.Fatal(err)
				}
			}()
			if current, err := runs.GetForRuntimeAuthority(ctx, r.ID); err == nil || current.AgentRuntime != nil {
				t.Fatalf("bad request state became empty authority: %+v %v", current, err)
			}
			tx, err := f.db.beginWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			guardErr := requireResolvedResponsesTx(ctx, tx, r.ID)
			_ = tx.Rollback()
			if guardErr == nil {
				t.Fatal("unreadable request history released the canonical finalization guard")
			}
		})
	}
	assertUnknown()
}
