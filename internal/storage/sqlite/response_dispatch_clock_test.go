package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"testing"
	"time"
)

func TestResponseDispatchRecoveryUsesCommittedOrder(t *testing.T) {
	for _, mode := range []string{"rollback", "equal", "rollback-after-proof"} {
		t.Run(mode, func(t *testing.T) {
			f := newAgentStorageFixture(t, "clock-order")
			r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "clock-order", true)
			r.ThreadID = "thread-clock-order"
			r.TurnID = "turn-clock-order"
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

			v, e = st.Read(ctx, r.ID)
			if e != nil {
				t.Fatal(e)
			}
			now := time.Now().UTC()
			save := func() {
				t.Helper()
				expected := v.Sequence
				v.Sequence++
				if err := st.Save(ctx, expected, v, true); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 3; i++ {
				q := &v.Requests[0]
				at := now.Add(time.Duration(i) * time.Hour)
				if mode == "rollback" {
					at = now.Add(-time.Duration(i) * time.Hour)
				}
				if mode == "equal" {
					at = now
				}
				evidenceID := ""
				if i > 0 {
					evidenceID = fmt.Sprintf("proof-%d", i)
					observedAt := at.Add(-time.Second)
					if mode == "rollback-after-proof" {
						observedAt = at.Add(time.Hour)
					}
					proof := agentruntime.ResponseRecoveryEvidence{Identity: q.Identity, ID: evidenceID, RuntimeRequestID: q.ID, BackendRequestID: string(q.RequestID), Kind: "PRE_EFFECT_REJECTED", AttemptID: q.DispatchAttempts[i-1].ID, PayloadSHA256: q.DispatchAttempts[i-1].PayloadSHA256, ObservedAt: observedAt}
					q.RecoveryEvidence = append(q.RecoveryEvidence, proof)
					q.State = "USER_DECISION_RECORDED"
					q.Delivery = "NOT_DISPATCHED"
					save()
				}
				hash := sha256.Sum256(q.Resolution)
				attempt := agentruntime.ResponseDispatchAttempt{Identity: q.Identity, ID: fmt.Sprintf("attempt-%d", i), RuntimeRequestID: q.ID, UserDecisionID: d.ID, BackendRequestID: string(q.RequestID), ItemID: q.ItemID, CommandRequestID: d.SubmissionRequestID, Payload: q.Resolution, PayloadSHA256: hex.EncodeToString(hash[:]), RecoveryEvidenceID: evidenceID, StartedAt: at}
				q.DispatchAttempts = append(q.DispatchAttempts, attempt)
				q.State = "RESPONDING"
				q.Delivery = "DISPATCHING"
				save()
			}
			persisted, err := st.Read(ctx, r.ID)
			if err != nil || len(persisted.Requests[0].DispatchAttempts) != 3 {
				t.Fatalf("dispatch history missing: %v", err)
			}
			// Clock-independent ordering must not permit a caller to reorder or drop
			// committed attempts and thereby make older recovery evidence current.
			for _, mutation := range []string{"reorder", "truncate", "duplicate", "reorder-evidence", "truncate-evidence", "duplicate-evidence", "stale-proof", "reused-proof"} {
				t.Run(mutation, func(t *testing.T) {
					data, _ := json.Marshal(persisted)
					var forged agentruntime.Snapshot
					if err := json.Unmarshal(data, &forged); err != nil {
						t.Fatal(err)
					}
					q := &forged.Requests[0]
					attempts := q.DispatchAttempts
					wantErr := agentruntime.ErrInvalid
					switch mutation {
					case "reorder":
						attempts[0], attempts[1] = attempts[1], attempts[0]
					case "truncate":
						attempts = attempts[:1]
					case "duplicate":
						attempts = append(attempts, attempts[0])
					case "reorder-evidence":
						q.RecoveryEvidence[0], q.RecoveryEvidence[1] = q.RecoveryEvidence[1], q.RecoveryEvidence[0]
					case "truncate-evidence":
						q.RecoveryEvidence = q.RecoveryEvidence[:1]
					case "duplicate-evidence":
						q.RecoveryEvidence = append(q.RecoveryEvidence, q.RecoveryEvidence[0])
					case "stale-proof", "reused-proof":
						wantErr = agentruntime.ErrResponseDispositionUnknown
						proof := q.RecoveryEvidence[0]
						if mutation == "stale-proof" {
							proof.ID = "new-proof-for-old-attempt"
							q.RecoveryEvidence = append(q.RecoveryEvidence, proof)
						}
						attempt := attempts[len(attempts)-1]
						attempt.ID = "unproven-next-attempt"
						attempt.RecoveryEvidenceID = proof.ID
						attempts = append(attempts, attempt)
					}
					forged.Requests[0].DispatchAttempts = attempts
					forged.Sequence++
					if err := st.Save(ctx, persisted.Sequence, forged, false); !errors.Is(err, wantErr) {
						t.Fatalf("altered history admitted: %v", err)
					}
				})
			}

		})
	}
}
