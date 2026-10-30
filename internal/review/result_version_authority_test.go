package review

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
)

func TestV1ReviewRejectsMultiRepositoryAuthority(t *testing.T) {
	base := ImplementationResult{Version: 1, ReviewDisposition: DispositionApprovalReady, Summary: "Review scalar scope", WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update scalar repository", Constraints: []string{}, CodexInstruction: "Update scalar scope", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"Tests pass"}, CodexInstruction: "Verify scalar scope"}}
	parse := func(v ImplementationResult) error {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseResult(string(raw))
		return err
	}
	report := func(v ImplementationResult) ReviewReport {
		r := validBlockedReport()
		r.Disposition, r.PrimaryMemberID = v.ReviewDisposition, v.PrimaryMemberID
		r.WorkSpec, r.VerificationSpec = &v.WorkSpec, &v.VerificationSpec
		return r
	}
	if err := parse(base); err != nil {
		t.Fatal(err)
	}
	if err := report(base).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ImplementationResult)
	}{
		{"primary member", func(v *ImplementationResult) { v.PrimaryMemberID = "member-other" }},
		{"verification repository", func(v *ImplementationResult) { v.VerificationSpec.Checks[0].RepositoryID = "repo-other" }},
		{"verification repository set", func(v *ImplementationResult) { v.VerificationSpec.Checks[0].RepositoryIDs = []string{"repo-other"} }},
		{"verification directory", func(v *ImplementationResult) { v.VerificationSpec.Checks[0].WorkingDirectory = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			v.VerificationSpec = task.CloneVerificationSpec(base.VerificationSpec)
			tc.mutate(&v)
			if err := parse(v); err == nil {
				t.Error("canonical v1 result accepted v2 authority")
			}
			if err := report(v).Validate(); err == nil {
				t.Error("Upper v1 report accepted v2 authority")
			}
		})
	}
}
