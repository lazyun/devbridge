package sqlite

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"reflect"
	"testing"
)

func TestAgentExecutionProjectionReadsExactOrphanFacts(t *testing.T) {
	for _, kind := range []agentconversation.OperationKind{agentconversation.Review, agentconversation.Execution} {
		t.Run(string(kind), func(t *testing.T) {
			f, r, in := unifiedOwnerFixture(t, kind)
			ctx := context.Background()
			dispatchCancellation(t, f, in)
			if err := NewExecutionCancellationStore(f.db).FinishInterrupted(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			store := NewAgentExecutionStore(f.db)
			e, err := store.Get(ctx, r.AgentExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			cancellation, err := store.CancellationFacts(ctx, e.ID, r.ID)
			if err != nil || cancellation.CancellationState != "CANCELED" || cancellation.BackendDisposition != "UNKNOWN_ORPHANED" {
				t.Fatalf("exact Run receipt: %+v %v", cancellation, err)
			}
			noRun, err := store.CancellationFacts(ctx, e.ID, "")
			if err != nil || noRun != (agentexecution.RuntimeFacts{}) {
				t.Fatalf("NULL lookup borrowed Run: %+v %v", noRun, err)
			}
			facts, err := store.RuntimeFacts(ctx, e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if facts[r.ID].BackendDisposition != "UNKNOWN_ORPHANED" {
				t.Fatalf("missing durable orphan: %+v", facts)
			}
			terminal, err := NewRunStore(f.db).Get(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			p := agentexecution.Reduce(e, agentexecution.Facts{Runs: []run.Run{terminal}, CurrentRunID: r.ID, Runtime: facts})
			if p.Status != "UNKNOWN_ORPHANED" || p.BackendDisposition != "UNKNOWN_ORPHANED" || p.RecoveryRequired {
				t.Fatalf("orphan incorrectly presented: %+v", p)
			}
			unrelated, err := store.RuntimeFacts(ctx, "unrelated")
			if err != nil || len(unrelated) != 0 {
				t.Fatalf("borrowed orphan facts: %+v %v", unrelated, err)
			}
		})
	}
}

func TestAgentExecutionCancellationFactsExactNullableRun(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "facts")
	older := prepareAgentOperation(t, f, agentconversation.Execution, "older")
	store := NewAgentExecutionStore(f.db)
	cancel := NewExecutionCancellationStore(f.db)
	in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, ExecutionID: older.id, OwnerType: "EXECUTION", OwnerID: older.id, RequestID: "older"}
	receipt, _, err := cancel.Begin(ctx, in, false)
	if err != nil || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" {
		t.Fatalf("pre-run cancel %+v %v", receipt, err)
	}
	f.task = f.addTask(t, "another-intent")
	nextOwner := prepareAgentOperation(t, f, agentconversation.Execution, "next")
	next, err := NewExecutionProposalStore(f.db).Get(ctx, nextOwner.id)
	if err != nil {
		t.Fatal(err)
	}
	own, err := store.CancellationFacts(ctx, next.ID, "")
	if err != nil || own != (agentexecution.RuntimeFacts{}) {
		t.Fatalf("borrowed older owner: %+v %v", own, err)
	}
	for _, target := range []struct{ id, run string }{{older.id, "wrong-run"}, {"missing", ""}} {
		facts, err := store.CancellationFacts(ctx, target.id, target.run)
		if err != nil || facts != (agentexecution.RuntimeFacts{}) {
			t.Fatalf("borrowed target: %+v %v", facts, err)
		}
	}
	in.ExecutionID = next.ID
	in.OwnerID = next.ID
	in.RequestID = "own"
	receipt, _, err = cancel.Begin(ctx, in, false)
	if err != nil {
		t.Fatal(err)
	}
	own, err = store.CancellationFacts(ctx, next.ID, "")
	if err != nil || own.CancellationState != "CANCELED" || own.BackendDisposition != "NO_BACKEND" {
		t.Fatalf("missing own receipt: %+v %v", own, err)
	}
	replay, _, err := cancel.Begin(ctx, in, false)
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("receipt replay: %+v %v", replay, err)
	}
	again, err := store.CancellationFacts(ctx, next.ID, "")
	if err != nil || again != own {
		t.Fatalf("fact replay: %+v %v", again, err)
	}
}

func TestHistoricalExecutionDoesNotHideCurrentSuccessorRun(t *testing.T) {
	ctx := context.Background()
	f, current, older := acceptedExecutionFixture(t, "history-projection")
	older = terminalExecutionWithReport(t, f, older)
	work := older.WorkSpec
	work.CodexInstruction += "; address findings"
	verification := older.VerificationSpec
	next, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(older.ID), ExecutionID: older.ID, Option: wp.AcceptanceRequestChanges, RequestID: "next", NextWorkSpec: &work, NextVerificationSpec: &verification})
	if err != nil {
		t.Fatal(err)
	}
	value, r, err := f.store.ReserveTaskExecution(ctx, f.session, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := NewTaskStore(f.db).Get(ctx, current.ID)
	if err != nil || owner.Work.ExecutionID != next.SuccessorExecutionID || owner.Work.ExecutionID != value.ID || owner.Work.RunID != r.ID {
		t.Fatalf("history hid exact current Run: %+v %v", owner.Work, err)
	}
}
