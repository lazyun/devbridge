package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunRevisionAndSuccessorHaveExactlyOneWinner(t *testing.T) {
	for _, order := range []string{"revise-first", "successor-first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
			f, owner, predecessor := qualifiedExecutionFixture(t, "-revision-race")
			ctx = retryCallerCtx(t, f, ctx)
			successor := readyNoRunSuccessor(t, f, owner, predecessor, "cancel-revision-race")
			revise := wp.StopAndReviseNoRunTask{ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: predecessor.TaskDefinitionRevisionID, ActorAuthority: f.session}
			before := preparingCancellationHistory(t, f.db, owner.ID, predecessor.ID, predecessor.ID)
			var revised task.Task
			var created wp.SuccessorCommandReceipt
			var reviseErr, successorErr error
			stop := func() { revised, reviseErr = f.store.StopAndReviseNoRunTask(ctx, revise) }
			create := func() { created, successorErr = f.store.PrepareSuccessorExecution(ctx, successor) }
			switch order {
			case "revise-first":
				stop()
				create()
			case "successor-first":
				create()
				stop()
			default:
				start, done := make(chan struct{}), make(chan struct{}, 2)
				go func() { <-start; stop(); done <- struct{}{} }()
				go func() { <-start; create(); done <- struct{}{} }()
				close(start)
				<-done
				<-done
			}
			if (reviseErr == nil) == (successorErr == nil) {
				t.Fatalf("expected one winner: revision=%v successor=%v", reviseErr, successorErr)
			}
			current, err := NewTaskStore(f.db).Get(ctx, owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantEffects := 0
			if reviseErr == nil {
				if revised.Phase != task.PhaseReviewRequired || current.Phase != task.PhaseReviewRequired || current.CurrentExecutionID != predecessor.ID {
					t.Fatalf("revision winner lost authority: %+v", current)
				}
			} else {
				wantEffects = 1
				if created.Outcome != wp.NoRunSuccessorCreated || current.Phase != task.PhaseImplementationPreparing || current.CurrentExecutionID != created.SuccessorExecutionID {
					t.Fatalf("successor winner lost authority: %+v %+v", current, created)
				}
			}
			for _, table := range []string{"no_run_successor_effect_authorities", "no_run_successor_command_receipts"} {
				var count int
				if err := f.db.sql.QueryRow(`SELECT count(*) FROM `+table+` WHERE predecessor_execution_id=?`, predecessor.ID).Scan(&count); err != nil || count != wantEffects {
					t.Fatalf("%s count=%d want=%d err=%v", table, count, wantEffects, err)
				}
			}
			if current.CurrentDefinitionRevisionID != predecessor.TaskDefinitionRevisionID || !reflect.DeepEqual(before, preparingCancellationHistory(t, f.db, owner.ID, predecessor.ID, predecessor.ID)) {
				t.Fatal("competing commands changed predecessor history or definition")
			}
			assertPreparingCancellationNoRuntime(t, f.db, owner.ID)
		})
	}
}
