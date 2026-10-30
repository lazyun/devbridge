package execution

import (
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func exitCode(value int) *int { return &value }

func reportJSON(checkStatus, criterionStatus string) string {
	return `{"version":2,"summary":"verification facts recorded","checks":[{"index":0,"status":"` + checkStatus + `","summary":"agent observation","evidence":"reported evidence","reason":""}],"acceptanceCriteria":[{"index":0,"status":"` + criterionStatus + `","summary":"criterion observation","evidence":"","reason":""}]}`
}

func verificationSpec(kind task.VerificationCheckKind, command, instruction string) task.VerificationSpec {
	return task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "fixture", Checks: []task.VerificationCheck{{Kind: kind, Command: command, Instruction: instruction, Required: true}}, AcceptanceCriteria: []string{"criterion"}, CodexInstruction: "verify"}
}

func TestVerificationReportSeparatesObservedAndReportedFacts(t *testing.T) {
	spec := verificationSpec(task.VerificationCheckTestCommand, "go test ./...", "")
	report, err := buildVerificationReport(spec, reportJSON("PASSED", "UNKNOWN"), []ObservedCommand{{Command: "go test ./...", ExitCode: exitCode(7), Output: "failed"}}, evidence.GitEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	check := report.Checks[0]
	if report.Availability != run.VerificationReportAvailable || report.Status != "" || check.Observed == nil || check.Observed.Status != run.VerificationCheckFailed || check.Reported.Status != run.VerificationCheckPassed || check.Status != run.VerificationCheckFailed {
		t.Fatalf("report=%+v check=%+v", report, check)
	}
}

func TestVerificationReportMissingCommandIsEvidenceNotReportFailure(t *testing.T) {
	report, err := buildVerificationReport(verificationSpec(task.VerificationCheckTestCommand, "go test ./...", ""), reportJSON("PASSED", "PASSED"), nil, evidence.GitEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Checks[0].Observed; got == nil || got.Status != run.VerificationCheckNotRun || got.Reason != "MISSING_EVIDENCE" {
		t.Fatalf("observed=%+v", got)
	}
}

func TestVerificationReportRejectsProseAndShapeMismatch(t *testing.T) {
	spec := verificationSpec(task.VerificationCheckCustom, "", "review architecture")
	if _, err := buildVerificationReport(spec, "all good", nil, evidence.GitEvidence{}); err == nil {
		t.Fatal("freeform prose must not be accepted")
	}
	if _, err := buildVerificationReport(spec, `{"version":2,"summary":"x","checks":[],"acceptanceCriteria":[]}`, nil, evidence.GitEvidence{}); err == nil {
		t.Fatal("incomplete report must not be accepted")
	}
}

func TestVerificationCustomUsesOnlyStructuredReportedFact(t *testing.T) {
	report, err := buildVerificationReport(verificationSpec(task.VerificationCheckCustom, "", "review architecture"), reportJSON("BLOCKED", "UNKNOWN"), nil, evidence.GitEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	check := report.Checks[0]
	if check.Observed != nil || check.Reported.Status != run.VerificationCheckBlocked || check.Status != run.VerificationCheckBlocked {
		t.Fatalf("check=%+v", check)
	}
}

func TestVerificationObservedEvidenceIsBounded(t *testing.T) {
	report, err := buildVerificationReport(verificationSpec(task.VerificationCheckDiff, "git diff --check", ""), reportJSON("UNKNOWN", "UNKNOWN"), []ObservedCommand{{Command: "git diff --check", ExitCode: exitCode(0), Output: strings.Repeat("x", maxPersistedCommandEvidenceBytes+1)}}, evidence.GitEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Checks[0].Observed.Evidence) != maxPersistedCommandEvidenceBytes || !report.Checks[0].Observed.EvidenceTruncated {
		t.Fatalf("observed=%+v", report.Checks[0].Observed)
	}
}
