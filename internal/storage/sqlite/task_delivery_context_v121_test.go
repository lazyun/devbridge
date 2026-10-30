package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/developmentcycle"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestTaskContextV121UpgradePreservesPendingLegacyDelivery(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-task-context")
	execution = terminalExecutionWithReport(t, f, execution)
	_, _, base := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	ctx := context.Background()
	f.store.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "TASK_COMMIT_BEFORE_RESULT" {
			return errors.New("lost Task result")
		}
		return nil
	})
	accepted, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "task-context-accept"})
	if err != nil || accepted.CommitError == "" {
		t.Fatalf("fixture %+v %v", accepted, err)
	}
	f.db = restoreHistoricalMigrationFixture(t, f.db, 120)
	f.store = NewWorkflowProgressionStore(f.db)
	history := deliveryMigrationHistoricalSnapshot(t, f.db)
	before := history()
	for i := 0; i < 2; i++ {
		if err = ApplyMigrations(ctx, f.db.sql, DefaultMigrations()); err != nil {
			t.Fatal(err)
		}
	}
	if after := history(); after != before {
		t.Fatal("upgrade rewrote delivery history")
	}
	store := NewDevelopmentCycleStore(f.db)
	cycle := developmentcycle.Cycle{ID: "new-task-cycle", ProjectID: f.project, RepositoryID: owner.RepositoryID, DevelopmentRef: "refs/heads/develop", SelectedHeadSHA: base, RequestID: "new-task-selection"}
	if _, err = store.Select(ctx, cycle); !errors.Is(err, developmentcycle.ErrFinalizationPending) {
		t.Fatalf("pending legacy Task did not fence selection: %v", err)
	}
	if _, err = f.db.sql.Exec(`UPDATE development_cycles SET superseded_at=created_at,end_reason='BRANCH_RESELECTED' WHERE id='commit-cycle'`); err == nil {
		t.Fatal("database guard bypassed")
	}
	f.store.SetDeliveryFaultHookForTest(nil)
	if _, err = f.store.RetryTaskCommit(ctx, f.project, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Select(ctx, cycle); err != nil {
		t.Fatalf("completed Task still fenced: %v", err)
	}
}
