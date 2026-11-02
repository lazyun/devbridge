package sqlite

import (
	"github.com/lazyun/devbridge/internal/executionproposal"
	"testing"
)

// Pure identity-presence predicate; integration covers actual issued FILE
// verification, existing storage eligibility tests cover epoch/claim/latest.
func TestExecutionCancellationHasEitherIssuedRun(t *testing.T) {
	for _, tc := range []struct {
		name, implementation, verification string
		want                               bool
	}{
		{"no-run", "", "", false},
		{"implementation", "implementation", "", true},
		{"verification-only", "", "verification", true},
		{"implementation-and-verification", "implementation", "verification", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := executionproposal.Proposal{ImplementationRunID: tc.implementation, VerificationRunID: tc.verification}
			if got := executionCancellationHasRun(e); got != tc.want {
				t.Fatalf("hasRun=%t want%t", got, tc.want)
			}
		})
	}
}
