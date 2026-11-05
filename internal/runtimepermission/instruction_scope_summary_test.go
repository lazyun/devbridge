package runtimepermission

import (
	"strings"
	"testing"
)

// All inputs are inert strings and flags. These are not valid permission
// manifests; no policy validation, application, filesystem, or consumer runs.
func TestInstructionScopeSummary(t *testing.T) {
	// Exact wording below is the selected candidate contract, not a mandated
	// preexisting documentation format. The original eight vectors are retained.
	const completeScope = "the complete set of approved write grants listed in the policy JSON above remains the delivery scope; each REPOSITORY grant permits product changes only in its specified repository, and each PATHS grant permits changes only at its listed paths within its specified root"
	pathA := WriteGrant{Root: "/synthetic/alpha", Kind: "PATHS", Paths: []string{"alpha-src", "alpha-tests"}}
	pathB := WriteGrant{Root: "/synthetic/beta", Kind: "PATHS", Paths: []string{"beta-lib"}}
	repoA := WriteGrant{Root: "/synthetic/alpha", Kind: "REPOSITORY"}
	cases := []struct {
		name            string
		trusted         bool
		purpose         string
		writes          []WriteGrant
		json            string
		forbiddenScopes []string
		wantScope       string
		role            string
	}{
		{
			name: "two_paths_ab", trusted: true, purpose: "IMPLEMENTATION",
			writes:          []WriteGrant{pathA, pathB},
			json:            `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/alpha","kind":"PATHS","paths":["alpha-src","alpha-tests"]},{"root":"/synthetic/beta","kind":"PATHS","paths":["beta-lib"]}]}`,
			forbiddenScopes: []string{"the approved PATHS write grant still permits changes only at: alpha-src, alpha-tests (in /synthetic/alpha)", "the approved PATHS write grant still permits changes only at: beta-lib (in /synthetic/beta)"},
			wantScope:       completeScope,
		},
		{
			name: "two_paths_ba", trusted: true, purpose: "IMPLEMENTATION",
			writes:          []WriteGrant{pathB, pathA},
			json:            `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/beta","kind":"PATHS","paths":["beta-lib"]},{"root":"/synthetic/alpha","kind":"PATHS","paths":["alpha-src","alpha-tests"]}]}`,
			forbiddenScopes: []string{"the approved PATHS write grant still permits changes only at: alpha-src, alpha-tests (in /synthetic/alpha)", "the approved PATHS write grant still permits changes only at: beta-lib (in /synthetic/beta)"},
			wantScope:       completeScope,
		},
		{
			name: "mixed_repository_paths", trusted: true, purpose: "IMPLEMENTATION",
			writes:          []WriteGrant{repoA, pathB},
			json:            `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/alpha","kind":"REPOSITORY"},{"root":"/synthetic/beta","kind":"PATHS","paths":["beta-lib"]}]}`,
			forbiddenScopes: []string{"the approved PATHS write grant still permits changes only at: beta-lib (in /synthetic/beta)"},
			wantScope:       completeScope,
		},
		{
			name: "mixed_paths_repository", trusted: true, purpose: "IMPLEMENTATION",
			writes:          []WriteGrant{pathB, repoA},
			json:            `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/beta","kind":"PATHS","paths":["beta-lib"]},{"root":"/synthetic/alpha","kind":"REPOSITORY"}]}`,
			forbiddenScopes: []string{"the approved PATHS write grant still permits changes only at: beta-lib (in /synthetic/beta)"},
			wantScope:       completeScope,
		},
		// Candidate-only ninth case: the baseline did not exercise two REPOSITORY grants.
		{
			name: "candidate_two_repositories", trusted: true, purpose: "IMPLEMENTATION",
			writes:    []WriteGrant{repoA, {Root: "/synthetic/beta", Kind: "REPOSITORY"}},
			json:      `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/alpha","kind":"REPOSITORY"},{"root":"/synthetic/beta","kind":"REPOSITORY"}]}`,
			wantScope: completeScope,
		},
		{
			name: "single_paths_control", trusted: true, purpose: "IMPLEMENTATION",
			writes:    []WriteGrant{pathA},
			json:      `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/alpha","kind":"PATHS","paths":["alpha-src","alpha-tests"]}]}`,
			wantScope: "the approved PATHS write grant still permits changes only at: alpha-src, alpha-tests (in /synthetic/alpha)",
		},
		{
			name: "single_repository_control", trusted: true, purpose: "IMPLEMENTATION",
			writes:    []WriteGrant{repoA},
			json:      `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/alpha","kind":"REPOSITORY"}]}`,
			wantScope: "the approved REPOSITORY write grant permits product changes in that repository",
		},
		{
			name: "untrusted_paths_control", purpose: "IMPLEMENTATION",
			writes: []WriteGrant{pathA, pathB},
			json:   `{"purpose":"IMPLEMENTATION","scratch":"synthetic-scratch","writes":[{"root":"/synthetic/alpha","kind":"PATHS","paths":["alpha-src","alpha-tests"]},{"root":"/synthetic/beta","kind":"PATHS","paths":["beta-lib"]}]}`,
			role:   "This is an IMPLEMENTATION turn. Implement the approved engineering task within the listed writes grants:",
		},
		{
			name: "trusted_verification_control", trusted: true, purpose: "VERIFICATION",
			json: `{"purpose":"VERIFICATION","scratch":"synthetic-scratch"}`,
			role: "This is a trusted VERIFICATION turn under an explicit project-level authorization.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := TaskPermissions{Purpose: tc.purpose, ScratchRoot: "synthetic-scratch", Writes: tc.writes}
			if tc.trusted {
				p.TrustedExecution = &TrustedExecutionSnapshot{Trusted: true}
			}
			text := p.Instructions()
			prefix := "\nCore operation sandbox policy: " + tc.json + "\n"
			if !strings.HasPrefix(text, prefix) {
				t.Fatalf("fixed metadata prefix differs: got %q, want prefix %q", text, prefix)
			}
			body := strings.TrimPrefix(text, prefix)
			if tc.role != "" {
				if !strings.Contains(body, tc.role) {
					t.Errorf("control role missing %q", tc.role)
				}
				return
			}
			const start = "The sandbox relaxation does not widen the approved engineering scope: "
			const end = ", and Core's end-of-run scope checks enforce it"
			if strings.Count(body, start) != 1 || strings.Count(body, end) != 1 {
				t.Fatalf("scope delimiters missing or ambiguous: %q", body)
			}
			_, rest, _ := strings.Cut(body, start)
			scope, _, found := strings.Cut(rest, end)
			if !found {
				t.Fatalf("scope delimiters out of order: %q", body)
			}
			if !strings.Contains(body, "deliver only within that scope.") {
				t.Fatalf("missing full-scope delivery context: %q", body)
			}
			// Reject only the known contradictory single-PATHS summary when
			// more than that one grant is present in the fixed JSON above.
			// Full enumeration or an explicit reference to all JSON writes may
			// pass. This is not a general semantic parser or security proof.
			for _, forbidden := range tc.forbiddenScopes {
				if scope == forbidden {
					t.Errorf("multi-grant scope uses known single-grant-only template: %q", scope)
				}
			}
			if tc.wantScope != "" && scope != tc.wantScope {
				t.Errorf("selected scope contract differs: got %q, want %q", scope, tc.wantScope)
			}
		})
	}
}
