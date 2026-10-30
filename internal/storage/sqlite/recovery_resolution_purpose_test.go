package sqlite

import (
	"context"
	"errors"
	"testing"

	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func TestRecoveryResolutionRejectsPurposeOutsideRevision(t *testing.T) {
	for _, disposition := range []string{"RECOVERED_COMPLETE", "RECOVERED_PARTIAL"} {
		t.Run(disposition, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				_, in := prepareDiagnosticResolutionTest(t, f, ctx, record, source, disposition)
				in.Purpose = &rs.PurposeProposal{RecoveryID: record.ID, OwnerKind: "IMPLEMENTATION"}
				in.PayloadHash = in.ComputePayloadHash()
				tables := []string{"recovery_status_records", "runs", "tasks", "execution_proposals", "feature_area_agent_claims", "confirmation_intents", "recovery_purpose_link_history"}
				before := platformTableSnapshot(t, f.db, tables)
				resolved, err := store.Resolve(ctx, in)
				if !errors.Is(err, rs.ErrConflict) {
					t.Fatalf("non-revision purpose was not rejected: state=%s err=%v downstreamEvidence=%v", resolved.State, err, validateRecoveryResolutionEvidence(resolved))
				}
				if after := platformTableSnapshot(t, f.db, tables); after != before {
					t.Fatal("rejected purpose changed durable source or resolution state")
				}
				in.Purpose = nil
				in.PayloadHash = in.ComputePayloadHash()
				resolved, err = store.Resolve(ctx, in)
				if err != nil || resolved.State != "RESOLVED" {
					t.Fatalf("corrected request could not resolve: %v", err)
				}
				if err = validateRecoveryResolutionEvidence(resolved); err != nil {
					t.Fatalf("valid resolution unusable downstream: %v", err)
				}
			})
		})
	}
}
