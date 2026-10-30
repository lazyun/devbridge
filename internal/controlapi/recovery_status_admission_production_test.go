package controlapi_test

import (
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
)

func TestRecoveryStatusOfflineSupersededSourceAdmission(t *testing.T) {
	var fixture *upperLifecycleFixture
	var command controlapi.RecoveryStatusCommand
	var calls *atomic.Int64
	runRecoveryStatusOffline(t, "revision-required", func(f *upperLifecycleFixture, _ controlplane.Config, cmd controlapi.RecoveryStatusCommand, n *atomic.Int64) bool {
		fixture, command, calls = f, cmd, n
		return true
	})
	if fixture == nil {
		t.Fatal("fixture did not reach prepared recovery")
	}
	current, err := fixture.plane.Tasks.Get(fixture.ctx, command.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentExecutionID == command.OperationID || current.CurrentDefinitionRevisionID == command.TaskDefinitionRevisionID {
		t.Fatal("canonical confirmed successor missing")
	}
	record, err := fixture.plane.RecoveryStatus.Read(fixture.ctx, command.ID)
	if err != nil {
		t.Fatal(err)
	}
	spec := record.Spec
	spec.ActorID = ""
	spec.Generation++
	spec.RequestID = "superseded-source-recovery"
	spec.PayloadHash = spec.ComputePayloadHash()
	before := calls.Load()
	var count int
	if err = fixture.plane.DB.SQL().QueryRowContext(fixture.ctx, `SELECT count(*) FROM recovery_status_records`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.facade.PrepareRecoveryStatusForSession(fixture.ctx, fixture.session, spec); err == nil {
		t.Fatal("superseded source admitted new recovery generation")
	}
	var after int
	if err = fixture.plane.DB.SQL().QueryRowContext(fixture.ctx, `SELECT count(*) FROM recovery_status_records`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if count != after || calls.Load() != before {
		t.Fatal("rejected old source created record or invoked provider")
	}
}

func TestRecoveryStatusOfflinePreparedCancellationBlocksStart(t *testing.T) {
	runRecoveryStatusOffline(t, "complete-old-unknown", func(f *upperLifecycleFixture, _ controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
		before := calls.Load()
		receipt, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: cmd.OperationID, ExecutionID: cmd.OperationID, RunID: cmd.RunID, RequestID: "cancel-before-recovery-start", Reason: "Explicitly cancel original operation before diagnostic start"})
		if err != nil {
			t.Logf("canonical cancellation remains unresolved: state=%s error=%v", receipt.State, err)
		}
		t.Logf("canonical pre-start cancellation receipt state=%s", receipt.State)
		if receipt.State != "CANCELED" && receipt.State != "RECOVERY_REQUIRED" && receipt.State != "CANCEL_REQUESTED" {
			t.Fatalf("no durable canonical cancellation intent: %+v %v", receipt, err)
		}
		if _, err = f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err == nil {
			t.Fatal("prepared diagnostic started across canonical cancellation")
		}
		record, err := f.plane.RecoveryStatus.Read(f.ctx, cmd.ID)
		if err != nil {
			t.Fatal(err)
		}
		if record.RecoveryRunID != "" || calls.Load() != before {
			t.Fatal("cancellation-blocked start created child or invoked provider")
		}
		if record.SourceResponseDisposition != "UNKNOWN" {
			t.Fatal("original unknown response rewritten")
		}
		return false
	})
}
