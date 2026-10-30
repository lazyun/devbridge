package task

import (
	"strings"
	"testing"
)

func TestCommandPermissionGrantChangesImmutableDefinition(t *testing.T) {
	d := Definition{Version: 1, ProjectID: "project", FeatureAreaID: "area", RepositoryID: "repo", WorkSpec: WorkSpec{Goal: "goal", ExecutionScope: ExecutionScope{Kind: ExecutionScopeRepository}}}
	raw, before, err := EncodeDefinition(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "commandPermissions") {
		t.Fatal("legacy encoding changed")
	}
	d.WorkSpec.CommandPermissions = &CommandPermissions{NetworkAccess: true, VerificationOutputs: []string{"artifacts"}}
	encoded, after, err := EncodeDefinition(d)
	if err != nil || before == after {
		t.Fatal("network grant did not change immutable hash")
	}
	frozen, err := DecodeDefinition(encoded, after)
	if err != nil || !frozen.WorkSpec.CommandPermissions.NetworkAccess {
		t.Fatal("grant not persisted")
	}
	copy := CloneWorkSpec(frozen.WorkSpec)
	copy.CommandPermissions.NetworkAccess = false
	copy.CommandPermissions.VerificationOutputs[0] = "changed"
	if !frozen.WorkSpec.CommandPermissions.NetworkAccess || frozen.WorkSpec.CommandPermissions.VerificationOutputs[0] != "artifacts" {
		t.Fatal("cloning changed frozen grant")
	}
}

func TestCommandPermissionOutputRejectsTraversalAndMetadataBeforeApproval(t *testing.T) {
	w := WorkSpec{Goal: "goal", CodexInstruction: "complete engineering instruction", ExecutionScope: ExecutionScope{Kind: ExecutionScopeRepository}, CommandPermissions: &CommandPermissions{}}
	for _, path := range []string{"../outside", ".", "/absolute", "dist/../source", ".git/objects", "cache/.agents", ".codex/cache"} {
		w.CommandPermissions.VerificationOutputs = []string{path}
		if ValidateWorkSpec(w) == nil {
			t.Fatalf("unsafe output accepted: %s", path)
		}
	}
	w.CommandPermissions.VerificationOutputs = []string{"artifacts/reports"}
	if err := ValidateWorkSpec(w); err != nil {
		t.Fatal(err)
	}
}
