package sqlite

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
)

func TestInspectionCanonicalCancellationExactOwnerAndUnknown(t *testing.T) {
	f := newAgentStorageFixture(t, "inspection-cancel")
	ctx := context.Background()
	probes := NewContextProbeStore(f.db)
	p := contextprobe.Probe{ID: "inspection-cancel", AISessionID: f.session.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository, Paths: []string{}}, CodexInstruction: "inspect", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: testNow, UpdatedAt: testNow}
	if err := probes.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	p, err := probes.Update(ctx, p.ID, func(v *contextprobe.Probe) error {
		v.Status = contextprobe.StatusRunning
		v.StartedAt = &testNow
		v.ThreadID = "inspection-thread"
		v.TurnID = "inspection-turn"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewExecutionCancellationStore(f.db)
	in := executioncancel.Input{ProjectID: p.ProjectID, AISessionID: p.AISessionID, OwnerType: "INSPECTION", OwnerID: p.ID, RunID: p.RunID, RequestID: "cancel-inspection", Reason: "user canceled"}
	bad := in
	bad.RunID = "another-run"
	if _, _, err := store.Begin(ctx, bad, true); err == nil {
		t.Fatal("foreign run accepted")
	}
	receipt, dispatch, err := store.Begin(ctx, in, true)
	if err != nil || !dispatch || receipt.State != "CANCEL_REQUESTED" {
		t.Fatalf("%+v %v %v", receipt, dispatch, err)
	}
	if _, dispatch, err = store.Begin(ctx, in, true); err != nil || dispatch {
		t.Fatalf("duplicate signal admitted %v %v", dispatch, err)
	}
	target, dispatch, err := store.ClaimInterrupt(ctx, p.ProjectID, in.RequestID)
	if err != nil || !dispatch || target.ThreadID != p.ThreadID || target.TurnID != p.TurnID {
		t.Fatalf("%+v %v %v", target, dispatch, err)
	}
	if err = store.RecordInterrupt(ctx, p.ProjectID, in.RequestID, "ACKNOWLEDGED"); err != nil {
		t.Fatal(err)
	}
	receipt, err = store.Get(ctx, p.ProjectID, in.RequestID)
	if err != nil || receipt.QuiescenceState == "QUIESCED" {
		t.Fatalf("ACK invented quiescence %+v %v", receipt, err)
	}
	if err = store.FinishInspectionCancellation(ctx, p.RunID, false); err != nil {
		t.Fatal(err)
	}
	receipt, err = store.Get(ctx, p.ProjectID, in.RequestID)
	if err != nil || receipt.State != "RECOVERY_REQUIRED" || !receipt.RecoveryRequired {
		t.Fatalf("lost backend state %+v %v", receipt, err)
	}
	if _, dispatch, err = store.Begin(ctx, in, false); err != nil || dispatch {
		t.Fatalf("unknown replay dispatched %v %v", dispatch, err)
	}
	if err = store.FinishInspectionCancellation(ctx, p.RunID, true); err != nil {
		t.Fatal(err)
	}
	receipt, err = store.Get(ctx, p.ProjectID, in.RequestID)
	if err != nil || receipt.State != "CANCELED" || receipt.QuiescenceState != "QUIESCED" {
		t.Fatalf("terminal %+v %v", receipt, err)
	}
}
