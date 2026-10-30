package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/workset"
)

func targetDefinition() Definition {
	return Definition{Version: 2, ProjectID: "p", RepositoryTargets: []workset.Target{
		{RepositoryID: "app", MemberID: "member-app", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}},
		{RepositoryID: "sdk", MemberID: "member-sdk", MemberVersion: 1, OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
	}, WorkSpec: WorkSpec{Goal: "app with sdk", CodexInstruction: " exact\ninstructions\n", ExecutionScope: ExecutionScope{Kind: ExecutionScopeTargets}}, VerificationSpec: VerificationSpec{Mode: VerificationModeRun, Scope: "integration", CodexInstruction: "verify exact", AcceptanceCriteria: []string{"both covered"}, Checks: []VerificationCheck{{Kind: VerificationCheckCustom, Instruction: "integration", Required: true, RepositoryID: "app", WorkingDirectory: ".", RepositoryIDs: []string{"app", "sdk"}}}}}
}

func TestDefinitionTargetsAreExclusiveAndRepositoryQualified(t *testing.T) {
	d := targetDefinition()
	raw, hash, err := EncodeDefinition(d)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := DecodeDefinition(raw, hash)
	if err != nil || restored.WorkSpec.CodexInstruction != d.WorkSpec.CodexInstruction {
		t.Fatalf("instruction changed: %v", err)
	}
	applied := restored.ApplyTo(Task{RepositoryID: "legacy", BaseCommit: strings.Repeat("c", 40)})
	if applied.RepositoryID != "" || applied.BaseCommit != "" || len(applied.RepositoryTargets) != 2 {
		t.Fatal("v2 fell back to legacy identity")
	}
	applied.RepositoryTargets[0].Write.Paths[0] = "elsewhere"
	if restored.RepositoryTargets[0].Write.Paths[0] != "src" {
		t.Fatal("target mutation escaped clone")
	}
	// v2 bytes written before the runtime layout field remain visible to audit
	// and review, but cannot be dispatched as new multi-member Coding work.
	if restored.PrimaryMemberID != "" {
		t.Fatal("fixture unexpectedly supplied a primary member")
	}
	restored.WorkSpec.ImplementationMode = ImplementationCodingAgent
	if err := ValidateExecutablePrimary(restored); err == nil {
		t.Fatal("legacy multi-member Coding definition without primary was executable")
	}
	cases := map[string]func(*Definition){
		"dual authority":       func(v *Definition) { v.RepositoryID = "app" },
		"legacy scope":         func(v *Definition) { v.WorkSpec.ExecutionScope.Kind = ExecutionScopeRepository },
		"duplicate repository": func(v *Definition) { v.RepositoryTargets[1].RepositoryID = "app" },
		"missing coverage":     func(v *Definition) { v.VerificationSpec.Checks[0].RepositoryIDs = []string{"app"} },
		"wrong repository":     func(v *Definition) { v.VerificationSpec.Checks[0].RepositoryID = "backend" },
		"escaping directory":   func(v *Definition) { v.VerificationSpec.Checks[0].WorkingDirectory = "../sdk" },
		"v1 smuggling":         func(v *Definition) { v.Version = 1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := targetDefinition()
			change(&v)
			if _, _, err := EncodeDefinition(v); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
}

func TestDefinitionLegacyBytesVerifyWithoutReencoding(t *testing.T) {
	raw := "{ \"version\": 1, \"repositoryId\": \"legacy\", \"baseCommit\": \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\" }\n"
	sum := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(sum[:])
	d, err := DecodeDefinition(raw, hash)
	if err != nil || d.RepositoryID != "legacy" || len(d.RepositoryTargets) != 0 {
		t.Fatalf("legacy=%+v err=%v", d, err)
	}
	if _, err := DecodeDefinition(strings.TrimSpace(raw), hash); err == nil {
		t.Fatal("altered legacy bytes accepted")
	}
}

func TestProjectPlanningIntentHasNoRepositoryOrDefinition(t *testing.T) {
	modules, _ := taskRegistries(t, "project", true)
	store := NewMemoryStore()
	service := NewService(store, nil, modules, nil, nil, nil)
	input := CreateInput{RequestID: "planning-request", ProjectID: "project", FeatureAreaID: "feature", ModuleID: "module", Goal: "investigate app and sdk"}
	value, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if value.RepositoryID != "" || value.BaseCommit != "" || value.CurrentDefinitionRevisionID != "" || len(value.RepositoryTargets) != 0 || value.WorkSpec.CodexInstruction != "" {
		t.Fatalf("planning granted authority: %+v", value)
	}
	replay, err := service.Create(context.Background(), input)
	if err != nil || replay.ID != value.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	input.Goal = "changed payload"
	if _, err := service.Create(context.Background(), input); err == nil {
		t.Fatal("conflicting replay accepted")
	}
}

func TestTargetRevisionCannotBeMutatedThroughGenericTaskUpdate(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	d := targetDefinition()
	value := d.ApplyTo(Task{ID: "task-targets"})
	if err := store.Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Update(ctx, value.ID, func(v *Task) error {
		v.RepositoryTargets[0].Write.Paths[0] = "unauthorized"
		return nil
	}); err == nil {
		t.Fatal("generic update widened target scope")
	}
	after, err := store.Get(ctx, value.ID)
	if err != nil || after.RepositoryTargets[0].Write.Paths[0] != "src" || after.CurrentDefinitionRevisionID != before.CurrentDefinitionRevisionID {
		t.Fatalf("rejected mutation changed stored authority: %+v %v", after, err)
	}
	stored, err := store.GetDefinitionRevision(ctx, before.CurrentDefinitionRevisionID)
	if err != nil || stored.Definition.RepositoryTargets[0].Write.Paths[0] != "src" {
		t.Fatalf("revision changed: %+v %v", stored, err)
	}
}
