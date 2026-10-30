package controlapi_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunCancellationSuccessorCanVerifyAcceptAndDeliver(t *testing.T) {
	runNoRunSuccessorDelivery(t, nil)
}

func runNoRunSuccessorDelivery(t *testing.T, beforeRun func(*upperLifecycleFixture, wp.SuccessorCommandReceipt) wp.SuccessorCommandReceipt) {
	t.Helper()
	var original executioncancel.Receipt
	runCodingDeliveryFixtureWithStart(t, func(f *upperLifecycleFixture, executionID string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, executionID)
		_, commit := prepareAcceptanceRecoveryFixture(t, f, current, executionID, "ACCEPT")
		accepted, err := f.facade.CommitConfirmation(f.ctx, commit)
		if err != nil || accepted.State != "COMPLETED" {
			t.Fatalf("accept successor: %+v %v", accepted, err)
		}
		for _, cold := range []bool{false, true} {
			if cold {
				reopenDirectDeliveryFixture(t, f)
			}
			f.plane.ExecutionProposals.ApprovedContinuation()(f.ctx)
			var runs int
			if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, executionID).Scan(&runs); err != nil || runs != 2 {
				t.Fatalf("terminal continuation changed implementation/verification Runs: %d %v", runs, err)
			}
			current, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task)
			if err != nil || current.Status != task.StatusSucceeded {
				t.Fatalf("no-Run history blocks successor delivery (cold=%v): status=%s phase=%s %v", cold, current.Status, current.Phase, err)
			}
			receipts, err := f.facade.ListExecutionCancellationsForSession(f.ctx, f.session, original.OwnerID)
			if err != nil || len(receipts) != 1 || !reflect.DeepEqual(receipts[0], original) {
				t.Fatalf("delivery rewrote original no-backend cancellation: %+v %v", receipts, err)
			}
		}
		return false
	}, func(f *upperLifecycleFixture, intent c.Intent, launch controlapi.ApprovedTaskStartInput) (executionproposal.StartResult, error) {
		f.plane.Retirement.Close()
		admitted, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "admit-before-no-run-cancel", ContentHash: intent.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &launch.RuntimeConfig, Backend: &launch.Backend}})
		if err != nil || admitted.State != "COMPLETED" || len(admitted.Steps) != 1 || admitted.Steps[0].RunID != "" {
			t.Fatalf("pre-Run admission: %+v %v", admitted, err)
		}
		owner := admitted.Steps[0].ExecutionID
		// Resolve the actual runtime binding, then inject a failure at the first
		// Run insert. This leaves a qualified, admitted owner with no runtime work.
		launch.ExpectedTarget, err = f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER delivery_no_run_fault BEFORE INSERT ON runs WHEN NEW.purpose='IMPLEMENTATION' BEGIN SELECT RAISE(ABORT,'injected failure before Run'); END`); err != nil {
			t.Fatal(err)
		}
		failed, err := f.facade.StartApprovedTaskExecution(f.ctx, launch)
		if err == nil || failed.Run.ID != "" {
			t.Fatalf("pre-Run failure did not hold: %+v %v", failed, err)
		}
		if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER delivery_no_run_fault`); err != nil {
			t.Fatal(err)
		}
		original, err = f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: owner, RequestID: "cancel-before-delivery-run"})
		if err != nil || original.State != "CANCELED" || original.BackendDisposition != "NO_BACKEND" || original.RunID != "" {
			t.Fatalf("pre-Run cancellation: %+v %v", original, err)
		}
		in := wp.PrepareSuccessorExecution{RequestID: "successor-for-delivery", ProjectID: f.project, TaskID: f.task, PredecessorExecutionID: owner, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: owner, ExpectedDefinitionRevisionID: launch.ExpectedTarget.TaskDefinitionRevisionID, ActorAuthority: f.session}
		in.PayloadSHA256 = c.ValueHash(in)
		successor, err := f.facade.PrepareSuccessorExecutionForSession(f.ctx, f.session, in)
		if err != nil || successor.SuccessorExecutionID == "" || successor.SuccessorExecutionID == owner {
			t.Fatalf("prepare successor: %+v %v", successor, err)
		}
		if beforeRun != nil {
			successor = beforeRun(f, successor)
		}
		pending, err := sqlite.NewExecutionProposalStore(f.plane.DB).PendingApprovedExecutions(f.ctx, "")
		if err != nil || len(pending) != 1 || pending[0].ExecutionID != successor.SuccessorExecutionID {
			t.Fatalf("successor missing from authorized continuation: %+v %v", pending, err)
		}
		stale := runtimeepoch.WithFence(f.ctx, "obsolete-worker-epoch", "")
		if _, _, err = sqlite.NewExecutionProposalStore(f.plane.DB).PrepareAdmittedExecution(stale, successor.SuccessorExecutionID, time.Now().UTC()); !errors.Is(err, runtimeepoch.ErrFenced) {
			t.Fatalf("stale worker prepared successor: %v", err)
		}
		// Lose preparation-to-dispatch progress, then let the normal queue
		// resume the same registered Run without allocating a second one.
		if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER successor_handoff_fault BEFORE UPDATE OF state ON runtime_handoffs WHEN NEW.state='PREPARING' BEGIN SELECT RAISE(ABORT,'injected handoff failure'); END`); err != nil {
			t.Fatal(err)
		}
		f.plane.ExecutionProposals.ApprovedContinuation()(f.ctx)
		prepared, err := f.facade.GetExecutionProposal(f.ctx, f.session, successor.SuccessorExecutionID)
		if err != nil || prepared.RunID == "" {
			t.Fatalf("successor Run was not prepared: %v", err)
		}
		if _, dispatched, err := sqlite.NewExecutionProposalStore(f.plane.DB).ClaimHandoff(stale, f.project, prepared.RunID, time.Now().UTC()); !errors.Is(err, runtimeepoch.ErrFenced) || dispatched {
			t.Fatalf("stale worker claimed handoff: %v %v", dispatched, err)
		}
		if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER successor_handoff_fault`); err != nil {
			t.Fatal(err)
		}
		pending, err = sqlite.NewExecutionProposalStore(f.plane.DB).PendingApprovedExecutions(f.ctx, "")
		if err != nil || len(pending) != 1 || pending[0].ExecutionID != successor.SuccessorExecutionID {
			t.Fatalf("prepared successor not resumed: %+v %v", pending, err)
		}
		f.plane.ExecutionProposals.ApprovedContinuation()(f.ctx)
		proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, successor.SuccessorExecutionID)
		if err != nil || proposal.RunID == "" {
			_, _, preparationErr := sqlite.NewExecutionProposalStore(f.plane.DB).PrepareAdmittedExecution(f.ctx, successor.SuccessorExecutionID, time.Now().UTC())
			t.Fatalf("successor continuation failed: read=%v preparation=%v", err, preparationErr)
		}
		if proposal.RunID != prepared.RunID {
			t.Fatal("continuation allocated a second Run")
		}
		reserved, err := f.facade.GetRunForSession(f.ctx, f.session, proposal.RunID)
		return executionproposal.StartResult{Proposal: proposal, Run: reserved}, err
	})
}
