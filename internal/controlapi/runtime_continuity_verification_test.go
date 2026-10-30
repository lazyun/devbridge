package controlapi_test

import (
	"os"
	"testing"
)

// The real native runtime reaches Verification through a measured, formally
// approved Implementation. Only model responses come from a loopback fixture.
func TestRuntimeContinuityOfflineVerificationCoreReopen(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY")
	if native == "" {
		t.Skip("offline installed App Server binary not selected")
	}
	runQualifiedNativeContinuity(t, native, []string{"verification"})
}
