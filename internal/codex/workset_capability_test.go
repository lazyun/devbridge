package codex

import (
	"github.com/lazyun/devbridge/internal/workset"
	"strings"
	"testing"
)

func TestExactMixedWorksetIsAdmittedForExplicitRuntimeBinding(t *testing.T) {
	set := workset.Set{Version: 1, WorkspaceID: "workspace", Targets: []workset.Target{
		{RepositoryID: "app", MemberID: "app-member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}},
		{RepositoryID: "sdk", MemberID: "sdk-member", MemberVersion: 1, OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
	}}
	if err := ValidateExactWorkset(set); err != nil {
		t.Fatalf("valid mixed workset rejected: %v", err)
	}
	set.Targets = set.Targets[:1]
	if err := ValidateExactWorkset(set); err != nil {
		t.Fatalf("valid single-root workset rejected: %v", err)
	}
}

func TestExactWorksetStillRejectsInvalidFrozenTargets(t *testing.T) {
	err := ValidateExactTargets([]workset.Target{{RepositoryID: "app"}})
	if err == nil {
		t.Fatal("invalid frozen target accepted")
	}
}
