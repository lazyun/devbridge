package sqlite

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/featurearea"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
)

func withConsumedReviewPurpose(t *testing.T, check func(*testing.T, *DB, context.Context, confirmation.Intent, string)) {
	t.Helper()
	runPurposeBeforeCommit(t, "REVIEW", func(t *testing.T, db *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, _ rs.Record, intent confirmation.Intent) confirmation.Intent {
		intent = commitPurposeForTest(t, db, ctx, intent, "review-preparation-confirm")
		if _, err := NewRecoveryPurposeStore(db).ContinueConfirmedPurposes(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
		var review string
		var runs int
		if err := db.sql.QueryRow(`SELECT successor_review_id,(SELECT count(*) FROM runs WHERE review_id=successor_review_id) FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, intent.IntentID).Scan(&review, &runs); err != nil || review == "" || runs != 0 {
			t.Fatalf("consume produced runtime effects review=%s runs=%d %v", review, runs, err)
		}
		state, err := NewWorkflowProgressionStore(db).ReadTask(ctx, intent.ProjectID, intent.Content.RecoveryPurpose.Proposal.FeatureAreaID, intent.Content.RecoveryPurpose.Proposal.TaskID)
		if err != nil || state.NextAction.Code != "WAIT_FOR_REVIEW_START" {
			t.Fatalf("preparation guidance: %+v %v", state, err)
		}
		check(t, db, ctx, intent, review)
		return confirmation.Intent{}
	})
}

func TestRecoveryReviewPreparationAtomicQueueAndColdReplay(t *testing.T) {
	withConsumedReviewPurpose(t, func(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, review string) {
		if _, err := db.sql.Exec(`CREATE TRIGGER fail_purpose_start BEFORE INSERT ON feature_area_review_start_requests BEGIN SELECT RAISE(ABORT,'injected start receipt failure');END`); err != nil {
			t.Fatal(err)
		}
		if _, err := NewReviewStore(db).ContinueRecoveryReviewPreparations(ctx, ""); err == nil {
			t.Fatal("injected receipt failure succeeded")
		}
		var count int
		for _, table := range []string{"runs", "runtime_preparation_attempts", "runtime_preparation_receipts"} {
			var q string
			if table == "runs" {
				q = `SELECT count(*) FROM runs WHERE review_id=?`
			} else {
				q = `SELECT count(*) FROM ` + table + ` WHERE run_id='run_'||?`
			}
			if err := db.sql.QueryRow(q, review).Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s leaked after rollback count=%d %v", table, count, err)
			}
		}
		if _, err := db.sql.Exec(`DROP TRIGGER fail_purpose_start`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := NewReviewStore(db).ContinueRecoveryReviewPreparations(context.Background(), "")
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var runID, oldEpoch string
		if err := db.sql.QueryRow(`SELECT r.id,r.runtime_epoch FROM runs r WHERE r.review_id=?`, review).Scan(&runID, &oldEpoch); err != nil {
			t.Fatal(err)
		}
		queue, err := NewReviewStore(db).PendingAuthorizedReviewStarts(ctx, "")
		if err != nil || len(queue) != 1 || queue[0].Admission.Run.ID != runID {
			t.Fatalf("queue=%+v %v", queue, err)
		}
		root := db.stateRoot
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		cold := openTestDB(t, root)
		defer cold.Close()
		if err = NewReviewStore(cold).AdoptUnstartedRecoveryReviewPreparations(ctx); err != nil {
			t.Fatal(err)
		}
		if err = NewReviewStore(cold).AdoptUnstartedRecoveryReviewPreparations(ctx); err != nil {
			t.Fatal(err)
		}
		var epoch string
		if err = cold.sql.QueryRow(`SELECT runtime_epoch FROM runs WHERE id=?`, runID).Scan(&epoch); err != nil || epoch != cold.epoch || epoch == oldEpoch {
			t.Fatalf("cold epoch %s old %s %v", epoch, oldEpoch, err)
		}
		if err = cold.sql.QueryRow(`SELECT count(*) FROM review_purpose_preparation_adoptions WHERE run_id=? AND previous_epoch=? AND current_epoch=?`, runID, oldEpoch, epoch).Scan(&count); err != nil || count != 1 {
			t.Fatalf("cold exact ledger count=%d %v", count, err)
		}
		if _, err = NewReviewStore(cold).ContinueRecoveryReviewPreparations(ctx, ""); err != nil {
			t.Fatal(err)
		}
		if err = cold.sql.QueryRow(`SELECT count(*) FROM runs WHERE review_id=?`, review).Scan(&count); err != nil || count != 1 {
			t.Fatalf("replay allocated replacement %d %v", count, err)
		}
		if _, err = NewRunStore(cold).ReadReviewPreparation(ctx, runID); err != nil {
			t.Fatalf("cold exact preparation unreadable: %v", err)
		}
	})
}

func TestRecoveryReviewPreparationColdAdmissionRejectsExternalClaimAndDrift(t *testing.T) {
	for _, mode := range []string{"workspace-claimed", "topology-drift", "context-drift", "retiring"} {
		t.Run(mode, func(t *testing.T) {
			withConsumedReviewPurpose(t, func(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, review string) {
				if _, err := NewReviewStore(db).ContinueRecoveryReviewPreparations(ctx, ""); err != nil {
					t.Fatal(err)
				}
				var runID, planID, oldEpoch string
				if err := db.sql.QueryRow(`SELECT r.id,p.plan_id,r.runtime_epoch FROM runs r JOIN runtime_preparation_attempts p ON p.run_id=r.id WHERE r.review_id=?`, review).Scan(&runID, &planID, &oldEpoch); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "workspace-claimed":
					if _, err := NewRunStore(db).ClaimWorkspacePreparation(ctx, planID, runID, "external-claim", strings.Repeat("a", 64), time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				case "topology-drift":
					if _, err := db.sql.Exec(`UPDATE project_repository_topologies SET revision=revision+1 WHERE project_id=?`, intent.ProjectID); err != nil {
						t.Fatal(err)
					}
				case "context-drift":
					if _, err := db.sql.Exec(`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('context-added-repo',?,'context-added','/fixture/new',?,?)`, intent.ProjectID, formatTime(testNow), formatTime(testNow)); err != nil {
						t.Fatal(err)
					}
				case "retiring":
					if _, err := NewFeatureAreaStore(db).beginRetirement(ctx, featurearea.LifecycleMutation{ProjectID: intent.ProjectID, Action: featurearea.ActionRetire, PayloadSHA256: strings.Repeat("e", 64), AISessionID: intent.AISessionID, FeatureAreaID: intent.Content.RecoveryPurpose.Proposal.FeatureAreaID, RequestID: "retire-pending-preparation", At: time.Now().UTC()}); err != nil {
						t.Fatal(err)
					}
				}
				root := db.stateRoot
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				cold := openTestDB(t, root)
				defer cold.Close()
				if err := NewReviewStore(cold).AdoptUnstartedRecoveryReviewPreparations(ctx); err != nil {
					t.Fatalf("ordinary stale/blocked preparation prevented startup: %v", err)
				}
				var epoch string
				var count int
				if err := cold.sql.QueryRow(`SELECT runtime_epoch,(SELECT count(*) FROM review_purpose_preparation_adoptions WHERE run_id=r.id) FROM runs r WHERE id=?`, runID).Scan(&epoch, &count); err != nil || epoch != oldEpoch || count != 0 {
					t.Fatalf("blocked preparation adopted %s %d %v", epoch, count, err)
				}
			})
		})
	}
}

func TestTaskRecoveryReaderRejectsMissingExecutionSpecs(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION"} {
		t.Run(owner, func(t *testing.T) {
			runPurposeBeforeCommit(t, owner, func(t *testing.T, db *DB, ctx context.Context, _ *RecoveryStatusStore, _ rs.Spec, _ rs.Record, intent confirmation.Intent) confirmation.Intent {
				taskID := intent.Content.RecoveryPurpose.Proposal.TaskID
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "tasks")
				if _, err := db.sql.Exec(`UPDATE tasks SET work_spec_json='{}',verification_spec_json='{}' WHERE id=?`, taskID); err != nil {
					t.Fatal(err)
				}
				if _, err := NewTaskStore(db).Get(ctx, taskID); err == nil {
					t.Fatal("Review-only recovery exception accepted missing Execution specs")
				}
				return confirmation.Intent{}
			})
		})
	}
}
