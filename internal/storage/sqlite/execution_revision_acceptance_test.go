package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The caller has completed and reopened a real SQLite-backed implementation /
// verification workflow. Exercise a newer Task definition and plan on
// that same Task, then read the full old history through its frozen identities.
func assertExecutionHistoryAfterRevision(t *testing.T, db *DB, session aisession.Session, oldExecution executionproposal.Proposal, oldImplementation, oldVerification run.Run) {
	t.Helper()
	ctx := context.Background()
	oldDefinition, err := NewTaskStore(db).GetDefinitionRevision(ctx, oldExecution.TaskDefinitionRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	tasks := NewTaskStore(db)
	work := oldExecution.WorkSpec
	work.CodexInstruction = "Version two implementation instruction."
	verification := oldExecution.VerificationSpec
	verification.CodexInstruction = "Version two verification instruction."
	resolution, err := NewWorkflowProgressionStore(db).ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: session.ID, DecisionID: wp.TaskAcceptanceDecisionID(oldExecution.ID), ExecutionID: oldExecution.ID, Option: wp.AcceptanceRequestChanges, RequestID: "history-revision", NextWorkSpec: &work, NextVerificationSpec: &verification})
	if err != nil || resolution.Task.CurrentDefinitionRevisionID == oldDefinition.ID {
		t.Fatalf("acceptance did not select v2: %+v %v", resolution, err)
	}
	readExecution, err := NewExecutionProposalStore(db).Get(ctx, oldExecution.ID)
	if err != nil || !reflect.DeepEqual(readExecution, oldExecution) {
		t.Fatalf("v2 changed old Execution: %+v %v", readExecution, err)
	}
	readDefinition, err := tasks.GetDefinitionRevision(ctx, readExecution.TaskDefinitionRevisionID)
	if err != nil || !reflect.DeepEqual(readDefinition, oldDefinition) {
		t.Fatalf("v2 changed immutable v1 payload/hash: %+v %v", readDefinition, err)
	}
	for _, oldRun := range []run.Run{oldImplementation, oldVerification} {
		readRun, err := NewRunStore(db).Get(ctx, oldRun.ID)
		if err != nil || !reflect.DeepEqual(readRun, oldRun) || readRun.ExecutionProposalID != readExecution.ID {
			t.Fatalf("v2 changed old %s history: %+v %v", oldRun.Purpose, readRun, err)
		}
	}
	if oldDefinition.Definition.WorkSpec.CodexInstruction != oldExecution.WorkSpec.CodexInstruction || oldDefinition.Definition.VerificationSpec.CodexInstruction != oldExecution.VerificationSpec.CodexInstruction {
		t.Fatal("old implementation/verification no longer interpret their authored v1 definition")
	}
}
