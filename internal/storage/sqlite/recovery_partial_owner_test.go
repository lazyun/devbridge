package sqlite

import (
	"context"
	"errors"
	"testing"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Direct reducer coverage: even an old or incorrectly assessed PARTIAL record
// cannot make the owner-specific reducer close a Verification/Review source.
func TestPartialRecoveryReducerRejectsOtherTaskOwners(t *testing.T) {
	for _, purpose := range []run.Purpose{run.PurposeVerification, run.PurposeScopeReview} {
		t.Run(string(purpose), func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				record, _ = prepareDiagnosticResolutionTest(t, f, ctx, record, source, "RECOVERED_PARTIAL")
				source.Purpose = purpose
				if purpose == run.PurposeScopeReview {
					source.OperationKind = run.OperationReview
				}
				tx, err := f.db.beginWrite(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err = store.resolvePartialTx(ctx, tx, &record, source); !errors.Is(err, rs.ErrIneligible) {
					t.Fatalf("unsupported owner entered partial reducer: %v", err)
				}
			})
		})
	}
}

func TestPartialContinuationRejectsHistoricalVerificationSource(t *testing.T) {
	runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
		_, in := prepareDiagnosticResolutionTest(t, f, ctx, record, source, "RECOVERED_PARTIAL")
		if _, err := store.Resolve(ctx, in); err != nil {
			t.Fatal(err)
		}
		current, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		predecessor, err := NewExecutionProposalStore(f.db).Get(ctx, source.ExecutionProposalID)
		if err != nil {
			t.Fatal(err)
		}
		command := wp.TaskAcceptanceInput{Option: wp.AcceptanceRetryImplementation, RecoveryStatusID: record.ID, RecoveryContinuationInstruction: "Continue remaining approved work"}
		check := func(want error) {
			t.Helper()
			tx, err := f.db.beginWrite(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = validateRecoveryContinuationTx(ctx, tx, current, predecessor, command)
			if !errors.Is(err, want) {
				t.Fatalf("continuation eligibility: %v want %v", err, want)
			}
		}
		check(nil)
		// Represent a historical misclassified source; do not change its receipt
		// into a newly authorized Implementation recovery.
		purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runs")
		if _, err = f.db.sql.ExecContext(ctx, `UPDATE runs SET purpose='VERIFICATION' WHERE id=?`, source.ID); err != nil {
			t.Fatal(err)
		}
		check(rs.ErrConflict)
	})
}
