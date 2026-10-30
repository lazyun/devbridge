package controlapi_test

import "testing"

// Real Core, SQLite, managed worktrees and protocol subprocess. The subprocess
// is an explicit protocol fixture, not evidence of native qualification.
func TestRecoveryReviewEvidenceRangeProduction(t *testing.T) {
	for _, mode := range []string{"review-evidence-range-invalid", "review-evidence-range-valid"} {
		t.Run(mode, func(t *testing.T) { runRecoveryStatusSeparateProcess(t, mode) })
	}
}
