package execution

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
)

func TestDescribeReviewAnalysisUsesFrozenScalarContract(t *testing.T) {
	source := runanalysis.Source{Kind: "REVIEW", SourceHash: "unchanged-frozen-source", Review: &review.Review{RepositoryID: "legacy-repo"}}
	DescribeRunAnalysis(&source)
	if string(source.ReportSchema) != string(review.AnalysisResultSchema(nil, "")) || source.SourceHash != "unchanged-frozen-source" || len(source.Review.RepositoryTargets) != 0 {
		t.Fatal("analysis description invented target authority or changed source identity")
	}
}

func TestRequiredCustomAnalysisPreservesObligationsAndNativeEvidence(t *testing.T) {
	spec, _, scope, commands := qualifiedVerificationFixture(t)
	for _, instruction := range []string{"Exercise node CAS, migration and restart recovery", "Exercise real Codex read isolation and thread resume; report BLOCKED if unavailable"} {
		spec.Checks = append(spec.Checks, task.VerificationCheck{Kind: task.VerificationCheckCustom, Instruction: instruction, Required: true, RepositoryID: "app", RepositoryIDs: []string{"app", "sdk"}, WorkingDirectory: "."})
	}
	for _, status := range []run.VerificationCheckStatus{run.VerificationCheckPassed, run.VerificationCheckBlocked, run.VerificationCheckNotRun, run.VerificationCheckUnknown} {
		t.Run(string(status), func(t *testing.T) {
			source := runanalysis.Source{Kind: "VERIFICATION", Run: run.Run{ResultProtocol: run.NaturalResultProtocol, Status: run.StatusSucceeded, RuntimeWorkset: &scope.workset}, VerificationSpec: spec}
			reported := reportedVerificationV2{Version: 2, Summary: "Explicit analysis of actual Codex output", AcceptanceCriteria: []reportedVerificationItem{{Index: 0, Status: status, Summary: "separate user assessment"}}}
			for i, check := range spec.Checks {
				source.ObservedChecks = append(source.ObservedChecks, observeQualifiedCheck(check, commands, scope))
				reported.Checks = append(reported.Checks, reportedVerificationItem{Index: i, Status: status, Summary: "analysis outcome", Evidence: "Codex output and limitations", Reason: "environment may prevent real-model check"})
			}
			raw, _ := json.Marshal(reported)
			result, err := AnalyzeRunReport(source, raw)
			if err != nil || result.Verification.Availability != run.VerificationReportAvailable || !run.VerificationEvidenceComplete(*result.Verification, spec, &scope.workset) {
				t.Fatalf("complete indexed analysis unavailable: %+v %v", result, err)
			}
			for _, check := range result.Verification.Checks[2:] {
				if !check.Required || check.Observed != nil || check.Reported.Status != status || check.ExitCode != nil {
					t.Fatalf("custom obligation changed or acquired fabricated proof: %+v", check)
				}
			}
			if result.Verification.Checks[0].Observed.Status != run.VerificationCheckPassed || source.Run.Status != run.StatusSucceeded {
				t.Fatal("analysis replaced native completion or command facts")
			}
			source.ObservedChecks[0] = nil
			result, err = AnalyzeRunReport(source, raw)
			if err != nil || result.Verification.Availability != run.VerificationReportInsufficient {
				t.Fatal("custom analysis replaced missing required command evidence")
			}
			reported.Checks = reported.Checks[:3]
			raw, _ = json.Marshal(reported)
			if _, err = AnalyzeRunReport(source, raw); err == nil {
				t.Fatal("missing required custom assessment accepted")
			}
		})
	}
}

func TestGPTAnalysisCannotRewriteCapturedVerificationEvidence(t *testing.T) {
	_, spec := planSpecs()
	source := runanalysis.Source{Kind: "VERIFICATION", Run: run.Run{ResultProtocol: run.NaturalResultProtocol, Status: run.StatusSucceeded, Result: &run.Result{AgentClaim: evidence.AgentClaim{Message: "检查完成；命令记录中有失败，请分析。"}}}, VerificationSpec: spec, ObservedChecks: []*run.VerificationObservedFact{{Status: run.VerificationCheckFailed, Command: "go test ./...", ExitCode: exitCode(1), Summary: "native command exited 1"}}}
	result, err := AnalyzeRunReport(source, []byte(reportJSON("PASSED", "PASSED")))
	if err != nil {
		t.Fatal(err)
	}
	check := result.Verification.Checks[0]
	if check.Reported.Status != run.VerificationCheckPassed || check.Observed.Status != run.VerificationCheckFailed || check.Status != run.VerificationCheckFailed || *check.ExitCode != 1 {
		t.Fatalf("GPT replaced native evidence: %+v", check)
	}
	if source.Run.Status != run.StatusSucceeded || json.Valid([]byte(source.Run.Result.AgentClaim.Message)) {
		t.Fatal("native status or output changed")
	}
	if _, err = AnalyzeRunReport(source, []byte(`{"version":2,"summary":"missing indices","checks":[],"acceptanceCriteria":[]}`)); err == nil {
		t.Fatal("incomplete explicit GPT report accepted")
	}
	source.ObservedChecks = nil
	if _, err = AnalyzeRunReport(source, []byte(reportJSON("PASSED", "PASSED"))); err == nil {
		t.Fatal("missing captured evidence became success")
	}
}

func TestGPTExecutionSummaryIsStrictAndOptional(t *testing.T) {
	source := runanalysis.Source{Kind: "EXECUTION"}
	for _, raw := range []string{`{"summary":""}`, `{"summary":"ok","approved":true}`, `"ok"`} {
		if _, err := AnalyzeRunReport(source, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := AnalyzeRunReport(source, []byte(`{"summary":"Completed; user acceptance remains pending"}`)); err != nil {
		t.Fatal(err)
	}
}
