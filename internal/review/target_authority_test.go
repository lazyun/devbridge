package review

import (
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/workset"
)

func TestReviewTargetAuthorityPreservesFrozenIdentity(t *testing.T) {
	admitted := []workset.Target{{RepositoryID: "app", MemberID: "app-member", MemberVersion: 2, OriginCommit: strings.Repeat("a", 40), OriginRef: "refs/heads/main", DevelopmentCycleID: "cycle", ObservedCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}}
	for _, tc := range []struct {
		name   string
		mutate func(*workset.Target)
	}{
		{"repository", func(t *workset.Target) { t.RepositoryID = "other" }},
		{"member", func(t *workset.Target) { t.MemberID = "other" }},
		{"version", func(t *workset.Target) { t.MemberVersion++ }},
		{"origin", func(t *workset.Target) { t.OriginCommit = strings.Repeat("c", 40) }},
		{"ref", func(t *workset.Target) { t.OriginRef = "refs/heads/other" }},
		{"cycle", func(t *workset.Target) { t.DevelopmentCycleID = "other" }},
		{"observed", func(t *workset.Target) { t.ObservedCommit = strings.Repeat("c", 40) }},
		{"read", func(t *workset.Target) { t.Read = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proposed := workset.CloneTargets(admitted)
			tc.mutate(&proposed[0])
			if err := ValidateAdmittedTargets(admitted, proposed); err == nil {
				t.Fatal("changed identity accepted")
			}
		})
	}
	proposed := workset.CloneTargets(admitted)
	proposed[0].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"new.go"}}
	if err := ValidateAdmittedTargets(admitted, proposed); err != nil {
		t.Fatalf("legitimate write proposal rejected: %v", err)
	}
	if err := ValidateAdmittedTargets(admitted, append(proposed, proposed[0])); err == nil {
		t.Fatal("duplicate set accepted")
	}
	if err := ValidateAdmittedTargets(nil, proposed); err == nil {
		t.Fatal("missing admission accepted")
	}
}
