package sqlite

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/executioncancel"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestLegacyAppliedTaskRecoveryAndCancellation(t *testing.T) {
	for _, mode := range []string{"control", "offline-replay", "offline-cold", "cancel-after-effect", "historical-abandonment"} {
		t.Run(mode, func(t *testing.T) {
			f, owner, execution := acceptedExecutionFixture(t, "-audit-legacy-task")
			execution = terminalExecutionWithReport(t, f, execution)
			_, source, _ := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
			ctx := context.Background()
			if _, err := f.db.sql.Exec(`CREATE TRIGGER audit_lose_task_projection BEFORE UPDATE OF commit_sha ON tasks BEGIN SELECT RAISE(ABORT,'lost task projection'); END`); err != nil {
				t.Fatal(err)
			}
			accepted, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "audit-accept"})
			if err != nil || accepted.CommitError == "" {
				t.Fatalf("fixture: %+v %v", accepted, err)
			}
			if _, err = f.db.sql.Exec(`DROP TRIGGER audit_lose_task_projection`); err != nil {
				t.Fatal(err)
			}
			var sha, outcome string
			if err = f.db.sql.QueryRow(`SELECT r.commit_oid,r.outcome FROM task_delivery_operations o JOIN task_delivery_operation_outcomes r ON r.operation_id=o.operation_id WHERE o.task_id=?`, owner.ID).Scan(&sha, &outcome); err != nil || sha == "" || outcome != "APPLIED" {
				t.Fatalf("missing success proof: %s %s %v", sha, outcome, err)
			}
			if gitHead := commitTestGit(t, source, "rev-parse", "HEAD"); gitHead != sha {
				t.Fatal("Git proof mismatch")
			}
			if mode == "cancel-after-effect" || mode == "historical-abandonment" {
				if mode == "historical-abandonment" {
					// Reproduce the historical shape: abandonment saved the
					// operation proof but did not populate scalar checkpoints.
					if _, e := f.db.sql.Exec(`CREATE TRIGGER keep_old_task_checkpoint BEFORE UPDATE OF commit_sha ON tasks BEGIN SELECT RAISE(IGNORE); END;
 CREATE TRIGGER keep_old_parent_checkpoint BEFORE UPDATE OF resulting_commit ON task_commit_receipts BEGIN SELECT RAISE(IGNORE); END`); e != nil {
						t.Fatal(e)
					}
				}
				receipt, _, e := NewExecutionCancellationStore(f.db).Begin(ctx, executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "TASK", OwnerID: owner.ID, RequestID: "audit-cancel", Reason: "stop pending delivery"}, false)
				if e != nil || receipt.State != "CANCELED" {
					t.Fatalf("cancel: %+v %v", receipt, e)
				}
				task, e := NewTaskStore(f.db).Get(ctx, owner.ID)
				if e != nil {
					t.Fatal(e)
				}
				facts, e := f.store.CommitInventoryFacts(ctx, f.project, f.area)
				if e != nil {
					t.Fatal(e)
				}
				var recorded, operations string
				if e = f.db.sql.QueryRow(`SELECT COALESCE(q.resulting_commit,''),a.operations_json FROM task_commit_receipts q JOIN task_delivery_abandonments a ON a.task_id=q.task_id WHERE q.task_id=?`, owner.ID).Scan(&recorded, &operations); e != nil {
					t.Fatal(e)
				}
				if string(task.Status) != "CANCELED" || (mode == "cancel-after-effect" && (task.CommitSHA != sha || recorded != sha)) {
					t.Fatalf("cancel lost checkpoint or changed terminal status: %+v recorded=%s", task, recorded)
				}
				if len(facts.Tasks) != 1 || facts.Tasks[0].Delivery.State != "ABANDONED" {
					t.Fatalf("missing abandoned projection: %+v", facts)
				}
				visible := false
				for _, fact := range facts.Tasks {
					for _, m := range fact.Delivery.Members {
						if m.Commit == sha {
							visible = true
						}
					}
				}
				if !visible {
					t.Fatalf("known commit absent from canceled Task projection: expected=%s taskCommit=%s receiptCommit=%s facts=%+v preservedOperations=%s", sha, task.CommitSHA, recorded, facts, operations)
				}
				in := executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "TASK", OwnerID: owner.ID, RequestID: "audit-cancel", Reason: "stop pending delivery"}
				again, _, e := NewExecutionCancellationStore(f.db).Begin(ctx, in, false)
				if e != nil || !reflect.DeepEqual(again, receipt) {
					t.Fatalf("cancel replay: %+v %v", again, e)
				}
				if _, e = f.store.RetryTaskCommit(ctx, f.project, owner.ID); !errors.Is(e, delivery.ErrAbandoned) {
					t.Fatalf("canceled Task resumed: %v", e)
				}
				if mode == "historical-abandonment" {
					if task.CommitSHA != "" || recorded != "" {
						t.Fatal("historical fixture unexpectedly has scalar checkpoint")
					}
					if _, e = f.db.sql.Exec(`DROP TRIGGER keep_old_task_checkpoint; DROP TRIGGER keep_old_parent_checkpoint`); e != nil {
						t.Fatal(e)
					}
				}
				tables := selectionMigrationTables(t, f.db)
				snapshot := platformTableSnapshot(t, f.db, tables)
				if _, e = f.store.CommitInventoryFacts(ctx, f.project, f.area); e != nil {
					t.Fatal(e)
				}
				if after := platformTableSnapshot(t, f.db, tables); after != snapshot {
					t.Fatal("reading historical abandonment mutated facts")
				}
				root := f.db.stateRoot
				if e = f.db.Close(); e != nil {
					t.Fatal(e)
				}
				db, e := Open(ctx, root)
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				cold, e := NewWorkflowProgressionStore(db).CommitInventoryFacts(ctx, f.project, f.area)
				if e != nil || !reflect.DeepEqual(cold, facts) {
					t.Fatalf("cold abandonment projection changed: %+v %v", cold, e)
				}
				return
			}
			if mode == "offline-cold" {
				if err = os.Rename(source, source+"-offline"); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(source+"-offline", source)
			}
			root := f.db.stateRoot
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if mode == "offline-replay" {
				if err = os.Rename(source, source+"-offline"); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(source+"-offline", source)
			}
			var before string
			counts := `SELECT (SELECT count(*) FROM task_delivery_operations)||'/'||(SELECT count(*) FROM task_delivery_operation_dispatches)||'/'||(SELECT count(*) FROM task_delivery_operation_outcomes)`
			if err = db.sql.QueryRow(counts).Scan(&before); err != nil {
				t.Fatal(err)
			}
			got, replayErr := NewWorkflowProgressionStore(db).RetryTaskCommit(ctx, f.project, owner.ID)
			var after string
			if err = db.sql.QueryRow(counts).Scan(&after); err != nil || before != after {
				t.Fatalf("replay created operation evidence: %s -> %s %v", before, after, err)
			}
			if replayErr != nil || got.CommitSHA != sha {
				t.Fatalf("exact APPLIED failed to project after cold reopen: proof=%s outcome=%s taskCommit=%s err=%v", sha, outcome, got.CommitSHA, replayErr)
			}
		})
	}
}

func TestLegacyUnknownTaskCannotUseAppliedFastPath(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-legacy-unknown")
	execution = terminalExecutionWithReport(t, f, execution)
	_, source, base := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	ctx := context.Background()
	f.store.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "TASK_COMMIT_BEFORE_GIT" {
			return errors.New("unknown dispatch")
		}
		return nil
	})
	accepted, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "unknown-accept"})
	if err != nil || accepted.CommitError == "" {
		t.Fatalf("fixture: %+v %v", accepted, err)
	}
	if _, _, err = NewExecutionCancellationStore(f.db).Begin(ctx, executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "TASK", OwnerID: owner.ID, RequestID: "cancel-unknown", Reason: "stop"}, false); err == nil {
		t.Fatal("UNKNOWN cancellation fabricated completion")
	}
	root := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = os.Rename(source, source+"-offline"); err != nil {
		t.Fatal(err)
	}
	_, replayErr := NewWorkflowProgressionStore(db).RetryTaskCommit(ctx, f.project, owner.ID)
	if err = os.Rename(source+"-offline", source); err != nil {
		t.Fatal(err)
	}
	if replayErr == nil {
		t.Fatal("missing success used applied fast path")
	}
	if commitTestGit(t, source, "rev-parse", "HEAD") != base {
		t.Fatal("UNKNOWN replay committed")
	}
	var outcomes, completed int
	if err = db.sql.QueryRow(`SELECT (SELECT count(*) FROM task_delivery_operation_outcomes),(SELECT count(*) FROM task_commit_receipts WHERE completed_at IS NOT NULL)`).Scan(&outcomes, &completed); err != nil || outcomes != 0 || completed != 0 {
		t.Fatalf("UNKNOWN produced success: %d %d %v", outcomes, completed, err)
	}
}
