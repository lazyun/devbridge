package controlapi_test

import (
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlplane"
)

func TestRecoveryPurposePublicConfirmationContinuesAfterCoreRestart(t *testing.T) {
	// This fixture confirms through the public Facade and deliberately stops
	// the Core loop before confirmation. No ApplyConfirmed* call is made here.
	f, purpose, _ := qualifiedRecoveryPurposeFixture(t)
	var beforeRuns int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs`).Scan(&beforeRuns); err != nil {
		t.Fatal(err)
	}
	intent, err := f.plane.Confirmations.Get(f.ctx, purpose.AISessionID, purpose.IntentID)
	if err != nil || intent.State != "COMPLETED" {
		t.Fatalf("public confirmation not completed: %+v %v", intent, err)
	}
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	var count, runs int
	var request, execution, phase string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, purpose.IntentID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if count != 1 {
		t.Fatal("Core bootstrap did not drain the confirmed purpose")
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT c.consume_request_id,c.successor_execution_id,t.phase,(SELECT count(*) FROM runs) FROM recovery_purpose_confirmation_consumptions c JOIN tasks t ON t.current_execution_id=c.successor_execution_id WHERE c.intent_id=?`, purpose.IntentID).Scan(&request, &execution, &phase, &runs); err != nil || !strings.HasPrefix(request, "core_recovery_purpose_") || execution == "" || phase != "IMPLEMENTATION_READY" || runs != beforeRuns {
		t.Fatalf("Core result request=%s execution=%s phase=%s runs=%d/%d err=%v", request, execution, phase, beforeRuns, runs, err)
	}
	commit := *intent.Commit
	commit.SubmittedHash = intent.Submission.PayloadSHA256
	if replay, err := f.facade.CommitConfirmation(f.ctx, commit); err != nil || replay.State != "COMPLETED" {
		t.Fatalf("historical confirmation replay after Core consumption: %+v %v", replay, err)
	}
	read, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: purpose.AISessionID, IntentID: purpose.IntentID, Version: purpose.Version, Purpose: c.RecoveryPurpose, ContentHash: purpose.ContentHash})
	if err != nil || read.State != "COMPLETED" {
		t.Fatalf("historical confirmation read: %+v %v", read, err)
	}
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM recovery_purpose_confirmation_consumptions WHERE intent_id=? AND consume_request_id=? AND successor_execution_id=?`, purpose.IntentID, request, execution).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart replaced the exact consume result: %d %v", count, err)
	}
}
