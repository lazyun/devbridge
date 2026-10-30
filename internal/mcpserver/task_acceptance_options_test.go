package mcpserver

import (
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func TestVerificationDiscoveryUsesThreeChoicesAndRequiresPassedAssessmentForMergedCompletion(t *testing.T) {
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckCustom, Instruction: "Assess actual behavior", Required: true}}, AcceptanceCriteria: []string{"requirements met"}}
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "Evidence assessed", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: "CUSTOM", Required: true, Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "met"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: "requirements met", Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "met"}}}}
	projection := workflowProjection{proposal: &executionproposal.Proposal{VerificationSpec: spec, VerificationRunID: "verify"}, runs: []run.Run{{ID: "verify", Status: run.StatusSucceeded, VerificationReport: &report}}}
	options := taskAcceptanceDecisionOptions(projection)
	if len(options) != 3 || options[0].Option != "CONTINUE_TASK" || options[1].Option != "COMPLETE_TASK" || options[2].Option != "DEFER_TASK" {
		t.Fatalf("wrong primary verification choices: %+v", options)
	}
	report.Checks[0].Reported.Status = run.VerificationCheckBlocked
	options = taskAcceptanceDecisionOptions(projection)
	if len(options) != 2 {
		t.Fatalf("blocked assessment offers completion: %+v", options)
	}
	for _, option := range options {
		if option.Option == "ACCEPT" || option.Option == "COMPLETE_TASK" {
			t.Fatal("legacy acceptance or failed verdict offers completion")
		}
	}
}
