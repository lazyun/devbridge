package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestPrepareSuccessorExecutionCreatesOneSuccessor(t *testing.T) {
	ctx := context.Background()
	f, taskValue, predecessor := qualifiedExecutionFixture(t, "-no-run-successor")
	if _, _, err := reserveNoRunFixture(ctx, f, taskValue.ID); err != nil {
		t.Fatal(err)
	}
	cancel := executioncancel.Input{AISessionID: f.session, ProjectID: taskValue.ProjectID, ExecutionID: predecessor.ID, OwnerType: "EXECUTION", OwnerID: predecessor.ID, RequestID: "cancel-successor-test", Reason: "cancel pre-Run"}
	receipt, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, cancel, false)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "CANCELED" || receipt.RunID != "" {
		t.Fatal(receipt)
	}
	if _, err = NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, taskValue.ID, predecessor.ID); err != nil {
		t.Fatal(err)
	}
	in := wp.PrepareSuccessorExecution{RequestID: "successor-request-1", PayloadSHA256: strings.Repeat("a", 64), ProjectID: taskValue.ProjectID, TaskID: taskValue.ID, PredecessorExecutionID: predecessor.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: taskValue.CurrentDefinitionRevisionID, ActorAuthority: f.session}
	store := NewWorkflowProgressionStore(f.db)
	created, err := store.PrepareSuccessorExecution(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.Outcome != wp.NoRunSuccessorCreated {
		t.Fatal(created)
	}
	var predecessorPlanID, predecessorContentHash, predecessorHash, verifiedHash, successorHash, successorOwner string
	if err = f.db.sql.QueryRow(`SELECT predecessor_runtime_scope_plan_id,predecessor_plan_content_hash,predecessor_authority_snapshot_hash,verified_current_authority_snapshot_hash FROM no_run_successor_effect_authorities WHERE predecessor_execution_id=?`, predecessor.ID).Scan(&predecessorPlanID, &predecessorContentHash, &predecessorHash, &verifiedHash); err != nil {
		t.Fatal(err)
	}
	var persistedContentHash string
	if err = f.db.sql.QueryRow(`SELECT content_hash,authority_snapshot_hash FROM runtime_scope_plans WHERE plan_id=? AND owner_kind IN ('IMPLEMENTATION','EXECUTION') AND owner_id=?`, predecessorPlanID, predecessor.ID).Scan(&persistedContentHash, &successorHash); err != nil || predecessorHash != successorHash || predecessorContentHash != persistedContentHash || len(predecessorHash) != 64 {
		t.Fatalf("predecessor snapshot binding: %s %s %v", predecessorHash, successorHash, err)
	}
	if err = f.db.sql.QueryRow(`SELECT authority_snapshot_hash,owner_id FROM runtime_scope_plans WHERE plan_id=? AND owner_kind='EXECUTION'`, created.RuntimeScopePlanID).Scan(&successorHash, &successorOwner); err != nil || verifiedHash != successorHash || len(verifiedHash) != 64 || successorOwner != created.SuccessorExecutionID {
		t.Fatalf("successor snapshot binding: %s %s %s %v", verifiedHash, successorHash, successorOwner, err)
	}
	var phase task.Phase
	var current string
	if err = f.db.sql.QueryRow(`SELECT phase,current_execution_id FROM tasks WHERE id=?`, taskValue.ID).Scan(&phase, &current); err != nil {
		t.Fatal(err)
	}
	if phase != task.PhaseImplementationPreparing || current != created.SuccessorExecutionID {
		t.Fatalf("Task phase=%s current=%s receipt=%+v", phase, current, created)
	}
	for _, check := range []struct{ query, id string }{
		{`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, created.SuccessorExecutionID},
		{`SELECT count(*) FROM runtime_preparation_attempts WHERE plan_id=?`, created.RuntimeScopePlanID},
		{`SELECT count(*) FROM runtime_workset_bindings WHERE plan_id=?`, created.RuntimeScopePlanID},
		{`SELECT count(*) FROM runtime_start_authorities WHERE plan_id=?`, created.RuntimeScopePlanID},
	} {
		var n int
		if err = f.db.sql.QueryRow(check.query, check.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("successor created runtime fact: %d", n)
		}
	}
	replay, err := store.PrepareSuccessorExecution(ctx, in)
	if err != nil || replay != created {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var preparationRequest string
	if err = f.db.sql.QueryRow(`SELECT preparation_request_id FROM runtime_scope_plans WHERE plan_id=?`, created.RuntimeScopePlanID).Scan(&preparationRequest); err != nil {
		t.Fatal(err)
	}
	child, err := NewRunStore(f.db).CreatePreparationRun(ctx, created.RuntimeScopePlanID, "run-successor-test", preparationRequest, in.PayloadSHA256, f.now)
	if err != nil || child.ID == "" {
		t.Fatalf("successor canonical preparation: %+v %v", child, err)
	}
}
