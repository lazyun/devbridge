package controlapi_test

import (
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/recoverystatus"
)

func TestRecoveryResolutionRejectsExtraneousPurposeForSession(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVBRIDGE_RECOVERY_FAKE_SERVER", "1")
	t.Setenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY", executable)
	for _, mode := range []string{"complete-old-unknown", "partial-continuation"} {
		t.Run(mode, func(t *testing.T) {
			runRecoveryStatusOffline(t, mode, func(f *upperLifecycleFixture, cfg controlplane.Config, cmd controlapi.RecoveryStatusCommand, calls *atomic.Int64) bool {
				if _, err := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, cmd); err != nil {
					for cause := err; cause != nil; cause = errors.Unwrap(cause) {
						t.Logf("recovery start cause %T: %v", cause, cause)
					}
					t.Fatal(err)
				}
				assessed := waitRecoveryRecord(t, f, cmd.ID, func(r recoverystatus.Record) bool { return r.State == "ASSESSED" || r.State == "FAILED" })
				view, err := f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, cmd.ID)
				if err != nil || view.ResolveCommand == nil {
					t.Fatalf("resolution unavailable: state=%s err=%v", assessed.State, err)
				}
				in := *view.ResolveCommand
				in.Purpose = &recoverystatus.PurposeProposal{RecoveryID: cmd.ID, OwnerKind: "IMPLEMENTATION"}
				hash := func() {
					in.PayloadHash = (recoverystatus.ResolveInput{ID: in.ID, RequestID: in.RequestID, RuntimeEpoch: in.ExpectedRecoveryEpoch, AssessmentHash: in.AssessmentHash, ReportHash: in.ReportHash, Purpose: in.Purpose}).ComputePayloadHash()
				}
				hash()
				before := definitionFormalSnapshot(t, f, definitionFormalEveryTable(t, f))
				if _, err = f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, in); err == nil {
					t.Fatal("irrelevant Purpose consumed resolution")
				}
				if after := definitionFormalSnapshot(t, f, definitionFormalEveryTable(t, f)); after != before {
					t.Fatal("rejected Purpose changed durable state")
				}
				in.Purpose = nil
				hash()
				resolved, err := f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, in)
				if err != nil || resolved.Record.State != "RESOLVED" {
					t.Fatalf("corrected request failed: %v", err)
				}
				if calls.Load() != 4 {
					t.Fatalf("resolution duplicated model calls: %d", calls.Load())
				}
				return false
			})
		})
	}
}
