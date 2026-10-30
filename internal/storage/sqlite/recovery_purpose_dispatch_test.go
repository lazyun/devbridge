package sqlite

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

func TestRecoveryPurposeCoreDrainAllOwners(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION", "REVIEW"} {
		t.Run(owner, func(t *testing.T) {
			runPurposeBeforeCommit(t, owner, func(t *testing.T, db *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, _ rs.Record, intent confirmation.Intent) confirmation.Intent {
				consumer := NewRecoveryPurposeStore(db)
				before := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID)
				if _, err := consumer.ContinueConfirmedPurposes(context.Background(), 0); err != nil {
					t.Fatal(err)
				}
				if got := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID); got != before {
					t.Fatal("pending unconfirmed purpose produced business effects")
				}
				intent = commitPurposeForTest(t, db, ctx, intent, "core-drain-confirm")
				var runsBefore int
				if err := db.sql.QueryRow(`SELECT count(*) FROM runs`).Scan(&runsBefore); err != nil {
					t.Fatal(err)
				}
				// The worker has no caller scope and does not impersonate the user.
				if _, err := consumer.ContinueConfirmedPurposes(context.Background(), 0); err != nil {
					t.Fatal(err)
				}
				var in rs.PurposeConsumeInput
				var actualOwner string
				if err := db.sql.QueryRow(`SELECT intent_ai_session_id,intent_id,intent_version,intent_content_hash,consume_action,consume_request_id,consume_payload_hash,source_owner_kind FROM recovery_purpose_confirmation_consumptions`).Scan(&in.AISessionID, &in.IntentID, &in.Version, &in.ContentHash, &in.Action, &in.RequestID, &in.PayloadHash, &actualOwner); err != nil {
					t.Fatal(err)
				}
				if actualOwner != owner || !strings.HasPrefix(in.RequestID, "core_recovery_purpose_") || in.PayloadHash != in.ComputePayloadHash() {
					t.Fatalf("wrong Core receipt: %+v owner=%s", in, actualOwner)
				}
				if _, err := consumer.consume(context.Background(), in, owner, in.Action); !errors.Is(err, aisession.ErrScopeMismatch) {
					t.Fatalf("public caller authentication bypassed: %v", err)
				}
				out, err := consumer.consume(ctx, in, owner, in.Action)
				if err != nil || !out.Replay || out.RunID != "" {
					t.Fatalf("original exact consume replay: %+v %v", out, err)
				}
				var runsAfter, worksets, starts int
				if err = db.sql.QueryRow(`SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM runtime_workset_bindings WHERE owner_id IN (?,?)),(SELECT count(*) FROM runtime_start_authorities WHERE owner_id IN (?,?))`, out.ExecutionID, out.ReviewID, out.ExecutionID, out.ReviewID).Scan(&runsAfter, &worksets, &starts); err != nil || runsAfter != runsBefore || worksets != 0 || starts != 0 {
					t.Fatalf("consume exceeded preparation-only effects runs=%d/%d worksets=%d starts=%d err=%v", runsBefore, runsAfter, worksets, starts, err)
				}
				settled := purposeConsumptionSnapshot(t, ctx, db, out.TaskID)
				if _, err = consumer.ContinueConfirmedPurposes(context.Background(), 0); err != nil {
					t.Fatal(err)
				}
				if got := purposeConsumptionSnapshot(t, ctx, db, out.TaskID); got != settled {
					t.Fatal("repeated Core drain changed settled facts")
				}
				return confirmation.Intent{}
			})
		})
	}
}

func TestRecoveryPurposeCoreDrainRecoversReceiptCompletionAcrossRestart(t *testing.T) {
	runPurposeBeforeCommit(t, "IMPLEMENTATION", func(t *testing.T, db *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, _ rs.Record, intent confirmation.Intent) confirmation.Intent {
		store := NewConfirmationStore(db)
		commit := confirmation.CommitInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "crash-after-purpose-receipt", ContentHash: intent.ContentHash, VariantID: "revised"}
		commit.SubmittedHash = confirmation.ValueHash(commit)
		step := confirmation.Step{Kind: "WRITE_PURPOSE_RECEIPT", RequestID: "crash-purpose-step", State: "PENDING"}
		if _, _, err := store.BeginCommit(ctx, commit, []confirmation.Step{step}); err != nil {
			t.Fatal(err)
		}
		step.State = "UNKNOWN"
		if _, err := store.RecordStep(ctx, intent.AISessionID, intent.IntentID, intent.Version, step, "CONFIRMING"); err != nil {
			t.Fatal(err)
		}
		if err := store.WriteRecoveryPurposeReceipt(ctx, commit); err != nil {
			t.Fatal(err)
		}
		root := db.stateRoot
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		cold := openTestDB(t, root)
		defer cold.Close()
		if _, err := NewRecoveryPurposeStore(cold).ContinueConfirmedPurposes(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
		completed, err := NewConfirmationStore(cold).Get(ctx, intent.AISessionID, intent.IntentID)
		if err != nil || completed.State != "COMPLETED" || completed.Steps[0].Effect != "COMMAND_COMPLETED" {
			t.Fatalf("cold receipt completion: %+v %v", completed, err)
		}
		var count int
		if err = cold.sql.QueryRow(`SELECT count(*) FROM recovery_purpose_confirmation_consumptions`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("cold receipt not consumed exactly once: %d %v", count, err)
		}
		return confirmation.Intent{}
	})
}

func TestRecoveryPurposeCoreDrainRetriesAtomicFailureAndConcurrentWorkers(t *testing.T) {
	runPurposeBeforeCommit(t, "IMPLEMENTATION", func(t *testing.T, db *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, _ rs.Record, intent confirmation.Intent) confirmation.Intent {
		intent = commitPurposeForTest(t, db, ctx, intent, "core-retry-confirm")
		consumer := NewRecoveryPurposeStore(db)
		before := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID)
		if _, err := db.sql.Exec(`CREATE TRIGGER core_purpose_failure BEFORE INSERT ON execution_proposal_admissions BEGIN SELECT RAISE(ABORT,'injected consume failure'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := consumer.ContinueConfirmedPurposes(context.Background(), 0); err == nil {
			t.Fatal("injected consumption failure ignored")
		}
		if got := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID); got != before {
			t.Fatal("failed drain leaked partial business effects")
		}
		if _, err := db.sql.Exec(`DROP TRIGGER core_purpose_failure`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := consumer.ContinueConfirmedPurposes(context.Background(), 0); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		var count int
		if err := db.sql.QueryRow(`SELECT count(*) FROM recovery_purpose_confirmation_consumptions`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("concurrent drain count=%d err=%v", count, err)
		}
		return confirmation.Intent{}
	})
}

func TestRecoveryPurposeCoreDrainSkipsOldGenerationAndContinuesCurrentReceipt(t *testing.T) {
	runPurposeBeforeCommit(t, "IMPLEMENTATION", func(t *testing.T, db *DB, ctx context.Context, store *RecoveryStatusStore, spec rs.Spec, record rs.Record, old confirmation.Intent) confirmation.Intent {
		old = commitPurposeForTest(t, db, ctx, old, "core-old-generation-confirm")
		spec.RequestID = "core-new-generation"
		spec.PayloadHash = spec.ComputePayloadHash()
		next, err := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
		if err != nil {
			t.Fatal(err)
		}
		next.State, next.Report, next.After, next.Assessment, next.Admission = "ASSESSED", record.Report, record.After, record.Assessment, record.Admission
		next.ReportHash, next.AssessmentHash = record.ReportHash, record.AssessmentHash
		now := time.Now().UTC()
		next.CompletedAt = &now
		tx, err := db.beginWrite(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = saveRecoveryTx(ctx, tx, next); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		p := old.Content.RecoveryPurpose.Proposal
		p.RecoveryID, p.RecoveryGeneration = next.ID, next.Spec.Generation
		p.SubmittedHash = p.ComputeSubmittedHash()
		resolve := rs.ResolveInput{ID: next.ID, RequestID: "core-new-generation-resolve", ActorID: spec.ActorID, RuntimeEpoch: next.RuntimeEpoch, AssessmentHash: next.AssessmentHash, ReportHash: next.ReportHash, Purpose: &p, Admission: next.Admission}
		resolve.PayloadHash = resolve.ComputePayloadHash()
		if _, err = store.Resolve(ctx, resolve); err != nil {
			t.Fatal(err)
		}
		projection, err := store.ReadPurposeConfirmation(ctx, next.ID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := NewConfirmationStore(db).Get(ctx, projection.AISessionID, projection.IntentID)
		if err != nil {
			t.Fatal(err)
		}
		current = commitPurposeForTest(t, db, ctx, current, "core-current-generation-confirm")
		if _, err = NewRecoveryPurposeStore(db).ContinueConfirmedPurposes(context.Background(), 0); !errors.Is(err, rs.ErrPurposeReceiptMismatch) {
			t.Fatalf("old-generation refusal not reported: %v", err)
		}
		var oldCount, newCount int
		if err = db.sql.QueryRow(`SELECT (SELECT count(*) FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?),(SELECT count(*) FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?)`, old.IntentID, current.IntentID).Scan(&oldCount, &newCount); err != nil || oldCount != 0 || newCount != 1 {
			t.Fatalf("old receipt blocked current receipt or gained authority: old=%d current=%d err=%v", oldCount, newCount, err)
		}
		return confirmation.Intent{}
	})
}
