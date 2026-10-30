package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
)

func assertDirectFileCode(t *testing.T, err error, code string) {
	t.Helper()
	var domain *directfile.Error
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// Legacy Scope.Enable has no approved-definition identity. Every old entry
// point must stay closed for a FeatureArea that owns a DEVELOPMENT Task; v2
// Task authorization tests cover member/root pinning and mutation fencing.
func assertLegacyDirectFileScopeClosed(t *testing.T, label string) {
	t.Helper()
	_, authority, scope, _ := directFileAuthorityFixture(t, label)
	if _, err := authority.Enable(context.Background(), scope); err == nil {
		t.Fatal("legacy scope-only direct file grant succeeded")
	} else {
		assertDirectFileCode(t, err, directfile.CapabilityActive)
	}
}

func TestDirectFileWritableExecutionExcludedAndOtherAreaIndependent(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "exact-area")
}

func TestDirectFilePinnedDirectoryIdentityAndRepositoryCheckout(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "pin-retired")
}

func TestDirectFileSessionBindingAndRestart(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "session-restart")
}

func TestDirectFileUnknownBackendWithoutClaimStillRejected(t *testing.T) {
	assertLegacyDirectFileScopeClosed(t, "unknown-backend")
}
