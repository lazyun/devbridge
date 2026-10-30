package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func TestIndependentRecoveryCancellationPreservesSourceConversation(t *testing.T) {
	runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
		conversations := NewAgentConversationStore(f.db)
		before, err := conversations.Get(ctx, record.Spec.AgentConversationID)
		if err != nil {
			t.Fatal(err)
		}
		boundary, err := conversations.Boundary(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-independent-child", Reason: "Stop diagnostic"}
		_, _, dispatch, err := store.BeginCancel(ctx, record.ID, in)
		if err != nil || !dispatch {
			t.Fatalf("begin dispatch=%v err=%v", dispatch, err)
		}
		var thread, turn string
		if err = f.db.sql.QueryRowContext(ctx, `SELECT thread_id,turn_id FROM execution_cancellations WHERE project_id=? AND request_id=?`, f.project.ID, in.RequestID).Scan(&thread, &turn); err != nil {
			t.Fatal(err)
		}
		if thread != child.ThreadID || turn != child.TurnID {
			t.Fatalf("wrong interrupt identity: (%s,%s) child=(%s,%s)", thread, turn, child.ThreadID, child.TurnID)
		}
		_, receipt, err := store.FinishCancel(ctx, record.ID, in, record.Admission)
		if err != nil || receipt.State != "CANCELED" || receipt.QuiescenceState != "QUIESCED" {
			t.Fatalf("finish=%+v err=%v", receipt, err)
		}
		after, err := conversations.Get(ctx, record.Spec.AgentConversationID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("source conversation changed: before=%+v after=%+v err=%v", before, after, err)
		}
		afterBoundary, err := conversations.Boundary(ctx, source.ID)
		if err != nil || !reflect.DeepEqual(boundary, afterBoundary) {
			t.Fatalf("source boundary changed: before=%+v after=%+v err=%v", boundary, afterBoundary, err)
		}
		_, replay, dispatch, err := store.BeginCancel(ctx, record.ID, in)
		if err != nil || dispatch || !reflect.DeepEqual(receipt, replay) {
			t.Fatalf("begin replay=%+v dispatch=%v err=%v", replay, dispatch, err)
		}
		if _, replay, err = store.FinishCancel(ctx, record.ID, in, rs.Admission{}); err != nil || !reflect.DeepEqual(receipt, replay) {
			t.Fatalf("finish replay=%+v err=%v", replay, err)
		}
		in.Reason += " changed"
		if _, _, _, err = store.BeginCancel(ctx, record.ID, in); err == nil {
			t.Fatal("changed cancellation replay accepted")
		}
		state, err := readRuntimeAuthority(ctx, f.db.sql, source.FeatureAreaID)
		if err != nil || state.Busy || state.RecoveryRequired {
			t.Fatalf("completed cancellation did not release exact source fence: %+v %v", state, err)
		}
		if err = f.db.Close(); err != nil {
			t.Fatal(err)
		}
		cold := openTestDB(t, f.root)
		defer cold.Close()
		state, err = readRuntimeAuthority(ctx, cold.sql, source.FeatureAreaID)
		if err != nil || state.Busy || state.RecoveryRequired {
			t.Fatalf("cold canceled authority: %+v %v", state, err)
		}
		after, err = NewAgentConversationStore(cold).Get(ctx, before.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("cold projection rewrote source conversation: %+v %v", after, err)
		}
	})
}

func TestIndependentRecoveryCancellationCannotHideOtherUncertainty(t *testing.T) {
	for _, corruption := range []string{"other-run", "later-boundary", "receipt", "child-not-terminal"} {
		t.Run(corruption, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-with-independent-uncertainty"}
				if _, _, _, err := store.BeginCancel(ctx, record.ID, in); err != nil {
					t.Fatal(err)
				}
				if _, _, err := store.FinishCancel(ctx, record.ID, in, record.Admission); err != nil {
					t.Fatal(err)
				}
				var err error
				switch corruption {
				case "other-run":
					_, err = f.db.sql.ExecContext(ctx, `INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'UNKNOWN','independent uncertainty',?)`, child.ID, f.db.epoch)
				case "later-boundary":
					_, err = f.db.sql.ExecContext(ctx, `INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,advance_state,quiesced) VALUES(?,?,'UNKNOWN',1)`, child.ID, record.Spec.AgentConversationID)
				case "receipt":
					if _, blocked := f.db.sql.ExecContext(ctx, `UPDATE execution_cancellations SET result_json='{}' WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID); blocked == nil {
						t.Fatal("completed cancellation receipt was mutable")
					}
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "execution_cancellations")
					_, err = f.db.sql.ExecContext(ctx, `UPDATE execution_cancellations SET result_json='{}' WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID)
				case "child-not-terminal":
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runs")
					_, err = f.db.sql.ExecContext(ctx, `UPDATE runs SET status='RUNNING',finished_at=NULL WHERE id=?`, child.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				state, err := readRuntimeAuthority(ctx, f.db.sql, source.FeatureAreaID)
				if err == nil && (!state.Busy || !state.RecoveryRequired) {
					t.Fatalf("cancellation concealed independent uncertainty: %+v", state)
				}
			})
		})
	}
}

func TestFailedRecoveryCancellationAcrossRestartPreservesFailureEpoch(t *testing.T) {
	for _, cold := range []bool{false, true} {
		name := "warm"
		if cold {
			name = "cold"
		}
		t.Run(name, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				failed, err := store.Fail(ctx, record.ID, record.RuntimeEpoch, "invalid terminal report")
				if err != nil || failed.State != "FAILED" {
					t.Fatalf("fail=%+v err=%v", failed, err)
				}
				if cold {
					if err = f.db.Close(); err != nil {
						t.Fatal(err)
					}
					f.db = openTestDB(t, f.root)
					defer f.db.Close()
					store = NewRecoveryStatusStore(f.db)
					if _, err = store.Adopt(ctx, record.ID); err == nil {
						t.Fatal("failed diagnostic became adoptable")
					}
				}
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-failed-child", Reason: "Explicitly close failed diagnostic"}
				v, _, dispatch, err := store.BeginCancel(ctx, record.ID, in)
				if err != nil || !dispatch {
					t.Fatalf("begin dispatch=%v err=%v", dispatch, err)
				}
				if v.RuntimeEpoch != failed.RuntimeEpoch || v.State != "FAILED" || v.FailureReason != failed.FailureReason {
					t.Fatalf("cancellation revived or rewrote failed diagnostic: %+v", v)
				}
				var epoch string
				if err = f.db.sql.QueryRowContext(ctx, `SELECT runtime_epoch FROM execution_cancellations WHERE project_id=? AND request_id=?`, f.project.ID, in.RequestID).Scan(&epoch); err != nil || epoch != f.db.epoch {
					t.Fatalf("cancellation writer epoch=%s err=%v", epoch, err)
				}
				closed, receipt, err := store.FinishCancel(ctx, record.ID, in, record.Admission)
				if err != nil || receipt.State != "CANCELED" || closed.RuntimeEpoch != failed.RuntimeEpoch {
					t.Fatalf("finish=%+v record=%+v err=%v", receipt, closed, err)
				}
				got, err := NewRunStore(f.db).Get(ctx, child.ID)
				if err != nil || got.Status != run.StatusCanceled || got.RuntimeEpoch != child.RuntimeEpoch || got.ThreadID != child.ThreadID || got.TurnID != child.TurnID {
					t.Fatalf("child re-executed or re-epoched: %+v err=%v", got, err)
				}
				var adoptions int
				if err = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM recovery_status_adoptions WHERE recovery_id=?`, record.ID).Scan(&adoptions); err != nil || adoptions != 0 {
					t.Fatalf("cancellation minted runtime adoption: %d %v", adoptions, err)
				}
			})
		})
	}
}

func TestIndependentRecoveryCancellationRejectsInexactChild(t *testing.T) {
	for _, stage := range []string{"begin", "finish"} {
		for _, corruption := range []string{"thread", "turn", "owner", "receipt"} {
			t.Run(stage+"/"+corruption, func(t *testing.T) {
				runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
					in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-inexact-child", Reason: "Stop diagnostic"}
					if stage == "finish" {
						if _, _, _, err := store.BeginCancel(ctx, record.ID, in); err != nil {
							t.Fatal(err)
						}
					}
					var err error
					if corruption == "receipt" {
						purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "recovery_child_turn_receipts")
						_, err = f.db.sql.ExecContext(ctx, `UPDATE recovery_child_turn_receipts SET payload_sha256=?`, rs.Hash([]byte("wrong receipt")))
					} else {
						purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runs")
						column, value := "thread_id", source.ThreadID
						if corruption == "turn" {
							column, value = "turn_id", "foreign-turn"
						}
						if corruption == "owner" {
							column, value = "operation_id", "foreign-operation"
						}
						_, err = f.db.sql.ExecContext(ctx, `UPDATE runs SET `+column+`=? WHERE id=?`, value, child.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					before, err := NewAgentConversationStore(f.db).Get(ctx, record.Spec.AgentConversationID)
					if err != nil {
						t.Fatal(err)
					}
					if stage == "begin" {
						_, _, _, err = store.BeginCancel(ctx, record.ID, in)
					} else {
						_, _, err = store.FinishCancel(ctx, record.ID, in, record.Admission)
					}
					if err == nil {
						t.Fatal("inexact child cancellation accepted")
					}
					after, err := NewAgentConversationStore(f.db).Get(ctx, before.ID)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("rejected cancel changed source: %+v %v", after, err)
					}
				})
			})
		}
	}
}

func TestColdRecoveryCancellationDoesNotAdoptActiveDiagnostic(t *testing.T) {
	runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
		if err := f.db.Close(); err != nil {
			t.Fatal(err)
		}
		cold := openTestDB(t, f.root)
		defer cold.Close()
		in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "cancel-old-active-child"}
		if _, _, _, err := NewRecoveryStatusStore(cold).BeginCancel(ctx, record.ID, in); err != rs.ErrStale {
			t.Fatalf("active diagnostic bypassed ordinary adoption: %v", err)
		}
		var count int
		if err := cold.sql.QueryRowContext(ctx, `SELECT count(*) FROM execution_cancellations WHERE project_id=? AND request_id=?`, f.project.ID, in.RequestID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rejected cancellation wrote receipt: %d %v", count, err)
		}
	})
}
