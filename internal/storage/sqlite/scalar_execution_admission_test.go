package sqlite

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/executionproposal"
)

func TestScalarReserveExecutionCreatesAdmissionWithoutRun(t *testing.T) {
	ctx := context.Background()
	f, taskValue, proposal := acceptedExecutionFixture(t, "-scalar-reserve")
	if len(taskValue.RepositoryTargets) != 0 {
		t.Skip("fixture already uses multi-repo layout")
	}
	if taskValue.RepositoryID == "" {
		t.Fatal("scalar task has no RepositoryID")
	}
	store := NewWorkflowProgressionStore(f.db)
	if _, _, err := store.ReserveTaskExecution(ctx, f.session, taskValue.ID); err != nil {
		t.Fatal("ReserveTaskExecution failed:", err)
	}
	var admissionCount int
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM execution_proposal_admissions WHERE proposal_id=?`, proposal.ID).Scan(&admissionCount); err != nil {
		t.Fatal("admission query failed:", err)
	} else if admissionCount != 1 {
		t.Fatalf("expected 1 admission, got %d", admissionCount)
	}
	var runCount int
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE execution_proposal_id=?`, proposal.ID).Scan(&runCount); err != nil {
		t.Fatal("run query failed:", err)
	} else if runCount != 0 {
		t.Fatalf("ReserveTaskExecution created %d runs, expected 0", runCount)
	}
	updated, err := NewExecutionProposalStore(f.db).Get(ctx, proposal.ID)
	if err != nil {
		t.Fatal("Get after reserve failed:", err)
	}
	if updated.Status != executionproposal.StatusAdmitted {
		t.Fatalf("proposal status not ADMITTED: %s", updated.Status)
	}
}

func TestScalarExecutionPlanDerivesSingleMemberTarget(t *testing.T) {
	ctx := context.Background()
	f, taskValue, proposal := acceptedExecutionFixture(t, "-scalar-target")
	if len(taskValue.RepositoryTargets) != 0 {
		t.Skip("fixture already uses multi-repo layout")
	}
	if taskValue.RepositoryID == "" {
		t.Fatal("scalar task has no RepositoryID")
	}
	store := NewWorkflowProgressionStore(f.db)
	if _, _, err := store.ReserveTaskExecution(ctx, f.session, taskValue.ID); err != nil {
		t.Fatal("ReserveTaskExecution failed:", err)
	}
	var planID string
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT runtime_scope_plan_id FROM execution_proposal_admissions WHERE proposal_id=?`, proposal.ID).Scan(&planID); err != nil {
		t.Fatal("plan ID query failed:", err)
	}
	var primaryMemberID, backendIdentity, adapterIdentity string
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT primary_member_id,backend_identity,runtime_adapter_identity FROM runtime_scope_plans WHERE plan_id=?`, planID).Scan(&primaryMemberID, &backendIdentity, &adapterIdentity); err != nil {
		t.Fatal("plan fields query failed:", err)
	}
	if primaryMemberID != taskValue.RepositoryID {
		t.Fatalf("PrimaryMemberID not derived from RepositoryID: got %q want %q", primaryMemberID, taskValue.RepositoryID)
	}
	if backendIdentity == "" {
		t.Fatal("BackendIdentity is empty")
	}
	if adapterIdentity == "" {
		t.Fatal("RuntimeAdapterIdentity is empty")
	}
	var targetsJSON string
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT repository_targets_json FROM runtime_scope_plans WHERE plan_id=?`, planID).Scan(&targetsJSON); err != nil {
		t.Fatal("targets query failed:", err)
	} else if targetsJSON == "" || targetsJSON == "null" {
		t.Fatal("scalar plan has no repository targets")
	}
}

func TestScalarExecutionAdmissionReplayViaReserve(t *testing.T) {
	ctx := context.Background()
	f, taskValue, proposal := acceptedExecutionFixture(t, "-scalar-replay")
	if len(taskValue.RepositoryTargets) != 0 {
		t.Skip("fixture already uses multi-repo layout")
	}
	store := NewWorkflowProgressionStore(f.db)
	if _, _, err := store.ReserveTaskExecution(ctx, f.session, taskValue.ID); err != nil {
		t.Fatal("first ReserveTaskExecution failed:", err)
	}
	var firstPlanID string
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT runtime_scope_plan_id FROM execution_proposal_admissions WHERE proposal_id=?`, proposal.ID).Scan(&firstPlanID); err != nil {
		t.Fatal("first plan ID query failed:", err)
	}
	if _, _, err := store.ReserveTaskExecution(ctx, f.session, taskValue.ID); err != nil {
		t.Fatal("replay ReserveTaskExecution failed:", err)
	}
	var secondPlanID string
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT runtime_scope_plan_id FROM execution_proposal_admissions WHERE proposal_id=?`, proposal.ID).Scan(&secondPlanID); err != nil {
		t.Fatal("second plan ID query failed:", err)
	}
	if secondPlanID != firstPlanID {
		t.Fatalf("replay returned different PlanID: %q vs %q", secondPlanID, firstPlanID)
	}
	var admissionCount int
	if err := f.db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM execution_proposal_admissions WHERE proposal_id=?`, proposal.ID).Scan(&admissionCount); err != nil {
		t.Fatal("admission count query failed:", err)
	} else if admissionCount != 1 {
		t.Fatalf("replay created %d admissions, expected 1", admissionCount)
	}
}
