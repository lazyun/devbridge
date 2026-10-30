package task

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCanonicalizeExecutionScopeFilesDirectoriesAndNestedPaths(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"allowed", "allowed/nested", "outside"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "allowed", "exact.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalizeExecutionScope(root, ExecutionScope{Kind: ExecutionScopePaths, Paths: []string{"allowed/exact.txt", "allowed/nested"}})
	if err != nil {
		t.Fatal(err)
	}
	want := ExecutionScope{Kind: ExecutionScopePaths, Paths: []string{"allowed/exact.txt", "allowed/nested"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope=%+v want %+v", got, want)
	}
	repository, err := CanonicalizeExecutionScope(root, ExecutionScope{Kind: ExecutionScopeRepository})
	if err != nil || repository.Kind != ExecutionScopeRepository || len(repository.Paths) != 0 {
		t.Fatalf("repository scope=%+v err=%v", repository, err)
	}
}

func TestCanonicalizeExecutionScopeRejectsEscapesMissingAndDuplicates(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "allowed"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for name, scope := range map[string]ExecutionScope{
		"absolute":            {Kind: ExecutionScopePaths, Paths: []string{filepath.Join(root, "allowed")}},
		"parent traversal":    {Kind: ExecutionScopePaths, Paths: []string{"allowed/../outside"}},
		"missing":             {Kind: ExecutionScopePaths, Paths: []string{"missing"}},
		"symlink escape":      {Kind: ExecutionScopePaths, Paths: []string{"escape"}},
		"canonical duplicate": {Kind: ExecutionScopePaths, Paths: []string{"allowed", "alias"}},
		"repository paths":    {Kind: ExecutionScopeRepository, Paths: []string{"allowed"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalizeExecutionScope(root, scope); err == nil {
				t.Fatalf("scope unexpectedly accepted: %+v", scope)
			}
		})
	}
}

func TestNewWorkSpecRequiresExplicitNonLegacyExecutionScope(t *testing.T) {
	base := WorkSpec{Goal: "goal", CodexInstruction: "instruction"}
	if err := ValidateWorkSpec(base); err == nil {
		t.Fatal("missing execution scope accepted")
	}
	base.ExecutionScope = ExecutionScope{Kind: ExecutionScopeLegacyUnspecified}
	if err := ValidateWorkSpec(base); err == nil {
		t.Fatal("new legacy execution scope accepted")
	}
	if err := ValidatePersistedWorkSpec(base); err != nil {
		t.Fatalf("migration-only legacy scope rejected: %v", err)
	}
	base.ExecutionScope = ExecutionScope{Kind: ExecutionScopeRepository}
	if err := ValidateWorkSpec(base); err != nil {
		t.Fatalf("explicit repository scope rejected: %v", err)
	}
}

func TestVerificationSpecSupportsBoundedCheckKinds(t *testing.T) {
	for _, kind := range []VerificationCheckKind{
		VerificationCheckTestCommand,
		VerificationCheckStaticAnalysis,
		VerificationCheckDiff,
		VerificationCheckGitAssertion,
		VerificationCheckCustom,
	} {
		spec := VerificationSpec{
			Mode: VerificationModeRun, Scope: "confirmed scope",
			Checks:             []VerificationCheck{{Kind: kind, Instruction: "perform the confirmed check", Paths: []string{"changed.go"}, Packages: []string{"./..."}, Required: true}},
			AcceptanceCriteria: []string{"the confirmed check passes"}, CodexInstruction: "Verify the confirmed change without modifying files.",
		}
		if err := ValidateVerificationSpec(spec); err != nil {
			t.Fatalf("kind %s: %v", kind, err)
		}
	}
}
