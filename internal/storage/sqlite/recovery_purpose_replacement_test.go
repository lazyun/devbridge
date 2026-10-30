package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

func runPurposeBeforeCommit(t *testing.T, owner string, f purposeBeforeCommit) {
	t.Helper()
	if owner == "REVIEW" {
		runReviewPurposeFlowBeforeCommit(t, f)
	} else {
		runExecutionPurposeFlowBeforeCommit(t, owner, f)
	}
}

func expirePurpose(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent) {
	t.Helper()
	intent.ExpiresAt = time.Now().Add(-time.Minute)
	tx, err := db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = confirmationSave(ctx, tx, intent); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func purposeReplacementRequest(intent confirmation.Intent, id string) confirmation.ReprepareInput {
	return confirmation.ReprepareInput{AISessionID: intent.AISessionID, SourceIntentID: intent.IntentID, SourceVersion: intent.Version, SourceContentHash: intent.ContentHash, RequestID: id}
}

func commitPurposeForTest(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, request string) confirmation.Intent {
	t.Helper()
	in := confirmation.CommitInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, ContentHash: intent.ContentHash, Purpose: intent.Purpose, Action: intent.Action, VariantID: "revised", RequestID: request}
	in.SubmittedHash = confirmation.ValueHash(in)
	s := NewConfirmationStore(db)
	step := confirmation.Step{Kind: "WRITE_PURPOSE_RECEIPT", RequestID: request + "-step", State: "PENDING"}
	_, fresh, err := s.BeginCommit(ctx, in, []confirmation.Step{step})
	if err != nil || !fresh {
		t.Fatalf("begin purpose commit fresh=%v err=%v", fresh, err)
	}
	step.State = "UNKNOWN"
	if _, err = s.RecordStep(ctx, intent.AISessionID, intent.IntentID, intent.Version, step, "CONFIRMING"); err != nil {
		t.Fatal(err)
	}
	if err = s.WriteRecoveryPurposeReceipt(ctx, in); err != nil {
		t.Fatal(err)
	}
	step.State, step.Effect = "COMPLETED", "COMMAND_COMPLETED"
	intent, err = s.RecordStep(ctx, intent.AISessionID, intent.IntentID, intent.Version, step, "COMPLETED")
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func purposeReplacementCounts(t *testing.T, db *DB, ctx context.Context, recoveryID string) (int, int, int) {
	t.Helper()
	var history, receipts, intents int
	if err := db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM recovery_purpose_link_history WHERE recovery_id=?),(SELECT count(*) FROM confirmation_replacement_receipts),(SELECT count(*) FROM confirmation_intents)`, recoveryID).Scan(&history, &receipts, &intents); err != nil {
		t.Fatal(err)
	}
	return history, receipts, intents
}

func TestRecoveryPurposeReplacementCommitAllOwners(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION", "REVIEW"} {
		t.Run(owner, func(t *testing.T) {
			runPurposeBeforeCommit(t, owner, func(t *testing.T, db *DB, ctx context.Context, store *RecoveryStatusStore, spec rs.Spec, record rs.Record, old confirmation.Intent) confirmation.Intent {
				expirePurpose(t, db, ctx, old)
				in := purposeReplacementRequest(old, "replace-purpose")
				next, err := NewConfirmationStore(db).ReprepareRecoveryPurpose(ctx, in)
				if err != nil {
					t.Fatal(err)
				}
				if next.IntentID == old.IntentID || next.Version != 1 || next.State != "PENDING" || next.Content.SourceIntentID != old.IntentID || next.ReplacementReceipt == nil {
					t.Fatalf("replacement identity=%+v", next)
				}
				p, err := store.ReadPurposeConfirmation(ctx, record.ID)
				if err != nil || p.IntentID != next.IntentID || p.LinkOrdinal != 2 || p.RecoveryGeneration != record.Spec.Generation {
					t.Fatalf("current projection=%+v err=%v", p, err)
				}
				var oldHash string
				if err = db.sql.QueryRowContext(ctx, `SELECT intent_content_hash FROM recovery_purpose_link_history WHERE recovery_id=? AND link_ordinal=1`, record.ID).Scan(&oldHash); err != nil || oldHash != old.ContentHash {
					t.Fatalf("old history changed: %s %v", oldHash, err)
				}
				commitPurposeForTest(t, db, ctx, next, "commit-replacement")
				var id string
				var ordinal int
				if err = db.sql.QueryRowContext(ctx, `SELECT purpose_link_id,purpose_link_ordinal FROM recovery_purpose_confirmation_receipts WHERE intent_id=?`, next.IntentID).Scan(&id, &ordinal); err != nil || id != p.LinkID || ordinal != 2 {
					t.Fatalf("receipt binding %s/%d err=%v", id, ordinal, err)
				}
				// Replay must preserve the child's now-completed state, not reconstruct PENDING.
				replayed, err := NewConfirmationStore(db).ReprepareRecoveryPurpose(ctx, in)
				if err != nil || replayed.IntentID != next.IntentID || replayed.State != "COMPLETED" {
					t.Fatalf("replacement replay=%+v err=%v", replayed, err)
				}
				changed := in
				changed.ReviewFeedback = "different payload"
				if _, err = NewConfirmationStore(db).ReprepareRecoveryPurpose(ctx, changed); !errors.Is(err, confirmation.ErrRequestConflict) {
					t.Fatalf("changed request error=%v", err)
				}
				history, receipts, intents := purposeReplacementCounts(t, db, ctx, record.ID)
				if history != 2 || receipts != 1 || intents != 2 {
					t.Fatalf("replacement deltas history=%d receipt=%d intents=%d", history, receipts, intents)
				}
				return confirmation.Intent{}
			})
		})
	}
}

func TestRecoveryPurposeReplacementReviewConsumes(t *testing.T) {
	runReviewPurposeFlowBeforeCommit(t, func(t *testing.T, db *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, _ rs.Record, old confirmation.Intent) confirmation.Intent {
		expirePurpose(t, db, ctx, old)
		next, err := NewConfirmationStore(db).ReprepareRecoveryPurpose(ctx, purposeReplacementRequest(old, "replace-review-consume"))
		if err != nil {
			t.Fatal(err)
		}
		return next
	})
}

func TestRecoveryPurposeReplacementSurvivesRestart(t *testing.T) {
	runPurposeBeforeCommit(t, "IMPLEMENTATION", func(t *testing.T, db *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, record rs.Record, old confirmation.Intent) confirmation.Intent {
		expirePurpose(t, db, ctx, old)
		in := purposeReplacementRequest(old, "replace-before-restart")
		next, err := NewConfirmationStore(db).ReprepareRecoveryPurpose(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(ctx, db.stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		replayed, err := NewConfirmationStore(reopened).ReprepareRecoveryPurpose(ctx, in)
		if err != nil || replayed.IntentID != next.IntentID || replayed.ReplacementReceipt == nil || replayed.ReplacementReceipt.ReplacementIntentID != next.IntentID {
			t.Fatalf("restarted exact replacement=%+v err=%v", replayed, err)
		}
		h, r, i := purposeReplacementCounts(t, reopened, ctx, record.ID)
		if h != 2 || r != 1 || i != 2 {
			t.Fatalf("restart duplicated replacement h=%d r=%d i=%d", h, r, i)
		}
		var linkID string
		var ordinal, version int
		if err = reopened.sql.QueryRow(`SELECT link_id,link_ordinal,selection_version FROM recovery_purpose_current_links WHERE recovery_id=?`, record.ID).Scan(&linkID, &ordinal, &version); err != nil || linkID != "purpose-link:"+next.IntentID || ordinal != 2 || version != 2 {
			t.Fatalf("restart lost current selector %s/%d/%d err=%v", linkID, ordinal, version, err)
		}
		return confirmation.Intent{}
	})
}

func TestRecoveryPurposeReplacementRollsBackAndSerializes(t *testing.T) {
	runPurposeBeforeCommit(t, "IMPLEMENTATION", func(t *testing.T, db *DB, ctx context.Context, store *RecoveryStatusStore, spec rs.Spec, record rs.Record, old confirmation.Intent) confirmation.Intent {
		expirePurpose(t, db, ctx, old)
		if _, err := db.sql.ExecContext(ctx, `CREATE TRIGGER fail_replacement_receipt BEFORE INSERT ON confirmation_replacement_receipts BEGIN SELECT RAISE(ABORT,'injected last write failure'); END`); err != nil {
			t.Fatal(err)
		}
		in := purposeReplacementRequest(old, "replace-rollback")
		if _, err := NewConfirmationStore(db).ReprepareRecoveryPurpose(ctx, in); err == nil {
			t.Fatal("injected failure succeeded")
		}
		h, r, i := purposeReplacementCounts(t, db, ctx, record.ID)
		if h != 1 || r != 0 || i != 1 {
			t.Fatalf("partial replacement survived h=%d r=%d i=%d", h, r, i)
		}
		p, err := store.ReadPurposeConfirmation(ctx, record.ID)
		if err != nil || p.IntentID != old.IntentID || p.LinkOrdinal != 1 {
			t.Fatalf("rolled-back current link=%+v %v", p, err)
		}
		if _, err = db.sql.ExecContext(ctx, `DROP TRIGGER fail_replacement_receipt`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		results := make(chan error, 2)
		for _, id := range []string{"race-a", "race-b"} {
			go func(id string) {
				defer wg.Done()
				_, err := NewConfirmationStore(db).ReprepareRecoveryPurpose(ctx, purposeReplacementRequest(old, id))
				results <- err
			}(id)
		}
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else if !errors.Is(err, confirmation.ErrRequestConflict) {
				t.Fatalf("unexpected race rejection: %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("replacement winners=%d", wins)
		}
		h, r, i = purposeReplacementCounts(t, db, ctx, record.ID)
		if h != 2 || r != 1 || i != 2 {
			t.Fatalf("concurrent lineage fork h=%d r=%d i=%d", h, r, i)
		}
		return confirmation.Intent{}
	})
}

func TestRecoveryPurposeNewGenerationFencesOldAuthority(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh_commit", true: "first_consume"}[committed], func(t *testing.T) {
			runPurposeBeforeCommit(t, "IMPLEMENTATION", func(t *testing.T, db *DB, ctx context.Context, store *RecoveryStatusStore, spec rs.Spec, record rs.Record, old confirmation.Intent) confirmation.Intent {
				if committed {
					old = commitPurposeForTest(t, db, ctx, old, "commit-old-generation")
				}
				spec.RequestID = "new-owner-generation"
				spec.Generation = 999
				spec.PayloadHash = spec.ComputePayloadHash()
				next, err := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
				if err != nil {
					t.Fatal(err)
				}
				if next.ID == record.ID || next.Spec.Generation != record.Spec.Generation+1 {
					t.Fatalf("Core generation not allocated: %+v", next)
				}
				replayed, err := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
				if err != nil || replayed.ID != next.ID {
					t.Fatalf("generation request replay=%+v err=%v", replayed, err)
				}
				p, err := store.ReadPurposeConfirmation(ctx, record.ID)
				if err != nil || !p.Historical {
					t.Fatalf("old generation projection=%+v %v", p, err)
				}
				if !committed {
					in := confirmation.CommitInput{AISessionID: old.AISessionID, IntentID: old.IntentID, Version: old.Version, ContentHash: old.ContentHash, Purpose: old.Purpose, Action: old.Action, VariantID: "revised", RequestID: "old-gen-new-commit"}
					in.SubmittedHash = confirmation.ValueHash(in)
					_, fresh, err := NewConfirmationStore(db).BeginCommit(ctx, in, []confirmation.Step{{Kind: "WRITE_PURPOSE_RECEIPT", RequestID: "old-gen-step", State: "PENDING"}})
					var failure *confirmation.Failure
					if fresh || !errors.As(err, &failure) || failure.Code != "RECOVERY_PURPOSE_LINK_NOT_CURRENT" {
						t.Fatalf("old generation commit fresh=%v err=%v", fresh, err)
					}
				} else {
					in := rs.PurposeConsumeInput{AISessionID: old.AISessionID, IntentID: old.IntentID, Version: old.Version, ContentHash: old.ContentHash, Action: rs.ApplyConfirmedImplementationPurpose, RequestID: "old-gen-new-consume"}
					in.PayloadHash = in.ComputePayloadHash()
					if _, err := NewRecoveryPurposeStore(db).ApplyConfirmedImplementationPurpose(ctx, in); !errors.Is(err, rs.ErrPurposeReceiptMismatch) {
						t.Fatalf("old generation consume did not hit current fence first: %v", err)
					}
					// The already recorded commit remains readable and replayable as history.
					if err := NewConfirmationStore(db).WriteRecoveryPurposeReceipt(ctx, *old.Commit); err != nil {
						t.Fatalf("historical commit replay blocked: %v", err)
					}
				}
				var consumed int
				if err = db.sql.QueryRowContext(ctx, `SELECT count(*) FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, old.IntentID).Scan(&consumed); err != nil || consumed != 0 {
					t.Fatalf("old generation consumed=%d err=%v", consumed, err)
				}
				return confirmation.Intent{}
			})
		})
	}
}
