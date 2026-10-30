package task

import (
	"errors"
	"testing"
)

func TestExecutableVerificationSeparatesCommandsAndCustomAnalysis(t *testing.T) {
	for _, tc := range []struct {
		name     string
		check    VerificationCheck
		accepted bool
	}{
		{"required custom command", VerificationCheck{Kind: VerificationCheckCustom, Command: "test -f x", Required: true}, true},
		{"required custom instruction", VerificationCheck{Kind: VerificationCheckCustom, Instruction: "inspect x", Required: true}, true},
		{"optional custom", VerificationCheck{Kind: VerificationCheckCustom, Instruction: "inspect x"}, true},
		{"instruction only command", VerificationCheck{Kind: VerificationCheckTestCommand, Instruction: "run tests", Required: true}, false},
		{"actual command", VerificationCheck{Kind: VerificationCheckTestCommand, Command: "go test ./x", Required: true}, true},
		{"unsupported Git", VerificationCheck{Kind: VerificationCheckGitAssertion, Instruction: "everything is fine", Required: true}, false},
		{"typed Git", VerificationCheck{Kind: VerificationCheckGitAssertion, Instruction: "worktree is unchanged", Required: true}, true},
		{"nested Git", VerificationCheck{Kind: VerificationCheckGitAssertion, Instruction: "worktree is clean", Required: true, RepositoryID: "repo", WorkingDirectory: "sub"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := VerificationSpec{Mode: VerificationModeRun, Scope: "exact", Checks: []VerificationCheck{tc.check}, AcceptanceCriteria: []string{"complete"}, CodexInstruction: "Verify"}
			if err := ValidatePersistedVerificationSpec(spec); err != nil {
				t.Fatalf("historical spec no longer readable: %v", err)
			}
			if err := ValidateExecutableVerificationSpec(spec); (err == nil) != tc.accepted {
				t.Fatalf("accepted=%t err=%v", tc.accepted, err)
			} else if err != nil && !errors.Is(err, ErrUnobservableRequiredCheck) {
				t.Fatalf("unobservable check lost typed identity: %v", err)
			}
		})
	}
}
