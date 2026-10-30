package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/inspectionresult"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"strings"
)

// AnalyzeRunReport consumes only an explicit GPT submission. Native chat text
// is never passed to this function or interpreted as an engineering verdict.
func AnalyzeRunReport(source runanalysis.Source, raw json.RawMessage) (runanalysis.Result, error) {
	switch source.Kind {
	case "INSPECTION":
		if source.Run.RuntimeWorkset == nil {
			return runanalysis.Result{}, errors.New("Inspection workset missing")
		}
		_, err := inspectionresult.Parse(raw, *source.Run.RuntimeWorkset)
		return runanalysis.Result{Inspection: raw}, err
	case "RECOVERY":
		if source.Recovery == nil {
			return runanalysis.Result{}, errors.New("Recovery source missing")
		}
		_, err := recoverystatus.ParseReport(raw, source.Recovery.Spec.Source)
		return runanalysis.Result{Recovery: raw}, err
	case "REVIEW":
		if source.Review == nil {
			return runanalysis.Result{}, errors.New("frozen Review source unavailable")
		}
		result, err := review.ParseModelResult(string(raw), source.Review.RepositoryTargets, source.Review.PrimaryMemberID)
		if err != nil {
			return runanalysis.Result{}, err
		}
		if err = review.ValidateProposalAdmission(result); err != nil {
			return runanalysis.Result{}, err
		}
		canonical, err := json.Marshal(result)
		return runanalysis.Result{Review: canonical}, err
	case "VERIFICATION":
		if len(source.ObservedChecks) != len(source.VerificationSpec.Checks) {
			return runanalysis.Result{}, errors.New("captured verification observations unavailable")
		}
		report, err := buildVerificationReportWithScope(source.VerificationSpec, string(raw), nil, evidence.GitEvidence{}, nil)
		if err != nil {
			return runanalysis.Result{}, err
		}
		report.Availability = run.VerificationReportAvailable
		for i := range report.Checks {
			report.Checks[i].Observed = source.ObservedChecks[i]
			if source.ObservedChecks[i] != nil {
				copyObservedCompatibility(&report.Checks[i], source.ObservedChecks[i])
			}
		}
		if !run.VerificationEvidenceComplete(report, source.VerificationSpec, source.Run.RuntimeWorkset) {
			report.Availability = run.VerificationReportInsufficient
		}
		return runanalysis.Result{Verification: &report}, nil
	case "EXECUTION":
		var report struct {
			Summary string `json:"summary"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&report); err != nil || strings.TrimSpace(report.Summary) == "" || len(report.Summary) > 16384 {
			return runanalysis.Result{}, errors.New("analysis summary required")
		}
		return runanalysis.Result{}, nil
	}
	return runanalysis.Result{}, runanalysis.ErrInvalid
}

// DescribeRunAnalysis exposes the GPT submission contract separately from Codex.
func DescribeRunAnalysis(source *runanalysis.Source) {
	switch source.Kind {
	case "INSPECTION":
		if source.Run.RuntimeWorkset != nil {
			source.ReportSchema = inspectionresult.Schema(inspectionresult.SourceForWorkset(*source.Run.RuntimeWorkset))
		}
	case "RECOVERY":
		if source.Recovery != nil {
			source.ReportSchema = source.Recovery.Schema
		}
	case "REVIEW":
		if source.Review != nil {
			source.ReportSchema = review.AnalysisResultSchema(source.Review.RepositoryTargets, source.Review.PrimaryMemberID)
		}
	case "VERIFICATION":
		source.ReportSchema = json.RawMessage(VerificationReportV2JSONSchema)
	case "EXECUTION":
		source.ReportSchema = json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string","minLength":1,"maxLength":16384}},"required":["summary"],"additionalProperties":false}`)
	}
}
