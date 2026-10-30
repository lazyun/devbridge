package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

func TestPreWorkTaskCancellationEligibilityMatchesCanonicalOwner(t *testing.T) {
	ctx := context.Background()
	for _, state := range []string{"eligible", "inspection", "pending-execution", "active-run", "terminal", "pending-receipt"} {
		t.Run(state, func(t *testing.T) {
			f := newAgentStorageFixture(t, "prework-"+state)
			store := NewExecutionCancellationStore(f.db)
			in := executioncancel.Input{ProjectID: f.project.ID, AISessionID: f.session.ID, OwnerType: "TASK", OwnerID: f.task.ID, RequestID: "cancel-prework"}
			switch state {
			case "inspection":
				probe := contextprobe.Probe{ID: "inspection-owner", AISessionID: f.session.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository, Paths: []string{}}, CodexInstruction: "inspect", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: testNow, UpdatedAt: testNow}
				probes := NewContextProbeStore(f.db)
				if err := probes.Create(ctx, probe); err != nil {
					t.Fatal(err)
				}
				probe, err := probes.Get(ctx, probe.ID)
				if err != nil {
					t.Fatal(err)
				}
				f.task, err = NewTaskStore(f.db).Get(ctx, probe.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				in.OwnerID = f.task.ID
			case "pending-execution", "active-run":
				id := seedExecutionOwner(t, f.db, f.task, f.session, "prework-execution")
				if state == "active-run" {
					if _, _, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, id, "prework-run", testNow); err != nil {
						t.Fatal(err)
					}
				}
			case "terminal":
				if _, _, err := store.Begin(ctx, in, false); err != nil {
					t.Fatal(err)
				}
			case "pending-receipt":
				var cancel executioncancel.Input
				f, _, _, cancel = cancellationStoreFixture(t, "prework-pending-receipt")
				store = NewExecutionCancellationStore(f.db)
				if _, _, err := store.Begin(ctx, cancel, true); err != nil {
					t.Fatal(err)
				}
				in.ProjectID, in.AISessionID, in.OwnerID = f.project.ID, f.session.ID, f.task.ID
			}
			before, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			receiptsBefore, err := store.List(ctx, f.project.ID, f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			available, err := store.PreWorkTaskCancellationEligible(ctx, f.project.ID, f.task.ID)
			if err != nil || available != (state == "eligible") {
				t.Fatalf("available=%t err=%v", available, err)
			}
			after, _ := NewTaskStore(f.db).Get(ctx, f.task.ID)
			receiptsAfter, _ := store.List(ctx, f.project.ID, f.task.ID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(receiptsBefore, receiptsAfter) {
				t.Fatal("eligibility read changed owner or receipt")
			}
			if state == "eligible" {
				if ok, err := store.PreWorkTaskCancellationEligible(ctx, "foreign", f.task.ID); ok || !errors.Is(err, executioncancel.ErrProjectMismatch) {
					t.Fatalf("foreign owner accepted: %t %v", ok, err)
				}
				receipt, dispatch, err := store.Begin(ctx, in, false)
				if err != nil || dispatch || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" || receipt.OwnerType != "TASK" || receipt.OwnerID != f.task.ID || receipt.RunID != "" {
					t.Fatalf("canonical command diverged: %+v dispatch=%t err=%v", receipt, dispatch, err)
				}
			}
		})
	}
}
