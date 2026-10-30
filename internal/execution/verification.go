package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/structuredschema"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

const maxPersistedCommandEvidenceBytes = 32 << 10

// VerificationReportV2JSONSchema describes evidence, never Task acceptance.
var VerificationReportV2JSONSchema = string(structuredschema.Marshal(verificationReportV2Schema()))

func verificationReportV2Schema() structuredschema.Schema {
	boundedString := func(min, max int) structuredschema.Schema {
		result := structuredschema.String()
		if min > 0 {
			result["minLength"] = min
		}
		result["maxLength"] = max
		return result
	}
	observation := structuredschema.Object(map[string]any{
		"index":    structuredschema.Schema{"type": "integer", "minimum": 0},
		"status":   structuredschema.Enum("PASSED", "FAILED", "BLOCKED", "NOT_RUN", "UNKNOWN"),
		"summary":  boundedString(1, 2048),
		"evidence": boundedString(0, 32768),
		"reason":   boundedString(0, 2048),
	})
	checks := structuredschema.Array(observation)
	checks["maxItems"] = 32
	criteria := structuredschema.Array(observation)
	criteria["maxItems"] = 32
	return structuredschema.Object(map[string]any{
		"version":            structuredschema.Schema{"type": "integer", "const": 2},
		"summary":            boundedString(1, 4096),
		"checks":             checks,
		"acceptanceCriteria": criteria,
	})
}

// ObservedCommand is authoritative App Server command evidence.
type ObservedCommand = run.ObservedCommand

type reportedVerificationItem struct {
	Index    int                         `json:"index"`
	Status   run.VerificationCheckStatus `json:"status"`
	Summary  string                      `json:"summary"`
	Evidence string                      `json:"evidence"`
	Reason   string                      `json:"reason"`
}

type reportedVerificationV2 struct {
	Version            int                        `json:"version"`
	Summary            string                     `json:"summary"`
	Checks             []reportedVerificationItem `json:"checks"`
	AcceptanceCriteria []reportedVerificationItem `json:"acceptanceCriteria"`
}

func buildVerificationReport(spec task.VerificationSpec, message string, commands []ObservedCommand, gitEvidence evidence.GitEvidence) (run.VerificationReport, error) {
	return buildVerificationReportWithScope(spec, message, commands, gitEvidence, nil)
}

func buildVerificationReportWithScope(spec task.VerificationSpec, message string, commands []ObservedCommand, gitEvidence evidence.GitEvidence, scope *verificationScope) (run.VerificationReport, error) {
	var reported reportedVerificationV2
	decoder := json.NewDecoder(bytes.NewBufferString(message))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reported); err != nil {
		return run.VerificationReport{}, fmt.Errorf("decode VerificationReport v2: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing JSON")
		}
		return run.VerificationReport{}, fmt.Errorf("invalid VerificationReport v2: %w", err)
	}
	if reported.Version != 2 || strings.TrimSpace(reported.Summary) == "" || len(reported.Summary) > 4096 || len(reported.Checks) != len(spec.Checks) || len(reported.AcceptanceCriteria) != len(spec.AcceptanceCriteria) {
		return run.VerificationReport{}, errors.New("VerificationReport v2 does not exactly cover the confirmed VerificationSpec")
	}
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: reported.Summary, Checks: make([]run.VerificationCheckOutcome, 0, len(spec.Checks)), AcceptanceCriteria: make([]run.VerificationCriterionOutcome, 0, len(spec.AcceptanceCriteria))}
	for index, declared := range spec.Checks {
		item := reported.Checks[index]
		if item.Index != index || !validVerificationFact(item) {
			return run.VerificationReport{}, fmt.Errorf("invalid reported verification check %d", index)
		}
		outcome := run.VerificationCheckOutcome{ID: fmt.Sprintf("check-%d", index+1), Index: index, Kind: string(declared.Kind), Required: declared.Required, DeclaredCommand: declared.Command, Reported: reportedFact(item)}
		outcome.Observed = observeCheck(declared, commands, gitEvidence)
		if declared.RepositoryID != "" || declared.WorkingDirectory != "" || len(declared.RepositoryIDs) > 0 {
			outcome.Observed = observeQualifiedCheck(declared, commands, scope)
		}
		if outcome.Observed != nil {
			copyObservedCompatibility(&outcome, outcome.Observed)
		} else {
			outcome.Status, outcome.Summary, outcome.Evidence, outcome.FailureReason = item.Status, item.Summary, item.Evidence, item.Reason
		}
		report.Checks = append(report.Checks, outcome)
	}
	for index, criterion := range spec.AcceptanceCriteria {
		item := reported.AcceptanceCriteria[index]
		if item.Index != index || !validVerificationFact(item) {
			return run.VerificationReport{}, fmt.Errorf("invalid reported acceptance criterion %d", index)
		}
		report.AcceptanceCriteria = append(report.AcceptanceCriteria, run.VerificationCriterionOutcome{Index: index, Criterion: criterion, Reported: reportedFact(item)})
	}
	// Preserve the historical scalar report envelope. Qualified reports must
	// carry complete observations from this exact frozen Run workset before
	// they can be offered for user acceptance.
	for _, check := range spec.Checks {
		if check.RepositoryID != "" || check.WorkingDirectory != "" || len(check.RepositoryIDs) > 0 {
			var frozen *workset.RuntimeWorkset
			if scope != nil {
				frozen = &scope.workset
			}
			if !run.VerificationEvidenceComplete(report, spec, frozen) {
				report.Availability = run.VerificationReportInsufficient
			}
			break
		}
	}
	return report, nil
}

func validVerificationFact(item reportedVerificationItem) bool {
	if strings.TrimSpace(item.Summary) == "" || len(item.Summary) > 2048 || len(item.Evidence) > maxPersistedCommandEvidenceBytes || len(item.Reason) > 2048 {
		return false
	}
	switch item.Status {
	case run.VerificationCheckPassed, run.VerificationCheckFailed, run.VerificationCheckBlocked, run.VerificationCheckNotRun, run.VerificationCheckUnknown:
		return true
	}
	return false
}

func reportedFact(item reportedVerificationItem) run.VerificationReportedFact {
	return run.VerificationReportedFact{Status: item.Status, Summary: item.Summary, Evidence: item.Evidence, Reason: item.Reason}
}

func observeCheck(declared task.VerificationCheck, commands []ObservedCommand, gitEvidence evidence.GitEvidence) *run.VerificationObservedFact {
	switch declared.Kind {
	case task.VerificationCheckTestCommand, task.VerificationCheckStaticAnalysis, task.VerificationCheckDiff, task.VerificationCheckCustom:
		if !declared.RequiresObservation() {
			return nil
		}
		return observeCommand(declared.Command, commands)
	case task.VerificationCheckGitAssertion:
		if strings.TrimSpace(declared.Command) != "" {
			return observeCommand(declared.Command, commands)
		}
		return observeGitAssertion(declared.Instruction, gitEvidence)
	}
	return nil
}

func observeCommand(declared string, commands []ObservedCommand) *run.VerificationObservedFact {
	for _, observed := range commands {
		if !commandMatches(strings.TrimSpace(declared), observed) {
			continue
		}
		fact := &run.VerificationObservedFact{Command: observed.Command, ExitCode: cloneExitCode(observed.ExitCode), DurationMillis: cloneDuration(observed.DurationMillis), StartedAt: cloneObservedTime(observed.StartedAt), FinishedAt: cloneObservedTime(observed.FinishedAt)}
		fact.Evidence, fact.EvidenceTruncated = boundedEvidence(observed.Output, observed.OutputTruncated)
		if observed.ExitCode == nil {
			fact.Status, fact.Reason, fact.Summary = run.VerificationCheckUnknown, "MISSING_EXIT_CODE", "matching command item had no authoritative exit code"
		} else if *observed.ExitCode == 0 {
			fact.Status, fact.Summary = run.VerificationCheckPassed, "observed command exited with code 0"
		} else {
			fact.Status, fact.Reason, fact.Summary = run.VerificationCheckFailed, "NONZERO_EXIT", fmt.Sprintf("observed command exited with code %d", *observed.ExitCode)
		}
		return fact
	}
	return &run.VerificationObservedFact{Status: run.VerificationCheckNotRun, Reason: "MISSING_EVIDENCE", Summary: "no matching observable command execution was recorded"}
}

func commandMatches(declared string, observed ObservedCommand) bool {
	if run.VerificationCommandMatches(declared, observed.Command) {
		return true
	}
	for _, action := range observed.Actions {
		if strings.TrimSpace(action) == declared {
			return true
		}
	}
	return false
}

func observeGitAssertion(instruction string, value evidence.GitEvidence) *run.VerificationObservedFact {
	assertion := strings.ToLower(strings.TrimSpace(instruction))
	var passed bool
	switch assertion {
	case "worktree is unchanged", "no changes during verification":
		if value.StartSnapshot == "" || value.EndSnapshot == "" {
			return unavailableGitFact("snapshot identity is unavailable")
		}
		passed = value.StartSnapshot == value.EndSnapshot
	case "worktree is clean":
		if !hasGitObservation(value) {
			return unavailableGitFact("worktree state is unavailable")
		}
		passed = !value.Dirty
	case "worktree is dirty":
		if !hasGitObservation(value) {
			return unavailableGitFact("worktree state is unavailable")
		}
		passed = value.Dirty
	case "head equals base commit":
		if value.HeadCommit == "" || value.BaseCommit == "" {
			return unavailableGitFact("base or HEAD commit is unavailable")
		}
		passed = value.HeadCommit == value.BaseCommit
	default:
		return &run.VerificationObservedFact{Status: run.VerificationCheckUnknown, Reason: "UNSUPPORTED_GIT_ASSERTION", Summary: "assertion is not supported by the typed Git evidence evaluator"}
	}
	if passed {
		return &run.VerificationObservedFact{Status: run.VerificationCheckPassed, Summary: "typed Git assertion passed from authoritative Git evidence"}
	}
	return &run.VerificationObservedFact{Status: run.VerificationCheckFailed, Reason: "GIT_ASSERTION_FAILED", Summary: "typed Git assertion failed from authoritative Git evidence"}
}

func unavailableGitFact(detail string) *run.VerificationObservedFact {
	return &run.VerificationObservedFact{Status: run.VerificationCheckBlocked, Reason: "MISSING_EVIDENCE", Summary: "typed Git assertion unavailable: " + detail}
}

func copyObservedCompatibility(outcome *run.VerificationCheckOutcome, observed *run.VerificationObservedFact) {
	outcome.Status, outcome.ObservedCommand, outcome.ExitCode = observed.Status, observed.Command, cloneExitCode(observed.ExitCode)
	outcome.Evidence, outcome.EvidenceTruncated, outcome.DurationMillis = observed.Evidence, observed.EvidenceTruncated, cloneDuration(observed.DurationMillis)
	outcome.StartedAt, outcome.FinishedAt = cloneObservedTime(observed.StartedAt), cloneObservedTime(observed.FinishedAt)
	outcome.FailureReason, outcome.Summary = observed.Reason, observed.Summary
}

func hasGitObservation(value evidence.GitEvidence) bool {
	return value.StartSnapshot != "" || value.EndSnapshot != "" || value.BaseCommit != "" || value.HeadCommit != ""
}
func boundedEvidence(value string, truncated bool) (string, bool) {
	if len(value) <= maxPersistedCommandEvidenceBytes {
		return value, truncated
	}
	return value[:maxPersistedCommandEvidenceBytes], true
}
func cloneExitCode(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneDuration(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneObservedTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
