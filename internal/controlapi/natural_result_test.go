package controlapi_test

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
)

// This is an explicit GPT submission in the fixture, not extraction of Codex's
// final message. It exercises the same public Facade used by both MCP adapters.
func submitNaturalFixtureAnalysis(t *testing.T, f *upperLifecycleFixture, id string, report json.RawMessage) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var claims int
		if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, id).Scan(&claims); err != nil {
			t.Fatalf("analysis error: %T %v cause=%v", err, err, errors.Unwrap(err))
		}
		if claims == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native worker did not release claim")
		}
		time.Sleep(10 * time.Millisecond)
	}
	source, err := f.facade.GetRunAnalysis(f.ctx, f.session, id)
	if err != nil {
		t.Fatalf("analysis error: %T %v cause=%v", err, err, errors.Unwrap(err))
	}
	if source.State == "RECORDED" {
		return
	}
	if source.State != "REQUIRED" || source.Run.ResultProtocol != run.NaturalResultProtocol || source.Run.VerificationReport != nil || !json.Valid(source.ReportSchema) {
		t.Fatalf("analysis source %+v", source)
	}
	original := source.Run.Result.AgentClaim.Message
	if json.Valid([]byte(original)) {
		t.Fatal("fixture must prove natural Codex output")
	}
	if report == nil {
		checks := []map[string]any{}
		criteria := []map[string]any{}
		for i, check := range source.VerificationSpec.Checks {
			status := "PASSED"
			if check.Kind == task.VerificationCheckCustom && os.Getenv("DEVBRIDGE_TEST_REQUIRED_CUSTOM") != "" {
				status = os.Getenv("DEVBRIDGE_TEST_REQUIRED_CUSTOM")
			}
			checks = append(checks, map[string]any{"index": i, "status": status, "summary": "GPT analyzed actual fixture evidence", "evidence": "see immutable observations and Codex output", "reason": ""})
		}
		for i := range source.VerificationSpec.AcceptanceCriteria {
			status, reason := "PASSED", ""
			if i == 0 && os.Getenv("DEVBRIDGE_TEST_VERIFICATION_FAILURE") == "criterion" {
				status, reason = "FAILED", "authored fixture does not meet the acceptance criterion"
			}
			criteria = append(criteria, map[string]any{"index": i, "status": status, "summary": "GPT analyzed fixture changes", "evidence": "exact fixture files", "reason": reason})
		}
		report, _ = json.Marshal(map[string]any{"version": 2, "summary": "GPT verification analysis", "checks": checks, "acceptanceCriteria": criteria})
	}
	in := runanalysis.Input{AISessionID: f.session, RunID: id, RequestID: "gpt-analysis-" + id, SourceHash: source.SourceHash, Report: report}
	receipt, err := f.facade.SubmitRunAnalysis(f.ctx, in)
	if err != nil {
		t.Fatalf("analysis error: %T %v cause=%v", err, err, errors.Unwrap(err))
	}
	replay, err := f.facade.SubmitRunAnalysis(f.ctx, in)
	if err != nil || replay != receipt {
		t.Fatalf("analysis replay %+v %v", replay, err)
	}
	current, err := f.facade.GetRunForSession(f.ctx, f.session, id)
	if err != nil || current.Result.AgentClaim.Message != original || current.Status != run.StatusSucceeded || current.AnalysisState != "RECORDED" {
		t.Fatalf("native result changed %+v %v", current, err)
	}
}

// An authored acceptance fixture must explicitly submit GPT analysis once the
// independent Codex verification has completed; terminal native output alone
// cannot advance the Task to acceptance.
func submitCompletedFixtureVerificationAnalysis(t *testing.T, f *upperLifecycleFixture, executionID string) {
	t.Helper()
	value, err := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
	if err != nil {
		t.Fatal(err)
	}
	if value.VerificationRunID == "" {
		return
	}
	current, err := f.facade.GetRunForSession(f.ctx, f.session, value.VerificationRunID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status == run.StatusSucceeded && current.AnalysisState == "REQUIRED" {
		submitNaturalFixtureAnalysis(t, f, current.ID, nil)
	}
}
