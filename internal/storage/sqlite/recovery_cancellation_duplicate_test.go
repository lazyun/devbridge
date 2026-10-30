package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func TestRecoveryCancellationRejectsNewRequestWhileInterruptUnsettled(t *testing.T) {
	for _, outcome := range []string{"REQUESTED", "ACKNOWLEDGED", "OUTCOME_UNKNOWN"} {
		t.Run(outcome, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: source.ID, RequestID: "original-cancel"}
				if _, _, dispatch, err := store.BeginCancel(ctx, record.ID, in); err != nil || !dispatch {
					t.Fatalf("first cancellation: dispatch=%v err=%v", dispatch, err)
				}
				if outcome != "REQUESTED" {
					if _, err := store.RecordCancelInterrupt(ctx, record.ID, in, outcome); err != nil {
						t.Fatal(err)
					}
				}
				before, err := NewExecutionCancellationStore(f.db).Get(ctx, in.ProjectID, in.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				for _, runID := range []string{source.ID, child.ID} {
					other := in
					other.RunID, other.RequestID = runID, "different-request"
					if _, _, dispatch, err := store.BeginCancel(ctx, record.ID, other); !errors.Is(err, rs.ErrIneligible) || dispatch {
						t.Fatalf("pending cancellation did not reject new request: run=%s dispatch=%v err=%v", runID, dispatch, err)
					}
				}
				var count int
				if err := f.db.sql.QueryRow(`SELECT count(*) FROM execution_cancellations WHERE run_id=?`, child.ID).Scan(&count); err != nil || count != 1 {
					t.Fatalf("conflict wrote a second receipt: %d %v", count, err)
				}
				if _, replay, dispatch, err := store.BeginCancel(ctx, record.ID, in); err != nil || dispatch || !reflect.DeepEqual(before, replay) {
					t.Fatalf("duplicate rejection broke exact retry: %+v dispatch=%v %v", replay, dispatch, err)
				}
				closed, receipt, err := store.FinishCancel(ctx, record.ID, in, record.Admission)
				if err != nil || receipt.State != "CANCELED" || receipt.BackendInterruptState != outcome {
					t.Fatalf("original cancellation could not settle: %+v %v", receipt, err)
				}
				if err = validateIndependentRecoveryCancellationTx(ctx, f.db.sql, closed); err != nil {
					t.Fatal("original proof did not release source", err)
				}
			})
		})
	}
}

func TestConcurrentRecoveryCancellationsHaveOneInterruptOwner(t *testing.T) {
	runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
		type result struct {
			in       executioncancel.Input
			dispatch bool
			err      error
		}
		start, results := make(chan struct{}), make(chan result, 2)
		for _, key := range []string{"cancel-a", "cancel-b"} {
			go func(key string) {
				<-start
				in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: string(source.OperationKind), OwnerID: source.OperationID, RunID: child.ID, RequestID: key}
				_, _, dispatch, err := store.BeginCancel(ctx, record.ID, in)
				results <- result{in, dispatch, err}
			}(key)
		}
		close(start)
		var winner executioncancel.Input
		wins, conflicts := 0, 0
		for i := 0; i < 2; i++ {
			got := <-results
			if got.err == nil && got.dispatch {
				wins++
				winner = got.in
			} else if errors.Is(got.err, rs.ErrIneligible) && !got.dispatch {
				conflicts++
			} else {
				t.Fatalf("unexpected concurrent cancellation: %+v", got)
			}
		}
		if wins != 1 || conflicts != 1 {
			t.Fatalf("interrupt owners=%d conflicts=%d", wins, conflicts)
		}
		if _, _, dispatch, err := store.BeginCancel(ctx, record.ID, winner); err != nil || dispatch {
			t.Fatalf("winner replay resent interrupt: %v %v", dispatch, err)
		}
	})
}
