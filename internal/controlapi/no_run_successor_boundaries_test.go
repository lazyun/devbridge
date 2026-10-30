package controlapi_test

import (
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunSuccessorContinuesAfterColdRestart(t *testing.T) {
	runNoRunSuccessorDelivery(t, func(f *upperLifecycleFixture, successor wp.SuccessorCommandReceipt) wp.SuccessorCommandReceipt {
		reopenNoRunSuccessorProtocolFixture(t, f)
		return successor
	})
}

func TestCanceledNoRunSuccessorCannotPrepareOrDispatch(t *testing.T) {
	runNoRunSuccessorDelivery(t, func(f *upperLifecycleFixture, successor wp.SuccessorCommandReceipt) wp.SuccessorCommandReceipt {
		canceled, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: successor.SuccessorExecutionID, RequestID: "cancel-first-successor"})
		if err != nil || canceled.State != "CANCELED" || canceled.RunID != "" {
			t.Fatalf("cancel successor: %+v %v", canceled, err)
		}
		for _, cold := range []bool{false, true} {
			if cold {
				reopenNoRunSuccessorProtocolFixture(t, f)
			}
			store := sqlite.NewExecutionProposalStore(f.plane.DB)
			pending, err := store.PendingApprovedExecutions(f.ctx, "")
			if err != nil || len(pending) != 0 {
				t.Fatalf("canceled successor queued cold=%v: %+v %v", cold, pending, err)
			}
			if _, reserved, err := store.PrepareAdmittedExecution(f.ctx, successor.SuccessorExecutionID, time.Now().UTC()); err == nil || reserved.ID != "" {
				t.Fatalf("canceled successor prepared cold=%v: %+v %v", cold, reserved, err)
			}
			f.plane.ExecutionProposals.ApprovedContinuation()(f.ctx)
			current, err := f.facade.GetExecutionProposal(f.ctx, f.session, successor.SuccessorExecutionID)
			if err != nil || current.RunID != "" || current.Outcome != "CANCELED" {
				t.Fatalf("canceled successor changed: %+v %v", current, err)
			}
		}
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		in := wp.PrepareSuccessorExecution{RequestID: "second-successor", ProjectID: f.project, TaskID: f.task, PredecessorExecutionID: successor.SuccessorExecutionID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: successor.SuccessorExecutionID, ExpectedDefinitionRevisionID: current.CurrentDefinitionRevisionID, ActorAuthority: f.session}
		in.PayloadSHA256 = c.ValueHash(in)
		next, err := f.facade.PrepareSuccessorExecutionForSession(f.ctx, f.session, in)
		if err != nil || next.SuccessorExecutionID == "" || next.SuccessorExecutionID == successor.SuccessorExecutionID {
			t.Fatalf("second successor: %+v %v", next, err)
		}
		return next
	})
}

func reopenNoRunSuccessorProtocolFixture(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	factory, ok := f.plane.Coordinator.Codex.(execution.DefaultCodexFactory)
	if !ok {
		t.Fatal("protocol factory missing")
	}
	oldEpoch := f.plane.DB.RuntimeEpoch()
	// Bootstrap starts reconciliation immediately. Hold only Run insertion
	// until the reopened test dependencies are installed, so the fixture never
	// races its own protocol backend replacement.
	if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER successor_restart_window BEFORE INSERT ON runs WHEN NEW.purpose='IMPLEMENTATION' BEGIN SELECT RAISE(ABORT,'test dependencies reopening'); END`); err != nil {
		t.Fatal(err)
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	if f.plane.DB.RuntimeEpoch() == oldEpoch {
		t.Fatal("cold restart did not change runtime epoch")
	}
	installProtocolFixturePlatform(t, f)
	factory.Config.AgentRuntime = f.plane.Runs.AgentRuntime
	f.plane.Coordinator.Codex = factory
	if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER successor_restart_window`); err != nil {
		t.Fatal(err)
	}
}
