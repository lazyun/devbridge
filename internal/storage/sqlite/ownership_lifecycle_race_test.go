package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
)

func ownershipLifecycleService(t *testing.T, f agentStorageFixture) *featurearea.Service {
	t.Helper()
	service, err := featurearea.NewService(NewFeatureAreaStore(f.db), NewAISessionStore(f.db), NewTaskStore(f.db).CountByFeatureArea, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func raceReservationAndLifecycle(reserve func() (run.Run, error), mutate func() error) (run.Run, error, error) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var reserved run.Run
	var reserveErr, lifecycleErr error
	go func() { defer wg.Done(); <-start; reserved, reserveErr = reserve() }()
	go func() { defer wg.Done(); <-start; lifecycleErr = mutate() }()
	close(start)
	wg.Wait()
	return reserved, reserveErr, lifecycleErr
}

func TestOwnershipActivationOrdersAgainstDirectReviewReservation(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f, service := lifecycleFixture(t, fmt.Sprintf("activate-reserve-%d", i))
			ctx := context.Background()
			f.task.FeatureAreaID = f.area.ID
			f.task = f.addTask(t, "activation-intent-"+fmt.Sprint(i))
			op := prepareAgentOperation(t, f, agentconversation.Review, fmt.Sprintf("activate-reserve-%d", i))
			runID := fmt.Sprintf("run-activate-reserve-%d", i)
			reserved, reserveErr, activateErr := raceReservationAndLifecycle(func() (run.Run, error) { return op.make(ctx, runID) }, func() error { _, err := service.Activate(ctx, f.session.ID, f.area.ID, "activate-race"); return err })
			// REVIEW is permitted on either DISCOVERING or ACTIVE. Activation may
			// commit before the reservation, but may never pass an existing claim.
			if reserveErr != nil || reserved.TaskID != f.task.ID || reserved.OperationID != op.id {
				t.Fatalf("Review reservation=%+v err=%v", reserved, reserveErr)
			}
			area, err := NewFeatureAreaStore(f.db).Get(ctx, f.area.ID)
			if err != nil {
				t.Fatal(err)
			}
			var claimRun string
			if err := f.db.sql.QueryRow(`SELECT run_id FROM feature_area_agent_claims WHERE feature_area_id=?`, f.area.ID).Scan(&claimRun); err != nil || claimRun != runID {
				t.Fatalf("claim=%s err=%v", claimRun, err)
			}
			var receipts int
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_lifecycle_requests WHERE feature_area_id=?`, f.area.ID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if activateErr == nil {
				if area.Status != featurearea.StatusActive || receipts != 1 {
					t.Fatalf("activation result area=%+v receipts=%d", area, receipts)
				}
				var activationSequence, runSequence int
				if err := f.db.sql.QueryRow(`SELECT sequence FROM events WHERE entity_id=? AND type='feature_area.status_changed'`, f.area.ID).Scan(&activationSequence); err != nil {
					t.Fatal(err)
				}
				if err := f.db.sql.QueryRow(`SELECT sequence FROM events WHERE run_id=? AND type='run.created'`, runID).Scan(&runSequence); err != nil {
					t.Fatal(err)
				}
				if activationSequence >= runSequence {
					t.Fatalf("activation crossed existing Review reservation: activate=%d run=%d", activationSequence, runSequence)
				}
			} else if !errors.Is(activateErr, agentconversation.ErrBusy) || area.Status != featurearea.StatusDiscovering || receipts != 0 {
				t.Fatalf("nonserial outcome area=%+v receipts=%d err=%v", area, receipts, activateErr)
			}
			if count, err := NewTaskStore(f.db).CountByFeatureArea(ctx, f.area.ID); err != nil || count != 1 {
				t.Fatalf("Review introduced Task count=%d err=%v", count, err)
			}
		})
	}
}

func TestOwnershipRetirementOrdersAgainstPlanAndExecutionReservation(t *testing.T) {
	for _, kind := range []agentconversation.OperationKind{agentconversation.Execution} {
		t.Run(string(kind), func(t *testing.T) {
			for i := 0; i < 8; i++ {
				t.Run(fmt.Sprint(i), func(t *testing.T) {
					label := fmt.Sprintf("retire-%s-%d", kind, i)
					f := newAgentStorageFixture(t, label)
					ctx := context.Background()
					service := ownershipLifecycleService(t, f)
					runID := "run-" + label
					var ownerID string
					var reserve func() (run.Run, error)

					ownerID = seedExecutionOwner(t, f.db, f.task, f.session, "execution-"+label)
					reserve = func() (run.Run, error) {
						_, value, err := NewExecutionProposalStore(f.db).AuthorizeAndCreateRun(ctx, ownerID, runID, testNow)
						return value, err
					}

					reserved, reserveErr, retireErr := raceReservationAndLifecycle(reserve, func() error { _, err := service.Retire(ctx, f.session.ID, f.area.ID, "retire-race"); return err })
					if retireErr != nil {
						t.Fatalf("stop intent must be accepted in either ordering: %v", retireErr)
					}
					area, err := NewFeatureAreaStore(f.db).Get(ctx, f.area.ID)
					if err != nil {
						t.Fatal(err)
					}
					var claims, runs, receipts, nextAttempt int
					for _, check := range []struct {
						query  string
						args   []any
						target *int
					}{
						{`SELECT count(*) FROM feature_area_agent_claims WHERE feature_area_id=?`, []any{f.area.ID}, &claims},
						{`SELECT count(*) FROM runs WHERE id=?`, []any{runID}, &runs},
						{`SELECT count(*) FROM feature_area_lifecycle_requests WHERE feature_area_id=?`, []any{f.area.ID}, &receipts},
						{`SELECT next_attempt FROM tasks WHERE id=?`, []any{f.task.ID}, &nextAttempt},
					} {
						if err := f.db.sql.QueryRow(check.query, check.args...).Scan(check.target); err != nil {
							t.Fatal(err)
						}
					}
					if reserveErr == nil {
						if area.Status != featurearea.StatusRetiring || claims != 1 || runs != 1 || receipts != 1 || nextAttempt != 2 || reserved.OperationID != ownerID || string(reserved.OperationKind) != string(kind) {
							t.Fatalf("reservation winner violated ownership: area=%s claims=%d runs=%d receipts=%d attempt=%d run=%+v retire=%v", area.Status, claims, runs, receipts, nextAttempt, reserved, retireErr)
						}
					} else {
						if area.Status != featurearea.StatusRetiring || claims != 0 || runs != 0 || receipts != 1 || nextAttempt != 1 {
							t.Fatalf("retirement left partial reservation: area=%s claims=%d runs=%d receipts=%d attempt=%d reserve=%v", area.Status, claims, runs, receipts, nextAttempt, reserveErr)
						}
						if kind == agentconversation.Execution {
							proposal, err := NewExecutionProposalStore(f.db).Get(ctx, ownerID)
							if err != nil || proposal.Status != executionproposal.StatusConfirmed || proposal.RunID != "" || proposal.ConfirmedAt == nil {
								t.Fatalf("retirement leaked authorization: %+v err=%v", proposal, err)
							}
						}
					}
				})
			}
		})
	}
}

func TestOwnershipDiscoveringTaskCreationCannotRacePastDecomposition(t *testing.T) {
	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f, service := lifecycleFixture(t, fmt.Sprintf("decompose-task-%d", i))
			ctx := context.Background()
			value := f.task
			value.ID = fmt.Sprintf("forbidden-discovering-task-%d", i)
			value.FeatureAreaID = f.area.ID
			var createdErr, decomposeErr error
			var result featurearea.LifecycleResult
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); <-start; createdErr = NewTaskStore(f.db).Create(ctx, value) }()
			go func() { defer wg.Done(); <-start; result, decomposeErr = service.Decompose(ctx, decompositionInput(f)) }()
			close(start)
			wg.Wait()
			if (createdErr == nil) == (decomposeErr == nil) {
				t.Fatalf("both or neither transitions won: task=%v decomposition=%v", createdErr, decomposeErr)
			}
			if decomposeErr == nil && (result.Parent.Status != featurearea.StatusDecomposed || len(result.Children) != 2) {
				t.Fatal("incomplete decomposition")
			}

			var tasks, revisions, relations int
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM tasks WHERE feature_area_id=?`, f.area.ID).Scan(&tasks); err != nil {
				t.Fatal(err)
			}
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM task_definition_revisions WHERE task_id=?`, value.ID).Scan(&revisions); err != nil {
				t.Fatal(err)
			}
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_relations WHERE source_feature_area_id=?`, f.area.ID).Scan(&relations); err != nil {
				t.Fatal(err)
			}
			wantTasks, wantRelations := 0, 2
			if createdErr == nil {
				wantTasks, wantRelations = 1, 0
			}
			if tasks != wantTasks || revisions != 0 || relations != wantRelations {
				t.Fatalf("invalid partial graph tasks=%d revisions=%d relations=%d", tasks, revisions, relations)
			}
		})
	}
}

func TestOwnershipLifecycleAndRunsRemainIndependentAcrossFeatureAreas(t *testing.T) {
	f := newAgentStorageFixture(t, "independent-lifecycle")
	ctx := context.Background()
	service := ownershipLifecycleService(t, f)
	right := f.addAreaTask(t, "independent-right", "independent-right-task")
	leftOp := prepareAgentOperation(t, f, agentconversation.Execution, "independent-left")
	rightOp := prepareAgentOperation(t, right, agentconversation.Execution, "independent-right")
	left, err := leftOp.make(ctx, "independent-left-run")
	if err != nil {
		t.Fatal(err)
	}
	rightRun, err := rightOp.make(ctx, "independent-right-run")
	if err != nil {
		t.Fatalf("unrelated area was blocked: %v", err)
	}
	var claims int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE feature_area_id IN (?,?)`, f.area.ID, right.area.ID).Scan(&claims); err != nil || claims != 2 {
		t.Fatalf("independent claims=%d err=%v", claims, err)
	}
	idle := f.area
	idle.ID = "independent-idle"
	idle.Status = featurearea.StatusDiscovering
	if err := NewFeatureAreaStore(f.db).Create(ctx, idle); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Activate(ctx, f.session.ID, idle.ID, "activate-independent"); err != nil {
		t.Fatalf("unrelated activation blocked: %v", err)
	}
	if _, err := service.Retire(ctx, f.session.ID, idle.ID, "retire-independent"); err != nil {
		t.Fatalf("unrelated retirement blocked: %v", err)
	}
	for _, value := range []run.Run{left, rightRun} {
		var operationID, runID, epoch string
		if err := f.db.sql.QueryRow(`SELECT operation_id,run_id,runtime_epoch FROM feature_area_agent_claims WHERE feature_area_id=?`, value.FeatureAreaID).Scan(&operationID, &runID, &epoch); err != nil || operationID != value.OperationID || runID != value.ID || epoch != value.RuntimeEpoch {
			t.Fatalf("other lifecycle disrupted reservation: operation=%s run=%s epoch=%s err=%v", operationID, runID, epoch, err)
		}
	}
}
