package controlapi_test

import "testing"

// Real Core/store with an explicit separate-process protocol fixture. This
// checks recovery source authority, not native Codex compatibility.
func TestRecoveryReviewTargetIdentityProduction(t *testing.T) {
	for _, mode := range []string{"review-target-origin-ref", "review-target-observed-commit", "review-target-development-cycle"} {
		t.Run(mode, func(t *testing.T) { runRecoveryStatusSeparateProcess(t, mode) })
	}
}
