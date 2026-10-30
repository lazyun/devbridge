package run

import (
	"testing"

	"github.com/lazyun/devbridge/internal/task"
)

func TestRequiredCustomAssessmentDoesNotForgeObservedEvidence(t *testing.T) {
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Checks: []task.VerificationCheck{
		{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true},
		{Kind: task.VerificationCheckCustom, Instruction: "Exercise real Codex isolation and resume", Required: true},
	}, AcceptanceCriteria: []string{"all obligations satisfied"}}
	zero := 0
	report := VerificationReport{Version: 2, Availability: VerificationReportAvailable, Summary: "GPT analysis of Codex output", Checks: []VerificationCheckOutcome{
		{ID: "check-1", Index: 0, Kind: "TEST_COMMAND", Required: true, DeclaredCommand: "go test ./...", Reported: VerificationReportedFact{Status: VerificationCheckPassed, Summary: "claimed"}, Observed: &VerificationObservedFact{Status: VerificationCheckPassed, Command: "go test ./...", ExitCode: &zero, Summary: "actual exit 0"}},
		{ID: "check-2", Index: 1, Kind: "CUSTOM", Required: true, Reported: VerificationReportedFact{Status: VerificationCheckPassed, Summary: "actual model scenario assessed", Evidence: "Codex output reference"}},
	}, AcceptanceCriteria: []VerificationCriterionOutcome{{Index: 0, Criterion: spec.AcceptanceCriteria[0], Reported: VerificationReportedFact{Status: VerificationCheckPassed, Summary: "met"}}}}
	if !VerificationEvidenceComplete(report, spec, nil) || !VerificationAssessmentPassed(report, spec) {
		t.Fatal("complete command evidence plus explicit CUSTOM assessment was rejected")
	}
	for _, status := range []VerificationCheckStatus{VerificationCheckFailed, VerificationCheckBlocked, VerificationCheckNotRun, VerificationCheckUnknown} {
		report.Checks[1].Reported.Status = status
		if !VerificationEvidenceComplete(report, spec, nil) || VerificationAssessmentPassed(report, spec) {
			t.Fatalf("%s must remain a readable complete report without enabling completion", status)
		}
	}
	report.Checks[1].Reported.Status = VerificationCheckPassed
	report.Checks[1].Observed = &VerificationObservedFact{Status: VerificationCheckPassed, Summary: "fabricated observation"}
	if VerificationEvidenceComplete(report, spec, nil) || VerificationAssessmentPassed(report, spec) {
		t.Fatal("CUSTOM assessment acquired authoritative observation")
	}
	report.Checks[1].Observed = nil
	report.Checks[0].Observed = nil
	if VerificationEvidenceComplete(report, spec, nil) || VerificationAssessmentPassed(report, spec) {
		t.Fatal("analysis claims replaced a missing command result")
	}
	report.Checks = report.Checks[:1]
	if VerificationEvidenceComplete(report, spec, nil) || VerificationAssessmentPassed(report, spec) {
		t.Fatal("omitting the required CUSTOM obligation enabled completion")
	}
}
