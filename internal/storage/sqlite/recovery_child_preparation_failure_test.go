package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

type preparationCheckpointComplete struct{}

// Stop the shared canonical fixture at its observation checkpoint, before it
// would bind and dispatch the child. Only this private sentinel is recovered.
func atRecoveryPreparationObservation(t *testing.T, check func(*agentStorageFixture, rs.Record, runtimepreparation.FailureSettlement)) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(preparationCheckpointComplete); !ok {
				panic(p)
			}
		}
	}()
	runCanonicalRecoveryChildFixtureStages(t, nil, func(at string, f *agentStorageFixture, r *rs.Record, child *run.Run) {
		if at != "observed" {
			return
		}
		ctx := context.Background()
		current, err := NewRecoveryStatusStore(f.db).Read(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		settlement := runtimepreparation.FailureSettlement{RunID: child.ID, RuntimeEpoch: child.RuntimeEpoch, Stage: "MEMBER_OBSERVATION", Code: "RECOVERY_PREPARATION_FAILED", Message: "definite observation failure", At: time.Now().UTC()}
		if err = f.db.sql.QueryRow(`SELECT plan_id,request_id,payload_hash FROM runtime_workspace_preparation_claims WHERE run_id=?`, child.ID).Scan(&settlement.PlanID, &settlement.RequestID, &settlement.PayloadHash); err != nil {
			t.Fatal(err)
		}
		rows, err := f.db.sql.Query(`SELECT m.id,w.id,w.path FROM workspace_members m JOIN worktrees w ON w.id=m.worktree_id WHERE w.run_id=?`, child.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a runtimepreparation.FailureArtifact
			if err = rows.Scan(&a.MemberID, &a.WorktreeID, &a.Root); err != nil {
				t.Fatal(err)
			}
			settlement.Artifacts = append(settlement.Artifacts, a)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		check(f, current, settlement)
		panic(preparationCheckpointComplete{})
	})
}

func TestRecoveryPreparationFailureExactIdentity(t *testing.T) {
	atRecoveryPreparationObservation(t, func(f *agentStorageFixture, r rs.Record, settlement runtimepreparation.FailureSettlement) {
		ctx := context.Background()
		store := NewRunStore(f.db)
		for _, name := range []string{"recovery", "generation", "run", "plan", "request", "payload", "epoch", "artifact"} {
			bad := settlement
			bad.Artifacts = append([]runtimepreparation.FailureArtifact(nil), settlement.Artifacts...)
			id, generation := r.ID, r.Spec.Generation
			switch name {
			case "recovery":
				id += "-foreign"
			case "generation":
				generation++
			case "run":
				bad.RunID = r.Spec.RunID
			case "plan":
				bad.PlanID += "-foreign"
			case "request":
				bad.RequestID += "-foreign"
			case "payload":
				bad.PayloadHash = rs.Hash([]byte("foreign"))
			case "epoch":
				bad.RuntimeEpoch += "-foreign"
			case "artifact":
				bad.Artifacts[0].Root += "-foreign"
			}
			if err := store.SettleRecoveryPreparationFailure(ctx, id, generation, bad); err == nil {
				t.Fatalf("accepted %s identity", name)
			}
			var count int
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM recovery_child_preparation_failures`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rejection wrote failure %d %v", count, err)
			}
		}
		for range 2 {
			if err := store.SettleRecoveryPreparationFailure(ctx, r.ID, r.Spec.Generation, settlement); err != nil {
				t.Fatal(err)
			}
		}
		bad := settlement
		bad.Message = "changed failure"
		if err := store.SettleRecoveryPreparationFailure(ctx, r.ID, r.Spec.Generation, bad); err == nil {
			t.Fatal("failure overwritten")
		}
		for _, query := range []string{`UPDATE recovery_child_preparation_failures SET message='changed'`, `DELETE FROM recovery_child_preparation_failures`} {
			if _, err := f.db.sql.Exec(query); err == nil {
				t.Fatal("failure history mutated")
			}
		}
		if got, err := NewRecoveryStatusStore(f.db).Adopt(ctx, r.ID); err != nil || got.RuntimeEpoch != r.RuntimeEpoch || got.State != "FAILED" {
			t.Fatalf("same-epoch readback revived failed preparation: %+v %v", got, err)
		}
		var n int
		if err := f.db.sql.QueryRow(`SELECT count(*) FROM recovery_child_thread_attempts`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("failure invented thread %d %v", n, err)
		}
	})
}

func TestRecoveryPreparationAbandonmentColdExactEvidence(t *testing.T) {
	atRecoveryPreparationObservation(t, func(f *agentStorageFixture, r rs.Record, settlement runtimepreparation.FailureSettlement) {
		ctx := context.Background()
		// Simulate a slow returned preparation: the original source admission is
		// now historical, but no child dispatch has ever been attempted.
		r.Admission.ObservedAt = time.Now().Add(-2 * time.Hour)
		tx, err := f.db.beginWrite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = saveRecoveryTx(ctx, tx, r); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := NewRunStore(f.db).SettleRecoveryPreparationFailure(ctx, r.ID, r.Spec.Generation, settlement); err != nil {
			t.Fatal(err)
		}
		if err := f.db.Close(); err != nil {
			t.Fatal(err)
		}
		f.db = openTestDB(t, f.root)
		defer f.db.Close()
		store := NewRecoveryStatusStore(f.db)
		if _, err := store.Adopt(ctx, r.ID); err == nil {
			t.Fatal("settled failure adopted into a new epoch")
		}
		in := executioncancel.Input{ProjectID: r.Spec.ProjectID, AISessionID: f.session.ID, OwnerType: r.Spec.OperationKind, OwnerID: r.Spec.OperationID, RunID: r.RecoveryRunID, RequestID: "abandon-cold"}
		closed, receipt, dispatch, err := store.BeginCancel(ctx, r.ID, in)
		if err != nil || dispatch || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" || receipt.BackendInterruptState != "NOT_SENT" || receipt.QuiescenceState != "QUIESCED" || closed.RuntimeEpoch != r.RuntimeEpoch {
			t.Fatalf("cancel=%+v dispatch=%v err=%v", receipt, dispatch, err)
		}
		if err = validateIndependentRecoveryCancellationTx(ctx, f.db.sql, closed); err != nil {
			t.Fatal("abandonment evidence invalid", err)
		}
		_, replay, dispatch, err := store.BeginCancel(ctx, r.ID, in)
		if err != nil || dispatch || !reflect.DeepEqual(receipt, replay) {
			t.Fatalf("replay=%+v %v", replay, err)
		}
		in.Reason = "changed"
		if _, _, _, err = store.BeginCancel(ctx, r.ID, in); err == nil {
			t.Fatal("request conflict accepted")
		}
		for _, query := range []string{`UPDATE recovery_child_preparation_abandonments SET payload_hash=printf('%064d',0)`, `DELETE FROM recovery_child_preparation_abandonments`} {
			if _, err = f.db.sql.Exec(query); err == nil {
				t.Fatal("abandonment history mutated")
			}
		}
		var after string
		if err = f.db.sql.QueryRow(`SELECT artifact_inventory_json FROM recovery_child_preparation_failures WHERE recovery_id=?`, r.ID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		var inventory []runtimepreparation.FailureArtifact
		if err = json.Unmarshal([]byte(after), &inventory); err != nil || !reflect.DeepEqual(inventory, settlement.Artifacts) {
			t.Fatal("artifact history changed", err)
		}
	})
}
