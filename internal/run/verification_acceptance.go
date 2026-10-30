package run

import (
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// VerificationReportMatchesSpec validates coverage independently of whether
// the observations passed. Incomplete evidence is still a durable report.
func VerificationReportMatchesSpec(report VerificationReport, spec task.VerificationSpec) bool {
	if ValidateVerificationReportV2(report) != nil || spec.Mode != task.VerificationModeRun || len(report.Checks) != len(spec.Checks) || len(report.AcceptanceCriteria) != len(spec.AcceptanceCriteria) {
		return false
	}
	for i, check := range report.Checks {
		declared := spec.Checks[i]
		if check.Index != i || check.Kind != string(declared.Kind) || check.Required != declared.Required || check.DeclaredCommand != declared.Command {
			return false
		}
	}
	for i, criterion := range report.AcceptanceCriteria {
		if criterion.Index != i || criterion.Criterion != spec.AcceptanceCriteria[i] {
			return false
		}
	}
	return true
}

// VerificationEvidenceComplete is shared by collection, presentation and the
// acceptance writer. It establishes evidence, not the user's acceptance verdict.
// A failed command with a recorded exit status is complete evidence. Missing,
// pending or unbound command/Git observations cannot acquire authority from
// agent reports. CUSTOM instructions require indexed analysis coverage instead;
// they have no Bridge observation and do not certify engineering correctness.
func VerificationEvidenceComplete(report VerificationReport, spec task.VerificationSpec, frozen *workset.RuntimeWorkset) bool {
	if !VerificationReportMatchesSpec(report, spec) {
		return false
	}
	for i, check := range report.Checks {
		if !check.Required {
			continue
		}
		declared, fact := spec.Checks[i], check.Observed
		if !declared.RequiresObservation() {
			if fact != nil {
				return false // never promote an authored assessment to observed proof
			}
			continue
		}
		if fact == nil || (fact.Status != VerificationCheckPassed && fact.Status != VerificationCheckFailed) {
			return false
		}
		qualified := declared.RepositoryID != "" || declared.WorkingDirectory != "" || len(declared.RepositoryIDs) > 0
		if qualified && (frozen == nil || !fact.Source.Matches(*frozen, declared.RepositoryID, declared.WorkingDirectory)) {
			return false
		}
		if declared.Command != "" {
			if fact.ExitCode == nil || !VerificationCommandMatches(declared.Command, fact.Command) {
				return false
			}
			if qualified && (fact.Source.Kind != "COMMAND" || fact.Source.ItemID == "" || fact.Source.CommandStatus != "completed") {
				return false
			}
		} else if declared.Kind != task.VerificationCheckGitAssertion || (qualified && fact.Source.Kind != "GIT") {
			return false
		}
	}
	return true
}

// VerificationAssessmentPassed is the verdict gate for explicit user completion,
// after independent evidence/provenance validation. It does not authorize an
// automatic recovery resolution. Authored CUSTOM assessments remain reported
// claims; BLOCKED/NOT_RUN/UNKNOWN never make completion ready.
func VerificationAssessmentPassed(report VerificationReport, spec task.VerificationSpec) bool {
	if report.Availability != VerificationReportAvailable || !VerificationReportMatchesSpec(report, spec) {
		return false
	}
	for i, check := range report.Checks {
		if !check.Required {
			continue
		}
		if !spec.Checks[i].RequiresObservation() {
			if check.Observed != nil || check.Reported.Status != VerificationCheckPassed {
				return false
			}
		} else if check.Observed == nil || check.Observed.Status != VerificationCheckPassed || check.Observed.ExitCode != nil && *check.Observed.ExitCode != 0 {
			return false
		}
	}
	for _, criterion := range report.AcceptanceCriteria {
		if criterion.Reported.Status != VerificationCheckPassed {
			return false
		}
	}
	return true
}
